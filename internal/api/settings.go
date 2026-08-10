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
	Kind      string            `json:"kind"`
	Resources []awsx.Resource   `json:"resources"`
	Scaling   *awsx.NodeScaling `json:"scaling,omitempty"`
}

func (s *Service) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	if !s.requireAWS(w) {
		return
	}
	kind := r.PathValue("kind")

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resources, err := s.discover(ctx, kind, r.URL.Query().Get("prefix"))
	if err != nil {
		if err == errUnknownKind {
			badRequest(w, fmt.Errorf("unknown discovery kind %q", kind))
			return
		}
		upstream(w, err)
		return
	}
	writeJSON(w, http.StatusOK, discoveryResponse{Kind: kind, Resources: resources})
}

var errUnknownKind = fmt.Errorf("unknown discovery kind")

func (s *Service) discover(ctx context.Context, kind, prefix string) ([]awsx.Resource, error) {
	// The key names the regions the clients are pointed at rather than the one
	// the config records, which is a note about the credentials and does not
	// decide where a call lands. A listing from us-east-1 and one from the
	// working region must not be able to answer for each other.
	key := "discovery|" + kind + "|" + prefix + "|" + s.region() + "|" + s.wafRegion()
	switch kind {
	case "targetgroups":
		return awsx.Cached(ctx, s.Cache, key, func(ctx context.Context) ([]awsx.Resource, error) {
			return awsx.TargetGroups(ctx, s.Clients.ELB)
		})
	case "loadbalancers":
		return awsx.Cached(ctx, s.Cache, key, func(ctx context.Context) ([]awsx.Resource, error) {
			return awsx.LoadBalancers(ctx, s.Clients.ELB)
		})
	case "loggroups":
		return awsx.Cached(ctx, s.Cache, key, func(ctx context.Context) ([]awsx.Resource, error) {
			return awsx.LogGroups(ctx, s.Clients.Logs, prefix)
		})
	case "waf-loggroups":
		// WAF log groups are listed from the WAF region. A CLOUDFRONT-scoped
		// web ACL writes only into us-east-1, so listing the working region
		// returns nothing and reads as "this account has no WAF logging".
		return awsx.Cached(ctx, s.Cache, key, func(ctx context.Context) ([]awsx.Resource, error) {
			return awsx.LogGroups(ctx, s.Clients.LogsGlobal, prefix)
		})
	case "rdsproxies":
		return awsx.Cached(ctx, s.Cache, key, func(ctx context.Context) ([]awsx.Resource, error) {
			return awsx.RDSProxies(ctx, s.Clients.RDS)
		})
	case "clusters":
		return awsx.Cached(ctx, s.Cache, key, func(ctx context.Context) ([]awsx.Resource, error) {
			return awsx.Clusters(ctx, s.Clients.EKS)
		})
	case "webacls":
		// Both scopes are listed: a regional ACL fronting an ALB and a
		// CLOUDFRONT one are equally likely, and the CLOUDFRONT list only
		// exists in us-east-1.
		return awsx.Cached(ctx, s.Cache, key, func(ctx context.Context) ([]awsx.Resource, error) {
			regional, err := awsx.WebACLs(ctx, s.Clients.WAF, waftypes.ScopeRegional)
			if err != nil {
				return nil, err
			}
			global, err := awsx.WebACLs(ctx, s.Clients.WAFGlobal, waftypes.ScopeCloudfront)
			if err != nil {
				// A missing CLOUDFRONT permission should not hide the
				// regional ACLs the operator can actually use.
				return regional, nil
			}
			return append(regional, global...), nil
		})
	default:
		return nil, errUnknownKind
	}
}
