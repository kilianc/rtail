package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/api"
	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/normalize"
	"github.com/kilianc/rtail/v2/internal/query"
)

// searchServer is the API over a durable store, so search has an engine.
type searchServer struct {
	dir    string
	store  *logstore.Durable
	server *httptest.Server
}

func newSearchServer(t *testing.T) *searchServer {
	t.Helper()

	ctx := context.Background()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	store, err := logstore.OpenDurable(ctx, dir, logstore.DurableOptions{
		Backlog:       100,
		KeepRaw:       true,
		FlushInterval: time.Hour,
		SyncInterval:  -1,
		Grace:         time.Hour,
		Log:           log,
	})
	if nil != err {
		t.Fatal(err)
	}

	engine, err := query.Open(store.Catalog(), store.Backend(), query.Limits{})
	if nil != err {
		store.Close()
		t.Fatal(err)
	}

	server := httptest.NewServer(api.New(api.Options{
		Store:   store,
		Log:     log,
		Engine:  engine,
		Catalog: store.Catalog(),
	}).Handler())

	t.Cleanup(func() {
		server.Close()
		engine.Close()
		store.Close()
	})

	return &searchServer{dir: dir, store: store, server: server}
}

func (s *searchServer) ingest(t *testing.T, lines ...string) {
	t.Helper()

	ctx := context.Background()
	for _, line := range lines {
		if err := s.store.Append(ctx, normalize.FromLine("s", "10.0.0.1", 5000, []byte(line))); nil != err {
			t.Fatal(err)
		}
	}
	if err := s.store.Flush(ctx); nil != err {
		t.Fatal(err)
	}
}

// answer is a response's status and body, with the body also decoded.
type answer struct {
	status int
	body   string
	json   map[string]any
}

func (s *searchServer) get(t *testing.T, path string, query neturl.Values) answer {
	t.Helper()

	response, err := http.Get(s.server.URL + path + "?" + query.Encode())
	if nil != err {
		t.Fatal(err)
	}
	return read(t, response)
}

func (s *searchServer) sql(t *testing.T, statement string) answer {
	t.Helper()

	body, _ := json.Marshal(map[string]string{"sql": statement})
	response, err := http.Post(s.server.URL+"/v1/sql", "application/json", strings.NewReader(string(body)))
	if nil != err {
		t.Fatal(err)
	}
	return read(t, response)
}

func read(t *testing.T, response *http.Response) answer {
	t.Helper()
	defer response.Body.Close()

	raw, _ := io.ReadAll(response.Body)
	out := answer{status: response.StatusCode, body: string(raw)}
	json.Unmarshal(raw, &out.json)
	return out
}

// leaks reports anything in a response that only the operator should see.
func (s *searchServer) leaks(a answer) string {
	for _, secret := range []string{s.dir, "read_parquet", "query:\n", "LINE "} {
		if strings.Contains(a.body, secret) {
			return secret
		}
	}
	return ""
}

/*!
 * The caller's SQL being wrong is a 400 with DuckDB's message.
 *
 * On a fresh store there is no a_service column in any file, so the classic
 * first query is a binder error — the user's to fix, and it used to come back
 * as a 500 carrying our whole generated statement.
 */
func TestUserSQLMistakesAreBadRequests(t *testing.T) {
	s := newSearchServer(t)

	for name, a := range map[string]answer{
		"/v1/sql":         s.sql(t, `SELECT a_service, count(*) FROM logs GROUP BY 1`),
		"lang=sql search": s.get(t, "/v1/search", neturl.Values{"lang": {"sql"}, "q": {"a_service = 'api'"}}),
		"mutation":        s.sql(t, `DROP TABLE logs`),
		"syntax":          s.sql(t, `SELEC 1`),
	} {
		if http.StatusBadRequest != a.status {
			t.Errorf("%s: status = %d, want 400 (%s)", name, a.status, a.body)
		}
		if message, _ := a.json["message"].(string); "" == message {
			t.Errorf("%s: no message for the UI to show: %s", name, a.body)
		}
		if leak := s.leaks(a); "" != leak {
			t.Errorf("%s: response carries %q: %s", name, leak, a.body)
		}
	}

	if a := s.sql(t, `SELECT a_service FROM logs`); !strings.Contains(a.body, "a_service") {
		t.Errorf("the message should name the column: %s", a.body)
	}
}

// A bad regular expression is the query's fault, reported like any other
// parse error rather than as DuckDB failing mid-scan.
func TestABadRegexIsABadRequest(t *testing.T) {
	s := newSearchServer(t)
	s.ingest(t, `{"msg":"a"}`)

	a := s.get(t, "/v1/search", neturl.Values{"q": {`msg:~"("`}})

	if http.StatusBadRequest != a.status {
		t.Fatalf("status = %d, want 400: %s", a.status, a.body)
	}
	if message, _ := a.json["message"].(string); !strings.Contains(message, "regular expression") {
		t.Errorf("message = %q", message)
	}
	if leak := s.leaks(a); "" != leak {
		t.Errorf("response carries %q: %s", leak, a.body)
	}
}

// A genuine failure is a 500 that says so and nothing more; the statement and
// the file paths stay in the server's log.
func TestServerFailuresDoNotEchoTheStatement(t *testing.T) {
	s := newSearchServer(t)
	s.ingest(t, `{"msg":"a"}`)

	// The catalog still lists the file, so DuckDB fails to open it.
	filepath.Walk(s.dir, func(path string, _ os.FileInfo, err error) error {
		if nil == err && strings.HasSuffix(path, ".parquet") {
			os.Remove(path)
		}
		return err
	})

	a := s.get(t, "/v1/search", neturl.Values{})

	if http.StatusInternalServerError != a.status {
		t.Fatalf("status = %d, want 500: %s", a.status, a.body)
	}
	if leak := s.leaks(a); "" != leak {
		t.Errorf("response carries %q: %s", leak, a.body)
	}
}
