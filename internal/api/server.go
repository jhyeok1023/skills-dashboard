// Package api serves the dashboard's HTTP interface.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/jhyeok1023/skills-dashboard/internal/awsx"
	"github.com/jhyeok1023/skills-dashboard/internal/config"
	"github.com/jhyeok1023/skills-dashboard/internal/domain"
)

// Service carries everything a handler needs. One instance lives for the life
// of the process, so the AWS clients and their connection pools are built once.
type Service struct {
	Clients  *awsx.Clients
	Store    *config.Store
	Metrics  *awsx.MetricFetcher
	Insights *awsx.InsightsRunner
	Cache    *awsx.Cache
	Identity awsx.Identity
	Logger   *slog.Logger

	// InsightsGlobal queries the WAF region. A CLOUDFRONT-scoped web ACL
	// publishes its logs only into us-east-1, so a runner bound to the working
	// region cannot see the log group at all — StartQuery fails with a group
	// that does not exist. When the two regions coincide this is the same
	// runner as Insights, so the concurrency budget stays a single pool.
	InsightsGlobal *awsx.InsightsRunner

	// Now is overridable so tests can pin the window.
	Now func() time.Time

	// CredentialError, when set, is why AWS is unreachable. Handlers report it
	// instead of failing opaquely, so the settings page can explain what to fix.
	CredentialError error
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Handler builds the API router. Everything under /api is served here; the
// caller mounts the embedded UI on everything else.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/identity", s.handleIdentity)

	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("POST /api/logfmt/preview", s.handleLogFormatPreview)

	mux.HandleFunc("GET /api/discovery/{kind}", s.handleDiscovery)

	// Single-panel endpoints and page endpoints share the same builders, so a
	// panel fetched on its own and the same panel inside a page snapshot are
	// byte-for-byte identical. That is asserted in the tests: it is the
	// mechanism that keeps an overview tile and its detail view in agreement.
	mux.HandleFunc("GET /api/panel/{id}", s.handlePanel)
	mux.HandleFunc("GET /api/page/{id}", s.handlePage)

	return s.recoverPanics(mux)
}

// recoverPanics keeps one bad response from taking the process down.
//
// The reference implementation had two live panic paths — an unchecked slice
// index in the metric decoder and a nil statement in the ingest goroutine — and
// no recovery anywhere, so either one ended the dashboard rather than one
// panel. A panic is still a bug and is logged as one; this only bounds the
// blast radius.
func (s *Service) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			s.log().Error("panic while serving", "path", r.URL.Path, "panic", rec,
				"stack", string(debug.Stack()))
			writeError(w, http.StatusInternalServerError,
				fmt.Errorf("internal error while serving %s", r.URL.Path), "")
		}()
		next.ServeHTTP(w, r)
	})
}

// errorResponse is the shape every failure takes.
type errorResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// The dashboard reads live data; a cached response would quietly show a
	// stale window after the user changed the range.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Default().Error("write response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error, hint string) {
	writeJSON(w, status, errorResponse{Error: http.StatusText(status), Detail: err.Error(), Hint: hint})
}

// badRequest reports a client mistake, such as a range beyond the four-hour cap.
func badRequest(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadRequest, err, "")
}

// upstream reports a failure that came from AWS rather than from the caller.
func upstream(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadGateway, err, "AWS 호출이 실패했습니다. 자격증명과 권한을 확인하세요.")
}

// requireAWS reports whether the service has usable credentials.
func (s *Service) requireAWS(w http.ResponseWriter) bool {
	if s.CredentialError != nil {
		writeError(w, http.StatusServiceUnavailable, s.CredentialError,
			".env 파일에 AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION을 설정한 뒤 다시 실행하세요.")
		return false
	}
	if s.Clients == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS clients are not configured"),
			".env 파일을 확인하세요.")
		return false
	}
	return true
}

// region is where the primary clients point, and wafRegion is where the
// CLOUDFRONT-scope ones do.
//
// Both read the clients rather than the stored config. The clients were built
// from the credentials' region (awsx.New), while config.json carries a region
// of its own that nothing enforces — asking the config which region a call
// lands in is how the two came to disagree. With no clients there is nothing to
// describe, so the config is all that is left to answer with.
func (s *Service) region() string {
	if s.Clients != nil {
		return s.Clients.Region
	}
	return s.Store.Get().Region
}

func (s *Service) wafRegion() string {
	if s.Clients != nil {
		return s.Clients.WAFRegion
	}
	return s.Store.Get().WAFRegion
}

// window resolves the range and period from the query string.
//
// It is called once per request and the result is threaded through every panel
// builder. Anchoring each panel to its own clock is how two charts on one
// screen come to describe slightly different spans.
func (s *Service) window(r *http.Request) (domain.Window, error) {
	q := r.URL.Query()
	return domain.Resolve(s.now(), q.Get("range"), q.Get("period"))
}

func (s *Service) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          s.CredentialError == nil,
		"credentials": s.CredentialError == nil,
	})
}

// metaResponse tells the frontend which range and period combinations exist, so
// the UI selector cannot offer something the server would reject.
type metaResponse struct {
	MaxRangeSeconds int              `json:"maxRangeSeconds"`
	Ranges          []metaRangeEntry `json:"ranges"`
	DefaultRange    string           `json:"defaultRange"`
	Limits          config.Limits    `json:"limits"`
}

type metaRangeEntry struct {
	Range         string   `json:"range"`
	Seconds       int      `json:"seconds"`
	Periods       []string `json:"periods"`
	DefaultPeriod string   `json:"defaultPeriod"`
}

func (s *Service) handleMeta(w http.ResponseWriter, _ *http.Request) {
	resp := metaResponse{
		MaxRangeSeconds: int(domain.MaxRange.Seconds()),
		DefaultRange:    domain.Range1h.String(),
		Limits:          s.Store.Get().Limits,
	}
	for _, r := range domain.Ranges() {
		entry := metaRangeEntry{
			Range:         r.String(),
			Seconds:       int(r.Duration().Seconds()),
			DefaultPeriod: domain.DefaultPeriod(r).String(),
		}
		for _, p := range domain.PeriodsFor(r) {
			entry.Periods = append(entry.Periods, p.String())
		}
		resp.Ranges = append(resp.Ranges, entry)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) handleIdentity(w http.ResponseWriter, _ *http.Request) {
	if s.CredentialError != nil {
		writeError(w, http.StatusServiceUnavailable, s.CredentialError,
			".env 파일에 AWS 액세스 키를 설정하세요.")
		return
	}
	writeJSON(w, http.StatusOK, s.Identity)
}

// panelBuilders maps a panel id to the function that assembles it. Panels are
// looked up by name from both the panel and the page endpoints, which is what
// guarantees the two produce the same thing.
func (s *Service) panelBuilders() map[string]panelBuilder {
	return map[string]panelBuilder{
		"targetgroup":      s.buildTargetGroupPanel,
		"pod-resource":     s.buildPodResourcePanel,
		"node-resource":    s.buildNodeResourcePanel,
		"counts":           s.buildCountsPanel,
		"pod-status":       s.buildPodStatusPanel,
		"rds-proxy":        s.buildRDSProxyPanel,
		"waf-metrics":      s.buildWAFMetricsPanel,
		"pod-latency":      s.buildPodLatencyPanel,
		"pod-status-codes": s.buildPodStatusCodePanel,
		"pod-errors":       s.buildPodErrorPanel,
		"waf-traffic":      s.buildWAFTrafficPanel,
		"waf-blocked":      s.buildWAFBlockedPanel,
		"waf-breakdown":    s.buildWAFBreakdownPanel,
	}
}

type panelBuilder func(ctx requestCtx) (*domain.Panel, error)

// pages lists which panels each screen shows.
var pages = map[string][]string{
	"overview":    {"pod-latency", "pod-status-codes", "targetgroup", "counts", "pod-status", "waf-traffic"},
	"pod-logs":    {"pod-latency", "pod-status-codes", "pod-errors"},
	"waf":         {"waf-traffic", "waf-blocked", "waf-breakdown", "waf-metrics"},
	"targetgroup": {"targetgroup"},
	"kubernetes":  {"pod-resource", "node-resource", "counts", "pod-status"},
	"database":    {"rds-proxy"},
}

func (s *Service) handlePanel(w http.ResponseWriter, r *http.Request) {
	if !s.requireAWS(w) {
		return
	}
	id := r.PathValue("id")
	build, ok := s.panelBuilders()[id]
	if !ok {
		badRequest(w, fmt.Errorf("unknown panel %q", id))
		return
	}
	win, err := s.window(r)
	if err != nil {
		badRequest(w, err)
		return
	}

	rc := requestCtx{ctx: r.Context(), w: win, cfg: s.Store.Get()}
	payload := domain.NewPayload(win)
	panel, err := build(rc)
	if err != nil {
		upstream(w, err)
		return
	}
	payload.Add(panel)
	s.finish(w, payload)
}

func (s *Service) handlePage(w http.ResponseWriter, r *http.Request) {
	if !s.requireAWS(w) {
		return
	}
	id := r.PathValue("id")
	ids, ok := pages[id]
	if !ok {
		badRequest(w, fmt.Errorf("unknown page %q", id))
		return
	}
	win, err := s.window(r)
	if err != nil {
		badRequest(w, err)
		return
	}

	rc := requestCtx{ctx: r.Context(), w: win, cfg: s.Store.Get()}
	builders := s.panelBuilders()
	payload := domain.NewPayload(win)

	// Panels are built sequentially against a shared cache; the expensive work
	// underneath is already concurrent, and a failing panel must not blank the
	// ones beside it.
	for _, pid := range ids {
		build, ok := builders[pid]
		if !ok {
			continue
		}
		panel, err := build(rc)
		if err != nil {
			s.log().Warn("panel failed", "panel", pid, "error", err)
			payload.Add(&domain.Panel{
				ID:       pid,
				Title:    pid,
				Warnings: []string{err.Error()},
			})
			continue
		}
		payload.Add(panel)
	}
	s.finish(w, payload)
}

// finish validates the payload before it goes out. A series that has drifted
// out of alignment with the time axis is a bug that would otherwise render as
// plausible-looking but shifted data.
func (s *Service) finish(w http.ResponseWriter, payload *domain.Payload) {
	if err := payload.Validate(); err != nil {
		s.log().Error("payload failed validation", "error", err)
		writeError(w, http.StatusInternalServerError, err, "")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}
