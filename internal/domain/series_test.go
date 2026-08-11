package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSeriesGapsSurviveAsNull(t *testing.T) {
	s := NewSeries("p99", UnitMillis, "systemBlue", 4)
	s.Set(0, 12.5)
	s.Set(2, 30)

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	// A gap must serialise as null, not 0: uPlot draws null as a break in the
	// line, whereas a zero reads as a real measurement.
	if !strings.Contains(got, `"values":[12.5,null,30,null]`) {
		t.Errorf("gaps did not survive marshalling: %s", got)
	}
}

func TestSeriesSetIgnoresOutOfRange(t *testing.T) {
	s := NewSeries("x", UnitCount, "", 3)
	s.Set(-1, 1)
	s.Set(3, 1)
	s.Set(99, 1)
	if s.Defined() != 0 {
		t.Errorf("out-of-range writes landed somewhere: %v", s.Values)
	}
}

func TestSeriesAddAccumulates(t *testing.T) {
	s := NewSeries("count", UnitCount, "", 2)
	s.Add(0, 2)
	s.Add(0, 3)
	s.Add(1, 5)
	if v := s.Values[0]; v == nil || *v != 5 {
		t.Errorf("bucket 0 = %v, want 5", v)
	}
	if v := s.Values[1]; v == nil || *v != 5 {
		t.Errorf("bucket 1 = %v, want 5", v)
	}
}

func TestSeriesReductions(t *testing.T) {
	s := NewSeries("x", UnitCount, "", 5)
	s.Set(1, 10)
	s.Set(2, 4)
	s.Set(4, 7)

	if v := s.Max(); v == nil || *v != 10 {
		t.Errorf("Max = %v, want 10", v)
	}
	if v := s.Min(); v == nil || *v != 4 {
		t.Errorf("Min = %v, want 4", v)
	}
	if v := s.Sum(); v == nil || *v != 21 {
		t.Errorf("Sum = %v, want 21", v)
	}
	if v := s.Avg(); v == nil || *v != 7 {
		t.Errorf("Avg = %v, want 7 (mean of defined samples only)", v)
	}
	// Last skips the trailing gap rather than reporting it as zero.
	if v := s.Last(); v == nil || *v != 7 {
		t.Errorf("Last = %v, want 7", v)
	}
}

func TestSeriesReductionsOnEmptySeries(t *testing.T) {
	s := NewSeries("x", UnitCount, "", 3)
	for name, got := range map[string]Point{
		"Max": s.Max(), "Min": s.Min(), "Sum": s.Sum(), "Avg": s.Avg(), "Last": s.Last(),
	} {
		if got != nil {
			t.Errorf("%s on an all-gap series = %v, want nil", name, *got)
		}
	}
}

// The headline number and the chart must be two views of one array. This is the
// property that broke in the reference implementation, where a tile reduced a
// differently-filtered array than the chart beside it plotted.
func TestStatDerivedFromTheSameSeriesItLabels(t *testing.T) {
	s := NewSeries("p99", UnitMillis, "systemBlue", 4)
	s.Set(0, 100)
	s.Set(1, 250)
	s.Set(3, 180)

	stat := Stat{Key: "p99_max", Label: "최대 p99", Value: s.Max(), Unit: s.Unit}
	if stat.Value == nil || *stat.Value != 250 {
		t.Fatalf("stat = %v, want the series max 250", stat.Value)
	}
	if stat.Unit != s.Unit {
		t.Errorf("stat unit %q drifted from series unit %q", stat.Unit, s.Unit)
	}
}

func TestNewTableComputesTruncationFromAnIndependentTotal(t *testing.T) {
	cols := []Column{{Key: "path", Label: "Path", Copyable: true}}
	rows := make([]Row, 300)
	for i := range rows {
		rows[i] = Row{"path": "/v1/user"}
	}

	// 1284 bad responses exist; only 300 were fetched.
	tbl := NewTable(cols, rows, 1284, 300)
	if tbl.Total != 1284 {
		t.Errorf("Total = %d, want the independently counted 1284", tbl.Total)
	}
	if int64(len(tbl.Rows)) == tbl.Total {
		t.Error("Total collapsed onto the row count, which is the bug this guards")
	}
	if !tbl.Truncated {
		t.Error("Truncated = false despite 1284 > 300")
	}

	// When everything fits, nothing is flagged.
	full := NewTable(cols, rows[:10], 10, 300)
	if full.Truncated {
		t.Error("Truncated = true when all rows were returned")
	}
}

func TestNewTableNeverMarshalsNullRows(t *testing.T) {
	tbl := NewTable(nil, nil, 0, 100)
	b, err := json.Marshal(tbl)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"rows":[]`) {
		t.Errorf("empty table marshalled rows as null: %s", b)
	}
}

func TestPayloadValidateCatchesMisalignedSeries(t *testing.T) {
	w, err := NewWindow(mustTime(t, "2026-08-10T10:00:00Z"), Range1h, Period5m)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPayload(w)
	p.Add(&Panel{ID: "latency", Series: []*Series{NewSeries("p99", UnitMillis, "", 11)}})

	err = p.Validate()
	if err == nil {
		t.Fatal("Validate accepted a series of 11 values against a 12-bucket axis")
	}
	if !strings.Contains(err.Error(), "11 values") {
		t.Errorf("error should name the mismatch, got: %v", err)
	}
}

func TestPayloadValidateCatchesTotalBelowRowCount(t *testing.T) {
	w, err := NewWindow(mustTime(t, "2026-08-10T10:00:00Z"), Range1h, Period5m)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPayload(w)
	p.Add(&Panel{
		ID: "bad",
		Table: &Table{
			Rows:  []Row{{"a": 1}, {"a": 2}, {"a": 3}},
			Total: 2, // impossible: fewer than the rows carried
		},
	})
	if err := p.Validate(); err == nil {
		t.Fatal("Validate accepted a total below the row count")
	}
}

func TestPayloadValidateAcceptsAWellFormedPayload(t *testing.T) {
	w, err := NewWindow(mustTime(t, "2026-08-10T10:00:00Z"), Range4h, Period5m)
	if err != nil {
		t.Fatal(err)
	}
	n := w.Buckets()
	p := NewPayload(w)
	p.Add(&Panel{
		ID:     "latency",
		Title:  "응답 시간",
		Series: []*Series{NewSeries("p50", UnitMillis, "systemBlue", n), NewSeries("p99", UnitMillis, "systemRed", n)},
		Stats:  []Stat{{Key: "p99_max", Label: "최대 p99", Unit: UnitMillis, Basis: "latency_ms > 0"}},
		Table:  NewTable([]Column{{Key: "path"}}, []Row{{"path": "/x"}}, 42, 100),
	})
	if err := p.Validate(); err != nil {
		t.Fatalf("well-formed payload rejected: %v", err)
	}
}

// Every panel in one payload shares a single window, so two panels cannot
// describe different spans the way independently-anchored handlers did.
func TestPayloadCarriesOneWindowForEveryPanel(t *testing.T) {
	w, err := NewWindow(mustTime(t, "2026-08-10T10:03:47Z"), Range2h, Period5m)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPayload(w)
	p.Add(&Panel{ID: "a"}, &Panel{ID: "b"}, &Panel{ID: "c"})

	var decoded struct {
		Window WindowJSON        `json:"window"`
		Panels []json.RawMessage `json:"panels"`
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Panels) != 3 {
		t.Fatalf("got %d panels, want 3", len(decoded.Panels))
	}
	if decoded.Window.Period != 300 {
		t.Errorf("window period = %d, want 300", decoded.Window.Period)
	}
	if got := len(decoded.Window.Timestamps); got != 24 {
		t.Errorf("timestamps = %d, want 24", got)
	}
	// The axis is transmitted once, not per panel.
	if strings.Count(string(b), `"timestamps"`) != 1 {
		t.Error("timestamps appear more than once; series could drift onto different axes")
	}
}

func TestWindowJSONMatchesWindow(t *testing.T) {
	w, err := NewWindow(mustTime(t, "2026-08-10T10:03:47Z"), Range4h, Period10m)
	if err != nil {
		t.Fatal(err)
	}
	j := w.JSON()
	if j.Start != w.Start.Unix() || j.End != w.End.Unix() {
		t.Errorf("JSON bounds %d..%d disagree with window %s", j.Start, j.End, w)
	}
	if j.Period != 600 {
		t.Errorf("period = %d, want 600", j.Period)
	}
	if j.Range != "4h" {
		t.Errorf("range = %q, want \"4h\"", j.Range)
	}
	if len(j.Timestamps) != w.Buckets() {
		t.Errorf("timestamps = %d, want %d", len(j.Timestamps), w.Buckets())
	}
}

// Colour names the subject on a fan-out panel, so the metric has to be readable
// from the line itself. The first metric stays solid: a panel that never sets a
// dash must look exactly as it did before dashes existed.
func TestVariantDashSeparatesTheMetricsOnOnePanel(t *testing.T) {
	if got := VariantDash(0); got != DashSolid {
		t.Errorf("VariantDash(0) = %q, want a solid line", got)
	}
	if VariantDash(1) == VariantDash(0) {
		t.Error("the second metric on a panel draws the same line as the first")
	}
	if VariantDash(2) == VariantDash(1) || VariantDash(2) == VariantDash(0) {
		t.Error("the third metric reuses a line pattern already on the chart")
	}
	if got, want := VariantDash(3), VariantDash(0); got != want {
		t.Errorf("VariantDash(3) = %q, want it to cycle back to %q", got, want)
	}
	if got := VariantDash(-1); got != DashSolid {
		t.Errorf("VariantDash(-1) = %q, want the solid default", got)
	}
}

// The zero value has to be the solid line, or every panel that does not set a
// dash silently changes how it draws.
func TestASeriesWithNoDashIsSolidAndOmittedFromTheWire(t *testing.T) {
	s := NewSeries("p99", UnitMillis, "systemBlue", 2)
	if s.Dash != DashSolid {
		t.Errorf("a fresh series has dash %q, want solid", s.Dash)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "dash") {
		t.Errorf("a solid series serialises its dash: %s", b)
	}
}
