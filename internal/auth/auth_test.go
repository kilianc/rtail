package auth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }

func guarded(token string, open ...string) http.Handler {
	return New(token, false).Wrap(http.HandlerFunc(ok), open...)
}

// The zero-configuration case: no token, no gate. This is the laptop.
func TestNoTokenLetsEverythingThrough(t *testing.T) {
	recorder := httptest.NewRecorder()
	guarded("").ServeHTTP(recorder, httptest.NewRequest("GET", "/v1/search", nil))

	if http.StatusTeapot != recorder.Code {
		t.Fatalf("code = %d, want %d", recorder.Code, http.StatusTeapot)
	}
}

func TestRejectsWithoutAToken(t *testing.T) {
	recorder := httptest.NewRecorder()
	guarded("s3cret").ServeHTTP(recorder, httptest.NewRequest("GET", "/v1/search", nil))

	if http.StatusUnauthorized != recorder.Code {
		t.Fatalf("code = %d, want 401", recorder.Code)
	}

	// A client needs to be told how to authenticate, not just that it failed.
	if want := `Bearer realm="rtail"`; want != recorder.Header().Get("WWW-Authenticate") {
		t.Errorf("WWW-Authenticate = %q, want %q", recorder.Header().Get("WWW-Authenticate"), want)
	}
}

func TestAcceptsEveryCarrier(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*http.Request)
	}{
		{"bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }},
		{"header", func(r *http.Request) { r.Header.Set("X-Rtail-Token", "s3cret") }},
		{"cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: "s3cret"}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/v1/search", nil)
			test.prepare(request)

			recorder := httptest.NewRecorder()
			guarded("s3cret").ServeHTTP(recorder, request)

			if http.StatusTeapot != recorder.Code {
				t.Fatalf("code = %d, want %d", recorder.Code, http.StatusTeapot)
			}
		})
	}
}

func TestRejectsAWrongToken(t *testing.T) {
	request := httptest.NewRequest("GET", "/v1/search", nil)
	request.Header.Set("Authorization", "Bearer wrong")

	recorder := httptest.NewRecorder()
	guarded("s3cret").ServeHTTP(recorder, request)

	if http.StatusUnauthorized != recorder.Code {
		t.Fatalf("code = %d, want 401", recorder.Code)
	}
}

// A prefix of the real token must not pass. ConstantTimeCompare returns 0 on
// a length mismatch, and this is the test that keeps someone from "optimising"
// it into a strings.HasPrefix.
func TestRejectsAPrefixOfTheToken(t *testing.T) {
	request := httptest.NewRequest("GET", "/v1/search", nil)
	request.Header.Set("Authorization", "Bearer s3c")

	recorder := httptest.NewRecorder()
	guarded("s3cret").ServeHTTP(recorder, request)

	if http.StatusUnauthorized != recorder.Code {
		t.Fatalf("code = %d, want 401", recorder.Code)
	}
}

// /healthz has to answer a container healthcheck that was never given the
// secret, so it is exempt — and nothing else is.
func TestOpenPathsSkipTheGuard(t *testing.T) {
	recorder := httptest.NewRecorder()
	guarded("s3cret", "/healthz").ServeHTTP(recorder, httptest.NewRequest("GET", "/healthz", nil))

	if http.StatusTeapot != recorder.Code {
		t.Fatalf("code = %d, want %d", recorder.Code, http.StatusTeapot)
	}

	recorder = httptest.NewRecorder()
	guarded("s3cret", "/healthz").ServeHTTP(recorder, httptest.NewRequest("GET", "/healthzz", nil))

	if http.StatusUnauthorized != recorder.Code {
		t.Fatalf("a path merely starting with an open one must not be exempt: code = %d", recorder.Code)
	}
}

/*!
 * The shared-link path: ?token=… signs the browser in, then redirects to the
 * same URL without it.
 *
 * The redirect is the point of the test. If the token survived in the address
 * bar it would end up in history, bookmarks and any Referer the page sends.
 */
func TestQueryTokenSetsACookieAndIsStripped(t *testing.T) {
	recorder := httptest.NewRecorder()
	guarded("s3cret").ServeHTTP(recorder, httptest.NewRequest("GET", "/?token=s3cret&stream=api", nil))

	if http.StatusSeeOther != recorder.Code {
		t.Fatalf("code = %d, want 303", recorder.Code)
	}

	location := recorder.Header().Get("Location")
	if strings.Contains(location, "token") {
		t.Errorf("the token survived into %q", location)
	}
	if !strings.Contains(location, "stream=api") {
		t.Errorf("the rest of the query was lost: %q", location)
	}

	cookies := recorder.Result().Cookies()
	if 0 == len(cookies) || CookieName != cookies[0].Name || "s3cret" != cookies[0].Value {
		t.Fatalf("cookies = %v, want one %s", cookies, CookieName)
	}
	if !cookies[0].HttpOnly {
		t.Error("the cookie must be HttpOnly so a log line cannot read it")
	}
}

func TestWrongQueryTokenIsNotARedirect(t *testing.T) {
	recorder := httptest.NewRecorder()
	guarded("s3cret").ServeHTTP(recorder, httptest.NewRequest("GET", "/?token=wrong", nil))

	if http.StatusUnauthorized != recorder.Code {
		t.Fatalf("code = %d, want 401", recorder.Code)
	}
}

// A browser gets something it can act on; a program gets JSON.
func TestDeniesInTheRequestersOwnTerms(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept", "text/html,application/xhtml+xml")

	recorder := httptest.NewRecorder()
	guarded("s3cret").ServeHTTP(recorder, request)

	if !strings.Contains(recorder.Body.String(), "<form") {
		t.Error("a browser should be offered a sign-in form")
	}

	recorder = httptest.NewRecorder()
	guarded("s3cret").ServeHTTP(recorder, httptest.NewRequest("GET", "/v1/search", nil))

	if want := "application/json"; want != recorder.Header().Get("Content-Type") {
		t.Errorf("Content-Type = %q, want %q", recorder.Header().Get("Content-Type"), want)
	}
}

func TestIsLoopback(t *testing.T) {
	for _, test := range []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"localhost", true},
		{"0.0.0.0", false},
		{"::", false},
		{"192.168.1.10", false},
		// Empty means every interface, which is the commonest accident.
		{"", false},
		{"no-such-host.invalid", false},
	} {
		if got := IsLoopback(test.host); got != test.want {
			t.Errorf("IsLoopback(%q) = %v, want %v", test.host, got, test.want)
		}
	}
}

/*!
 * A query token on anything but a page load authenticates in place.
 *
 * A 303 makes the client re-issue the request as a GET without its body, so
 * redirecting `POST /v1/ingest?token=…` silently threw the lines away.
 */
func TestQueryTokenOnAPostIsNotARedirect(t *testing.T) {
	var body string
	handler := New("s3cret", false).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(strings.Builder)
		io.Copy(buf, r.Body)
		body = buf.String()
		w.WriteHeader(http.StatusAccepted)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/v1/ingest?token=s3cret", strings.NewReader("a line\n")))

	if http.StatusAccepted != recorder.Code {
		t.Fatalf("code = %d, want the handler's 202", recorder.Code)
	}
	if "a line\n" != body {
		t.Errorf("body = %q, want the request's own", body)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/v1/ingest?token=wrong", strings.NewReader("x")))

	if http.StatusUnauthorized != recorder.Code {
		t.Errorf("a wrong query token on a POST: code = %d, want 401", recorder.Code)
	}
}
