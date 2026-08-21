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
	// Cache.Do is used directly rather than the typed Cached wrapper, because
	// the wrapper drops the value when the loader reports an error and the
	// value is exactly what carries the per-query detail.
	got, err := s.Cache.Do(rc.ctx, b.String(), func(ctx context.Context) (any, error) {
		res, errs := src.runner.RunAll(ctx, src.group, rc.w, qs)
		// A wave in which nothing at all came back is reported as a failure so
		// the cache files it under its short error TTL. Returning nil here — as
		// this used to, unconditionally — recorded a total wipeout as a
		// perfectly good result, so an expired credential or a throttled
		// account stayed on screen for the full TTL after it had cleared.
		//
		// A partial failure is still a success: the queries that answered are
		// worth keeping, and the panel already reports the rest as warnings.
		if len(qs) > 0 && len(errs) == len(qs) {
			return bundle{res, errs}, errAllQueriesFailed
		}
		return bundle{res, errs}, nil
	})
	// The error is deliberately not inspected when a bundle came back. It is
	// this function's own signal to the cache, and the caller is better served
	// by the individual query errors it wraps.
	if got, ok := got.(bundle); ok {
		return got.results, got.errs
	}
	// No value at all means the lookup itself failed — a cancelled request,
	// typically — so there is nothing per-query to report.
	errs := map[string]error{}
	for _, q := range qs {
		errs[q.ID] = err
	}
	return nil, errs
}

// errAllQueriesFailed marks a bundle in which no query survived, so awsx.Cache
// expires it under ErrorTTL instead of remembering the wipeout for a full TTL.
// It never reaches a panel: runLogQueries hands back the per-query errors.
var errAllQueriesFailed = errors.New("every log query failed")

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

// warnIfTruncated says so when Logs Insights cut an aggregate at its result
// ceiling.
//
// An aggregate that hit the ceiling is not a smaller answer, it is a wrong one:
// the rows Insights kept are whatever the query happened to order first, so
// every total derived from them is short by an unknown amount. Nothing read
// this flag before, which is how a truncated stats could be summed into a
// headline number and drawn as a chart with nothing on screen to say it had
// stopped counting.
func warnIfTruncated(panel *domain.Panel, id string, results map[string]awsx.QueryResult) {
	if res, ok := results[id]; ok && res.Truncated {
		panel.Warn("%s 집계가 Logs Insights 결과 상한(%d행)에 걸려 잘렸습니다. 값이 실제보다 작습니다 — 조회 구간을 좁히거나 네임스페이스·제외 경로를 설정하세요.",
			id, awsx.InsightsMaxRows)
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
	warnIfTruncated(panel, series.ID, results)

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
				"container": r["container"],
				"namespace": r["namespace"],
				"method":    r["method"],
				"path":      r["path"],
				"target":    r["requestTarget"],
				"status":    r["status"],
				"latencyMs": r["latencyMs"],
				"clientIp":  r["clientIp"],
				"userAgent": r["userAgent"],
			})
		}
	}

	cols := []domain.Column{
		{Key: "timestamp", Label: "시각", Mono: true},
		{Key: "status", Label: "코드", Numeric: true, Copyable: true},
		{Key: "method", Label: "메소드"},
		{Key: "target", Label: "요청 대상", Mono: true, Copyable: true},
		{Key: "latencyMs", Label: "응답 시간", Unit: domain.UnitMillis, Numeric: true},
		{Key: "app", Label: "앱", Copyable: true},
		{Key: "pod", Label: "팟", Mono: true, Copyable: true},
		{Key: "clientIp", Label: "클라이언트 IP", Mono: true, Copyable: true},
		// Unconditional, because these two come from the Kubernetes envelope
		// rather than from the application's log line: they are there whatever
		// the operator has configured, so they can never open a detail view that
		// reads "—". Declaring them is also what gives the panel a row detail at
		// all — the frontend offers the expander wherever a detail column
		// exists, and the expanded view then shows every column, so an operator
		// looking at one 404 gets the whole request unclipped in one place.
		{Key: "container", Label: "컨테이너", Detail: true, Mono: true, Copyable: true},
		{Key: "namespace", Label: "네임스페이스", Detail: true, Mono: true, Copyable: true},
	}
	// Declared only when the query actually selected it — HasUserAgent is the
	// same test accessFields makes, so the column and the projection can never
	// disagree. This one *is* operator-configured, and an always-present column
	// would put a "—" in the detail of every cluster that has not named the
	// field or whose preset cannot parse one.
	if rc.cfg.LogFormat.HasUserAgent() {
		cols = append(cols, domain.Column{
			Key: "userAgent", Label: "User-Agent", Detail: true, Mono: true, Copyable: true,
		})
	}
	panel.Table = domain.NewTable(cols, rows, honestTotal(total, len(rows)), limit)

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

// statusBucket is one status code with the paths that produced it.
type statusBucket struct {
	code   string
	total  float64
	lastTs string
	paths  []pathCount
}

type pathCount struct {
	path string
	n    float64
}

// pivotStatusPaths folds (status, path) rows into one bucket per status code,
// busiest first, with each bucket's paths busiest first too.
//
// The same shape as pivotWAFActions and for the same reason: the query groups
// by both keys in one scan, and the fold that turns that into "one row per code,
// its paths underneath" is cheaper to do here than as a second query over the
// same bytes.
func pivotStatusPaths(rows []map[string]string) []statusBucket {
	index := map[string]int{}
	var out []statusBucket

	for _, r := range rows {
		code := r["status"]
		if code == "" {
			code = "(none)"
		}
		n, ok := rowFloat(r, "n")
		if !ok {
			continue
		}
		i, seen := index[code]
		if !seen {
			i = len(out)
			index[code] = i
			out = append(out, statusBucket{code: code})
		}
		b := &out[i]
		b.total += n
		// A row with no path still counts toward the code's total — dropping it
		// would make the breakdown disagree with the chart — but it has no name
		// to list, so it is not offered as a path.
		if p := r["path"]; p != "" {
			b.paths = append(b.paths, pathCount{path: p, n: n})
		}
		// Fixed-width @timestamp, so lexical order is chronological order.
		if ts := r["lastTs"]; ts > b.lastTs {
			b.lastTs = ts
		}
	}

	for i := range out {
		p := out[i].paths
		sort.SliceStable(p, func(a, b int) bool {
			if p[a].n != p[b].n {
				return p[a].n > p[b].n
			}
			return p[a].path < p[b].path
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].total != out[j].total {
			return out[i].total > out[j].total
		}
		return out[i].code < out[j].code
	})
	return out
}

// topPathsNote renders a bucket's paths for the row's expanded detail, and says
// how many it left out.
//
// One string rather than a nested table: the detail view is a definition list of
// this row's own values, and a code's paths are exactly that — one value that
// happens to be a list. Cutting silently at topN would read as "these are the
// paths", which for a 404 flood is the opposite of true.
func topPathsNote(b statusBucket, topN int) string {
	if len(b.paths) == 0 {
		return "경로가 기록되지 않았습니다"
	}
	shown := b.paths
	if topN > 0 && len(shown) > topN {
		shown = shown[:topN]
	}
	parts := make([]string, 0, len(shown)+1)
	for _, p := range shown {
		parts = append(parts, fmt.Sprintf("%s (%s건)", p.path, strconv.FormatFloat(p.n, 'f', -1, 64)))
	}
	if rest := len(b.paths) - len(shown); rest > 0 {
		parts = append(parts, fmt.Sprintf("외 %d개", rest))
	}
	return strings.Join(parts, " · ")
}

// buildPodStatusBreakdownPanel answers "which paths made up the 404s, and which
// made up the 403s" — one row per status code, its paths in the row's detail.
//
// It is a panel of its own rather than a second table on pod-status-codes
// because a panel carries one table, and the two are different kinds of thing:
// that one lists individual requests, this one aggregates them. Keeping it off
// the overview page is deliberate — a per-code path breakdown is a thing an
// operator goes looking for, and putting it there would add an Insights scan to
// the screen that loads most often.
func (s *Service) buildPodStatusBreakdownPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "pod-status-breakdown", Title: "응답 코드별 경로"}
	q := domain.LogQueries{Format: rc.cfg.LogFormat}
	topN := rc.cfg.Limits.TopN

	// A path field is not optional here the way it is elsewhere — this panel is
	// the breakdown *by path*, so without one there is nothing for it to say.
	// Said as a warning rather than an error: every other pod panel degrades in
	// this configuration instead of failing, and returning an error here would
	// turn the whole pod-logs page into a failure card and this panel's own
	// endpoint into a 502. Skipping the query also saves the scan.
	if rc.cfg.LogFormat.PathField == "" {
		panel.Warn("경로 필드(pathField)가 설정되지 않아 코드별 경로를 집계할 수 없습니다.")
		return panel, nil
	}

	byPath, err := q.PodBadStatusByPath()
	if err != nil {
		return nil, err
	}

	src := s.podLogs(rc)
	results, errs := s.runLogQueries(rc, src, "pod-status-breakdown", []domain.Query{byPath})
	noteQueryErrors(panel, src, errs)
	warnIfTruncated(panel, byPath.ID, results)

	var rows []domain.Row
	var total float64
	if res, ok := results[byPath.ID]; ok {
		for _, b := range pivotStatusPaths(res.Rows) {
			total += b.total
			rows = append(rows, domain.Row{
				"status": b.code,
				"count":  b.total,
				"paths":  float64(len(b.paths)),
				// `timestamp`, not a name of its own: the frontend routes a cell
				// through the log-timestamp formatter by that key, so any other
				// name renders the raw UTC string beside columns that show local
				// time.
				"timestamp": b.lastTs,
				"topPaths":  topPathsNote(b, topN),
			})
		}
	}

	// The total is the row count, not the request count: this table lists status
	// codes, and every code the query saw is on it. Nothing is capped away at
	// this level — the cap inside a row, on its path list, is stated by
	// topPathsNote, and the one above the query is stated by warnIfTruncated.
	panel.Table = domain.NewTable([]domain.Column{
		{Key: "status", Label: "코드", Mono: true, Copyable: true},
		{Key: "count", Label: "건수", Numeric: true},
		{Key: "paths", Label: "경로 종류", Numeric: true},
		{Key: "timestamp", Label: "마지막 발생", Mono: true},
		{Key: "topPaths", Label: "상위 경로", Detail: true, Mono: true, Copyable: true},
	}, rows, int64(len(rows)), topN)

	// Two renderings of the same rows, so the bars cannot count something the
	// table does not.
	panel.Bars = &domain.Bars{KeyColumn: "status", ValueColumn: "count"}

	// Both of these are floors rather than counts once the query hit the row cap:
	// the rows Insights dropped were the lowest-count (status, path) pairs, so a
	// code that only ever appeared in them is missing from the tally and its
	// requests are missing from the sum. Saying "이상" in the basis is the whole
	// fix available here — the number cannot be recovered without a second scan.
	codesBasis := "구간 내 관측된 비정상 응답 코드"
	totalBasis := "코드 · 경로별 집계 합계 (전체)"
	if res, ok := results[byPath.ID]; ok && res.Truncated {
		codesBasis += " (결과 상한에 걸려 실제보다 적을 수 있음)"
		totalBasis = "코드 · 경로별 집계 합계 (결과 상한에 걸려 실제 이상)"
	}

	panel.Stats = append(panel.Stats, domain.Stat{
		Key: "pod.badStatus.codes", Label: "코드 종류", Unit: domain.UnitCount,
		Value: domain.P(float64(len(rows))),
		Basis: codesBasis + excludedPaths(rc.cfg.LogFormat),
	}, domain.Stat{
		Key: "pod.badStatus.byPath.total", Label: "비정상 응답", Unit: domain.UnitCount,
		Value: domain.P(total),
		Basis: totalBasis + excludedPaths(rc.cfg.LogFormat),
		// No intent: the same number is already tagged bad on pod-status-codes,
		// and a second alarm tile for one fact puts two cards in the red for it.
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

	limit := rc.cfg.Limits.LogRows
	series := q.ActionSeries(rc.w)
	recent := q.RecentList(limit)
	src := s.wafLogs(rc)
	results, errs := s.runLogQueries(rc, src, "waf-traffic", []domain.Query{series, recent})
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
	var overall float64
	for _, a := range actions {
		panel.Series = append(panel.Series, byAction[a])
		overall += totals[a]
		// No intent. A WAF that blocks traffic is a WAF doing its job, so
		// tagging BLOCK as "bad" put the dashboard in a permanent alarm state
		// and made a genuine surge indistinguishable from a normal Tuesday.
		// The action is carried by its colour and glyph instead.
		panel.Stats = append(panel.Stats, domain.Stat{
			Key: "waf.log." + strings.ToLower(a), Label: a, Unit: domain.UnitCount,
			Value: domain.P(totals[a]), Basis: "WAF 로그 action 집계 (전체)",
		})
	}

	var rows []domain.Row
	if res, ok := results[recent.ID]; ok {
		for _, r := range res.Rows {
			rows = append(rows, domain.Row{
				"timestamp":    r["@timestamp"],
				"action":       r["action"],
				"rule":         r["rule"],
				"clientIp":     r["clientIp"],
				"country":      r["country"],
				"method":       r["method"],
				"uri":          r["uri"],
				"args":         r["args"],
				"ruleType":     r["ruleType"],
				"userAgent":    r["userAgent"],
				"responseCode": wafResponseNote(r["action"], r["responseCode"]),
			})
		}
	}

	// The total is the action series' own sum, counted over the whole window and
	// entirely independently of this list. That is what lets the list stop at
	// the row cap without the count above it stopping too.
	//
	// No Bars: these are individual requests, not a distribution. Drawing bars
	// from them would put a chart on screen whose bar heights are all 1.
	panel.Table = domain.NewTable([]domain.Column{
		{Key: "timestamp", Label: "시각", Mono: true},
		{Key: "action", Label: "처리"},
		{Key: "method", Label: "메소드"},
		{Key: "uri", Label: "경로", Mono: true, Copyable: true},
		{Key: "args", Label: "쿼리", Mono: true, Copyable: true},
		{Key: "clientIp", Label: "클라이언트", Mono: true, Copyable: true},
		{Key: "country", Label: "국가"},
		{Key: "rule", Label: "룰", Copyable: true},
		// Detail only. A rule type is a word an operator wants once, about one
		// row; a User-Agent is too long to give a column without squeezing
		// every other one; and the response note is a sentence.
		{Key: "ruleType", Label: "룰 종류", Detail: true},
		{Key: "userAgent", Label: "User-Agent", Detail: true, Mono: true, Copyable: true},
		{Key: "responseCode", Label: "응답 코드", Detail: true},
	}, rows, honestTotal(overall, len(rows)), limit)

	noteQueryCost(panel, results)
	return panel, nil
}

// wafResponseNote says what response a request got, and says so in words
// because for most rows there is no number to give.
//
// A WAF log does not carry the application's status code. responseCodeSent is
// written only when a Block action has a custom response configured; a plain
// block answers 403 and records nothing about it; and whatever the WAF allowed
// was answered by the application, which only the pod logs saw. A "상태 코드"
// column here would therefore print a number that no record supports for every
// row but one kind — so the column is a sentence, and the sentence says which
// case this row is and where the rest of the answer lives.
func wafResponseNote(action, sent string) string {
	if sent != "" {
		return sent + " (WAF 사용자 지정 응답)"
	}
	switch strings.ToUpper(strings.TrimSpace(action)) {
	case "BLOCK":
		return "403 · WAF 기본 차단 응답 (로그에 코드가 기록되지 않음)"
	case "CAPTCHA", "CHALLENGE":
		return "WAF가 CAPTCHA · Challenge 응답을 보냈습니다 (코드는 로그에 없음)"
	default:
		return "WAF 로그에 없음 · 애플리케이션 응답 코드는 팟 로그에서 확인하세요"
	}
}

// wafActionColor fixes one colour per WAF action, for every panel that shows
// one. The frontend aliases these as --waf-allow, --waf-block and so on, and
// draws a distinct glyph beside each, so the action is still readable when the
// colours are not.
//
// CAPTCHA and CHALLENGE are named rather than left to the default. They used to
// share the fallback with an empty action, so three different outcomes were
// drawn as one indistinguishable yellow.
func wafActionColor(action string) string {
	switch strings.ToUpper(action) {
	case "ALLOW":
		return domain.ColorGreen
	case "BLOCK":
		return domain.ColorPink
	case "COUNT", "EXCLUDED_AS_COUNT":
		return domain.ColorGray
	case "CHALLENGE":
		return domain.ColorOrange
	case "CAPTCHA":
		return domain.ColorPurple
	default:
		return domain.ColorYellow
	}
}

func (s *Service) buildWAFBlockedPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "waf-blocked", Title: "차단된 요청"}
	q := domain.WAFQueries{Headers: rc.cfg.WAFHeaders}

	agg := q.Blocked(rc.cfg.Limits.TopN)
	src := s.wafLogs(rc)
	results, errs := s.runLogQueries(rc, src, "waf-blocked", []domain.Query{agg})
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

	// The per-request list that used to be fetched here and thrown away now
	// lives on waf-traffic, where it is actually rendered — and where it covers
	// every action rather than only blocks. This panel's scan count is one
	// lower than it was, and the page's is unchanged.
	noteQueryCost(panel, results)
	return panel, nil
}

// actionFanout is how much room the breakdown queries are given on top of the
// number of keys the table will show.
//
// Each breakdown now groups by action as well as by its own key, so a single
// path occupies one row per action it was ever given. Without the headroom the
// query's own limit would decide which keys survive — and it would decide by
// (key, action) volume, so a path with a big ALLOW row and a small BLOCK row
// could arrive with the block silently missing, which is the one number the
// breakdown exists to show.
const actionFanout = 4

// wafBucket is one breakdown key with its per-action split folded back
// together.
type wafBucket struct {
	key                 string
	allow, block, other float64
	total               float64
	lastAction          string
	lastTs              string
}

// pivotWAFActions folds (key, action) rows into one bucket per key, busiest
// first.
//
// The per-action counts are summed here rather than in the query because Logs
// Insights has no conditional aggregate: asking for "count where action=BLOCK"
// alongside "count where action=ALLOW" in one stats command is not expressible,
// and asking for them as two commands is two scans of the same bytes.
func pivotWAFActions(rows []map[string]string, keyOf func(map[string]string) string) []wafBucket {
	index := map[string]int{}
	var out []wafBucket

	for _, r := range rows {
		key := keyOf(r)
		if key == "" {
			continue
		}
		n, ok := rowFloat(r, "n")
		if !ok {
			continue
		}
		i, seen := index[key]
		if !seen {
			i = len(out)
			index[key] = i
			out = append(out, wafBucket{key: key})
		}
		b := &out[i]

		action := strings.ToUpper(strings.TrimSpace(r["action"]))
		switch action {
		case "ALLOW":
			b.allow += n
		case "BLOCK":
			b.block += n
		default:
			// COUNT, CHALLENGE, CAPTCHA and anything a future WAF version adds.
			// Lumped rather than dropped: the columns have to add up to the
			// total, or the table invites the reader to work out the difference
			// and find it means nothing.
			b.other += n
		}
		b.total += n

		// Insights renders @timestamp as a fixed-width "YYYY-MM-DD hh:mm:ss.SSS",
		// so lexical order is chronological order and no parse is needed. An
		// empty action still wins the comparison if it is genuinely the newest —
		// it is reported as it was recorded rather than quietly skipped.
		if ts := r["lastTs"]; ts > b.lastTs {
			b.lastTs = ts
			b.lastAction = action
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].total != out[j].total {
			return out[i].total > out[j].total
		}
		return out[i].key < out[j].key
	})
	return out
}

func (s *Service) buildWAFBreakdownPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "waf-breakdown", Title: "WAF 통계"}
	q := domain.WAFQueries{Headers: rc.cfg.WAFHeaders}
	topN := rc.cfg.Limits.TopN
	fetch := topN * actionFanout

	queries := []domain.Query{q.ByMethod(), q.ByPath(fetch)}
	headerQueries := map[string]string{}
	for _, h := range rc.cfg.WAFHeaders {
		hq, err := q.ByHeader(h, fetch)
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

	// Every breakdown becomes rows of the one table, tagged by which breakdown
	// it came from, so each value keeps its own copy button.
	var rows []domain.Row
	var total float64
	// keysFound counts what the breakdowns actually distinguished, before the
	// per-dimension cap. The table's total is that, not the number of rows on
	// screen — a capped list must not shrink the count printed beside it.
	var keysFound int

	add := func(dimension string, buckets []wafBucket) {
		keysFound += len(buckets)
		if len(buckets) > topN {
			buckets = buckets[:topN]
		}
		for _, b := range buckets {
			rows = append(rows, domain.Row{
				"dimension":  dimension,
				"key":        b.key,
				"allow":      b.allow,
				"block":      b.block,
				"other":      b.other,
				"count":      b.total,
				"lastAction": b.lastAction,
			})
		}
	}

	if res, ok := results["waf.byMethod"]; ok {
		buckets := pivotWAFActions(res.Rows, func(r map[string]string) string { return r["method"] })
		for _, b := range buckets {
			total += b.total
		}
		add("method", buckets)
	}
	if res, ok := results["waf.byPath"]; ok {
		add("path", pivotWAFActions(res.Rows, func(r map[string]string) string {
			key := r["uri"]
			if r["args"] != "" {
				key += "?" + r["args"]
			}
			return key
		}))
	}
	// Sorted, because ranging a map would reorder the header breakdowns between
	// two otherwise identical responses.
	headerIDs := make([]string, 0, len(headerQueries))
	for id := range headerQueries {
		headerIDs = append(headerIDs, id)
	}
	sort.Strings(headerIDs)
	for _, id := range headerIDs {
		res, ok := results[id]
		if !ok {
			continue
		}
		add("header:"+headerQueries[id], pivotWAFActions(res.Rows, func(r map[string]string) string {
			return r["value"]
		}))
	}

	panel.Table = domain.NewTable([]domain.Column{
		{Key: "dimension", Label: "구분"},
		{Key: "key", Label: "값", Mono: true, Copyable: true},
		{Key: "allow", Label: "허용", Numeric: true},
		{Key: "block", Label: "차단", Numeric: true},
		{Key: "other", Label: "기타", Numeric: true},
		{Key: "count", Label: "합계", Numeric: true},
		{Key: "lastAction", Label: "마지막 처리"},
	}, rows, honestTotal(float64(keysFound), len(rows)), topN)

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
