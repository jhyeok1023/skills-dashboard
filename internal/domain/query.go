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

const ginInsightsPattern = `(?:\x1b\[[0-9;]*m)*\[GIN\]\s+\d{4}/\d{2}/\d{2}\s+-\s+\d{2}:\d{2}:\d{2}\s+\|(?:\x1b\[[0-9;]*m)*\s*(?<ginStatus>\d{3})\s*(?:\x1b\[[0-9;]*m)*\|\s*(?:(?<ginHours>\d+)h)?(?:(?<ginMinutes>\d+)m)?(?<ginLatency>[\d.]+)(?<ginLatencyUnit>ns|µs|μs|us|ms|s)\s*\|\s*(?<ginClientIp>\S+)\s*\|(?:\x1b\[[0-9;]*m)*\s*(?<ginMethod>[A-Z]+)\s*(?:\x1b\[[0-9;]*m)*\s+"(?<ginTarget>(?:\\.|[^"])*)"`

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
	fmt.Fprintf(&b, "fields @timestamp, coalesce(%s, @message) as message\n", msg)
	if q.Format.Preset != LogPresetJSON {
		fmt.Fprintf(&b, "| parse message /%s/\n", ginInsightsPattern)
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
	fmt.Fprintf(&b, "| stats count() as n by bin(%s) as t, status, path\n", w.Period)
	b.WriteString("| sort t asc")
	return Query{ID: "pod.badStatus.series", Text: b.String()}, nil
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
	b.WriteString("| fields @timestamp, message, kubernetes.pod_name as pod, kubernetes.container_name as container\n")
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
	b.WriteString("| fields lower(rawLevel) as lvl, message as raw\n")
	b.WriteString("| filter lvl in ['error', 'err', 'fatal', 'panic', 'warn', 'warning']\n")
	b.WriteString("    or (lvl = '' and not ispresent(status) and raw like /(?i)\\b(error|fatal|panic|warn|warning|oomkilled)\\b/)\n")
	b.WriteString("| fields if(lvl in ['warn', 'warning'] or (lvl = '' and not ispresent(status) and raw like /(?i)\\b(warn|warning)\\b/), 'warn', 'error') as level\n")
	return b.String(), nil
}

func (q LogQueries) accessFields() ([]string, error) {
	return []string{
		"kubernetes.pod_name as pod", "app", "method", "path",
		"requestTarget", "status", "latencyMs", "clientIp",
	}, nil
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

// ByMethod counts requests per HTTP method.
func (q WAFQueries) ByMethod() Query {
	return Query{
		ID:   "waf.byMethod",
		Text: "stats count() as n by httpRequest.httpMethod as method\n| sort n desc",
	}
}

// ByPath counts requests per URI and query string. The two are reported as
// separate columns rather than concatenated so the UI can offer a copy button
// for each.
func (q WAFQueries) ByPath(limit int) Query {
	return Query{
		ID: "waf.byPath",
		Text: "stats count() as n by httpRequest.uri as uri, httpRequest.args as args\n" +
			"| sort n desc\n" +
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

// BlockedList returns individual blocked requests, newest first.
func (q WAFQueries) BlockedList(limit int) Query {
	return Query{
		ID: "waf.blocked.list",
		Text: "fields @timestamp, terminatingRuleId as rule, httpRequest.clientIp as clientIp,\n" +
			"       httpRequest.country as country, httpRequest.httpMethod as method,\n" +
			"       httpRequest.uri as uri, httpRequest.args as args\n" +
			"| filter action = 'BLOCK'\n" +
			"| sort @timestamp desc\n" +
			fmt.Sprintf("| limit %d", limit),
		Limit: limit,
	}
}

// headerNameRe keeps an operator-supplied header name to the characters RFC 9110
// permits in a field name, so it can be embedded in the parse pattern below
// without escaping surprises.
var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+$`)

// ByHeader counts the distinct values of one request header.
//
// WAF stores headers as an array of {name, value} objects. Logs Insights cannot
// group by an array element, and indexing by position (headers.0.value) is
// meaningless because the order varies per request, so the value is pulled out
// of the raw record with a parse. The reference implementation solved the same
// problem with a SQLite json_each cross join over every stored row, which is
// the single most expensive query it ran.
func (q WAFQueries) ByHeader(name string, limit int) (Query, error) {
	if !headerNameRe.MatchString(name) {
		return Query{}, fmt.Errorf("invalid header name %q", name)
	}
	pattern := fmt.Sprintf(`"name":"(?i)%s","value":"(?<headerValue>[^"]*)"`, regexp.QuoteMeta(name))
	return Query{
		ID: "waf.byHeader." + strings.ToLower(name),
		Text: fmt.Sprintf("parse @message /%s/\n", pattern) +
			"| filter ispresent(headerValue)\n" +
			"| stats count() as n by headerValue as value\n" +
			"| sort n desc\n" +
			fmt.Sprintf("| limit %d", limit),
		Limit: limit,
	}, nil
}
