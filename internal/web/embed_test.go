package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The API is mounted ahead of this handler. Refusing /api here as well means a
// mistake in the routing order shows up as a 404 rather than as an HTML page
// arriving where the frontend expected JSON.
func TestAPIPathsAreNeverAnsweredWithHTML(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/api/health", "/api/page/overview", "/api/anything"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("%s was answered with HTML", path)
		}
	}
}

// Client-side routes have to survive a hard refresh, so anything that is not a
// real file falls back to the app shell.
func TestClientRoutesFallBackToTheAppShell(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/", "/logs/pod", "/infra/kubernetes", "/settings"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type = %q", path, ct)
		}
	}
}

// A binary built before the frontend has to explain itself rather than serve a
// blank page or fail to build at all.
func TestAnUnbuiltFrontendExplainsItself(t *testing.T) {
	_, err := FS()
	if err == nil {
		// The frontend is built in this tree; nothing to check.
		t.Skip("frontend is built")
	}
	if err != ErrNotBuilt {
		t.Fatalf("unexpected error: %v", err)
	}

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"mise run build", "/api/health"} {
		if !strings.Contains(body, want) {
			t.Errorf("the explanation does not mention %q", want)
		}
	}
}

func TestHashedAssetsAreCacheableAndTheShellIsNot(t *testing.T) {
	if _, err := FS(); err != nil {
		t.Skip("frontend is not built")
	}
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("app shell Cache-Control = %q, want no-store so a stale shell cannot request missing assets", got)
	}
}
