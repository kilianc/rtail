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
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

	store := logstore.NewMemory(1000)
	receiver := ingest.NewHTTP(ingest.HTTPOptions{Store: store, Log: quiet()})

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
