package domain

import (
	"fmt"
	"math"
)

// Unit tags a number so the frontend can format it without knowing where it
// came from. Conversion happens here, never in a Svelte template: the reference
// implementation multiplied ALB seconds by 1000 inside the view while app
// latency arrived already in milliseconds, which left two numbers on one screen
// whose units could only be reconciled by reading the markup.
type Unit string

const (
	UnitNone    Unit = ""
	UnitMillis  Unit = "ms"
	UnitSeconds Unit = "s"
	UnitPercent Unit = "%"
	UnitCount   Unit = "count"
	UnitPerSec  Unit = "/s"
	UnitBytes   Unit = "bytes"
	UnitConns   Unit = "conn"
)

// Intent colours a value by what it means, not by which panel it sits on.
type Intent string

const (
	IntentNeutral Intent = "neutral"
	IntentGood    Intent = "good"
	IntentWarn    Intent = "warn"
	IntentBad     Intent = "bad"
)

// Point is one sample. A nil Point is a genuine gap in the data and stays nil
// all the way to uPlot, which renders it as a break in the line. Filling gaps
// with zero would turn "CloudWatch had nothing here" into "the value was zero",
// which reads as a traffic collapse on a latency chart.
type Point = *float64

// P boxes a float into a Point.
func P(v float64) Point { return &v }

// Series is one line on a chart, aligned index-for-index with the enclosing
// payload's Timestamps.
type Series struct {
	Label  string  `json:"label"`
	Unit   Unit    `json:"unit"`
	Color  string  `json:"color,omitempty"`
	Values []Point `json:"values"`
}

// NewSeries allocates a series of n gaps, ready to be filled by index.
func NewSeries(label string, unit Unit, color string, n int) *Series {
	return &Series{Label: label, Unit: unit, Color: color, Values: make([]Point, n)}
}

// Set writes v at i, ignoring out-of-range indices so a stray CloudWatch
// timestamp cannot panic a handler.
func (s *Series) Set(i int, v float64) {
	if i < 0 || i >= len(s.Values) {
		return
	}
	s.Values[i] = P(v)
}

// Add accumulates into bucket i, treating a gap as zero. Use it for counts,
// where several source rows land in the same bucket.
func (s *Series) Add(i int, v float64) {
	if i < 0 || i >= len(s.Values) {
		return
	}
	if s.Values[i] == nil {
		s.Values[i] = P(v)
		return
	}
	*s.Values[i] += v
}

// Defined counts the non-gap samples.
func (s *Series) Defined() int {
	n := 0
	for _, v := range s.Values {
		if v != nil {
			n++
		}
	}
	return n
}

// Max, Min, Sum, Avg and Last reduce a series to a single number, returning nil
// when the series is entirely gaps.
//
// Every headline number on the dashboard goes through one of these. That is the
// point: the overview tile and the detail chart read the same reduction of the
// same series, so they cannot disagree the way the reference implementation's
// hand-rolled client-side reductions did.

func (s *Series) Max() Point { return s.reduce(func(a, b float64) float64 { return math.Max(a, b) }) }
func (s *Series) Min() Point { return s.reduce(func(a, b float64) float64 { return math.Min(a, b) }) }
func (s *Series) Sum() Point { return s.reduce(func(a, b float64) float64 { return a + b }) }

func (s *Series) Avg() Point {
	sum, n := 0.0, 0
	for _, v := range s.Values {
		if v != nil {
			sum += *v
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return P(sum / float64(n))
}

// Last is the newest defined sample. Because NewWindow floors End to a period
// boundary, the final bucket is a complete one — there is no need for the
// "second-to-last bucket" fudge the reference implementation used to dodge its
// own partially-filled tail.
func (s *Series) Last() Point {
	for i := len(s.Values) - 1; i >= 0; i-- {
		if s.Values[i] != nil {
			return s.Values[i]
		}
	}
	return nil
}

func (s *Series) reduce(f func(a, b float64) float64) Point {
	var acc *float64
	for _, v := range s.Values {
		if v == nil {
			continue
		}
		if acc == nil {
			acc = P(*v)
			continue
		}
		*acc = f(*acc, *v)
	}
	return acc
}

// Stat is a single number rendered as text somewhere on the dashboard.
//
// Basis names the population the number was computed over. Two "request count"
// stats drawn from different filters are a real thing that happens — the
// reference implementation had exactly that, one counting rows with a status
// and another counting rows with a latency — and carrying the basis on the wire
// makes the difference visible in the UI instead of leaving it to be discovered
// as a bug.
type Stat struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Value  Point  `json:"value"`
	Unit   Unit   `json:"unit"`
	Text   string `json:"text,omitempty"`
	Basis  string `json:"basis,omitempty"`
	Intent Intent `json:"intent,omitempty"`
}

// Column describes one table column, including whether its cells are worth
// offering a copy button for.
type Column struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Unit     Unit   `json:"unit,omitempty"`
	Copyable bool   `json:"copyable,omitempty"`
	Mono     bool   `json:"mono,omitempty"`
	Numeric  bool   `json:"numeric,omitempty"`
}

// Row is one table row keyed by Column.Key.
type Row map[string]any

// Table is a list view plus the honest size of the thing it lists.
//
// Total is not derived from Rows. The reference implementation displayed
// `errorLogs.length` as a total while that array was capped at 300 by the SQL
// underneath, so the headline silently stopped counting past 300 and disagreed
// with the aggregate shown beside it. Requiring the caller to supply Total
// separately is what prevents that shape of bug from being written again.
type Table struct {
	Columns   []Column `json:"columns"`
	Rows      []Row    `json:"rows"`
	Total     int64    `json:"total"`
	Truncated bool     `json:"truncated"`
	Limit     int      `json:"limit"`
}

// NewTable pairs rows with a total that was counted independently of them.
func NewTable(cols []Column, rows []Row, total int64, limit int) *Table {
	if rows == nil {
		rows = []Row{}
	}
	return &Table{
		Columns:   cols,
		Rows:      rows,
		Total:     total,
		Truncated: total > int64(len(rows)),
		Limit:     limit,
	}
}

// Bars tells the frontend that a table is really a categorical breakdown and
// which of its columns carry the category and the count.
//
// The backend names the columns rather than letting the view guess, so a bar
// chart and the table beside it are two renderings of one set of rows — not two
// independently derived views that can disagree about what they are counting.
type Bars struct {
	KeyColumn   string `json:"keyColumn"`
	ValueColumn string `json:"valueColumn"`
	// GroupColumn, when set, splits the rows into one breakdown per value.
	GroupColumn string `json:"groupColumn,omitempty"`
}

// Panel is one card on the dashboard: its chart, its headline numbers, and
// optionally a table.
type Panel struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Series   []*Series `json:"series,omitempty"`
	Stats    []Stat    `json:"stats,omitempty"`
	Table    *Table    `json:"table,omitempty"`
	Bars     *Bars     `json:"bars,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
}

// Warn appends a caveat the UI should surface rather than swallow.
func (p *Panel) Warn(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

// WindowJSON is the wire form of a Window. Timestamps live here, once, rather
// than on each series: every series in a payload is aligned to this one axis by
// construction, so no consumer can plot two panels against different clocks.
type WindowJSON struct {
	Start      int64   `json:"start"`
	End        int64   `json:"end"`
	Period     int32   `json:"period"`
	Range      string  `json:"range"`
	Timestamps []int64 `json:"timestamps"`
}

// JSON renders the window for the wire.
func (w Window) JSON() WindowJSON {
	return WindowJSON{
		Start:      w.Start.Unix(),
		End:        w.End.Unix(),
		Period:     w.Period.Seconds(),
		Range:      w.Range.String(),
		Timestamps: w.Timestamps(),
	}
}

// Payload is what every data endpoint returns. Single-panel endpoints return
// one panel; page endpoints return several — all under one Window, which is the
// mechanism that stops two panels in a response from describing different spans.
type Payload struct {
	Window   WindowJSON `json:"window"`
	Panels   []*Panel   `json:"panels"`
	Warnings []string   `json:"warnings,omitempty"`
}

// NewPayload starts a payload for w.
func NewPayload(w Window) *Payload {
	return &Payload{Window: w.JSON(), Panels: []*Panel{}}
}

// Add appends a panel.
func (p *Payload) Add(panels ...*Panel) *Payload {
	p.Panels = append(p.Panels, panels...)
	return p
}

// Warn appends a payload-level caveat.
func (p *Payload) Warn(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

// Validate checks the invariants the frontend relies on. Handlers call it
// before writing a response, and the API tests call it on every fixture, so a
// series that has drifted out of alignment with the time axis fails loudly here
// instead of silently plotting shifted data.
func (p *Payload) Validate() error {
	n := len(p.Window.Timestamps)
	if n == 0 {
		return fmt.Errorf("payload has no timestamps")
	}
	for _, panel := range p.Panels {
		for _, s := range panel.Series {
			if len(s.Values) != n {
				return fmt.Errorf("panel %q series %q has %d values, want %d",
					panel.ID, s.Label, len(s.Values), n)
			}
		}
		if t := panel.Table; t != nil {
			if t.Total < int64(len(t.Rows)) {
				return fmt.Errorf("panel %q table reports total %d below its %d rows",
					panel.ID, t.Total, len(t.Rows))
			}
			if want := t.Total > int64(len(t.Rows)); t.Truncated != want {
				return fmt.Errorf("panel %q table truncated=%v disagrees with total %d over %d rows",
					panel.ID, t.Truncated, t.Total, len(t.Rows))
			}
		}
	}
	return nil
}
