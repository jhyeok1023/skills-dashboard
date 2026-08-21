package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MetricSpec pins everything about one CloudWatch metric in a single place:
// which namespace and statistic it is read with, what unit it carries, and how
// it is coloured.
//
// The statistic in particular belongs here rather than at each call site. Read
// the same counter as Sum on one screen and Average on another and the two
// screens disagree forever, with nothing in either one hinting at why.
type MetricSpec struct {
	Key        string
	Label      string
	Namespace  string
	MetricName string
	Stat       string
	Unit       Unit
	Color      string
	Intent     Intent

	// Dimensions are the dimension names the SEARCH expression groups by. The
	// last one is what distinguishes the individual series.
	Dimensions []string
}

// CloudWatch namespaces.
const (
	NSApplicationELB  = "AWS/ApplicationELB"
	NSContainer       = "ContainerInsights"
	NSRDS             = "AWS/RDS"
	NSWAFV2           = "AWS/WAFV2"
	NSContainerPodSvc = "ContainerInsights"
)

// Statistics. Counters are summed, utilisations are averaged, latencies are
// read at percentiles.
const (
	StatSum = "Sum"
	StatAvg = "Average"
	StatMax = "Maximum"
	StatMin = "Minimum"
	StatP50 = "p50"
	StatP90 = "p90"
	StatP99 = "p99"
)

// Apple's system palette, referenced by name. The CSS custom properties that
// carry the actual oklch values live in the frontend; the backend only decides
// which semantic colour a series should take, so a metric's meaning and its
// colour cannot drift apart between panels.
const (
	ColorBlue   = "systemBlue"
	ColorGreen  = "systemGreen"
	ColorIndigo = "systemIndigo"
	ColorOrange = "systemOrange"
	ColorPink   = "systemPink"
	ColorPurple = "systemPurple"
	ColorRed    = "systemRed"
	ColorTeal   = "systemTeal"
	ColorYellow = "systemYellow"
	ColorMint   = "systemMint"
	ColorGray   = "systemGray"
)

// SubjectPalette colours one subject — a pod, a node — on a panel where a
// single spec fans out into many series.
//
// A spec's own colour says what the line measures, which is the right answer
// when a chart holds one line per metric. It is the wrong answer when the
// SEARCH fans out: every pod on the CPU panel took ColorIndigo, so twenty pods
// drew twenty identical lines and the legend text was the only way to tell them
// apart. Colour has to carry the subject there, and the metric moves to the
// line's dash pattern — see VariantDash.
//
// The order is not the constant block's. Neighbouring entries are kept apart
// under red-green and blue-yellow colour vision deficiency, so a chart with
// three pods on it does not hand out three colours that some readers see as
// one. ColorGray is excluded: sumSeries already spends it on totals.
var SubjectPalette = []string{
	ColorBlue, ColorOrange, ColorGreen, ColorPink, ColorPurple,
	ColorYellow, ColorTeal, ColorRed, ColorIndigo, ColorMint,
}

// SubjectColor picks the palette entry for the i-th subject, cycling when a
// panel holds more subjects than the palette has colours. Past that point the
// dash pattern and the legend label are what separate two lines; the caller is
// expected to order subjects deterministically so a colour does not move
// between refreshes.
func SubjectColor(i int) string {
	if i < 0 {
		i = 0
	}
	return SubjectPalette[i%len(SubjectPalette)]
}

// TargetGroupMetrics covers requirement 3: target response time, 5xx and 4xx.
func TargetGroupMetrics() []MetricSpec {
	dims := []string{"LoadBalancer", "TargetGroup"}
	return []MetricSpec{
		{Key: "tg.p50", Label: "응답 시간 p50", Namespace: NSApplicationELB, MetricName: "TargetResponseTime", Stat: StatP50, Unit: UnitSeconds, Color: ColorTeal, Dimensions: dims},
		{Key: "tg.p90", Label: "응답 시간 p90", Namespace: NSApplicationELB, MetricName: "TargetResponseTime", Stat: StatP90, Unit: UnitSeconds, Color: ColorBlue, Dimensions: dims},
		{Key: "tg.p99", Label: "응답 시간 p99", Namespace: NSApplicationELB, MetricName: "TargetResponseTime", Stat: StatP99, Unit: UnitSeconds, Color: ColorIndigo, Dimensions: dims},
		{Key: "tg.4xx", Label: "대상 4xx", Namespace: NSApplicationELB, MetricName: "HTTPCode_Target_4XX_Count", Stat: StatSum, Unit: UnitCount, Color: ColorOrange, Intent: IntentWarn, Dimensions: dims},
		{Key: "tg.5xx", Label: "대상 5xx", Namespace: NSApplicationELB, MetricName: "HTTPCode_Target_5XX_Count", Stat: StatSum, Unit: UnitCount, Color: ColorRed, Intent: IntentBad, Dimensions: dims},
		{Key: "tg.requests", Label: "요청 수", Namespace: NSApplicationELB, MetricName: "RequestCount", Stat: StatSum, Unit: UnitCount, Color: ColorGray, Dimensions: dims},
		{Key: "tg.healthy", Label: "정상 대상", Namespace: NSApplicationELB, MetricName: "HealthyHostCount", Stat: StatAvg, Unit: UnitCount, Color: ColorGreen, Intent: IntentGood, Dimensions: dims},
	}
}

// PodResourceMetrics covers requirement 4.
func PodResourceMetrics() []MetricSpec {
	dims := []string{"ClusterName", "Namespace", "PodName"}
	return []MetricSpec{
		{Key: "pod.cpu", Label: "CPU 사용률", Namespace: NSContainer, MetricName: "pod_cpu_utilization", Stat: StatAvg, Unit: UnitPercent, Color: ColorIndigo, Dimensions: dims},
		{Key: "pod.mem", Label: "메모리 사용률", Namespace: NSContainer, MetricName: "pod_memory_utilization", Stat: StatAvg, Unit: UnitPercent, Color: ColorTeal, Dimensions: dims},
		{Key: "pod.cpu.limit", Label: "CPU 사용률 (limit 대비)", Namespace: NSContainer, MetricName: "pod_cpu_utilization_over_pod_limit", Stat: StatAvg, Unit: UnitPercent, Color: ColorPurple, Dimensions: dims},
		{Key: "pod.mem.limit", Label: "메모리 사용률 (limit 대비)", Namespace: NSContainer, MetricName: "pod_memory_utilization_over_pod_limit", Stat: StatAvg, Unit: UnitPercent, Color: ColorPink, Intent: IntentWarn, Dimensions: dims},
	}
}

// NodeResourceMetrics covers requirement 5.
//
// InstanceId belongs in the schema even though nothing pins it: Container
// Insights publishes the node metrics under {NodeName, ClusterName, InstanceId}
// and {ClusterName}, and nothing else. A SEARCH schema is matched as a set, so
// {ClusterName,NodeName} matched no metric at all and the panel rendered empty
// — indistinguishable from a cluster with Container Insights switched off.
func NodeResourceMetrics() []MetricSpec {
	dims := []string{"ClusterName", "InstanceId", "NodeName"}
	return []MetricSpec{
		{Key: "node.cpu", Label: "CPU 사용률", Namespace: NSContainer, MetricName: "node_cpu_utilization", Stat: StatAvg, Unit: UnitPercent, Color: ColorIndigo, Dimensions: dims},
		{Key: "node.mem", Label: "메모리 사용률", Namespace: NSContainer, MetricName: "node_memory_utilization", Stat: StatAvg, Unit: UnitPercent, Color: ColorTeal, Dimensions: dims},
		{Key: "node.fs", Label: "디스크 사용률", Namespace: NSContainer, MetricName: "node_filesystem_utilization", Stat: StatAvg, Unit: UnitPercent, Color: ColorYellow, Dimensions: dims},
	}
}

// CountMetrics covers requirement 6: how many pods and nodes there are.
func CountMetrics() []MetricSpec {
	return []MetricSpec{
		{Key: "count.pods", Label: "실행 중 팟", Namespace: NSContainer, MetricName: "service_number_of_running_pods", Stat: StatAvg, Unit: UnitCount, Color: ColorBlue, Dimensions: []string{"ClusterName", "Namespace", "Service"}},
		{Key: "count.nodes", Label: "노드", Namespace: NSContainer, MetricName: "cluster_node_count", Stat: StatAvg, Unit: UnitCount, Color: ColorGreen, Dimensions: []string{"ClusterName"}},
		{Key: "count.nodes.failed", Label: "실패 노드", Namespace: NSContainer, MetricName: "cluster_failed_node_count", Stat: StatMax, Unit: UnitCount, Color: ColorRed, Intent: IntentBad, Dimensions: []string{"ClusterName"}},
	}
}

// PodStatusMetrics covers requirement 7.
//
// PodName is in the schema because none of these metrics is published at
// {Namespace, ClusterName}: the pod-level set is {PodName, Namespace,
// ClusterName}. The panel sums the fan-out back down, so the numbers mean the
// same thing they were meant to; without PodName they meant nothing, because
// the SEARCH matched no series.
//
// pod_status_* is published only by Container Insights with enhanced
// observability. pod_number_of_container_restarts is published by both, which
// is why the panel can still say something on a cluster running the older
// agent.
func PodStatusMetrics() []MetricSpec {
	dims := []string{"ClusterName", "Namespace", "PodName"}
	return []MetricSpec{
		{Key: "pod.running", Label: "Running", Namespace: NSContainer, MetricName: "pod_status_running", Stat: StatAvg, Unit: UnitCount, Color: ColorGreen, Intent: IntentGood, Dimensions: dims},
		{Key: "pod.pending", Label: "Pending", Namespace: NSContainer, MetricName: "pod_status_pending", Stat: StatAvg, Unit: UnitCount, Color: ColorOrange, Intent: IntentWarn, Dimensions: dims},
		{Key: "pod.failed", Label: "Failed", Namespace: NSContainer, MetricName: "pod_status_failed", Stat: StatAvg, Unit: UnitCount, Color: ColorRed, Intent: IntentBad, Dimensions: dims},
		{Key: "pod.restarts", Label: "컨테이너 재시작", Namespace: NSContainer, MetricName: "pod_number_of_container_restarts", Stat: StatSum, Unit: UnitCount, Color: ColorPink, Intent: IntentBad, Dimensions: dims},
	}
}

// RDSProxyMetrics covers requirement 8.
func RDSProxyMetrics() []MetricSpec {
	dims := []string{"ProxyName"}
	return []MetricSpec{
		{Key: "proxy.db", Label: "DB 커넥션", Namespace: NSRDS, MetricName: "DatabaseConnections", Stat: StatAvg, Unit: UnitConns, Color: ColorPurple, Dimensions: dims},
		{Key: "proxy.client", Label: "클라이언트 커넥션", Namespace: NSRDS, MetricName: "ClientConnections", Stat: StatAvg, Unit: UnitConns, Color: ColorBlue, Dimensions: dims},
		{Key: "proxy.pinned", Label: "세션 고정 커넥션", Namespace: NSRDS, MetricName: "DatabaseConnectionsCurrentlySessionPinned", Stat: StatAvg, Unit: UnitConns, Color: ColorOrange, Intent: IntentWarn, Dimensions: dims},
		{Key: "proxy.borrow", Label: "커넥션 대기", Namespace: NSRDS, MetricName: "DatabaseConnectionsBorrowLatency", Stat: StatAvg, Unit: UnitMillis, Color: ColorYellow, Dimensions: dims},
		{Key: "proxy.max", Label: "최대 허용 커넥션", Namespace: NSRDS, MetricName: "MaxDatabaseConnectionsAllowed", Stat: StatMax, Unit: UnitConns, Color: ColorGray, Dimensions: dims},
	}
}

// WAFMetrics is the metric-side view of the WAF, complementing the log-derived
// statistics on the same page.
func WAFMetrics() []MetricSpec {
	dims := []string{"WebACL", "Rule", "Region"}
	return []MetricSpec{
		// No intent on either: a WAF blocking requests is the normal operating
		// state, so flagging it permanently lit the alarm and buried the cases
		// that actually deviate. Colour still separates the two.
		{Key: "waf.allowed", Label: "허용", Namespace: NSWAFV2, MetricName: "AllowedRequests", Stat: StatSum, Unit: UnitCount, Color: ColorGreen, Dimensions: dims},
		{Key: "waf.blocked", Label: "차단", Namespace: NSWAFV2, MetricName: "BlockedRequests", Stat: StatSum, Unit: UnitCount, Color: ColorPink, Dimensions: dims},
		{Key: "waf.counted", Label: "카운트", Namespace: NSWAFV2, MetricName: "CountedRequests", Stat: StatSum, Unit: UnitCount, Color: ColorGray, Dimensions: dims},
	}
}

// SpecsWithPrefix picks the specs whose key begins with prefix, so a panel can
// plot CPU without memory sharing its chart. The keys already nest — pod.cpu
// carries pod.cpu.limit with it — so one prefix names a whole resource.
func SpecsWithPrefix(specs []MetricSpec, prefix string) []MetricSpec {
	out := make([]MetricSpec, 0, len(specs))
	for _, s := range specs {
		if strings.HasPrefix(s.Key, prefix) {
			out = append(out, s)
		}
	}
	return out
}

// AllMetrics is every spec the dashboard knows about, used to assert that keys
// are unique and that no metric is read two different ways.
func AllMetrics() []MetricSpec {
	var out []MetricSpec
	for _, group := range [][]MetricSpec{
		TargetGroupMetrics(), PodResourceMetrics(), NodeResourceMetrics(),
		CountMetrics(), PodStatusMetrics(), RDSProxyMetrics(), WAFMetrics(),
	} {
		out = append(out, group...)
	}
	return out
}

// searchValueRe keeps a dimension value to characters that are safe inside the
// double-quoted term of a SEARCH expression.
var searchValueRe = regexp.MustCompile(`^[A-Za-z0-9 ._:/+=@-]+$`)

// SearchExpression renders a CloudWatch metric-math SEARCH for spec, filtered
// by the given dimension values.
//
// Using SEARCH removes the ListMetrics call entirely, and with it the failure
// that eventually took the reference implementation's metric panels offline:
// ListMetrics keeps returning dimensions for pods that no longer exist, so the
// generated query list grew monotonically until it crossed GetMetricData's
// 500-query ceiling and every subsequent call failed validation. A SEARCH is
// one query no matter how many series it matches.
func SearchExpression(spec MetricSpec, filters map[string]string, p Period) (string, error) {
	if spec.Namespace == "" || spec.MetricName == "" {
		return "", fmt.Errorf("metric %q is missing a namespace or name", spec.Key)
	}
	schema := append([]string{spec.Namespace}, spec.Dimensions...)

	terms := []string{fmt.Sprintf("MetricName=%q", spec.MetricName)}
	keys := make([]string, 0, len(filters))
	for k := range filters {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic output, so queries cache and tests compare
	for _, k := range keys {
		v := filters[k]
		if v == "" {
			continue
		}
		if !searchValueRe.MatchString(k) || !searchValueRe.MatchString(v) {
			return "", fmt.Errorf("dimension %s=%q contains characters that cannot be embedded in a SEARCH expression", k, v)
		}
		terms = append(terms, fmt.Sprintf("%s=%q", k, v))
	}

	return fmt.Sprintf("SEARCH('{%s} %s', '%s', %d)",
		strings.Join(schema, ","),
		strings.Join(terms, " "),
		spec.Stat,
		p.Seconds(),
	), nil
}

// queryIDRe matches what GetMetricData accepts as a query identifier.
var queryIDRe = regexp.MustCompile(`^[a-z][a-zA-Z0-9_]*$`)

// QueryID turns a metric key into a valid GetMetricData id.
//
// Results are matched back to specs through this identifier rather than by
// position. The reference implementation recovered an array index by scanning
// the id with Sscanf, discarded the error, and then indexed a slice with the
// result — so an unexpected id wrote data into the wrong series, and an
// out-of-range one panicked the handler.
func QueryID(key string) string {
	var b strings.Builder
	b.WriteByte('q')
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	id := b.String()
	if !queryIDRe.MatchString(id) {
		// Unreachable for any key made of printable characters, but a
		// malformed id would be rejected by CloudWatch rather than silently
		// mismatched, so fall back to something valid.
		return "q"
	}
	return id
}
