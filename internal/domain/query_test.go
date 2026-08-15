package domain

import (
	"strings"
	"testing"
)

func testWindow(t *testing.T) Window {
	t.Helper()
	w, err := NewWindow(mustTime(t, "2026-08-10T10:00:00Z"), Range1h, Period5m)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// Field names arrive from user configuration, so the builder must refuse
// anything that could re-shape the query instead of naming a field.
func TestFieldRejectsInjection(t *testing.T) {
	bad := []string{
		"",
		"log_processed.status | delete @message",
		"status' or '1'='1",
		"status\n| limit 10000",
		"../etc/passwd",
		"status; drop",
		"stats count()",
		"9field",
		"field..double",
		"field.",
	}
	for _, in := range bad {
		if got, err := Field(in); err == nil {
			t.Errorf("Field(%q) = %q, want rejection", in, got)
		}
	}

	good := []string{"status", "log_processed.status", "@timestamp", "@message", "kubernetes.pod_name", "a_b.c_d"}
	for _, in := range good {
		if _, err := Field(in); err != nil {
			t.Errorf("Field(%q) rejected a legitimate reference: %v", in, err)
		}
	}
}

func TestQueryBuilderPropagatesFieldRejection(t *testing.T) {
	f := DefaultLogFormat()
	f.StatusField = "status | delete @message"
	q := LogQueries{Format: f}

	if _, err := q.PodTraffic(testWindow(t)); err == nil {
		t.Error("PodTraffic accepted an injected status field")
	}
	if _, err := q.PodBadStatusSeries(testWindow(t)); err == nil {
		t.Error("PodBadStatusSeries accepted an injected status field")
	}
	if _, err := q.PodBadStatusList(testWindow(t), 100); err == nil {
		t.Error("PodBadStatusList accepted an injected status field")
	}
}

func TestNamespaceValueIsQuotedNotInterpolated(t *testing.T) {
	f := DefaultLogFormat()
	f.Namespace = `default' | delete @message | filter '1`
	q := LogQueries{Format: f}

	got, err := q.PodTraffic(testWindow(t))
	if err != nil {
		t.Fatal(err)
	}
	// The quote inside the value must be escaped, leaving the injected pipe
	// inert inside a string literal.
	if strings.Contains(got.Text, "| delete @message") && !strings.Contains(got.Text, `\'`) {
		t.Errorf("namespace value escaped its literal:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, `\'`) {
		t.Errorf("expected an escaped quote in:\n%s", got.Text)
	}
}

// Both request populations are counted in one query under distinct names. This
// is the structural answer to a panel that showed two different "request
// count" numbers depending on which query produced them.
func TestPodTrafficCountsBothPopulationsSeparately(t *testing.T) {
	q := LogQueries{Format: DefaultLogFormat()}
	got, err := q.PodTraffic(testWindow(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"count(log_processed.status) as requests",
		"count(log_processed.latency_ms) as latencySamples",
		"pct(log_processed.latency_ms, 50) as p50",
		"pct(log_processed.latency_ms, 90) as p90",
		"pct(log_processed.latency_ms, 99) as p99",
		"by bin(5m) as t",
		"filter kubernetes.namespace_name = 'default'",
	} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("query is missing %q:\n%s", want, got.Text)
		}
	}
}

// Percentiles are computed by CloudWatch. The reference implementation pulled
// every latency value out of SQLite and stepped to an offset with no index on
// the column, which is why the panel slowed down as logs accumulated.
func TestPodTrafficDoesNotFetchRawLatencies(t *testing.T) {
	q := LogQueries{Format: DefaultLogFormat()}
	got, err := q.PodTraffic(testWindow(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Text, "limit") {
		t.Errorf("aggregate query carries a row limit, so its totals would be partial:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "stats") {
		t.Errorf("aggregation was not pushed into the query:\n%s", got.Text)
	}
}

func TestBinMatchesTheWindowPeriod(t *testing.T) {
	for _, tc := range []struct {
		p    Period
		want string
	}{
		{Period1m, "bin(1m)"},
		{Period5m, "bin(5m)"},
		{Period10m, "bin(10m)"},
		{Period1h, "bin(1h)"},
	} {
		w, err := NewWindow(mustTime(t, "2026-08-10T10:00:00Z"), Range4h, tc.p)
		if err != nil {
			t.Fatal(err)
		}
		q := LogQueries{Format: DefaultLogFormat()}
		got, err := q.PodTraffic(w)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.Text, tc.want) {
			t.Errorf("period %s did not produce %q:\n%s", tc.p, tc.want, got.Text)
		}
	}
}

// The list and the aggregate it is displayed beside must filter identically,
// otherwise the header count and the visible rows describe different things.
func TestBadStatusListAndSeriesShareOneFilter(t *testing.T) {
	q := LogQueries{Format: DefaultLogFormat()}
	w := testWindow(t)

	series, err := q.PodBadStatusSeries(w)
	if err != nil {
		t.Fatal(err)
	}
	list, err := q.PodBadStatusList(w, 300)
	if err != nil {
		t.Fatal(err)
	}

	filter := "log_processed.status not in [200, 201]"
	if !strings.Contains(series.Text, filter) {
		t.Errorf("series query lost the filter:\n%s", series.Text)
	}
	if !strings.Contains(list.Text, filter) {
		t.Errorf("list query lost the filter:\n%s", list.Text)
	}

	// Only the list is capped; the aggregate must stay complete so it can
	// supply the true total.
	if !strings.Contains(list.Text, "limit 300") {
		t.Errorf("list query is uncapped:\n%s", list.Text)
	}
	if strings.Contains(series.Text, "limit") {
		t.Errorf("aggregate query is capped, so its total would be a lie:\n%s", series.Text)
	}
	if list.Limit != 300 {
		t.Errorf("list.Limit = %d, want 300", list.Limit)
	}
}

// A liveness probe every few seconds is usually the largest single source of
// request lines. Left in, it drags the latency percentiles toward a route that
// does no work, and when it starts failing it floods the bad-response table
// with identical rows that push the real failures past the row limit.
func TestProbePathsAreExcludedFromEveryPodQuery(t *testing.T) {
	q := LogQueries{Format: DefaultLogFormat()}
	w := testWindow(t)

	traffic, err := q.PodTraffic(w)
	if err != nil {
		t.Fatal(err)
	}
	badSeries, err := q.PodBadStatusSeries(w)
	if err != nil {
		t.Fatal(err)
	}
	badList, err := q.PodBadStatusList(w, 300)
	if err != nil {
		t.Fatal(err)
	}
	errSeries, err := q.PodErrorSeries(w)
	if err != nil {
		t.Fatal(err)
	}
	errList, err := q.PodErrorList(w, 300)
	if err != nil {
		t.Fatal(err)
	}

	want := "log_processed.path not in ['/health', '/healthcheck']"
	for name, got := range map[string]Query{
		"traffic":          traffic,
		"badStatus.series": badSeries,
		"badStatus.list":   badList,
		"errors.series":    errSeries,
		"errors.list":      errList,
	} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("%s does not exclude probe traffic:\n%s", name, got.Text)
		}
	}
}

// A comparison against a field a record does not carry matches nothing in Logs
// Insights, so an unguarded exclusion would silently discard every plain log
// line — the ERROR and WARN panels would simply go empty.
func TestProbeExclusionKeepsLinesThatHaveNoPath(t *testing.T) {
	q := LogQueries{Format: DefaultLogFormat()}
	got, err := q.PodErrorList(testWindow(t), 300)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "not ispresent(log_processed.path) or") {
		t.Errorf("the exclusion is not guarded on ispresent:\n%s", got.Text)
	}
}

// Logs Insights rejects unknown syntax at StartQuery with a 400, and the panel
// degrades to a note rather than an error, so a query that only Go can parse
// looks like "no errors in this window". A ternary and lower() both shipped
// that way. Only the two error queries use levelFilter, so they are the pair.
func TestErrorQueriesUseOnlyLogsInsightsSyntax(t *testing.T) {
	q := LogQueries{Format: DefaultLogFormat()}
	w := testWindow(t)

	series, err := q.PodErrorSeries(w)
	if err != nil {
		t.Fatal(err)
	}
	list, err := q.PodErrorList(w, 300)
	if err != nil {
		t.Fatal(err)
	}

	for name, got := range map[string]Query{
		"errors.series": series,
		"errors.list":   list,
	} {
		if !strings.Contains(got.Text, "if(isWarn, 'warn', 'error') as level") {
			t.Errorf("%s does not derive level with if():\n%s", name, got.Text)
		}
		// (?i) inside a regex literal is the only legitimate '?'.
		if strings.Contains(strings.ReplaceAll(got.Text, "(?i)", ""), "?") {
			t.Errorf("%s carries a '?' outside a regex literal:\n%s", name, got.Text)
		}
		if !strings.Contains(got.Text, "tolower(rawLevel)") {
			t.Errorf("%s does not lowercase with tolower():\n%s", name, got.Text)
		}
	}

	// PodErrorList selects the message field by name, and Logs Insights will
	// not compile a query that also re-aliases it.
	msg := DefaultLogFormat().MessageField
	if !strings.Contains(list.Text, ", "+msg+",") {
		t.Fatalf("list query no longer selects %q, so the message column would be empty:\n%s", msg, list.Text)
	}
	for name, got := range map[string]Query{
		"errors.series": series,
		"errors.list":   list,
	} {
		if strings.Contains(got.Text, msg+" as ") {
			t.Errorf("%s re-aliases the selected message field:\n%s", name, got.Text)
		}
	}
}

// The list and the aggregate beside it must exclude identically, or the header
// count and the visible rows describe different populations again.
func TestProbeExclusionIsIdenticalAcrossListAndAggregate(t *testing.T) {
	f := DefaultLogFormat()
	f.ExcludePaths = []string{"/health", "/healthz", "/readyz"}
	q := LogQueries{Format: f}
	w := testWindow(t)

	series, err := q.PodBadStatusSeries(w)
	if err != nil {
		t.Fatal(err)
	}
	list, err := q.PodBadStatusList(w, 300)
	if err != nil {
		t.Fatal(err)
	}

	clause := "log_processed.path not in ['/health', '/healthz', '/readyz']"
	if !strings.Contains(series.Text, clause) {
		t.Errorf("aggregate:\n%s", series.Text)
	}
	if !strings.Contains(list.Text, clause) {
		t.Errorf("list:\n%s", list.Text)
	}
}

func TestProbeExclusionCanBeTurnedOff(t *testing.T) {
	f := DefaultLogFormat()
	f.ExcludePaths = nil
	q := LogQueries{Format: f}

	got, err := q.PodTraffic(testWindow(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Text, "not in ['/health'") {
		t.Errorf("an empty exclusion list still filtered:\n%s", got.Text)
	}
	if strings.Contains(got.Text, "not in []") {
		t.Errorf("an empty exclusion list produced a malformed filter:\n%s", got.Text)
	}
}

// Excluded paths are operator input and must be quoted, not interpolated.
func TestExcludedPathsAreQuoted(t *testing.T) {
	f := DefaultLogFormat()
	f.ExcludePaths = []string{`/health' | delete @message | filter '1`}
	q := LogQueries{Format: f}

	got, err := q.PodTraffic(testWindow(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, `\'`) {
		t.Errorf("an injected quote was not escaped:\n%s", got.Text)
	}
}

func TestOKStatusesAreConfigurable(t *testing.T) {
	f := DefaultLogFormat()
	f.OKStatuses = []int{200, 201, 204, 304}
	q := LogQueries{Format: f}

	got, err := q.PodBadStatusSeries(testWindow(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "not in [200, 201, 204, 304]") {
		t.Errorf("configured healthy codes not reflected:\n%s", got.Text)
	}

	f.OKStatuses = nil
	q = LogQueries{Format: f}
	got, err = q.PodBadStatusSeries(testWindow(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Text, "not in []") {
		t.Errorf("empty healthy set produced a malformed filter:\n%s", got.Text)
	}
}

func TestPodBadStatusListSelectsTheCopyableColumns(t *testing.T) {
	q := LogQueries{Format: DefaultLogFormat()}
	got, err := q.PodBadStatusList(testWindow(t), 300)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kubernetes.pod_name as pod",
		"log_processed.path as path",
		"log_processed.status as status",
		"log_processed.latency_ms as latencyMs",
		"log_processed.client_ip as clientIp",
		"sort @timestamp desc",
	} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("list query is missing %q:\n%s", want, got.Text)
		}
	}
}

func TestPodErrorQueriesCoverBothLevels(t *testing.T) {
	q := LogQueries{Format: DefaultLogFormat()}
	w := testWindow(t)

	series, err := q.PodErrorSeries(w)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(series.Text, "by bin(5m) as t, level") {
		t.Errorf("error series does not group by level:\n%s", series.Text)
	}
	if strings.Contains(series.Text, "limit") {
		t.Errorf("error aggregate is capped, so its total would be a lie:\n%s", series.Text)
	}

	list, err := q.PodErrorList(w, 300)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.Text, "limit 300") {
		t.Errorf("error list is uncapped:\n%s", list.Text)
	}
	if !strings.Contains(list.Text, "oomkilled") {
		t.Errorf("OOM lines are not matched, so the pod-status panel loses its only OOM signal:\n%s", list.Text)
	}
}

func TestWAFQueries(t *testing.T) {
	q := WAFQueries{Headers: DefaultWAFHeaders()}
	w := testWindow(t)

	if got := q.ActionSeries(w); !strings.Contains(got.Text, "by bin(5m) as t, action") {
		t.Errorf("action series:\n%s", got.Text)
	}
	if got := q.ByMethod(); !strings.Contains(got.Text, "httpRequest.httpMethod as method") {
		t.Errorf("byMethod:\n%s", got.Text)
	}
	got := q.ByPath(20)
	for _, want := range []string{"httpRequest.uri as uri", "httpRequest.args as args", "limit 20"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("byPath is missing %q:\n%s", want, got.Text)
		}
	}
	blocked := q.Blocked(10)
	for _, want := range []string{"filter action = 'BLOCK'", "terminatingRuleId as rule", "httpRequest.clientIp as clientIp"} {
		if !strings.Contains(blocked.Text, want) {
			t.Errorf("blocked is missing %q:\n%s", want, blocked.Text)
		}
	}

	list := q.RecentList(100)
	if !strings.Contains(list.Text, "sort @timestamp desc") {
		t.Errorf("recent list is not newest-first:\n%s", list.Text)
	}
	if !strings.Contains(list.Text, "action") {
		t.Errorf("recent list does not report what the WAF did with each request:\n%s", list.Text)
	}
	// Filtered to blocks, an empty list means either "nothing was blocked" or
	// "nothing arrived", and those are the two answers it exists to separate.
	if strings.Contains(list.Text, "filter action") {
		t.Errorf("recent list is filtered to one action:\n%s", list.Text)
	}
}

// Without the action in the grouping, one number counted the requests that
// reached the application and the requests the WAF stopped, together.
func TestWAFBreakdownsSplitByAction(t *testing.T) {
	q := WAFQueries{Headers: DefaultWAFHeaders()}
	header, err := q.ByHeader("User-Agent", 20)
	if err != nil {
		t.Fatal(err)
	}

	for name, got := range map[string]Query{
		"byMethod": q.ByMethod(),
		"byPath":   q.ByPath(20),
		"byHeader": header,
	} {
		for _, want := range []string{", action", "max(@timestamp) as lastTs", "sort n desc"} {
			if !strings.Contains(got.Text, want) {
				t.Errorf("%s is missing %q:\n%s", name, want, got.Text)
			}
		}
	}

	// The header breakdown's stats has to stay piped after its parse/filter.
	if strings.HasPrefix(header.Text, "|") {
		t.Errorf("byHeader starts with a pipe:\n%s", header.Text)
	}
	if !strings.Contains(header.Text, "| stats count()") {
		t.Errorf("byHeader's stats is not piped after the parse:\n%s", header.Text)
	}
}

func TestWAFByHeaderRejectsInvalidNames(t *testing.T) {
	q := WAFQueries{}
	for _, bad := range []string{"", "User Agent", "x/y", `x"y`, "x\ny", "헤더"} {
		if _, err := q.ByHeader(bad, 10); err == nil {
			t.Errorf("ByHeader(%q) accepted an invalid header name", bad)
		}
	}
	for _, ok := range []string{"Host", "User-Agent", "X-Forwarded-For", "x_custom"} {
		if _, err := q.ByHeader(ok, 10); err != nil {
			t.Errorf("ByHeader(%q) rejected a valid header name: %v", ok, err)
		}
	}
}

func TestWAFByHeaderParsesRatherThanIndexingByPosition(t *testing.T) {
	q := WAFQueries{}
	got, err := q.ByHeader("User-Agent", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "parse @message") {
		t.Errorf("header breakdown does not parse the raw record:\n%s", got.Text)
	}
	// Header order varies per request, so a positional reference would sample
	// whichever header happened to be first.
	if strings.Contains(got.Text, "headers.0") {
		t.Errorf("header breakdown indexes by position:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "limit 10") {
		t.Errorf("header breakdown is uncapped:\n%s", got.Text)
	}
	if got.ID != "waf.byHeader.user-agent" {
		t.Errorf("ID = %q", got.ID)
	}
}

func TestQuoteEscapesLiterals(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"default", `'default'`},
		{"it's", `'it\'s'`},
		{`back\slash`, `'back\\slash'`},
		{"line\nbreak", `'line\nbreak'`},
	}
	for _, tc := range tests {
		if got := quote(tc.in); got != tc.want {
			t.Errorf("quote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
