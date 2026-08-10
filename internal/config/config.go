package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
		LogRows:             300,
		TopN:                20,
		InsightsConcurrency: 6,
		QueryTimeoutSeconds: 45,
		CacheTTLSeconds:     30,
	}
}

// Default returns a config with everything set except the resource names,
// which have to be discovered or typed in.
func Default() Config {
	return Config{
		Region:     "ap-northeast-2",
		WAFRegion:  "us-east-1",
		Namespace:  "default",
		WAFHeaders: domain.DefaultWAFHeaders(),
		LogFormat:  domain.DefaultLogFormat(),
		Limits:     DefaultLimits(),
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
func (c *Config) Validate() error {
	if c.Region == "" {
		return fmt.Errorf("region is not set")
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
	if err := c.LogFormat.Validate(); err != nil {
		return fmt.Errorf("logFormat: %w", err)
	}
	for _, h := range c.WAFHeaders {
		if _, err := (domain.WAFQueries{}).ByHeader(h, 1); err != nil {
			return fmt.Errorf("wafHeaders: %w", err)
		}
	}
	return nil
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
}

// NewStore loads the config at path, falling back to defaults when the file
// does not exist yet.
func NewStore(path string) (*Store, error) {
	s := &Store{path: path, cfg: Default()}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	cfg := Default()
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.cfg = cfg
	return s, nil
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Write and rename so an interrupted save cannot leave a truncated file
	// that fails to parse on the next start.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c Config) clone() Config {
	out := c
	out.TargetGroups = append([]string(nil), c.TargetGroups...)
	out.RDSProxies = append([]string(nil), c.RDSProxies...)
	out.WebACLs = append([]string(nil), c.WebACLs...)
	out.WAFHeaders = append([]string(nil), c.WAFHeaders...)
	out.LogFormat.OKStatuses = append([]int(nil), c.LogFormat.OKStatuses...)
	out.LogFormat.ExcludePaths = append([]string(nil), c.LogFormat.ExcludePaths...)
	return out
}
