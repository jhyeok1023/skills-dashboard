package domain

import (
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
