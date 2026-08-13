/*!
 * The OTLP logs receiver.
 *
 *   POST /v1/logs
 *
 * OpenTelemetry over HTTP, in protobuf or JSON. This is the highest-leverage
 * feature in the whole ingest surface: every OTel collector already speaks it,
 * so pointing one at rTail is a config line and no code change anywhere.
 *
 * The mapping onto the envelope is the interesting part. OTLP nests
 * resource → scope → record, and the resource is where the useful identity
 * lives: service.name becomes the stream, so logs land where a person would
 * look for them rather than in one undifferentiated pile.
 */

package ingest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/kilianc/rtail/v2/internal/model"
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

	// No severity number, but a text one is still worth keeping.
	if "" != text {
		return strings.ToUpper(strings.TrimSpace(text))
	}

	return ""
}

/*!
 * OTLP handles POST /v1/logs.
 */
func (h *HTTP) OTLP(w http.ResponseWriter, r *http.Request) {
	h.stats.Requests.Add(1)

	body, err := h.open(w, r)
	if nil != err {
		h.fail(w, http.StatusBadRequest, err)
		return
	}
	defer body.Close()

	raw, err := readAll(body, h.opts.MaxBody)
	if nil != err {
		h.fail(w, http.StatusBadRequest, err)
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

	default:
		if err := proto.Unmarshal(raw, &request); nil != err {
			h.fail(w, http.StatusBadRequest, fmt.Errorf("decoding OTLP protobuf: %w", err))
			return
		}
	}

	host := remoteHost(r)

	var out result

	for _, resource := range request.GetResourceLogs() {
		attributes := attributeMap(resource.GetResource().GetAttributes())
		stream := h.streamOf(attributes)

		for _, scope := range resource.GetScopeLogs() {
			scopeName := scope.GetScope().GetName()

			for _, entry := range scope.GetLogRecords() {
				rec := h.record(entry, attributes, scopeName, stream, host)

				if err := h.opts.Store.Append(r.Context(), rec); nil != err {
					h.opts.Log.Error("appending an OTLP record", "stream", stream, "err", err)
					h.fail(w, http.StatusServiceUnavailable, fmt.Errorf("storing records: %w", err))
					return
				}

				out.Accepted++
				h.stats.Received.Add(1)
			}
		}
	}

	/*!
	 * OTLP expects an ExportLogsServiceResponse, and a collector reads
	 * `partialSuccess` to decide whether to retry. An empty object means
	 * everything was accepted, which is the only case that reaches here —
	 * anything else already returned an error status.
	 */
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"partialSuccess":{}}`))
}

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

// record turns one OTLP log record into ours.
func (h *HTTP) record(
	entry *logspb.LogRecord,
	resource map[string]model.Value,
	scope, stream, host string,
) *model.Record {
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
	// when it happened. Prefer the latter, fall back, then to now.
	switch {
	case 0 != entry.GetTimeUnixNano():
		rec.Ts = time.Unix(0, int64(entry.GetTimeUnixNano())).UTC()
	case 0 != entry.GetObservedTimeUnixNano():
		rec.Ts = time.Unix(0, int64(entry.GetObservedTimeUnixNano())).UTC()
	default:
		rec.Ts = now
	}

	// The body is usually a string; when it is not, it is kept as JSON so
	// nothing is lost.
	body := anyValue(entry.GetBody())
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
	for key, value := range attributeMap(entry.GetAttributes()) {
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

	return rec
}

// attributeMap flattens OTLP key-values.
func attributeMap(attributes []*commonpb.KeyValue) map[string]model.Value {
	out := make(map[string]model.Value, len(attributes))

	for _, attribute := range attributes {
		if nil == attribute {
			continue
		}
		out[attribute.GetKey()] = anyValue(attribute.GetValue())
	}

	return out
}

/*!
 * anyValue converts OTLP's AnyValue union.
 *
 * Arrays and maps become JSON text, matching how the normalizer treats a
 * nested object on any other path — so `req.path` means the same thing
 * whether the record arrived over OTLP or as a line of JSON.
 */
func anyValue(value *commonpb.AnyValue) model.Value {
	if nil == value {
		return model.Null()
	}

	switch inner := value.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return model.Str(inner.StringValue)
	case *commonpb.AnyValue_BoolValue:
		return model.Bool(inner.BoolValue)
	case *commonpb.AnyValue_IntValue:
		return model.Int(inner.IntValue)
	case *commonpb.AnyValue_DoubleValue:
		return model.Float(inner.DoubleValue)
	case *commonpb.AnyValue_BytesValue:
		return model.Str(hex(inner.BytesValue))

	case *commonpb.AnyValue_ArrayValue:
		items := make([]any, 0, len(inner.ArrayValue.GetValues()))
		for _, item := range inner.ArrayValue.GetValues() {
			items = append(items, anyValue(item))
		}
		return jsonOf(items)

	case *commonpb.AnyValue_KvlistValue:
		nested := map[string]any{}
		for key, item := range attributeMap(inner.KvlistValue.GetValues()) {
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
			return nil, fmt.Errorf("body exceeds %s", byteCount(limit))
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
