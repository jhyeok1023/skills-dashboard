package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jhyeok1023/skills-dashboard/internal/awsx"
	"github.com/jhyeok1023/skills-dashboard/internal/config"
)

// CredentialSource says where the key in use came from, so the settings page
// can show it. Which of the two wins is a decision, not an accident, and a
// screen that does not name the winner leaves an operator editing the file that
// is being ignored.
type CredentialSource string

const (
	// SourceSaved is the key saved from the settings page. It wins.
	SourceSaved CredentialSource = "saved"
	// SourceEnv is the key read from .env or the process environment.
	SourceEnv CredentialSource = "env"
	// SourceNone is no key at all.
	SourceNone CredentialSource = "none"
)

// AWSConn is one set of AWS clients and everything built on top of them.
//
// It is replaced whole rather than field by field. Saving a key used to be
// impossible, so the clients were built once at start and read straight off the
// Service without a lock; now that a save can replace them mid-flight, swapping
// the fields one at a time would let a request read half of each — Insights on
// the new account and metrics on the old — and the panel it produced would
// describe neither.
type AWSConn struct {
	Clients        *awsx.Clients
	Metrics        *awsx.MetricFetcher
	Insights       *awsx.InsightsRunner
	InsightsGlobal *awsx.InsightsRunner
	Identity       awsx.Identity

	// Source is where the key came from.
	Source CredentialSource

	// Err is why there is no usable connection, nil when there is one.
	// Handlers report it rather than failing opaquely, so the settings page can
	// explain what to fix.
	Err error
}

// ok reports whether this connection can be called through.
func (c *AWSConn) ok() bool { return c != nil && c.Err == nil && c.Clients != nil }

// credentials returns the saved-key store. A Service assembled without one —
// which is how the tests build it — gets an empty store with no path behind it,
// so reading it is safe and saving to it writes nothing.
func (s *Service) credentials() *config.CredentialStore {
	if s.Credentials != nil {
		return s.Credentials
	}
	return &config.CredentialStore{}
}

// AWS returns the connection in force. It never returns nil, so a caller can
// read Err without a guard first.
func (s *Service) AWS() *AWSConn {
	if c := s.conn.Load(); c != nil {
		return c
	}
	return &AWSConn{Source: SourceNone, Err: errors.New("AWS clients are not configured")}
}

// SetAWS installs a connection, dropping whatever the previous one had cached.
//
// The cache is keyed by region and resource, not by account, so an answer
// fetched with the old key would be served as the new key's answer — the same
// number under a different account. Emptying it is what keeps a swap honest.
func (s *Service) SetAWS(c *AWSConn) {
	s.conn.Store(c)
	if s.Cache != nil {
		s.Cache.Invalidate()
	}
}

// connect is the seam every caller goes through, so a test can decide what a
// key resolves to without an AWS account behind it. Production leaves
// Connector nil and gets Connect.
func (s *Service) connect(ctx context.Context, creds config.Credentials, source CredentialSource) *AWSConn {
	if s.Connector != nil {
		return s.Connector(ctx, creds, source)
	}
	return s.Connect(ctx, creds, source)
}

// Connect builds a connection from one set of credentials.
//
// It is the only path to a set of AWS clients, so a key saved from the settings
// page and a key read at start-up go through the same checks in the same order
// and reach the same state. A failure comes back inside the AWSConn rather than
// as a second return value: "there is no connection, and this is why" is a
// thing handlers have to carry either way.
func (s *Service) Connect(ctx context.Context, creds config.Credentials, source CredentialSource) *AWSConn {
	if err := creds.Validate(); err != nil {
		return &AWSConn{Source: source, Err: err}
	}

	cfg := s.Store.Get()
	clients, err := awsx.New(ctx, creds, cfg.WAFRegion)
	if err != nil {
		return &AWSConn{Source: source, Err: err}
	}

	runner := func(api awsx.LogsAPI) *awsx.InsightsRunner {
		return &awsx.InsightsRunner{
			Concurrency: cfg.Limits.InsightsConcurrency,
			Timeout:     time.Duration(cfg.Limits.QueryTimeoutSeconds) * time.Second,
			API:         api,
		}
	}
	conn := &AWSConn{
		Clients:  clients,
		Metrics:  &awsx.MetricFetcher{},
		Insights: runner(clients.Logs),
		Source:   source,
	}
	// WAF logs need their own runner. A CLOUDFRONT-scoped web ACL publishes
	// only into us-east-1, so querying its log group through the working-region
	// client fails on a group that is not there. When the regions coincide the
	// runner is shared, which keeps the concurrency limit a single pool rather
	// than two of the same size.
	if clients.WAFRegion == clients.Region {
		conn.InsightsGlobal = conn.Insights
	} else {
		conn.InsightsGlobal = runner(clients.LogsGlobal)
	}

	// The credentials decide the region; config.json only records it. Leaving a
	// stale region in the file is what let the dashboard reason about one region
	// while calling another.
	if cfg.Region != clients.Region {
		s.log().Info("recording the credentials' region in the config",
			"was", cfg.Region, "now", clients.Region)
		cfg.Region = clients.Region
		if err := s.Store.Set(cfg); err != nil {
			s.log().Warn("could not save the region to the config", "error", err)
		}
	}

	whoCtx, cancel := context.WithTimeout(ctx, identityTimeout)
	identity, err := awsx.WhoAmI(whoCtx, clients.STS, clients.Region)
	cancel()
	if err != nil {
		conn.Err = fmt.Errorf("자격증명 확인 실패: %w", err)
		return conn
	}
	identity.WAFRegion = clients.WAFRegion
	conn.Identity = identity
	s.log().Info("credentials accepted", "source", source,
		"account", identity.Account, "region", identity.Region,
		"wafRegion", identity.WAFRegion, "key", creds.Redacted())
	return conn
}

// identityTimeout bounds the one call that decides whether a key works.
const identityTimeout = 15 * time.Second

// Resolve picks the credentials to run with and connects to AWS with them.
//
// A saved key wins over .env, and the reason it does is that the settings page
// is the more recent and more deliberate statement of which account to watch —
// a key typed there and then ignored in favour of a file the operator may not
// know exists is the worse surprise of the two. The one that lost is not
// silent about it: the settings page names the source in force.
func (s *Service) Resolve(ctx context.Context) *AWSConn {
	if creds, ok := s.credentials().Get(); ok {
		return s.connect(ctx, creds, SourceSaved)
	}
	creds, err := config.LoadCredentials(s.EnvFile)
	if err != nil {
		return &AWSConn{Source: SourceEnv, Err: err}
	}
	if err := creds.Validate(); err != nil {
		return &AWSConn{Source: SourceNone, Err: err}
	}
	return s.connect(ctx, creds, SourceEnv)
}
