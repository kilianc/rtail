/*!
 * The OTLP logs receiver.
 *
 *   POST /v1/logs
 *
 * OpenTelemetry over HTTP, in protobuf or JSON. This is the highest-leverage
 * feature in the whole ingest surface: every OTel collector already speaks it,
 * so pointing one at rtail is a config line and no code change anywhere.
 *
 * The mapping onto the envelope is the interesting part. OTLP nests
 * resource → scope → record, and the resource is where the useful identity
 * lives: service.name becomes the stream, so logs land where a person would
 * look for them rather than in one undifferentiated pile.
 */

package ingest

import (
	"encoding/base64"
	stdhex "encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/normalize"
)

/*!
 * severityNames maps OTLP's numeric severity onto our level names.
 *
 * The scale runs 1–24 in four-wide bands, so the band is what matters and the
 * offset within it (INFO2, INFO3, …) is detail nobody filters on. Anything
 * outside the range is left empty rather than guessed at.
 */
func severityName(number int32, text string) string {
	switch {
	case number >= 1 && number <= 4:
		return "TRACE"
	case number >= 5 && number <= 8:
		return "DEBUG"
	case number >= 9 && number <= 12:
		return "INFO"
	case number >= 13 && number <= 16:
		return "WARN"
	case number >= 17 && number <= 20:
		return "ERROR"
	case number >= 21 && number <= 24:
		return "FATAL"
	}

	// No severity number, but a text one is still worth keeping — spelled
	// the way every other receiver spells it, or `level>=WARN` would miss an
	// exporter that says "WARNING".
	return normalize.Level(text)
}

/*!
 * OTLP handles POST /v1/logs.
 */
func (h *HTTP) OTLP(w http.ResponseWriter, r *http.Request) {
	h.stats.Requests.Add(1)

	body, err := h.open(w, r)
	if nil != err {
		h.fail(w, statusOf(err), err)
		return
	}
	defer body.Close()

	raw, err := readAll(body, h.opts.MaxBody)
	if nil != err {
		h.fail(w, statusOf(err), err)
		return
	}

	h.stats.Bytes.Add(uint64(len(raw)))

	/*!
	 * Decoded as LogsData rather than as ExportLogsServiceRequest.
	 *
	 * The two messages are wire-identical — both are `repeated ResourceLogs
	 * resource_logs = 1` — but the request type lives in the collector package,
	 * whose generated gateway stub drags in gRPC and grpc-gateway. That is
	 * megabytes of dependency for a struct definition we only ever unmarshal
	 * into. LogsData is the same bytes from a leaf package.
	 */
	var request logspb.LogsData

	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))

	switch {
	case strings.HasPrefix(contentType, "application/json"):
		// DiscardUnknown, because the OTLP schema gains fields faster than any
		// one implementation adopts them and an unknown field is not a reason
		// to drop a batch.
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &request); nil != err {
			h.fail(w, http.StatusBadRequest, fmt.Errorf("decoding OTLP JSON: %w", err))
			return
		}

		hexIDs(&request)

	default:
		if err := proto.Unmarshal(raw, &request); nil != err {
			h.fail(w, http.StatusBadRequest, fmt.Errorf("decoding OTLP protobuf: %w", err))
			return
		}
	}

	host := remoteHost(r)

	var accepted, rejected, truncated int

	for _, resource := range request.GetResourceLogs() {
		var cutResource bool
		attributes := attributeMap(resource.GetResource().GetAttributes(), &cutResource)
		stream := h.streamOf(attributes)

		for _, scope := range resource.GetScopeLogs() {
			scopeName := scope.GetScope().GetName()

			for _, entry := range scope.GetLogRecords() {
				rec, cut := h.record(entry, attributes, scopeName, stream, host)

				// Refused on its own, before storage can refuse the batch
				// over it — see maxRecord.
				if len(rec.Raw) > maxRecord {
					h.opts.Log.Debug("refusing an over-large OTLP record", "stream", stream, "bytes", len(rec.Raw))
					rejected++
					continue
				}

				if cut || cutResource {
					truncated++
				}

				if err := h.opts.Store.Append(r.Context(), rec); nil != err {
					h.opts.Log.Error("appending an OTLP record", "stream", stream, "err", err)
					h.fail(w, http.StatusServiceUnavailable, fmt.Errorf("storing records: %w", err))
					return
				}

				accepted++
				h.stats.Received.Add(1)
			}
		}
	}

	// Synced before it is acknowledged, for the reason Lines' finish gives:
	// a collector drops its copy of a batch on success.
	if 0 != accepted {
		if err := h.opts.Store.Sync(r.Context()); nil != err {
			h.opts.Log.Error("syncing OTLP records", "err", err)
			h.fail(w, http.StatusServiceUnavailable, fmt.Errorf("storing records: %w", err))
			return
		}
	}

	h.stats.Rejected.Add(uint64(rejected))

	/*!
	 * OTLP expects an ExportLogsServiceResponse, and a collector reads
	 * `partialSuccess` to decide what to send again.
	 *
	 * A record refused for its size is refused for what it is, not for the
	 * state of the server, so it is reported here as rejected — which a
	 * collector takes as "do not retry these" — and the rest of the batch is
	 * acknowledged. A 503 would have it retry the whole batch: every record
	 * before the large one stored again, and the large one refused again,
	 * forever. Truncation is reported in the same place with nothing
	 * rejected, which is how the specification has a server pass on a
	 * warning. An empty object means everything was stored as sent.
	 */
	var (
		answer otlpResponse
		notes  []string
	)

	if 0 != truncated {
		notes = append(notes, fmt.Sprintf("%d log record(s) had a value over %s, truncated to it", truncated, byteCount(maxLine)))
	}
	if 0 != rejected {
		answer.PartialSuccess.RejectedLogRecords = int64(rejected)
		notes = append(notes, fmt.Sprintf("%d log record(s) exceeded %s and were not stored", rejected, byteCount(maxRecord)))
	}
	answer.PartialSuccess.ErrorMessage = strings.Join(notes, "; ")

	// An int and a string cannot fail to encode.
	encoded, _ := json.Marshal(answer)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(encoded)
}

/*!
 * otlpResponse is ExportLogsServiceResponse in OTLP/JSON, which is proto3's
 * JSON mapping: an int64 is written as a string.
 */
type otlpResponse struct {
	PartialSuccess struct {
		RejectedLogRecords int64  `json:"rejectedLogRecords,omitempty,string"`
		ErrorMessage       string `json:"errorMessage,omitempty"`
	} `json:"partialSuccess"`
}

/*!
 * maxRecord bounds one OTLP record, measured as its JSON rendering.
 *
 * Holding the body and every string attribute to maxLine, as the other
 * receivers hold a line, bounds each value but not how many there are, nor
 * an array or map value, which cannot be cut without breaking the JSON it is.
 * And the write-ahead log holds a record at up to six times its rendering —
 * raw and payload, fields and message, with raw and fields escaped a second
 * time — so past the log's 64MB frame the append fails. Failing there
 * answers 503, and every retry stores again each record the batch held
 * before it. Eight lines' worth is 48MB at the worst, inside the frame, and
 * far past any record a logger writes.
 */
const maxRecord = 8 * maxLine

/*!
 * streamOf picks the stream from the resource attributes.
 *
 * service.name is OTLP's own convention for "which program is this", which
 * makes it the natural stream, and it is what a collector sets by default.
 */
func (h *HTTP) streamOf(attributes map[string]model.Value) string {
	for _, key := range []string{"service.name", "service", "k8s.deployment.name", "host.name"} {
		if value, ok := attributes[key]; ok && model.KindString == value.Kind && "" != value.Str {
			return value.Str
		}
	}

	return h.opts.Default
}

// record turns one OTLP log record into ours, reporting whether any of its
// own values had to be truncated.
func (h *HTTP) record(
	entry *logspb.LogRecord,
	resource map[string]model.Value,
	scope, stream, host string,
) (*model.Record, bool) {
	now := time.Now().UTC()

	rec := &model.Record{
		Stream:   stream,
		Host:     host,
		IngestTs: now,
		Level:    severityName(int32(entry.GetSeverityNumber()), entry.GetSeverityText()),
		Type:     "string",
		Fields:   map[string]model.Value{},
	}

	// Observed time is when the collector saw it; the record's own time is
	// when it happened. Prefer the latter, fall back, then to now — passing
	// over either one that is implausible, as FromLine does, so an exporter
	// that writes microseconds into timeUnixNano does not file its records in
	// 1970. Unset is zero, which is implausible too.
	rec.Ts = now
	for _, nanos := range []uint64{entry.GetTimeUnixNano(), entry.GetObservedTimeUnixNano()} {
		if ts := time.Unix(0, int64(nanos)).UTC(); normalize.Plausible(ts) {
			rec.Ts = ts
			break
		}
	}

	// The body is usually a string; when it is not, it is kept as JSON so
	// nothing is lost.
	var cut bool
	body := anyValue(entry.GetBody(), &cut)
	switch body.Kind {
	case model.KindString:
		rec.Msg = body.Str
	case model.KindNull:
		rec.Msg = ""
	default:
		rec.Msg = body.Text()
	}

	// Resource attributes first, so a log attribute of the same name wins —
	// the more specific scope should.
	for key, value := range resource {
		rec.Fields[key] = value
	}
	for key, value := range attributeMap(entry.GetAttributes(), &cut) {
		rec.Fields[key] = value
	}

	if "" != scope {
		rec.Fields["otel.scope"] = model.Str(scope)
	}

	// Trace correlation is the reason to run OTLP at all, so these are
	// promoted rather than left inside the attribute soup.
	if id := entry.GetTraceId(); 0 != len(id) {
		rec.Fields["trace_id"] = model.Str(hex(id))
	}
	if id := entry.GetSpanId(); 0 != len(id) {
		rec.Fields["span_id"] = model.Str(hex(id))
	}

	/*!
	 * Raw is a JSON rendering of the record rather than the original protobuf.
	 *
	 * raw exists so an unpromoted key is still reachable with ->>, which needs
	 * text a JSON path can walk. Protobuf bytes could not serve that, and
	 * keeping them would be storing something nothing can read.
	 */
	rendered := map[string]any{"msg": rec.Msg}
	if "" != rec.Level {
		rendered["level"] = rec.Level
	}
	for key, value := range rec.Fields {
		rendered[key] = value
	}

	if encoded, err := json.Marshal(rendered); nil == err {
		rec.Raw = string(encoded)
		rec.JSON = encoded
		rec.Type = "object"
	} else {
		rec.Raw = rec.Msg
	}

	// Both OTLP decoders already refuse a string that is not UTF-8, so this
	// finds nothing today; it is here so the invariant storage depends on
	// does not rest on a decoder's detail.
	normalize.Sanitize(rec)

	return rec, cut
}

// attributeMap flattens OTLP key-values, setting cut when a value had to be
// truncated.
func attributeMap(attributes []*commonpb.KeyValue, cut *bool) map[string]model.Value {
	out := make(map[string]model.Value, len(attributes))

	for _, attribute := range attributes {
		if nil == attribute {
			continue
		}
		out[attribute.GetKey()] = anyValue(attribute.GetValue(), cut)
	}

	return out
}

/*!
 * anyValue converts OTLP's AnyValue union.
 *
 * Arrays and maps become JSON text, matching how the normalizer treats a
 * nested object on any other path — so `req.path` means the same thing
 * whether the record arrived over OTLP or as a line of JSON.
 *
 * Strings and bytes are held to maxLine, as every other receiver holds a
 * line, and cut is set when one was longer. Those inside an array or a map
 * too, which is the only way to shorten one of those and keep it JSON.
 */
func anyValue(value *commonpb.AnyValue, cut *bool) model.Value {
	if nil == value {
		return model.Null()
	}

	switch inner := value.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		text := inner.StringValue
		if len(text) > maxLine {
			text, *cut = text[:maxLine], true
		}
		return model.Str(text)
	case *commonpb.AnyValue_BoolValue:
		return model.Bool(inner.BoolValue)
	case *commonpb.AnyValue_IntValue:
		return model.Int(inner.IntValue)
	case *commonpb.AnyValue_DoubleValue:
		// NaN and the infinities are legal doubles with no JSON spelling, and
		// the write-ahead log is JSON: stored as a float, one fails the append,
		// the collector retries, and every record before it in the batch is
		// stored again — forever. As text the value survives and the batch
		// goes through.
		if math.IsNaN(inner.DoubleValue) || math.IsInf(inner.DoubleValue, 0) {
			return model.Str(strconv.FormatFloat(inner.DoubleValue, 'g', -1, 64))
		}
		return model.Float(inner.DoubleValue)
	case *commonpb.AnyValue_BytesValue:
		// Two hex digits a byte, so half of maxLine is what fits.
		raw := inner.BytesValue
		if len(raw) > maxLine/2 {
			raw, *cut = raw[:maxLine/2], true
		}
		return model.Str(hex(raw))

	case *commonpb.AnyValue_ArrayValue:
		items := make([]any, 0, len(inner.ArrayValue.GetValues()))
		for _, item := range inner.ArrayValue.GetValues() {
			items = append(items, anyValue(item, cut))
		}
		return jsonOf(items)

	case *commonpb.AnyValue_KvlistValue:
		nested := map[string]any{}
		for key, item := range attributeMap(inner.KvlistValue.GetValues(), cut) {
			nested[key] = item
		}
		return jsonOf(nested)
	}

	return model.Null()
}

func jsonOf(value any) model.Value {
	encoded, err := json.Marshal(value)
	if nil != err {
		return model.Null()
	}
	return model.JSON(string(encoded))
}

/*!
 * hexIDs recovers the trace and span ids an OTLP/JSON body wrote as hex.
 *
 * protojson follows the standard proto3 JSON mapping, where bytes are base64.
 * OTLP/JSON departs from it for exactly these two fields, which the spec
 * writes as hex — the way every tracing UI prints them. Hex digits are base64
 * digits too, so a hex id read as base64 does not even fail: it decodes into
 * different bytes, and every stored trace id is garbage that matches nothing.
 *
 * So the reading is undone after protojson, rather than the body rewritten
 * before it. A trace id is sixteen bytes and a span id eight; their hex is 32
 * and 16 characters, which read as base64 come out as 24 and 12 bytes —
 * lengths no real id has. An id of one of those lengths is encoded back into
 * the text it was read from, and taken as hex when that text is hex. The
 * rewrite this replaces found the fields by name, and missed the snake_case
 * spellings protojson accepts just as well — trace_id under log_records
 * under scope_logs — and it decoded the whole body twice over to do it, at
 * ten times the body's size in memory.
 *
 * An id sent as base64, as exporters written before the spec settled on hex
 * send it, decodes to sixteen or eight bytes and is left alone.
 */
func hexIDs(request *logspb.LogsData) {
	for _, resource := range request.GetResourceLogs() {
		for _, scope := range resource.GetScopeLogs() {
			for _, entry := range scope.GetLogRecords() {
				entry.TraceId = fromHex(entry.GetTraceId(), 16)
				entry.SpanId = fromHex(entry.GetSpanId(), 8)
			}
		}
	}
}

// fromHex returns the size-byte id whose hex protojson read as base64 into
// id, or id itself when that is not what happened.
func fromHex(id []byte, size int) []byte {
	// 2*size hex digits, at six bits a base64 digit.
	if 2*size*6/8 != len(id) {
		return id
	}

	decoded, err := stdhex.DecodeString(base64.StdEncoding.EncodeToString(id))
	if nil != err || size != len(decoded) {
		return id
	}

	return decoded
}

const hexDigits = "0123456789abcdef"

func hex(raw []byte) string {
	out := make([]byte, 0, len(raw)*2)
	for _, b := range raw {
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	return string(out)
}

// readAll reads at most limit bytes, erroring past it rather than truncating
// into something that would fail to parse in a confusing way.
func readAll(reader interface{ Read([]byte) (int, error) }, limit int64) ([]byte, error) {
	buffer := make([]byte, 0, 64<<10)
	chunk := make([]byte, 32<<10)

	for {
		n, err := reader.Read(chunk)
		buffer = append(buffer, chunk[:n]...)

		if int64(len(buffer)) > limit {
			return nil, &tooLargeError{what: "body", limit: limit}
		}

		if nil != err {
			if "EOF" == err.Error() {
				return buffer, nil
			}
			return nil, err
		}
	}
}

func byteCount(n int64) string {
	if n >= 1<<20 {
		return strconv.FormatInt(n>>20, 10) + "MB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}
