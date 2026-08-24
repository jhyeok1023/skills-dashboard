package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/jhyeok1023/skills-dashboard/internal/domain"
)

// Config records which AWS resources the dashboard watches and how it reads
// them. It is written to disk so a selection survives a restart; credentials
// are never part of it.
type Config struct {
	// Region for everything except CLOUDFRONT-scoped WAF, which only reports
	// into us-east-1 no matter where the distribution serves from.
	Region    string `json:"region"`
	WAFRegion string `json:"wafRegion"`

	ClusterName string `json:"clusterName"`
	Namespace   string `json:"namespace"`

	PodLogGroup string `json:"podLogGroup"`
	WAFLogGroup string `json:"wafLogGroup"`

	LoadBalancer string   `json:"loadBalancer"`
	TargetGroups []string `json:"targetGroups"`
	RDSProxies   []string `json:"rdsProxies"`
	WebACLs      []string `json:"webAcls"`

	// WAFHeaders are the request headers to break WAF traffic down by. Each
	// one costs a further scan of the window, so the list is kept short.
	WAFHeaders []string `json:"wafHeaders"`

	LogFormat domain.LogFormat `json:"logFormat"`
	Limits    Limits           `json:"limits"`

	// Check is the endpoint the 트래픽 점검 screen requests. Empty leaves that
	// screen with nothing to do but say so.
	Check HealthCheck `json:"check"`
}

// HealthCheck is the one place the dashboard reaches something other than AWS.
//
// Everything else here reads CloudWatch, which lags by minutes and cannot tell
// "no metrics were published" apart from "the service is down". A request the
// operator asks for, to an address they typed, answers that directly.
type HealthCheck struct {
	// URL is the address to request. http and https only.
	URL string `json:"url"`
	// ExpectStatus is the status code that counts as healthy. Zero means any
	// 2xx, which is what most people mean by "it works".
	ExpectStatus int `json:"expectStatus"`
}

// OK reports whether status is the one this check calls healthy.
func (h HealthCheck) OK(status int) bool {
	if h.ExpectStatus > 0 {
		return status == h.ExpectStatus
	}
	return status >= 200 && status < 300
}

// Limits bound the work a single request may provoke.
type Limits struct {
	// LogRows caps the detail lists. Totals are always counted separately, so
	// capping a list never distorts the number displayed beside it.
	LogRows int `json:"logRows"`
	// TopN caps the breakdown tables.
	TopN int `json:"topN"`
	// InsightsConcurrency bounds how many Logs Insights queries may run at
	// once. CloudWatch allows about thirty per account; staying well under
	// leaves room for anything else using the same credentials.
	InsightsConcurrency int `json:"insightsConcurrency"`
	// QueryTimeoutSeconds bounds a single Insights query.
	QueryTimeoutSeconds int `json:"queryTimeoutSeconds"`
	// CacheTTLSeconds is how long an identical request is served from memory.
	CacheTTLSeconds int `json:"cacheTtlSeconds"`
}

// DefaultLimits are tuned for a single operator watching one cluster.
func DefaultLimits() Limits {
	return Limits{
		LogRows: 300,
		TopN:    20,
		// The WAF page issues seven Insights queries, and since panels are built
		// concurrently (api.handlePage) they now arrive as one wave rather than
		// three. At six, one query waited out a full query's latency before it
		// could start. Two runners exist when the WAF region differs from the
		// working region (cmd/skills-dashboard/main.go), each with its own
		// semaphore, so this is sixteen in the worst case against CloudWatch's
		// thirty — still comfortable.
		InsightsConcurrency: 8,
		QueryTimeoutSeconds: 45,
		// One period. The log-query cache key pins the window bounds
		// (api.runLogQueries) and NewWindow floors End to the period, so a key
		// is reachable for exactly one period and then never again. At thirty
		// seconds against the default 1m period, the back half of every minute
		// was a guaranteed miss on a key whose answer was already in memory.
		//
		// Not raised further on purpose. The window is fixed but its contents
		// are not: CloudWatch delivers records late, so the same query over the
		// same window can return more rows a minute later. Living exactly as
		// long as the key is the most this can claim.
		CacheTTLSeconds: 60,
	}
}

// Default returns a config with everything set except the resource names,
// which have to be discovered or typed in.
func Default() Config {
	return Config{
		Region:       "ap-northeast-2",
		WAFRegion:    "us-east-1",
		Namespace:    "default",
		TargetGroups: []string{},
		RDSProxies:   []string{},
		WebACLs:      []string{},
		WAFHeaders:   domain.DefaultWAFHeaders(),
		LogFormat:    domain.DefaultLogFormat(),
		Limits:       DefaultLimits(),
	}
}

// PodLogGroupOrDefault derives the Container Insights application log group
// from the cluster name when one has not been set explicitly.
func (c Config) PodLogGroupOrDefault() string {
	if c.PodLogGroup != "" {
		return c.PodLogGroup
	}
	if c.ClusterName == "" {
		return ""
	}
	return "/aws/containerinsights/" + c.ClusterName + "/application"
}

// Validate fills in defaults for anything unset and reports what is unusable.
// It is the save path: a person is looking at the settings page, so refusing
// the write and saying why is the feedback they came for.
func (c *Config) Validate() error {
	ps := c.inspect()
	if len(ps) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(ps))
	for _, p := range ps {
		msgs = append(msgs, p.msg)
	}
	return errors.New(strings.Join(msgs, "; "))
}

// Repair fills in defaults, discards whatever is left unusable, and returns a
// line about each thing it discarded.
//
// It is the load path, and it deliberately does not fail. A stored value that
// Validate would refuse — one an older build accepted, or a hand-edited file —
// must not keep the dashboard from starting: the settings page is the only
// place to fix it, and it cannot be reached from a process that exited. This is
// the same bargain the credentials already make in cmd/skills-dashboard.
func (c *Config) Repair() []string {
	ps := c.inspect()
	notes := make([]string, 0, len(ps))
	for _, p := range ps {
		p.drop(c)
		notes = append(notes, p.msg+" → "+p.dropped)
	}
	return notes
}

// problem is one thing a config cannot be used with, together with how to make
// it usable without that thing.
//
// Both paths read this one list, so the rule the settings page enforces and the
// rule the loader applies cannot drift apart.
type problem struct {
	// msg says what is wrong. It is the error text on the save path.
	msg string
	// dropped says what was done about it. It is appended on the load path.
	dropped string
	drop    func(*Config)
}

// inspect fills in defaults and returns what is still unusable.
func (c *Config) inspect() []problem {
	c.fillDefaults()

	var out []problem
	if c.Region == "" {
		out = append(out, problem{
			msg:     "region이 설정되지 않았습니다",
			dropped: "기본 리전 " + Default().Region + " 을 사용합니다.",
			drop:    func(c *Config) { c.Region = Default().Region },
		})
	}
	if c.LoadBalancer != "" && !albDimensionRe.MatchString(c.LoadBalancer) {
		out = append(out, problem{
			msg: fmt.Sprintf(
				"loadBalancer %q는 CloudWatch 차원 값이 아닙니다. app/<이름>/<ID> 형식이어야 합니다 "+
					"(예: app/my-alb/50dc6c495c0c9188). 전체 ARN을 붙여넣어도 됩니다",
				c.LoadBalancer),
			dropped: "이 값을 비웠습니다. 설정에서 다시 선택하세요.",
			drop:    func(c *Config) { c.LoadBalancer = "" },
		})
	}
	// The one address in this file that the dashboard itself will request, so
	// it is the one that has to be checked as an address and not just as text.
	// A scheme other than http/https would let a stored config point the
	// process at file:// or a custom handler; refusing at the boundary is
	// cheaper than reasoning about what each scheme would do.
	if c.Check.URL != "" {
		if reason := checkURLProblem(c.Check.URL); reason != "" {
			out = append(out, problem{
				msg:     fmt.Sprintf("check.url %q는 사용할 수 없습니다: %s", c.Check.URL, reason),
				dropped: "이 값을 비웠습니다. 설정에서 다시 입력하세요.",
				drop:    func(c *Config) { c.Check = HealthCheck{} },
			})
		}
	}
	if c.Check.ExpectStatus != 0 && (c.Check.ExpectStatus < 100 || c.Check.ExpectStatus > 599) {
		out = append(out, problem{
			msg:     fmt.Sprintf("check.expectStatus %d는 HTTP 상태 코드가 아닙니다", c.Check.ExpectStatus),
			dropped: "2xx 를 정상으로 보도록 되돌렸습니다.",
			drop:    func(c *Config) { c.Check.ExpectStatus = 0 },
		})
	}
	if err := c.LogFormat.Validate(); err != nil {
		out = append(out, problem{
			msg:     fmt.Sprintf("logFormat: %v", err),
			dropped: "로그 형식을 기본값으로 되돌렸습니다.",
			drop:    func(c *Config) { c.LogFormat = domain.DefaultLogFormat() },
		})
	}
	// Only the headers that cannot be embedded are dropped; the rest of the
	// list is a working selection and survives.
	var badHeaders []string
	for _, h := range c.WAFHeaders {
		if _, err := (domain.WAFQueries{}).ByHeader(h, 1); err != nil {
			badHeaders = append(badHeaders, h)
		}
	}
	if len(badHeaders) > 0 {
		out = append(out, problem{
			msg:     fmt.Sprintf("wafHeaders: %s 는 쿼리에 넣을 수 없는 헤더 이름입니다", strings.Join(badHeaders, ", ")),
			dropped: "해당 헤더를 목록에서 제외했습니다.",
			drop: func(c *Config) {
				kept := c.WAFHeaders[:0]
				for _, h := range c.WAFHeaders {
					if _, err := (domain.WAFQueries{}).ByHeader(h, 1); err == nil {
						kept = append(kept, h)
					}
				}
				c.WAFHeaders = kept
			},
		})
	}
	return out
}

// fillDefaults supplies everything that was left unset and converts what can be
// converted. Nothing here can fail — a conversion that does not produce a
// usable value is reported by inspect instead.
func (c *Config) fillDefaults() {
	if c.TargetGroups == nil {
		c.TargetGroups = []string{}
	}
	if c.RDSProxies == nil {
		c.RDSProxies = []string{}
	}
	if c.WebACLs == nil {
		c.WebACLs = []string{}
	}
	if c.WAFRegion == "" {
		c.WAFRegion = "us-east-1"
	}
	if c.Limits.LogRows <= 0 {
		c.Limits.LogRows = DefaultLimits().LogRows
	}
	if c.Limits.TopN <= 0 {
		c.Limits.TopN = DefaultLimits().TopN
	}
	if c.Limits.InsightsConcurrency <= 0 {
		c.Limits.InsightsConcurrency = DefaultLimits().InsightsConcurrency
	}
	if c.Limits.QueryTimeoutSeconds <= 0 {
		c.Limits.QueryTimeoutSeconds = DefaultLimits().QueryTimeoutSeconds
	}
	if c.Limits.CacheTTLSeconds < 0 {
		c.Limits.CacheTTLSeconds = DefaultLimits().CacheTTLSeconds
	}
	if len(c.LogFormat.OKStatuses) == 0 {
		c.LogFormat.OKStatuses = domain.DefaultLogFormat().OKStatuses
	}
	// ExcludePaths is left alone when empty: an operator who cleared the list
	// wants probe traffic back, and silently reinstating the defaults would
	// make that impossible.
	for i, p := range c.LogFormat.ExcludePaths {
		c.LogFormat.ExcludePaths[i] = strings.TrimSpace(p)
	}
	c.normaliseDimensions()
}

// albDimensionRe is the shape CloudWatch gives an Application Load Balancer:
// app/<name>/<id>. The dashboard reads only AWS/ApplicationELB, so an NLB or
// gateway dimension would be as useless here as a name or an ARN.
var albDimensionRe = regexp.MustCompile(`^app/[^/]+/[0-9a-fA-F]+$`)

// checkURLProblem says why a health-check URL cannot be requested, or "" when
// it can. Both the save path and the load path go through it, so the settings
// screen refuses exactly what the loader would have discarded.
func checkURLProblem(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "주소를 해석할 수 없습니다"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "http 또는 https 로 시작해야 합니다"
	}
	if u.Host == "" {
		return "호스트가 없습니다"
	}
	return ""
}

// normaliseDimensions converts the load balancer and target group entries into
// the form CloudWatch expects.
//
// Both fields hold a CloudWatch dimension value rather than an ARN, and the
// SEARCH value regex (domain.searchValueRe) accepts ':' and '/' — so an ARN
// pasted here passes every check and then matches no metric at all. The panel
// renders empty with no warning, which reads as "this load balancer had no
// traffic". Converting what can be converted is how that silence is avoided;
// what the conversion cannot rescue is left for inspect to report.
func (c *Config) normaliseDimensions() {
	c.LoadBalancer = domain.LoadBalancerDimension(strings.TrimSpace(c.LoadBalancer))
	for i, tg := range c.TargetGroups {
		c.TargetGroups[i] = domain.TargetGroupDimension(strings.TrimSpace(tg))
	}
}

// Dir is where the config file lives.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".skills-dashboard"), nil
}

// Path is the config file itself.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Store holds the active config and serialises access to it, so a save from
// the settings page cannot interleave with a read from a data handler.
type Store struct {
	mu   sync.RWMutex
	cfg  Config
	path string

	// notices records what loading the file had to change. It is fixed at
	// construction, so it needs no lock.
	notices []string
}

// NewStore loads the config at path, falling back to defaults when the file
// does not exist yet.
//
// Nothing about the file's *contents* is fatal. A value an older build accepted
// but this one will not, or a file someone edited into invalid JSON, must not
// stop the dashboard from starting — the settings page is where such a thing
// gets fixed, and a process that exited serves no settings page. Only being
// unable to read the file at all is reported as an error.
func NewStore(path string) (*Store, error) {
	s := &Store{path: path, cfg: Default()}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var storedShape struct {
		LogFormat struct {
			Preset *domain.LogPreset `json:"preset"`
		} `json:"logFormat"`
	}
	hasLogPreset := json.Unmarshal(b, &storedShape) == nil && storedShape.LogFormat.Preset != nil

	cfg := Default()
	if err := json.Unmarshal(b, &cfg); err != nil {
		s.notices = append(s.notices,
			fmt.Sprintf("설정 파일을 읽을 수 없어 기본값으로 시작했습니다 (%v).", err))
		// Starting from defaults means the next save overwrites the file, so
		// the original is moved aside rather than left to be lost.
		if bak, err := keepAside(path); err == nil {
			s.notices = append(s.notices, "원본은 "+bak+" 에 두었습니다.")
		}
		return s, nil
	}
	if !hasLogPreset {
		cfg.LogFormat = domain.DefaultLogFormat()
		s.notices = append(s.notices,
			"기존 로그 파싱 규칙 → 지우고 Gin·JSON 자동 인식값을 적용했습니다.")
	}
	s.notices = append(s.notices, cfg.Repair()...)
	s.cfg = cfg
	return s, nil
}

// keepAside renames a file that could not be understood, returning where it
// went.
func keepAside(path string) (string, error) {
	bak := path + ".bak"
	if err := os.Rename(path, bak); err != nil {
		return "", err
	}
	return bak, nil
}

// Notices is what loading the config had to change, in words meant for the
// settings page. Empty when the file was already usable.
func (s *Store) Notices() []string {
	return append([]string(nil), s.notices...)
}

// Get returns a copy of the current config. Callers get their own slices, so a
// handler holding a config while a save lands cannot observe a half-updated one.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.clone()
}

// Set validates and stores cfg, then persists it.
func (s *Store) Set(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg.clone()
	path := s.path
	snapshot := s.cfg.clone()
	s.mu.Unlock()

	if path == "" {
		return nil
	}
	return save(path, snapshot)
}

func save(path string, cfg Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'))
}

// writeFileAtomic replaces path with b, creating the directory if it is not
// there yet.
//
// Write and rename, so an interrupted save cannot leave a truncated file that
// fails to parse on the next start. The modes are what they are because the
// directory also holds credentials.json: 0700 on the directory and 0600 on the
// file keep an access key out of reach of other accounts on the machine.
func writeFileAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// clone copies a config, and every list in it comes out empty rather than nil.
//
// The empty start is what keeps `null` off the wire. A nil slice marshals to
// `null`, the browser's Config declares these as `string[]`, and the settings
// page's first `.filter` on one of them throws — which takes the whole
// monitoring-targets section down with it and leaves a page that looks like it
// has no resource fields at all. Nothing selected yet is the state a fresh
// install is in, so that was every first run.
//
// This is the one seam every stored config crosses on its way out: Get feeds
// /api/config, and Set feeds both the in-memory copy and the file, so a
// hand-edited `"targetGroups": null` is normalised on the next save too.
func (c Config) clone() Config {
	out := c
	out.TargetGroups = append([]string{}, c.TargetGroups...)
	out.RDSProxies = append([]string{}, c.RDSProxies...)
	out.WebACLs = append([]string{}, c.WebACLs...)
	out.WAFHeaders = append([]string{}, c.WAFHeaders...)
	out.LogFormat.OKStatuses = append([]int{}, c.LogFormat.OKStatuses...)
	out.LogFormat.ExcludePaths = append([]string{}, c.LogFormat.ExcludePaths...)
	return out
}
