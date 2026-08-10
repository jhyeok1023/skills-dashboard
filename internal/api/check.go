package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// errNoCheckURL is a missing setting, not a failed probe, so it is the one
// outcome here that is an HTTP error.
var errNoCheckURL = errors.New("점검할 주소가 설정되지 않았습니다")

// checkState is the reusable client and its one-time construction, embedded in
// Service.
type checkState struct {
	checkOnce sync.Once
	check     *http.Client
}

// The traffic check: the one thing here that reaches something other than AWS.
//
// Every other number on this dashboard comes from CloudWatch, which lags by
// minutes and, when a panel is empty, cannot say whether nothing happened or
// nothing was published. "Is the service answering right now" is not a question
// that data can answer, so this asks the service.
//
// It is deliberately small. One GET, one status code, one elapsed time, run
// only when someone asks for it. Nothing is stored: a check that kept history
// would be a monitoring system, and this is a button.

// checkTimeout bounds one probe. Long enough to distinguish "slow" from "gone",
// short enough that the button comes back while the operator is still looking.
const checkTimeout = 10 * time.Second

// checkResult is what one probe found. A probe that failed is still a completed
// probe, so this rides a 200 with Ok=false rather than an HTTP error: the
// endpoint failing and the target failing are different facts, and collapsing
// them would make a dashboard bug look like an outage.
type checkResult struct {
	URL       string `json:"url"`
	OK        bool   `json:"ok"`
	Status    int    `json:"status,omitempty"`
	ElapsedMs int64  `json:"elapsedMs"`
	At        string `json:"at"`
	Error     string `json:"error,omitempty"`
	// Expect states what was treated as healthy, so a red result cannot be
	// read without also reading what it was compared against.
	Expect string `json:"expect"`
}

// checkClient is built once and reused, like the AWS clients are. A fresh
// http.Client per call leaks a connection pool per call.
func (s *Service) checkClient() *http.Client {
	s.checkOnce.Do(func() {
		s.check = &http.Client{Timeout: checkTimeout}
	})
	return s.check
}

func (s *Service) handleCheck(w http.ResponseWriter, r *http.Request) {
	cfg := s.Store.Get().Check
	if cfg.URL == "" {
		badRequest(w, errNoCheckURL)
		return
	}

	expect := "2xx"
	if cfg.ExpectStatus > 0 {
		expect = strconv.Itoa(cfg.ExpectStatus)
		if text := http.StatusText(cfg.ExpectStatus); text != "" {
			expect += " " + text
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	defer cancel()

	res := checkResult{URL: cfg.URL, Expect: expect}
	started := s.now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		res.Error = err.Error()
		res.At = started.UTC().Format(time.RFC3339)
		writeJSON(w, http.StatusOK, res)
		return
	}
	// Named on purpose: whoever reads the target's access log should be able to
	// tell this request from the real traffic it is standing in for.
	req.Header.Set("User-Agent", "skills-dashboard/traffic-check")

	start := time.Now()
	resp, err := s.checkClient().Do(req)
	res.ElapsedMs = time.Since(start).Milliseconds()
	res.At = started.UTC().Format(time.RFC3339)

	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	// Drained and closed so the connection returns to the pool rather than
	// being torn down and redialled on the next press. The body is not read
	// into anything: this reports whether the service answered, and a response
	// body on screen is a response body in a screenshot.
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	res.Status = resp.StatusCode
	res.OK = cfg.OK(resp.StatusCode)
	writeJSON(w, http.StatusOK, res)
}
