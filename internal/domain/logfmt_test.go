package domain

import (
	"testing"
	"time"
)

// These three records are the shapes a Container Insights application log group
// actually delivers: an access line that fluent-bit decoded into log_processed,
// a line from another namespace, and a plain-text line with no structure at all.
const (
	sampleAccess = `{"time":"2026-08-09T02:57:08.636329927Z","stream":"stdout","_p":"F","log":"{\"app\":\"stress\",\"latency_ms\":12.5,\"method\":\"GET\",\"path\":\"/healthcheck\",\"status\":503,\"ts\":\"2026-08-09T02:57:08.635992281Z\"}","log_processed":{"app":"stress","client_ip":"10.0.3.123","latency_ms":12.5,"method":"GET","path":"/healthcheck","status":503,"ts":"2026-08-09T02:57:08.635992281Z"},"kubernetes":{"pod_name":"stress-5cbb6d585d-cr4rd","namespace_name":"default","container_name":"stress"}}`

	sampleOtherNS = `{"time":"2026-08-09T02:57:08.69Z","stream":"stderr","log":"[error] something","kubernetes":{"pod_name":"fluent-bit-x","namespace_name":"amazon-cloudwatch","container_name":"fluent-bit"}}`

	samplePlain = `{"time":"2026-08-09T03:00:00Z","stream":"stderr","log":"WARN: connection pool exhausted","kubernetes":{"pod_name":"user-1","namespace_name":"default","container_name":"user"}}`
)

func TestParseAccessLine(t *testing.T) {
	f := DefaultLogFormat()
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}

	line, err := f.Parse(sampleAccess, time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := line.Timestamp.Format(time.RFC3339), "2026-08-09T02:57:08Z"; got != want {
		t.Errorf("timestamp = %s, want %s", got, want)
	}
	if line.App != "stress" {
		t.Errorf("app = %q, want stress", line.App)
	}
	if line.Pod != "stress-5cbb6d585d-cr4rd" {
		t.Errorf("pod = %q", line.Pod)
	}
	if line.Namespace != "default" {
		t.Errorf("namespace = %q", line.Namespace)
	}
	if line.Method != "GET" || line.Path != "/healthcheck" {
		t.Errorf("method/path = %q %q", line.Method, line.Path)
	}
	if line.Status != 503 {
		t.Errorf("status = %d, want 503", line.Status)
	}
	if line.LatencyMS == nil || *line.LatencyMS != 12.5 {
		t.Errorf("latency = %v, want 12.5", line.LatencyMS)
	}
	// The reference implementation parsed client_ip into its struct but never
	// read it; the dashboard wants it for the copy-a-value affordance.
	if line.ClientIP != "10.0.3.123" {
		t.Errorf("clientIp = %q, want 10.0.3.123", line.ClientIP)
	}
	if !line.HasAccess {
		t.Error("HasAccess = false on a line carrying status and latency")
	}
	// "healthcheck" contains no level word, and a request line should not be
	// classified as an error just because it failed — that is the status
	// panel's job.
	if line.Level != "" {
		t.Errorf("level = %q, want empty for an access line", line.Level)
	}
}

func TestParseKeepsNamespaceForTheCallerToFilter(t *testing.T) {
	f := DefaultLogFormat()
	line, err := f.Parse(sampleOtherNS, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if line.Namespace != "amazon-cloudwatch" {
		t.Errorf("namespace = %q, want amazon-cloudwatch", line.Namespace)
	}
	// The parser reports the namespace rather than silently dropping the line,
	// so the filter stays a configurable query concern instead of being
	// hard-coded the way the reference implementation had it.
	if line.Level != "error" {
		t.Errorf("level = %q, want error from \"[error] something\"", line.Level)
	}
}

func TestParsePlainTextLine(t *testing.T) {
	f := DefaultLogFormat()
	line, err := f.Parse(samplePlain, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if line.Message != "WARN: connection pool exhausted" {
		t.Errorf("message = %q", line.Message)
	}
	if line.Level != "warn" {
		t.Errorf("level = %q, want warn", line.Level)
	}
	if line.App != "user" {
		t.Errorf("app = %q, want the container name as fallback", line.App)
	}
	if line.HasAccess {
		t.Error("HasAccess = true on a line with no request fields")
	}
	if line.LatencyMS != nil {
		t.Errorf("latency = %v, want nil rather than a zero that would drag the average down", line.LatencyMS)
	}
	if line.Status != 0 {
		t.Errorf("status = %d, want 0", line.Status)
	}
}

func TestParseFallsBackToDecodingTheInnerLineItself(t *testing.T) {
	// A cluster whose fluent-bit has no JSON parser filter emits the inner
	// line as a string with no log_processed alongside it.
	raw := `{"time":"2026-08-09T03:00:00Z","stream":"stdout","log":"{\"app\":\"api\",\"latency_ms\":8,\"method\":\"POST\",\"path\":\"/v1/order\",\"status\":201}","kubernetes":{"pod_name":"api-1","namespace_name":"default","container_name":"api"}}`
	f := DefaultLogFormat()
	line, err := f.Parse(raw, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if line.Status != 201 || line.Path != "/v1/order" {
		t.Errorf("inner JSON was not decoded: status=%d path=%q", line.Status, line.Path)
	}
	if line.LatencyMS == nil || *line.LatencyMS != 8 {
		t.Errorf("latency = %v, want 8", line.LatencyMS)
	}
}

func TestParseNonEnvelopeInputDoesNotFail(t *testing.T) {
	f := DefaultLogFormat()
	fallback := time.Date(2026, 8, 9, 4, 0, 0, 0, time.UTC)
	line, err := f.Parse("panic: runtime error: index out of range", fallback)
	if err != nil {
		t.Fatalf("a bare log line should parse, got %v", err)
	}
	if !line.Timestamp.Equal(fallback) {
		t.Errorf("timestamp = %s, want the fallback %s", line.Timestamp, fallback)
	}
	if line.Level != "error" {
		t.Errorf("level = %q, want error", line.Level)
	}
}

func TestLatencyUnitConversionHappensInTheBackend(t *testing.T) {
	f := DefaultLogFormat()
	f.LatencyField = "duration"
	f.LatencyUnit = UnitSeconds
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	raw := `{"time":"2026-08-09T03:00:00Z","log":"x","log_processed":{"duration":0.0125,"status":200},"kubernetes":{"namespace_name":"default","container_name":"api"}}`
	line, err := f.Parse(raw, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Seconds are normalised to milliseconds here so the view layer never has
	// to know which source a number came from.
	if line.LatencyMS == nil || *line.LatencyMS != 12.5 {
		t.Errorf("latency = %v, want 12.5ms from 0.0125s", line.LatencyMS)
	}
}

func TestTextPatternExtractsFieldsFromPlainLines(t *testing.T) {
	f := DefaultLogFormat()
	f.TextPattern = `(?P<method>[A-Z]+)\s+(?P<path>\S+)\s+(?P<status>\d{3})\s+(?P<latency_ms>[\d.]+)ms`
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	raw := `{"time":"2026-08-09T03:00:00Z","log":"GET /v1/user 404 31.7ms","kubernetes":{"namespace_name":"default","container_name":"api"}}`
	line, err := f.Parse(raw, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if line.Method != "GET" || line.Path != "/v1/user" || line.Status != 404 {
		t.Errorf("regex extraction failed: %+v", line)
	}
	if line.LatencyMS == nil || *line.LatencyMS != 31.7 {
		t.Errorf("latency = %v, want 31.7", line.LatencyMS)
	}
	if !line.HasAccess {
		t.Error("HasAccess = false after a successful regex extraction")
	}
}

func TestIsBadStatus(t *testing.T) {
	f := DefaultLogFormat()
	tests := []struct {
		status int
		bad    bool
	}{
		{200, false},
		{201, false},
		{204, true}, // healthy in general, but not in the configured set
		{301, true},
		{404, true},
		{500, true},
		{0, false}, // no status at all is not a bad status
	}
	for _, tc := range tests {
		if got := f.IsBadStatus(tc.status); got != tc.bad {
			t.Errorf("IsBadStatus(%d) = %v, want %v", tc.status, got, tc.bad)
		}
	}

	f.OKStatuses = []int{200, 201, 204}
	if f.IsBadStatus(204) {
		t.Error("204 should be healthy once configured as such")
	}
}

func TestValidateRejectsBadPatterns(t *testing.T) {
	f := DefaultLogFormat()
	f.LevelPattern = `(unclosed`
	if err := f.Validate(); err == nil {
		t.Error("Validate accepted an uncompilable levelPattern")
	}

	f = DefaultLogFormat()
	f.TextPattern = `(?P<bad`
	if err := f.Validate(); err == nil {
		t.Error("Validate accepted an uncompilable textPattern")
	}

	f = DefaultLogFormat()
	f.LatencyUnit = UnitPercent
	if err := f.Validate(); err == nil {
		t.Error("Validate accepted a nonsensical latency unit")
	}

	f = DefaultLogFormat()
	f.TimeField = ""
	if err := f.Validate(); err == nil {
		t.Error("Validate accepted an empty timeField")
	}
}

func TestParseTimestampFormats(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"2026-08-09T02:57:08.636329927Z", "2026-08-09T02:57:08Z"},
		{"2026-08-09T02:57:08Z", "2026-08-09T02:57:08Z"},
		{"1786168613711", "2026-08-08T05:56:53Z"}, // epoch ms, as WAF uses
	}
	for _, tc := range tests {
		got, err := parseTimestamp(tc.in)
		if err != nil {
			t.Errorf("parseTimestamp(%q): %v", tc.in, err)
			continue
		}
		if got.Format(time.RFC3339) != tc.want {
			t.Errorf("parseTimestamp(%q) = %s, want %s", tc.in, got.Format(time.RFC3339), tc.want)
		}
	}
	if _, err := parseTimestamp("not a time"); err == nil {
		t.Error("parseTimestamp accepted nonsense")
	}
}
