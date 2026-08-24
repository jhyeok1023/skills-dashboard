package api

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jhyeok1023/skills-dashboard/internal/awsx"
	"github.com/jhyeok1023/skills-dashboard/internal/config"
	"github.com/jhyeok1023/skills-dashboard/internal/domain"
)

// requestCtx is the per-request state every panel builder reads. The window is
// resolved once at the edge and passed down, so no builder can anchor itself to
// a different clock.
//
// The AWS connection is carried the same way and for the same reason: one
// snapshot per request, so a key saved on the settings page cannot land between
// two panels of the same page and leave them describing different accounts.
type requestCtx struct {
	ctx context.Context
	w   domain.Window
	cfg config.Config
	aws *AWSConn
}

// fetchMetrics runs a metric fetch behind the cache.
func (s *Service) fetchMetrics(rc requestCtx, api awsx.MetricsAPI, name string, reqs []awsx.MetricRequest) (map[string][]awsx.MetricSeries, error) {
	if len(reqs) == 0 {
		return map[string][]awsx.MetricSeries{}, nil
	}
	key := metricCacheKey(name, rc.w, reqs)
	return awsx.Cached(rc.ctx, s.Cache, key, func(ctx context.Context) (map[string][]awsx.MetricSeries, error) {
		f := &awsx.MetricFetcher{API: api}
		if rc.aws != nil && rc.aws.Metrics != nil {
			f = &awsx.MetricFetcher{API: api, MaxQueries: rc.aws.Metrics.MaxQueries}
		}
		return f.Fetch(ctx, rc.w, reqs)
	})
}

// metricCacheKey folds the window and every request into a stable string. The
// window is part of the key, so a cached value can never be served against a
// different span than the one it was fetched for.
func metricCacheKey(name string, w domain.Window, reqs []awsx.MetricRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "metrics|%s|%d|%d|%d|", name, w.Start.Unix(), w.End.Unix(), w.Period.Seconds())
	parts := make([]string, 0, len(reqs))
	for _, r := range reqs {
		keys := make([]string, 0, len(r.Filters))
		for k := range r.Filters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var f strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&f, "%s=%s;", k, r.Filters[k])
		}
		parts = append(parts, r.ResultKey()+"@"+r.Spec.Stat+"{"+f.String()+"}")
	}
	sort.Strings(parts)
	b.WriteString(strings.Join(parts, "|"))
	return b.String()
}

// toSeries projects fetched results onto the window, sorted so the busiest
// series appear first in the legend.
//
// Nothing is dropped. The reference implementation's chart silently plotted
// only the top six series and then recomputed its axes from that subset, so the
// axis described less data than the panel claimed to show.
//
// styleFor overrides the spec's colour and gives the line a dash pattern. It is
// nil everywhere the spec's own colour is the answer — a WAF panel's 차단 line
// is systemPink because blocking is what it means, and a per-series palette
// would destroy that. It is set on the panels where one spec fans out into one
// line per pod or node, and colour has to name the subject instead.
func toSeries(
	w domain.Window,
	list []awsx.MetricSeries,
	spec domain.MetricSpec,
	labelFor func(awsx.MetricSeries) string,
	styleFor func(awsx.MetricSeries) (color, dash string),
) []*domain.Series {
	out := make([]*domain.Series, 0, len(list))
	for _, m := range list {
		label := spec.Label
		if labelFor != nil {
			label = labelFor(m)
		}
		color, dash := spec.Color, domain.DashSolid
		if styleFor != nil {
			color, dash = styleFor(m)
		}
		s := m.ToSeries(w, label, spec.Unit, color)
		s.Dash = dash
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Max(), out[j].Max()
		switch {
		case a == nil && b == nil:
			return out[i].Label < out[j].Label
		case a == nil:
			return false
		case b == nil:
			return true
		case *a != *b:
			return *a > *b
		default:
			return out[i].Label < out[j].Label
		}
	})
	return out
}

// reduceAcross applies f to the reductions of every series and returns the
// extreme. It is how a panel of many pods produces one headline number.
func reduceAcross(list []*domain.Series, reduce func(*domain.Series) domain.Point, pick func(a, b float64) float64) domain.Point {
	var acc *float64
	for _, s := range list {
		v := reduce(s)
		if v == nil {
			continue
		}
		if acc == nil {
			c := *v
			acc = &c
			continue
		}
		*acc = pick(*acc, *v)
	}
	return acc
}

func maxOf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minOf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func addOf(a, b float64) float64 { return a + b }

// setSeriesLabel names one series of a panel plotted from several filter sets:
// the metric, prefixed by the set when more than one is on the chart, and
// suffixed by CloudWatch's own label when a single set matched several series.
//
// The suffix is what tells duplicates apart. One filter set normally yields one
// series; it yields more whenever the SEARCH schema leaves a dimension
// unpinned — a target group registered with two load balancers, a WAF metric
// spanning rules — and then the set's own name labels every one of them
// identically. CloudWatch's label carries the dimensions it actually matched.
// Appending it only when there is something to disambiguate keeps the common
// legend short.
func setSeriesLabel(sets []filterSet, fs filterSet, spec domain.MetricSpec, n int) func(awsx.MetricSeries) string {
	label := spec.Label
	if len(sets) > 1 {
		label = fs.label + " · " + spec.Label
	}
	ambiguous := n > 1
	return func(m awsx.MetricSeries) string {
		if ambiguous && m.Label != "" {
			return label + " (" + m.Label + ")"
		}
		return label
	}
}

// sumSeries collapses many series into one by adding them bucket by bucket.
// Used where the individual series are not interesting on their own — the total
// pod count across services, say.
func sumSeries(w domain.Window, list []*domain.Series, label string, unit domain.Unit, color string) *domain.Series {
	out := domain.NewSeries(label, unit, color, w.Buckets())
	for _, s := range list {
		for i, v := range s.Values {
			if v != nil {
				out.Add(i, *v)
			}
		}
	}
	return out
}

// metricRequests expands one spec per filter set, keeping the result keys
// distinct so several target groups or proxies stay separable.
func metricRequests(specs []domain.MetricSpec, sets []filterSet) []awsx.MetricRequest {
	var out []awsx.MetricRequest
	for _, spec := range specs {
		for _, fs := range sets {
			out = append(out, awsx.MetricRequest{
				Key:     spec.Key + "|" + fs.id,
				Spec:    spec,
				Filters: fs.filters,
			})
		}
	}
	return out
}

// filterSet is one named combination of dimension values.
type filterSet struct {
	id      string
	label   string
	filters map[string]string
}

func (s *Service) buildTargetGroupPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "targetgroup", Title: "타겟 그룹"}

	sets := targetGroupFilterSets(rc.cfg)
	if len(sets) == 0 {
		panel.Warn("타겟 그룹이 선택되지 않았습니다. 설정에서 대상을 선택하세요.")
		return panel, nil
	}

	specs := domain.TargetGroupMetrics()
	results, err := s.fetchMetrics(rc, rc.aws.Clients.CW, "targetgroup", metricRequests(specs, sets))
	if err != nil {
		return nil, err
	}

	byKey := map[string][]*domain.Series{}
	for _, spec := range specs {
		for _, fs := range sets {
			list := results[spec.Key+"|"+fs.id]
			awsx.SortSeries(list)
			series := toSeries(rc.w, list, spec, setSeriesLabel(sets, fs, spec, len(list)), nil)
			panel.Series = append(panel.Series, series...)
			byKey[spec.Key] = append(byKey[spec.Key], series...)
		}
	}

	panel.Stats = []domain.Stat{
		{
			Key: "tg.p99.max", Label: "최대 응답 시간 p99", Unit: domain.UnitSeconds,
			Value:  reduceAcross(byKey["tg.p99"], (*domain.Series).Max, maxOf),
			Basis:  "TargetResponseTime p99, 선택 구간 전체",
			Intent: domain.IntentNeutral,
		},
		{
			Key: "tg.4xx.total", Label: "대상 4xx", Unit: domain.UnitCount,
			Value:  reduceAcross(byKey["tg.4xx"], (*domain.Series).Sum, addOf),
			Basis:  "HTTPCode_Target_4XX_Count Sum",
			Intent: domain.IntentWarn,
		},
		{
			Key: "tg.5xx.total", Label: "대상 5xx", Unit: domain.UnitCount,
			Value:  reduceAcross(byKey["tg.5xx"], (*domain.Series).Sum, addOf),
			Basis:  "HTTPCode_Target_5XX_Count Sum",
			Intent: domain.IntentBad,
		},
		{
			Key: "tg.requests.total", Label: "요청 수", Unit: domain.UnitCount,
			Value: reduceAcross(byKey["tg.requests"], (*domain.Series).Sum, addOf),
			Basis: "RequestCount Sum",
		},
	}
	return panel, nil
}

// targetGroupFilterSets builds one filter set per selected target group.
//
// The LoadBalancer dimension is deliberately not pinned to cfg.LoadBalancer
// here. The SEARCH schema already carries it — {AWS/ApplicationELB,
// LoadBalancer,TargetGroup} — and that is what restricts the match to
// per-target metrics rather than the ALB's own; the value term would only
// narrow it further. A target group dimension is globally unique, so narrowing
// buys nothing, and it costs something real: with one target group per app
// spread across more than one ALB, every group not on cfg.LoadBalancer matched
// no metric at all and plotted as a flat empty series, which reads as "this app
// had no traffic".
func targetGroupFilterSets(cfg config.Config) []filterSet {
	out := make([]filterSet, 0, len(cfg.TargetGroups))
	for _, tg := range cfg.TargetGroups {
		out = append(out, filterSet{
			id:      tg,
			label:   domain.FriendlyTargetGroupName(lastSegmentName(tg)),
			filters: map[string]string{"TargetGroup": tg},
		})
	}
	if len(out) == 0 && cfg.LoadBalancer != "" {
		out = append(out, filterSet{
			id:      cfg.LoadBalancer,
			label:   cfg.LoadBalancer,
			filters: map[string]string{"LoadBalancer": cfg.LoadBalancer},
		})
	}
	return out
}

// lastSegmentName pulls the human-readable name out of a target group
// dimension: targetgroup/k8s-default-product-abc/def -> k8s-default-product-abc
func lastSegmentName(dimension string) string {
	parts := strings.Split(dimension, "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return dimension
}

// podResourcePanel and nodeResourcePanel build one panel per resource rather
// than one panel per subject. A single "pod resources" panel put CPU, memory
// and both over-limit ratios on one chart, which is one line per pod per
// metric: twenty pods drew eighty lines over 220 pixels. Splitting by prefix
// costs nothing at the API — the same specs are still fetched, just grouped by
// what they measure — and each chart gets its own axis and its own stat row.
func (s *Service) podResourcePanel(id, title, prefix string) panelBuilder {
	return func(rc requestCtx) (*domain.Panel, error) {
		return s.buildResourcePanel(rc, id, title,
			domain.SpecsWithPrefix(domain.PodResourceMetrics(), prefix),
			map[string]string{
				"ClusterName": rc.cfg.ClusterName,
				"Namespace":   rc.cfg.Namespace,
			}, "팟")
	}
}

func (s *Service) nodeResourcePanel(id, title, prefix string) panelBuilder {
	return func(rc requestCtx) (*domain.Panel, error) {
		return s.buildResourcePanel(rc, id, title,
			domain.SpecsWithPrefix(domain.NodeResourceMetrics(), prefix),
			map[string]string{
				"ClusterName": rc.cfg.ClusterName,
			}, "노드")
	}
}

func (s *Service) buildResourcePanel(rc requestCtx, id, title string, specs []domain.MetricSpec, filters map[string]string, noun string) (*domain.Panel, error) {
	panel := &domain.Panel{ID: id, Title: title}
	if rc.cfg.ClusterName == "" {
		panel.Warn("클러스터가 선택되지 않았습니다. 설정에서 EKS 클러스터를 선택하세요.")
		return panel, nil
	}

	sets := []filterSet{{id: "cluster", label: rc.cfg.ClusterName, filters: filters}}
	results, err := s.fetchMetrics(rc, rc.aws.Clients.CW, id, metricRequests(specs, sets))
	if err != nil {
		return nil, err
	}

	// Two passes over the fetched results, because both the colour and the
	// count depend on the whole panel rather than on one spec.
	empty := true
	live := make([][]awsx.MetricSeries, len(specs))
	gone := make([]int, len(specs))
	for i, spec := range specs {
		list := results[spec.Key+"|cluster"]
		if len(list) > 0 {
			// Deliberately before the filter: a cluster that publishes nothing
			// and a cluster whose old nodes are all that matched are different
			// facts, and only the first one is about Container Insights.
			empty = false
		}
		live[i], gone[i] = dropSilent(list)
		awsx.SortSeries(live[i])
	}

	subject := subjectIndex(live)
	for i, spec := range specs {
		dash := domain.VariantDash(i)
		series := toSeries(rc.w, live[i], spec,
			func(m awsx.MetricSeries) string { return m.Label + " · " + spec.Label },
			func(m awsx.MetricSeries) (string, string) {
				return domain.SubjectColor(subject[m.Label]), dash
			})
		panel.Series = append(panel.Series, series...)

		basis := fmt.Sprintf("%s %s, %s %d개", spec.MetricName, spec.Stat, noun, len(series))
		if gone[i] > 0 {
			// Said rather than left silent, but said in the basis and not as a
			// panel warning: a warning turns the whole card red, and a cluster
			// rebuilt last week would light it up permanently for no fault.
			basis += fmt.Sprintf(" (구간 내 데이터 없음 %d개 제외)", gone[i])
		}
		panel.Stats = append(panel.Stats, domain.Stat{
			Key:    spec.Key + ".max",
			Label:  "최대 " + spec.Label,
			Unit:   spec.Unit,
			Value:  reduceAcross(series, (*domain.Series).Max, maxOf),
			Basis:  basis,
			Intent: spec.Intent,
		})
	}

	if empty {
		panel.Warn("Container Insights 지표가 없습니다. 클러스터에서 Container Insights가 활성화되어 있는지 확인하세요.")
	}
	return panel, nil
}

// dropSilent removes the results that carry no sample in the window, returning
// what is left and how many went.
//
// A SEARCH matches CloudWatch's metric index, not the data in the requested
// span, and the index keeps a metric for about a fortnight after its last
// datapoint. Every node the cluster has ever run therefore comes back — a
// rebuilt cluster answers with a fresh InstanceId each time — and each one drew
// an all-gap line, took a legend row, and counted towards the panel's "노드 N개".
// One live node read as a dozen.
//
// Only the fan-out panels call this. Where the subject is one the operator
// picked — a target group, an RDS proxy — an empty series is the answer to
// their question and has to stay on the chart.
func dropSilent(list []awsx.MetricSeries) ([]awsx.MetricSeries, int) {
	out := make([]awsx.MetricSeries, 0, len(list))
	for _, m := range list {
		if len(m.Points) > 0 {
			out = append(out, m)
		}
	}
	return out, len(list) - len(out)
}

// subjectIndex numbers the subjects a panel draws, so a colour can be looked up
// by pod or node rather than by position in one spec's list.
//
// The numbering is over the union across specs and taken in label order, which
// buys two things. A pod's CPU line and its over-limit line are the same
// colour, because they are the same pod. And the colour does not move when the
// next poll reorders the series — toSeries sorts by value, so the busiest pod
// changes places constantly.
func subjectIndex(live [][]awsx.MetricSeries) map[string]int {
	seen := map[string]bool{}
	var labels []string
	for _, list := range live {
		for _, m := range list {
			if !seen[m.Label] {
				seen[m.Label] = true
				labels = append(labels, m.Label)
			}
		}
	}
	sort.Strings(labels)

	idx := make(map[string]int, len(labels))
	for i, l := range labels {
		idx[l] = i
	}
	return idx
}

func (s *Service) buildCountsPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "counts", Title: "팟 · 노드 개수"}
	if rc.cfg.ClusterName == "" {
		panel.Warn("클러스터가 선택되지 않았습니다.")
		return panel, nil
	}

	specs := domain.CountMetrics()
	sets := []filterSet{{id: "cluster", label: rc.cfg.ClusterName, filters: map[string]string{
		"ClusterName": rc.cfg.ClusterName,
	}}}
	results, err := s.fetchMetrics(rc, rc.aws.Clients.CW, "counts", metricRequests(specs, sets))
	if err != nil {
		return nil, err
	}

	// Looked up by key rather than by position: the specs are a list, and
	// reading specs[0]/specs[1] out of it means adding a metric anywhere but
	// the end silently repoints these two.
	byKey := map[string]domain.MetricSpec{}
	for _, spec := range specs {
		byKey[spec.Key] = spec
	}
	podSpec, nodeSpec, failedSpec := byKey["count.pods"], byKey["count.nodes"], byKey["count.nodes.failed"]

	podParts := toSeries(rc.w, results["count.pods|cluster"], podSpec, func(m awsx.MetricSeries) string { return m.Label }, nil)
	pods := sumSeries(rc.w, podParts, "실행 중 팟", podSpec.Unit, podSpec.Color)
	nodes := toSeries(rc.w, results["count.nodes|cluster"], nodeSpec, func(awsx.MetricSeries) string { return nodeSpec.Label }, nil)
	failed := toSeries(rc.w, results["count.nodes.failed|cluster"], failedSpec, func(awsx.MetricSeries) string { return failedSpec.Label }, nil)

	panel.Series = append(panel.Series, pods)
	panel.Series = append(panel.Series, nodes...)
	panel.Series = append(panel.Series, failed...)

	// Pods: minimum and maximum are what was observed over the window.
	// CloudWatch does not publish a HorizontalPodAutoscaler's configured
	// bounds, and this dashboard reads only AWS APIs, so the basis says
	// "observed" rather than implying these are the autoscaler's limits.
	observed := "관측값 (구간 내 최소/최대)"
	panel.Stats = append(panel.Stats,
		domain.Stat{Key: "pods.current", Label: "팟 (현재)", Unit: domain.UnitCount, Value: pods.Last(), Basis: "service_number_of_running_pods 합계"},
		domain.Stat{Key: "pods.min", Label: "팟 (최소)", Unit: domain.UnitCount, Value: pods.Min(), Basis: observed},
		domain.Stat{Key: "pods.max", Label: "팟 (최대)", Unit: domain.UnitCount, Value: pods.Max(), Basis: observed},
	)

	current := reduceAcross(nodes, (*domain.Series).Last, maxOf)
	panel.Stats = append(panel.Stats, domain.Stat{
		Key: "nodes.current", Label: "노드 (현재)", Unit: domain.UnitCount, Value: current,
		Basis: "cluster_node_count",
	})
	panel.Stats = append(panel.Stats, domain.Stat{
		Key: "nodes.failed", Label: "실패 노드", Unit: domain.UnitCount,
		Value: reduceAcross(failed, (*domain.Series).Max, maxOf),
		Basis: "cluster_failed_node_count Maximum, 구간 최대", Intent: failedSpec.Intent,
	})

	// Node bounds do have an authoritative source: the node groups' scaling
	// configuration. It is read per request rather than captured once at
	// startup, so a rescale is reflected immediately.
	scaling, err := s.nodeScaling(rc)
	if err != nil {
		panel.Warn("노드 최소/최대를 읽지 못했습니다: %v", err)
	} else {
		minV, maxV := float64(scaling.Min), float64(scaling.Max)
		basis := "EKS 노드그룹 scalingConfig 합계"
		if len(scaling.Groups) > 0 {
			basis += " (" + strings.Join(scaling.Groups, ", ") + ")"
		}
		panel.Stats = append(panel.Stats,
			domain.Stat{Key: "nodes.min", Label: "노드 (최소)", Unit: domain.UnitCount, Value: &minV, Basis: basis},
			domain.Stat{Key: "nodes.max", Label: "노드 (최대)", Unit: domain.UnitCount, Value: &maxV, Basis: basis},
		)
	}
	return panel, nil
}

func (s *Service) nodeScaling(rc requestCtx) (awsx.NodeScaling, error) {
	key := "nodescaling|" + rc.cfg.ClusterName
	return awsx.Cached(rc.ctx, s.Cache, key, func(ctx context.Context) (awsx.NodeScaling, error) {
		return awsx.ClusterNodeScaling(ctx, rc.aws.Clients.EKS, rc.cfg.ClusterName)
	})
}

func (s *Service) buildPodStatusPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "pod-status", Title: "팟 상태"}
	if rc.cfg.ClusterName == "" {
		panel.Warn("클러스터가 선택되지 않았습니다.")
		return panel, nil
	}

	specs := domain.PodStatusMetrics()
	filters := map[string]string{"ClusterName": rc.cfg.ClusterName}
	if rc.cfg.Namespace != "" {
		filters["Namespace"] = rc.cfg.Namespace
	}
	sets := []filterSet{{id: "cluster", label: rc.cfg.ClusterName, filters: filters}}

	results, err := s.fetchMetrics(rc, rc.aws.Clients.CW, "pod-status", metricRequests(specs, sets))
	if err != nil {
		return nil, err
	}

	empty := true
	for _, spec := range specs {
		list := results[spec.Key+"|cluster"]
		if len(list) > 0 {
			empty = false
		}
		parts := toSeries(rc.w, list, spec, func(m awsx.MetricSeries) string { return m.Label }, nil)
		total := sumSeries(rc.w, parts, spec.Label, spec.Unit, spec.Color)
		panel.Series = append(panel.Series, total)

		value, basis := total.Last(), spec.MetricName
		if spec.Key == "pod.restarts" {
			value, basis = total.Sum(), spec.MetricName+" 합계"
		}
		panel.Stats = append(panel.Stats, domain.Stat{
			Key: spec.Key, Label: spec.Label, Unit: spec.Unit, Value: value,
			Basis: basis, Intent: spec.Intent,
		})
	}

	// pod_status_* is published only by Container Insights with enhanced
	// observability, so an empty panel here has two quite different causes and
	// the warning has to name the one that is actually actionable. Without it,
	// "no pods are failing" and "this cluster never publishes these metrics"
	// look identical.
	if empty {
		panel.Warn("pod_status_* 지표가 없습니다. 이 지표는 Container Insights 확장 관찰성(amazon-cloudwatch-observability 애드온)에서만 게시됩니다.")
	}

	// This panel does not read the OOMKilled metric. Enhanced observability
	// does publish one (pod_container_status_terminated_reason_oom_killed), but
	// it appears only after the event, so the signal shown here is the restart
	// count and the OOM count on the log panel. Saying so beats implying the
	// restart count is an OOM count.
	panel.Warn("CrashLoop은 재시작 증가로, OOM은 팟 로그의 OOMKilled 패턴으로 확인하세요. 이 패널은 OOMKilled 지표를 읽지 않습니다.")
	return panel, nil
}

func (s *Service) buildRDSProxyPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "rds-proxy", Title: "RDS Proxy 커넥션"}
	if len(rc.cfg.RDSProxies) == 0 {
		panel.Warn("RDS Proxy가 선택되지 않았습니다. 설정에서 대상을 선택하세요.")
		return panel, nil
	}

	specs := domain.RDSProxyMetrics()
	var sets []filterSet
	for _, p := range rc.cfg.RDSProxies {
		sets = append(sets, filterSet{id: p, label: p, filters: map[string]string{"ProxyName": p}})
	}

	results, err := s.fetchMetrics(rc, rc.aws.Clients.CW, "rds-proxy", metricRequests(specs, sets))
	if err != nil {
		return nil, err
	}

	byKey := map[string][]*domain.Series{}
	for _, spec := range specs {
		for _, fs := range sets {
			list := results[spec.Key+"|"+fs.id]
			awsx.SortSeries(list)
			series := toSeries(rc.w, list, spec, setSeriesLabel(sets, fs, spec, len(list)), nil)
			panel.Series = append(panel.Series, series...)
			byKey[spec.Key] = append(byKey[spec.Key], series...)
		}
	}

	panel.Stats = []domain.Stat{
		{Key: "proxy.db.current", Label: "DB 커넥션 (현재)", Unit: domain.UnitConns,
			Value: reduceAcross(byKey["proxy.db"], (*domain.Series).Last, addOf), Basis: "DatabaseConnections Average"},
		{Key: "proxy.db.max", Label: "DB 커넥션 (최대)", Unit: domain.UnitConns,
			Value: reduceAcross(byKey["proxy.db"], (*domain.Series).Max, maxOf), Basis: "DatabaseConnections Average, 구간 최대"},
		{Key: "proxy.client.current", Label: "클라이언트 커넥션 (현재)", Unit: domain.UnitConns,
			Value: reduceAcross(byKey["proxy.client"], (*domain.Series).Last, addOf), Basis: "ClientConnections Average"},
		{Key: "proxy.pinned.max", Label: "세션 고정 (최대)", Unit: domain.UnitConns,
			Value: reduceAcross(byKey["proxy.pinned"], (*domain.Series).Max, maxOf),
			Basis: "DatabaseConnectionsCurrentlySessionPinned", Intent: domain.IntentWarn},
		{Key: "proxy.max.allowed", Label: "최대 허용 커넥션", Unit: domain.UnitConns,
			Value: reduceAcross(byKey["proxy.max"], (*domain.Series).Max, maxOf), Basis: "MaxDatabaseConnectionsAllowed"},
	}
	return panel, nil
}

func (s *Service) buildWAFMetricsPanel(rc requestCtx) (*domain.Panel, error) {
	panel := &domain.Panel{ID: "waf-metrics", Title: "WAF 메트릭"}
	if len(rc.cfg.WebACLs) == 0 {
		panel.Warn("Web ACL이 선택되지 않았습니다. 설정에서 대상을 선택하세요.")
		return panel, nil
	}

	specs := domain.WAFMetrics()
	var sets []filterSet
	for _, acl := range rc.cfg.WebACLs {
		sets = append(sets, filterSet{
			id: acl, label: acl,
			filters: map[string]string{"WebACL": acl, "Rule": "ALL"},
		})
	}

	// CLOUDFRONT-scoped ACLs publish into us-east-1 regardless of where the
	// distribution serves from. The comparison is between the regions the
	// clients were actually built for, not the two the config records: the
	// config's region is a note about the credentials, not what sets them.
	api := rc.aws.Clients.CW
	if s.wafRegion() != s.region() {
		api = rc.aws.Clients.CWGlobal
	}

	results, err := s.fetchMetrics(rc, api, "waf-metrics", metricRequests(specs, sets))
	if err != nil {
		return nil, err
	}

	byKey := map[string][]*domain.Series{}
	for _, spec := range specs {
		for _, fs := range sets {
			list := results[spec.Key+"|"+fs.id]
			awsx.SortSeries(list)
			series := toSeries(rc.w, list, spec, setSeriesLabel(sets, fs, spec, len(list)), nil)
			panel.Series = append(panel.Series, series...)
			byKey[spec.Key] = append(byKey[spec.Key], series...)
		}
	}

	allowed := reduceAcross(byKey["waf.allowed"], (*domain.Series).Sum, addOf)
	blocked := reduceAcross(byKey["waf.blocked"], (*domain.Series).Sum, addOf)
	panel.Stats = []domain.Stat{
		// Deliberately intent-free: see buildWAFTrafficPanel. Blocking is the
		// normal state here, not a condition to raise.
		{Key: "waf.allowed.total", Label: "허용", Unit: domain.UnitCount, Value: allowed,
			Basis: "AWS/WAFV2 AllowedRequests Sum"},
		{Key: "waf.blocked.total", Label: "차단", Unit: domain.UnitCount, Value: blocked,
			Basis: "AWS/WAFV2 BlockedRequests Sum"},
	}
	if allowed != nil && blocked != nil && (*allowed+*blocked) > 0 {
		rate := *blocked / (*allowed + *blocked) * 100
		panel.Stats = append(panel.Stats, domain.Stat{
			Key: "waf.blocked.rate", Label: "차단 비율", Unit: domain.UnitPercent, Value: &rate,
			Basis: "BlockedRequests / (Allowed + Blocked), 메트릭 기준",
		})
	}

	// The same page carries block counts derived from WAF logs. The two will
	// not match exactly — log delivery lags the metric by minutes — so the
	// basis on each stat names its source rather than leaving the discrepancy
	// to be discovered.
	panel.Warn("이 값은 CloudWatch 메트릭 기준입니다. 로그 기반 통계와는 전달 지연으로 차이가 날 수 있습니다.")
	return panel, nil
}
