// Package domain holds the pure logic of the dashboard: how a time window is
// chosen, how CloudWatch Logs Insights queries are built, and how AWS responses
// are shaped for the frontend. Nothing here talks to AWS or to HTTP, so all of
// it is directly testable.
package domain

import (
	"fmt"
	"strings"
	"time"
)

// Range is how far back a view looks. Four hours is a hard product limit.
type Range time.Duration

// Period is the width of one bucket on the time axis.
type Period time.Duration

const (
	Range15m Range = Range(15 * time.Minute)
	Range30m Range = Range(30 * time.Minute)
	Range1h  Range = Range(time.Hour)
	Range2h  Range = Range(2 * time.Hour)
	Range4h  Range = Range(4 * time.Hour)

	Period1m  Period = Period(time.Minute)
	Period5m  Period = Period(5 * time.Minute)
	Period10m Period = Period(10 * time.Minute)
	Period1h  Period = Period(time.Hour)
)

// MaxRange is the ceiling the product imposes on every log and metric query.
// Bounding the window is what keeps a Logs Insights scan predictable, so this
// is enforced server-side and not merely offered as the largest UI option.
const MaxRange = 4 * time.Hour

// A window must hold enough buckets to read as a series, and few enough that
// the browser is not asked to plot noise. Both bounds are inclusive.
const (
	minBuckets = 4
	maxBuckets = 250
)

var (
	allRanges  = []Range{Range15m, Range30m, Range1h, Range2h, Range4h}
	allPeriods = []Period{Period1m, Period5m, Period10m, Period1h}

	// defaultPeriods favours resolution while keeping bucket counts modest.
	defaultPeriods = map[Range]Period{
		Range15m: Period1m,
		Range30m: Period1m,
		Range1h:  Period1m,
		Range2h:  Period1m,
		Range4h:  Period5m,
	}
)

func (r Range) Duration() time.Duration  { return time.Duration(r) }
func (p Period) Duration() time.Duration { return time.Duration(p) }

// String renders the compact form used on the wire and in the URL: "15m", "4h".
func (r Range) String() string  { return compactDuration(time.Duration(r)) }
func (p Period) String() string { return compactDuration(time.Duration(p)) }

// Seconds is the unit CloudWatch's Period parameter expects.
func (p Period) Seconds() int32 { return int32(time.Duration(p) / time.Second) }

func compactDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}

// Ranges lists every selectable range, shortest first.
func Ranges() []Range { return append([]Range(nil), allRanges...) }

// ParseRange accepts the compact form and rejects anything outside the fixed
// set. Free-form durations are deliberately not supported: an arbitrary range
// would let a caller slip past MaxRange by a rounding error, and it would make
// the range/period compatibility table unbounded.
func ParseRange(s string) (Range, error) {
	for _, r := range allRanges {
		if r.String() == s {
			return r, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("unknown range %q", s)
	}
	if d > MaxRange {
		return 0, fmt.Errorf("range %s exceeds the %s maximum", s, compactDuration(MaxRange))
	}
	return 0, fmt.Errorf("unsupported range %q; allowed: %s", s, joinRanges(allRanges))
}

// ParsePeriod accepts the compact form of a bucket width.
func ParsePeriod(s string) (Period, error) {
	for _, p := range allPeriods {
		if p.String() == s {
			return p, nil
		}
	}
	return 0, fmt.Errorf("unsupported period %q; allowed: %s", s, joinPeriods(allPeriods))
}

// PeriodsFor lists the bucket widths that produce a sensible number of buckets
// for r. Callers use it to build the UI selector, so the frontend can never
// offer a combination the server would reject.
func PeriodsFor(r Range) []Period {
	var out []Period
	for _, p := range allPeriods {
		if n := buckets(r, p); n >= minBuckets && n <= maxBuckets {
			out = append(out, p)
		}
	}
	return out
}

// DefaultPeriod is the bucket width used when a request omits one.
func DefaultPeriod(r Range) Period {
	if p, ok := defaultPeriods[r]; ok {
		return p
	}
	// Fall back to the coarsest period that still clears minBuckets.
	valid := PeriodsFor(r)
	if len(valid) == 0 {
		return Period1m
	}
	return valid[len(valid)-1]
}

func buckets(r Range, p Period) int {
	if p <= 0 {
		return 0
	}
	return int(time.Duration(r) / time.Duration(p))
}

// Window is the single time window that every panel in one response shares.
//
// The reference implementation anchored each panel to its own time.Now(), so a
// dashboard could show a metric chart and a log chart describing windows that
// were seconds apart and bucketed differently. Building the window once at the
// edge of a request and threading it through is what makes two panels — and the
// overview and detail views of the same panel — agree.
type Window struct {
	Start  time.Time
	End    time.Time
	Range  Range
	Period Period
}

// NewWindow builds the window for r/p as of now.
//
// End is floored to a period boundary and Start is derived from it, so every
// bucket in the window is a complete one. The reference implementation used a
// raw now for the query bound while bucketing with (ts/width)*width, which left
// the newest bucket partially filled and the oldest one truncated — the UI then
// worked around it by reading the second-to-last bucket in one place and the
// last one in another, which is precisely how the same number came to differ
// between two spots on the same screen.
func NewWindow(now time.Time, r Range, p Period) (Window, error) {
	if err := Validate(r, p); err != nil {
		return Window{}, err
	}
	end := now.UTC().Truncate(p.Duration())
	return Window{
		Start:  end.Add(-r.Duration()),
		End:    end,
		Range:  r,
		Period: p,
	}, nil
}

// Validate reports whether r and p may be combined.
func Validate(r Range, p Period) error {
	if r.Duration() <= 0 {
		return fmt.Errorf("range must be positive")
	}
	if r.Duration() > MaxRange {
		return fmt.Errorf("range %s exceeds the %s maximum", r, compactDuration(MaxRange))
	}
	if p.Duration() <= 0 {
		return fmt.Errorf("period must be positive")
	}
	n := buckets(r, p)
	if n < minBuckets {
		return fmt.Errorf("period %s over range %s yields %d buckets, fewer than the %d minimum", p, r, n, minBuckets)
	}
	if n > maxBuckets {
		return fmt.Errorf("period %s over range %s yields %d buckets, more than the %d maximum", p, r, n, maxBuckets)
	}
	if time.Duration(r)%time.Duration(p) != 0 {
		return fmt.Errorf("period %s does not divide range %s evenly", p, r)
	}
	return nil
}

// Buckets is the number of points on the time axis.
func (w Window) Buckets() int { return buckets(w.Range, w.Period) }

// Timestamps lists the left edge of every bucket as Unix seconds, ascending.
// This is the x axis the frontend plots against and the index every series in
// the response is aligned to.
func (w Window) Timestamps() []int64 {
	n := w.Buckets()
	out := make([]int64, n)
	step := int64(w.Period.Duration() / time.Second)
	start := w.Start.Unix()
	for i := range out {
		out[i] = start + int64(i)*step
	}
	return out
}

// Index returns the bucket t falls in, and whether it falls in the window at
// all. Points outside are dropped rather than clamped: clamping would silently
// pile out-of-window data onto the edge buckets and inflate them.
func (w Window) Index(t time.Time) (int, bool) {
	if t.Before(w.Start) || !t.Before(w.End) {
		return 0, false
	}
	return int(t.Sub(w.Start) / w.Period.Duration()), true
}

// Contains reports whether t falls inside the window.
func (w Window) Contains(t time.Time) bool {
	_, ok := w.Index(t)
	return ok
}

// Align snaps t down to its bucket's left edge.
func (w Window) Align(t time.Time) time.Time {
	return t.UTC().Truncate(w.Period.Duration())
}

// Equal reports whether two windows describe the same span at the same
// resolution. Handlers assert this across the panels they assemble.
func (w Window) Equal(o Window) bool {
	return w.Start.Equal(o.Start) && w.End.Equal(o.End) && w.Period == o.Period
}

func (w Window) String() string {
	return fmt.Sprintf("%s..%s/%s", w.Start.Format(time.RFC3339), w.End.Format(time.RFC3339), w.Period)
}

// Resolve turns raw query-string values into a window, filling in defaults.
// An empty range means the 1h default; an empty period means the default for
// the resolved range.
func Resolve(now time.Time, rangeStr, periodStr string) (Window, error) {
	r := Range1h
	if rangeStr != "" {
		parsed, err := ParseRange(rangeStr)
		if err != nil {
			return Window{}, err
		}
		r = parsed
	}
	p := DefaultPeriod(r)
	if periodStr != "" {
		parsed, err := ParsePeriod(periodStr)
		if err != nil {
			return Window{}, err
		}
		p = parsed
	}
	return NewWindow(now, r, p)
}

func joinRanges(rs []Range) string {
	s := make([]string, len(rs))
	for i, r := range rs {
		s[i] = r.String()
	}
	return strings.Join(s, ", ")
}

func joinPeriods(ps []Period) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.String()
	}
	return strings.Join(s, ", ")
}
