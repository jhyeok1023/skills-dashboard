package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// Query is one CloudWatch Logs Insights query, tagged with what it is for so a
// handler can match results back to panels without positional guessing.
type Query struct {
	ID    string
	Text  string
	Limit int
}

// fieldRe constrains a field reference to what Logs Insights actually accepts:
// an optional @ prefix, then dotted identifiers. Field names reach this package
// from user configuration, so anything outside that shape is rejected rather
// than interpolated — otherwise a "field name" of `x | delete @message |` would
// rewrite the query.
var fieldRe = regexp.MustCompile(`^@?[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// Field validates a configured field reference for interpolation.
func Field(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty field reference")
	}
	if !fieldRe.MatchString(name) {
		return "", fmt.Errorf("invalid field reference %q", name)
	}
	return name, nil
}

// quote renders a Logs Insights string literal.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`)
	return "'" + r.Replace(s) + "'"
}

// LogQueries builds the queries for one log group from a LogFormat.
type LogQueries struct {
	Format LogFormat
}

// namespaceFilter restricts results to the configured namespace. The reference
// implementation hard-coded "default" in Go and dropped everything else after
// downloading it. The filter runs before aggregation, but it does not reduce
// Logs Insights scanned bytes unless the account uses a matching field index.
func (q LogQueries) namespaceFilter() (string, error) {
	if q.Format.Namespace == "" {
		return "", nil
	}
	return fmt.Sprintf("| filter kubernetes.namespace_name = %s\n", quote(q.Format.Namespace)), nil
}

// excludePathFilter drops probe traffic from a pod-log query.
//
// The guard on ispresent matters: a comparison against a field a record does
// not carry matches nothing in Logs Insights, so an unguarded `path not in
// [...]` would silently discard every plain log line — the ERROR and WARN
// output would simply go empty, which is a much worse failure than the noise
// this is meant to remove.
func (q LogQueries) excludePathFilter() (string, error) {
	if len(q.Format.ExcludePaths) == 0 {
		return "", nil
	}

	// Exact match, deliberately. A substring or prefix rule would be the kind
	// of thing that quietly swallows /healthy-users, and it would have to be
	// reimplemented identically in Go for the settings preview to agree with
	// the query. An explicit list stays predictable and stays in step.
	exact := make([]string, 0, len(q.Format.ExcludePaths))
	for _, p := range q.Format.ExcludePaths {
		if p == "" {
			continue
		}
		exact = append(exact, quote(p))
	}
	if len(exact) == 0 {
		return "", nil
	}

	return fmt.Sprintf("| filter not ispresent(%s) or %s not in [%s]\n",
		"path", "path", strings.Join(exact, ", ")), nil
}

func (q LogQueries) processedField(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("field is not configured")
	}
	if q.Format.ProcessedField == "" {
		return Field(name)
	}
	return Field(q.Format.ProcessedField + "." + name)
}

const ginInsightsPattern = `(?:\x1b\[[0-9;]*m)*\[GIN\]\s+\d{4}/\d{2}/\d{2}\s+-\s+\d{2}:\d{2}:\d{2}\s+\|(?:\x1b\[[0-9;]*m)*\s*(?<ginStatus>\d{3})\s*(?:\x1b\[[0-9;]*m)*\|(?:\x1b\[[0-9;]*m)*\s*(?:(?<ginHours>\d+)h)?(?:(?<ginMinutes>\d+)m)?(?<ginLatency>[\d.]+)(?<ginLatencyUnit>ns|µs|μs|us|ms|s)\s*(?:\x1b\[[0-9;]*m)*\|\s*(?<ginClientIp>\S+)\s*\|(?:\x1b\[[0-9;]*m)*\s*(?<ginMethod>[A-Z]+)\s*(?:\x1b\[[0-9;]*m)*\s+"?(?<ginTarget>(?:\\.|[^"\s])+?)"?(?:\s|$)`

// accessPreamble gives structured JSON and Gin access lines one set of field
// names. Every query reads those aliases, so auto detection cannot make the
// latency panel and the bad-status panel disagree about the same line.
func (q LogQueries) accessPreamble() (string, error) {
	msg, err := Field(q.Format.MessageField)
	if err != nil {
		return "", fmt.Errorf("messageField: %w", err)
	}

	jsonFields := map[string]string{}
	if q.Format.Preset != LogPresetGin {
		for key, name := range map[string]string{
			"app": q.Format.AppField, "latency": q.Format.LatencyField,
			"status": q.Format.StatusField, "method": q.Format.MethodField,
			"target": q.Format.PathField, "level": q.Format.LevelField,
			"clientIp": q.Format.ClientIPField,
		} {
			if name == "" {
				continue
			}
			field, err := q.processedField(name)
			if err != nil {
				return "", fmt.Errorf("%sField: %w", key, err)
			}
			jsonFields[key] = field
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "fields @timestamp, coalesce(%s, @message) as dashboardMessage\n", msg)
	if q.Format.Preset != LogPresetJSON {
		pattern := strings.ReplaceAll(ginInsightsPattern, `\\`, `\`)
		pattern = strings.ReplaceAll(pattern, "/", `\/`)
		fmt.Fprintf(&b, "| parse dashboardMessage /%s/\n", pattern)
	}

	var calculated []string
	jsonLatency := ""
	if source := jsonFields["latency"]; source != "" {
		expr := source
		if q.Format.LatencyUnit == UnitSeconds {
			expr = "(" + source + " * 1000)"
		}
		calculated = append(calculated, aliasExpr(expr, "jsonLatencyMs"))
		jsonLatency = "jsonLatencyMs"
	}
	ginLatency, ginStatus := "", ""
	if q.Format.Preset != LogPresetJSON {
		calculated = append(calculated,
			aliasExpr("(ginStatus * 1)", "ginStatusNumber"),
			aliasExpr("case(ispresent(ginHours), ginHours * 3600000, 0) + "+
				"case(ispresent(ginMinutes), ginMinutes * 60000, 0) + "+
				"case(ginLatencyUnit = 's', ginLatency * 1000, "+
				"ginLatencyUnit in ['µs', 'μs', 'us'], ginLatency / 1000, "+
				"ginLatencyUnit = 'ns', ginLatency / 1000000, ginLatency)", "ginLatencyMs"),
		)
		ginLatency = "ginLatencyMs"
		ginStatus = "ginStatusNumber"
	}
	if len(calculated) > 0 {
		fmt.Fprintf(&b, "| fields %s\n", strings.Join(calculated, ", "))
	}

	fields := []string{
		aliasExpr(coalesceExpr(jsonFields["app"], "kubernetes.container_name"), "app"),
		aliasExpr(coalesceExpr(jsonFields["status"], ginStatus), "status"),
		aliasExpr(coalesceExpr(jsonLatency, ginLatency), "latencyMs"),
		aliasExpr(coalesceExpr(jsonFields["method"], ginField(q.Format.Preset, "ginMethod")), "method"),
		aliasExpr(coalesceExpr(jsonFields["target"], ginField(q.Format.Preset, "ginTarget")), "requestTarget"),
		aliasExpr(coalesceExpr(jsonFields["clientIp"], ginField(q.Format.Preset, "ginClientIp")), "clientIp"),
		aliasExpr(coalesceExpr(jsonFields["level"], "''"), "rawLevel"),
	}
	fmt.Fprintf(&b, "| fields %s\n", strings.Join(fields, ", "))
	b.WriteString("| parse requestTarget /^(?<requestPath>[^?]*)/\n")
	b.WriteString("| fields coalesce(requestPath, requestTarget) as path\n")
	return b.String(), nil
}

func ginField(preset LogPreset, name string) string {
	if preset == LogPresetJSON {
		return ""
	}
	return name
}

func coalesceExpr(values ...string) string {
	kept := values[:0]
	for _, value := range values {
		if value != "" {
			kept = append(kept, value)
		}
	}
	if len(kept) == 0 {
		return "''"
	}
	if len(kept) == 1 {
		return kept[0]
	}
	return "coalesce(" + strings.Join(kept, ", ") + ")"
}

func aliasExpr(expr, alias string) string {
	return expr + " as " + alias
}

// PodTraffic returns latency percentiles and request counts per bucket.
//
// Both populations are counted in the same query and named separately:
// `requests` counts lines that carried a status, `latencySamples` counts lines
// that carried a latency. The reference implementation derived those two
// numbers from two different SQL statements and then labelled both of them
// "요청 수" in the UI, which is why the same panel could show two totals. Here
// the difference is explicit on the wire and reported as each stat's basis.
func (q LogQueries) PodTraffic(w Window) (Query, error) {
	preamble, err := q.accessPreamble()
	if err != nil {
		return Query{}, err
	}
	ns, err := q.namespaceFilter()
	if err != nil {
		return Query{}, err
	}
	probes, err := q.excludePathFilter()
	if err != nil {
		return Query{}, err
	}

	var b strings.Builder
	b.WriteString(preamble)
	b.WriteString(ns)
	b.WriteString(probes)
	b.WriteString("| filter ispresent(status) or ispresent(latencyMs)\n")
	b.WriteString("| stats count(status) as requests,\n")
	b.WriteString("        count(latencyMs) as latencySamples,\n")
	b.WriteString("        avg(latencyMs) as avg,\n")
	b.WriteString("        pct(latencyMs, 50) as p50,\n")
	b.WriteString("        pct(latencyMs, 90) as p90,\n")
	b.WriteString("        pct(latencyMs, 99) as p99\n")
	fmt.Fprintf(&b, "    by bin(%s) as t, app\n", w.Period)
	b.WriteString("| sort t asc")
	return Query{ID: "pod.traffic", Text: b.String()}, nil
}

// PodBadStatusSeries counts non-OK responses per bucket and status code. It is
// a complete aggregate, so summing it yields the honest total that the
// truncated detail list is compared against.
//
// It groups by bucket and status and nothing else, deliberately. It used to
// carry `path` as a third key that no caller ever read, and that key is what
// decided whether the total was honest: Insights cuts a stats result at
// InsightsMaxRows, and the row count here is buckets × statuses × paths. At a
// 5-minute period over two hours that is 24 × 5 × paths, so around eighty
// distinct paths — one scanner hitting random URLs — was enough to truncate the
// chart and the "비정상 응답" count beside it, silently. The per-path breakdown
// now has its own unbinned query, where the same paths cost one row each.
func (q LogQueries) PodBadStatusSeries(w Window) (Query, error) {
	preamble, err := q.accessPreamble()
	if err != nil {
		return Query{}, err
	}
	ns, err := q.namespaceFilter()
	if err != nil {
		return Query{}, err
	}
	probes, err := q.excludePathFilter()
	if err != nil {
		return Query{}, err
	}

	var b strings.Builder
	b.WriteString(preamble)
	b.WriteString(ns)
	b.WriteString(probes)
	fmt.Fprintf(&b, "| filter ispresent(status) and %s\n", notInStatuses("status", q.Format.OKStatuses))
	fmt.Fprintf(&b, "| stats count() as n by bin(%s) as t, status\n", w.Period)
	b.WriteString("| sort t asc")
	return Query{ID: "pod.badStatus.series", Text: b.String()}, nil
}

// PodBadStatusByPath counts non-OK responses per status code and path, over the
// whole window rather than per bucket.
//
// The filter is character-for-character the one PodBadStatusSeries and
// PodBadStatusList use, so all three describe one population. The totals can
// still differ at the margin — the series drops a bin landing on the window's
// exclusive end that Insights' inclusive EndTime handed to this query, and this
// query is the one subject to the row cap below — but never because the two are
// counting different requests.
//
// No limit clause, which is not the same as no limit. Insights caps every
// result set at InsightsMaxRows, so the `sort n desc` below still decides what
// survives once (status, path) cardinality passes 10,000 — and it decides by
// volume, meaning a flood of 404s against random paths eventually pushes a rare
// 403 row out. That is the failure this ordering was meant to avoid and it is
// only postponed, not removed: there is no single Insights scan that both
// enumerates paths and keeps a correct per-code total past the row cap. What
// the missing `limit N` does buy is the ordinary case, where every code is seen
// and the cut to a readable number of paths happens in Go, per status code,
// after all of them have been. When the cap is reached the caller says so —
// see warnIfTruncated and the byPath total's basis in panels_logs.go.
//
// Dropping bin() is what makes an uncapped stats affordable at all: one row per
// (status, path) rather than one per (bucket, status, path).
// The window is not a parameter: without bin() nothing here varies with it, and
// the runner scopes every query to the window when it starts it.
func (q LogQueries) PodBadStatusByPath() (Query, error) {
	preamble, err := q.accessPreamble()
	if err != nil {
		return Query{}, err
	}
	ns, err := q.namespaceFilter()
	if err != nil {
		return Query{}, err
	}
	probes, err := q.excludePathFilter()
	if err != nil {
		return Query{}, err
	}

	var b strings.Builder
	b.WriteString(preamble)
	b.WriteString(ns)
	b.WriteString(probes)
	fmt.Fprintf(&b, "| filter ispresent(status) and %s\n", notInStatuses("status", q.Format.OKStatuses))
	// max(@timestamp) rather than a sort: the caller wants the newest hit per
	// group, and Insights renders @timestamp fixed-width so the comparison that
	// picks it needs no parse.
	b.WriteString("| stats count() as n, max(@timestamp) as lastTs by status, path\n")
	b.WriteString("| sort n desc")
	return Query{ID: "pod.badStatus.byPath", Text: b.String()}, nil
}

// PodBadStatusList returns the most recent non-OK responses, newest first.
// The filter is identical to PodBadStatusSeries so the list and the count it is
// displayed beside can never describe different populations.
func (q LogQueries) PodBadStatusList(w Window, limit int) (Query, error) {
	preamble, err := q.accessPreamble()
	if err != nil {
		return Query{}, err
	}
	ns, err := q.namespaceFilter()
	if err != nil {
		return Query{}, err
	}
	fields, err := q.accessFields()
	if err != nil {
		return Query{}, err
	}
	probes, err := q.excludePathFilter()
	if err != nil {
		return Query{}, err
	}

	var b strings.Builder
	b.WriteString(preamble)
	fmt.Fprintf(&b, "| fields @timestamp, %s\n", strings.Join(fields, ", "))
	b.WriteString(ns)
	b.WriteString(probes)
	fmt.Fprintf(&b, "| filter ispresent(status) and %s\n", notInStatuses("status", q.Format.OKStatuses))
	b.WriteString("| sort @timestamp desc\n")
	fmt.Fprintf(&b, "| limit %d", limit)
	return Query{ID: "pod.badStatus.list", Text: b.String(), Limit: limit}, nil
}

// PodErrorSeries counts ERROR and WARN lines per bucket. Like the bad-status
// aggregate, it exists so the detail list's header can show a real total rather
// than the length of a capped array.
func (q LogQueries) PodErrorSeries(w Window) (Query, error) {
	preamble, err := q.accessPreamble()
	if err != nil {
		return Query{}, err
	}
	ns, err := q.namespaceFilter()
	if err != nil {
		return Query{}, err
	}
	filter, err := q.levelFilter()
	if err != nil {
		return Query{}, err
	}
	probes, err := q.excludePathFilter()
	if err != nil {
		return Query{}, err
	}

	var b strings.Builder
	b.WriteString(preamble)
	b.WriteString(ns)
	b.WriteString(probes)
	b.WriteString(filter)
	fmt.Fprintf(&b, "| stats count() as n by bin(%s) as t, level\n", w.Period)
	b.WriteString("| sort t asc")
	return Query{ID: "pod.errors.series", Text: b.String()}, nil
}

// PodErrorList returns the most recent ERROR and WARN lines.
func (q LogQueries) PodErrorList(w Window, limit int) (Query, error) {
	preamble, err := q.accessPreamble()
	if err != nil {
		return Query{}, err
	}
	ns, err := q.namespaceFilter()
	if err != nil {
		return Query{}, err
	}
	filter, err := q.levelFilter()
	if err != nil {
		return Query{}, err
	}
	probes, err := q.excludePathFilter()
	if err != nil {
		return Query{}, err
	}

	var b strings.Builder
	b.WriteString(preamble)
	b.WriteString("| fields @timestamp, dashboardMessage, kubernetes.pod_name as pod, kubernetes.container_name as container\n")
	b.WriteString(ns)
	b.WriteString(probes)
	b.WriteString(filter)
	b.WriteString("| sort @timestamp desc\n")
	fmt.Fprintf(&b, "| limit %d", limit)
	return Query{ID: "pod.errors.list", Text: b.String(), Limit: limit}, nil
}

// levelFilter selects error and warn lines, preferring an explicit level field
// and falling back to a pattern match over the raw message. The `level` column
// it defines is what PodErrorSeries groups by.
func (q LogQueries) levelFilter() (string, error) {
	var b strings.Builder
	// Normalise into two buckets so the series has a stable set of keys.
	//
	// The message field is matched in place rather than aliased: PodErrorList
	// already selects it by name, and Logs Insights refuses to compile a query
	// that both selects a field and re-aliases it.
	b.WriteString("| fields tolower(rawLevel) as lvl\n")
	b.WriteString("| filter lvl in ['error', 'err', 'fatal', 'panic', 'warn', 'warning']\n")
	b.WriteString("    or (lvl = '' and not ispresent(status) and dashboardMessage like /(?i)\\b(error|fatal|panic|warn|warning|oomkilled)\\b/)\n")
	// if(), not a ternary: Logs Insights has no `? :` and fails at the lexer.
	b.WriteString("| fields if(lvl in ['warn', 'warning'] or (lvl = '' and not ispresent(status) and dashboardMessage like /(?i)\\b(warn|warning)\\b/), 'warn', 'error') as level\n")
	return b.String(), nil
}

func (q LogQueries) accessFields() ([]string, error) {
	// The three fixed ones come from the Kubernetes envelope, not from the
	// application's own log line, so they are present whatever an operator has
	// or has not named in the log format. That is what lets the panel offer a
	// row detail unconditionally instead of only where a User-Agent field
	// happens to be configured. PodErrorList already selects `container` under
	// this name.
	//
	// The rest are the aliases accessPreamble defines, so they read the same
	// whether the line arrived as JSON or as a Gin access log.
	out := []string{
		"kubernetes.pod_name as pod",
		"kubernetes.container_name as container",
		"kubernetes.namespace_name as namespace",
		"app", "method", "path", "requestTarget", "status", "latencyMs", "clientIp",
	}
	// userAgent has no preamble alias: there is no default name to guess, so
	// it is read straight from the configured field when there is one.
	if q.Format.UserAgentField != "" {
		f, err := q.processedField(q.Format.UserAgentField)
		if err != nil {
			return nil, fmt.Errorf("userAgentField: %w", err)
		}
		out = append(out, fmt.Sprintf("%s as userAgent", f))
	}
	return out, nil
}

// notInStatuses renders the healthy-code exclusion. An empty set means every
// response with a status is interesting.
func notInStatuses(field string, ok []int) string {
	if len(ok) == 0 {
		return "1 = 1"
	}
	parts := make([]string, len(ok))
	for i, s := range ok {
		parts[i] = fmt.Sprint(s)
	}
	return fmt.Sprintf("%s not in [%s]", field, strings.Join(parts, ", "))
}

// WAFQueries builds the queries for a WAF log group. WAF records have a fixed
// schema, so unlike pod logs nothing here is configurable except which headers
// the operator cares about.
type WAFQueries struct {
	// Headers are the HTTP header names to break traffic down by.
	Headers []string
}

// DefaultWAFHeaders are the headers worth a breakdown by default. The list is
// kept short on purpose: each header costs one more full scan of the window.
func DefaultWAFHeaders() []string { return []string{"Host", "User-Agent"} }

// ActionSeries counts requests per bucket and action, which drives both the
// allow/block chart and the honest totals beside it.
func (q WAFQueries) ActionSeries(w Window) Query {
	return Query{
		ID: "waf.action.series",
		Text: "fields @timestamp\n" +
			fmt.Sprintf("| stats count() as n by bin(%s) as t, action\n", w.Period) +
			"| sort t asc",
	}
}

// Every breakdown groups by action as well as by its own key, and carries the
// newest timestamp in each group.
//
// Without the action, "this path was requested 4,000 times" says nothing about
// whether those requests reached the application — the allowed and the blocked
// were summed into one number. Grouping is what splits them, and max(@timestamp)
// per (key, action) is what lets the caller say which action was the most
// recent one for that key.
//
// The cost is unchanged: the same scan of the same window, one more grouping
// key. What does change is the row count, which is why the callers raise the
// limit — see actionFanout.
func breakdownStats(by string) string {
	return "stats count() as n, max(@timestamp) as lastTs by " + by + ", action\n| sort n desc\n"
}

// ByMethod counts requests per HTTP method and action.
func (q WAFQueries) ByMethod() Query {
	return Query{
		ID:   "waf.byMethod",
		Text: breakdownStats("httpRequest.httpMethod as method"),
	}
}

// ByPath counts requests per URI, query string and action. The URI and the args
// are reported as separate columns rather than concatenated so the UI can offer
// a copy button for each.
func (q WAFQueries) ByPath(limit int) Query {
	return Query{
		ID: "waf.byPath",
		Text: breakdownStats("httpRequest.uri as uri, httpRequest.args as args") +
			fmt.Sprintf("| limit %d", limit),
		Limit: limit,
	}
}

// Blocked counts blocked requests per terminating rule and client address.
func (q WAFQueries) Blocked(limit int) Query {
	return Query{
		ID: "waf.blocked",
		Text: "filter action = 'BLOCK'\n" +
			"| stats count() as n by terminatingRuleId as rule, httpRequest.clientIp as clientIp, httpRequest.country as country\n" +
			"| sort n desc\n" +
			fmt.Sprintf("| limit %d", limit),
		Limit: limit,
	}
}

// RecentList returns individual requests, newest first, each with the action
// the WAF took on it.
//
// It is deliberately not filtered to blocked requests. What a rule blocked is
// already summarised by Blocked; what this answers is the other question — is
// traffic arriving at all, and is it getting through — which a list of blocks
// alone cannot, because an empty one means either "nothing was blocked" or
// "nothing arrived".
//
// Beyond what the table shows, it selects what an operator asks next about one
// row: which client sent it, calling itself what, and what the WAF actually
// did about it. Those are extra fields on a scan that already happened, so they
// cost nothing — Insights bills for bytes read, and the record was read whole
// either way.
//
// What it does not select is @message. The raw WAF record is roughly a
// kilobyte with its header array and rule-group lists, against a couple of
// hundred bytes for the fields below, and this list is refetched on every poll.
// Naming the fields is what keeps a detail view from becoming a download.
func (q WAFQueries) RecentList(limit int) Query {
	// The capture is named apart from the column it feeds, and aliased into it
	// below.
	//
	// Logs Insights reads a name in `fields` as a definition rather than a
	// selection, so listing the parse's own field there defines it twice and the
	// query is rejected before it runs: "Ephemeral field is already defined".
	// It is the same rule that stops the pod error list from re-aliasing the
	// message field it already selected — a query may use an ephemeral field
	// freely, and may alias it into a new name, but may not name it again.
	//
	// The header name is a constant, so the error is impossible and dropping it
	// keeps this signature free of one that no caller could act on.
	ua, _ := headerParse("User-Agent", "uaCapture")
	return Query{
		ID: "waf.recent.list",
		Text: ua + "\n" +
			"| fields @timestamp, action, terminatingRuleId as rule, terminatingRuleType as ruleType,\n" +
			"       responseCodeSent as responseCode, uaCapture as userAgent,\n" +
			"       httpRequest.clientIp as clientIp,\n" +
			"       httpRequest.country as country, httpRequest.httpMethod as method,\n" +
			"       httpRequest.uri as uri, httpRequest.args as args\n" +
			"| sort @timestamp desc\n" +
			fmt.Sprintf("| limit %d", limit),
		Limit: limit,
	}
}

// headerNameRe keeps an operator-supplied header name to the characters RFC 9110
// permits in a field name, so it can be embedded in the parse pattern below
// without escaping surprises.
var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+$`)

// headerParse renders the command that lifts one request header out of a WAF
// record and binds it to alias.
//
// WAF stores headers as an array of {name, value} objects. Logs Insights cannot
// group by an array element, and indexing by position (headers.0.value) is
// meaningless because the order varies per request, so the value is pulled out
// of the raw record with a parse. The reference implementation solved the same
// problem with a SQLite json_each cross join over every stored row, which is
// the single most expensive query it ran.
//
// Both the per-header breakdown and the recent-request list need this, and they
// need it to behave identically — one shape of regex to reason about when a
// header stops coming through.
func headerParse(name, alias string) (string, error) {
	if !headerNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid header name %q", name)
	}
	return fmt.Sprintf(`parse @message /"name":"(?i)%s","value":"(?<%s>[^"]*)"/`,
		regexp.QuoteMeta(name), alias), nil
}

// ByHeader counts the distinct values of one request header, per action.
func (q WAFQueries) ByHeader(name string, limit int) (Query, error) {
	parse, err := headerParse(name, "headerValue")
	if err != nil {
		return Query{}, err
	}
	return Query{
		ID: "waf.byHeader." + strings.ToLower(name),
		Text: parse + "\n" +
			"| filter ispresent(headerValue)\n" +
			"| " + breakdownStats("headerValue as value") +
			fmt.Sprintf("| limit %d", limit),
		Limit: limit,
	}, nil
}
