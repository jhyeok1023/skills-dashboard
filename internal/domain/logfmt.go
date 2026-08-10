package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// LogFormat describes how to read one line out of a pod log group.
//
// Every field name is configurable because the exact application log shape is
// still being settled. The defaults below are read off real Container Insights
// records: fluent-bit wraps each container line in an envelope and, when the
// line is itself JSON, publishes the decoded fields under log_processed. Lines
// that are not access logs arrive as plain text, so both paths have to work.
type LogFormat struct {
	// Envelope
	TimeField      string `json:"timeField"`
	MessageField   string `json:"messageField"`
	ProcessedField string `json:"processedField"`
	StreamField    string `json:"streamField"`

	// Fields read out of the processed map
	AppField      string `json:"appField"`
	LatencyField  string `json:"latencyField"`
	LatencyUnit   Unit   `json:"latencyUnit"`
	StatusField   string `json:"statusField"`
	MethodField   string `json:"methodField"`
	PathField     string `json:"pathField"`
	LevelField    string `json:"levelField"`
	ClientIPField string `json:"clientIpField"`

	// Plain-text fallback. TextPattern is an optional RE2 with named capture
	// groups matching the *Field names above; LevelPattern classifies a line
	// that carries no explicit level.
	TextPattern  string `json:"textPattern"`
	LevelPattern string `json:"levelPattern"`

	// Namespace, when set, restricts every query to one Kubernetes namespace.
	Namespace string `json:"namespace"`

	// OKStatuses are the response codes considered healthy. Anything else is
	// surfaced in the bad-response panel.
	OKStatuses []int `json:"okStatuses"`

	// ExcludePaths are request paths dropped from every pod-log panel.
	//
	// Health checks are the reason this exists. A liveness probe every few
	// seconds is usually the single largest source of request lines, and it
	// drags the latency percentiles toward a route that does no work. Worse,
	// when a probe starts failing it floods the bad-response table with
	// thousands of identical rows and pushes the real failures past the row
	// limit. Excluding them is a filter in the query, so the bytes are never
	// scanned and the counts beside the charts already have them removed.
	//
	// Matching is exact. A prefix rule would quietly swallow /healthy-users,
	// and it would have to be reimplemented identically in the query builder
	// for the settings preview to agree with what actually gets filtered.
	ExcludePaths []string `json:"excludePaths"`

	compiledText  *regexp.Regexp
	compiledLevel *regexp.Regexp
}

// DefaultLogFormat matches the Container Insights records this dashboard was
// built against.
func DefaultLogFormat() LogFormat {
	return LogFormat{
		TimeField:      "time",
		MessageField:   "log",
		ProcessedField: "log_processed",
		StreamField:    "stream",

		AppField:      "app",
		LatencyField:  "latency_ms",
		LatencyUnit:   UnitMillis,
		StatusField:   "status",
		MethodField:   "method",
		PathField:     "path",
		LevelField:    "level",
		ClientIPField: "client_ip",

		LevelPattern: `(?i)\b(error|err|fatal|panic|warn|warning|oomkilled)\b`,
		Namespace:    "default",
		OKStatuses:   []int{200, 201},
		ExcludePaths: DefaultExcludePaths(),
	}
}

// DefaultExcludePaths are the probe endpoints excluded unless configured
// otherwise.
func DefaultExcludePaths() []string {
	return []string{"/health", "/healthcheck"}
}

// Compile validates the configured patterns and caches them. It must be called
// before Parse; Validate calls it, and so does config loading.
func (f *LogFormat) Compile() error {
	f.compiledText, f.compiledLevel = nil, nil
	if f.LevelPattern != "" {
		re, err := regexp.Compile(f.LevelPattern)
		if err != nil {
			return fmt.Errorf("levelPattern: %w", err)
		}
		f.compiledLevel = re
	}
	if f.TextPattern != "" {
		re, err := regexp.Compile(f.TextPattern)
		if err != nil {
			return fmt.Errorf("textPattern: %w", err)
		}
		f.compiledText = re
	}
	return nil
}

// Validate reports whether the format is usable.
func (f *LogFormat) Validate() error {
	if f.TimeField == "" {
		return fmt.Errorf("timeField must be set")
	}
	if f.MessageField == "" {
		return fmt.Errorf("messageField must be set")
	}
	switch f.LatencyUnit {
	case UnitMillis, UnitSeconds, UnitNone:
	default:
		return fmt.Errorf("latencyUnit %q must be ms, s, or empty", f.LatencyUnit)
	}
	return f.Compile()
}

// LogLine is one parsed record. Latency is normalised to milliseconds here so
// that no consumer downstream has to know what unit the source used.
type LogLine struct {
	Timestamp time.Time `json:"timestamp"`
	App       string    `json:"app"`
	Pod       string    `json:"pod"`
	Namespace string    `json:"namespace"`
	Container string    `json:"container"`
	Stream    string    `json:"stream"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	ClientIP  string    `json:"clientIp"`
	Status    int       `json:"status"`
	LatencyMS *float64  `json:"latencyMs"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`

	// HasAccess reports whether this line carried request fields at all, so a
	// plain log line is never counted as a zero-latency request.
	HasAccess bool `json:"hasAccess"`
}

// IsExcludedPath reports whether a request path is filtered out of the pod-log
// panels. It mirrors the query-side filter exactly, so the settings preview
// tells the truth about what a sample line would do.
func (f *LogFormat) IsExcludedPath(path string) bool {
	if path == "" {
		return false
	}
	for _, p := range f.ExcludePaths {
		if p != "" && path == p {
			return true
		}
	}
	return false
}

// IsBadStatus reports whether the line's response code falls outside the set
// the operator considers healthy.
func (f *LogFormat) IsBadStatus(status int) bool {
	if status <= 0 {
		return false
	}
	for _, ok := range f.OKStatuses {
		if status == ok {
			return false
		}
	}
	return true
}

type kubernetesMeta struct {
	PodName       string `json:"pod_name"`
	NamespaceName string `json:"namespace_name"`
	ContainerName string `json:"container_name"`
}

// Parse reads one raw log record. fallback is used when the envelope carries no
// usable timestamp — pass the CloudWatch event time.
//
// Note which clock wins: the envelope's own timestamp. The reference
// implementation stored the envelope time but advanced its ingest watermark
// from the CloudWatch event time, so records could be written with a timestamp
// behind the watermark and then fall outside every subsequent query window.
// Reading a single clock removes that class of drift.
func (f *LogFormat) Parse(raw string, fallback time.Time) (LogLine, error) {
	if f.compiledLevel == nil && f.LevelPattern != "" {
		if err := f.Compile(); err != nil {
			return LogLine{}, err
		}
	}

	line := LogLine{Timestamp: fallback, Message: strings.TrimSpace(raw)}

	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		// Not an envelope at all — treat the whole thing as a plain message.
		f.applyText(&line, line.Message)
		f.applyLevel(&line)
		return line, nil
	}

	if v, ok := env[f.TimeField]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			if ts, err := parseTimestamp(s); err == nil {
				line.Timestamp = ts
			}
		}
	}
	if v, ok := env[f.StreamField]; ok {
		_ = json.Unmarshal(v, &line.Stream)
	}
	if v, ok := env["kubernetes"]; ok {
		var k kubernetesMeta
		if json.Unmarshal(v, &k) == nil {
			line.Pod, line.Namespace, line.Container = k.PodName, k.NamespaceName, k.ContainerName
		}
	}

	msg := ""
	if v, ok := env[f.MessageField]; ok {
		if json.Unmarshal(v, &msg) != nil {
			msg = strings.TrimSpace(string(v))
		}
	}
	if msg != "" {
		line.Message = strings.TrimSpace(msg)
	}

	processed := map[string]any{}
	if v, ok := env[f.ProcessedField]; ok {
		_ = json.Unmarshal(v, &processed)
	}
	if len(processed) == 0 && msg != "" {
		// fluent-bit did not decode the inner line; try it ourselves so a
		// cluster without the JSON parser filter still yields access fields.
		var inner map[string]any
		if json.Unmarshal([]byte(msg), &inner) == nil {
			processed = inner
		}
	}

	if len(processed) > 0 {
		f.applyProcessed(&line, processed)
	} else {
		f.applyText(&line, line.Message)
	}

	if line.App == "" {
		line.App = line.Container
	}
	f.applyLevel(&line)
	return line, nil
}

func (f *LogFormat) applyProcessed(line *LogLine, m map[string]any) {
	line.App = firstString(line.App, str(m[f.AppField]))
	line.Method = firstString(line.Method, str(m[f.MethodField]))
	line.Path = firstString(line.Path, str(m[f.PathField]))
	line.ClientIP = firstString(line.ClientIP, str(m[f.ClientIPField]))
	line.Level = firstString(line.Level, strings.ToLower(str(m[f.LevelField])))

	if v, ok := num(m[f.StatusField]); ok {
		line.Status = int(v)
		line.HasAccess = true
	}
	if v, ok := num(m[f.LatencyField]); ok {
		line.LatencyMS = P(f.toMillis(v))
		line.HasAccess = true
	}
}

func (f *LogFormat) applyText(line *LogLine, msg string) {
	if f.compiledText == nil || msg == "" {
		return
	}
	m := f.compiledText.FindStringSubmatch(msg)
	if m == nil {
		return
	}
	for i, name := range f.compiledText.SubexpNames() {
		if name == "" || i >= len(m) || m[i] == "" {
			continue
		}
		switch name {
		case f.AppField, "app":
			line.App = firstString(line.App, m[i])
		case f.MethodField, "method":
			line.Method = firstString(line.Method, m[i])
		case f.PathField, "path":
			line.Path = firstString(line.Path, m[i])
		case f.ClientIPField, "client_ip", "clientIp":
			line.ClientIP = firstString(line.ClientIP, m[i])
		case f.LevelField, "level":
			line.Level = firstString(line.Level, strings.ToLower(m[i]))
		case f.StatusField, "status":
			if v, err := strconv.Atoi(m[i]); err == nil {
				line.Status = v
				line.HasAccess = true
			}
		case f.LatencyField, "latency", "latency_ms":
			if v, err := strconv.ParseFloat(m[i], 64); err == nil {
				line.LatencyMS = P(f.toMillis(v))
				line.HasAccess = true
			}
		}
	}
}

// applyLevel classifies a line that carries no explicit level by scanning its
// text. A request line that already reported a status is left alone: the word
// "error" inside a URL should not promote a 200 into an error log.
func (f *LogFormat) applyLevel(line *LogLine) {
	switch strings.ToLower(line.Level) {
	case "error", "err", "fatal", "panic":
		line.Level = "error"
		return
	case "warn", "warning":
		line.Level = "warn"
		return
	case "":
	default:
		line.Level = strings.ToLower(line.Level)
		return
	}
	if line.HasAccess || f.compiledLevel == nil {
		return
	}
	m := f.compiledLevel.FindString(line.Message)
	if m == "" {
		return
	}
	if c := strings.ToLower(m); strings.HasPrefix(c, "w") {
		line.Level = "warn"
	} else {
		line.Level = "error"
	}
}

func (f *LogFormat) toMillis(v float64) float64 {
	if f.LatencyUnit == UnitSeconds {
		return v * 1000
	}
	return v
}

func parseTimestamp(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	// Epoch milliseconds, as WAF records use.
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil && ms > 0 {
		return time.UnixMilli(ms).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", s)
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func num(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func firstString(cur, next string) string {
	if cur != "" {
		return cur
	}
	return next
}
