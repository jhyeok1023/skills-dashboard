package awsx

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/jhyeok1023/skills-dashboard/internal/domain"
)

// MaxQueriesPerCall is CloudWatch's hard ceiling on MetricDataQueries in one
// GetMetricData call.
//
// The reference implementation documented this number in a comment and never
// enforced it. Because its query list was built from ListMetrics — which keeps
// returning dimensions for pods that were deleted up to two weeks ago — the
// list grew monotonically until it crossed the ceiling, after which every call
// failed validation and the metric panels stayed empty for good. Here the limit
// is enforced by splitting the work, and the tests hold it to that.
const MaxQueriesPerCall = 500

// maxPages bounds the NextToken walk so a pathological response cannot hold a
// request open indefinitely.
const maxPages = 20

// MetricRequest pairs a catalog entry with the dimension values to filter it by.
type MetricRequest struct {
	// Key distinguishes this request's results. It defaults to the spec's key
	// and is overridden when one spec is issued several times with different
	// filters — one per target group, say — so the results stay separable.
	Key     string
	Spec    domain.MetricSpec
	Filters map[string]string
}

// ResultKey is the key this request's series are grouped under.
func (r MetricRequest) ResultKey() string {
	if r.Key != "" {
		return r.Key
	}
	return r.Spec.Key
}

// MetricSeries is one time series returned for a request. A SEARCH expression
// matches many series at once — one per pod, node, or target group — so a
// single request usually yields several of these.
type MetricSeries struct {
	Key    string // the requesting spec's key
	Label  string // CloudWatch's label, e.g. the pod name
	Points map[int64]float64
}

// MetricFetcher runs GetMetricData against a MetricsAPI.
type MetricFetcher struct {
	API MetricsAPI

	// MaxQueries overrides MaxQueriesPerCall in tests.
	MaxQueries int
}

func (f *MetricFetcher) chunkSize() int {
	if f.MaxQueries > 0 {
		return f.MaxQueries
	}
	return MaxQueriesPerCall
}

// Fetch resolves every request over the window and returns the series grouped
// by spec key.
func (f *MetricFetcher) Fetch(ctx context.Context, w domain.Window, reqs []MetricRequest) (map[string][]MetricSeries, error) {
	out := map[string][]MetricSeries{}
	if len(reqs) == 0 {
		return out, nil
	}

	queries := make([]cwtypes.MetricDataQuery, 0, len(reqs))
	// byID maps a query identifier back to the request that asked for it.
	// Results are matched through this map rather than by position: the
	// reference implementation recovered an array index from the id with
	// Sscanf, ignored the parse error, and then indexed a slice with whatever
	// came out, so an unexpected id wrote into the wrong series and an
	// out-of-range one panicked the handler.
	byID := make(map[string]MetricRequest, len(reqs))

	for _, req := range reqs {
		expr, err := domain.SearchExpression(req.Spec, req.Filters, w.Period)
		if err != nil {
			return nil, fmt.Errorf("build search for %s: %w", req.ResultKey(), err)
		}
		id := domain.QueryID(req.ResultKey())
		if _, clash := byID[id]; clash {
			return nil, fmt.Errorf("duplicate query id %q for metric %s", id, req.ResultKey())
		}
		byID[id] = req
		queries = append(queries, cwtypes.MetricDataQuery{
			Id:         aws.String(id),
			Expression: aws.String(expr),
			ReturnData: aws.Bool(true),
			Period:     aws.Int32(w.Period.Seconds()),
		})
	}

	size := f.chunkSize()
	for start := 0; start < len(queries); start += size {
		end := min(start+size, len(queries))
		if err := f.fetchChunk(ctx, w, queries[start:end], byID, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (f *MetricFetcher) fetchChunk(
	ctx context.Context,
	w domain.Window,
	queries []cwtypes.MetricDataQuery,
	byID map[string]MetricRequest,
	out map[string][]MetricSeries,
) error {
	// Index the accumulating series so repeated pages and multiple chunks
	// append into the same series instead of creating duplicates.
	index := map[string]*MetricSeries{}
	for key, list := range out {
		for i := range list {
			index[key+"\x00"+list[i].Label] = &out[key][i]
		}
	}

	in := &cloudwatch.GetMetricDataInput{
		MetricDataQueries: queries,
		StartTime:         aws.Time(w.Start),
		EndTime:           aws.Time(w.End),
		ScanBy:            cwtypes.ScanByTimestampAscending,
	}

	for page := 0; ; page++ {
		if page >= maxPages {
			return fmt.Errorf("GetMetricData returned more than %d pages; refusing to keep walking", maxPages)
		}
		resp, err := f.API.GetMetricData(ctx, in)
		if err != nil {
			return fmt.Errorf("GetMetricData: %w", err)
		}

		for _, r := range resp.MetricDataResults {
			id := aws.ToString(r.Id)
			req, ok := byID[id]
			if !ok {
				// An id nobody asked for is a bug, not data. Say so rather
				// than writing it into an arbitrary series.
				return fmt.Errorf("GetMetricData returned unknown query id %q", id)
			}
			if len(r.Timestamps) != len(r.Values) {
				return fmt.Errorf("query %q returned %d timestamps for %d values",
					id, len(r.Timestamps), len(r.Values))
			}

			key := req.ResultKey()
			label := aws.ToString(r.Label)
			if label == "" {
				label = req.Spec.Label
			}
			k := key + "\x00" + label
			s, ok := index[k]
			if !ok {
				out[key] = append(out[key], MetricSeries{
					Key:    key,
					Label:  label,
					Points: map[int64]float64{},
				})
				s = &out[key][len(out[key])-1]
				index[k] = s
			}
			for i, ts := range r.Timestamps {
				s.Points[alignToBucket(ts, w)] = r.Values[i]
			}
		}

		if resp.NextToken == nil || *resp.NextToken == "" {
			return nil
		}
		in.NextToken = resp.NextToken
	}
}

// alignToBucket snaps a CloudWatch timestamp onto the window's grid. CloudWatch
// returns period-aligned timestamps already; snapping again costs nothing and
// makes a mismatched period show up as overwritten points rather than as a
// series that silently fails to plot.
func alignToBucket(ts time.Time, w domain.Window) int64 {
	return ts.UTC().Truncate(w.Period.Duration()).Unix()
}

// ToSeries projects a MetricSeries onto the window's axis, producing the
// gap-preserving representation the frontend plots.
func (m MetricSeries) ToSeries(w domain.Window, label string, unit domain.Unit, color string) *domain.Series {
	s := domain.NewSeries(label, unit, color, w.Buckets())
	for i, ts := range w.Timestamps() {
		if v, ok := m.Points[ts]; ok {
			s.Set(i, v)
		}
	}
	return s
}

// SortSeries orders series by label so a chart's legend does not reshuffle
// between refreshes.
func SortSeries(list []MetricSeries) {
	sort.Slice(list, func(i, j int) bool { return list[i].Label < list[j].Label })
}
