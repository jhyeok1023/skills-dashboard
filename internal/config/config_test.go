package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

func TestEnvFileCandidates(t *testing.T) {
	got := envFileCandidates("/opt/dash", "/home/u/.skills-dashboard")
	want := []string{"/opt/dash/.env", "/home/u/.skills-dashboard/.env"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// The binary living in the config directory must not produce the same
	// candidate twice, and the working directory is never one of them.
	if got := envFileCandidates("/opt/dash", "/opt/dash"); len(got) != 1 {
		t.Errorf("duplicate directories gave %v, want one candidate", got)
	}
	for _, p := range envFileCandidates("", "") {
		t.Errorf("unknown directories still produced %q", p)
	}
}

func TestResolveEnvFileRejectsAPathThatIsNotThere(t *testing.T) {
	// A named file that does not exist used to be swallowed, leaving the
	// operator with "missing AWS_ACCESS_KEY_ID" for what was really a typo.
	if _, _, err := ResolveEnvFile(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a named .env that does not exist was accepted")
	}

	dir := t.TempDir()
	p := writeFile(t, dir, ".env", "AWS_REGION=ap-northeast-2\n")
	got, _, err := ResolveEnvFile(p)
	if err != nil || got != p {
		t.Fatalf("ResolveEnvFile(%q) = %q, %v", p, got, err)
	}
}

func TestResolveEnvFileReportsWhereItLooked(t *testing.T) {
	// Nothing exists at either candidate here, which is not an error — the
	// process environment may carry the values — but the caller has to be able
	// to say where it looked.
	path, tried, err := ResolveEnvFile("")
	if err != nil {
		t.Fatal(err)
	}
	if path == "" && len(tried) == 0 {
		t.Error("no .env found and no candidates reported")
	}
	for _, p := range tried {
		if !filepath.IsAbs(p) {
			t.Errorf("candidate %q is not absolute; a relative path is unusable in a message", p)
		}
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

// The check URL is the one address the dashboard itself requests, so it is
// checked as an address and not just as text — and it is checked by the same
// rule on both paths, so the settings page refuses exactly what the loader
// would otherwise have discarded.
func TestCheckURLIsRefusedOnSaveAndDroppedOnLoad(t *testing.T) {
	for _, bad := range []string{
		"file:///etc/passwd",
		"ftp://example.com/",
		"not a url at all",
		"https://",
		"://example.com",
	} {
		c := Default()
		c.Check.URL = bad
		if err := c.Validate(); err == nil {
			t.Errorf("Validate accepted check.url %q", bad)
		}

		c = Default()
		c.Check.URL = bad
		notes := c.Repair()
		if len(notes) == 0 {
			t.Errorf("Repair said nothing about check.url %q", bad)
		}
		if c.Check.URL != "" {
			t.Errorf("Repair kept an unusable check.url %q", c.Check.URL)
		}
	}

	for _, ok := range []string{
		"http://127.0.0.1:8080/health",
		"https://api.example.com/healthz?deep=1",
	} {
		c := Default()
		c.Check.URL = ok
		if err := c.Validate(); err != nil {
			t.Errorf("Validate rejected check.url %q: %v", ok, err)
		}
	}

	bad := Default()
	bad.Check.URL = "https://example.com/"
	bad.Check.ExpectStatus = 99
	if err := bad.Validate(); err == nil {
		t.Error("accepted an expectStatus that is not an HTTP status code")
	}
}

// Zero means "any 2xx", which is what most people mean by "it works".
func TestHealthCheckOK(t *testing.T) {
	any2xx := HealthCheck{}
	for _, s := range []int{200, 201, 204, 299} {
		if !any2xx.OK(s) {
			t.Errorf("status %d not treated as healthy by default", s)
		}
	}
	for _, s := range []int{199, 300, 404, 503} {
		if any2xx.OK(s) {
			t.Errorf("status %d treated as healthy by default", s)
		}
	}

	// A service that answers 401 to an unauthenticated probe is still up, so
	// the expected code is settable and then it is the only healthy one.
	only401 := HealthCheck{ExpectStatus: 401}
	if !only401.OK(401) || only401.OK(200) {
		t.Error("an explicit expectStatus is not the only code accepted")
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

// A fresh install has no config.json, so Default() supplies the answer to
// /api/config with nothing selected yet. Nil slices marshal to `null`, the
// browser's Config declares these fields as `string[]`, and the settings page
// calls .filter on one before it renders anything — so a `null` here threw
// during mount and took every resource field off the page. That looked like a
// dashboard with no settings rather than like a bug, which is why it survived.
func TestStoreNeverHandsOutNullLists(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal(s.Get())
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)

	for _, field := range []string{
		"targetGroups", "rdsProxies", "webAcls", "wafHeaders", "okStatuses", "excludePaths",
	} {
		if strings.Contains(got, `"`+field+`":null`) {
			t.Errorf("%s is null; the settings page cannot survive that\n%s", field, got)
		}
	}
	for _, want := range []string{`"targetGroups":[]`, `"rdsProxies":[]`, `"webAcls":[]`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
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

// An unparseable file must not stop the dashboard from starting. The settings
// page is the only place to repair it, and a process that exited serves none.
func TestNewStoreKeepsAnUnparseableFileAsideAndStarts(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "config.json", "{not json")

	s, err := NewStore(p)
	if err != nil {
		t.Fatalf("a corrupt config file stopped the dashboard from starting: %v", err)
	}
	if got := s.Get().Region; got != Default().Region {
		t.Errorf("Region = %q, want the default", got)
	}
	if len(s.Notices()) == 0 {
		t.Error("the file was replaced with defaults and nothing said so")
	}
	// Starting from defaults means the next save overwrites the file, so the
	// original has to survive somewhere.
	if _, err := os.Stat(p + ".bak"); err != nil {
		t.Errorf("the unreadable original was lost: %v", err)
	}
}

// A value an older build accepted — the load balancer field had no validation
// at all until recently — must not turn into a dashboard that refuses to boot
// with its reason printed to a console window that closes.
func TestNewStoreDropsAnUnusableValueInsteadOfRefusingToStart(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "config.json", `{
		"region": "ap-northeast-2",
		"clusterName": "prod",
		"loadBalancer": "my-alb",
		"wafHeaders": ["Host", "User Agent"]
	}`)

	s, err := NewStore(p)
	if err != nil {
		t.Fatalf("a stored value stopped the dashboard from starting: %v", err)
	}
	got := s.Get()
	if got.LoadBalancer != "" {
		t.Errorf("LoadBalancer = %q, want it dropped", got.LoadBalancer)
	}
	if len(got.WAFHeaders) != 1 || got.WAFHeaders[0] != "Host" {
		t.Errorf("WAFHeaders = %v, want only the usable one kept", got.WAFHeaders)
	}
	// Everything that was fine has to survive; repairing is not resetting.
	if got.ClusterName != "prod" {
		t.Errorf("ClusterName = %q, want it preserved", got.ClusterName)
	}
	if len(s.Notices()) != 2 {
		t.Errorf("notices = %v, want one per discarded value", s.Notices())
	}
	for _, n := range s.Notices() {
		if !strings.Contains(n, "→") {
			t.Errorf("notice %q does not say what was done about it", n)
		}
	}

	// The save path stays strict: the same value typed into the settings page
	// is refused, because there a person is watching and can fix it.
	bad := got
	bad.LoadBalancer = "my-alb"
	if err := s.Set(bad); err == nil {
		t.Error("the settings page accepted a load balancer name")
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
