/*!
 * Turning an incoming line into a model.Record.
 *
 * This is the envelope extraction step: pull level, message and event time
 * out of whatever the payload happens to call them, promote every root-level
 * key to its own field, and
 * keep the original around so nothing we guessed wrong about is ever lost.
 *
 * The line-shaped receivers (HTTP, UDP) funnel through FromLine and
 * FromPayload. OTLP and syslog carry their own structure and build records
 * themselves, so the invariants every stored record must hold are exported
 * for them too — Level, Plausible and Sanitize — and the storage engine and
 * the query planner only ever see one shape regardless of how a line arrived.
 */

package normalize

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

	// The client's clock gets the same scrutiny as a timestamp in the payload:
	// a v1 client may have lifted it out of the line itself, and one that
	// sends seconds where milliseconds belong would file the record in 1970.
	if !Plausible(rec.Ts) {
		rec.Ts = ingest
	}

	// A JSON string payload is a text line that happened to survive the trip
	// quoted; unwrap it so Raw reads like the line the user actually printed.
	if "string" == rec.Type {
		var text string
		if err := json.Unmarshal(content, &text); nil == err {
			rec.Raw = text
			rec.Msg = text
			Sanitize(rec)
			return rec
		}
	}

	rec.Raw = string(content)
	rec.JSON = content
	enrich(rec)
	Sanitize(rec)

	return rec
}

/*!
 * FromLine builds a record from an original, unparsed line.
 *
 * Used by the HTTP receiver, where we still have the bytes as they were
 * written and Raw can be exact.
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
		Sanitize(rec)
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

	Sanitize(rec)

	return rec
}

/*!
 * Sanitize replaces invalid UTF-8 in every string a record carries.
 *
 * Parquet strings are UTF-8 by definition and DuckDB checks: one Latin-1 byte
 * in one stored line makes the whole file unreadable — "Invalid string
 * encoding found in Parquet file" — and with it every search that touches
 * the file. Such bytes arrive routinely, from a Latin-1 syslog device or a
 * program printing a binary buffer, and json.Valid does not catch them
 * inside a string. Replaced with U+FFFD here, the damage is one character of
 * a line that was already mangled; stored as they came, it is the file.
 *
 * The JSON payload stays valid JSON: a byte that is not ASCII can only sit
 * inside a string in text json.Valid accepted, and a replacement character
 * is just as legal there.
 */
func Sanitize(rec *model.Record) {
	rec.Stream = validUTF8(rec.Stream)
	rec.Host = validUTF8(rec.Host)
	rec.Level = validUTF8(rec.Level)
	rec.Msg = validUTF8(rec.Msg)
	rec.Raw = validUTF8(rec.Raw)

	if !utf8.Valid(rec.JSON) {
		rec.JSON = bytes.ToValidUTF8(rec.JSON, []byte(replacement))
	}

	var renamed []string

	for key, value := range rec.Fields {
		if model.KindString == value.Kind || model.KindJSON == value.Kind {
			if clean := validUTF8(value.Str); clean != value.Str {
				value.Str = clean
				rec.Fields[key] = value
			}
		}

		// Keys become column names, which are strings in the file too.
		if !utf8.ValidString(key) {
			renamed = append(renamed, key)
		}
	}

	/*!
	 * Two keys that differ only in their bad bytes clean to the same name —
	 * a syslog device's structured data reaches here with whatever bytes it
	 * likes — and renamed in map order, whichever came last won, at random.
	 * So keys are renamed in sorted order, and when the clean name is taken
	 * the later key gets it with a number after it: "a\uFFFD", then
	 * "a\uFFFD~2". A key that was valid as it came is never displaced.
	 * Numbered rather than dropped, because a field gone from a record is a
	 * filter that silently stops matching it.
	 */
	slices.Sort(renamed)

	for _, key := range renamed {
		value := rec.Fields[key]
		delete(rec.Fields, key)

		base := validUTF8(key)
		name := base

		for n := 2; ; n++ {
			if _, taken := rec.Fields[name]; !taken {
				break
			}
			name = base + "~" + strconv.Itoa(n)
		}

		rec.Fields[name] = value
	}
}

const replacement = "\uFFFD"

// validUTF8 returns s unchanged, without allocating, when it is already valid.
func validUTF8(s string) string { return strings.ToValidUTF8(s, replacement) }

/*!
 * enrich promotes root keys and lifts level, message and event time out of an
 * object payload. Records whose payload is not an object pass through
 * untouched: there are no root keys to promote.
 */
func enrich(rec *model.Record) {
	lowered, ok := Promote(rec)
	if !ok {
		return
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

/*!
 * Promote fills a record's Fields from its JSON payload, leaving the envelope
 * alone.
 *
 * Split out from enrich because the read path needs it on its own: a record
 * reconstructed from Parquet already has its level, message and event time as
 * stored columns, and re-deriving those would let a later change to the
 * extraction rules silently rewrite history. Promoting the root keys, on the
 * other hand, is a pure function of the payload and is how a searched record
 * gets the fields the explorer and click-to-filter need.
 *
 * Returns the lower-cased key index enrich uses for its case-insensitive
 * lookups, and false when there was nothing to promote.
 */
func Promote(rec *model.Record) (map[string]string, bool) {
	if "object" != rec.Type || nil == rec.JSON {
		return nil, false
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(rec.JSON, &root); nil != err {
		// Arrays and null are typeof "object" too, and neither has root keys.
		return nil, false
	}

	rec.Fields = make(map[string]Value, len(root))
	lowered := make(map[string]string, len(root))

	for key, raw := range root {
		rec.Fields[key] = classify(raw)
		lowered[strings.ToLower(key)] = key
	}

	return lowered, true
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
 * Scalars become their natural type; objects and arrays are kept as JSON
 * text. Integers are distinguished from floats by the absence of a
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

// Level is the canonical name for a level as written — "warning" is WARN, as
// it is at ingest. Anything that compares a level a person typed against
// stored ones has to go through this, or `level=warning` finds nothing.
func Level(name string) string { return normalizeLevel(model.Str(name)) }

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
				return ts.UTC(), Plausible(ts)
			}
		}
		return time.Time{}, false

	case model.KindInt:
		return fromEpoch(value.Int)

	case model.KindFloat:
		// Fractional epoch seconds, as Python and Ruby emit.
		seconds, frac := int64(value.Float), value.Float-float64(int64(value.Float))
		ts := time.Unix(seconds, int64(frac*1e9)).UTC()
		return ts, Plausible(ts)
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
	return ts, Plausible(ts)
}

// Plausible reports whether ts could be a log record's event time, by the
// window described at tsFloor. A receiver that reads a timestamp from the
// wire itself checks it here and falls back to the time it received the
// record, as FromLine does, rather than storing whatever it was handed.
func Plausible(ts time.Time) bool {
	return ts.After(tsFloor) && ts.Before(time.Now().Add(tsRoof))
}
