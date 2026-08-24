// Package api serves the dashboard's HTTP interface.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jhyeok1023/skills-dashboard/internal/awsx"
	"github.com/jhyeok1023/skills-dashboard/internal/config"
	"github.com/jhyeok1023/skills-dashboard/internal/domain"
)

// Service carries everything a handler needs. One instance lives for the life
// of the process.
type Service struct {
	Store  *config.Store
	Cache  *awsx.Cache
	Logger *slog.Logger

	// Credentials is the key saved from the settings page, if there is one.
	Credentials *config.CredentialStore

	// Connector overrides how a key becomes an AWS connection. Only tests set
	// it; production leaves it nil and gets Connect.
	Connector func(context.Context, config.Credentials, CredentialSource) *AWSConn

	// conn is the AWS connection every handler reads, through AWS(). It is a
	// pointer swap rather than a set of fields because the settings page can
	// replace it while requests are in flight — see conn.go.
	conn atomic.Pointer[AWSConn]

	// Now is overridable so tests can pin the window.
	Now func() time.Time

	// EnvFile is the .env the credentials were read from, empty when none was
	// found. It rides into the hint so "fix your .env" names an actual path
	// rather than leaving the operator to guess which of two locations was used.
	EnvFile string

	// ConfigNotices is what loading the stored config had to change. A value
	// the loader discarded leaves a panel empty, and an empty panel with no
	// explanation is indistinguishable from a resource with no traffic.
	ConfigNotices []string

	// The traffic check's outbound client. See check.go.
	checkState
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

	// The key itself, kept apart from /api/config: the settings page sends the
	// config object it was given straight back on save, and a secret does not
	// belong on a round trip like that.
	mux.HandleFunc("GET /api/credentials", s.handleGetCredentials)
	mux.HandleFunc("PUT /api/credentials", s.handlePutCredentials)
	mux.HandleFunc("DELETE /api/credentials", s.handleDeleteCredentials)

	// POST, though it reads rather than writes: every call sends a real request
	// to a real service, so it must not be something a browser, a proxy or a
	// prefetch can decide to repeat on its own.
	mux.HandleFunc("POST /api/check", s.handleCheck)

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

// credentialHint says what to fix and where. The settings page shows the hint
// in preference to the detail, so the path has to travel in the hint or it is
// never seen.
//
// The page can now save a key itself, so that is what it leads with. The .env
// path still rides along, because the operator who put a key there is the one
// most likely to be looking for why it did not take.
func (s *Service) credentialHint() string {
	const fix = "설정 화면에서 AWS 액세스 키를 입력해 저장하세요."
	if s.EnvFile == "" {
		return fix + " .env 로 주려면 실행 파일과 같은 폴더나 ~/.skills-dashboard 에 두고 다시 실행하세요."
	}
	return fix + " 또는 " + s.EnvFile + " 에 AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION 을 설정하세요."
}

// requireAWS returns the connection a handler should use, or reports why there
// is none. The snapshot is taken once per request: a save landing halfway
// through must not move the ground under a page that is already building.
func (s *Service) requireAWS(w http.ResponseWriter) (*AWSConn, bool) {
	conn := s.AWS()
	if !conn.ok() {
		err := conn.Err
		if err == nil {
			err = errors.New("AWS clients are not configured")
		}
		writeError(w, http.StatusServiceUnavailable, err, s.credentialHint())
		return nil, false
	}
	return conn, true
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
	if c := s.AWS(); c.Clients != nil {
		return c.Clients.Region
	}
	return s.Store.Get().Region
}

func (s *Service) wafRegion() string {
	if c := s.AWS(); c.Clients != nil {
		return c.Clients.WAFRegion
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

var namespaceRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func (s *Service) requestConfig(r *http.Request) (config.Config, error) {
	cfg := s.Store.Get()
	cfg.LogFormat.Namespace = cfg.Namespace
	if !r.URL.Query().Has("namespace") {
		return cfg, nil
	}
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if namespace == "*" {
		cfg.LogFormat.Namespace = ""
		return cfg, nil
	}
	if len(namespace) > 63 || !namespaceRe.MatchString(namespace) {
		return cfg, fmt.Errorf("namespace %q는 올바른 Kubernetes namespace 이름이 아닙니다", namespace)
	}
	cfg.LogFormat.Namespace = namespace
	return cfg, nil
}

func (s *Service) handleHealth(w http.ResponseWriter, _ *http.Request) {
	ok := s.AWS().ok()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          ok,
		"credentials": ok,
	})
}

// metaResponse tells the frontend which range and period combinations exist, so
// the UI selector cannot offer something the server would reject.
type metaResponse struct {
	MaxRangeSeconds int              `json:"maxRangeSeconds"`
	Ranges          []metaRangeEntry `json:"ranges"`
	DefaultRange    string           `json:"defaultRange"`
	Limits          config.Limits    `json:"limits"`
	// Notices ride here rather than on /api/config because the settings page
	// sends the config object it was given straight back on save, and the PUT
	// handler rejects unknown fields.
	Notices []string `json:"notices,omitempty"`
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
		Notices:         s.ConfigNotices,
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
	conn, ok := s.requireAWS(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, conn.Identity)
}

// panelBuilders maps a panel id to the function that assembles it. Panels are
// looked up by name from both the panel and the page endpoints, which is what
// guarantees the two produce the same thing.
func (s *Service) panelBuilders() map[string]panelBuilder {
	return map[string]panelBuilder{
		"targetgroup":          s.buildTargetGroupPanel,
		"pod-cpu":              s.podResourcePanel("pod-cpu", "팟 CPU 사용률", "pod.cpu"),
		"pod-mem":              s.podResourcePanel("pod-mem", "팟 메모리 사용률", "pod.mem"),
		"node-cpu":             s.nodeResourcePanel("node-cpu", "노드 CPU 사용률", "node.cpu"),
		"node-mem":             s.nodeResourcePanel("node-mem", "노드 메모리 사용률", "node.mem"),
		"node-disk":            s.nodeResourcePanel("node-disk", "노드 디스크 사용률", "node.fs"),
		"counts":               s.buildCountsPanel,
		"pod-status":           s.buildPodStatusPanel,
		"rds-proxy":            s.buildRDSProxyPanel,
		"waf-metrics":          s.buildWAFMetricsPanel,
		"pod-latency":          s.buildPodLatencyPanel,
		"pod-status-codes":     s.buildPodStatusCodePanel,
		"pod-status-breakdown": s.buildPodStatusBreakdownPanel,
		"pod-errors":           s.buildPodErrorPanel,
		"waf-traffic":          s.buildWAFTrafficPanel,
		"waf-blocked":          s.buildWAFBlockedPanel,
		"waf-breakdown":        s.buildWAFBreakdownPanel,
	}
}

type panelBuilder func(ctx requestCtx) (*domain.Panel, error)

// pageBudget bounds one whole page request. It has to stay under the server's
// WriteTimeout (cmd/skills-dashboard/main.go) with room for the response to be
// written, so that a page which runs long degrades into warnings rather than a
// severed connection.
const pageBudget = 90 * time.Second

// pages lists which panels each screen shows.
var pages = map[string][]string{
	"overview":    {"pod-latency", "pod-status-codes", "targetgroup", "counts", "pod-status", "waf-traffic"},
	"pod-logs":    {"pod-latency", "pod-status-codes", "pod-status-breakdown", "pod-errors"},
	"waf":         {"waf-traffic", "waf-blocked", "waf-breakdown", "waf-metrics"},
	"targetgroup": {"targetgroup"},
	"kubernetes":  {"pod-cpu", "pod-mem", "node-cpu", "node-mem", "node-disk", "counts", "pod-status"},
	"database":    {"rds-proxy"},
}

func (s *Service) handlePanel(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireAWS(w)
	if !ok {
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

	cfg, err := s.requestConfig(r)
	if err != nil {
		badRequest(w, err)
		return
	}
	rc := requestCtx{ctx: r.Context(), w: win, cfg: cfg, aws: conn}
	payload := domain.NewPayload(win)
	panel, err := build(rc)
	if err != nil {
		if rc.ctx.Err() != nil {
			return
		}
		upstream(w, err)
		return
	}
	payload.Add(panel)
	s.finish(w, payload)
}

func (s *Service) handlePage(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireAWS(w)
	if !ok {
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

	cfg, err := s.requestConfig(r)
	if err != nil {
		badRequest(w, err)
		return
	}

	// A page sits on a wave of Logs Insights queries and can outlast the
	// server's own WriteTimeout — at which point Go closes the connection
	// mid-response and the browser reports a transport failure, blanking the
	// whole page. This deadline lands first, and lands *inside* a handler: the
	// queries still running are cancelled, noteQueryErrors turns them into
	// per-panel warnings, and the panels that did finish are still rendered. A
	// page that names its slow query beats a page that says nothing at all.
	//
	// The budget now covers one wave rather than one per panel, so it bounds
	// the slowest panel instead of their sum.
	ctx, cancel := context.WithTimeout(r.Context(), pageBudget)
	defer cancel()

	rc := requestCtx{ctx: ctx, w: win, cfg: cfg, aws: conn}
	payload := domain.NewPayload(win)

	panels := s.buildPanels(rc, id, ids, s.panelBuilders())
	// The budget expiring is different from the browser leaving: the first
	// still has partial panels worth rendering, the second has nobody to
	// render them to.
	if r.Context().Err() != nil {
		return
	}
	// Payload.Add is a bare append, so it stays on this goroutine.
	for _, panel := range panels {
		if panel != nil {
			payload.Add(panel)
		}
	}
	s.finish(w, payload)
}

// buildPanels builds every panel of a page at once and returns them in the
// order they were asked for.
//
// They used to be built one after another, and since each one sits on its own
// wave of Logs Insights queries — only the queries *within* a panel overlapped
// — a four-panel page cost the sum of four waves when it need only cost the
// longest of them. The WAF page was the worst of it: its metrics panel is a
// single GetMetricData that answers in well under a second, and it waited out
// three Insights waves before anyone saw it.
//
// Nothing the builders touch is shared mutable state. requestCtx carries a
// context, a window and a config snapshot, all read-only and passed by value;
// awsx.Cache is mutex-guarded and single-flight, so two panels asking the same
// question collapse into one call rather than racing; InsightsRunner keeps its
// own semaphore, so this queues against the concurrency limit instead of
// overrunning it; and noteQueryCost/noteQueryErrors only ever append to the
// panel handed to them.
//
// Results land in a fixed slot rather than being appended as they finish. The
// frontend renders payload.panels in array order and lays out the wide ones
// around it, so the response has to keep the order `pages` declares no matter
// which panel won the race.
func (s *Service) buildPanels(rc requestCtx, page string, ids []string, builders map[string]panelBuilder) []*domain.Panel {
	// Nothing used to record how long a panel took, so the cost of a page was
	// only ever visible as a wait. These sit at debug level: they answer "which
	// panel is the slow one" without adding a line per panel to every refresh.
	pageStarted := time.Now()

	// A request the browser has already dropped must not open a wave of paid
	// Insights scans. Sequential building used to check this between panels;
	// building them at once means checking before the wave and again inside
	// each goroutine, since a cancellation can land after the wave starts.
	if rc.ctx.Err() != nil {
		return nil
	}

	results := make([]*domain.Panel, len(ids))
	var wg sync.WaitGroup
	for i, pid := range ids {
		build, ok := builders[pid]
		if !ok {
			continue
		}
		wg.Add(1)
		go func(i int, pid string, build panelBuilder) {
			defer wg.Done()
			// recoverPanics wraps the handler's own goroutine and cannot see
			// this one, so a panic here would end the process rather than the
			// panel — the exact blast radius that middleware exists to bound.
			// Builders used to run inline and were covered by it; the recovery
			// has to move with them.
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				s.log().Error("panic while building panel", "panel", pid, "panic", rec,
					"stack", string(debug.Stack()))
				results[i] = warnPanel(pid, fmt.Errorf("패널을 만들지 못했습니다: %v", rec))
			}()
			if rc.ctx.Err() != nil {
				return
			}
			started := time.Now()
			panel, err := build(rc)
			s.log().Debug("panel built", "page", page, "panel", pid,
				"ms", time.Since(started).Milliseconds(), "failed", err != nil)
			if err != nil {
				s.log().Warn("panel failed", "panel", pid, "error", err)
				results[i] = warnPanel(pid, err)
				return
			}
			results[i] = panel
		}(i, pid, build)
	}
	wg.Wait()
	s.log().Debug("page built", "page", page, "panels", len(ids),
		"ms", time.Since(pageStarted).Milliseconds())
	return results
}

// warnPanel stands in for a panel that could not be built, so the rest of the
// page still renders and the gap says why it is there.
func warnPanel(id string, err error) *domain.Panel {
	return &domain.Panel{
		ID:       id,
		Title:    id,
		Warnings: []string{err.Error()},
	}
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
