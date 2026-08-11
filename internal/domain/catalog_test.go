package domain

import (
	"sort"
	"strings"
	"testing"
)

// One metric read two different ways on two screens is a permanent
// disagreement with nothing in the UI to explain it. The catalog is the single
// place a statistic is chosen, and this test holds it to that.
func TestEachMetricIsReadExactlyOneWay(t *testing.T) {
	type reading struct{ stat, unit string }
	seen := map[string]reading{}

	for _, spec := range AllMetrics() {
		id := spec.Namespace + "/" + spec.MetricName
		// TargetResponseTime is deliberately read at three percentiles, which
		// are distinct statistics of one metric rather than a disagreement.
		id += "@" + spec.Stat

		if prev, ok := seen[id]; ok {
			if prev.unit != string(spec.Unit) {
				t.Errorf("%s is read as %s in one place and %s in another", id, prev.unit, spec.Unit)
			}
		}
		seen[id] = reading{spec.Stat, string(spec.Unit)}
	}
}

func TestMetricKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range AllMetrics() {
		if seen[spec.Key] {
			t.Errorf("duplicate metric key %q; results would overwrite each other", spec.Key)
		}
		seen[spec.Key] = true
	}
}

func TestEveryMetricIsFullySpecified(t *testing.T) {
	for _, spec := range AllMetrics() {
		if spec.Key == "" || spec.Label == "" {
			t.Errorf("metric %+v is missing a key or label", spec)
		}
		if spec.Namespace == "" || spec.MetricName == "" {
			t.Errorf("metric %q is missing a namespace or metric name", spec.Key)
		}
		if spec.Stat == "" {
			t.Errorf("metric %q has no statistic; the choice must not fall to the call site", spec.Key)
		}
		if spec.Unit == UnitNone {
			t.Errorf("metric %q has no unit, so the frontend would have to guess how to format it", spec.Key)
		}
		if len(spec.Dimensions) == 0 {
			t.Errorf("metric %q has no dimensions to search by", spec.Key)
		}
		if spec.Color == "" {
			t.Errorf("metric %q has no colour", spec.Key)
		}
	}
}

// Counters must be summed and utilisations averaged. Summing a utilisation
// produces a number with no meaning, and averaging a counter understates it.
func TestStatisticMatchesTheKindOfMetric(t *testing.T) {
	for _, spec := range AllMetrics() {
		switch spec.Unit {
		case UnitPercent:
			if spec.Stat == StatSum {
				t.Errorf("%s is a percentage summed across the window", spec.Key)
			}
		case UnitCount:
			if strings.HasSuffix(spec.MetricName, "_Count") && spec.Stat != StatSum {
				t.Errorf("%s is a request counter read as %s rather than Sum", spec.Key, spec.Stat)
			}
		}
	}
}

func TestSearchExpression(t *testing.T) {
	spec := MetricSpec{
		Key: "pod.cpu", Namespace: NSContainer, MetricName: "pod_cpu_utilization",
		Stat: StatAvg, Dimensions: []string{"ClusterName", "Namespace", "PodName"},
	}
	got, err := SearchExpression(spec, map[string]string{"ClusterName": "prod", "Namespace": "default"}, Period1m)
	if err != nil {
		t.Fatal(err)
	}
	want := `SEARCH('{ContainerInsights,ClusterName,Namespace,PodName} MetricName="pod_cpu_utilization" ClusterName="prod" Namespace="default"', 'Average', 60)`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSearchExpressionIsDeterministic(t *testing.T) {
	spec := MetricSpec{Key: "x", Namespace: NSContainer, MetricName: "m", Stat: StatAvg, Dimensions: []string{"A", "B", "C"}}
	filters := map[string]string{"C": "3", "A": "1", "B": "2"}
	first, err := SearchExpression(spec, filters, Period5m)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		got, err := SearchExpression(spec, filters, Period5m)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("expression varies between calls, so results would never cache:\n%s\n%s", first, got)
		}
	}
}

func TestSearchExpressionRejectsUnsafeDimensionValues(t *testing.T) {
	spec := MetricSpec{Key: "x", Namespace: NSContainer, MetricName: "m", Stat: StatAvg, Dimensions: []string{"ClusterName"}}
	bad := []string{
		`prod" OR MetricName="something_else`,
		"prod'\n",
		`prod"`,
		"prod\ttab",
	}
	for _, v := range bad {
		if got, err := SearchExpression(spec, map[string]string{"ClusterName": v}, Period1m); err == nil {
			t.Errorf("accepted unsafe dimension value %q -> %s", v, got)
		}
	}

	// Real cluster and proxy names contain these and must keep working.
	for _, v := range []string{"prod-cluster", "my_proxy", "team/app", "a.b.c", "arn:aws:elasticloadbalancing:x"} {
		if _, err := SearchExpression(spec, map[string]string{"ClusterName": v}, Period1m); err != nil {
			t.Errorf("rejected a legitimate value %q: %v", v, err)
		}
	}
}

func TestSearchExpressionOmitsEmptyFilters(t *testing.T) {
	spec := MetricSpec{Key: "x", Namespace: NSRDS, MetricName: "DatabaseConnections", Stat: StatAvg, Dimensions: []string{"ProxyName"}}
	got, err := SearchExpression(spec, map[string]string{"ProxyName": ""}, Period1m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, `ProxyName=""`) {
		t.Errorf("an empty filter was emitted, which matches nothing:\n%s", got)
	}
	if !strings.Contains(got, `MetricName="DatabaseConnections"`) {
		t.Errorf("metric name missing:\n%s", got)
	}
}

func TestSearchExpressionPeriodMatchesTheWindow(t *testing.T) {
	spec := MetricSpec{Key: "x", Namespace: NSContainer, MetricName: "m", Stat: StatAvg, Dimensions: []string{"A"}}
	for _, tc := range []struct {
		p    Period
		want string
	}{
		{Period1m, ", 60)"},
		{Period5m, ", 300)"},
		{Period10m, ", 600)"},
		{Period1h, ", 3600)"},
	} {
		got, err := SearchExpression(spec, nil, tc.p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(got, tc.want) {
			t.Errorf("period %s produced %q, want suffix %q", tc.p, got, tc.want)
		}
	}
}

func TestSearchExpressionRejectsIncompleteSpecs(t *testing.T) {
	if _, err := SearchExpression(MetricSpec{Key: "x", MetricName: "m"}, nil, Period1m); err == nil {
		t.Error("accepted a spec with no namespace")
	}
	if _, err := SearchExpression(MetricSpec{Key: "x", Namespace: NSRDS}, nil, Period1m); err == nil {
		t.Error("accepted a spec with no metric name")
	}
}

// Every catalog entry must render, or a panel silently loses a series.
func TestEveryCatalogEntryProducesAValidSearch(t *testing.T) {
	filters := map[string]string{
		"ClusterName":  "prod",
		"Namespace":    "default",
		"ProxyName":    "app-proxy",
		"WebACL":       "skills-waf",
		"LoadBalancer": "app/my-alb/abc123",
		"TargetGroup":  "targetgroup/k8s-default-product/def456",
	}
	for _, spec := range AllMetrics() {
		got, err := SearchExpression(spec, filters, Period5m)
		if err != nil {
			t.Errorf("metric %q: %v", spec.Key, err)
			continue
		}
		if !strings.HasPrefix(got, "SEARCH('{"+spec.Namespace) {
			t.Errorf("metric %q produced a malformed expression: %s", spec.Key, got)
		}
	}
}

func TestQueryIDIsAcceptableToCloudWatch(t *testing.T) {
	for _, spec := range AllMetrics() {
		id := QueryID(spec.Key)
		if !queryIDRe.MatchString(id) {
			t.Errorf("QueryID(%q) = %q, which CloudWatch would reject", spec.Key, id)
		}
	}
}

// Results are matched back to specs by identifier, so two specs must never
// collapse onto one id — that is how data lands in the wrong series.
func TestQueryIDsAreUniqueAcrossTheCatalog(t *testing.T) {
	seen := map[string]string{}
	for _, spec := range AllMetrics() {
		id := QueryID(spec.Key)
		if prev, ok := seen[id]; ok {
			t.Errorf("metrics %q and %q both map to id %q", prev, spec.Key, id)
		}
		seen[id] = spec.Key
	}
}

func TestQueryIDHandlesAwkwardKeys(t *testing.T) {
	tests := []struct{ in, want string }{
		{"pod.cpu", "qpod_cpu"},
		{"tg.5xx", "qtg_5xx"},
		{"waf.byHeader.user-agent", "qwaf_byHeader_user_agent"},
	}
	for _, tc := range tests {
		if got := QueryID(tc.in); got != tc.want {
			t.Errorf("QueryID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A SEARCH schema is matched as a *set* of dimension names. Name a set that
// Container Insights does not publish and the expression is still valid, still
// costs a query, and matches nothing — the panel renders an empty chart, which
// on this dashboard is indistinguishable from a quiet cluster. That is exactly
// how the node resource and pod status panels shipped blank.
//
// The right-hand column is the dimension set this dashboard leans on, copied
// from the Container Insights metric tables:
// https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/Container-Insights-metrics-EKS.html
// https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/Container-Insights-metrics-enhanced-EKS.html
func TestContainerInsightsDimensionsArePublishedSets(t *testing.T) {
	published := map[string]string{
		"pod_cpu_utilization":                   "ClusterName,Namespace,PodName",
		"pod_memory_utilization":                "ClusterName,Namespace,PodName",
		"pod_cpu_utilization_over_pod_limit":    "ClusterName,Namespace,PodName",
		"pod_memory_utilization_over_pod_limit": "ClusterName,Namespace,PodName",
		"node_cpu_utilization":                  "ClusterName,InstanceId,NodeName",
		"node_memory_utilization":               "ClusterName,InstanceId,NodeName",
		"node_filesystem_utilization":           "ClusterName,InstanceId,NodeName",
		"service_number_of_running_pods":        "ClusterName,Namespace,Service",
		"cluster_node_count":                    "ClusterName",
		"cluster_failed_node_count":             "ClusterName",
		"pod_status_running":                    "ClusterName,Namespace,PodName",
		"pod_status_pending":                    "ClusterName,Namespace,PodName",
		"pod_status_failed":                     "ClusterName,Namespace,PodName",
		"pod_number_of_container_restarts":      "ClusterName,Namespace,PodName",
	}

	for _, spec := range AllMetrics() {
		if spec.Namespace != NSContainer {
			continue
		}
		want, ok := published[spec.MetricName]
		if !ok {
			t.Errorf("%s reads %s but this test does not record which dimension sets it is published at; look it up before trusting the panel",
				spec.Key, spec.MetricName)
			continue
		}
		dims := append([]string(nil), spec.Dimensions...)
		sort.Strings(dims)
		if got := strings.Join(dims, ","); got != want {
			t.Errorf("%s searches {%s} but %s is published at {%s}; the SEARCH matches nothing and the panel renders empty",
				spec.Key, got, spec.MetricName, want)
		}
	}
}

func TestPodStatusCoversTheRequestedStates(t *testing.T) {
	labels := map[string]bool{}
	for _, spec := range PodStatusMetrics() {
		labels[spec.Label] = true
	}
	for _, want := range []string{"Running", "Pending"} {
		if !labels[want] {
			t.Errorf("pod status panel has no %q series", want)
		}
	}
	// Crash and OOM have no direct Container Insights metric; restarts are the
	// available proxy and the panel must at least carry that.
	if !labels["컨테이너 재시작"] {
		t.Error("pod status panel has no restart series, leaving crash loops invisible")
	}
}

// The palette is the only thing separating one pod's line from another's on a
// chart that draws twenty of them, so a repeat inside one cycle is a pair of
// lines nobody can tell apart.
func TestSubjectPaletteHandsOutDistinctColours(t *testing.T) {
	seen := map[string]bool{}
	for i, c := range SubjectPalette {
		if seen[c] {
			t.Errorf("SubjectPalette[%d] repeats %q", i, c)
		}
		seen[c] = true
	}
	if seen[ColorGray] {
		// Reserved: sumSeries spends it on totals, and a pod drawn in the same
		// grey as the total line reads as the total.
		t.Error("the palette hands out systemGray, which already means 'total'")
	}
	if len(SubjectPalette) < 8 {
		t.Errorf("palette holds %d colours; a namespace of that many pods would recycle immediately", len(SubjectPalette))
	}
}

// Recycling past the end is fine — running off it is a panic on a live panel.
func TestSubjectColorCyclesRatherThanRunningOut(t *testing.T) {
	n := len(SubjectPalette)
	if got, want := SubjectColor(n), SubjectPalette[0]; got != want {
		t.Errorf("SubjectColor(%d) = %q, want it to cycle back to %q", n, got, want)
	}
	if got := SubjectColor(n*3 + 2); got != SubjectPalette[2] {
		t.Errorf("SubjectColor wrapped to %q on the third cycle", got)
	}
	if got := SubjectColor(-1); got == "" {
		t.Error("SubjectColor(-1) returned no colour")
	}
}
