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
type requestCtx struct {
	ctx context.Context
	w   domain.Window
	cfg config.Config
}

// fetchMetrics runs a metric fetch behind the cache.
func (s *Service) fetchMetrics(rc requestCtx, api awsx.MetricsAPI, name string, reqs []awsx.MetricRequest) (map[string][]awsx.MetricSeries, error) {
	if len(reqs) == 0 {
		return map[string][]awsx.MetricSeries{}, nil
	}
	key := metricCacheKey(name, rc.w, reqs)
	return awsx.Cached(rc.ctx, s.Cache, key, func(ctx context.Context) (map[string][]awsx.MetricSeries, error) {
		f := &awsx.MetricFetcher{API: api}
		if s.Metrics != nil {
			f = &awsx.MetricFetcher{API: api, MaxQueries: s.Metrics.MaxQueries}
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
func toSeries(w domain.Window, list []awsx.MetricSeries, spec domain.MetricSpec, labelFor func(awsx.MetricSeries) string) []*domain.Series {
	out := make([]*domain.Series, 0, len(list))
	for _, m := range list {
		label := spec.Label
		if labelFor != nil {
			label = labelFor(m)
		}
		out = append(out, m.ToSeries(w, label, spec.Unit, spec.Color))
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
	results, err := s.fetchMetrics(rc, s.Clients.CW, "targetgroup", metricRequests(specs, sets))
	if err != nil {
		return nil, err
	}

	byKey := map[string][]*domain.Series{}
	for _, spec := range specs {
		for _, fs := range sets {
			list := results[spec.Key+"|"+fs.id]
			awsx.SortSeries(list)
			series := toSeries(rc.w, list, spec, func(m awsx.MetricSeries) string {
				if len(sets) == 1 {
					return spec.Label
				}
				return fs.label + " · " + spec.Label
			})
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

func targetGroupFilterSets(cfg config.Config) []filterSet {
	var out []filterSet
	for _, tg := range cfg.TargetGroups {
		filters := map[string]string{"TargetGroup": tg}
		if cfg.LoadBalancer != "" {
			filters["LoadBalancer"] = cfg.LoadBalancer
		}
		out = append(out, filterSet{id: tg, label: domain.FriendlyTargetGroupName(lastSegmentName(tg)), filters: filters})
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

func (s *Service) buildPodResourcePanel(rc requestCtx) (*domain.Panel, error) {
	return s.buildResourcePanel(rc, "pod-resource", "팟 리소스 사용률", domain.PodResourceMetrics(), map[string]string{
		"ClusterName": rc.cfg.ClusterName,
		"Namespace":   rc.cfg.Namespace,
	}, "팟")
}

func (s *Service) buildNodeResourcePanel(rc requestCtx) (*domain.Panel, error) {
	return s.buildResourcePanel(rc, "node-resource", "노드 리소스 사용률", domain.NodeResourceMetrics(), map[string]string{
		"ClusterName": rc.cfg.ClusterName,
	}, "노드")
}

func (s *Service) buildResourcePanel(rc requestCtx, id, title string, specs []domain.MetricSpec, filters map[string]string, noun string) (*domain.Panel, error) {
	panel := &domain.Panel{ID: id, Title: title}
	if rc.cfg.ClusterName == "" {
		panel.Warn("클러스터가 선택되지 않았습니다. 설정에서 EKS 클러스터를 선택하세요.")
		return panel, nil
	}

	sets := []filterSet{{id: "cluster", label: rc.cfg.ClusterName, filters: filters}}
	results, err := s.fetchMetrics(rc, s.Clients.CW, id, metricRequests(specs, sets))
	if err != nil {
		return nil, err
	}

	empty := true
	for _, spec := range specs {
		list := results[spec.Key+"|cluster"]
		if len(list) > 0 {
			empty = false
		}
		awsx.SortSeries(list)
		series := toSeries(rc.w, list, spec, func(m awsx.MetricSeries) string {
			return m.Label + " · " + spec.Label
		})
		panel.Series = append(panel.Series, series...)

		panel.Stats = append(panel.Stats, domain.Stat{
			Key:    spec.Key + ".max",
			Label:  "최대 " + spec.Label,
			Unit:   spec.Unit,
			Value:  reduceAcross(series, (*domain.Series).Max, maxOf),
			Basis:  fmt.Sprintf("%s %s, %s %d개", spec.MetricName, spec.Stat, noun, len(series)),
			Intent: spec.Intent,
		})
	}

	if empty {
		panel.Warn("Container Insights 지표가 없습니다. 클러스터에서 Container Insights가 활성화되어 있는지 확인하세요.")
	}
	return panel, nil
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
	results, err := s.fetchMetrics(rc, s.Clients.CW, "counts", metricRequests(specs, sets))
	if err != nil {
		return nil, err
	}

	podSpec, nodeSpec := specs[0], specs[1]
	podParts := toSeries(rc.w, results["count.pods|cluster"], podSpec, func(m awsx.MetricSeries) string { return m.Label })
	pods := sumSeries(rc.w, podParts, "실행 중 팟", podSpec.Unit, podSpec.Color)
	nodes := toSeries(rc.w, results["count.nodes|cluster"], nodeSpec, func(awsx.MetricSeries) string { return nodeSpec.Label })

	panel.Series = append(panel.Series, pods)
	panel.Series = append(panel.Series, nodes...)

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
		return awsx.ClusterNodeScaling(ctx, s.Clients.EKS, rc.cfg.ClusterName)
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

	results, err := s.fetchMetrics(rc, s.Clients.CW, "pod-status", metricRequests(specs, sets))
	if err != nil {
		return nil, err
	}

	for _, spec := range specs {
		parts := toSeries(rc.w, results[spec.Key+"|cluster"], spec, func(m awsx.MetricSeries) string { return m.Label })
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

	// Container Insights publishes no OOMKilled metric. Restarts are the
	// closest signal it has, and the OOM count on the log panel comes from
	// matching the pod log stream instead. Saying so beats implying the
	// restart count is an OOM count.
	panel.Warn("Container Insights에는 OOMKilled 전용 지표가 없습니다. CrashLoop은 재시작 증가로, OOM은 팟 로그의 OOMKilled 패턴으로 확인하세요.")
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

	results, err := s.fetchMetrics(rc, s.Clients.CW, "rds-proxy", metricRequests(specs, sets))
	if err != nil {
		return nil, err
	}

	byKey := map[string][]*domain.Series{}
	for _, spec := range specs {
		for _, fs := range sets {
			list := results[spec.Key+"|"+fs.id]
			awsx.SortSeries(list)
			series := toSeries(rc.w, list, spec, func(awsx.MetricSeries) string {
				if len(sets) == 1 {
					return spec.Label
				}
				return fs.label + " · " + spec.Label
			})
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
	api := s.Clients.CW
	if s.wafRegion() != s.region() {
		api = s.Clients.CWGlobal
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
			series := toSeries(rc.w, list, spec, func(awsx.MetricSeries) string {
				if len(sets) == 1 {
					return spec.Label
				}
				return fs.label + " · " + spec.Label
			})
			panel.Series = append(panel.Series, series...)
			byKey[spec.Key] = append(byKey[spec.Key], series...)
		}
	}

	allowed := reduceAcross(byKey["waf.allowed"], (*domain.Series).Sum, addOf)
	blocked := reduceAcross(byKey["waf.blocked"], (*domain.Series).Sum, addOf)
	panel.Stats = []domain.Stat{
		{Key: "waf.allowed.total", Label: "허용", Unit: domain.UnitCount, Value: allowed,
			Basis: "AWS/WAFV2 AllowedRequests Sum", Intent: domain.IntentGood},
		{Key: "waf.blocked.total", Label: "차단", Unit: domain.UnitCount, Value: blocked,
			Basis: "AWS/WAFV2 BlockedRequests Sum", Intent: domain.IntentBad},
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
