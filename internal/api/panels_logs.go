package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jhyeok1023/skills-dashboard/internal/awsx"
	"github.com/jhyeok1023/skills-dashboard/internal/domain"
)

// insightsTimeLayouts are the shapes a Logs Insights bin() or @timestamp comes
// back as. They are UTC without a zone marker.
var insightsTimeLayouts = []string{
	"2006-01-02 15:04:05.000",
	"2006-01-02 15:04:05",
	time.RFC3339Nano,
	time.RFC3339,
}

func parseInsightsTime(s string) (time.Time, bool) {
	for _, layout := range insightsTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func rowFloat(row map[string]string, key string) (float64, bool) {
	v, ok := row[key]
	if !ok || v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// logSource is where one panel's log queries go: which runner, in which
// region, against which group.
//
// The three travel together because the cache key needs all of them. Keyed on
// the group name alone, a pod query and a WAF query against same-named groups
// in two different regions would serve each other's rows.
type logSource struct {
	runner *awsx.InsightsRunner
	region string
	group  string
	// missing is what to tell the operator when group is empty. Pod logs and
	// WAF logs are configured in different places, and naming the wrong one is
	// how an operator ends up looking at a setting that was already correct.
	missing string
}

func (s *Service) podLogs(rc requestCtx) logSource {
	return logSource{
		runner:  s.Insights,
		region:  s.region(),
		group:   rc.cfg.PodLogGroupOrDefault(),
		missing: "팟 로그 그룹이 설정되지 않았습니다. 설정에서 클러스터 또는 로그 그룹을 지정하세요.",
	}
}

// wafLogs points the WAF panels at the WAF region. It falls back to the
// primary runner when no global one was wired up, which is the shape a test
// that assembles a Service by hand tends to leave behind.
func (s *Service) wafLogs(rc requestCtx) logSource {
	runner := s.InsightsGlobal
	if runner == nil {
		runner = s.Insights
	}
	region := s.wafRegion()
	return logSource{
		runner: runner,
		region: region,
		group:  rc.cfg.WAFLogGroup,
		missing: fmt.Sprintf(
			"WAF 로그 그룹이 설정되지 않았습니다. CLOUDFRONT 스코프 WAF는 %s에만 로그를 남기므로, 설정에서 해당 리전의 aws-waf-logs-* 그룹을 지정하세요.",
			region),
	}
}

// runLogQueries executes a set of queries behind the cache.
func (s *Service) runLogQueries(rc requestCtx, src logSource, name string, qs []domain.Query) (map[string]awsx.QueryResult, map[string]error) {
	if src.group == "" {
		errs := map[string]error{}
		for _, q := range qs {
			errs[q.ID] = awsx.ErrNoLogGroup
		}
		return nil, errs
	}

	var b strings.Builder
	fmt.Fprintf(&b, "logs|%s|%s|%s|%d|%d|%d", name, src.region, src.group,
		rc.w.Start.Unix(), rc.w.End.Unix(), rc.w.Period.Seconds())
	ids := make([]string, 0, len(qs))
	for _, q := range qs {
		sum := sha256.Sum256([]byte(q.Text))
		ids = append(ids, q.ID+":"+strconv.Itoa(q.Limit)+":"+fmt.Sprintf("%x", sum))
	}
	sort.Strings(ids)
	b.WriteString("|" + strings.Join(ids, ","))

	type bundle struct {
		results map[string]awsx.QueryResult
		errs    map[string]error
	}
	got, err := awsx.Cached(rc.ctx, s.Cache, b.String(), func(ctx context.Context) (bundle, error) {
		res, errs := src.runner.RunAll(ctx, src.group, rc.w, qs)
		return bundle{res, errs}, nil
	})
	if err != nil {
		errs := map[string]error{}
		for _, q := range qs {
			errs[q.ID] = err
		}
		return nil, errs
	}
	return got.results, got.errs
}

// excludedPaths renders the probe-exclusion clause appended to every pod-log
// stat's basis.
//
// A number that quietly drops a slice of the traffic is worse than one that is
// merely wrong: nothing on screen hints that it happened. Naming the excluded
// paths beside the value is what keeps "요청 수" honest once health checks stop
// being counted.
func excludedPaths(f domain.LogFormat) string {
	if len(f.ExcludePaths) == 0 {
		return ""
	}
	return ", " + strings.Join(f.ExcludePaths, " · ") + " 제외"
}

// noteQueryCost records what a set of Insights queries scanned. Insights bills
// per byte, so the cost of a refresh is shown rather than left to a bill.
func noteQueryCost(panel *domain.Panel, results map[string]awsx.QueryResult) {
	bytes := awsx.TotalBytesScanned(results)
	if bytes <= 0 {
		return
	}
	panel.Stats = append(panel.Stats, domain.Stat{
		Key:   "insights.bytesScanned",
		Label: "스캔량",
		Unit:  domain.UnitBytes,
		Value: domain.P(bytes),
		Basis: fmt.Sprintf("Logs Insights 쿼리 %d건", len(results)),
	})
}

// noteQueryErrors turns per-query failures into panel warnings so a page keeps
// rendering when one of its queries fails.
func noteQueryErrors(panel *domain.Panel, src logSource, errs map[string]error) {
	ids := make([]string, 0, len(errs))
	for id := range errs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		err := errs[id]
		if errors.Is(err, awsx.ErrNoLogGroup) {
			panel.Warn("%s", src.missing)
			return
		}
		// The region is named because the most common failure here is a group
		// that exists, just not where the query went looking for it.
		panel.Warn("%s 쿼리 실패 (%s): %v", id, src.region, err)
	}
}

func (s *Service) buildPodLatencyPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "pod-latency", Title: "팟 응답 시간"}
	q := domain.LogQueries{Format: rc.cfg.LogFormat}

	traffic, err := q.PodTraffic(rc.w)
	if err != nil {
		return nil, err
	}
	src := s.podLogs(rc)
	results, errs := s.runLogQueries(rc, src, "pod-latency", []domain.Query{traffic})
	noteQueryErrors(panel, src, errs)
	res, ok := results[traffic.ID]
	if !ok {
		return panel, nil
	}

	// Series are built per app, one set of percentile lines each.
	type appSeries struct {
		p50, p90, p99, requests *domain.Series
	}
	apps := map[string]*appSeries{}
	order := []string{}
	var totalRequests, totalLatencySamples float64

	for _, row := range res.Rows {
		ts, ok := parseInsightsTime(row["t"])
		if !ok {
			continue
		}
		idx, inWindow := rc.w.Index(ts)
		if !inWindow {
			continue
		}
		app := row["app"]
		if app == "" {
			app = "(unknown)"
		}
		a, seen := apps[app]
		if !seen {
			n := rc.w.Buckets()
			a = &appSeries{
				p50:      domain.NewSeries(app+" · p50", domain.UnitMillis, domain.ColorTeal, n),
				p90:      domain.NewSeries(app+" · p90", domain.UnitMillis, domain.ColorBlue, n),
				p99:      domain.NewSeries(app+" · p99", domain.UnitMillis, domain.ColorIndigo, n),
				requests: domain.NewSeries(app+" · 요청 수", domain.UnitCount, domain.ColorGray, n),
			}
			apps[app] = a
			order = append(order, app)
		}
		if v, ok := rowFloat(row, "p50"); ok {
			a.p50.Set(idx, v)
		}
		if v, ok := rowFloat(row, "p90"); ok {
			a.p90.Set(idx, v)
		}
		if v, ok := rowFloat(row, "p99"); ok {
			a.p99.Set(idx, v)
		}
		if v, ok := rowFloat(row, "requests"); ok {
			a.requests.Add(idx, v)
			totalRequests += v
		}
		if v, ok := rowFloat(row, "latencySamples"); ok {
			totalLatencySamples += v
		}
	}

	sort.Strings(order)
	var p99s []*domain.Series
	for _, app := range order {
		a := apps[app]
		panel.Series = append(panel.Series, a.p50, a.p90, a.p99, a.requests)
		p99s = append(p99s, a.p99)
	}

	// Two counts, two names, two stated populations.
	//
	// The reference implementation derived a request total from rows carrying
	// a status and a "요청 수" column from rows carrying a latency, labelled
	// both the same way, and displayed them side by side. Here they come from
	// one query, keep distinct names, and each says what it counted.
	excluded := excludedPaths(rc.cfg.LogFormat)
	panel.Stats = append(panel.Stats,
		domain.Stat{
			Key: "pod.p99.max", Label: "최대 p99", Unit: domain.UnitMillis,
			Value: reduceAcross(p99s, (*domain.Series).Max, maxOf),
			Basis: rc.cfg.LogFormat.LatencyField + " 가 있는 요청, 구간 전체" + excluded,
		},
		domain.Stat{
			Key: "pod.requests.total", Label: "요청 수", Unit: domain.UnitCount,
			Value: domain.P(totalRequests),
			Basis: rc.cfg.LogFormat.StatusField + " 가 있는 로그 라인" + excluded,
		},
		domain.Stat{
			Key: "pod.latencySamples.total", Label: "응답 시간 표본 수", Unit: domain.UnitCount,
			Value: domain.P(totalLatencySamples),
			Basis: rc.cfg.LogFormat.LatencyField + " 가 있는 로그 라인" + excluded,
		},
	)
	noteQueryCost(panel, results)
	return panel, nil
}

func (s *Service) buildPodStatusCodePanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "pod-status-codes", Title: "비정상 응답 코드"}
	q := domain.LogQueries{Format: rc.cfg.LogFormat}
	limit := rc.cfg.Limits.LogRows

	series, err := q.PodBadStatusSeries(rc.w)
	if err != nil {
		return nil, err
	}
	list, err := q.PodBadStatusList(rc.w, limit)
	if err != nil {
		return nil, err
	}

	src := s.podLogs(rc)
	results, errs := s.runLogQueries(rc, src, "pod-status-codes", []domain.Query{series, list})
	noteQueryErrors(panel, src, errs)

	// The aggregate is uncapped, so summing it gives the real number of
	// non-OK responses. The list beside it is capped. Counting the list would
	// make the headline stop at the cap and disagree with the chart — which is
	// exactly what the reference implementation did.
	var total float64
	byStatus := map[string]*domain.Series{}
	if res, ok := results[series.ID]; ok {
		for _, row := range res.Rows {
			ts, ok := parseInsightsTime(row["t"])
			if !ok {
				continue
			}
			idx, inWindow := rc.w.Index(ts)
			if !inWindow {
				continue
			}
			n, ok := rowFloat(row, "n")
			if !ok {
				continue
			}
			code := row["status"]
			if code == "" {
				code = "(none)"
			}
			s, seen := byStatus[code]
			if !seen {
				s = domain.NewSeries(code, domain.UnitCount, statusColor(code), rc.w.Buckets())
				byStatus[code] = s
			}
			s.Add(idx, n)
			total += n
		}
	}

	codes := make([]string, 0, len(byStatus))
	for c := range byStatus {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		panel.Series = append(panel.Series, byStatus[c])
	}

	var rows []domain.Row
	if res, ok := results[list.ID]; ok {
		for _, r := range res.Rows {
			rows = append(rows, domain.Row{
				"timestamp": r["@timestamp"],
				"app":       r["app"],
				"pod":       r["pod"],
				"method":    r["method"],
				"path":      r["path"],
				"target":    r["requestTarget"],
				"status":    r["status"],
				"latencyMs": r["latencyMs"],
				"clientIp":  r["clientIp"],
			})
		}
	}

	panel.Table = domain.NewTable([]domain.Column{
		{Key: "timestamp", Label: "시각", Mono: true},
		{Key: "status", Label: "코드", Numeric: true, Copyable: true},
		{Key: "method", Label: "메소드"},
		{Key: "target", Label: "요청 대상", Mono: true, Copyable: true},
		{Key: "latencyMs", Label: "응답 시간", Unit: domain.UnitMillis, Numeric: true},
		{Key: "app", Label: "앱", Copyable: true},
		{Key: "pod", Label: "팟", Mono: true, Copyable: true},
		{Key: "clientIp", Label: "클라이언트 IP", Mono: true, Copyable: true},
	}, rows, honestTotal(total, len(rows)), limit)

	okList := make([]string, 0, len(rc.cfg.LogFormat.OKStatuses))
	for _, c := range rc.cfg.LogFormat.OKStatuses {
		okList = append(okList, strconv.Itoa(c))
	}
	panel.Stats = append(panel.Stats, domain.Stat{
		Key: "pod.badStatus.total", Label: "비정상 응답", Unit: domain.UnitCount,
		Value: domain.P(total),
		Basis: "상태 코드가 " + strings.Join(okList, ", ") + " 가 아닌 요청 (집계 전체)" +
			excludedPaths(rc.cfg.LogFormat),
		Intent: domain.IntentBad,
	})
	noteQueryCost(panel, results)
	return panel, nil
}

func statusColor(code string) string {
	switch {
	case strings.HasPrefix(code, "5"):
		return domain.ColorRed
	case strings.HasPrefix(code, "4"):
		return domain.ColorOrange
	case strings.HasPrefix(code, "3"):
		return domain.ColorYellow
	default:
		return domain.ColorGray
	}
}

// honestTotal keeps a table's total at or above the rows it carries. A total
// below the row count would be nonsense; the payload validator rejects it, and
// this is the one place a rounding difference could produce it.
func honestTotal(total float64, rows int) int64 {
	t := int64(total)
	if t < int64(rows) {
		return int64(rows)
	}
	return t
}

func (s *Service) buildPodErrorPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "pod-errors", Title: "ERROR · WARN 로그"}
	q := domain.LogQueries{Format: rc.cfg.LogFormat}
	limit := rc.cfg.Limits.LogRows

	series, err := q.PodErrorSeries(rc.w)
	if err != nil {
		return nil, err
	}
	list, err := q.PodErrorList(rc.w, limit)
	if err != nil {
		return nil, err
	}

	src := s.podLogs(rc)
	results, errs := s.runLogQueries(rc, src, "pod-errors", []domain.Query{series, list})
	noteQueryErrors(panel, src, errs)

	n := rc.w.Buckets()
	errSeries := domain.NewSeries("error", domain.UnitCount, domain.ColorRed, n)
	warnSeries := domain.NewSeries("warn", domain.UnitCount, domain.ColorOrange, n)
	var errTotal, warnTotal float64

	if res, ok := results[series.ID]; ok {
		for _, row := range res.Rows {
			ts, ok := parseInsightsTime(row["t"])
			if !ok {
				continue
			}
			idx, inWindow := rc.w.Index(ts)
			if !inWindow {
				continue
			}
			v, ok := rowFloat(row, "n")
			if !ok {
				continue
			}
			if strings.EqualFold(row["level"], "warn") {
				warnSeries.Add(idx, v)
				warnTotal += v
			} else {
				errSeries.Add(idx, v)
				errTotal += v
			}
		}
	}
	panel.Series = append(panel.Series, errSeries, warnSeries)

	var rows []domain.Row
	if res, ok := results[list.ID]; ok {
		for _, r := range res.Rows {
			msg := r["dashboardMessage"]
			if msg == "" {
				msg = r["@message"]
			}
			rows = append(rows, domain.Row{
				"timestamp": r["@timestamp"],
				"pod":       r["pod"],
				"container": r["container"],
				"message":   msg,
			})
		}
	}

	// The total counts every matching line; the list stops at the cap. The
	// reference implementation displayed the capped array's length as the
	// total, so the headline silently froze at 300.
	panel.Table = domain.NewTable([]domain.Column{
		{Key: "timestamp", Label: "시각", Mono: true},
		{Key: "container", Label: "컨테이너", Copyable: true},
		{Key: "pod", Label: "팟", Mono: true, Copyable: true},
		{Key: "message", Label: "메시지", Mono: true, Copyable: true},
	}, rows, honestTotal(errTotal+warnTotal, len(rows)), limit)

	excluded := excludedPaths(rc.cfg.LogFormat)
	panel.Stats = append(panel.Stats,
		domain.Stat{Key: "pod.error.total", Label: "ERROR", Unit: domain.UnitCount,
			Value: domain.P(errTotal),
			Basis: "level 또는 메시지 패턴이 error 계열 (집계 전체)" + excluded, Intent: domain.IntentBad},
		domain.Stat{Key: "pod.warn.total", Label: "WARN", Unit: domain.UnitCount,
			Value: domain.P(warnTotal),
			Basis: "level 또는 메시지 패턴이 warn 계열 (집계 전체)" + excluded, Intent: domain.IntentWarn},
	)
	noteQueryCost(panel, results)
	return panel, nil
}

func (s *Service) buildWAFTrafficPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "waf-traffic", Title: "WAF 트래픽"}
	q := domain.WAFQueries{Headers: rc.cfg.WAFHeaders}

	series := q.ActionSeries(rc.w)
	src := s.wafLogs(rc)
	results, errs := s.runLogQueries(rc, src, "waf-traffic", []domain.Query{series})
	noteQueryErrors(panel, src, errs)

	byAction := map[string]*domain.Series{}
	totals := map[string]float64{}
	if res, ok := results[series.ID]; ok {
		for _, row := range res.Rows {
			ts, ok := parseInsightsTime(row["t"])
			if !ok {
				continue
			}
			idx, inWindow := rc.w.Index(ts)
			if !inWindow {
				continue
			}
			v, ok := rowFloat(row, "n")
			if !ok {
				continue
			}
			action := row["action"]
			if action == "" {
				action = "(none)"
			}
			s, seen := byAction[action]
			if !seen {
				s = domain.NewSeries(action, domain.UnitCount, wafActionColor(action), rc.w.Buckets())
				byAction[action] = s
			}
			s.Add(idx, v)
			totals[action] += v
		}
	}

	actions := make([]string, 0, len(byAction))
	for a := range byAction {
		actions = append(actions, a)
	}
	sort.Strings(actions)
	for _, a := range actions {
		panel.Series = append(panel.Series, byAction[a])
		panel.Stats = append(panel.Stats, domain.Stat{
			Key: "waf.log." + strings.ToLower(a), Label: a, Unit: domain.UnitCount,
			Value: domain.P(totals[a]), Basis: "WAF 로그 action 집계 (전체)",
			Intent: wafActionIntent(a),
		})
	}
	noteQueryCost(panel, results)
	return panel, nil
}

func wafActionColor(action string) string {
	switch strings.ToUpper(action) {
	case "ALLOW":
		return domain.ColorGreen
	case "BLOCK":
		return domain.ColorPink
	case "COUNT":
		return domain.ColorGray
	default:
		return domain.ColorYellow
	}
}

func wafActionIntent(action string) domain.Intent {
	switch strings.ToUpper(action) {
	case "ALLOW":
		return domain.IntentGood
	case "BLOCK":
		return domain.IntentBad
	default:
		return domain.IntentNeutral
	}
}

func (s *Service) buildWAFBlockedPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "waf-blocked", Title: "차단된 요청"}
	q := domain.WAFQueries{Headers: rc.cfg.WAFHeaders}
	limit := rc.cfg.Limits.LogRows

	agg := q.Blocked(rc.cfg.Limits.TopN)
	list := q.BlockedList(limit)
	src := s.wafLogs(rc)
	results, errs := s.runLogQueries(rc, src, "waf-blocked", []domain.Query{agg, list})
	noteQueryErrors(panel, src, errs)

	var rows []domain.Row
	var listTotal float64
	if res, ok := results[agg.ID]; ok {
		for _, r := range res.Rows {
			n, _ := rowFloat(r, "n")
			listTotal += n
			rows = append(rows, domain.Row{
				"rule":     r["rule"],
				"clientIp": r["clientIp"],
				"country":  r["country"],
				"count":    r["n"],
			})
		}
	}
	panel.Table = domain.NewTable([]domain.Column{
		{Key: "rule", Label: "규칙", Mono: true, Copyable: true},
		{Key: "clientIp", Label: "클라이언트 IP", Mono: true, Copyable: true},
		{Key: "country", Label: "국가"},
		{Key: "count", Label: "건수", Numeric: true},
	}, rows, honestTotal(listTotal, len(rows)), rc.cfg.Limits.TopN)
	panel.Bars = &domain.Bars{KeyColumn: "rule", ValueColumn: "count"}

	if res, ok := results[list.ID]; ok && len(res.Rows) > 0 {
		var recent []domain.Row
		for _, r := range res.Rows {
			recent = append(recent, domain.Row{
				"timestamp": r["@timestamp"],
				"rule":      r["rule"],
				"clientIp":  r["clientIp"],
				"country":   r["country"],
				"method":    r["method"],
				"uri":       r["uri"],
				"args":      r["args"],
			})
		}
		panel.Warn("최근 차단 요청 %d건을 함께 조회했습니다.", len(recent))
	}
	noteQueryCost(panel, results)
	return panel, nil
}

func (s *Service) buildWAFBreakdownPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "waf-breakdown", Title: "WAF 통계"}
	q := domain.WAFQueries{Headers: rc.cfg.WAFHeaders}
	topN := rc.cfg.Limits.TopN

	queries := []domain.Query{q.ByMethod(), q.ByPath(topN)}
	headerQueries := map[string]string{}
	for _, h := range rc.cfg.WAFHeaders {
		hq, err := q.ByHeader(h, topN)
		if err != nil {
			panel.Warn("헤더 %q 통계를 만들 수 없습니다: %v", h, err)
			continue
		}
		queries = append(queries, hq)
		headerQueries[hq.ID] = h
	}

	src := s.wafLogs(rc)
	results, errs := s.runLogQueries(rc, src, "waf-breakdown", queries)
	noteQueryErrors(panel, src, errs)

	// The breakdowns are rendered as one table each; the panel carries the
	// method breakdown and reports the others as separate rows so every value
	// stays copyable.
	var rows []domain.Row
	var total float64
	if res, ok := results["waf.byMethod"]; ok {
		for _, r := range res.Rows {
			n, _ := rowFloat(r, "n")
			total += n
			rows = append(rows, domain.Row{"dimension": "method", "key": r["method"], "count": r["n"]})
		}
	}
	if res, ok := results["waf.byPath"]; ok {
		for _, r := range res.Rows {
			key := r["uri"]
			if r["args"] != "" {
				key += "?" + r["args"]
			}
			rows = append(rows, domain.Row{"dimension": "path", "key": key, "count": r["n"]})
		}
	}
	for id, name := range headerQueries {
		res, ok := results[id]
		if !ok {
			continue
		}
		for _, r := range res.Rows {
			rows = append(rows, domain.Row{"dimension": "header:" + name, "key": r["value"], "count": r["n"]})
		}
	}

	panel.Table = domain.NewTable([]domain.Column{
		{Key: "dimension", Label: "구분"},
		{Key: "key", Label: "값", Mono: true, Copyable: true},
		{Key: "count", Label: "건수", Numeric: true},
	}, rows, honestTotal(float64(len(rows)), len(rows)), topN)

	// The bars and the table are two renderings of these same rows, so the
	// chart cannot end up counting something different from the list under it.
	panel.Bars = &domain.Bars{KeyColumn: "key", ValueColumn: "count", GroupColumn: "dimension"}

	panel.Stats = append(panel.Stats, domain.Stat{
		Key: "waf.requests.total", Label: "요청 수", Unit: domain.UnitCount,
		Value: domain.P(total), Basis: "WAF 로그 메소드별 집계 합계 (전체)",
	})
	noteQueryCost(panel, results)
	return panel, nil
}
