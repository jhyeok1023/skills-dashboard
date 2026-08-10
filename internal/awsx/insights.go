package awsx

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/jhyeok1023/skills-dashboard/internal/domain"
)

// InsightsMaxRows is the ceiling CloudWatch puts on a Logs Insights result set.
// A query that returns exactly this many rows was probably cut off, and the
// payload says so rather than presenting a partial answer as complete.
const InsightsMaxRows = 10000

// QueryResult is one completed Logs Insights query.
type QueryResult struct {
	ID   string
	Rows []map[string]string

	// BytesScanned is what the query cost. Insights bills per byte scanned, so
	// this is surfaced in the UI: the operator can see what a refresh costs
	// instead of discovering it on a bill.
	BytesScanned   float64
	RecordsMatched float64
	Truncated      bool
	Elapsed        time.Duration
}

// InsightsRunner executes Logs Insights queries with the guard rails the
// reference implementation lacked: a bounded number in flight, a deadline on
// each one, and a StopQuery on the way out.
type InsightsRunner struct {
	API LogsAPI

	// Concurrency bounds queries in flight. CloudWatch allows roughly thirty
	// concurrent queries per account; a page here issues five or six, and
	// staying well under leaves headroom for anything else using the key.
	Concurrency int
	// Timeout bounds a single query.
	Timeout time.Duration
	// PollInterval is how often GetQueryResults is called while a query runs.
	PollInterval time.Duration

	once sync.Once
	sem  chan struct{}
}

func (r *InsightsRunner) init() {
	r.once.Do(func() {
		n := r.Concurrency
		if n <= 0 {
			n = 6
		}
		r.sem = make(chan struct{}, n)
		if r.Timeout <= 0 {
			r.Timeout = 45 * time.Second
		}
		if r.PollInterval <= 0 {
			r.PollInterval = 250 * time.Millisecond
		}
	})
}

// ErrNoLogGroup is returned when a query is requested for a log group that has
// not been configured, so the caller can render an explanatory panel instead of
// an error.
var ErrNoLogGroup = errors.New("no log group configured")

// Run executes one query over the window and waits for it to finish.
func (r *InsightsRunner) Run(ctx context.Context, logGroup string, w domain.Window, q domain.Query) (QueryResult, error) {
	r.init()
	if logGroup == "" {
		return QueryResult{}, ErrNoLogGroup
	}

	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return QueryResult{}, ctx.Err()
	}

	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	started := time.Now()
	in := &cloudwatchlogs.StartQueryInput{
		LogGroupNames: []string{logGroup},
		StartTime:     aws.Int64(w.Start.Unix()),
		EndTime:       aws.Int64(w.End.Unix()),
		QueryString:   aws.String(q.Text),
	}
	if q.Limit > 0 {
		in.Limit = aws.Int32(int32(q.Limit))
	}

	out, err := r.API.StartQuery(ctx, in)
	if err != nil {
		return QueryResult{}, fmt.Errorf("StartQuery %s: %w", q.ID, err)
	}
	queryID := aws.ToString(out.QueryId)
	if queryID == "" {
		return QueryResult{}, fmt.Errorf("StartQuery %s returned no query id", q.ID)
	}

	res, err := r.poll(ctx, queryID, q)
	if err != nil {
		// The query is still running on CloudWatch's side and still holding a
		// concurrency slot. Releasing it needs a live context, so a fresh one
		// is used rather than the cancelled one we are unwinding from.
		r.stop(queryID)
		return QueryResult{}, err
	}
	res.Elapsed = time.Since(started)
	return res, nil
}

func (r *InsightsRunner) poll(ctx context.Context, queryID string, q domain.Query) (QueryResult, error) {
	ticker := time.NewTicker(r.PollInterval)
	defer ticker.Stop()

	for {
		out, err := r.API.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{
			QueryId: aws.String(queryID),
		})
		if err != nil {
			return QueryResult{}, fmt.Errorf("GetQueryResults %s: %w", q.ID, err)
		}

		switch out.Status {
		case logtypes.QueryStatusComplete:
			return buildResult(q, out), nil
		case logtypes.QueryStatusFailed:
			return QueryResult{}, fmt.Errorf("query %s failed", q.ID)
		case logtypes.QueryStatusCancelled:
			return QueryResult{}, fmt.Errorf("query %s was cancelled", q.ID)
		case logtypes.QueryStatusTimeout:
			return QueryResult{}, fmt.Errorf("query %s timed out in CloudWatch", q.ID)
		}

		select {
		case <-ctx.Done():
			return QueryResult{}, fmt.Errorf("query %s: %w", q.ID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func buildResult(q domain.Query, out *cloudwatchlogs.GetQueryResultsOutput) QueryResult {
	res := QueryResult{ID: q.ID, Rows: make([]map[string]string, 0, len(out.Results))}
	for _, row := range out.Results {
		m := make(map[string]string, len(row))
		for _, f := range row {
			name := aws.ToString(f.Field)
			if name == "@ptr" {
				continue
			}
			m[name] = aws.ToString(f.Value)
		}
		res.Rows = append(res.Rows, m)
	}
	if s := out.Statistics; s != nil {
		res.BytesScanned = s.BytesScanned
		res.RecordsMatched = s.RecordsMatched
	}

	limit := q.Limit
	if limit <= 0 || limit > InsightsMaxRows {
		limit = InsightsMaxRows
	}
	res.Truncated = len(res.Rows) >= limit
	return res
}

// stop releases the CloudWatch-side query slot. It runs on its own short-lived
// context because the caller's has usually just been cancelled, and it ignores
// the error: the query may well have finished on its own, and there is nothing
// useful to do about a failure to cancel something already gone.
func (r *InsightsRunner) stop(queryID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = r.API.StopQuery(ctx, &cloudwatchlogs.StopQueryInput{QueryId: aws.String(queryID)})
}

// RunAll executes every query concurrently, subject to the runner's limit, and
// returns the results keyed by query id.
//
// One query failing does not sink the rest: its error is recorded against its
// id so the panel it feeds can say what went wrong while its neighbours still
// render.
func (r *InsightsRunner) RunAll(ctx context.Context, logGroup string, w domain.Window, qs []domain.Query) (map[string]QueryResult, map[string]error) {
	r.init()
	results := make(map[string]QueryResult, len(qs))
	errs := map[string]error{}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, q := range qs {
		wg.Add(1)
		go func(q domain.Query) {
			defer wg.Done()
			res, err := r.Run(ctx, logGroup, w, q)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[q.ID] = err
				return
			}
			results[q.ID] = res
		}(q)
	}
	wg.Wait()
	return results, errs
}

// TotalBytesScanned sums what a set of results cost.
func TotalBytesScanned(results map[string]QueryResult) float64 {
	var total float64
	for _, r := range results {
		total += r.BytesScanned
	}
	return total
}

// SortedRows returns a result's rows ordered by the named field, which keeps a
// table from reshuffling between refreshes when CloudWatch returns ties in a
// different order.
func SortedRows(rows []map[string]string, field string) []map[string]string {
	out := append([]map[string]string(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool { return out[i][field] < out[j][field] })
	return out
}
