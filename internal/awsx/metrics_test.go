package awsx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/jhyeok1023/skills-dashboard/internal/domain"
)

// fakeMetrics records what it was asked for and replays a scripted answer.
type fakeMetrics struct {
	calls  [][]cwtypes.MetricDataQuery
	pages  []*cloudwatch.GetMetricDataOutput
	err    error
	callNo int
}

func (f *fakeMetrics) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, in.MetricDataQueries)
	if f.callNo < len(f.pages) {
		out := f.pages[f.callNo]
		f.callNo++
		return out, nil
	}
	return &cloudwatch.GetMetricDataOutput{}, nil
}

func testWindow(t *testing.T) domain.Window {
	t.Helper()
	at, err := time.Parse(time.RFC3339, "2026-08-10T10:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	w, err := domain.NewWindow(at, domain.Range1h, domain.Period5m)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func requests(n int) []MetricRequest {
	out := make([]MetricRequest, n)
	for i := range out {
		out[i] = MetricRequest{
			Spec: domain.MetricSpec{
				Key: fmt.Sprintf("m%d", i), Label: "L", Namespace: domain.NSContainer,
				MetricName: "m", Stat: domain.StatAvg, Unit: domain.UnitPercent,
				Color: domain.ColorBlue, Dimensions: []string{"ClusterName"},
			},
			Filters: map[string]string{"ClusterName": "prod"},
		}
	}
	return out
}

// The ceiling that eventually took the reference implementation's metric panels
// offline is enforced here by splitting, not by a comment.
func TestFetchSplitsAtTheFiveHundredQueryCeiling(t *testing.T) {
	f := &fakeMetrics{}
	fetcher := &MetricFetcher{API: f}

	if _, err := fetcher.Fetch(context.Background(), testWindow(t), requests(1201)); err != nil {
		t.Fatal(err)
	}

	if len(f.calls) != 3 {
		t.Fatalf("made %d calls for 1201 queries, want 3", len(f.calls))
	}
	for i, call := range f.calls {
		if len(call) > MaxQueriesPerCall {
			t.Errorf("call %d carried %d queries, above the %d ceiling", i, len(call), MaxQueriesPerCall)
		}
	}
	total := 0
	for _, call := range f.calls {
		total += len(call)
	}
	if total != 1201 {
		t.Errorf("split dropped queries: sent %d of 1201", total)
	}
}

func TestFetchSendsOneCallWhenUnderTheCeiling(t *testing.T) {
	f := &fakeMetrics{}
	fetcher := &MetricFetcher{API: f}
	if _, err := fetcher.Fetch(context.Background(), testWindow(t), requests(7)); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("made %d calls for 7 queries, want 1", len(f.calls))
	}
}

func TestFetchSendsSearchExpressionsNotDimensionLists(t *testing.T) {
	f := &fakeMetrics{}
	fetcher := &MetricFetcher{API: f}
	w := testWindow(t)
	if _, err := fetcher.Fetch(context.Background(), w, requests(1)); err != nil {
		t.Fatal(err)
	}

	q := f.calls[0][0]
	if q.Expression == nil {
		t.Fatal("query carries no expression, so it must have been built from an enumerated dimension list")
	}
	if !strings.HasPrefix(*q.Expression, "SEARCH('{") {
		t.Errorf("expression = %q, want a SEARCH", *q.Expression)
	}
	if q.MetricStat != nil {
		t.Error("query carries a MetricStat as well as an expression")
	}
	if got := aws.ToInt32(q.Period); got != w.Period.Seconds() {
		t.Errorf("period = %d, want %d", got, w.Period.Seconds())
	}
}

func TestFetchSpansTheWindowExactly(t *testing.T) {
	f := &fakeMetrics{}
	fetcher := &MetricFetcher{API: f}
	w := testWindow(t)
	if _, err := fetcher.Fetch(context.Background(), w, requests(1)); err != nil {
		t.Fatal(err)
	}
	// The fake records queries, not the whole input, so re-run with a
	// capturing wrapper to inspect the bounds.
	var got *cloudwatch.GetMetricDataInput
	cap := &capturingMetrics{onCall: func(in *cloudwatch.GetMetricDataInput) { got = in }}
	fetcher = &MetricFetcher{API: cap}
	if _, err := fetcher.Fetch(context.Background(), w, requests(1)); err != nil {
		t.Fatal(err)
	}
	if !aws.ToTime(got.StartTime).Equal(w.Start) || !aws.ToTime(got.EndTime).Equal(w.End) {
		t.Errorf("queried %s..%s, want %s", aws.ToTime(got.StartTime), aws.ToTime(got.EndTime), w)
	}
	if got.ScanBy != cwtypes.ScanByTimestampAscending {
		t.Errorf("ScanBy = %v", got.ScanBy)
	}
}

type capturingMetrics struct {
	onCall func(*cloudwatch.GetMetricDataInput)
}

func (c *capturingMetrics) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	c.onCall(in)
	return &cloudwatch.GetMetricDataOutput{}, nil
}

// An identifier CloudWatch did not receive from us is a bug. The reference
// implementation wrote such results into series zero and could panic on the
// index; here it is an error.
func TestFetchRejectsAnUnknownQueryID(t *testing.T) {
	f := &fakeMetrics{pages: []*cloudwatch.GetMetricDataOutput{{
		MetricDataResults: []cwtypes.MetricDataResult{{
			Id:         aws.String("qsomethingelse"),
			Label:      aws.String("x"),
			Timestamps: []time.Time{time.Now()},
			Values:     []float64{1},
		}},
	}}}
	fetcher := &MetricFetcher{API: f}
	_, err := fetcher.Fetch(context.Background(), testWindow(t), requests(1))
	if err == nil {
		t.Fatal("an unknown query id was accepted")
	}
	if !strings.Contains(err.Error(), "unknown query id") {
		t.Errorf("error should name the problem: %v", err)
	}
}

// Indexing values by the timestamp loop is exactly how the reference
// implementation panicked when a paginated response arrived with mismatched
// lengths.
func TestFetchRejectsMismatchedTimestampsAndValues(t *testing.T) {
	f := &fakeMetrics{pages: []*cloudwatch.GetMetricDataOutput{{
		MetricDataResults: []cwtypes.MetricDataResult{{
			Id:         aws.String(domain.QueryID("m0")),
			Label:      aws.String("pod-a"),
			Timestamps: []time.Time{time.Now(), time.Now()},
			Values:     []float64{1},
		}},
	}}}
	fetcher := &MetricFetcher{API: f}
	if _, err := fetcher.Fetch(context.Background(), testWindow(t), requests(1)); err == nil {
		t.Fatal("a mismatched result was accepted; a length mismatch must not become an index panic")
	}
}

func TestFetchGroupsSearchResultsBySpecKey(t *testing.T) {
	w := testWindow(t)
	ts := w.Timestamps()
	id := domain.QueryID("m0")

	f := &fakeMetrics{pages: []*cloudwatch.GetMetricDataOutput{{
		MetricDataResults: []cwtypes.MetricDataResult{
			{
				Id: aws.String(id), Label: aws.String("pod-a"),
				Timestamps: []time.Time{time.Unix(ts[0], 0).UTC(), time.Unix(ts[2], 0).UTC()},
				Values:     []float64{10, 30},
			},
			{
				Id: aws.String(id), Label: aws.String("pod-b"),
				Timestamps: []time.Time{time.Unix(ts[1], 0).UTC()},
				Values:     []float64{55},
			},
		},
	}}}

	fetcher := &MetricFetcher{API: f}
	got, err := fetcher.Fetch(context.Background(), w, requests(1))
	if err != nil {
		t.Fatal(err)
	}

	list := got["m0"]
	if len(list) != 2 {
		t.Fatalf("got %d series, want 2 (a SEARCH matches many series under one id)", len(list))
	}
	SortSeries(list)
	if list[0].Label != "pod-a" || list[1].Label != "pod-b" {
		t.Fatalf("labels = %q, %q", list[0].Label, list[1].Label)
	}
	if list[0].Points[ts[0]] != 10 || list[0].Points[ts[2]] != 30 {
		t.Errorf("pod-a points = %v", list[0].Points)
	}
}

func TestFetchFollowsNextTokenAndMergesIntoOneSeries(t *testing.T) {
	w := testWindow(t)
	ts := w.Timestamps()
	id := domain.QueryID("m0")

	f := &fakeMetrics{pages: []*cloudwatch.GetMetricDataOutput{
		{
			NextToken: aws.String("page2"),
			MetricDataResults: []cwtypes.MetricDataResult{{
				Id: aws.String(id), Label: aws.String("pod-a"),
				Timestamps: []time.Time{time.Unix(ts[0], 0).UTC()},
				Values:     []float64{1},
			}},
		},
		{
			MetricDataResults: []cwtypes.MetricDataResult{{
				Id: aws.String(id), Label: aws.String("pod-a"),
				Timestamps: []time.Time{time.Unix(ts[1], 0).UTC()},
				Values:     []float64{2},
			}},
		},
	}}

	fetcher := &MetricFetcher{API: f}
	got, err := fetcher.Fetch(context.Background(), w, requests(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(got["m0"]) != 1 {
		t.Fatalf("paging created %d series for one label, want 1", len(got["m0"]))
	}
	pts := got["m0"][0].Points
	if pts[ts[0]] != 1 || pts[ts[1]] != 2 {
		t.Errorf("pages did not merge: %v", pts)
	}
}

// An endless NextToken walk is how a single request comes to hold a connection
// open forever.
func TestFetchStopsWalkingRunawayPagination(t *testing.T) {
	fetcher := &MetricFetcher{API: &endlessPager{}}
	_, err := fetcher.Fetch(context.Background(), testWindow(t), requests(1))
	if err == nil {
		t.Fatal("an endless pagination walk was allowed to continue")
	}
	if !strings.Contains(err.Error(), "pages") {
		t.Errorf("error should name the page cap: %v", err)
	}
}

type endlessPager struct{ n int }

func (e *endlessPager) GetMetricData(_ context.Context, _ *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	e.n++
	return &cloudwatch.GetMetricDataOutput{NextToken: aws.String("more")}, nil
}

func TestFetchPropagatesAPIErrors(t *testing.T) {
	want := errors.New("throttled")
	fetcher := &MetricFetcher{API: &fakeMetrics{err: want}}
	_, err := fetcher.Fetch(context.Background(), testWindow(t), requests(1))
	if !errors.Is(err, want) {
		t.Errorf("error = %v, want it to wrap %v", err, want)
	}
}

func TestFetchRejectsDuplicateSpecKeys(t *testing.T) {
	reqs := requests(2)
	reqs[1].Spec.Key = reqs[0].Spec.Key
	fetcher := &MetricFetcher{API: &fakeMetrics{}}
	if _, err := fetcher.Fetch(context.Background(), testWindow(t), reqs); err == nil {
		t.Fatal("two specs sharing a key were accepted; results would overwrite each other")
	}
}

func TestFetchRejectsUnsafeFilters(t *testing.T) {
	reqs := requests(1)
	reqs[0].Filters = map[string]string{"ClusterName": `prod" OR MetricName="other`}
	fetcher := &MetricFetcher{API: &fakeMetrics{}}
	if _, err := fetcher.Fetch(context.Background(), testWindow(t), reqs); err == nil {
		t.Fatal("an unsafe dimension value reached the expression builder")
	}
}

func TestToSeriesPreservesGaps(t *testing.T) {
	w := testWindow(t)
	ts := w.Timestamps()
	m := MetricSeries{Key: "m0", Label: "pod-a", Points: map[int64]float64{
		ts[0]: 10,
		ts[3]: 40,
	}}

	s := m.ToSeries(w, "pod-a", domain.UnitPercent, domain.ColorBlue)
	if len(s.Values) != w.Buckets() {
		t.Fatalf("series has %d values, want %d", len(s.Values), w.Buckets())
	}
	if s.Values[0] == nil || *s.Values[0] != 10 {
		t.Errorf("bucket 0 = %v, want 10", s.Values[0])
	}
	if s.Values[1] != nil {
		t.Errorf("bucket 1 = %v, want a gap rather than a zero", *s.Values[1])
	}
	if s.Values[3] == nil || *s.Values[3] != 40 {
		t.Errorf("bucket 3 = %v, want 40", s.Values[3])
	}
	if s.Defined() != 2 {
		t.Errorf("defined samples = %d, want 2", s.Defined())
	}
}

func TestFetchWithNoRequestsMakesNoCalls(t *testing.T) {
	f := &fakeMetrics{}
	fetcher := &MetricFetcher{API: f}
	got, err := fetcher.Fetch(context.Background(), testWindow(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Errorf("made %d calls for an empty request set", len(f.calls))
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}
