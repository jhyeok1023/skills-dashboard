package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
	waftypes "github.com/aws/aws-sdk-go-v2/service/wafv2/types"

	"github.com/jhyeok1023/skills-dashboard/internal/awsx"
	"github.com/jhyeok1023/skills-dashboard/internal/config"
	"github.com/jhyeok1023/skills-dashboard/internal/domain"
)

var testNow = time.Date(2026, 8, 10, 10, 3, 47, 0, time.UTC)

// stubMetrics answers any GetMetricData with one deterministic series per
// query, so the same request always produces the same numbers.
type stubMetrics struct {
	mu    sync.Mutex
	calls int
	// exprs keeps every SEARCH that was issued, so a test can assert on the
	// dimensions a panel actually asked CloudWatch to filter by.
	exprs []string
}

// expressions returns the SEARCH strings seen so far.
func (s *stubMetrics) expressions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.exprs...)
}

func (s *stubMetrics) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	s.mu.Lock()
	s.calls++
	for _, q := range in.MetricDataQueries {
		s.exprs = append(s.exprs, aws.ToString(q.Expression))
	}
	s.mu.Unlock()

	start := aws.ToTime(in.StartTime)
	end := aws.ToTime(in.EndTime)
	var out cloudwatch.GetMetricDataOutput
	for qi, q := range in.MetricDataQueries {
		id := aws.ToString(q.Id)
		period := time.Duration(aws.ToInt32(q.Period)) * time.Second
		if period <= 0 {
			period = 5 * time.Minute
		}
		// Two labelled series per query, as a SEARCH would return.
		for li, label := range []string{"alpha", "beta"} {
			var ts []time.Time
			var vals []float64
			i := 0
			for t := start; t.Before(end); t = t.Add(period) {
				ts = append(ts, t)
				vals = append(vals, float64((qi+1)*10+li*3+i%7))
				i++
			}
			out.MetricDataResults = append(out.MetricDataResults, cwtypes.MetricDataResult{
				Id:         aws.String(id),
				Label:      aws.String(label),
				Timestamps: ts,
				Values:     vals,
			})
		}
	}
	return &out, nil
}

// stubLogs recognises each query by its text and replays fixed rows.
type stubLogs struct {
	mu      sync.Mutex
	queries map[string]string // queryId -> kind
	next    int
	starts  []string
	// groups records the log group each query was aimed at, which is how the
	// tests tell a query sent to the working region from one sent to us-east-1.
	groups []string
	// name labels the stub in failure messages.
	name string
	// logGroups is what this region holds. The two regions hold different
	// groups, which is the whole reason the WAF panels need their own client.
	logGroups []string
}

func newStubLogs(name string, logGroups ...string) *stubLogs {
	return &stubLogs{queries: map[string]string{}, name: name, logGroups: logGroups}
}

// startedGroups is a snapshot of every log group this stub was queried against.
func (s *stubLogs) startedGroups() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.groups...)
}

func (s *stubLogs) startedQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.starts...)
}

func classify(q string) string {
	switch {
	case strings.Contains(q, "latencySamples"):
		return "traffic"
	case strings.Contains(q, "not in [200, 201]") && strings.Contains(q, "sort @timestamp desc"):
		return "badStatusList"
	case strings.Contains(q, "not in [200, 201]"):
		return "badStatusSeries"
	case strings.Contains(q, "as t, level"):
		return "errorSeries"
	case strings.Contains(q, "as level") && strings.Contains(q, "sort @timestamp desc"):
		return "errorList"
	case strings.Contains(q, "as t, action"):
		return "wafAction"
	case strings.Contains(q, "httpMethod as method"):
		return "wafMethod"
	case strings.Contains(q, "uri as uri, httpRequest.args"):
		return "wafPath"
	case strings.Contains(q, "terminatingRuleId as rule") && strings.Contains(q, "sort @timestamp desc"):
		return "wafBlockedList"
	case strings.Contains(q, "terminatingRuleId as rule"):
		return "wafBlocked"
	case strings.Contains(q, "parse @message"):
		return "wafHeader"
	default:
		return "unknown"
	}
}

func (s *stubLogs) StartQuery(_ context.Context, in *cloudwatchlogs.StartQueryInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := fmt.Sprintf("q%d", s.next)
	s.queries[id] = classify(aws.ToString(in.QueryString))
	s.starts = append(s.starts, aws.ToString(in.QueryString))
	s.groups = append(s.groups, in.LogGroupNames...)
	return &cloudwatchlogs.StartQueryOutput{QueryId: aws.String(id)}, nil
}

func (s *stubLogs) GetQueryResults(_ context.Context, in *cloudwatchlogs.GetQueryResultsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error) {
	s.mu.Lock()
	kind := s.queries[aws.ToString(in.QueryId)]
	s.mu.Unlock()
	return &cloudwatchlogs.GetQueryResultsOutput{
		Status:     logtypes.QueryStatusComplete,
		Results:    rowsFor(kind),
		Statistics: &logtypes.QueryStatistics{BytesScanned: 2048, RecordsMatched: 42},
	}, nil
}

func (s *stubLogs) StopQuery(_ context.Context, _ *cloudwatchlogs.StopQueryInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StopQueryOutput, error) {
	return &cloudwatchlogs.StopQueryOutput{}, nil
}

func (s *stubLogs) DescribeLogGroups(_ context.Context, in *cloudwatchlogs.DescribeLogGroupsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	prefix := aws.ToString(in.LogGroupNamePrefix)
	var out cloudwatchlogs.DescribeLogGroupsOutput
	for _, name := range s.logGroups {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		out.LogGroups = append(out.LogGroups, logtypes.LogGroup{LogGroupName: aws.String(name)})
	}
	return &out, nil
}

func f(name, value string) logtypes.ResultField {
	return logtypes.ResultField{Field: aws.String(name), Value: aws.String(value)}
}

// bucket renders a bin() timestamp inside the test window.
func bucket(i int) string {
	w, _ := domain.NewWindow(testNow, domain.Range1h, domain.Period5m)
	return time.Unix(w.Timestamps()[i], 0).UTC().Format("2006-01-02 15:04:05.000")
}

func rowsFor(kind string) [][]logtypes.ResultField {
	switch kind {
	case "traffic":
		return [][]logtypes.ResultField{
			{f("t", bucket(0)), f("app", "api"), f("requests", "100"), f("latencySamples", "90"), f("avg", "12"), f("p50", "10"), f("p90", "40"), f("p99", "120")},
			{f("t", bucket(1)), f("app", "api"), f("requests", "150"), f("latencySamples", "140"), f("avg", "15"), f("p50", "11"), f("p90", "45"), f("p99", "180")},
			{f("t", bucket(1)), f("app", "worker"), f("requests", "20"), f("latencySamples", "20"), f("avg", "5"), f("p50", "4"), f("p90", "8"), f("p99", "30")},
		}
	case "badStatusSeries":
		// 1284 non-OK responses in total, far more than the list can carry.
		return [][]logtypes.ResultField{
			{f("t", bucket(0)), f("status", "503"), f("path", "/healthcheck"), f("n", "800")},
			{f("t", bucket(1)), f("status", "404"), f("path", "/v1/user"), f("n", "484")},
		}
	case "badStatusList":
		rows := make([][]logtypes.ResultField, 300)
		for i := range rows {
			rows[i] = []logtypes.ResultField{
				f("@timestamp", "2026-08-10 09:30:00.000"),
				f("pod", "api-5cbb6d585d-cr4rd"), f("app", "api"),
				f("method", "GET"), f("path", "/healthcheck"),
				f("status", "503"), f("latencyMs", "12.5"), f("clientIp", "10.0.3.123"),
			}
		}
		return rows
	case "errorSeries":
		return [][]logtypes.ResultField{
			{f("t", bucket(0)), f("level", "error"), f("n", "412")},
			{f("t", bucket(1)), f("level", "warn"), f("n", "77")},
		}
	case "errorList":
		rows := make([][]logtypes.ResultField, 300)
		for i := range rows {
			rows[i] = []logtypes.ResultField{
				f("@timestamp", "2026-08-10 09:31:00.000"),
				f("pod", "user-1"), f("container", "user"),
				f("log", "WARN: connection pool exhausted"),
			}
		}
		return rows
	case "wafAction":
		return [][]logtypes.ResultField{
			{f("t", bucket(0)), f("action", "ALLOW"), f("n", "900")},
			{f("t", bucket(0)), f("action", "BLOCK"), f("n", "100")},
			{f("t", bucket(2)), f("action", "ALLOW"), f("n", "700")},
		}
	case "wafMethod":
		return [][]logtypes.ResultField{
			{f("method", "GET"), f("n", "1500")},
			{f("method", "POST"), f("n", "200")},
		}
	case "wafPath":
		return [][]logtypes.ResultField{
			{f("uri", "/v1/user"), f("args", "email=a@b.c"), f("n", "80")},
			{f("uri", "/healthcheck"), f("args", ""), f("n", "1200")},
		}
	case "wafBlocked":
		return [][]logtypes.ResultField{
			{f("rule", "sqli"), f("clientIp", "1.2.3.4"), f("country", "KR"), f("n", "60")},
			{f("rule", "xss"), f("clientIp", "5.6.7.8"), f("country", "US"), f("n", "40")},
		}
	case "wafBlockedList":
		return [][]logtypes.ResultField{
			{f("@timestamp", "2026-08-10 09:40:00.000"), f("rule", "sqli"), f("clientIp", "1.2.3.4"), f("country", "KR"), f("method", "GET"), f("uri", "/v1/user"), f("args", "email=a@b.c")},
		}
	case "wafHeader":
		return [][]logtypes.ResultField{
			{f("value", "Mozilla/5.0"), f("n", "1100")},
			{f("value", "curl/8"), f("n", "60")},
		}
	default:
		return nil
	}
}

// stubELB answers the load balancer discovery. The ARN is the point: what the
// endpoint hands back must be the CloudWatch dimension, not this.
type stubELB struct{}

const testLBArn = "arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:loadbalancer/app/my-alb/50dc6c495c0c9188"

func (stubELB) DescribeLoadBalancers(context.Context, *elasticloadbalancingv2.DescribeLoadBalancersInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
	return &elasticloadbalancingv2.DescribeLoadBalancersOutput{
		LoadBalancers: []elbtypes.LoadBalancer{{
			LoadBalancerArn:  aws.String(testLBArn),
			LoadBalancerName: aws.String("my-alb"),
			DNSName:          aws.String("my-alb-123.ap-northeast-2.elb.amazonaws.com"),
			Type:             elbtypes.LoadBalancerTypeEnumApplication,
			Scheme:           elbtypes.LoadBalancerSchemeEnumInternetFacing,
		}},
	}, nil
}

func (stubELB) DescribeTargetGroups(context.Context, *elasticloadbalancingv2.DescribeTargetGroupsInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	return &elasticloadbalancingv2.DescribeTargetGroupsOutput{}, nil
}

// stubRDS and stubWAF answer the two discoveries that had no test service to
// run against at all: Clients.RDS, Clients.WAF and Clients.WAFGlobal were left
// nil, so calling either endpoint here reached a method on a nil interface,
// panicked, and was flattened into a 500 by recoverPanics. Both are exactly the
// endpoints an operator reported as returning nothing.
type stubRDS struct {
	err error
}

func (s stubRDS) DescribeDBProxies(context.Context, *rds.DescribeDBProxiesInput, ...func(*rds.Options)) (*rds.DescribeDBProxiesOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &rds.DescribeDBProxiesOutput{DBProxies: []rdstypes.DBProxy{{
		DBProxyName:  aws.String("app-proxy"),
		DBProxyArn:   aws.String("arn:aws:rds:ap-northeast-2:123456789012:db-proxy/app-proxy"),
		EngineFamily: aws.String("POSTGRESQL"),
		Status:       rdstypes.DBProxyStatusAvailable,
	}}}, nil
}

// stubWAF answers per scope, so a test can deny one scope and keep the other.
type stubWAF struct {
	scope waftypes.Scope
	name  string
	err   error
}

func (s stubWAF) ListWebACLs(_ context.Context, in *wafv2.ListWebACLsInput, _ ...func(*wafv2.Options)) (*wafv2.ListWebACLsOutput, error) {
	if in.Scope != s.scope {
		return nil, fmt.Errorf("AccessDeniedException: not permitted for scope %s", in.Scope)
	}
	if s.err != nil {
		return nil, s.err
	}
	return &wafv2.ListWebACLsOutput{WebACLs: []waftypes.WebACLSummary{{
		Name: aws.String(s.name),
		Id:   aws.String("acl-" + s.name),
		ARN:  aws.String("arn:aws:wafv2:ap-northeast-2:123456789012:regional/webacl/" + s.name + "/1"),
	}}}, nil
}

// stubEKS supplies the node group scaling limits, the only source for the
// minimum and maximum node counts.
type stubEKS struct{}

func (stubEKS) ListClusters(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	return &eks.ListClustersOutput{Clusters: []string{"prod"}}, nil
}

func (stubEKS) ListNodegroups(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	return &eks.ListNodegroupsOutput{Nodegroups: []string{"general"}}, nil
}

func (stubEKS) DescribeNodegroup(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{
		ScalingConfig: &ekstypes.NodegroupScalingConfig{
			MinSize: aws.Int32(2), MaxSize: aws.Int32(9), DesiredSize: aws.Int32(4),
		},
	}}, nil
}

func newTestService(t *testing.T) (*Service, http.Handler) {
	t.Helper()

	store, err := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.Get()
	cfg.ClusterName = "prod"
	cfg.Namespace = "default"
	cfg.LoadBalancer = "app/my-alb/abc"
	cfg.TargetGroups = []string{"targetgroup/k8s-default-product-d6d507c878/def"}
	cfg.RDSProxies = []string{"app-proxy"}
	cfg.WebACLs = []string{"skills-waf"}
	cfg.WAFLogGroup = "aws-waf-logs-demo"
	if err := store.Set(cfg); err != nil {
		t.Fatal(err)
	}

	// Two regions, two sets of log groups. The WAF group exists only in
	// us-east-1, so a WAF query that goes to the working region finds nothing —
	// which is exactly the failure the split runner exists to prevent.
	logs := newStubLogs("ap-northeast-2", "/aws/containerinsights/prod/application")
	logsGlobal := newStubLogs("us-east-1", "aws-waf-logs-demo")
	metrics := &stubMetrics{}
	svc := &Service{
		Store: store,
		Now:   func() time.Time { return testNow },
		Cache: &awsx.Cache{TTL: time.Minute},
		Clients: &awsx.Clients{
			Region: "ap-northeast-2", WAFRegion: "us-east-1",
			CW: metrics, CWGlobal: metrics, Logs: logs, LogsGlobal: logsGlobal,
			EKS: stubEKS{}, ELB: stubELB{}, RDS: stubRDS{},
			WAF:       stubWAF{scope: waftypes.ScopeRegional, name: "skills-waf"},
			WAFGlobal: stubWAF{scope: waftypes.ScopeCloudfront, name: "edge-waf"},
		},
		Insights:       &awsx.InsightsRunner{API: logs, Concurrency: 6, PollInterval: time.Millisecond},
		InsightsGlobal: &awsx.InsightsRunner{API: logsGlobal, Concurrency: 6, PollInterval: time.Millisecond},
		Metrics:        &awsx.MetricFetcher{},
	}
	return svc, svc.Handler()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func decodePayload(t *testing.T, rec *httptest.ResponseRecorder) domain.Payload {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var p domain.Payload
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	return p
}

func findPanel(t *testing.T, p domain.Payload, id string) *domain.Panel {
	t.Helper()
	for _, panel := range p.Panels {
		if panel.ID == id {
			return panel
		}
	}
	t.Fatalf("payload has no panel %q", id)
	return nil
}

// This is the regression line for the defect the rewrite exists to fix: a
// number shown on an overview disagreeing with the same number on its detail
// view. Both views are served by the same builder, and here we prove that a
// panel fetched alone is byte-for-byte the panel inside a page.
func TestPanelIsIdenticalWhetherFetchedAloneOrInsideAPage(t *testing.T) {
	_, h := newTestService(t)

	cases := []struct{ panel, page string }{
		{"pod-latency", "overview"},
		{"pod-latency", "pod-logs"},
		{"pod-status-codes", "overview"},
		{"pod-status-codes", "pod-logs"},
		{"targetgroup", "overview"},
		{"counts", "overview"},
		{"counts", "kubernetes"},
		{"pod-status", "overview"},
		{"pod-status", "kubernetes"},
		{"waf-traffic", "overview"},
		{"waf-traffic", "waf"},
		{"pod-errors", "pod-logs"},
		{"rds-proxy", "database"},
	}

	for _, tc := range cases {
		alone := findPanel(t, decodePayload(t, get(t, h, "/api/panel/"+tc.panel+"?range=1h&period=5m")), tc.panel)
		inPage := findPanel(t, decodePayload(t, get(t, h, "/api/page/"+tc.page+"?range=1h&period=5m")), tc.panel)

		a, err := json.Marshal(alone)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(inPage)
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Errorf("panel %q differs between /api/panel and /api/page/%s\nalone:  %s\nin page: %s",
				tc.panel, tc.page, a, b)
		}
	}
}

// Every panel in a response is plotted against one axis, so two panels cannot
// describe different spans.
func TestPageCarriesOneWindowForEveryPanel(t *testing.T) {
	_, h := newTestService(t)
	for _, page := range []string{"overview", "pod-logs", "waf", "kubernetes", "database", "targetgroup"} {
		p := decodePayload(t, get(t, h, "/api/page/"+page+"?range=4h&period=5m"))
		if p.Window.Period != 300 {
			t.Errorf("page %s: period = %d, want 300", page, p.Window.Period)
		}
		if got := len(p.Window.Timestamps); got != 48 {
			t.Errorf("page %s: %d timestamps, want 48", page, got)
		}
		if len(p.Panels) == 0 {
			t.Errorf("page %s rendered no panels", page)
		}
		for _, panel := range p.Panels {
			for _, s := range panel.Series {
				if len(s.Values) != len(p.Window.Timestamps) {
					t.Errorf("page %s panel %s series %q has %d values against a %d-bucket axis",
						page, panel.ID, s.Label, len(s.Values), len(p.Window.Timestamps))
				}
			}
		}
	}
}

// The list is capped at 300; the true count is 1284. Reporting the list's
// length as the total is the bug this guards.
func TestTableTotalIsCountedIndependentlyOfTheRowsShown(t *testing.T) {
	_, h := newTestService(t)
	panel := findPanel(t, decodePayload(t, get(t, h, "/api/panel/pod-status-codes?range=1h&period=5m")), "pod-status-codes")

	if panel.Table == nil {
		t.Fatal("no table")
	}
	if len(panel.Table.Rows) != 300 {
		t.Fatalf("got %d rows, want the capped 300", len(panel.Table.Rows))
	}
	if panel.Table.Total != 1284 {
		t.Errorf("Total = %d, want the aggregate's 1284", panel.Table.Total)
	}
	if !panel.Table.Truncated {
		t.Error("Truncated = false even though 1284 > 300")
	}

	// The headline stat must agree with the aggregate, not with the list.
	var stat *domain.Stat
	for i := range panel.Stats {
		if panel.Stats[i].Key == "pod.badStatus.total" {
			stat = &panel.Stats[i]
		}
	}
	if stat == nil {
		t.Fatal("no total stat")
	}
	if stat.Value == nil || *stat.Value != 1284 {
		t.Errorf("headline = %v, want 1284", stat.Value)
	}
	if stat.Basis == "" {
		t.Error("the stat does not say what population it counted")
	}
}

func TestErrorPanelTotalIsNotTheCappedListLength(t *testing.T) {
	_, h := newTestService(t)
	panel := findPanel(t, decodePayload(t, get(t, h, "/api/panel/pod-errors?range=1h&period=5m")), "pod-errors")

	if len(panel.Table.Rows) != 300 {
		t.Fatalf("got %d rows", len(panel.Table.Rows))
	}
	// 412 errors + 77 warnings.
	if panel.Table.Total != 489 {
		t.Errorf("Total = %d, want 489", panel.Table.Total)
	}
	stats := map[string]float64{}
	for _, s := range panel.Stats {
		if s.Value != nil {
			stats[s.Key] = *s.Value
		}
	}
	if stats["pod.error.total"] != 412 {
		t.Errorf("ERROR total = %v, want 412", stats["pod.error.total"])
	}
	if stats["pod.warn.total"] != 77 {
		t.Errorf("WARN total = %v, want 77", stats["pod.warn.total"])
	}
}

// Two counts of "requests" existed in the reference implementation under one
// label. Here they are separate stats, each naming its population.
func TestLatencyPanelSeparatesItsTwoRequestPopulations(t *testing.T) {
	_, h := newTestService(t)
	panel := findPanel(t, decodePayload(t, get(t, h, "/api/panel/pod-latency?range=1h&period=5m")), "pod-latency")

	byKey := map[string]domain.Stat{}
	for _, s := range panel.Stats {
		byKey[s.Key] = s
	}

	requests, ok := byKey["pod.requests.total"]
	if !ok {
		t.Fatal("no request total")
	}
	samples, ok := byKey["pod.latencySamples.total"]
	if !ok {
		t.Fatal("no latency sample total")
	}
	if requests.Value == nil || *requests.Value != 270 {
		t.Errorf("requests = %v, want 270", requests.Value)
	}
	if samples.Value == nil || *samples.Value != 250 {
		t.Errorf("latency samples = %v, want 250", samples.Value)
	}
	if requests.Label == samples.Label {
		t.Error("two different populations share one label, which is exactly the confusion being fixed")
	}
	if requests.Basis == "" || samples.Basis == "" || requests.Basis == samples.Basis {
		t.Errorf("the two stats do not distinguish their populations: %q vs %q", requests.Basis, samples.Basis)
	}

	if v := byKey["pod.p99.max"].Value; v == nil || *v != 180 {
		t.Errorf("max p99 = %v, want 180", v)
	}
}

// Health-check traffic is dropped before aggregation, so every count on the
// panel already has it removed.
func TestHealthCheckPathsAreExcludedFromPodLogQueries(t *testing.T) {
	svc, h := newTestService(t)

	if rec := get(t, h, "/api/page/pod-logs?range=1h&period=5m"); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}

	logs := svc.Clients.Logs.(*stubLogs)
	logs.mu.Lock()
	started := append([]string(nil), logs.starts...)
	logs.mu.Unlock()

	if len(started) == 0 {
		t.Fatal("no queries were issued")
	}
	for _, q := range started {
		if !strings.Contains(q, "path not in ['/health', '/healthcheck']") {
			t.Errorf("a pod-log query does not exclude probe traffic:\n%s", q)
		}
	}
}

func TestPodLogNamespaceCanOverrideTheStoredValue(t *testing.T) {
	svc, h := newTestService(t)
	cfg := svc.Store.Get()
	cfg.Namespace = "stored"
	cfg.LogFormat.Namespace = "stale-log-setting"
	if err := svc.Store.Set(cfg); err != nil {
		t.Fatal(err)
	}

	if rec := get(t, h, "/api/page/pod-logs?range=1h&period=5m&namespace=payments"); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	started := svc.Clients.Logs.(*stubLogs).startedQueries()
	if len(started) == 0 {
		t.Fatal("no pod-log queries were issued")
	}
	for _, q := range started {
		if !strings.Contains(q, "kubernetes.namespace_name = 'payments'") {
			t.Errorf("query did not use the page namespace:\n%s", q)
		}
		if strings.Contains(q, "stale-log-setting") {
			t.Errorf("query used the retired log-format namespace:\n%s", q)
		}
	}
}

func TestPodLogNamespaceCanSelectAll(t *testing.T) {
	svc, h := newTestService(t)
	if rec := get(t, h, "/api/page/pod-logs?range=1h&period=5m&namespace=%2A"); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	for _, q := range svc.Clients.Logs.(*stubLogs).startedQueries() {
		if strings.Contains(q, "kubernetes.namespace_name =") {
			t.Errorf("all-namespace query still has a namespace filter:\n%s", q)
		}
	}
}

func TestPodLogNamespaceRejectsAnInvalidName(t *testing.T) {
	_, h := newTestService(t)
	rec := get(t, h, "/api/page/pod-logs?namespace=Not_A_Namespace")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

// A count that silently drops part of the traffic is worse than one that is
// merely wrong: nothing on screen hints that it happened.
func TestStatsSayThatHealthChecksWereExcluded(t *testing.T) {
	_, h := newTestService(t)
	payload := decodePayload(t, get(t, h, "/api/page/pod-logs?range=1h&period=5m"))

	checked := 0
	for _, panel := range payload.Panels {
		for _, s := range panel.Stats {
			if s.Key == "insights.bytesScanned" {
				continue
			}
			if !strings.Contains(s.Basis, "/health") {
				t.Errorf("stat %q does not mention the excluded paths: %q", s.Key, s.Basis)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no stats were examined")
	}
}

func TestExclusionCanBeClearedFromTheSettings(t *testing.T) {
	svc, h := newTestService(t)
	cfg := svc.Store.Get()
	cfg.LogFormat.ExcludePaths = nil
	if err := svc.Store.Set(cfg); err != nil {
		t.Fatal(err)
	}

	if got := svc.Store.Get().LogFormat.ExcludePaths; len(got) != 0 {
		t.Fatalf("clearing the list was undone by defaults: %v", got)
	}

	logs := svc.Clients.Logs.(*stubLogs)
	logs.mu.Lock()
	logs.starts = nil
	logs.mu.Unlock()

	if rec := get(t, h, "/api/page/pod-logs?range=1h&period=5m"); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	logs.mu.Lock()
	started := append([]string(nil), logs.starts...)
	logs.mu.Unlock()

	for _, q := range started {
		if strings.Contains(q, "'/health'") {
			t.Errorf("probe traffic was still filtered after the list was cleared:\n%s", q)
		}
	}
}

func TestLogFormatPreviewFlagsAnExcludedPath(t *testing.T) {
	_, h := newTestService(t)
	sample := `{"time":"2026-08-10T07:12:04Z","log":"x","log_processed":{"app":"api","latency_ms":2,"method":"GET","path":"/healthcheck","status":200},"kubernetes":{"namespace_name":"default","container_name":"api"}}`

	body, err := json.Marshal(map[string]any{"sample": sample})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/logfmt/preview", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var resp logFormatPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Excluded {
		t.Error("a /healthcheck line was not reported as excluded")
	}
	if resp.Suggestion == "" {
		t.Error("nothing explained why the line will not appear in any panel")
	}

	// An ordinary path is not flagged.
	body, err = json.Marshal(map[string]any{
		"sample": strings.Replace(sample, "/healthcheck", "/v1/user", 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/logfmt/preview", strings.NewReader(string(body))))
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Excluded {
		t.Error("/v1/user was reported as excluded")
	}
}

func TestRangeBeyondFourHoursIsRejected(t *testing.T) {
	_, h := newTestService(t)
	for _, q := range []string{"?range=8h", "?range=24h", "?range=6h&period=5m"} {
		rec := get(t, h, "/api/page/overview"+q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, rec.Code)
		}
	}
	for _, q := range []string{"?range=15m&period=10m", "?range=1h&period=1h"} {
		rec := get(t, h, "/api/page/overview"+q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 for an unusable bucket count", q, rec.Code)
		}
	}
}

func TestMetaNeverOffersACombinationTheServerRejects(t *testing.T) {
	_, h := newTestService(t)
	rec := get(t, h, "/api/meta")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var meta metaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.MaxRangeSeconds != 4*60*60 {
		t.Errorf("MaxRangeSeconds = %d, want 14400", meta.MaxRangeSeconds)
	}
	if len(meta.Ranges) == 0 {
		t.Fatal("no ranges offered")
	}
	for _, r := range meta.Ranges {
		if r.Seconds > meta.MaxRangeSeconds {
			t.Errorf("range %s exceeds the advertised maximum", r.Range)
		}
		for _, p := range r.Periods {
			rec := get(t, h, "/api/page/overview?range="+r.Range+"&period="+p)
			if rec.Code != http.StatusOK {
				t.Errorf("meta offers %s/%s but the server answers %d", r.Range, p, rec.Code)
			}
		}
	}
}

// A value the loader discarded leaves a panel empty, and an empty panel looks
// exactly like a resource with no traffic. The notice is how the settings page
// gets to say which field went missing and why.
func TestConfigNoticesReachTheSettingsPage(t *testing.T) {
	svc, h := newTestService(t)
	svc.ConfigNotices = []string{`loadBalancer "my-alb"는 ... → 이 값을 비웠습니다.`}
	h = svc.Handler()

	rec := get(t, h, "/api/meta")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var meta metaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Notices) != 1 || !strings.Contains(meta.Notices[0], "my-alb") {
		t.Errorf("notices = %v, want the discarded value explained", meta.Notices)
	}

	// They ride on meta rather than on the config, because the settings page
	// sends the config object back verbatim on save and the PUT handler
	// rejects unknown fields.
	cfgRec := get(t, h, "/api/config")
	if strings.Contains(cfgRec.Body.String(), "notices") {
		t.Error("notices leaked into the config payload, which is round-tripped on save")
	}
}

func TestUnknownPanelAndPageAreRejected(t *testing.T) {
	_, h := newTestService(t)
	if rec := get(t, h, "/api/panel/nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown panel: status %d", rec.Code)
	}
	if rec := get(t, h, "/api/page/nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown page: status %d", rec.Code)
	}
}

func TestMissingCredentialsExplainThemselves(t *testing.T) {
	svc, _ := newTestService(t)
	svc.CredentialError = fmt.Errorf("missing AWS_ACCESS_KEY_ID")
	h := svc.Handler()

	rec := get(t, h, "/api/page/overview")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	var resp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Hint, ".env") {
		t.Errorf("hint does not say where credentials go: %q", resp.Hint)
	}

	// Health still answers, so the UI can tell the difference between a
	// misconfigured dashboard and a dead one.
	if rec := get(t, h, "/api/health"); rec.Code != http.StatusOK {
		t.Errorf("health: status %d", rec.Code)
	}
}

// The cost of a refresh is reported rather than left to a bill.
func TestInsightsScanCostIsReported(t *testing.T) {
	_, h := newTestService(t)
	panel := findPanel(t, decodePayload(t, get(t, h, "/api/panel/pod-latency?range=1h&period=5m")), "pod-latency")
	for _, s := range panel.Stats {
		if s.Key == "insights.bytesScanned" {
			if s.Value == nil || *s.Value <= 0 {
				t.Errorf("bytesScanned = %v", s.Value)
			}
			return
		}
	}
	t.Error("no scan-cost stat")
}

// A panel that fails must not blank the ones beside it.
func TestAFailingPanelDoesNotSinkThePage(t *testing.T) {
	svc, _ := newTestService(t)
	cfg := svc.Store.Get()
	cfg.WAFLogGroup = "" // the WAF panels now have nothing to query
	if err := svc.Store.Set(cfg); err != nil {
		t.Fatal(err)
	}
	h := svc.Handler()

	p := decodePayload(t, get(t, h, "/api/page/overview?range=1h&period=5m"))
	waf := findPanel(t, p, "waf-traffic")
	if len(waf.Warnings) == 0 {
		t.Error("the WAF panel reported no reason for being empty")
	}
	latency := findPanel(t, p, "pod-latency")
	if len(latency.Series) == 0 {
		t.Error("an unrelated panel lost its data because a neighbour failed")
	}
}

func TestUnselectedResourcesProduceAnExplanationNotAnError(t *testing.T) {
	store, err := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	logs := newStubLogs("ap-northeast-2")
	metrics := &stubMetrics{}
	svc := &Service{
		Store: store,
		Now:   func() time.Time { return testNow },
		Cache: &awsx.Cache{TTL: time.Minute},
		Clients: &awsx.Clients{
			Region: "ap-northeast-2", WAFRegion: "us-east-1",
			CW: metrics, CWGlobal: metrics, Logs: logs, LogsGlobal: logs, EKS: stubEKS{},
		},
		Insights: &awsx.InsightsRunner{API: logs, PollInterval: time.Millisecond},
		Metrics:  &awsx.MetricFetcher{},
	}
	h := svc.Handler()

	for _, id := range []string{"targetgroup", "rds-proxy", "waf-metrics", "pod-resource", "counts"} {
		p := decodePayload(t, get(t, h, "/api/panel/"+id+"?range=1h&period=5m"))
		panel := findPanel(t, p, id)
		if len(panel.Warnings) == 0 {
			t.Errorf("panel %q is empty with no explanation", id)
		}
	}
}

func TestConfigRoundTripThroughTheAPI(t *testing.T) {
	svc, h := newTestService(t)

	rec := get(t, h, "/api/config")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var cfg config.Config
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Namespace = "payments"

	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(string(body)))
	put := httptest.NewRecorder()
	h.ServeHTTP(put, req)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", put.Code, put.Body.String())
	}
	if got := svc.Store.Get().Namespace; got != "payments" {
		t.Errorf("namespace = %q after save", got)
	}
}

func TestConfigRejectsAnUncompilablePattern(t *testing.T) {
	_, h := newTestService(t)
	body := `{"logFormat":{"timeField":"time","messageField":"log","levelPattern":"(unclosed","latencyUnit":"ms"}}`
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
}

// The log format is still being settled, so the settings page needs to check a
// pattern against a real line before saving it.
func TestLogFormatPreview(t *testing.T) {
	_, h := newTestService(t)
	sample := `{"time":"2026-08-09T02:57:08.636329927Z","stream":"stdout","log":"{\"app\":\"stress\",\"latency_ms\":12.5,\"method\":\"GET\",\"path\":\"/healthcheck\",\"status\":503}","log_processed":{"app":"stress","client_ip":"10.0.3.123","latency_ms":12.5,"method":"GET","path":"/healthcheck","status":503},"kubernetes":{"pod_name":"stress-1","namespace_name":"default","container_name":"stress"}}`

	body, err := json.Marshal(map[string]any{"sample": sample})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/logfmt/preview", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var resp logFormatPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Matched {
		t.Error("a well-formed access line was not recognised")
	}
	if resp.Parsed.Status != 503 || resp.Parsed.Path != "/healthcheck" {
		t.Errorf("parsed = %+v", resp.Parsed)
	}
	if !resp.BadStatus {
		t.Error("503 was not flagged as a bad status")
	}
	if resp.Parsed.ClientIP != "10.0.3.123" {
		t.Errorf("clientIp = %q", resp.Parsed.ClientIP)
	}
}

func TestLogFormatPreviewExplainsANonMatch(t *testing.T) {
	_, h := newTestService(t)
	body := `{"sample":"just some text with no structure"}`
	req := httptest.NewRequest(http.MethodPost, "/api/logfmt/preview", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp logFormatPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Matched {
		t.Error("an unstructured line was reported as matched")
	}
	if resp.Suggestion == "" {
		t.Error("no suggestion offered for a line that did not match")
	}
}

func TestDiscovery(t *testing.T) {
	_, h := newTestService(t)
	rec := get(t, h, "/api/discovery/loggroups?prefix=/aws/containerinsights/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp discoveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Resources) != 1 {
		t.Fatalf("got %d resources", len(resp.Resources))
	}
	if resp.Resources[0].ID != "/aws/containerinsights/prod/application" {
		t.Errorf("resource = %+v", resp.Resources[0])
	}

	if rec := get(t, h, "/api/discovery/nonsense"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown kind: status %d", rec.Code)
	}
}

func discover(t *testing.T, h http.Handler, path string) discoveryResponse {
	t.Helper()
	rec := get(t, h, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body.String())
	}
	var resp discoveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return resp
}

// A CLOUDFRONT-scoped web ACL logs only into us-east-1. Listing the working
// region returns nothing, which reads as "this account has no WAF logging" and
// leaves the operator with a field they can only guess at.
func TestWAFLogGroupsAreListedFromTheWAFRegion(t *testing.T) {
	_, h := newTestService(t)

	resp := discover(t, h, "/api/discovery/waf-loggroups?prefix=aws-waf-logs-")
	if len(resp.Resources) != 1 || resp.Resources[0].ID != "aws-waf-logs-demo" {
		t.Fatalf("waf-loggroups = %+v, want the us-east-1 group", resp.Resources)
	}

	// The working-region listing must not have acquired it.
	if got := discover(t, h, "/api/discovery/loggroups?prefix=aws-waf-logs-"); len(got.Resources) != 0 {
		t.Errorf("the working region reported WAF log groups it does not have: %+v", got.Resources)
	}
}

// The WAF panels query us-east-1; the pod panels query the working region.
// Sending a WAF query to the working region fails on a log group that is not
// there, which is what the dashboard did before the runners were split.
func TestWAFLogQueriesGoToTheWAFRegion(t *testing.T) {
	svc, h := newTestService(t)

	if rec := get(t, h, "/api/page/waf?range=1h&period=5m"); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if rec := get(t, h, "/api/page/pod-logs?range=1h&period=5m"); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}

	global := svc.Clients.LogsGlobal.(*stubLogs).startedGroups()
	primary := svc.Clients.Logs.(*stubLogs).startedGroups()

	if len(global) == 0 {
		t.Fatal("no query reached us-east-1")
	}
	for _, g := range global {
		if g != "aws-waf-logs-demo" {
			t.Errorf("us-east-1 was queried for %q, want only the WAF group", g)
		}
	}
	if len(primary) == 0 {
		t.Fatal("no query reached the working region")
	}
	for _, g := range primary {
		if g == "aws-waf-logs-demo" {
			t.Error("a WAF query went to the working region, where the group does not exist")
		}
	}
}

// Two regions can hold a log group of the same name. Keyed on the name alone,
// the second query would be answered out of the first one's cache entry and
// the second region would never be called at all.
func TestLogCacheIsKeyedByRegion(t *testing.T) {
	svc, _ := newTestService(t)
	w, err := domain.NewWindow(testNow, domain.Range1h, domain.Period5m)
	if err != nil {
		t.Fatal(err)
	}
	rc := requestCtx{ctx: context.Background(), w: w, cfg: svc.Store.Get()}
	q := domain.WAFQueries{}.ActionSeries(w)

	// Identical in every respect but the region they are read from.
	const shared = "same-name-in-both-regions"
	primary := logSource{runner: svc.Insights, region: "ap-northeast-2", group: shared}
	global := logSource{runner: svc.InsightsGlobal, region: "us-east-1", group: shared}

	svc.runLogQueries(rc, primary, "same-panel", []domain.Query{q})
	svc.runLogQueries(rc, global, "same-panel", []domain.Query{q})

	if got := svc.Clients.LogsGlobal.(*stubLogs).startedGroups(); len(got) == 0 {
		t.Error("the us-east-1 query was served from the working region's cache entry")
	}
}

// The endpoint hands back the CloudWatch dimension, not the ARN. An ARN in
// that field passes every check the config makes and then matches no metric,
// so the panel renders empty with nothing to explain why.
func TestLoadBalancerDiscoveryReturnsTheDimensionNotTheARN(t *testing.T) {
	_, h := newTestService(t)

	resp := discover(t, h, "/api/discovery/loadbalancers")
	if len(resp.Resources) != 1 {
		t.Fatalf("got %d load balancers", len(resp.Resources))
	}
	r := resp.Resources[0]
	if r.ID != "app/my-alb/50dc6c495c0c9188" {
		t.Errorf("ID = %q, want the CloudWatch dimension", r.ID)
	}
	if r.Name != "my-alb" {
		t.Errorf("Name = %q, want the load balancer name", r.Name)
	}
	if r.ARN != testLBArn {
		t.Errorf("ARN = %q, want it carried through for reference", r.ARN)
	}
}

// A target group's metrics must not be scoped to the one load balancer the
// config happens to name.
//
// With one target group per application, those groups can sit behind more than
// one ALB. Pinning LoadBalancer to the single configured value made every group
// on any other ALB match nothing at all, and a SEARCH that matches nothing
// plots as a flat empty line — which reads as an application with no traffic
// rather than as a query asking the wrong question. The SEARCH schema already
// restricts the match to per-target metrics, and a TargetGroup dimension is
// unique on its own, so the value term was only ever narrowing.
func TestTargetGroupMetricsAreNotPinnedToOneLoadBalancer(t *testing.T) {
	svc, h := newTestService(t)
	cfg := svc.Store.Get()
	cfg.LoadBalancer = "app/other-alb/50dc6c495c0c9188"
	cfg.TargetGroups = []string{
		"targetgroup/k8s-default-checkout-1111111111/aaa",
		"targetgroup/k8s-default-search-2222222222/bbb",
	}
	if err := svc.Store.Set(cfg); err != nil {
		t.Fatal(err)
	}

	if rec := get(t, h, "/api/panel/targetgroup?range=1h&period=5m"); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	exprs := svc.Clients.CW.(*stubMetrics).expressions()
	if len(exprs) == 0 {
		t.Fatal("the panel issued no queries")
	}
	var sawCheckout bool
	for _, e := range exprs {
		if strings.Contains(e, "LoadBalancer=") {
			t.Errorf("a target group query pinned the load balancer: %s", e)
		}
		if !strings.Contains(e, "{AWS/ApplicationELB,LoadBalancer,TargetGroup}") {
			t.Errorf("the schema no longer restricts the match to per-target metrics: %s", e)
		}
		if strings.Contains(e, "k8s-default-checkout-1111111111") {
			sawCheckout = true
		}
	}
	if !sawCheckout {
		t.Error("no query was issued for the first target group")
	}
}

// With no target group chosen the panel falls back to the load balancer's own
// metrics, and there the dimension really is required.
func TestTargetGroupPanelStillScopesTheLoadBalancerFallback(t *testing.T) {
	svc, h := newTestService(t)
	cfg := svc.Store.Get()
	cfg.TargetGroups = nil
	cfg.LoadBalancer = "app/my-alb/50dc6c495c0c9188"
	if err := svc.Store.Set(cfg); err != nil {
		t.Fatal(err)
	}

	if rec := get(t, h, "/api/panel/targetgroup?range=1h&period=5m"); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}

	for _, e := range svc.Clients.CW.(*stubMetrics).expressions() {
		if !strings.Contains(e, `LoadBalancer="app/my-alb/50dc6c495c0c9188"`) {
			t.Errorf("the fallback lost its load balancer scope: %s", e)
		}
	}
}

// RDS Proxy and Web ACL discovery had no test at all, which is how two
// endpoints an operator depends on came to be shipped untried.
func TestRDSProxyDiscoveryListsProxies(t *testing.T) {
	_, h := newTestService(t)

	resp := discover(t, h, "/api/discovery/rdsproxies")
	if len(resp.Resources) != 1 {
		t.Fatalf("got %d proxies: %+v", len(resp.Resources), resp.Resources)
	}
	r := resp.Resources[0]
	if r.ID != "app-proxy" {
		t.Errorf("ID = %q, want the ProxyName dimension", r.ID)
	}
	if r.Extra["engine"] != "POSTGRESQL" {
		t.Errorf("engine = %q", r.Extra["engine"])
	}
}

func TestWebACLDiscoveryListsBothScopes(t *testing.T) {
	_, h := newTestService(t)

	resp := discover(t, h, "/api/discovery/webacls")
	byName := map[string]string{}
	for _, r := range resp.Resources {
		byName[r.Name] = r.Extra["scope"]
	}
	if byName["skills-waf"] != string(waftypes.ScopeRegional) {
		t.Errorf("the regional ACL is missing or misfiled: %+v", resp.Resources)
	}
	if byName["edge-waf"] != string(waftypes.ScopeCloudfront) {
		t.Errorf("the CLOUDFRONT ACL is missing or misfiled: %+v", resp.Resources)
	}
	if len(resp.Partial) != 0 {
		t.Errorf("a listing that worked reported %v as partial", resp.Partial)
	}
}

// A denied CLOUDFRONT listing must not hide the regional ACLs — and must not be
// discarded in silence either. Swallowing it is how "this account has no web
// ACLs" came to be said on the strength of one refused call.
func TestWebACLDiscoveryReportsADiscardedScope(t *testing.T) {
	svc, h := newTestService(t)
	svc.Clients.WAFGlobal = stubWAF{
		scope: waftypes.ScopeCloudfront,
		err:   fmt.Errorf("AccessDeniedException: wafv2:ListWebACLs is not allowed"),
	}

	resp := discover(t, h, "/api/discovery/webacls")
	if len(resp.Resources) != 1 || resp.Resources[0].Name != "skills-waf" {
		t.Fatalf("the usable regional ACL was lost with the denied one: %+v", resp.Resources)
	}
	if len(resp.Partial) == 0 {
		t.Fatal("the denied CLOUDFRONT scope was discarded without saying so")
	}
	if !strings.Contains(resp.Partial[0], "CLOUDFRONT") {
		t.Errorf("partial = %q, want it to name the scope", resp.Partial[0])
	}
}

// Every walk is capped. A capped list that does not say so offers a short list
// with no reason to doubt it, which is indistinguishable from a complete one.
func TestDiscoveryReportsATruncatedWalk(t *testing.T) {
	svc, h := newTestService(t)
	svc.Clients.ELB = endlessELB{}

	resp := discover(t, h, "/api/discovery/targetgroups")
	if !resp.Truncated {
		t.Error("the walk hit the page cap without saying so")
	}
	if len(resp.Resources) == 0 {
		t.Error("the capped walk returned nothing at all")
	}
}

// endlessELB never stops paginating.
type endlessELB struct{}

func (endlessELB) DescribeLoadBalancers(context.Context, *elasticloadbalancingv2.DescribeLoadBalancersInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
	return &elasticloadbalancingv2.DescribeLoadBalancersOutput{}, nil
}

func (endlessELB) DescribeTargetGroups(context.Context, *elasticloadbalancingv2.DescribeTargetGroupsInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	return &elasticloadbalancingv2.DescribeTargetGroupsOutput{
		TargetGroups: []elbtypes.TargetGroup{{
			TargetGroupArn:  aws.String("arn:aws:elasticloadbalancing:ap-northeast-2:1:targetgroup/k8s-default-a-1/x"),
			TargetGroupName: aws.String("k8s-default-a-1"),
		}},
		NextMarker: aws.String("more"),
	}, nil
}

// Why a lookup failed is the whole reason the settings page has an error line.
// It has to survive from the AWS call to the browser without being reduced to
// a bare status code.
func TestDiscoveryFailureReachesTheBrowser(t *testing.T) {
	svc, h := newTestService(t)
	svc.Clients.RDS = stubRDS{err: fmt.Errorf("AccessDeniedException: rds:DescribeDBProxies is not allowed")}

	rec := get(t, h, "/api/discovery/rdsproxies")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502: %s", rec.Code, rec.Body.String())
	}
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Detail, "DescribeDBProxies") {
		t.Errorf("detail = %q, want the AWS call and its reason", body.Detail)
	}
	if body.Hint == "" {
		t.Error("no hint about what to check")
	}
}

// Responses must not be cached by the browser, or a range change would keep
// showing the previous window.
func TestResponsesAreNotBrowserCacheable(t *testing.T) {
	_, h := newTestService(t)
	for _, path := range []string{"/api/page/overview", "/api/meta", "/api/config"} {
		rec := get(t, h, path)
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", path, got)
		}
	}
}

// Identical requests must not re-issue AWS calls within the cache window.
func TestRepeatedRequestsAreServedFromCache(t *testing.T) {
	svc, h := newTestService(t)
	metrics := svc.Clients.CW.(*stubMetrics)

	for i := 0; i < 5; i++ {
		if rec := get(t, h, "/api/panel/counts?range=1h&period=5m"); rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
	}
	metrics.mu.Lock()
	calls := metrics.calls
	metrics.mu.Unlock()
	if calls != 1 {
		t.Errorf("GetMetricData ran %d times for five identical requests, want 1", calls)
	}
}

// A panic is a bug, but it must cost one response rather than the process.
func TestAPanicIsContainedToOneResponse(t *testing.T) {
	svc, _ := newTestService(t)
	h := svc.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/page/overview", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	var resp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("a panic did not produce JSON: %v (%s)", err, rec.Body.String())
	}
	if !strings.Contains(resp.Detail, "/api/page/overview") {
		t.Errorf("detail does not say what failed: %q", resp.Detail)
	}
	// The next request still works.
	if rec := get(t, svc.Handler(), "/api/health"); rec.Code != http.StatusOK {
		t.Errorf("health after a panic: status %d", rec.Code)
	}
}

// Node bounds come from the node group scaling configuration, and are read per
// request so a rescale shows up immediately.
func TestNodeCountReportsMinimumAndMaximum(t *testing.T) {
	_, h := newTestService(t)
	panel := findPanel(t, decodePayload(t, get(t, h, "/api/panel/counts?range=1h&period=5m")), "counts")

	stats := map[string]domain.Stat{}
	for _, s := range panel.Stats {
		stats[s.Key] = s
	}
	for _, key := range []string{"pods.current", "pods.min", "pods.max", "nodes.current", "nodes.min", "nodes.max"} {
		if _, ok := stats[key]; !ok {
			t.Errorf("counts panel has no %q stat", key)
		}
	}
	if v := stats["nodes.min"].Value; v == nil || *v != 2 {
		t.Errorf("nodes.min = %v, want 2", v)
	}
	if v := stats["nodes.max"].Value; v == nil || *v != 9 {
		t.Errorf("nodes.max = %v, want 9", v)
	}
	// Pod bounds have no AWS-side source, so they must say they are observed
	// rather than imply they are an autoscaler's configured limits.
	if b := stats["pods.min"].Basis; !strings.Contains(b, "관측") {
		t.Errorf("pods.min basis = %q, want it to state that the value is observed", b)
	}
}

// A different window must not be served from another window's cache entry.
func TestCacheIsKeyedByWindow(t *testing.T) {
	svc, h := newTestService(t)
	metrics := svc.Clients.CW.(*stubMetrics)

	get(t, h, "/api/panel/counts?range=1h&period=5m")
	get(t, h, "/api/panel/counts?range=4h&period=5m")

	metrics.mu.Lock()
	calls := metrics.calls
	metrics.mu.Unlock()
	if calls != 2 {
		t.Errorf("GetMetricData ran %d times for two different windows, want 2", calls)
	}
}
