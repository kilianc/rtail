/*!
 * The v2 receivers.
 *
 * Each protocol gets the same treatment: a real payload in the shape the
 * ecosystem actually emits, and an assertion that it lands in the envelope
 * correctly — because the whole value of speaking these protocols is that a
 * record arriving over one is indistinguishable from a record arriving over
 * another once it is stored.
 */

package ingest_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/kilianc/rtail/v2/internal/ingest"
	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/model"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// httpHarness mounts the ingest handlers over a memory store.
type httpHarness struct {
	server *httptest.Server
	store  *logstore.Memory
}

func newHTTPHarness(t *testing.T) *httpHarness {
	t.Helper()

	return newHTTPHarnessWith(t, ingest.HTTPOptions{})
}

// newHTTPHarnessWith mounts the handlers with opts. records reads the
// harness's own memory store, which is the one used unless opts names another.
func newHTTPHarnessWith(t *testing.T, opts ingest.HTTPOptions) *httpHarness {
	t.Helper()

	store := logstore.NewMemory(1000)
	if nil == opts.Store {
		opts.Store = store
	}
	opts.Log = quiet()

	receiver := ingest.NewHTTP(opts)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingest", receiver.Lines)
	mux.HandleFunc("POST /v1/logs", receiver.OTLP)

	server := httptest.NewServer(mux)

	t.Cleanup(func() {
		server.Close()
		store.Close()
	})

	return &httpHarness{server: server, store: store}
}

func (h *httpHarness) records(t *testing.T, stream string) []*model.Record {
	t.Helper()

	records, err := h.store.Backlog(context.Background(), stream, 0)
	if nil != err {
		t.Fatal(err)
	}

	return records
}

func (h *httpHarness) post(t *testing.T, path, contentType string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, h.server.URL+path, body)
	if nil != err {
		t.Fatal(err)
	}

	request.Header.Set("Content-Type", contentType)
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	response, err := http.DefaultClient.Do(request)
	if nil != err {
		t.Fatal(err)
	}

	t.Cleanup(func() { response.Body.Close() })

	return response
}

// CRLF bodies store their lines without the \r, whether or not the last line
// has a newline of its own. The line reader keeps it; FromLine drops it.
func TestIngestStripsCarriageReturns(t *testing.T) {
	h := newHTTPHarness(t)

	body := "first line\r\n{\"msg\":\"second\"}\r\nlast line\r"
	response := h.post(t, "/v1/ingest?stream=crlf", "text/plain", strings.NewReader(body), nil)

	if http.StatusOK != response.StatusCode {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	records := h.records(t, "crlf")
	if 3 != len(records) {
		t.Fatalf("stored %d records, want 3", len(records))
	}

	for i, want := range []string{"first line", `{"msg":"second"}`, "last line"} {
		if want != records[i].Raw {
			t.Errorf("record %d raw = %q, want %q", i, records[i].Raw, want)
		}
	}
}

/*!
 * NDJSON and plain text share one code path, because the normalizer already
 * decides per line what it is looking at.
 */
func TestIngestAcceptsNDJSONAndPlainLines(t *testing.T) {
	h := newHTTPHarness(t)

	body := strings.Join([]string{
		`{"level":"error","msg":"upstream timeout","service":"api","latency_ms":30004.2}`,
		`a plain unstructured line`,
		``,
		`{"level":"info","msg":"ok"}`,
	}, "\n")

	response := h.post(t, "/v1/ingest?stream=api", "application/x-ndjson", strings.NewReader(body), nil)

	if http.StatusOK != response.StatusCode {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	var out struct {
		Accepted int `json:"accepted"`
		Rejected int `json:"rejected"`
	}
	json.NewDecoder(response.Body).Decode(&out)

	// The blank line is accepted with the rest — accepted counts lines, which
	// is where a shipper resumes — and has nothing in it to store.
	if 4 != out.Accepted || 0 != out.Rejected {
		t.Fatalf("accepted %d rejected %d, want 4 and 0", out.Accepted, out.Rejected)
	}

	records := h.records(t, "api")
	if 3 != len(records) {
		t.Fatalf("stored %d records, want 3", len(records))
	}

	if "ERROR" != records[0].Level {
		t.Errorf("level = %q, want ERROR", records[0].Level)
	}
	if "upstream timeout" != records[0].Msg {
		t.Errorf("msg = %q", records[0].Msg)
	}
	if 30004.2 != records[0].Fields["latency_ms"].Float {
		t.Errorf("latency_ms = %v", records[0].Fields["latency_ms"])
	}

	// The plain line keeps its text and is not pretending to be structured.
	if "a plain unstructured line" != records[1].Msg {
		t.Errorf("msg = %q", records[1].Msg)
	}
	if "string" != records[1].Type {
		t.Errorf("type = %q, want string", records[1].Type)
	}
}

func TestIngestDecodesGzip(t *testing.T) {
	h := newHTTPHarness(t)

	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	writer.Write([]byte(`{"msg":"compressed"}` + "\n" + `{"msg":"also compressed"}`))
	writer.Close()

	response := h.post(t, "/v1/ingest?stream=gz", "application/x-ndjson", &buffer,
		map[string]string{"Content-Encoding": "gzip"})

	if http.StatusOK != response.StatusCode {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	if 2 != len(h.records(t, "gz")) {
		t.Errorf("stored %d records, want 2", len(h.records(t, "gz")))
	}
}

func TestIngestDecodesZstd(t *testing.T) {
	h := newHTTPHarness(t)

	encoder, err := zstd.NewWriter(nil)
	if nil != err {
		t.Fatal(err)
	}
	body := encoder.EncodeAll([]byte(`{"msg":"compressed"}`+"\n"+`{"msg":"also compressed"}`), nil)
	encoder.Close()

	response := h.post(t, "/v1/ingest?stream=zst", "application/x-ndjson", bytes.NewReader(body),
		map[string]string{"Content-Encoding": "zstd"})

	if http.StatusOK != response.StatusCode {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	if 2 != len(h.records(t, "zst")) {
		t.Errorf("stored %d records, want 2", len(h.records(t, "zst")))
	}
}

/*!
 * A zstd frame names its window size in its header, and the decoder
 * allocates that much before it reads a block. At the library's default
 * ceiling, ten bytes declaring a 512MB window cost half a gigabyte apiece.
 */
func TestIngestRefusesAZstdWindowTooLargeToAllocate(t *testing.T) {
	h := newHTTPHarness(t)

	// The magic number, a descriptor with no content size, a window of 2^29,
	// and one raw block holding "a".
	frame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 19 << 3, 0x09, 0x00, 0x00, 'a'}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	response := h.post(t, "/v1/ingest?stream=window", "text/plain", bytes.NewReader(frame),
		map[string]string{"Content-Encoding": "zstd"})

	runtime.ReadMemStats(&after)

	if http.StatusBadRequest != response.StatusCode {
		t.Errorf("status = %d, want 400", response.StatusCode)
	}

	if grown := after.TotalAlloc - before.TotalAlloc; grown > 64<<20 {
		t.Errorf("a ten-byte body allocated %dMB", grown>>20)
	}
}

func TestIngestRejectsUnknownEncoding(t *testing.T) {
	h := newHTTPHarness(t)

	response := h.post(t, "/v1/ingest", "application/x-ndjson", strings.NewReader("{}"),
		map[string]string{"Content-Encoding": "br"})

	if http.StatusBadRequest != response.StatusCode {
		t.Errorf("status = %d, want 400", response.StatusCode)
	}
}

// Records with no stream land somewhere predictable rather than being dropped.
func TestIngestDefaultsTheStream(t *testing.T) {
	h := newHTTPHarness(t)

	h.post(t, "/v1/ingest", "text/plain", strings.NewReader("no stream named"), nil)

	if 1 != len(h.records(t, ingest.DefaultStream)) {
		t.Errorf("records on %q = %d, want 1", ingest.DefaultStream, len(h.records(t, ingest.DefaultStream)))
	}
}

/*!
 * A body of blank lines is accepted line for line, because accepted is how
 * far into the body a shipper resumes, and stores nothing. An empty body is
 * no lines at all.
 */
func TestIngestOfNothingIsSuccessfulAndEmpty(t *testing.T) {
	for body, want := range map[string]int{"": 0, "\n\n  \n": 3} {
		h := newHTTPHarness(t)

		response := h.post(t, "/v1/ingest?stream=empty", "application/x-ndjson", strings.NewReader(body), nil)

		if http.StatusOK != response.StatusCode {
			t.Fatalf("%q: status = %d, want 200", body, response.StatusCode)
		}

		if out := decodeAnswer(t, response); want != out.Accepted {
			t.Errorf("%q: accepted = %d, want %d", body, out.Accepted, want)
		}

		if stored := len(h.records(t, "empty")); 0 != stored {
			t.Errorf("%q: stored %d records, want none", body, stored)
		}
	}
}

// answer is the body every /v1/ingest response carries.
type answer struct {
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Errors   []string `json:"errors"`
}

// decodeAnswer reads a response body, failing unless it has all three keys —
// a shipper resuming after a failure depends on accepted being there.
func decodeAnswer(t *testing.T, response *http.Response) answer {
	t.Helper()

	raw, err := io.ReadAll(response.Body)
	if nil != err {
		t.Fatal(err)
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); nil != err {
		t.Fatalf("response %q is not a JSON object: %v", raw, err)
	}
	for _, key := range []string{"accepted", "rejected", "errors"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("response %s has no %q", raw, key)
		}
	}

	var out answer
	json.Unmarshal(raw, &out)

	return out
}

func TestIngestAnswersWithAllThreeKeysOnSuccess(t *testing.T) {
	h := newHTTPHarness(t)

	out := decodeAnswer(t, h.post(t, "/v1/ingest?stream=keys", "text/plain", strings.NewReader("one\n"), nil))

	if 1 != out.Accepted || 0 != out.Rejected || 0 != len(out.Errors) {
		t.Errorf("answer = %+v, want one accepted and nothing else", out)
	}
}

/*!
 * An over-long line is truncated and stored, and the batch goes on: one
 * pathological line must not cost a shipper the rest of its batch, and the
 * 200 still says what happened to it.
 */
func TestIngestTruncatesAnOverlongLine(t *testing.T) {
	h := newHTTPHarness(t)

	long := strings.Repeat("x", ingest.MaxLineForTest+100)
	response := h.post(t, "/v1/ingest?stream=long", "text/plain", strings.NewReader(long+"\nafter\n"), nil)

	if http.StatusOK != response.StatusCode {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	out := decodeAnswer(t, response)
	if 2 != out.Accepted || 0 != out.Rejected {
		t.Errorf("accepted %d rejected %d, want 2 and 0", out.Accepted, out.Rejected)
	}
	if 1 != len(out.Errors) || !strings.Contains(out.Errors[0], "1 line(s)") {
		t.Errorf("errors = %q, want one note about the truncated line", out.Errors)
	}

	records := h.records(t, "long")
	if 2 != len(records) {
		t.Fatalf("stored %d records, want 2", len(records))
	}
	if ingest.MaxLineForTest != len(records[0].Raw) {
		t.Errorf("stored %d bytes of the long line, want %d", len(records[0].Raw), ingest.MaxLineForTest)
	}
	if "after" != records[1].Msg {
		t.Errorf("msg = %q, want the line after the long one", records[1].Msg)
	}
}

/*!
 * A failure part-way through a batch says how much of it is stored, and that
 * count is exact: a shipper resumes after `accepted` lines, so a fragment the
 * failed read left behind must not be stored, or counted.
 */
func TestIngestAnswers413ForABodyOverMaxBody(t *testing.T) {
	h := newHTTPHarnessWith(t, ingest.HTTPOptions{MaxBody: 1024})

	line := strings.Repeat("y", 199) + "\n"
	response := h.post(t, "/v1/ingest?stream=big", "text/plain", strings.NewReader(strings.Repeat(line, 10)), nil)

	if http.StatusRequestEntityTooLarge != response.StatusCode {
		t.Fatalf("status = %d, want 413", response.StatusCode)
	}

	// 1024 bytes hold five whole 200-byte lines and a piece of the sixth.
	out := decodeAnswer(t, response)
	if 5 != out.Accepted || 1 != out.Rejected {
		t.Errorf("accepted %d rejected %d, want 5 and 1", out.Accepted, out.Rejected)
	}
	if 5 != len(h.records(t, "big")) {
		t.Errorf("stored %d records, want 5", len(h.records(t, "big")))
	}
}

// MaxBody bounds the bytes on the wire, which says nothing about what they
// decompress to.
func TestIngestAnswers413ForABodyThatInflatesPastTheLimit(t *testing.T) {
	const maxBody = 256

	h := newHTTPHarnessWith(t, ingest.HTTPOptions{MaxBody: maxBody})

	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	writer.Write(bytes.Repeat([]byte("abcdefg\n"), 4096))
	writer.Close()

	if maxBody < buffer.Len() {
		t.Fatalf("compressed to %d bytes, want it under MaxBody", buffer.Len())
	}

	response := h.post(t, "/v1/ingest?stream=bomb", "text/plain", &buffer,
		map[string]string{"Content-Encoding": "gzip"})

	if http.StatusRequestEntityTooLarge != response.StatusCode {
		t.Fatalf("status = %d, want 413", response.StatusCode)
	}

	// The limit falls exactly between two eight-byte lines.
	want := ingest.MaxInflationForTest * maxBody / 8
	out := decodeAnswer(t, response)
	if want != out.Accepted || 1 != out.Rejected {
		t.Errorf("accepted %d rejected %d, want %d and 1", out.Accepted, out.Rejected, want)
	}
}

func TestIngestAnswers400ForABodyThatStopsDecoding(t *testing.T) {
	h := newHTTPHarness(t)

	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	for i := range 500 {
		fmt.Fprintf(writer, `{"msg":"line %d"}`+"\n", i)
	}
	writer.Close()

	// A good header, and a stream that ends halfway.
	cut := buffer.Bytes()[:buffer.Len()/2]

	response := h.post(t, "/v1/ingest?stream=cut", "application/x-ndjson", bytes.NewReader(cut),
		map[string]string{"Content-Encoding": "gzip"})

	if http.StatusBadRequest != response.StatusCode {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}

	out := decodeAnswer(t, response)
	if 1 != out.Rejected {
		t.Errorf("rejected = %d, want 1", out.Rejected)
	}

	records := h.records(t, "cut")
	if out.Accepted != len(records) {
		t.Fatalf("accepted %d but stored %d", out.Accepted, len(records))
	}
	for i, rec := range records {
		if want := fmt.Sprintf("line %d", i); want != rec.Msg {
			t.Fatalf("record %d msg = %q, want %q — a fragment was stored", i, rec.Msg, want)
		}
	}
}

// failingStore stores a fixed number of records, then refuses.
type failingStore struct {
	*logstore.Memory
	left int
}

func (s *failingStore) Append(ctx context.Context, rec *model.Record) error {
	if 0 == s.left {
		return errors.New("disk full")
	}
	s.left--
	return s.Memory.Append(ctx, rec)
}

func TestIngestAnswers503WithWhatWasStored(t *testing.T) {
	memory := logstore.NewMemory(10)
	t.Cleanup(func() { memory.Close() })

	h := newHTTPHarnessWith(t, ingest.HTTPOptions{Store: &failingStore{Memory: memory, left: 2}})

	response := h.post(t, "/v1/ingest?stream=full", "text/plain", strings.NewReader("one\ntwo\nthree\nfour\n"), nil)

	if http.StatusServiceUnavailable != response.StatusCode {
		t.Fatalf("status = %d, want 503", response.StatusCode)
	}

	out := decodeAnswer(t, response)
	if 2 != out.Accepted || 1 != out.Rejected {
		t.Errorf("accepted %d rejected %d, want 2 and 1", out.Accepted, out.Rejected)
	}
}

/*!
 * A shipper resumes after `accepted` lines of its body, blank ones included.
 * Counting only the lines stored had it resume short, and send again lines
 * that were already stored.
 */
func TestIngestCountsBlankLinesInWhatItAccepted(t *testing.T) {
	memory := logstore.NewMemory(10)
	t.Cleanup(func() { memory.Close() })

	h := newHTTPHarnessWith(t, ingest.HTTPOptions{Store: &failingStore{Memory: memory, left: 2}})

	lines := []string{"one", "", "two", "three", "four"}
	response := h.post(t, "/v1/ingest?stream=blank", "text/plain", strings.NewReader(strings.Join(lines, "\n")+"\n"), nil)

	if http.StatusServiceUnavailable != response.StatusCode {
		t.Fatalf("status = %d, want 503", response.StatusCode)
	}

	// One, the blank line and two; three is where storage refused.
	out := decodeAnswer(t, response)
	if 3 != out.Accepted {
		t.Fatalf("accepted = %d, want 3", out.Accepted)
	}

	records, _ := memory.Backlog(context.Background(), "blank", 0)
	if 2 != len(records) || "one" != records[0].Msg || "two" != records[1].Msg {
		t.Fatalf("stored %v, want one and two", records)
	}

	if "three" != lines[out.Accepted] {
		t.Errorf("a shipper would resume at %q, want the first line not stored", lines[out.Accepted])
	}
}

/*!
 * A body that stops arriving is let go of at the body timeout, and answered
 * with what of it was stored, rather than holding a handler and its
 * goroutine for as long as the client likes.
 */
func TestIngestAnswers408ForABodyThatStalls(t *testing.T) {
	store := logstore.NewMemory(10)
	t.Cleanup(func() { store.Close() })

	receiver := ingest.NewHTTP(ingest.HTTPOptions{Store: store, Log: quiet()})
	ingest.SetBodyTimeoutForTest(receiver, 200*time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingest", receiver.Lines)
	mux.HandleFunc("POST /v1/logs", receiver.OTLP)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	for path, want := range map[string]int{"/v1/ingest?stream=stalled": 2, "/v1/logs": 0} {
		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		if nil != err {
			t.Fatal(err)
		}
		defer conn.Close()

		// Two whole lines of a body that promises a kilobyte, then nothing.
		fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: rtail\r\nContent-Type: text/plain\r\n"+
			"Content-Length: 1024\r\n\r\none\ntwo\n", path)

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if nil != err {
			t.Fatalf("%s: %v — still waiting for the body", path, err)
		}

		if http.StatusRequestTimeout != response.StatusCode {
			t.Errorf("%s: status = %d, want 408", path, response.StatusCode)
		}

		if out := decodeAnswer(t, response); want != out.Accepted || 1 != out.Rejected {
			t.Errorf("%s: accepted %d rejected %d, want %d and 1", path, out.Accepted, out.Rejected, want)
		}
	}

	if records, _ := store.Backlog(context.Background(), "stalled", 0); 2 != len(records) {
		t.Errorf("stored %d records, want the 2 that arrived", len(records))
	}
}

// syncingStore records when it was synced, and can refuse to be.
type syncingStore struct {
	*logstore.Memory
	appended int
	// syncedAt is how many records had been appended at the last Sync, or -1
	// when Sync was never called.
	syncedAt int
	refuse   error
}

func (s *syncingStore) Append(ctx context.Context, rec *model.Record) error {
	s.appended++
	return s.Memory.Append(ctx, rec)
}

func (s *syncingStore) Sync(context.Context) error {
	if nil != s.refuse {
		return s.refuse
	}
	s.syncedAt = s.appended
	return nil
}

func newSyncingStore(t *testing.T, refuse error) *syncingStore {
	memory := logstore.NewMemory(10)
	t.Cleanup(func() { memory.Close() })
	return &syncingStore{Memory: memory, syncedAt: -1, refuse: refuse}
}

/*!
 * A 200 is sent after the records are synced, not merely appended.
 *
 * Append leaves a record in the write-ahead log's buffer until the next sync
 * tick, and a shipper drops its copy the moment it reads a 200 — so an
 * acknowledgement sent between the two was one a crash could take back.
 */
func TestIngestSyncsBeforeItAcknowledges(t *testing.T) {
	store := newSyncingStore(t, nil)
	h := newHTTPHarnessWith(t, ingest.HTTPOptions{Store: store})

	response := h.post(t, "/v1/ingest?stream=s", "text/plain", strings.NewReader("one\ntwo\nthree\n"), nil)

	if http.StatusOK != response.StatusCode {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if 3 != store.syncedAt {
		t.Errorf("synced after %d of 3 records, want all 3 (-1 is never)", store.syncedAt)
	}
}

// A batch that stopped early reports what it stored, and that is synced too.
func TestIngestSyncsWhatAnEarlyStopAccepted(t *testing.T) {
	store := newSyncingStore(t, nil)
	h := newHTTPHarnessWith(t, ingest.HTTPOptions{Store: store, MaxBody: 10})

	response := h.post(t, "/v1/ingest?stream=s", "text/plain", strings.NewReader("one\ntwo\nthree\nfour\n"), nil)

	if http.StatusRequestEntityTooLarge != response.StatusCode {
		t.Fatalf("status = %d, want 413", response.StatusCode)
	}

	out := decodeAnswer(t, response)
	if 0 == out.Accepted || store.syncedAt != out.Accepted {
		t.Errorf("accepted %d, synced after %d", out.Accepted, store.syncedAt)
	}
}

// When the sync fails nothing can be vouched for, so nothing is accepted: a
// retry may store some lines twice, which beats a shipper skipping lines that
// were never on disk.
func TestIngestAcceptsNothingWhenTheSyncFails(t *testing.T) {
	store := newSyncingStore(t, errors.New("fsync: input/output error"))
	h := newHTTPHarnessWith(t, ingest.HTTPOptions{Store: store})

	response := h.post(t, "/v1/ingest?stream=s", "text/plain", strings.NewReader("one\ntwo\n"), nil)

	if http.StatusServiceUnavailable != response.StatusCode {
		t.Fatalf("status = %d, want 503", response.StatusCode)
	}

	out := decodeAnswer(t, response)
	if 0 != out.Accepted || 1 != out.Rejected {
		t.Errorf("accepted %d rejected %d, want 0 and 1", out.Accepted, out.Rejected)
	}
}

// OTLP acknowledges with partialSuccess, and a collector drops its batch on
// it just the same.
func TestOTLPSyncsBeforeItAcknowledges(t *testing.T) {
	for _, refuse := range []error{nil, errors.New("fsync: input/output error")} {
		store := newSyncingStore(t, refuse)
		h := newHTTPHarnessWith(t, ingest.HTTPOptions{Store: store})

		batch := otlpBatch("svc", "hello", logspb.SeverityNumber_SEVERITY_NUMBER_INFO, time.Now())
		body, err := proto.Marshal(batch)
		if nil != err {
			t.Fatal(err)
		}

		response := h.post(t, "/v1/logs", "application/x-protobuf", bytes.NewReader(body), nil)

		switch {
		case nil == refuse && http.StatusOK != response.StatusCode:
			t.Errorf("status = %d, want 200", response.StatusCode)
		case nil == refuse && 1 != store.syncedAt:
			t.Errorf("synced after %d of 1 records", store.syncedAt)
		case nil != refuse && http.StatusServiceUnavailable != response.StatusCode:
			t.Errorf("with a failing sync, status = %d, want 503", response.StatusCode)
		}
	}
}

// The peer's address is the record's host, and an IPv6 one has colons in it.
func TestIngestReadsAnIPv6PeerAddress(t *testing.T) {
	store := logstore.NewMemory(10)
	t.Cleanup(func() { store.Close() })

	receiver := ingest.NewHTTP(ingest.HTTPOptions{Store: store, Log: quiet()})

	request := httptest.NewRequest(http.MethodPost, "/v1/ingest?stream=v6", strings.NewReader("hello\n"))
	request.RemoteAddr = "[::1]:54321"
	receiver.Lines(httptest.NewRecorder(), request)

	records, _ := store.Backlog(context.Background(), "v6", 0)
	if 1 != len(records) || "::1" != records[0].Host {
		t.Fatalf("records = %v, want one from host ::1", records)
	}
}

/*!
 * One byte that is not UTF-8 makes DuckDB refuse the whole Parquet file it is
 * stored in, so it is replaced at the door.
 */
func TestIngestReplacesInvalidUTF8(t *testing.T) {
	h := newHTTPHarness(t)

	body := "caf\xe9 au lait\n{\"msg\":\"na\xefve\"}\n"
	h.post(t, "/v1/ingest?stream=latin", "text/plain", strings.NewReader(body), nil)

	records := h.records(t, "latin")
	if 2 != len(records) {
		t.Fatalf("stored %d records, want 2", len(records))
	}

	for _, rec := range records {
		if !utf8.ValidString(rec.Raw) || !utf8.ValidString(rec.Msg) || !utf8.Valid(rec.JSON) {
			t.Errorf("record %q is not valid UTF-8", rec.Raw)
		}
	}

	if "caf\uFFFD au lait" != records[0].Msg {
		t.Errorf("msg = %q, want the bad byte replaced", records[0].Msg)
	}
}

/*!
 * OTLP, in both encodings a collector might use.
 */
func otlpBatch(service, body string, severity logspb.SeverityNumber, at time.Time) *logspb.LogsData {
	return &logspb.LogsData{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					{
						Key:   "service.name",
						Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: service}},
					},
					{
						Key:   "deployment.environment",
						Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "prod"}},
					},
				},
			},
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope: &commonpb.InstrumentationScope{Name: "checkout"},
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano:   uint64(at.UnixNano()),
					SeverityNumber: severity,
					SeverityText:   "Error",
					Body: &commonpb.AnyValue{
						Value: &commonpb.AnyValue_StringValue{StringValue: body},
					},
					Attributes: []*commonpb.KeyValue{
						{
							Key:   "user_id",
							Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 4471}},
						},
						{
							Key:   "retry",
							Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}},
						},
					},
					TraceId: []byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
					SpanId:  []byte{1, 2, 3, 4, 5, 6, 7, 8},
				}},
			}},
		}},
	}
}

func TestOTLPProtobuf(t *testing.T) {
	h := newHTTPHarness(t)

	at := time.Now().UTC().Truncate(time.Millisecond)
	batch := otlpBatch("checkout-api", "payment declined", logspb.SeverityNumber_SEVERITY_NUMBER_ERROR, at)

	encoded, err := proto.Marshal(batch)
	if nil != err {
		t.Fatal(err)
	}

	response := h.post(t, "/v1/logs", "application/x-protobuf", bytes.NewReader(encoded), nil)
	if http.StatusOK != response.StatusCode {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}

	// service.name becomes the stream, so logs land where a person looks.
	records := h.records(t, "checkout-api")
	if 1 != len(records) {
		t.Fatalf("stored %d records on checkout-api, want 1", len(records))
	}

	rec := records[0]

	if "ERROR" != rec.Level {
		t.Errorf("level = %q, want ERROR", rec.Level)
	}
	if "payment declined" != rec.Msg {
		t.Errorf("msg = %q", rec.Msg)
	}
	if !rec.Ts.Equal(at) {
		t.Errorf("ts = %s, want %s", rec.Ts, at)
	}

	// Log attributes, resource attributes and scope all survive as fields.
	if 4471 != rec.Fields["user_id"].Int {
		t.Errorf("user_id = %v", rec.Fields["user_id"])
	}
	if !rec.Fields["retry"].Bool {
		t.Errorf("retry = %v", rec.Fields["retry"])
	}
	if "prod" != rec.Fields["deployment.environment"].Str {
		t.Errorf("deployment.environment = %v", rec.Fields["deployment.environment"])
	}
	if "checkout" != rec.Fields["otel.scope"].Str {
		t.Errorf("otel.scope = %v", rec.Fields["otel.scope"])
	}

	// Trace correlation is the reason to run OTLP, so it is promoted.
	if "deadbeef0102030405060708090a0b0c" != rec.Fields["trace_id"].Str {
		t.Errorf("trace_id = %q", rec.Fields["trace_id"].Str)
	}
	if "0102030405060708" != rec.Fields["span_id"].Str {
		t.Errorf("span_id = %q", rec.Fields["span_id"].Str)
	}

	// raw has to be JSON text, or the ->> fallback for unpromoted keys cannot
	// walk it.
	if !json.Valid([]byte(rec.Raw)) {
		t.Errorf("raw is not valid JSON: %q", rec.Raw)
	}
}

func TestOTLPJSON(t *testing.T) {
	h := newHTTPHarness(t)

	// The shape an OTLP/JSON exporter sends, written out rather than generated,
	// so the test would notice if our decoding drifted from the wire format.
	body := `{
	  "resourceLogs": [{
	    "resource": { "attributes": [
	      { "key": "service.name", "value": { "stringValue": "billing" } }
	    ]},
	    "scopeLogs": [{
	      "logRecords": [{
	        "timeUnixNano": "1786724529000000000",
	        "severityNumber": 13,
	        "severityText": "Warn",
	        "body": { "stringValue": "retrying charge" },
	        "attributes": [
	          { "key": "attempt", "value": { "intValue": "2" } }
	        ]
	      }]
	    }]
	  }]
	}`

	response := h.post(t, "/v1/logs", "application/json", strings.NewReader(body), nil)
	if http.StatusOK != response.StatusCode {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d: %s", response.StatusCode, raw)
	}

	records := h.records(t, "billing")
	if 1 != len(records) {
		t.Fatalf("stored %d records, want 1", len(records))
	}

	if "WARN" != records[0].Level {
		t.Errorf("level = %q, want WARN", records[0].Level)
	}
	if "retrying charge" != records[0].Msg {
		t.Errorf("msg = %q", records[0].Msg)
	}
	if 2 != records[0].Fields["attempt"].Int {
		t.Errorf("attempt = %v", records[0].Fields["attempt"])
	}
}

// The severity scale runs 1-24 in four-wide bands.
func TestOTLPSeverityBands(t *testing.T) {
	cases := []struct {
		number logspb.SeverityNumber
		want   string
	}{
		{logspb.SeverityNumber_SEVERITY_NUMBER_TRACE, "TRACE"},
		{logspb.SeverityNumber_SEVERITY_NUMBER_DEBUG4, "DEBUG"},
		{logspb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO"},
		{logspb.SeverityNumber_SEVERITY_NUMBER_INFO3, "INFO"},
		{logspb.SeverityNumber_SEVERITY_NUMBER_WARN, "WARN"},
		{logspb.SeverityNumber_SEVERITY_NUMBER_ERROR2, "ERROR"},
		{logspb.SeverityNumber_SEVERITY_NUMBER_FATAL4, "FATAL"},
	}

	for _, tc := range cases {
		h := newHTTPHarness(t)

		batch := otlpBatch("sev", "x", tc.number, time.Now())
		encoded, _ := proto.Marshal(batch)

		h.post(t, "/v1/logs", "application/x-protobuf", bytes.NewReader(encoded), nil)

		records := h.records(t, "sev")
		if 1 != len(records) {
			t.Fatalf("%v: stored %d records", tc.number, len(records))
		}
		if records[0].Level != tc.want {
			t.Errorf("severity %v → %q, want %q", tc.number, records[0].Level, tc.want)
		}
	}
}

/*!
 * OTLP/JSON writes trace and span ids as hex, not as the base64 protojson
 * expects of bytes. Hex digits are base64 digits too, so reading them the
 * standard way does not fail: it stores ids that match nothing.
 */
func TestOTLPJSONReadsHexTraceIDs(t *testing.T) {
	h := newHTTPHarness(t)

	// The second record is the same ids in base64, as an exporter written
	// before the spec settled on hex sends them.
	body := `{"resourceLogs": [{
	  "resource": { "attributes": [
	    { "key": "service.name", "value": { "stringValue": "traced" } }
	  ]},
	  "scopeLogs": [{ "logRecords": [
	    { "body": { "stringValue": "one" },
	      "traceId": "5B8EFFF798038103D269B633813FC60C", "spanId": "eee19b7ec3c1b174" },
	    { "body": { "stringValue": "two" },
	      "traceId": "W47/95gDgQPSabYzgT/GDA==", "spanId": "7uGbfsPBsXQ=" }
	  ]}]
	}]}`

	response := h.post(t, "/v1/logs", "application/json", strings.NewReader(body), nil)
	if http.StatusOK != response.StatusCode {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d: %s", response.StatusCode, raw)
	}

	records := h.records(t, "traced")
	if 2 != len(records) {
		t.Fatalf("stored %d records, want 2", len(records))
	}

	for _, rec := range records {
		if "5b8efff798038103d269b633813fc60c" != rec.Fields["trace_id"].Str {
			t.Errorf("%s: trace_id = %q", rec.Msg, rec.Fields["trace_id"].Str)
		}
		if "eee19b7ec3c1b174" != rec.Fields["span_id"].Str {
			t.Errorf("%s: span_id = %q", rec.Msg, rec.Fields["span_id"].Str)
		}
	}
}

// protojson reads the proto field names as well as the JSON ones, and an id
// is hex in either spelling.
func TestOTLPJSONReadsSnakeCaseHexTraceIDs(t *testing.T) {
	h := newHTTPHarness(t)

	body := `{"resource_logs": [{
	  "resource": { "attributes": [
	    { "key": "service.name", "value": { "string_value": "snaked" } }
	  ]},
	  "scope_logs": [{ "log_records": [
	    { "body": { "string_value": "one" },
	      "trace_id": "5b8efff798038103d269b633813fc60c", "span_id": "eee19b7ec3c1b174" }
	  ]}]
	}]}`

	response := h.post(t, "/v1/logs", "application/json", strings.NewReader(body), nil)
	if http.StatusOK != response.StatusCode {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d: %s", response.StatusCode, raw)
	}

	records := h.records(t, "snaked")
	if 1 != len(records) {
		t.Fatalf("stored %d records, want 1", len(records))
	}

	if "5b8efff798038103d269b633813fc60c" != records[0].Fields["trace_id"].Str {
		t.Errorf("trace_id = %q", records[0].Fields["trace_id"].Str)
	}
	if "eee19b7ec3c1b174" != records[0].Fields["span_id"].Str {
		t.Errorf("span_id = %q", records[0].Fields["span_id"].Str)
	}
}

// otlpAnswer is ExportLogsServiceResponse as OTLP/JSON spells it: proto3's
// JSON mapping, which writes an int64 as a string.
type otlpAnswer struct {
	PartialSuccess *struct {
		RejectedLogRecords string `json:"rejectedLogRecords"`
		ErrorMessage       string `json:"errorMessage"`
	} `json:"partialSuccess"`
}

func decodeOTLPAnswer(t *testing.T, response *http.Response) otlpAnswer {
	t.Helper()

	var out otlpAnswer
	if err := json.NewDecoder(response.Body).Decode(&out); nil != err || nil == out.PartialSuccess {
		t.Fatalf("response is not an ExportLogsServiceResponse: %v", err)
	}

	return out
}

/*!
 * An OTLP body or attribute longer than maxLine is truncated to it, as a line
 * is on every other path. Kept whole, a 22MB body under MaxBody came to a
 * write-ahead log frame past the 64MB limit, the batch answered 503, and the
 * collector's retry stored every record before it again — forever.
 */
func TestOTLPTruncatesAnOverlongValue(t *testing.T) {
	store, err := logstore.OpenDurable(context.Background(), t.TempDir(), logstore.DurableOptions{Log: quiet()})
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	h := newHTTPHarnessWith(t, ingest.HTTPOptions{Store: store})

	batch := otlpBatch("long", "first", logspb.SeverityNumber_SEVERITY_NUMBER_INFO, time.Now())
	records := batch.ResourceLogs[0].ScopeLogs[0].LogRecords

	long := proto.Clone(records[0]).(*logspb.LogRecord)
	long.Body = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: strings.Repeat("a", 22<<20)}}
	long.Attributes = append(long.Attributes, &commonpb.KeyValue{
		Key:   "dump",
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: strings.Repeat("b", 2*ingest.MaxLineForTest)}},
	})
	batch.ResourceLogs[0].ScopeLogs[0].LogRecords = append(records, long)

	encoded, err := proto.Marshal(batch)
	if nil != err {
		t.Fatal(err)
	}

	response := h.post(t, "/v1/logs", "application/x-protobuf", bytes.NewReader(encoded), nil)
	if http.StatusOK != response.StatusCode {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d: %s", response.StatusCode, raw)
	}

	// Truncation is a warning: nothing rejected, and a message saying so.
	answer := decodeOTLPAnswer(t, response)
	if "" != answer.PartialSuccess.RejectedLogRecords {
		t.Errorf("rejected = %q, want nothing", answer.PartialSuccess.RejectedLogRecords)
	}
	if !strings.Contains(answer.PartialSuccess.ErrorMessage, "1 log record(s)") {
		t.Errorf("error message = %q, want a note about the truncated record", answer.PartialSuccess.ErrorMessage)
	}

	stored, _ := store.Backlog(context.Background(), "long", 0)
	if 2 != len(stored) {
		t.Fatalf("stored %d records, want 2", len(stored))
	}
	if ingest.MaxLineForTest != len(stored[1].Msg) {
		t.Errorf("stored %d bytes of the long body, want %d", len(stored[1].Msg), ingest.MaxLineForTest)
	}
	if ingest.MaxLineForTest != len(stored[1].Fields["dump"].Str) {
		t.Errorf("stored %d bytes of the long attribute, want %d", len(stored[1].Fields["dump"].Str), ingest.MaxLineForTest)
	}
}

/*!
 * A record still too large to store once its values are truncated — a map
 * cannot be cut and stay JSON — is refused on its own, and reported in
 * partialSuccess, which a collector does not retry. The rest of the batch is
 * stored, once.
 */
func TestOTLPRejectsARecordTooLargeToStore(t *testing.T) {
	h := newHTTPHarness(t)

	var values []*commonpb.KeyValue
	for i := range ingest.MaxRecordForTest/ingest.MaxLineForTest + 1 {
		values = append(values, &commonpb.KeyValue{
			Key:   fmt.Sprint(i),
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: strings.Repeat("z", ingest.MaxLineForTest)}},
		})
	}

	batch := otlpBatch("huge", "first", logspb.SeverityNumber_SEVERITY_NUMBER_INFO, time.Now())
	records := batch.ResourceLogs[0].ScopeLogs[0].LogRecords

	huge := proto.Clone(records[0]).(*logspb.LogRecord)
	huge.Body = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "huge"}}
	huge.Attributes = append(huge.Attributes, &commonpb.KeyValue{
		Key:   "nested",
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_KvlistValue{KvlistValue: &commonpb.KeyValueList{Values: values}}},
	})

	last := proto.Clone(records[0]).(*logspb.LogRecord)
	last.Body = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "last"}}

	batch.ResourceLogs[0].ScopeLogs[0].LogRecords = append(records, huge, last)

	encoded, err := proto.Marshal(batch)
	if nil != err {
		t.Fatal(err)
	}

	response := h.post(t, "/v1/logs", "application/x-protobuf", bytes.NewReader(encoded), nil)
	if http.StatusOK != response.StatusCode {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d: %s", response.StatusCode, raw)
	}

	answer := decodeOTLPAnswer(t, response)
	if "1" != answer.PartialSuccess.RejectedLogRecords {
		t.Errorf("rejected = %q, want \"1\"", answer.PartialSuccess.RejectedLogRecords)
	}
	if !strings.Contains(answer.PartialSuccess.ErrorMessage, "not stored") {
		t.Errorf("error message = %q, want it to say why", answer.PartialSuccess.ErrorMessage)
	}

	stored := h.records(t, "huge")
	if 2 != len(stored) || "first" != stored[0].Msg || "last" != stored[1].Msg {
		t.Errorf("stored %d records, want first and last", len(stored))
	}
}

// A batch stored as sent is acknowledged with an empty partialSuccess.
func TestOTLPAnswersAnEmptyPartialSuccess(t *testing.T) {
	h := newHTTPHarness(t)

	encoded, _ := proto.Marshal(otlpBatch("clean", "x", logspb.SeverityNumber_SEVERITY_NUMBER_INFO, time.Now()))

	response := h.post(t, "/v1/logs", "application/x-protobuf", bytes.NewReader(encoded), nil)

	if raw, _ := io.ReadAll(response.Body); `{"partialSuccess":{}}` != string(raw) {
		t.Errorf("response = %s, want an empty partialSuccess", raw)
	}
}

/*!
 * NaN and the infinities are legal OTLP doubles and have no JSON spelling.
 * Stored as floats they fail the write-ahead log's encoding, the request
 * answers 503, and the collector's retry duplicates every record before them
 * in the batch, forever — so they are kept as text.
 */
func TestOTLPKeepsNonFiniteDoublesAsText(t *testing.T) {
	store, err := logstore.OpenDurable(context.Background(), t.TempDir(), logstore.DurableOptions{Log: quiet()})
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	h := newHTTPHarnessWith(t, ingest.HTTPOptions{Store: store})

	batch := otlpBatch("floats", "x", logspb.SeverityNumber_SEVERITY_NUMBER_INFO, time.Now())
	entry := batch.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	for key, value := range map[string]float64{"nan": math.NaN(), "up": math.Inf(1), "down": math.Inf(-1), "ratio": 0.5} {
		entry.Attributes = append(entry.Attributes, &commonpb.KeyValue{
			Key:   key,
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: value}},
		})
	}

	encoded, _ := proto.Marshal(batch)

	response := h.post(t, "/v1/logs", "application/x-protobuf", bytes.NewReader(encoded), nil)
	if http.StatusOK != response.StatusCode {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d: %s", response.StatusCode, raw)
	}

	records, _ := store.Backlog(context.Background(), "floats", 0)
	if 1 != len(records) {
		t.Fatalf("stored %d records, want 1", len(records))
	}

	for key, want := range map[string]string{"nan": "NaN", "up": "+Inf", "down": "-Inf"} {
		if got := records[0].Fields[key]; model.KindString != got.Kind || want != got.Str {
			t.Errorf("%s = %+v, want the text %q", key, got, want)
		}
	}
	if got := records[0].Fields["ratio"]; model.KindFloat != got.Kind || 0.5 != got.Float {
		t.Errorf("ratio = %+v, want the float 0.5", got)
	}
}

/*!
 * A record OTLP builds itself holds the same invariants as one through the
 * normalizer: severity text in the canonical spelling, so `level>=WARN` finds
 * an exporter that says "WARNING", and an implausible timestamp passed over —
 * here microseconds where nanoseconds belong — rather than filed in 1970.
 */
func TestOTLPCanonicalisesSeverityTextAndTime(t *testing.T) {
	observed := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)

	for text, want := range map[string]string{"WARNING": "WARN", "err": "ERROR", "Information": "INFO"} {
		h := newHTTPHarness(t)

		batch := otlpBatch("canon", "x", logspb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED, time.Now())
		entry := batch.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
		entry.SeverityText = text
		entry.TimeUnixNano = uint64(time.Now().UnixMicro())
		entry.ObservedTimeUnixNano = uint64(observed.UnixNano())

		encoded, _ := proto.Marshal(batch)
		h.post(t, "/v1/logs", "application/x-protobuf", bytes.NewReader(encoded), nil)

		records := h.records(t, "canon")
		if 1 != len(records) {
			t.Fatalf("%s: stored %d records", text, len(records))
		}
		if want != records[0].Level {
			t.Errorf("severity text %q → level %q, want %q", text, records[0].Level, want)
		}
		if !records[0].Ts.Equal(observed) {
			t.Errorf("ts = %s, want the observed time %s", records[0].Ts, observed)
		}
	}
}

// A body past the limit is the sender's to fix by sending less, which is what
// 413 says and 400 does not.
func TestOTLPAnswers413ForABodyOverMaxBody(t *testing.T) {
	h := newHTTPHarnessWith(t, ingest.HTTPOptions{MaxBody: 64})

	encoded, _ := proto.Marshal(otlpBatch("big", strings.Repeat("z", 1024), logspb.SeverityNumber_SEVERITY_NUMBER_INFO, time.Now()))

	response := h.post(t, "/v1/logs", "application/x-protobuf", bytes.NewReader(encoded), nil)
	if http.StatusRequestEntityTooLarge != response.StatusCode {
		t.Errorf("status = %d, want 413", response.StatusCode)
	}
}

func TestOTLPRejectsGarbage(t *testing.T) {
	h := newHTTPHarness(t)

	response := h.post(t, "/v1/logs", "application/json", strings.NewReader("not json at all"), nil)

	if http.StatusBadRequest != response.StatusCode {
		t.Errorf("status = %d, want 400", response.StatusCode)
	}
}

/*!
 * Syslog.
 */
type syslogHarness struct {
	receiver *ingest.Syslog
	store    *logstore.Memory
}

func newSyslogHarness(t *testing.T) *syslogHarness {
	t.Helper()

	store := logstore.NewMemory(1000)

	receiver, err := ingest.ListenSyslog("127.0.0.1", 0, ingest.SyslogOptions{Store: store, Log: quiet()})
	if nil != err {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go receiver.Serve(ctx)

	t.Cleanup(func() {
		cancel()
		receiver.Close()
		store.Close()
	})

	return &syslogHarness{receiver: receiver, store: store}
}

func (h *syslogHarness) sendUDP(t *testing.T, message string) {
	t.Helper()

	conn, err := net.DialUDP("udp4", nil, h.receiver.Addr())
	if nil != err {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.Write([]byte(message))
}

func (h *syslogHarness) wait(t *testing.T, stream string, want int) []*model.Record {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		records, _ := h.store.Backlog(context.Background(), stream, 0)
		if len(records) >= want {
			return records
		}
		time.Sleep(5 * time.Millisecond)
	}

	records, _ := h.store.Backlog(context.Background(), stream, 0)
	t.Fatalf("timed out waiting for %d records on %q, have %d", want, stream, len(records))
	return nil
}

func TestSyslogRFC5424(t *testing.T) {
	h := newSyslogHarness(t)

	// Priority 11 = facility 1 (user), severity 3 (ERROR).
	h.sendUDP(t, `<11>1 2026-08-11T14:22:09.123Z web-01 nginx 4711 ID47 `+
		`[exampleSDID@32473 iut="3" eventSource="Application"] upstream timed out`)

	records := h.wait(t, "nginx", 1)
	rec := records[0]

	if "ERROR" != rec.Level {
		t.Errorf("level = %q, want ERROR", rec.Level)
	}
	if "upstream timed out" != rec.Msg {
		t.Errorf("msg = %q", rec.Msg)
	}
	if "user" != rec.Fields["facility"].Str {
		t.Errorf("facility = %v", rec.Fields["facility"])
	}
	if "web-01" != rec.Fields["hostname"].Str {
		t.Errorf("hostname = %v", rec.Fields["hostname"])
	}
	if "4711" != rec.Fields["pid"].Str {
		t.Errorf("pid = %v", rec.Fields["pid"])
	}

	// Structured data is the one part of syslog carrying real structure.
	if "3" != rec.Fields["exampleSDID@32473.iut"].Str {
		t.Errorf("structured data lost: %v", rec.Fields)
	}
	if "Application" != rec.Fields["exampleSDID@32473.eventSource"].Str {
		t.Errorf("eventSource = %v", rec.Fields["exampleSDID@32473.eventSource"])
	}

	want := time.Date(2026, 8, 11, 14, 22, 9, 123_000_000, time.UTC)
	if !rec.Ts.Equal(want) {
		t.Errorf("ts = %s, want %s", rec.Ts, want)
	}
}

// The severity scale runs the opposite way to everyone else's; getting it
// backwards turns every alert into a debug line, silently.
func TestSyslogSeverityIsNotInverted(t *testing.T) {
	cases := []struct {
		priority int
		want     string
	}{
		{0, "EMERGENCY"},
		{11, "ERROR"},
		{12, "WARN"},
		{14, "INFO"},
		{15, "DEBUG"},
		{191, "DEBUG"},
	}

	for _, tc := range cases {
		h := newSyslogHarness(t)

		h.sendUDP(t, fmt.Sprintf(`<%d>1 - - app - - - message`, tc.priority))

		records := h.wait(t, "app", 1)
		if records[0].Level != tc.want {
			t.Errorf("priority %d → %q, want %q", tc.priority, records[0].Level, tc.want)
		}
	}
}

func TestSyslogRFC3164(t *testing.T) {
	h := newSyslogHarness(t)

	h.sendUDP(t, `<34>Aug 11 14:22:09 web-01 sshd[1234]: Failed password for root`)

	records := h.wait(t, "sshd", 1)
	rec := records[0]

	if "CRITICAL" != rec.Level {
		t.Errorf("level = %q, want CRITICAL", rec.Level)
	}
	if "Failed password for root" != rec.Msg {
		t.Errorf("msg = %q", rec.Msg)
	}
	if "1234" != rec.Fields["pid"].Str {
		t.Errorf("pid = %v", rec.Fields["pid"])
	}
	if "web-01" != rec.Fields["hostname"].Str {
		t.Errorf("hostname = %v", rec.Fields["hostname"])
	}
}

/*!
 * rsyslog's forwarding template puts an RFC3339 stamp where the BSD one goes.
 * Read as BSD, the stamp became the hostname and the hostname part of the tag.
 */
func TestSyslogRFC3164WithAnRFC3339Stamp(t *testing.T) {
	h := newSyslogHarness(t)

	h.sendUDP(t, `<30>2024-05-01T10:00:00.123456+02:00 myhost nginx[123]: GET /`)

	rec := h.wait(t, "nginx", 1)[0]

	if want := time.Date(2024, 5, 1, 8, 0, 0, 123456000, time.UTC); !rec.Ts.Equal(want) {
		t.Errorf("ts = %s, want %s", rec.Ts, want)
	}
	if "myhost" != rec.Fields["hostname"].Str {
		t.Errorf("hostname = %v", rec.Fields["hostname"])
	}
	if "123" != rec.Fields["pid"].Str {
		t.Errorf("pid = %v", rec.Fields["pid"])
	}
	if "GET /" != rec.Msg {
		t.Errorf("msg = %q", rec.Msg)
	}
}

// An escaped quote that ends a value is part of the value.
func TestSyslogUnescapesStructuredDataValues(t *testing.T) {
	h := newSyslogHarness(t)

	h.sendUDP(t, `<11>1 - - sd - - [ex@1 q="say \"hi\"" path="C:\\temp" br="a\]b"] body`)

	rec := h.wait(t, "sd", 1)[0]

	for key, want := range map[string]string{"ex@1.q": `say "hi"`, "ex@1.path": `C:\temp`, "ex@1.br": `a]b`} {
		if want != rec.Fields[key].Str {
			t.Errorf("%s = %q, want %q", key, rec.Fields[key].Str, want)
		}
	}
	if "body" != rec.Msg {
		t.Errorf("msg = %q", rec.Msg)
	}
}

// A device whose clock was never set says it is 1970; it is filed by arrival.
func TestSyslogIgnoresImplausibleTimestamps(t *testing.T) {
	h := newSyslogHarness(t)

	before := time.Now()
	h.sendUDP(t, `<11>1 1970-01-01T00:00:12Z - clockless - - - booted`)

	rec := h.wait(t, "clockless", 1)[0]
	if rec.Ts.Before(before.Add(-time.Second)) || rec.Ts.After(time.Now()) {
		t.Errorf("ts = %s, want the arrival time", rec.Ts)
	}
}

// A Latin-1 device is common, and its bytes must not reach storage as-is.
func TestSyslogReplacesInvalidUTF8(t *testing.T) {
	h := newSyslogHarness(t)

	h.sendUDP(t, "<11>1 - - latin - - - caf\xe9")

	rec := h.wait(t, "latin", 1)[0]
	if "caf\uFFFD" != rec.Msg || !utf8.ValidString(rec.Raw) {
		t.Errorf("msg = %q raw = %q, want the bad byte replaced", rec.Msg, rec.Raw)
	}
}

// Anything without a priority is not syslog, and is dropped rather than
// stored as a mystery line.
func TestSyslogIgnoresNonSyslog(t *testing.T) {
	h := newSyslogHarness(t)

	h.sendUDP(t, "just some text")
	h.sendUDP(t, `<11>1 - - marker - - - real one`)

	h.wait(t, "marker", 1)

	if 0 != h.receiver.Stats().Invalid.Load() {
		// One invalid is expected; assert it was counted rather than silent.
		if 1 != h.receiver.Stats().Invalid.Load() {
			t.Errorf("invalid = %d, want 1", h.receiver.Stats().Invalid.Load())
		}
	}

	streams, _ := h.store.Streams(context.Background())
	for _, stream := range streams {
		if ingest.DefaultSyslogStream == stream {
			records, _ := h.store.Backlog(context.Background(), stream, 0)
			for _, rec := range records {
				if strings.Contains(rec.Raw, "just some text") {
					t.Error("a non-syslog message was stored")
				}
			}
		}
	}
}

/*!
 * TCP, in both framings RFC6587 allows. There is no way to negotiate which, so
 * both have to work on the same listener.
 */
func TestSyslogTCPFramings(t *testing.T) {
	h := newSyslogHarness(t)

	conn, err := net.Dial("tcp", h.receiver.TCPAddr().String())
	if nil != err {
		t.Fatal(err)
	}
	defer conn.Close()

	// Newline-delimited.
	fmt.Fprint(conn, "<11>1 - - tcpapp - - - first line\n")

	// Octet-counted: "<length> <message>".
	message := `<11>1 - - tcpapp - - - second line`
	fmt.Fprintf(conn, "%d %s", len(message), message)

	records := h.wait(t, "tcpapp", 2)

	if "first line" != records[0].Msg {
		t.Errorf("records[0].Msg = %q", records[0].Msg)
	}
	if "second line" != records[1].Msg {
		t.Errorf("records[1].Msg = %q", records[1].Msg)
	}
}

// A message body starting with a digit must not be read as an octet count.
func TestSyslogTCPHandlesDigitLeadingBodies(t *testing.T) {
	h := newSyslogHarness(t)

	conn, err := net.Dial("tcp", h.receiver.TCPAddr().String())
	if nil != err {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprint(conn, "<11>1 - - digits - - - 200 OK from upstream\n")

	records := h.wait(t, "digits", 1)
	if "200 OK from upstream" != records[0].Msg {
		t.Errorf("msg = %q", records[0].Msg)
	}
}

// A connection that ends must take its goroutine with it, rather than leave
// one waiting for a shutdown that may be weeks away.
func TestSyslogTCPConnectionsDoNotLeakGoroutines(t *testing.T) {
	h := newSyslogHarness(t)

	send := func(i int) {
		conn, err := net.Dial("tcp", h.receiver.TCPAddr().String())
		if nil != err {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "<11>1 - - leak - - - %d\n", i)
		conn.Close()
	}

	// One round trip first, so everything the receiver starts once is running.
	send(0)
	h.wait(t, "leak", 1)
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	for i := 1; i <= 50; i++ {
		send(i)
	}
	h.wait(t, "leak", 51)

	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > baseline+5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if n := runtime.NumGoroutine(); n > baseline+5 {
		t.Errorf("%d goroutines after 50 closed connections, %d before", n, baseline)
	}
}

// A line is held to maxLine however long the sender keeps writing it, and the
// next one still starts in the right place.
func TestSyslogTCPTruncatesAnOverlongLine(t *testing.T) {
	h := newSyslogHarness(t)

	conn, err := net.Dial("tcp", h.receiver.TCPAddr().String())
	if nil != err {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprint(conn, "<11>1 - - longline - - - "+strings.Repeat("x", 2*ingest.MaxLineForTest)+"\n")
	fmt.Fprint(conn, "<11>1 - - longline - - - after\n")

	records := h.wait(t, "longline", 2)

	if ingest.MaxLineForTest != len(records[0].Raw) {
		t.Errorf("stored %d bytes of the long line, want %d", len(records[0].Raw), ingest.MaxLineForTest)
	}
	if "after" != records[1].Msg {
		t.Errorf("msg = %q, want the line after the long one", records[1].Msg)
	}
}

// An octet count that never ends is refused after a few digits rather than
// buffered until the sender stops.
func TestSyslogTCPRefusesAnEndlessOctetCount(t *testing.T) {
	h := newSyslogHarness(t)

	conn, err := net.Dial("tcp", h.receiver.TCPAddr().String())
	if nil != err {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprint(conn, strings.Repeat("9", 64))

	assertClosedByPeer(t, conn)

	if 1 != h.receiver.Stats().Invalid.Load() {
		t.Errorf("invalid = %d, want 1", h.receiver.Stats().Invalid.Load())
	}
}

/*!
 * An octet count is the sender's claim, and claiming a megabyte must not cost
 * one. Allocated up front, eight bytes a connection held a megabyte each for
 * as long as the connection stayed open.
 */
func TestSyslogTCPDoesNotAllocateAnOctetCountUpFront(t *testing.T) {
	h := newSyslogHarness(t)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	const connections = 64
	for range connections {
		conn, err := net.Dial("tcp", h.receiver.TCPAddr().String())
		if nil != err {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })

		fmt.Fprintf(conn, "%d <11>1 - - claim - - - ", ingest.MaxLineForTest)
	}

	// Long enough for every count to have been read.
	time.Sleep(300 * time.Millisecond)

	runtime.GC()
	runtime.ReadMemStats(&after)

	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 16<<20 {
		t.Errorf("%d connections that sent a count and a few bytes hold %dMB", connections, grown>>20)
	}
}

// An octet-counted message past maxLine is truncated like a long line is,
// and the connection, with whatever is queued behind it, carries on.
func TestSyslogTCPTruncatesAnOverlongCountedMessage(t *testing.T) {
	h := newSyslogHarness(t)

	conn, err := net.Dial("tcp", h.receiver.TCPAddr().String())
	if nil != err {
		t.Fatal(err)
	}
	defer conn.Close()

	for _, message := range []string{
		"<11>1 - - counted - - - " + strings.Repeat("x", 2*ingest.MaxLineForTest),
		"<11>1 - - counted - - - after",
	} {
		fmt.Fprintf(conn, "%d %s", len(message), message)
	}

	records := h.wait(t, "counted", 2)

	if ingest.MaxLineForTest != len(records[0].Raw) {
		t.Errorf("stored %d bytes of the long message, want %d", len(records[0].Raw), ingest.MaxLineForTest)
	}
	if "after" != records[1].Msg {
		t.Errorf("msg = %q, want the message after the long one", records[1].Msg)
	}
}

// failingListener fails its first accepts the way a process out of file
// descriptors does.
type failingListener struct {
	net.Listener
	failures atomic.Int32
}

func (l *failingListener) Accept() (net.Conn, error) {
	if l.failures.Add(-1) >= 0 {
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EMFILE)}
	}
	return l.Listener.Accept()
}

// listenSyslog binds a receiver and lets prepare adjust it before it serves.
func listenSyslog(t *testing.T, prepare func(*ingest.Syslog)) (*syslogHarness, <-chan error) {
	t.Helper()

	store := logstore.NewMemory(10)

	receiver, err := ingest.ListenSyslog("127.0.0.1", 0, ingest.SyslogOptions{Store: store, Log: quiet()})
	if nil != err {
		t.Fatal(err)
	}
	prepare(receiver)

	ctx, cancel := context.WithCancel(context.Background())

	served := make(chan error, 1)
	go func() { served <- receiver.Serve(ctx) }()

	t.Cleanup(func() {
		cancel()
		receiver.Close()
		store.Close()
	})

	return &syslogHarness{receiver: receiver, store: store}, served
}

/*!
 * A failed accept is waited out, not the end of TCP. Returning on one left
 * the listener bound and nothing reading it: senders connected, and what
 * they sent went nowhere until a restart.
 */
func TestSyslogTCPSurvivesAFailedAccept(t *testing.T) {
	h, served := listenSyslog(t, func(receiver *ingest.Syslog) {
		ingest.WrapTCPForTest(receiver, func(inner net.Listener) net.Listener {
			failing := &failingListener{Listener: inner}
			failing.failures.Store(3)
			return failing
		})
	})

	conn, err := net.Dial("tcp", h.receiver.TCPAddr().String())
	if nil != err {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprint(conn, "<11>1 - - survivor - - - after the flood\n")

	h.wait(t, "survivor", 1)

	select {
	case err := <-served:
		t.Errorf("Serve returned %v, want it still serving", err)
	default:
	}
}

// Past the connection limit a sender waits its turn rather than being served
// at the cost of every other receiver's file descriptors.
func TestSyslogTCPQueuesConnectionsPastTheLimit(t *testing.T) {
	h, _ := listenSyslog(t, func(receiver *ingest.Syslog) {
		ingest.SetMaxConnsForTest(receiver, 1)
	})

	first, err := net.Dial("tcp", h.receiver.TCPAddr().String())
	if nil != err {
		t.Fatal(err)
	}
	defer first.Close()

	fmt.Fprint(first, "<11>1 - - first - - - holding the only slot\n")
	h.wait(t, "first", 1)

	second, err := net.Dial("tcp", h.receiver.TCPAddr().String())
	if nil != err {
		t.Fatal(err)
	}
	defer second.Close()

	fmt.Fprint(second, "<11>1 - - second - - - waiting\n")

	time.Sleep(200 * time.Millisecond)
	if records, _ := h.store.Backlog(context.Background(), "second", 0); 0 != len(records) {
		t.Fatalf("a connection past the limit was served")
	}

	first.Close()
	h.wait(t, "second", 1)
}

// A connection that sends nothing is let go of, rather than held forever.
func TestSyslogTCPClosesAnIdleConnection(t *testing.T) {
	store := logstore.NewMemory(10)

	receiver, err := ingest.ListenSyslog("127.0.0.1", 0, ingest.SyslogOptions{Store: store, Log: quiet()})
	if nil != err {
		t.Fatal(err)
	}
	ingest.SetIdleForTest(receiver, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	go receiver.Serve(ctx)

	t.Cleanup(func() {
		cancel()
		receiver.Close()
		store.Close()
	})

	conn, err := net.Dial("tcp", receiver.TCPAddr().String())
	if nil != err {
		t.Fatal(err)
	}
	defer conn.Close()

	assertClosedByPeer(t, conn)
}

// assertClosedByPeer waits for the receiver to hang up on conn.
func assertClosedByPeer(t *testing.T, conn net.Conn) {
	t.Helper()

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	_, err := conn.Read(make([]byte, 1))

	var netErr net.Error
	if nil == err || (errors.As(err, &netErr) && netErr.Timeout()) {
		t.Errorf("read = %v, want the receiver to have closed the connection", err)
	}
}

/*!
 * RFC3164 carries neither a zone nor a year, and both defaults are decisions
 * that are invisible until they are wrong.
 */
func TestSyslog3164UsesTheReceiversZone(t *testing.T) {
	h := newSyslogHarness(t)

	// The stamp a sender in this machine's own zone would write.
	now := time.Now()
	h.sendUDP(t, fmt.Sprintf(`<34>%s web-01 zoned[1]: hello`, now.Format(time.Stamp)))

	records := h.wait(t, "zoned", 1)

	// Interpreted as UTC instead, this would be off by the local offset —
	// which is zero in a container and hours anywhere else.
	if drift := records[0].Ts.Sub(now); drift > time.Minute || drift < -time.Minute {
		t.Errorf("ts = %s, want within a minute of %s (drift %s)",
			records[0].Ts, now.UTC(), drift)
	}
}

// A message sent on 31 December and received on 1 January is last year's.
func TestSyslog3164HandlesTheYearBoundary(t *testing.T) {
	newYear := time.Date(2027, time.January, 1, 0, 5, 0, 0, time.Local)
	sent := time.Date(2026, time.December, 31, 23, 59, 0, 0, time.Local)

	got := ingest.Resolve3164ForTest(sent, newYear)

	if 2026 != got.In(time.Local).Year() {
		t.Errorf("year = %d, want 2026 — a December message arriving in January is last year's",
			got.In(time.Local).Year())
	}
}
