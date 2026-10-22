/*!
 * Authentication: one shared token, or nothing.
 *
 * A single-node tool that anyone can run on their own machine does not want
 * accounts, password resets, or an identity provider. It wants one secret that
 * an operator can put in an environment variable and a reverse proxy can
 * forward. So that is what this is: a bearer token, compared in constant time,
 * accepted from a header for programs and from a cookie for browsers.
 *
 * What it is deliberately not: OIDC, SAML, users, roles, per-stream access.
 * Those belong to a multi-tenant product, and rtail is not one. Anyone who
 * needs them has a reverse proxy that already does them better.
 *
 * The important design decision is the default. Running with no token is the
 * right thing on a laptop and a catastrophe on a public address, so the two
 * cases are separated by where the server binds rather than left to the
 * operator to notice: see RequireTokenOffLoopback. Nothing here can stop a
 * misconfiguration, but it can refuse to be the quiet kind.
 *
 * Two holes worth stating plainly rather than burying. UDP ingest is
 * unauthenticated, because UDP has no handshake to carry a token and v1's wire
 * format is frozen; syslog is unauthenticated for the same reason. Both listen
 * on 127.0.0.1 by default. If you move either off loopback you are trusting
 * your network, and no flag here changes that.
 */

package auth

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// CookieName holds the token for browser sessions.
const CookieName = "rtail_token"

// Guard checks requests against a shared token.
type Guard struct {
	token string
	// secure marks the cookie, set when the server is behind TLS.
	secure bool
}

// New builds a Guard. An empty token disables it entirely.
func New(token string, secure bool) *Guard {
	return &Guard{token: strings.TrimSpace(token), secure: secure}
}

// Enabled reports whether a token is configured.
func (g *Guard) Enabled() bool { return "" != g.token }

/*!
 * Allows reports whether a request carries the token.
 *
 * Three places are accepted, in the order a client is likely to use them:
 *
 *   Authorization: Bearer <token>   programs, and what the docs show
 *   X-Rtail-Token: <token>          for clients that reserve Authorization
 *   Cookie                          browsers, after signing in once
 *
 * The comparison is constant time in every branch. A token check that returns
 * early on the first wrong byte leaks the token's prefix to anyone willing to
 * make a few million requests, and this one is cheap to get right.
 */
func (g *Guard) Allows(r *http.Request) bool {
	if !g.Enabled() {
		return true
	}

	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		if g.matches(strings.TrimPrefix(header, "Bearer ")) {
			return true
		}
	}

	if header := r.Header.Get("X-Rtail-Token"); "" != header {
		if g.matches(header) {
			return true
		}
	}

	if cookie, err := r.Cookie(CookieName); nil == err {
		if g.matches(cookie.Value) {
			return true
		}
	}

	return false
}

func (g *Guard) matches(candidate string) bool {
	return 1 == subtle.ConstantTimeCompare([]byte(candidate), []byte(g.token))
}

/*!
 * Wrap guards a handler, leaving `open` paths alone.
 *
 * /healthz is open so that a container healthcheck, a load balancer and
 * `docker compose` all keep working without being handed the secret. It
 * reports liveness and stream names, which is the least interesting thing the
 * server knows; everything that could show a log line is behind the guard.
 */
func (g *Guard) Wrap(next http.Handler, open ...string) http.Handler {
	if !g.Enabled() {
		return next
	}

	exempt := make(map[string]bool, len(open))
	for _, path := range open {
		exempt[path] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		/*!
		 * A token in the query string.
		 *
		 * It is the shared token itself, not a one-time code: nothing is
		 * spent, and a link carrying it keeps working for as long as the
		 * token does. Putting a secret in a URL is bad practice — it lands in
		 * proxy logs, browser history and anything that records a Referer —
		 * but "open this link and you are in" is how people actually share a
		 * view of their logs, and the alternative is that they paste the
		 * token into chat instead, which is worse. The compromise, for a
		 * browser opening a page: honour it, set the cookie, then redirect to
		 * the same URL without it so it does not linger in the address bar or
		 * get bookmarked.
		 *
		 * Only for GET and HEAD. A 303 turns any other request into a GET of
		 * the new address and drops its body, so `POST /v1/ingest?token=…`
		 * lost every line it carried; those are authenticated where they
		 * stand instead.
		 */
		if token := r.URL.Query().Get("token"); "" != token && g.matches(token) {
			if http.MethodGet != r.Method && http.MethodHead != r.Method {
				next.ServeHTTP(w, r)
				return
			}

			g.SetCookie(w)

			stripped := *r.URL
			query := stripped.Query()
			query.Del("token")
			stripped.RawQuery = query.Encode()

			http.Redirect(w, r, stripped.RequestURI(), http.StatusSeeOther)
			return
		}

		if !g.Allows(r) {
			g.deny(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// SetCookie remembers a successful sign-in for this browser.
func (g *Guard) SetCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:  CookieName,
		Value: g.token,
		Path:  "/",
		// HttpOnly: the webapp never needs to read this, and not exposing it
		// to script means an XSS in a log line cannot exfiltrate it.
		HttpOnly: true,
		Secure:   g.secure,
		SameSite: http.SameSiteLaxMode,
		// A session cookie. The token is the operator's to rotate, and a
		// year-long cookie on a shared machine is a trap.
		MaxAge: 0,
	})
}

/*!
 * deny answers in the requester's own terms.
 *
 * A browser gets a sign-in page, because a bare 401 in a tab is a dead end
 * that tells someone nothing about what to do next. Everything else gets JSON
 * with a WWW-Authenticate header, which is what a client can act on.
 */
func (g *Guard) deny(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="rtail"`)

	if !wantsHTML(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		//nolint:errcheck // nothing useful to do if the client is already gone
		w.Write([]byte(`{"error":"a token is required: send Authorization: Bearer <token>"}` + "\n"))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	//nolint:errcheck // same
	w.Write([]byte(signInPage))
}

func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

/*!
 * IsLoopback reports whether a listen address is reachable only from this
 * machine.
 *
 * An empty host means every interface, which is the single most common way to
 * expose something by accident — `--web-host ''` or a Docker default — so it
 * is explicitly not loopback. A name that is not an IP is resolved, and every
 * address it resolves to must be loopback: "localhost" normally is, but it is
 * a name under the operator's control and can be pointed anywhere.
 */
func IsLoopback(host string) bool {
	if "" == host {
		return false
	}

	if ip := net.ParseIP(host); nil != ip {
		return ip.IsLoopback()
	}

	addrs, err := net.LookupIP(host)
	if nil != err || 0 == len(addrs) {
		return false
	}

	for _, addr := range addrs {
		if !addr.IsLoopback() {
			return false
		}
	}

	return true
}

// The sign-in page, inline rather than in the webapp bundle: it has to render
// for someone who is not allowed to download the bundle yet.
const signInPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>rtail — sign in</title>
<style>
  :root { color-scheme: dark }
  body {
    align-items: center;
    background: #1f1f1f;
    color: #e8eaed;
    display: flex;
    font: 11px/19px 'Roboto Mono', ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    justify-content: center;
    margin: 0;
    min-height: 100vh;
  }
  form { display: flex; flex-direction: column; gap: 12px; width: 280px }
  h1 { font-size: 12.5px; font-weight: 700; letter-spacing: .04em; margin: 0; color: #8ab4f8 }
  p { color: #9aa0a6; margin: 0 }
  label { color: #9aa0a6 }
  input {
    background: #202124;
    border: 1px solid #5f6368;
    border-radius: 4px;
    color: #e8eaed;
    font: inherit;
    padding: 5px 8px;
  }
  input:focus { border-color: #8ab4f8; outline: none }
  button {
    background: #8ab4f8;
    border: 0;
    border-radius: 4px;
    color: #202124;
    cursor: pointer;
    font: inherit;
    font-weight: 500;
    height: 22px;
  }
  button:hover { background: #aecbfa }
  code { color: #bdc1c6 }
</style>
</head>
<body>
<form method="GET" action="/">
  <h1>rtail</h1>
  <p>This server requires a token.</p>
  <label for="token">Token</label>
  <input id="token" name="token" type="password" autocomplete="current-password" autofocus>
  <button type="submit">Sign in</button>
  <p>Programs send it as <code>Authorization: Bearer &lt;token&gt;</code>.</p>
</form>
</body>
</html>
`
