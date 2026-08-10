package awsx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/jhyeok1023/skills-dashboard/internal/domain"
)

// fakeLogs scripts a Logs Insights conversation.
type fakeLogs struct {
	mu sync.Mutex

	startInputs []*cloudwatchlogs.StartQueryInput
	stopped     []string

	// runningPolls is how many GetQueryResults calls return Running before the
	// query completes.
	runningPolls int
	polls        int32

	results    [][]logtypes.ResultField
	statistics *logtypes.QueryStatistics
	status     logtypes.QueryStatus

	startErr error
	pollErr  error

	// inFlight tracks concurrency for the semaphore test.
	inFlight, maxInFlight int32
	hold                  time.Duration
}

func (f *fakeLogs) StartQuery(_ context.Context, in *cloudwatchlogs.StartQueryInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.mu.Lock()
	f.startInputs = append(f.startInputs, in)
	n := len(f.startInputs)
	f.mu.Unlock()

	cur := atomic.AddInt32(&f.inFlight, 1)
	for {
		hi := atomic.LoadInt32(&f.maxInFlight)
		if cur <= hi || atomic.CompareAndSwapInt32(&f.maxInFlight, hi, cur) {
			break
		}
	}
	if f.hold > 0 {
		time.Sleep(f.hold)
	}
	return &cloudwatchlogs.StartQueryOutput{QueryId: aws.String(fmt.Sprintf("query-%d", n))}, nil
}

func (f *fakeLogs) GetQueryResults(_ context.Context, _ *cloudwatchlogs.GetQueryResultsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error) {
	if f.pollErr != nil {
		return nil, f.pollErr
	}
	n := atomic.AddInt32(&f.polls, 1)
	if int(n) <= f.runningPolls {
		return &cloudwatchlogs.GetQueryResultsOutput{Status: logtypes.QueryStatusRunning}, nil
	}
	atomic.AddInt32(&f.inFlight, -1)

	status := f.status
	if status == "" {
		status = logtypes.QueryStatusComplete
	}
	return &cloudwatchlogs.GetQueryResultsOutput{
		Status:     status,
		Results:    f.results,
		Statistics: f.statistics,
	}, nil
}

func (f *fakeLogs) StopQuery(_ context.Context, in *cloudwatchlogs.StopQueryInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StopQueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, aws.ToString(in.QueryId))
	return &cloudwatchlogs.StopQueryOutput{}, nil
}

func (f *fakeLogs) stoppedQueries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stopped...)
}

func field(name, value string) logtypes.ResultField {
	return logtypes.ResultField{Field: aws.String(name), Value: aws.String(value)}
}

func testQuery() domain.Query {
	return domain.Query{ID: "pod.traffic", Text: "stats count() by bin(5m)"}
}

func TestRunReturnsRowsKeyedByField(t *testing.T) {
	f := &fakeLogs{
		results: [][]logtypes.ResultField{
			{field("t", "2026-08-10 09:00:00.000"), field("app", "api"), field("requests", "120"), field("@ptr", "opaque")},
			{field("t", "2026-08-10 09:05:00.000"), field("app", "api"), field("requests", "98")},
		},
		statistics: &logtypes.QueryStatistics{BytesScanned: 4096, RecordsMatched: 218},
	}
	r := &InsightsRunner{API: f, PollInterval: time.Millisecond}

	got, err := r.Run(context.Background(), "/aws/containerinsights/prod/application", testWindow(t), testQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(got.Rows))
	}
	if got.Rows[0]["app"] != "api" || got.Rows[0]["requests"] != "120" {
		t.Errorf("row 0 = %v", got.Rows[0])
	}
	// @ptr is CloudWatch bookkeeping, not data.
	if _, ok := got.Rows[0]["@ptr"]; ok {
		t.Error("@ptr leaked into the row")
	}
	if got.BytesScanned != 4096 {
		t.Errorf("BytesScanned = %v, want 4096; the cost of a refresh must be visible", got.BytesScanned)
	}
	if got.RecordsMatched != 218 {
		t.Errorf("RecordsMatched = %v", got.RecordsMatched)
	}
	if got.Truncated {
		t.Error("a two-row result was reported as truncated")
	}
}

func TestRunPassesTheWindowBounds(t *testing.T) {
	f := &fakeLogs{}
	r := &InsightsRunner{API: f, PollInterval: time.Millisecond}
	w := testWindow(t)

	if _, err := r.Run(context.Background(), "/lg", w, domain.Query{ID: "q", Text: "fields @timestamp", Limit: 300}); err != nil {
		t.Fatal(err)
	}
	in := f.startInputs[0]
	if aws.ToInt64(in.StartTime) != w.Start.Unix() || aws.ToInt64(in.EndTime) != w.End.Unix() {
		t.Errorf("query spans %d..%d, want %d..%d", aws.ToInt64(in.StartTime), aws.ToInt64(in.EndTime), w.Start.Unix(), w.End.Unix())
	}
	if got := in.LogGroupNames; len(got) != 1 || got[0] != "/lg" {
		t.Errorf("log groups = %v", got)
	}
	if aws.ToInt32(in.Limit) != 300 {
		t.Errorf("limit = %d, want 300", aws.ToInt32(in.Limit))
	}
}

func TestRunPollsUntilComplete(t *testing.T) {
	f := &fakeLogs{runningPolls: 3, results: [][]logtypes.ResultField{{field("n", "1")}}}
	r := &InsightsRunner{API: f, PollInterval: time.Millisecond}

	got, err := r.Run(context.Background(), "/lg", testWindow(t), testQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 1 {
		t.Errorf("got %d rows", len(got.Rows))
	}
	if n := atomic.LoadInt32(&f.polls); n < 4 {
		t.Errorf("polled %d times, want at least 4", n)
	}
}

// An abandoned query keeps holding one of the account's concurrent slots until
// it finishes on its own. Releasing it is what keeps a page of cancelled
// refreshes from starving the next one.
func TestRunStopsTheQueryWhenTheContextIsCancelled(t *testing.T) {
	f := &fakeLogs{runningPolls: 1 << 30} // never completes
	r := &InsightsRunner{API: f, PollInterval: time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := r.Run(ctx, "/lg", testWindow(t), testQuery())
	if err == nil {
		t.Fatal("a cancelled query returned success")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	if got := f.stoppedQueries(); len(got) != 1 || got[0] != "query-1" {
		t.Errorf("StopQuery calls = %v, want the query to have been released", got)
	}
}

func TestRunStopsTheQueryWhenItsDeadlineExpires(t *testing.T) {
	f := &fakeLogs{runningPolls: 1 << 30}
	r := &InsightsRunner{API: f, PollInterval: time.Millisecond, Timeout: 30 * time.Millisecond}

	_, err := r.Run(context.Background(), "/lg", testWindow(t), testQuery())
	if err == nil {
		t.Fatal("a query with no end was allowed to run forever")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want a deadline", err)
	}
	if len(f.stoppedQueries()) != 1 {
		t.Error("the timed-out query was not released")
	}
}

func TestRunReportsTerminalCloudWatchStatuses(t *testing.T) {
	for _, tc := range []struct {
		status logtypes.QueryStatus
		want   string
	}{
		{logtypes.QueryStatusFailed, "failed"},
		{logtypes.QueryStatusCancelled, "cancelled"},
		{logtypes.QueryStatusTimeout, "timed out"},
	} {
		f := &fakeLogs{status: tc.status}
		r := &InsightsRunner{API: f, PollInterval: time.Millisecond}
		_, err := r.Run(context.Background(), "/lg", testWindow(t), testQuery())
		if err == nil {
			t.Errorf("status %s returned success", tc.status)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("status %s produced %q, want it to mention %q", tc.status, err, tc.want)
		}
	}
}

func TestRunRequiresALogGroup(t *testing.T) {
	r := &InsightsRunner{API: &fakeLogs{}, PollInterval: time.Millisecond}
	_, err := r.Run(context.Background(), "", testWindow(t), testQuery())
	if !errors.Is(err, ErrNoLogGroup) {
		t.Errorf("error = %v, want ErrNoLogGroup so the UI can explain itself", err)
	}
}

func TestRunPropagatesStartQueryErrors(t *testing.T) {
	want := errors.New("throttled")
	r := &InsightsRunner{API: &fakeLogs{startErr: want}, PollInterval: time.Millisecond}
	_, err := r.Run(context.Background(), "/lg", testWindow(t), testQuery())
	if !errors.Is(err, want) {
		t.Errorf("error = %v, want it to wrap %v", err, want)
	}
}

// CloudWatch caps a result set; a query that came back exactly full was
// probably cut off, and the payload has to say so.
func TestTruncationIsReported(t *testing.T) {
	rows := make([][]logtypes.ResultField, 300)
	for i := range rows {
		rows[i] = []logtypes.ResultField{field("n", "1")}
	}
	f := &fakeLogs{results: rows}
	r := &InsightsRunner{API: f, PollInterval: time.Millisecond}

	got, err := r.Run(context.Background(), "/lg", testWindow(t), domain.Query{ID: "list", Text: "x", Limit: 300})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated {
		t.Error("a result that exactly filled its limit was not flagged as truncated")
	}

	under, err := r.Run(context.Background(), "/lg", testWindow(t), domain.Query{ID: "list", Text: "x", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if under.Truncated {
		t.Error("300 rows against a 500 limit was flagged as truncated")
	}
}

// Concurrency is bounded so a page cannot exhaust the account's query slots.
func TestRunAllRespectsTheConcurrencyLimit(t *testing.T) {
	f := &fakeLogs{hold: 15 * time.Millisecond, results: [][]logtypes.ResultField{{field("n", "1")}}}
	r := &InsightsRunner{API: f, Concurrency: 3, PollInterval: time.Millisecond}

	qs := make([]domain.Query, 12)
	for i := range qs {
		qs[i] = domain.Query{ID: fmt.Sprintf("q%d", i), Text: "stats count()"}
	}

	got, errs := r.RunAll(context.Background(), "/lg", testWindow(t), qs)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 12 {
		t.Fatalf("got %d results, want 12", len(got))
	}
	if peak := atomic.LoadInt32(&f.maxInFlight); peak > 3 {
		t.Errorf("peak concurrency was %d, above the configured 3", peak)
	}
}

// A failing panel must not blank the ones beside it.
func TestRunAllIsolatesFailures(t *testing.T) {
	r := &InsightsRunner{API: &fakeLogs{startErr: errors.New("boom")}, PollInterval: time.Millisecond}
	qs := []domain.Query{{ID: "a", Text: "x"}, {ID: "b", Text: "y"}}

	got, errs := r.RunAll(context.Background(), "/lg", testWindow(t), qs)
	if len(got) != 0 {
		t.Errorf("got %d results from a failing API", len(got))
	}
	if len(errs) != 2 {
		t.Fatalf("got %d errors, want one per query", len(errs))
	}
	for _, id := range []string{"a", "b"} {
		if errs[id] == nil {
			t.Errorf("no error recorded against query %q", id)
		}
	}
}

func TestRunAllKeysResultsByQueryID(t *testing.T) {
	f := &fakeLogs{results: [][]logtypes.ResultField{{field("n", "7")}}}
	r := &InsightsRunner{API: f, PollInterval: time.Millisecond}
	qs := []domain.Query{{ID: "pod.traffic", Text: "a"}, {ID: "pod.errors.series", Text: "b"}}

	got, errs := r.RunAll(context.Background(), "/lg", testWindow(t), qs)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, id := range []string{"pod.traffic", "pod.errors.series"} {
		if _, ok := got[id]; !ok {
			t.Errorf("no result for %q", id)
		}
	}
}

func TestTotalBytesScanned(t *testing.T) {
	got := TotalBytesScanned(map[string]QueryResult{
		"a": {BytesScanned: 1024},
		"b": {BytesScanned: 2048},
	})
	if got != 3072 {
		t.Errorf("got %v, want 3072", got)
	}
}

func TestSortedRowsIsStable(t *testing.T) {
	rows := []map[string]string{
		{"t": "3", "v": "c"},
		{"t": "1", "v": "a"},
		{"t": "2", "v": "b"},
	}
	got := SortedRows(rows, "t")
	for i, want := range []string{"a", "b", "c"} {
		if got[i]["v"] != want {
			t.Errorf("position %d = %q, want %q", i, got[i]["v"], want)
		}
	}
	// The input is left alone so a caller can sort the same rows twice.
	if rows[0]["v"] != "c" {
		t.Error("SortedRows mutated its input")
	}
}
