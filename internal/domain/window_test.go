package domain

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v.UTC()
}

func TestMaxRangeIsFourHours(t *testing.T) {
	if MaxRange != 4*time.Hour {
		t.Fatalf("MaxRange = %v, want 4h", MaxRange)
	}
	if Range4h.Duration() != MaxRange {
		t.Fatalf("largest selectable range %v != MaxRange %v", Range4h.Duration(), MaxRange)
	}
	for _, r := range Ranges() {
		if r.Duration() > MaxRange {
			t.Errorf("selectable range %s exceeds MaxRange", r)
		}
	}
}

func TestParseRangeRejectsBeyondMax(t *testing.T) {
	for _, in := range []string{"6h", "24h", "5h"} {
		if _, err := ParseRange(in); err == nil {
			t.Errorf("ParseRange(%q) = nil error, want rejection", in)
		}
	}
}

func TestParseRange(t *testing.T) {
	tests := []struct {
		in      string
		want    Range
		wantErr bool
	}{
		{in: "15m", want: Range15m},
		{in: "30m", want: Range30m},
		{in: "1h", want: Range1h},
		{in: "2h", want: Range2h},
		{in: "4h", want: Range4h},
		{in: "3h", wantErr: true},  // inside the cap but not offered
		{in: "90m", wantErr: true}, // ditto
		{in: "", wantErr: true},
		{in: "banana", wantErr: true},
	}
	for _, tc := range tests {
		got, err := ParseRange(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseRange(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRange(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseRange(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestValidateCombinations(t *testing.T) {
	tests := []struct {
		r    Range
		p    Period
		want bool // valid
	}{
		{Range15m, Period1m, true},   // 15 buckets
		{Range15m, Period5m, false},  // 3 buckets, below the minimum
		{Range15m, Period10m, false}, // does not divide evenly either
		{Range30m, Period1m, true},   // 30
		{Range30m, Period5m, true},   // 6
		{Range30m, Period10m, false}, // 3
		{Range1h, Period1m, true},    // 60
		{Range1h, Period5m, true},    // 12
		{Range1h, Period10m, true},   // 6
		{Range1h, Period1h, false},   // 1
		{Range2h, Period1m, true},    // 120
		{Range2h, Period10m, true},   // 12
		{Range2h, Period1h, false},   // 2
		{Range4h, Period1m, true},    // 240, just under the ceiling
		{Range4h, Period5m, true},    // 48
		{Range4h, Period10m, true},   // 24
		{Range4h, Period1h, true},    // 4, exactly the minimum
	}
	for _, tc := range tests {
		err := Validate(tc.r, tc.p)
		if tc.want && err != nil {
			t.Errorf("Validate(%s, %s) = %v, want valid", tc.r, tc.p, err)
		}
		if !tc.want && err == nil {
			t.Errorf("Validate(%s, %s) = nil, want rejection", tc.r, tc.p)
		}
	}
}

func TestPeriodsForOnlyOffersValidCombinations(t *testing.T) {
	for _, r := range Ranges() {
		ps := PeriodsFor(r)
		if len(ps) == 0 {
			t.Errorf("PeriodsFor(%s) is empty; the UI would have nothing to offer", r)
		}
		for _, p := range ps {
			if err := Validate(r, p); err != nil {
				t.Errorf("PeriodsFor(%s) offered %s, which Validate rejects: %v", r, p, err)
			}
		}
	}
}

func TestDefaultPeriodIsAlwaysValid(t *testing.T) {
	for _, r := range Ranges() {
		p := DefaultPeriod(r)
		if err := Validate(r, p); err != nil {
			t.Errorf("DefaultPeriod(%s) = %s, which Validate rejects: %v", r, p, err)
		}
	}
}

// The reference implementation anchored the window to a raw now, so the newest
// bucket was always partially filled and the oldest was truncated to whatever
// fraction happened to be left. Flooring End to a period boundary is what makes
// every bucket in the window a complete one.
func TestNewWindowFloorsEndToPeriodBoundary(t *testing.T) {
	tests := []struct {
		now     string
		r       Range
		p       Period
		wantEnd string
		wantSta string
	}{
		{
			now: "2026-08-10T10:03:47Z", r: Range1h, p: Period1m,
			wantEnd: "2026-08-10T10:03:00Z", wantSta: "2026-08-10T09:03:00Z",
		},
		{
			now: "2026-08-10T10:03:47Z", r: Range1h, p: Period5m,
			wantEnd: "2026-08-10T10:00:00Z", wantSta: "2026-08-10T09:00:00Z",
		},
		{
			now: "2026-08-10T10:47:12Z", r: Range4h, p: Period10m,
			wantEnd: "2026-08-10T10:40:00Z", wantSta: "2026-08-10T06:40:00Z",
		},
		{
			now: "2026-08-10T10:47:12Z", r: Range4h, p: Period1h,
			wantEnd: "2026-08-10T10:00:00Z", wantSta: "2026-08-10T06:00:00Z",
		},
		{
			// Already on a boundary: nothing should move.
			now: "2026-08-10T10:00:00Z", r: Range2h, p: Period5m,
			wantEnd: "2026-08-10T10:00:00Z", wantSta: "2026-08-10T08:00:00Z",
		},
	}
	for _, tc := range tests {
		w, err := NewWindow(mustTime(t, tc.now), tc.r, tc.p)
		if err != nil {
			t.Fatalf("NewWindow(%s, %s, %s): %v", tc.now, tc.r, tc.p, err)
		}
		if want := mustTime(t, tc.wantEnd); !w.End.Equal(want) {
			t.Errorf("now=%s %s/%s: End = %s, want %s", tc.now, tc.r, tc.p, w.End, want)
		}
		if want := mustTime(t, tc.wantSta); !w.Start.Equal(want) {
			t.Errorf("now=%s %s/%s: Start = %s, want %s", tc.now, tc.r, tc.p, w.Start, want)
		}
		if got := w.End.Sub(w.Start); got != tc.r.Duration() {
			t.Errorf("now=%s %s/%s: span = %v, want %v", tc.now, tc.r, tc.p, got, tc.r.Duration())
		}
	}
}

func TestWindowTimestampsCoverEveryBucketExactlyOnce(t *testing.T) {
	w, err := NewWindow(mustTime(t, "2026-08-10T10:03:47Z"), Range1h, Period5m)
	if err != nil {
		t.Fatal(err)
	}
	ts := w.Timestamps()
	if len(ts) != 12 {
		t.Fatalf("got %d buckets, want 12", len(ts))
	}
	if len(ts) != w.Buckets() {
		t.Fatalf("Timestamps() length %d disagrees with Buckets() %d", len(ts), w.Buckets())
	}
	if ts[0] != w.Start.Unix() {
		t.Errorf("first bucket = %d, want Start %d", ts[0], w.Start.Unix())
	}
	// The last bucket starts one period before End, so the window never
	// includes an in-flight bucket.
	if want := w.End.Add(-w.Period.Duration()).Unix(); ts[len(ts)-1] != want {
		t.Errorf("last bucket = %d, want %d", ts[len(ts)-1], want)
	}
	for i := 1; i < len(ts); i++ {
		if got := ts[i] - ts[i-1]; got != 300 {
			t.Errorf("gap between bucket %d and %d = %ds, want 300s", i-1, i, got)
		}
	}
}

func TestWindowIndexDropsOutOfWindowPoints(t *testing.T) {
	w, err := NewWindow(mustTime(t, "2026-08-10T10:00:00Z"), Range1h, Period5m)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		at      string
		wantIdx int
		wantOK  bool
	}{
		{at: "2026-08-10T09:00:00Z", wantIdx: 0, wantOK: true},  // exactly Start
		{at: "2026-08-10T09:04:59Z", wantIdx: 0, wantOK: true},  // still bucket 0
		{at: "2026-08-10T09:05:00Z", wantIdx: 1, wantOK: true},  // boundary rolls over
		{at: "2026-08-10T09:59:59Z", wantIdx: 11, wantOK: true}, // last bucket
		{at: "2026-08-10T10:00:00Z", wantOK: false},             // End is exclusive
		{at: "2026-08-10T08:59:59Z", wantOK: false},             // before Start
		{at: "2026-08-10T11:00:00Z", wantOK: false},             // well past End
	}
	for _, tc := range tests {
		idx, ok := w.Index(mustTime(t, tc.at))
		if ok != tc.wantOK {
			t.Errorf("Index(%s) ok = %v, want %v", tc.at, ok, tc.wantOK)
			continue
		}
		if ok && idx != tc.wantIdx {
			t.Errorf("Index(%s) = %d, want %d", tc.at, idx, tc.wantIdx)
		}
	}
}

func TestResolveDefaults(t *testing.T) {
	now := mustTime(t, "2026-08-10T10:03:47Z")

	w, err := Resolve(now, "", "")
	if err != nil {
		t.Fatalf("Resolve with no arguments: %v", err)
	}
	if w.Range != Range1h {
		t.Errorf("default range = %s, want 1h", w.Range)
	}
	if w.Period != DefaultPeriod(Range1h) {
		t.Errorf("default period = %s, want %s", w.Period, DefaultPeriod(Range1h))
	}

	w, err = Resolve(now, "4h", "")
	if err != nil {
		t.Fatalf("Resolve(4h): %v", err)
	}
	if w.Period != DefaultPeriod(Range4h) {
		t.Errorf("period for 4h = %s, want %s", w.Period, DefaultPeriod(Range4h))
	}

	if _, err := Resolve(now, "4h", "1m"); err != nil {
		t.Errorf("Resolve(4h, 1m) should be accepted: %v", err)
	}
	if _, err := Resolve(now, "15m", "10m"); err == nil {
		t.Error("Resolve(15m, 10m) should be rejected")
	}
	if _, err := Resolve(now, "8h", ""); err == nil {
		t.Error("Resolve(8h) should be rejected by the 4h cap")
	}
}

// Two panels assembled in the same request must describe the same span. This is
// the property that keeps an overview tile and its detail chart in agreement.
func TestWindowEqualityIsStructural(t *testing.T) {
	now := mustTime(t, "2026-08-10T10:03:47Z")
	a, err := NewWindow(now, Range1h, Period5m)
	if err != nil {
		t.Fatal(err)
	}
	// 10:03:47 and 10:04:47 both floor to 10:00, so the windows must match
	// even though the wall clocks differ. This is what lets two handlers in
	// one request agree without passing a timestamp between them.
	b, err := NewWindow(now.Add(60*time.Second), Range1h, Period5m)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Equal(b) {
		t.Errorf("windows built 60s apart within one period differ: %s vs %s", a, b)
	}

	// Crossing a period boundary must move the window, otherwise the view
	// would go stale without anyone noticing.
	d, err := NewWindow(now.Add(90*time.Second), Range1h, Period5m)
	if err != nil {
		t.Fatal(err)
	}
	if a.Equal(d) {
		t.Error("window did not advance across a period boundary")
	}

	c, err := NewWindow(now, Range1h, Period10m)
	if err != nil {
		t.Fatal(err)
	}
	if a.Equal(c) {
		t.Error("windows with different periods compared equal")
	}
}

func TestCompactDurationRoundTrips(t *testing.T) {
	for _, r := range Ranges() {
		got, err := ParseRange(r.String())
		if err != nil {
			t.Errorf("ParseRange(%q) failed to round-trip: %v", r.String(), err)
			continue
		}
		if got != r {
			t.Errorf("round trip of %s produced %s", r, got)
		}
	}
	for _, p := range allPeriods {
		got, err := ParsePeriod(p.String())
		if err != nil {
			t.Errorf("ParsePeriod(%q) failed to round-trip: %v", p.String(), err)
			continue
		}
		if got != p {
			t.Errorf("round trip of %s produced %s", p, got)
		}
	}
}

func TestPeriodSeconds(t *testing.T) {
	tests := []struct {
		p    Period
		want int32
	}{
		{Period1m, 60},
		{Period5m, 300},
		{Period10m, 600},
		{Period1h, 3600},
	}
	for _, tc := range tests {
		if got := tc.p.Seconds(); got != tc.want {
			t.Errorf("%s.Seconds() = %d, want %d", tc.p, got, tc.want)
		}
	}
}
