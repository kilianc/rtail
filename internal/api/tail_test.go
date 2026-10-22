package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/api"
	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/model"
)

// The live tail answers the envelope fields search does. It used to know
// nothing about ts, so `ts>2020-01-01` streamed nothing at all.
func TestTailFiltersOnTheTimestamp(t *testing.T) {
	h := newHarness(t)

	response, err := http.Get(h.server.URL + "/v1/tail?stream=api&q=" + neturl.QueryEscape("ts>2020-01-01 is_json=true"))
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })

	feed := &sseStream{body: response.Body, scanner: bufio.NewScanner(response.Body)}
	feed.nextOf(t, "backlog")

	h.send(t, "api", "a plain line, so not json")
	h.send(t, "api", map[string]any{"msg": "kept"})

	var line struct {
		Msg string `json:"msg"`
	}
	json.Unmarshal(feed.nextOf(t, "line").Data, &line)

	if "kept" != line.Msg {
		t.Errorf("msg = %q, want the JSON line", line.Msg)
	}
}

/*!
 * tailServer is a memory store behind the API, with a way to know when a
 * tail handler has returned — which is what the stall tests are about.
 */
type tailServer struct {
	store    *logstore.Memory
	api      *api.Server
	url      string
	addr     string
	finished chan struct{}
}

func newTailServer(t *testing.T, stall time.Duration) *tailServer {
	t.Helper()

	store := logstore.NewMemory(100)
	server := api.New(api.Options{
		Store:        store,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Heartbeat:    time.Hour,
		StallTimeout: stall,
	})

	finished := make(chan struct{}, 1)
	handler := server.Handler()

	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		if "/v1/tail" == r.URL.Path {
			finished <- struct{}{}
		}
	}))

	t.Cleanup(func() {
		listener.CloseClientConnections()
		listener.Close()
		store.Close()
	})

	return &tailServer{store: store, api: server, url: listener.URL, addr: listener.Listener.Addr().String(), finished: finished}
}

/*!
 * open starts a tail on a raw connection and reads up to the backlog, so the
 * subscription exists, then hands back the reader it has not drained.
 */
func (s *tailServer) open(t *testing.T) (net.Conn, *bufio.Scanner) {
	t.Helper()

	conn, err := net.Dial("tcp", s.addr)
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	request, _ := http.NewRequest("GET", s.url+"/v1/tail?stream=api", nil)
	if err := request.Write(conn); nil != err {
		t.Fatal(err)
	}

	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if nil != err {
		t.Fatal(err)
	}

	feed := &sseStream{body: response.Body, scanner: bufio.NewScanner(response.Body)}
	feed.scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	feed.nextOf(t, "backlog")

	return conn, feed.scanner
}

// flood appends far more than the socket buffers and the subscriber's
// channel can hold together, sharing one large payload so it costs nothing.
func (s *tailServer) flood(t *testing.T) {
	t.Helper()

	payload := strings.Repeat("x", 16<<10)
	now := time.Now().UTC()

	for range 4000 {
		rec := &model.Record{Stream: "api", Ts: now, IngestTs: now, Raw: payload, Msg: payload, Type: "string"}
		if err := s.store.Append(context.Background(), rec); nil != err {
			t.Fatal(err)
		}
	}
}

/*!
 * A client that stops reading is let go of.
 *
 * Without a write deadline its full socket blocked the handler forever,
 * holding the goroutine and the subscription and making every graceful
 * shutdown wait out its grace period.
 */
func TestTailGivesUpOnAClientThatStopsReading(t *testing.T) {
	s := newTailServer(t, 200*time.Millisecond)

	s.open(t)
	s.flood(t)

	select {
	case <-s.finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the tail handler is still blocked writing to a client that stopped reading")
	}
}

/*!
 * Lines the store dropped for a slow client are reported to it.
 *
 * The store counts them so loss can be visible; the count used to go nowhere,
 * and a tail that had skipped lines looked just like one that had not.
 */
func TestTailReportsDroppedLines(t *testing.T) {
	s := newTailServer(t, 30*time.Second)

	conn, scanner := s.open(t)
	s.flood(t)

	// A missing event fails the test rather than hanging it.
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	feed := &sseStream{scanner: scanner}
	for range 10000 {
		event, err := feed.read()
		if nil != err {
			t.Fatalf("stream ended before a dropped event: %v", err)
		}
		if "dropped" != event.Name {
			continue
		}

		var report struct {
			Dropped uint64 `json:"dropped"`
			Total   uint64 `json:"total"`
		}
		if err := json.Unmarshal(event.Data, &report); nil != err {
			t.Fatal(err)
		}
		if 0 == report.Dropped || report.Total < report.Dropped {
			t.Errorf("dropped event = %s", event.Data)
		}
		return
	}

	t.Fatal("no dropped event arrived")
}

// Shutting the server down ends open tails rather than waiting on them.
func TestDrainEndsOpenTails(t *testing.T) {
	s := newTailServer(t, time.Second)

	_, scanner := s.open(t)

	s.api.Drain()

	select {
	case <-s.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("a tail outlived Drain")
	}

	feed := &sseStream{scanner: scanner}
	if _, err := feed.read(); io.EOF != err {
		t.Errorf("read after drain = %v, want the stream to end", err)
	}
}
