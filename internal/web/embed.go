// Package web serves the built SvelteKit application out of the binary.
package web

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the frontend build. The SvelteKit build writes straight into this
// directory (see web/vite.config.ts), so there is no copy step to forget.
//
// The `all:` prefix makes the embed include .gitkeep, which is what keeps this
// compiling on a fresh clone where the frontend has not been built yet. A
// binary in that state serves the explanatory page below rather than failing to
// build.
//
//go:embed all:dist
var dist embed.FS

// ErrNotBuilt reports that the binary carries no frontend.
var ErrNotBuilt = errors.New("frontend is not built")

// FS returns the built site rooted at dist.
func FS() (fs.FS, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return sub, ErrNotBuilt
	}
	return sub, nil
}

const notBuiltPage = `<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><title>빌드되지 않음</title>
<style>
body{font:16px/1.6 ui-sans-serif,system-ui,sans-serif;margin:0;display:grid;place-items:center;min-height:100vh;background:#f5f5f7;color:#1d1d1f}
main{max-width:38rem;padding:2rem}
code{font:14px ui-monospace,SFMono-Regular,Menlo,monospace;background:#e8e8ed;padding:.15em .4em;border-radius:.3em}
pre{background:#e8e8ed;padding:1rem;border-radius:.6em;overflow-x:auto}
@media(prefers-color-scheme:dark){body{background:#1d1d1f;color:#f5f5f7}code,pre{background:#2c2c2e}}
</style></head>
<body><main>
<h1>프론트엔드가 빌드되지 않았습니다</h1>
<p>이 바이너리에는 웹 UI가 포함되어 있지 않습니다. 아래를 실행한 뒤 다시 빌드하세요.</p>
<pre>mise run web:install
mise run build</pre>
<p>API는 정상 동작합니다. <code>/api/health</code> 로 확인할 수 있습니다.</p>
</main></body></html>`

// Handler serves the SPA.
//
// Anything that is not a real file falls back to index.html so client-side
// routes such as /logs/pod resolve on a hard refresh. The API is mounted ahead
// of this handler, and /api is refused here as well so a mistake in the routing
// order surfaces as a 404 rather than as an HTML page where JSON was expected.
func Handler() http.Handler {
	site, err := FS()
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(notBuiltPage))
		})
	}

	files := http.FileServer(http.FS(site))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." {
			serveIndex(w, r, site)
			return
		}
		if f, err := site.Open(name); err == nil {
			info, statErr := f.Stat()
			_ = f.Close()
			if statErr == nil && !info.IsDir() {
				// Hashed build assets are immutable; index.html is not, or a
				// stale shell would keep loading assets that no longer exist.
				if strings.HasPrefix(name, "_app/immutable/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		serveIndex(w, r, site)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, site fs.FS) {
	b, err := fs.ReadFile(site, "index.html")
	if err != nil {
		http.Error(w, "index.html is missing from the embedded build", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(b)
}
