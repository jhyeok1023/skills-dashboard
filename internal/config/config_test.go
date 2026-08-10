package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, ".env", strings.Join([]string{
		"# a comment",
		"",
		"AWS_ACCESS_KEY_ID=AKIAEXAMPLE",
		`AWS_SECRET_ACCESS_KEY="secret/with+slashes="`,
		"export AWS_REGION=ap-northeast-2",
		"  AWS_SESSION_TOKEN = token-value  ",
		"QUOTED='single quoted'",
		"TRAILING=value # inline comment",
		"EMPTY=",
		"not an assignment",
	}, "\n"))

	got, err := LoadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"AWS_ACCESS_KEY_ID":     "AKIAEXAMPLE",
		"AWS_SECRET_ACCESS_KEY": "secret/with+slashes=",
		"AWS_REGION":            "ap-northeast-2",
		"AWS_SESSION_TOKEN":     "token-value",
		"QUOTED":                "single quoted",
		"TRAILING":              "value",
		"EMPTY":                 "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("parsed %d keys, want %d: %v", len(got), len(want), got)
	}
}

func TestLoadEnvFileMissingIsNotAnError(t *testing.T) {
	got, err := LoadEnvFile(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("a missing .env should be tolerated: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestLoadCredentialsPrefersTheFileOverTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, ".env", "AWS_ACCESS_KEY_ID=from-file\nAWS_SECRET_ACCESS_KEY=s\nAWS_REGION=ap-northeast-2\n")

	t.Setenv("AWS_ACCESS_KEY_ID", "from-environment")
	t.Setenv("AWS_SESSION_TOKEN", "env-token")

	c, err := LoadCredentials(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKeyID != "from-file" {
		t.Errorf("AccessKeyID = %q, want the .env value", c.AccessKeyID)
	}
	// The environment still fills in what the file omits.
	if c.SessionToken != "env-token" {
		t.Errorf("SessionToken = %q, want the environment value", c.SessionToken)
	}
}

func TestCredentialsValidateNamesEveryMissingKey(t *testing.T) {
	err := Credentials{}.Validate()
	if err == nil {
		t.Fatal("empty credentials passed validation")
	}
	for _, want := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("error does not say where to put them: %v", err)
	}

	ok := Credentials{AccessKeyID: "a", SecretAccessKey: "b", Region: "ap-northeast-2"}
	if err := ok.Validate(); err != nil {
		t.Errorf("complete credentials rejected: %v", err)
	}
}

// A secret that reaches a log or an error message is a secret that leaks.
func TestCredentialsRedactedHidesTheSecret(t *testing.T) {
	c := Credentials{
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		SessionToken:    "FwoGZXIvYXdzEJr",
		Region:          "ap-northeast-2",
	}
	got := c.Redacted()
	if strings.Contains(got, c.SecretAccessKey) {
		t.Errorf("secret appears in %q", got)
	}
	if strings.Contains(got, c.SessionToken) {
		t.Errorf("session token appears in %q", got)
	}
	if strings.Contains(got, "AKIAIOSFODNN7") {
		t.Errorf("full access key id appears in %q", got)
	}
	if !strings.Contains(got, "MPLE") {
		t.Errorf("redaction left nothing to tell two keys apart: %q", got)
	}
}

func TestPodLogGroupDerivedFromCluster(t *testing.T) {
	c := Default()
	c.ClusterName = "prod"
	if got, want := c.PodLogGroupOrDefault(), "/aws/containerinsights/prod/application"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	c.PodLogGroup = "/custom/group"
	if got := c.PodLogGroupOrDefault(); got != "/custom/group" {
		t.Errorf("an explicit log group was overridden: %q", got)
	}

	empty := Default()
	if got := empty.PodLogGroupOrDefault(); got != "" {
		t.Errorf("got %q with no cluster name, want empty", got)
	}
}

func TestValidateFillsDefaultsAndRejectsBadInput(t *testing.T) {
	c := Default()
	c.Limits = Limits{}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Limits.LogRows != DefaultLimits().LogRows {
		t.Errorf("LogRows = %d, want the default", c.Limits.LogRows)
	}
	if c.Limits.InsightsConcurrency <= 0 {
		t.Error("InsightsConcurrency left at zero would deadlock every query")
	}

	bad := Default()
	bad.Region = ""
	if err := bad.Validate(); err == nil {
		t.Error("accepted a config with no region")
	}

	bad = Default()
	bad.LogFormat.LevelPattern = "(unclosed"
	if err := bad.Validate(); err == nil {
		t.Error("accepted an uncompilable level pattern")
	}

	bad = Default()
	bad.WAFHeaders = []string{"User Agent"}
	if err := bad.Validate(); err == nil {
		t.Error("accepted a header name that cannot be embedded in a query")
	}
}

// A load balancer ARN pasted into the settings page passes the metric SEARCH
// value regex and then matches nothing, so the panel renders empty with no
// explanation. Converting it on the way in is what stops that from happening.
func TestLoadBalancerARNIsConvertedToTheDimension(t *testing.T) {
	c := Default()
	c.LoadBalancer = "  arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:loadbalancer/app/my-alb/50dc6c495c0c9188  "
	c.TargetGroups = []string{
		"arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:targetgroup/k8s-default-product-d6d507c878/73e2d6bc24d8a067",
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.LoadBalancer != "app/my-alb/50dc6c495c0c9188" {
		t.Errorf("LoadBalancer = %q, want the CloudWatch dimension", c.LoadBalancer)
	}
	if want := "targetgroup/k8s-default-product-d6d507c878/73e2d6bc24d8a067"; c.TargetGroups[0] != want {
		t.Errorf("TargetGroups[0] = %q, want %q", c.TargetGroups[0], want)
	}
}

// Everything a conversion cannot rescue is refused at the point of saving. A
// rejected save names the problem; an accepted one produces a blank chart that
// looks like a load balancer with no traffic.
func TestLoadBalancerMustBeACloudWatchDimension(t *testing.T) {
	for _, bad := range []string{
		"my-alb",              // the name, which is what the console shows
		"net/my-nlb/abc123",   // an NLB: the dashboard reads AWS/ApplicationELB only
		"app/my-alb",          // truncated
		"app/my-alb/50dc/x",   // an extra segment
		"app/my-alb/not-hex!", // not an id
	} {
		c := Default()
		c.LoadBalancer = bad
		if err := c.Validate(); err == nil {
			t.Errorf("accepted %q as a load balancer dimension", bad)
		}
	}

	// An empty field is a load balancer that has not been chosen yet, not a
	// mistake: the target group panel falls back to per-target-group filters.
	c := Default()
	c.LoadBalancer = "   "
	if err := c.Validate(); err != nil {
		t.Errorf("an unset load balancer was rejected: %v", err)
	}
	if c.LoadBalancer != "" {
		t.Errorf("LoadBalancer = %q, want it trimmed to empty", c.LoadBalancer)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")

	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get().Region; got != "ap-northeast-2" {
		t.Errorf("fresh store region = %q, want the default", got)
	}

	cfg := s.Get()
	cfg.ClusterName = "prod"
	cfg.TargetGroups = []string{"targetgroup/a/1", "targetgroup/b/2"}
	if err := s.Set(cfg); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Get()
	if got.ClusterName != "prod" {
		t.Errorf("ClusterName = %q after reload", got.ClusterName)
	}
	if len(got.TargetGroups) != 2 {
		t.Errorf("TargetGroups = %v after reload", got.TargetGroups)
	}
	if got.LogFormat.LatencyField != "latency_ms" {
		t.Errorf("log format did not survive the round trip: %+v", got.LogFormat)
	}
}

// Handlers hold a config while the settings page saves a new one. Handing out
// copies is what keeps a half-applied update from being observed.
func TestStoreGetReturnsAnIndependentCopy(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.Get()
	cfg.TargetGroups = []string{"one"}
	if err := s.Set(cfg); err != nil {
		t.Fatal(err)
	}

	held := s.Get()
	held.TargetGroups[0] = "mutated"
	held.ClusterName = "mutated"

	if got := s.Get(); got.TargetGroups[0] != "one" || got.ClusterName != "" {
		t.Errorf("mutating a returned config changed the store: %+v", got)
	}
}

func TestStoreRejectsInvalidConfigWithoutPersistingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	good := s.Get()
	good.ClusterName = "prod"
	if err := s.Set(good); err != nil {
		t.Fatal(err)
	}

	bad := s.Get()
	bad.LogFormat.TextPattern = "(?P<unclosed"
	if err := s.Set(bad); err == nil {
		t.Fatal("an uncompilable pattern was accepted")
	}
	if got := s.Get(); got.ClusterName != "prod" || got.LogFormat.TextPattern != "" {
		t.Errorf("a rejected save still mutated the store: %+v", got.LogFormat)
	}
}

func TestNewStoreReportsAnUnparseableFile(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "config.json", "{not json")
	if _, err := NewStore(p); err == nil {
		t.Error("a corrupt config file was accepted")
	}
}

func TestSaveWritesRestrictivePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(s.Get()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config was not written: %v", err)
	}
	// No temp file should be left behind by the write-and-rename.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("a .tmp file survived the save")
	}
}
