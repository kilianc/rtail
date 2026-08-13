/*!
 * End-to-end tests for the P0 server: a v1 datagram goes in over UDP and comes
 * out of the SSE endpoint in the shape the existing webapp expects.
 */

package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/api"
	"github.com/kilianc/rtail/v2/internal/ingest"
	"github.com/kilianc/rtail/v2/internal/logstore"
)

type harness struct {
	server *httptest.Server
	store  *logstore.Memory
	udp    *net.UDPAddr
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := logstore.NewMemory(100)

	receiver, err := ingest.ListenUDP("127.0.0.1", 0, store, log)
	if nil != err {
		t.Fatalf("binding UDP: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go receiver.Serve(ctx)

	server := httptest.NewServer(api.New(api.Options{
		Store:     store,
		Log:       log,
		Version:   "test",
		UDP:       receiver.Stats(),
		Heartbeat: time.Hour,
	}).Handler())

	t.Cleanup(func() {
		server.Close()
		cancel()
		receiver.Close()
		store.Close()
	})

	return &harness{server: server, store: store, udp: receiver.Addr()}
}

// send emits a datagram in the frozen v1 format.
func (h *harness) send(t *testing.T, id string, content any) {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"id":        id,
		"timestamp": time.Now().UnixMilli(),
		"content":   content,
	})
	if nil != err {
		t.Fatal(err)
	}

	conn, err := net.DialUDP("udp4", nil, h.udp)
	if nil != err {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write(payload); nil != err {
		t.Fatal(err)
	}
}

// sendRaw emits arbitrary bytes, for the malformed-input cases.
func (h *harness) sendRaw(t *testing.T, payload []byte) {
	t.Helper()

	conn, err := net.DialUDP("udp4", nil, h.udp)
	if nil != err {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.Write(payload)
}

// waitFor polls until cond holds, because UDP delivery is asynchronous.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

/*!
 * An SSE reader.
 */
type sseEvent struct {
	Name string
	Data []byte
}

type sseStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

func (h *harness) tail(t *testing.T, stream string) *sseStream {
	t.Helper()

	url := h.server.URL + "/v1/tail"
	if "" != stream {
		url += "?stream=" + stream
	}

	response, err := http.Get(url)
	if nil != err {
		t.Fatal(err)
	}

	if http.StatusOK != response.StatusCode {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if ct := response.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	t.Cleanup(func() { response.Body.Close() })

	return &sseStream{body: response.Body, scanner: bufio.NewScanner(response.Body)}
}

// read returns the next event, ignoring heartbeat comments. It reports an
// error rather than failing the test, so it is safe to call from a goroutine
// that may still be blocked when the test ends.
func (s *sseStream) read() (sseEvent, error) {
	var event sseEvent

	for s.scanner.Scan() {
		line := s.scanner.Text()

		switch {
		case strings.HasPrefix(line, ":"):
			continue
		case strings.HasPrefix(line, "event: "):
			event.Name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			event.Data = []byte(strings.TrimPrefix(line, "data: "))
		case "" == line:
			if "" != event.Name {
				return event, nil
			}
		}
	}

	if err := s.scanner.Err(); nil != err {
		return event, err
	}

	return event, io.EOF
}

// next reads one event, failing the test if none arrives.
func (s *sseStream) next(t *testing.T) sseEvent {
	t.Helper()

	event, err := s.read()
	if nil != err {
		t.Fatalf("stream ended early: %v", err)
	}

	return event
}

func TestTailDeliversBacklogThenLiveLines(t *testing.T) {
	h := newHarness(t)

	h.send(t, "api", "first line")
	waitFor(t, "the first line", func() bool {
		backlog, _ := h.store.Backlog(context.Background(), "api", 0)
		return 1 == len(backlog)
	})

	stream := h.tail(t, "api")

	// 1. the stream list
	event := stream.next(t)
	if "streams" != event.Name {
		t.Fatalf("first event = %q, want streams", event.Name)
	}

	var streams []string
	if err := json.Unmarshal(event.Data, &streams); nil != err {
		t.Fatal(err)
	}
	if 1 != len(streams) || "api" != streams[0] {
		t.Fatalf("streams = %v, want [api]", streams)
	}

	// 2. the backlog, in v1's wire shape
	event = stream.next(t)
	if "backlog" != event.Name {
		t.Fatalf("second event = %q, want backlog", event.Name)
	}

	var backlog []struct {
		Timestamp int64           `json:"timestamp"`
		StreamID  string          `json:"streamid"`
		Content   json.RawMessage `json:"content"`
		Type      string          `json:"type"`
		Seq       uint64          `json:"seq"`
	}
	if err := json.Unmarshal(event.Data, &backlog); nil != err {
		t.Fatal(err)
	}
	if 1 != len(backlog) {
		t.Fatalf("backlog = %d records, want 1", len(backlog))
	}
	if "api" != backlog[0].StreamID || `"first line"` != string(backlog[0].Content) {
		t.Errorf("backlog[0] = %+v", backlog[0])
	}

	// 3. a live line
	h.send(t, "api", map[string]any{"level": "error", "msg": "boom", "user_id": 42})

	event = stream.next(t)
	if "line" != event.Name {
		t.Fatalf("third event = %q, want line", event.Name)
	}

	var line struct {
		Type   string            `json:"type"`
		Level  string            `json:"level"`
		Msg    string            `json:"msg"`
		Fields map[string]any    `json:"fields"`
		Seq    uint64            `json:"seq"`
		Extra  map[string]string `json:"-"`
	}
	if err := json.Unmarshal(event.Data, &line); nil != err {
		t.Fatal(err)
	}

	if "object" != line.Type {
		t.Errorf("type = %q, want object", line.Type)
	}
	if "ERROR" != line.Level {
		t.Errorf("level = %q, want ERROR", line.Level)
	}
	if "boom" != line.Msg {
		t.Errorf("msg = %q, want boom", line.Msg)
	}
	if float64(42) != line.Fields["user_id"] {
		t.Errorf("fields.user_id = %v, want 42", line.Fields["user_id"])
	}
	if line.Seq <= backlog[0].Seq {
		t.Errorf("seq = %d, want greater than the backlog's %d", line.Seq, backlog[0].Seq)
	}
}

// A record already delivered in the backlog must not arrive again as a line.
func TestBacklogLinesAreNotRepeated(t *testing.T) {
	h := newHarness(t)

	for i := range 3 {
		h.send(t, "api", fmt.Sprintf("line-%d", i))
	}
	waitFor(t, "three lines", func() bool {
		backlog, _ := h.store.Backlog(context.Background(), "api", 0)
		return 3 == len(backlog)
	})

	stream := h.tail(t, "api")

	if event := stream.next(t); "streams" != event.Name {
		t.Fatalf("first event = %q", event.Name)
	}

	event := stream.next(t)
	if "backlog" != event.Name {
		t.Fatalf("second event = %q", event.Name)
	}

	var backlog []json.RawMessage
	json.Unmarshal(event.Data, &backlog)
	if 3 != len(backlog) {
		t.Fatalf("backlog = %d, want 3", len(backlog))
	}

	// The next thing on the wire must be the new line, not a replay.
	h.send(t, "api", "line-3")

	event = stream.next(t)
	if "line" != event.Name {
		t.Fatalf("event = %q, want line", event.Name)
	}

	var line struct {
		Content string `json:"content"`
	}
	json.Unmarshal(event.Data, &line)
	if "line-3" != line.Content {
		t.Errorf("content = %q, want line-3 — an already-delivered line was repeated", line.Content)
	}
}

// No stream selected is the webapp's paused state: stream-list events only.
func TestTailWithoutStreamGetsStreamsOnly(t *testing.T) {
	h := newHarness(t)

	stream := h.tail(t, "")

	if event := stream.next(t); "streams" != event.Name {
		t.Fatalf("first event = %q, want streams", event.Name)
	}

	h.send(t, "api", "hello")

	// The new stream is announced ...
	event := stream.next(t)
	if "streams" != event.Name {
		t.Fatalf("event = %q, want streams", event.Name)
	}

	var streams []string
	json.Unmarshal(event.Data, &streams)
	if 1 != len(streams) || "api" != streams[0] {
		t.Errorf("streams = %v, want [api]", streams)
	}

	// ... and its lines are not. This reader may still be blocked when the
	// test ends, so it must not touch t.
	done := make(chan sseEvent, 1)
	go func() {
		if event, err := stream.read(); nil == err {
			done <- event
		}
	}()

	h.send(t, "api", "second")

	select {
	case event := <-done:
		t.Fatalf("unexpected %q event while paused", event.Name)
	case <-time.After(250 * time.Millisecond):
	}
}

func TestStreamsEndpoint(t *testing.T) {
	h := newHarness(t)

	response, err := http.Get(h.server.URL + "/v1/streams")
	if nil != err {
		t.Fatal(err)
	}
	defer response.Body.Close()

	var empty struct {
		Streams []string `json:"streams"`
	}
	json.NewDecoder(response.Body).Decode(&empty)

	// An empty list, never null — the webapp iterates it without a guard.
	if nil == empty.Streams {
		t.Error("streams = null, want []")
	}

	h.send(t, "api", "hello")
	waitFor(t, "the stream to appear", func() bool {
		streams, _ := h.store.Streams(context.Background())
		return 1 == len(streams)
	})

	response, err = http.Get(h.server.URL + "/v1/streams")
	if nil != err {
		t.Fatal(err)
	}
	defer response.Body.Close()

	var payload struct {
		Streams []string `json:"streams"`
	}
	json.NewDecoder(response.Body).Decode(&payload)

	if 1 != len(payload.Streams) || "api" != payload.Streams[0] {
		t.Errorf("streams = %v, want [api]", payload.Streams)
	}
}

func TestHealthReportsIngestCounters(t *testing.T) {
	h := newHarness(t)

	h.send(t, "api", "good")
	h.sendRaw(t, []byte("this is not json"))
	h.sendRaw(t, []byte(`{"timestamp":1,"content":"missing an id"}`))

	waitFor(t, "all three datagrams", func() bool {
		response, err := http.Get(h.server.URL + "/healthz")
		if nil != err {
			return false
		}
		defer response.Body.Close()

		var payload struct {
			UDP struct {
				Received uint64 `json:"received"`
				Invalid  uint64 `json:"invalid"`
			} `json:"udp"`
		}
		json.NewDecoder(response.Body).Decode(&payload)

		return 3 == payload.UDP.Received && 2 == payload.UDP.Invalid
	})

	// The malformed datagrams created no streams.
	streams, _ := h.store.Streams(context.Background())
	if 1 != len(streams) || "api" != streams[0] {
		t.Errorf("streams = %v, want [api]", streams)
	}
}

// A datagram larger than the read buffer must not wedge the receiver.
func TestOversizedDatagramDoesNotStopIngest(t *testing.T) {
	h := newHarness(t)

	h.sendRaw(t, []byte(`{"id":"api","timestamp":1,"content":"`+strings.Repeat("x", 70000)+`"}`))
	h.send(t, "api", "still alive")

	waitFor(t, "ingest to keep working", func() bool {
		backlog, _ := h.store.Backlog(context.Background(), "api", 0)
		return 1 == len(backlog) && "still alive" == backlog[0].Raw
	})
}
