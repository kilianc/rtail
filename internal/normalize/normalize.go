/*!
 * Turning an incoming line into a model.Record.
 *
 * This is the envelope extraction step from docs/proposal-logging-system.md
 * §3.1–3.2: pull level, message and event time out of whatever the payload
 * happens to call them, promote every root-level key to its own field, and
 * keep the original around so nothing we guessed wrong about is ever lost.
 *
 * Every receiver funnels through here, so the storage engine and the query
 * planner only ever see one shape regardless of how a line arrived.
 */

package normalize

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
)

/*!
 * Field names we look for, in priority order.
 *
 * Matching is case-insensitive. The lists are deliberately short: every extra
 * candidate is a chance to promote something that only looks like a message,
 * and a wrong guess here is visible on every single line.
 */
var (
	levelKeys = []string{"level", "severity", "severity_text", "severitytext", "lvl", "loglevel", "log_level", "@l"}
	msgKeys   = []string{"message", "msg", "text", "short_message", "@m"}
	tsKeys    = []string{"timestamp", "ts", "time", "@timestamp", "eventtime", "datetime"}
)

// Canonical level names, keyed by lower-cased input.
var levelNames = map[string]string{
	"trace": "TRACE", "verbose": "TRACE",
	"debug": "DEBUG", "dbg": "DEBUG",
	"info": "INFO", "information": "INFO", "informational": "INFO",
	"notice": "NOTICE",
	"warn":   "WARN", "warning": "WARN",
	"error": "ERROR", "err": "ERROR",
	"crit": "CRITICAL", "critical": "CRITICAL",
	"alert": "ALERT",
	"emerg": "EMERGENCY", "emergency": "EMERGENCY",
	"fatal": "FATAL", "panic": "FATAL",
}

// Numeric levels as emitted by pino and bunyan, which between them cover most
// of the JSON-logging world. Syslog's 0-7 scale runs the other way and is not
// distinguishable from these without more context, so it is left alone rather
// than guessed at — a wrongly inverted severity is worse than a missing one.
var pinoLevels = map[int64]string{
	10: "TRACE", 20: "DEBUG", 30: "INFO", 40: "WARN", 50: "ERROR", 60: "FATAL",
}

// Timestamps outside this window are assumed not to be timestamps at all — a
// field called `time` holding a duration of 500 would otherwise land the record
// in 1970 and wreck the ordering of everything around it.
var (
	tsFloor = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	tsRoof  = 365 * 24 * time.Hour
)

/*!
 * FromPayload builds a record from an already-parsed payload.
 *
 * This is the v1 UDP path: the npm client parses the line with JSON5 and sends
 * the result, so by the time it reaches us the original text is already gone.
 * Raw is therefore a faithful reconstruction rather than the true source line.
 * Receivers that do get the original bytes should call FromLine instead.
 */
func FromPayload(stream, host string, port int, clientTs time.Time, content json.RawMessage) *model.Record {
	ingest := time.Now().UTC()

	rec := &model.Record{
		Stream:   stream,
		Host:     host,
		Port:     port,
		Ts:       clientTs,
		IngestTs: ingest,
		Type:     typeOf(content),
	}

	if rec.Ts.IsZero() {
		rec.Ts = ingest
	}

	// A JSON string payload is a text line that happened to survive the trip
	// quoted; unwrap it so Raw reads like the line the user actually printed.
	if "string" == rec.Type {
		var text string
		if err := json.Unmarshal(content, &text); nil == err {
			rec.Raw = text
			rec.Msg = text
			return rec
		}
	}

	rec.Raw = string(content)
	rec.JSON = content
	enrich(rec)

	return rec
}

/*!
 * FromLine builds a record from an original, unparsed line.
 *
 * Used by every v2 receiver (HTTP, syslog, OTLP), where we still have the
 * bytes as they were written and Raw can be exact.
 */
func FromLine(stream, host string, port int, raw []byte) *model.Record {
	ingest := time.Now().UTC()
	text := strings.TrimRight(string(raw), "\r\n")

	rec := &model.Record{
		Stream:   stream,
		Host:     host,
		Port:     port,
		Ts:       ingest,
		IngestTs: ingest,
		Raw:      text,
		Type:     "string",
		Msg:      text,
	}

	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") || !json.Valid([]byte(trimmed)) {
		return rec
	}

	rec.JSON = json.RawMessage(trimmed)
	rec.Type = "object"
	rec.Msg = ""
	enrich(rec)

	// Nothing message-shaped in the object — fall back to the whole line, so a
	// structured record without a recognised message field still displays.
	if "" == rec.Msg {
		rec.Msg = text
	}

	return rec
}

/*!
 * enrich promotes root keys and lifts level, message and event time out of an
 * object payload. Records whose payload is not an object pass through
 * untouched: there are no root keys to promote.
 */
func enrich(rec *model.Record) {
	if "object" != rec.Type || nil == rec.JSON {
		return
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(rec.JSON, &root); nil != err {
		// Arrays and null are typeof "object" too, and neither has root keys.
		return
	}

	rec.Fields = make(map[string]Value, len(root))
	lowered := make(map[string]string, len(root))

	for key, raw := range root {
		rec.Fields[key] = classify(raw)
		lowered[strings.ToLower(key)] = key
	}

	if value, ok := pick(rec.Fields, lowered, levelKeys); ok {
		rec.Level = normalizeLevel(value)
	}

	if value, ok := pick(rec.Fields, lowered, msgKeys); ok {
		if model.KindString == value.Kind {
			rec.Msg = value.Str
		} else {
			rec.Msg = value.Text()
		}
	}

	if value, ok := pick(rec.Fields, lowered, tsKeys); ok {
		if ts, ok := parseTimestamp(value); ok {
			rec.Ts = ts
		}
	}
}

// Value is re-exported so callers of this package do not need to import model
// just to read a promoted field.
type Value = model.Value

// pick returns the first field matching a candidate name, case-insensitively.
func pick(fields map[string]Value, lowered map[string]string, candidates []string) (Value, bool) {
	for _, candidate := range candidates {
		if key, ok := lowered[candidate]; ok {
			value := fields[key]
			if model.KindNull != value.Kind {
				return value, true
			}
		}
	}
	return Value{}, false
}

/*!
 * classify infers a field's type from its JSON text.
 *
 * Per §3.2: scalars become their natural type, objects and arrays are kept as
 * JSON text. Integers are distinguished from floats by the absence of a
 * decimal point or exponent, so an id of 4471 stays an integer and does not
 * come back out of a float column as 4471.0.
 */
func classify(raw json.RawMessage) Value {
	trimmed := strings.TrimSpace(string(raw))
	if "" == trimmed {
		return model.Null()
	}

	switch trimmed[0] {
	case '{', '[':
		return model.JSON(trimmed)
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); nil != err {
			return model.Null()
		}
		return model.Str(text)
	case 't':
		return model.Bool(true)
	case 'f':
		return model.Bool(false)
	case 'n':
		return model.Null()
	}

	if !strings.ContainsAny(trimmed, ".eE") {
		if n, err := strconv.ParseInt(trimmed, 10, 64); nil == err {
			return model.Int(n)
		}
	}

	if f, err := strconv.ParseFloat(trimmed, 64); nil == err {
		return model.Float(f)
	}

	return model.Null()
}

// typeOf mirrors JavaScript's typeof for a JSON payload, which is what the v1
// wire format carried and what the existing webapp switches on. Note that both
// arrays and null are "object" there — faithfully reproduced here.
func typeOf(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if "" == trimmed {
		return "undefined"
	}

	switch trimmed[0] {
	case '{', '[', 'n':
		return "object"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	default:
		return "number"
	}
}

// normalizeLevel maps a level field onto a canonical name. Unrecognised
// strings are upper-cased and kept: an unknown level is still a level, and
// dropping it would hide it from the severity filter entirely.
func normalizeLevel(value Value) string {
	switch value.Kind {
	case model.KindString:
		trimmed := strings.TrimSpace(value.Str)
		if "" == trimmed {
			return ""
		}
		if name, ok := levelNames[strings.ToLower(trimmed)]; ok {
			return name
		}
		return strings.ToUpper(trimmed)

	case model.KindInt:
		if name, ok := pinoLevels[value.Int]; ok {
			return name
		}
	}

	return ""
}

// parseTimestamp reads an event time from a promoted field, accepting RFC3339
// text and epoch numbers at any of the four common precisions.
func parseTimestamp(value Value) (time.Time, bool) {
	switch value.Kind {
	case model.KindString:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
			if ts, err := time.Parse(layout, value.Str); nil == err {
				return ts.UTC(), plausible(ts)
			}
		}
		return time.Time{}, false

	case model.KindInt:
		return fromEpoch(value.Int)

	case model.KindFloat:
		// Fractional epoch seconds, as Python and Ruby emit.
		seconds, frac := int64(value.Float), value.Float-float64(int64(value.Float))
		ts := time.Unix(seconds, int64(frac*1e9)).UTC()
		return ts, plausible(ts)
	}

	return time.Time{}, false
}

// fromEpoch guesses the precision of an epoch number by magnitude.
func fromEpoch(n int64) (time.Time, bool) {
	var ts time.Time

	switch {
	case n < 1e11:
		ts = time.Unix(n, 0)
	case n < 1e14:
		ts = time.UnixMilli(n)
	case n < 1e17:
		ts = time.UnixMicro(n)
	default:
		ts = time.Unix(0, n)
	}

	ts = ts.UTC()
	return ts, plausible(ts)
}

func plausible(ts time.Time) bool {
	return ts.After(tsFloor) && ts.Before(time.Now().Add(tsRoof))
}
