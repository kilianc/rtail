/*!
 * The embedded webapp.
 *
 * dist/ is a build artifact and is gitignored, so only web/dist/.gitkeep is
 * committed — which is enough for the embed pattern to resolve and for
 * `go build ./...` to work on a fresh clone with no Node toolchain anywhere in
 * sight. When the assets are missing the server serves a short page explaining
 * how to build them, rather than a bare 404 that looks like a broken install.
 */

package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var embedded embed.FS

// notBuilt is served when the webapp assets are absent.
const notBuilt = `<!doctype html>
<meta charset="utf-8">
<title>rTail — webapp not built</title>
<style>
  body { font: 14px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace;
         max-width: 46rem; margin: 12vh auto; padding: 0 2rem;
         color: #d8d8d8; background: #16181d; }
  code { background: #22252c; padding: .15em .4em; border-radius: 3px; }
  a { color: #04cd7e; }
</style>
<h1>rTail</h1>
<p>The server is running, but the webapp assets are not in this binary.</p>
<p>Build them and rebuild the server:</p>
<pre><code>make dist
make server</code></pre>
<p>The API is up regardless — try <a href="/v1/streams">/v1/streams</a>.</p>
`

// Assets returns the built webapp, and whether it was actually built.
func Assets() (fs.FS, bool) {
	sub, err := fs.Sub(embedded, "dist")
	if nil != err {
		return nil, false
	}

	if _, err := fs.Stat(sub, "index.html"); nil != err {
		return nil, false
	}

	return sub, true
}

// Handler serves the webapp, or the explanatory page when it is missing.
func Handler() http.Handler {
	assets, built := Assets()
	if !built {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(notBuilt))
		})
	}

	return http.FileServer(http.FS(assets))
}
