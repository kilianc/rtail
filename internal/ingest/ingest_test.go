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
	"runtime"
	"strings"
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

	// The blank line is skipped, not counted or rejected.
	if 3 != out.Accepted || 0 != out.Rejected {
		t.Fatalf("accepted %d rejected %d, want 3 and 0", out.Accepted, out.Rejected)
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
 * A 200 means the records are durable, so an empty batch must not claim to
 * have stored anything.
 */
func TestIngestOfNothingIsSuccessfulAndEmpty(t *testing.T) {
	h := newHTTPHarness(t)

	response := h.post(t, "/v1/ingest?stream=empty", "application/x-ndjson", strings.NewReader("\n\n  \n"), nil)

	if http.StatusOK != response.StatusCode {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	var out struct {
		Accepted int `json:"accepted"`
	}
	json.NewDecoder(response.Body).Decode(&out)

	if 0 != out.Accepted {
		t.Errorf("accepted = %d, want 0", out.Accepted)
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
