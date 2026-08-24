package domain

import (
	"regexp"
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
		"count(status) as requests",
		"count(latencyMs) as latencySamples",
		"pct(latencyMs, 50) as p50",
		"pct(latencyMs, 90) as p90",
		"pct(latencyMs, 99) as p99",
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

	filter := "status not in [200, 201]"
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

	want := "path not in ['/health', '/healthcheck']"
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
	if !strings.Contains(got.Text, "not ispresent(path) or") {
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
		if !strings.Contains(got.Text, ", 'warn', 'error') as level") {
			t.Errorf("%s does not derive level with if():\n%s", name, got.Text)
		}
		// Insights has no `? :` and fails at the lexer. Regex literals are
		// exempt — the Gin pattern accessPreamble writes is full of `(?:` and
		// `(?<`, so they are stripped before the check rather than searched.
		if strings.Contains(withoutRegexLiterals(got.Text), "?") {
			t.Errorf("%s carries a '?' outside a regex literal:\n%s", name, got.Text)
		}
		if !strings.Contains(got.Text, "tolower(rawLevel)") {
			t.Errorf("%s does not lowercase with tolower():\n%s", name, got.Text)
		}
	}

	// PodErrorList selects the message under the alias accessPreamble gives it,
	// and Logs Insights will not compile a query that also re-aliases that.
	// Selecting it and aliasing it to `raw` in the level filter is exactly what
	// the service rejected.
	if !strings.Contains(list.Text, ", dashboardMessage,") {
		t.Fatalf("list query no longer selects the message, so the column would be empty:\n%s", list.Text)
	}
	for name, got := range map[string]Query{
		"errors.series": series,
		"errors.list":   list,
	} {
		if strings.Contains(got.Text, "dashboardMessage as ") {
			t.Errorf("%s re-aliases the selected message field:\n%s", name, got.Text)
		}
	}
}

// withoutRegexLiterals drops every /.../ run from a query, so a check meant for
// Insights expressions does not trip over regex syntax.
func withoutRegexLiterals(text string) string {
	var b strings.Builder
	inside, escaped := false, false
	for _, r := range text {
		switch {
		case escaped:
			escaped = false
			if !inside {
				b.WriteRune(r)
			}
		case r == '\\':
			// The Gin pattern escapes its date delimiters as `\/`. Reading one
			// of those as a closing slash flips the parity and leaves half the
			// regex in the text this is meant to strip.
			escaped = true
			if !inside {
				b.WriteRune(r)
			}
		case r == '/':
			inside = !inside
		case r == '\n':
			// No literal spans lines, so a newline ends any run a stray slash
			// opened — a quoted path such as '/health' cannot swallow the rest
			// of the query.
			inside = false
			b.WriteRune(r)
		case !inside:
			b.WriteRune(r)
		}
	}
	return b.String()
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

	clause := "path not in ['/health', '/healthz', '/readyz']"
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
		// From the Kubernetes envelope, so present whatever the operator
		// configured. They are what lets the panel offer a row detail on a
		// fresh install instead of only where a User-Agent field was named.
		"kubernetes.container_name as container",
		"kubernetes.namespace_name as namespace",
		"coalesce(log_processed.path, ginTarget) as requestTarget",
		"coalesce(log_processed.status, ginStatusNumber) as status",
		"coalesce(jsonLatencyMs, ginLatencyMs) as latencyMs",
		"coalesce(log_processed.client_ip, ginClientIp) as clientIp",
		"sort @timestamp desc",
	} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("list query is missing %q:\n%s", want, got.Text)
		}
	}
}

func TestAutoPresetBuildsGinAndJSONAliases(t *testing.T) {
	q := LogQueries{Format: DefaultLogFormat()}
	got, err := q.PodTraffic(testWindow(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"parse dashboardMessage",
		"(?<ginStatus>",
		`\|(?:\x1b\[[0-9;]*m)*\s*(?:(?<ginHours>`,
		`"?(?<ginTarget>`,
		"coalesce(log_processed.app, kubernetes.container_name) as app",
		"ginLatencyUnit = 'ns'",
		"case(",
		"parse requestTarget",
	} {
		want = strings.ReplaceAll(want, `\\`, `\`)
		if !strings.Contains(got.Text, want) {
			t.Errorf("auto query is missing %q:\n%s", want, got.Text)
		}
	}
	if strings.Contains(got.Text, `\\x1b`) || strings.Contains(got.Text, `\\d`) {
		t.Errorf("query contains double-escaped regex tokens:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, `\d{4}\/\d{2}\/\d{2}`) {
		t.Errorf("query does not escape date delimiters:\n%s", got.Text)
	}
}

func TestPresetRestrictsInsightsParsing(t *testing.T) {
	f := DefaultLogFormat()
	f.Preset = LogPresetGin
	ginQuery, err := (LogQueries{Format: f}).PodTraffic(testWindow(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ginQuery.Text, "log_processed.") {
		t.Errorf("Gin preset still reads JSON fields:\n%s", ginQuery.Text)
	}

	f.Preset = LogPresetJSON
	jsonQuery, err := (LogQueries{Format: f}).PodTraffic(testWindow(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(jsonQuery.Text, "ginStatus") || strings.Contains(jsonQuery.Text, "parse dashboardMessage") {
		t.Errorf("JSON preset still parses Gin fields:\n%s", jsonQuery.Text)
	}
}

// The series query decides whether the "비정상 응답" count is honest, and what
// decides that is how many rows it asks for: Insights cuts a stats result at
// InsightsMaxRows and reports nothing about having done so. Grouped by bucket
// and status the row count is bounded by the window — grouped by path as well,
// which it used to be for no reader at all, it is bounded by whatever a scanner
// decides to request, and around eighty distinct paths was enough to truncate a
// two-hour window silently.
func TestPodBadStatusSeriesGroupsOnlyByWhatItPlots(t *testing.T) {
	q := LogQueries{Format: DefaultLogFormat()}
	got, err := q.PodBadStatusSeries(testWindow(t))
	if err != nil {
		t.Fatal(err)
	}
	// The grouping is read off the stats clause rather than the whole query:
	// accessPreamble defines a `path` alias for every access query, so a
	// substring search for it would match the preamble and never the grouping.
	stats := statsClause(t, got.Text)
	if strings.Contains(stats, ", path") {
		t.Errorf("the series groups by path, which nothing plots and which uncaps its row count:\n%s", stats)
	}
	if !strings.HasSuffix(stats, "as t, status") {
		t.Errorf("the series no longer groups by bucket and status:\n%s", stats)
	}
}

// statsClause returns the query's stats line, without its trailing newline.
func statsClause(t *testing.T, text string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "| stats ") {
			return line
		}
	}
	t.Fatalf("query has no stats clause:\n%s", text)
	return ""
}

// The breakdown is what the dropped grouping became. It has to describe exactly
// the population the series and the list describe — otherwise the panel that
// answers "which paths made up the 404s" is answering about different 404s —
// and it has to come back whole.
func TestPodBadStatusByPathMatchesTheOtherTwoQueries(t *testing.T) {
	f := DefaultLogFormat()
	f.Namespace = "prod"
	f.ExcludePaths = []string{"/health"}
	q := LogQueries{Format: f}

	byPath, err := q.PodBadStatusByPath()
	if err != nil {
		t.Fatal(err)
	}
	series, err := q.PodBadStatusSeries(testWindow(t))
	if err != nil {
		t.Fatal(err)
	}
	// Same filters, character for character. Two queries that select the same
	// requests by two similar-looking filters is the failure this prevents.
	for _, want := range []string{
		"| filter kubernetes.namespace_name = 'prod'\n",
		"| filter not ispresent(path) or path not in ['/health']\n",
		"| filter ispresent(status) and " + notInStatuses("status", DefaultLogFormat().OKStatuses) + "\n",
	} {
		if !strings.Contains(byPath.Text, want) || !strings.Contains(series.Text, want) {
			t.Errorf("filter %q is not shared by both queries:\nbyPath:\n%s\nseries:\n%s",
				want, byPath.Text, series.Text)
		}
	}

	if !strings.Contains(byPath.Text, "stats count() as n, max(@timestamp) as lastTs by status, path\n") {
		t.Errorf("byPath does not group by status and path with a last-seen:\n%s", byPath.Text)
	}
	// No bin(), or the row count goes back to buckets × statuses × paths and
	// takes the ceiling with it.
	if strings.Contains(byPath.Text, "bin(") {
		t.Errorf("byPath bins, which is what made this grouping unaffordable:\n%s", byPath.Text)
	}
	// No limit clause: `sort n desc | limit N` cuts by (status, path) volume, so
	// a flood of 404s would push a rare 403 out of the result entirely. The cut
	// to a readable number of paths happens per code, in Go, after every code
	// has been seen.
	if strings.Contains(byPath.Text, "| limit") || byPath.Limit != 0 {
		t.Errorf("byPath caps itself server-side, which drops whole status codes:\n%s", byPath.Text)
	}
}

// An access log carries a User-Agent only if the application wrote one, so the
// field is named by the operator or not selected at all. Selecting it
// unconditionally would open a detail view whose one row reads "—" on every
// cluster that has not configured it.
func TestPodBadStatusListSelectsAUserAgentOnlyWhenOneIsConfigured(t *testing.T) {
	off := LogQueries{Format: DefaultLogFormat()}
	got, err := off.PodBadStatusList(testWindow(t), 300)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Text, "userAgent") {
		t.Errorf("an unconfigured User-Agent was queried anyway:\n%s", got.Text)
	}

	f := DefaultLogFormat()
	f.UserAgentField = "user_agent"
	got, err = LogQueries{Format: f}.PodBadStatusList(testWindow(t), 300)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "log_processed.user_agent as userAgent") {
		t.Errorf("a configured User-Agent was not selected:\n%s", got.Text)
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
	if strings.Contains(list.Text, "as message") || strings.Contains(list.Text, "message as raw") {
		t.Errorf("query redefines the discovered message field:\n%s", list.Text)
	}
	if !strings.Contains(list.Text, "limit 300") {
		t.Errorf("error list is uncapped:\n%s", list.Text)
	}
	if !strings.Contains(list.Text, "oomkilled") {
		t.Errorf("OOM lines are not matched, so the pod-status panel loses its only OOM signal:\n%s", list.Text)
	}
	if !strings.Contains(list.Text, "not ispresent(status)") {
		t.Errorf("access lines can leak into the ERROR panel:\n%s", list.Text)
	}
	if !strings.Contains(list.Text, "tolower(rawLevel)") || strings.Contains(list.Text, "fields lower(rawLevel)") {
		t.Errorf("error query uses an unsupported case function:\n%s", list.Text)
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

// The row detail can only show what the row was given, and these are the fields
// that answer what an operator asks after "it was blocked": by what kind of
// rule, from what client calling itself what, and what the WAF sent back.
func TestWAFRecentListCarriesTheDetailFields(t *testing.T) {
	list := WAFQueries{Headers: DefaultWAFHeaders()}.RecentList(300)
	for _, want := range []string{
		`parse @message /"name":"(?i)User-Agent","value":"(?<uaCapture>[^"]*)"/`,
		"uaCapture as userAgent",
		"terminatingRuleType as ruleType",
		"responseCodeSent as responseCode",
		"httpRequest.uri as uri",
		"httpRequest.args as args",
	} {
		if !strings.Contains(list.Text, want) {
			t.Errorf("recent list is missing %q:\n%s", want, list.Text)
		}
	}
}

// The detail exists so an operator does not have to open the console. It does
// not exist to ship the console's payload: a WAF record is roughly a kilobyte
// once its header array and rule-group lists are counted, against a couple of
// hundred bytes for the named fields, and this list is refetched on every poll.
// Selecting @message costs the same scan and multiplies what crosses the wire,
// which is exactly why it looks like a free improvement to someone later.
// captureRe finds the ephemeral fields a query's parse commands define.
var captureRe = regexp.MustCompile(`\(\?P?<([A-Za-z_][A-Za-z0-9_]*)>`)

// Logs Insights reads a name in `fields` as a definition, not a selection, so a
// query that lists its own parse capture there defines it twice and is rejected
// before it runs — MalformedQueryException, "Ephemeral field is already
// defined". A capture may be used and may be aliased into a new name; it may
// not be named again.
//
// This is the rule that has now bitten three separate queries, so it gets a
// test that reads the query rather than a comment asking the next person to
// remember.
func TestWAFRecentListAliasesItsParseCaptureRatherThanListingIt(t *testing.T) {
	list := WAFQueries{Headers: DefaultWAFHeaders()}.RecentList(300)
	captures := captureRe.FindAllStringSubmatch(list.Text, -1)
	if len(captures) == 0 {
		t.Fatalf("no parse capture at all, so the User-Agent cannot arrive:\n%s", list.Text)
	}
	for _, c := range captures {
		name := c[1]
		for _, listed := range []string{", " + name + "\n", ", " + name + ",", " " + name + ",\n"} {
			if strings.Contains(list.Text, listed) {
				t.Errorf("parse capture %q is also listed in fields, which Insights rejects:\n%s",
					name, list.Text)
			}
		}
		// Aliased, or it never becomes a column and the detail shows nothing.
		if !strings.Contains(list.Text, name+" as ") {
			t.Errorf("parse capture %q never reaches a column:\n%s", name, list.Text)
		}
	}
}

func TestWAFRecentListDoesNotSelectTheRawRecord(t *testing.T) {
	list := WAFQueries{Headers: DefaultWAFHeaders()}.RecentList(300)
	for _, banned := range []string{"fields @message", ", @message", "@message,"} {
		if strings.Contains(list.Text, banned) {
			t.Errorf("recent list selects the raw record via %q:\n%s", banned, list.Text)
		}
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
