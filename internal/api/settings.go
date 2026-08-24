package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	waftypes "github.com/aws/aws-sdk-go-v2/service/wafv2/types"

	"github.com/jhyeok1023/skills-dashboard/internal/awsx"
	"github.com/jhyeok1023/skills-dashboard/internal/domain"
)

func (s *Service) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.Get())
}

func (s *Service) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	// Start from the stored config so a partial body cannot silently blank
	// fields the caller did not mention.
	cfg := s.Store.Get()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		badRequest(w, fmt.Errorf("설정을 읽을 수 없습니다: %w", err))
		return
	}
	if err := s.Store.Set(cfg); err != nil {
		badRequest(w, err)
		return
	}
	// The resource selection changed, so anything cached against the old
	// selection is now describing the wrong thing.
	s.Cache.Invalidate()
	writeJSON(w, http.StatusOK, s.Store.Get())
}

// logFormatPreviewRequest is a sample log line plus the format to read it with.
type logFormatPreviewRequest struct {
	Sample string            `json:"sample"`
	Format *domain.LogFormat `json:"format"`
}

type logFormatPreviewResponse struct {
	Parsed    domain.LogLine `json:"parsed"`
	Matched   bool           `json:"matched"`
	BadStatus bool           `json:"badStatus"`
	// Excluded reports that this line's path is on the exclusion list, so it
	// would not reach any pod-log panel. Without it, an operator who mistyped
	// an excluded path would only find out by noticing a panel is emptier than
	// it should be.
	Excluded   bool   `json:"excluded"`
	Suggestion string `json:"suggestion,omitempty"`
}

// handleLogFormatPreview parses one pasted log line with the supplied format.
//
// The application log shape is still being settled, so the settings page needs
// a way to check a pattern against a real line before it is saved. Without it
// the only way to find out that a field name is wrong is to notice that a panel
// is quietly empty.
func (s *Service) handleLogFormatPreview(w http.ResponseWriter, r *http.Request) {
	var req logFormatPreviewRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		badRequest(w, fmt.Errorf("요청을 읽을 수 없습니다: %w", err))
		return
	}
	if req.Sample == "" {
		badRequest(w, fmt.Errorf("sample 로그 라인이 비어 있습니다"))
		return
	}

	format := s.Store.Get().LogFormat
	if req.Format != nil {
		format = *req.Format
	}
	if err := format.Validate(); err != nil {
		badRequest(w, err)
		return
	}

	line, err := format.Parse(req.Sample, s.now())
	if err != nil {
		badRequest(w, err)
		return
	}

	resp := logFormatPreviewResponse{
		Parsed:    line,
		Matched:   line.HasAccess || line.Level != "",
		BadStatus: format.IsBadStatus(line.Status),
		Excluded:  format.IsExcludedPath(line.Path),
	}
	switch {
	case !resp.Matched:
		resp.Suggestion = "요청 필드도 레벨도 인식되지 않았습니다. latencyField/statusField 이름이나 textPattern 정규식을 확인하세요."
	case resp.Excluded:
		resp.Suggestion = "이 경로는 제외 목록에 있어 팟 로그 패널에 집계되지 않습니다."
	}
	writeJSON(w, http.StatusOK, resp)
}

type discoveryResponse struct {
	Kind      string          `json:"kind"`
	Resources []awsx.Resource `json:"resources"`
	// Truncated reports that the page cap was reached with pages still waiting,
	// so the resource the operator is hunting for may simply not be on the list.
	Truncated bool `json:"truncated,omitempty"`
	// Partial names a scope that failed without failing the whole call. Today
	// only the CLOUDFRONT web ACL listing, which is discarded so a missing
	// permission cannot hide the regional ACLs the operator can actually use —
	// discarding it in silence is how "this account has no web ACLs" came to be
	// said on the strength of one denied call.
	Partial []string `json:"partial,omitempty"`
}

// discoveryResult is what one listing produced, before it becomes a response.
// It is what the cache stores, so a cached answer keeps its caveats.
type discoveryResult struct {
	awsx.Listing
	Partial []string
}

func (s *Service) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireAWS(w)
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	prefix := r.URL.Query().Get("prefix")

	// A start line, so a request that never comes back still leaves a trace of
	// having arrived. Without it, "the button did nothing" cannot be told apart
	// from "the request never reached the server".
	s.log().Debug("discovery requested", "kind", kind, "prefix", prefix)

	ctx, cancel := context.WithTimeout(r.Context(), discoveryTimeout)
	defer cancel()

	res, err := s.discover(ctx, conn, kind, prefix)
	if err != nil {
		if err == errUnknownKind {
			badRequest(w, fmt.Errorf("unknown discovery kind %q", kind))
			return
		}
		// Under retry-then-cancel the SDK reports "context deadline exceeded",
		// which reads as a broken dashboard rather than as a throttled or
		// unreachable account. Say which one it was.
		if ctx.Err() != nil {
			upstream(w, fmt.Errorf(
				"AWS 응답이 %d초 안에 오지 않았습니다 (재시도 중 취소됨). 계정이 스로틀링되고 있는지, 네트워크에서 AWS 엔드포인트에 닿는지 확인하세요: %w",
				int(discoveryTimeout.Seconds()), err))
			return
		}
		upstream(w, err)
		return
	}
	writeJSON(w, http.StatusOK, discoveryResponse{
		Kind:      kind,
		Resources: res.Resources,
		Truncated: res.Truncated,
		Partial:   res.Partial,
	})
}

// discoveryTimeout bounds one listing.
//
// The SDK's retry policy (five attempts, up to fifteen seconds of backoff) can
// outlast this on a sustained throttle, so a slow account will be cut off
// mid-retry rather than allowed to finish. That is the intended trade: a
// settings button that can sit silent for over a minute is worse than one that
// gives up at thirty seconds and says why.
const discoveryTimeout = 30 * time.Second

var errUnknownKind = fmt.Errorf("unknown discovery kind")

// cachedDiscovery runs one listing behind the cache and leaves a record of what
// it cost.
//
// Discovery is the first thing an operator points at when a resource does not
// appear on the settings page, and until now it left no trace whatsoever: a
// call that failed, a call that succeeded and found nothing, and a call that
// was never made because the cache answered were indistinguishable from
// outside the process. The log line goes inside the loader on purpose — the
// loader runs only on a miss, so a request that prints nothing was served from
// memory, and that distinction costs nothing to record.
func (s *Service) cachedDiscovery(
	ctx context.Context,
	key, kind, prefix string,
	load func(context.Context) (discoveryResult, error),
) (discoveryResult, error) {
	return awsx.Cached(ctx, s.Cache, key, func(ctx context.Context) (discoveryResult, error) {
		// Wall-clock, not s.now(): that one is pinned to a fixed instant in
		// tests, and this measures how long a call took rather than when it
		// happened.
		started := time.Now()
		res, err := load(ctx)
		elapsed := time.Since(started).Milliseconds()
		if err != nil {
			s.log().Warn("discovery failed", "kind", kind, "prefix", prefix,
				"region", s.region(), "wafRegion", s.wafRegion(),
				"elapsedMs", elapsed, "error", err)
			return discoveryResult{}, err
		}
		s.log().Info("discovery listed", "kind", kind, "prefix", prefix,
			"region", s.region(), "wafRegion", s.wafRegion(),
			"count", len(res.Resources), "truncated", res.Truncated,
			"partial", len(res.Partial), "elapsedMs", elapsed)
		return res, nil
	})
}

// listing adapts an awsx walk to a discoveryResult. The error is passed
// straight through: on a failure the caller ignores the value anyway.
func listing(l awsx.Listing, err error) (discoveryResult, error) {
	return discoveryResult{Listing: l}, err
}

func (s *Service) discover(ctx context.Context, conn *AWSConn, kind, prefix string) (discoveryResult, error) {
	var load func(context.Context) (discoveryResult, error)

	switch kind {
	case "targetgroups":
		load = func(ctx context.Context) (discoveryResult, error) {
			return listing(awsx.TargetGroups(ctx, conn.Clients.ELB))
		}
	case "loadbalancers":
		load = func(ctx context.Context) (discoveryResult, error) {
			return listing(awsx.LoadBalancers(ctx, conn.Clients.ELB))
		}
	case "loggroups":
		load = func(ctx context.Context) (discoveryResult, error) {
			return listing(awsx.LogGroups(ctx, conn.Clients.Logs, prefix))
		}
	case "waf-loggroups":
		// WAF log groups are listed from the WAF region. A CLOUDFRONT-scoped
		// web ACL writes only into us-east-1, so listing the working region
		// returns nothing and reads as "this account has no WAF logging".
		load = func(ctx context.Context) (discoveryResult, error) {
			return listing(awsx.LogGroups(ctx, conn.Clients.LogsGlobal, prefix))
		}
	case "rdsproxies":
		load = func(ctx context.Context) (discoveryResult, error) {
			return listing(awsx.RDSProxies(ctx, conn.Clients.RDS))
		}
	case "clusters":
		load = func(ctx context.Context) (discoveryResult, error) {
			return listing(awsx.Clusters(ctx, conn.Clients.EKS))
		}
	case "webacls":
		load = func(ctx context.Context) (discoveryResult, error) {
			return s.webACLs(ctx, conn)
		}
	default:
		return discoveryResult{}, errUnknownKind
	}

	// The key names the regions the clients are pointed at rather than the one
	// the config records, which is a note about the credentials and does not
	// decide where a call lands. A listing from us-east-1 and one from the
	// working region must not be able to answer for each other.
	key := "discovery|" + kind + "|" + prefix + "|" + s.region() + "|" + s.wafRegion()
	return s.cachedDiscovery(ctx, key, kind, prefix, load)
}

// webACLs lists both scopes: a regional ACL fronting an ALB and a CLOUDFRONT
// one are equally likely, and the CLOUDFRONT list only exists in us-east-1.
func (s *Service) webACLs(ctx context.Context, conn *AWSConn) (discoveryResult, error) {
	regional, err := awsx.WebACLs(ctx, conn.Clients.WAF, waftypes.ScopeRegional)
	if err != nil {
		return discoveryResult{}, err
	}
	out := discoveryResult{Listing: regional}

	global, err := awsx.WebACLs(ctx, conn.Clients.WAFGlobal, waftypes.ScopeCloudfront)
	if err != nil {
		// A missing CLOUDFRONT permission must not hide the regional ACLs the
		// operator can actually use — but it is reported rather than swallowed,
		// because this branch also fires when wafRegion equals the working
		// region (awsx.New aliases the global client to the regional one, and a
		// CLOUDFRONT-scoped call outside us-east-1 fails), which would
		// otherwise make a misconfigured wafRegion completely invisible.
		out.Partial = append(out.Partial, fmt.Sprintf("CLOUDFRONT 스코프 조회 실패: %v", err))
		return out, nil
	}
	out.Resources = append(out.Resources, global.Resources...)
	out.Truncated = out.Truncated || global.Truncated
	return out, nil
}
