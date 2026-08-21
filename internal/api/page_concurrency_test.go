package api

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/jhyeok1023/skills-dashboard/internal/awsx"
	"github.com/jhyeok1023/skills-dashboard/internal/domain"
)

// TestPageKeepsItsDeclaredPanelOrder pins the guarantee the concurrent build
// has to make. The frontend renders payload.panels in array order and sizes the
// grid around which of them are wide, so a page whose panels arrive in
// completion order rearranges itself according to which query happened to be
// slow — a layout that changes between two refreshes of the same screen.
func TestPageKeepsItsDeclaredPanelOrder(t *testing.T) {
	_, h := newTestService(t)

	for page, want := range pages {
		payload := decodePayload(t, get(t, h, "/api/page/"+page+"?range=1h&period=1m"))
		got := make([]string, 0, len(payload.Panels))
		for _, p := range payload.Panels {
			got = append(got, p.ID)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("page %q returned panels %v, want %v", page, got, want)
		}
	}
}

// TestPanelsAreBuiltConcurrently is the whole point of the change: a page must
// cost its slowest panel, not the sum of all of them.
//
// The threshold is deliberately loose. What would fail here is a return to
// sequential building — five 60ms panels taking 300ms rather than 60 — and that
// is an order of magnitude away from the bound, so this does not become the
// test that fails on a loaded CI box.
func TestPanelsAreBuiltConcurrently(t *testing.T) {
	const (
		panels = 5
		delay  = 60 * time.Millisecond
	)

	svc, _ := newTestService(t)
	ids := make([]string, 0, panels)
	builders := map[string]panelBuilder{}
	var peak, live int64
	var mu sync.Mutex
	for i := range panels {
		id := string(rune('a' + i))
		ids = append(ids, id)
		builders[id] = func(requestCtx) (*domain.Panel, error) {
			n := atomic.AddInt64(&live, 1)
			mu.Lock()
			if n > peak {
				peak = n
			}
			mu.Unlock()
			time.Sleep(delay)
			atomic.AddInt64(&live, -1)
			return &domain.Panel{ID: id, Title: id}, nil
		}
	}

	started := time.Now()
	got := svc.buildPanels(requestCtx{ctx: t.Context()}, "test", ids, builders)
	elapsed := time.Since(started)

	if elapsed >= panels*delay {
		t.Errorf("building %d panels took %s, which is the sequential cost — they did not overlap",
			panels, elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak < panels {
		t.Errorf("at most %d builders ran at once, want %d", peak, panels)
	}
	if len(got) != panels {
		t.Fatalf("got %d panels, want %d", len(got), panels)
	}
}

// TestAPanickingPanelDoesNotTakeTheProcessDown covers the hazard the concurrent
// build introduces. recoverPanics wraps the handler's goroutine; a builder now
// runs on its own, where that middleware cannot see it. Without a recover of
// its own, one bad panel ends the dashboard instead of one card.
func TestAPanickingPanelDoesNotTakeTheProcessDown(t *testing.T) {
	svc, _ := newTestService(t)

	builders := map[string]panelBuilder{
		"fine": func(requestCtx) (*domain.Panel, error) {
			return &domain.Panel{ID: "fine", Title: "fine"}, nil
		},
		"boom": func(requestCtx) (*domain.Panel, error) {
			panic("a builder dereferenced something absent")
		},
		"failing": func(requestCtx) (*domain.Panel, error) {
			return nil, errors.New("upstream said no")
		},
	}
	ids := []string{"fine", "boom", "failing"}

	got := svc.buildPanels(requestCtx{ctx: t.Context()}, "test", ids, builders)
	if len(got) != 3 {
		t.Fatalf("got %d panels, want 3", len(got))
	}
	for i, id := range ids {
		if got[i] == nil {
			t.Fatalf("panel %q is missing; a panicking neighbour blanked it", id)
		}
		if got[i].ID != id {
			t.Errorf("slot %d holds %q, want %q", i, got[i].ID, id)
		}
	}
	// The healthy panel is untouched, and both broken ones explain themselves
	// rather than arriving as a silently empty card.
	if len(got[0].Warnings) != 0 {
		t.Errorf("the healthy panel carries warnings: %v", got[0].Warnings)
	}
	if len(got[1].Warnings) == 0 {
		t.Error("the panicking panel arrived without a warning saying so")
	}
	if len(got[2].Warnings) == 0 {
		t.Error("the failing panel arrived without a warning saying so")
	}
}

// TestATotalQueryFailureExpiresUnderTheErrorTTL covers a bug that outlived its
// cause: runLogQueries packs per-query errors into its cached value and used to
// return a nil error alongside them, so awsx.Cache filed a wave in which
// nothing came back as a perfectly good result. A throttle or an expired
// credential then stayed on screen for the full TTL after it had cleared.
func TestATotalQueryFailureExpiresUnderTheErrorTTL(t *testing.T) {
	svc, _ := newTestService(t)

	now := testNow
	svc.Cache = &awsx.Cache{
		TTL:      time.Minute,
		ErrorTTL: 5 * time.Second,
		Now:      func() time.Time { return now },
	}
	failing := newFailingLogs()
	src := logSource{
		runner: &awsx.InsightsRunner{API: failing, Concurrency: 2, PollInterval: time.Millisecond},
		group:  "aws-waf-logs-demo",
		region: "us-east-1",
	}
	win, err := domain.NewWindow(testNow, domain.Range(time.Hour), domain.Period(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	rc := requestCtx{ctx: t.Context(), w: win, cfg: svc.Store.Get()}
	qs := []domain.Query{{ID: "one", Text: "fields @timestamp"}, {ID: "two", Text: "fields @message"}}

	// The per-query errors still reach the caller, so the panel can name what
	// broke rather than reporting a bare cache miss.
	_, errs := svc.runLogQueries(rc, src, "waf-test", qs)
	if len(errs) != len(qs) {
		t.Fatalf("got %d query errors, want %d", len(errs), len(qs))
	}
	first := failing.starts.Load()
	if first == 0 {
		t.Fatal("no query was issued")
	}

	// Inside the error TTL the failure is remembered, so a refresh does not
	// hammer a dependency that is already known to be down.
	svc.runLogQueries(rc, src, "waf-test", qs)
	if got := failing.starts.Load(); got != first {
		t.Errorf("a repeat inside the error TTL issued %d queries, want the cached failure", got-first)
	}

	// Past it, the next request tries again — which is the half that was
	// broken: the entry used to live for the full TTL instead.
	now = now.Add(10 * time.Second)
	svc.runLogQueries(rc, src, "waf-test", qs)
	if got := failing.starts.Load(); got == first {
		t.Error("the failure was still cached after the error TTL elapsed")
	}
}

// failingLogs starts queries happily and then reports every one of them failed,
// which is what a throttled account looks like from here.
type failingLogs struct {
	stubLogs
	starts atomic.Int64
}

func newFailingLogs() *failingLogs {
	return &failingLogs{stubLogs: *newStubLogs("failing", "aws-waf-logs-demo")}
}

func (f *failingLogs) StartQuery(ctx context.Context, in *cloudwatchlogs.StartQueryInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error) {
	f.starts.Add(1)
	return f.stubLogs.StartQuery(ctx, in, opts...)
}

func (f *failingLogs) GetQueryResults(context.Context, *cloudwatchlogs.GetQueryResultsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error) {
	return &cloudwatchlogs.GetQueryResultsOutput{Status: logtypes.QueryStatusFailed}, nil
}
