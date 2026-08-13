/*!
 * The v2 record model.
 *
 * A record is a fixed envelope plus a dynamic set of promoted root-level
 * fields — the shape described in docs/proposal-logging-system.md §3. The
 * envelope columns are always present and always the same type; everything
 * else is whatever the log line happened to contain.
 *
 * Value is a compact tagged union rather than `any` on purpose: its Kind maps
 * one-to-one onto the Parquet physical types the P1 writer will emit, so
 * inference happens once, here, and the writer never has to re-examine an
 * interface value per row.
 */

package model

import (
	"encoding/json"
	"strconv"
	"time"
)

// Kind is the inferred type of a promoted field.
type Kind uint8

const (
	KindNull Kind = iota
	KindBool
	KindInt
	KindFloat
	KindString
	// KindJSON is a nested object or array, kept as its original JSON text.
	// These become STRING columns annotated JSON, queried with ->> in P2.
	KindJSON
)

// String names the kind as it appears in the catalog and the API.
func (k Kind) String() string {
	switch k {
	case KindBool:
		return "boolean"
	case KindInt:
		return "int"
	case KindFloat:
		return "float"
	case KindString:
		return "string"
	case KindJSON:
		return "json"
	default:
		return "null"
	}
}

// Value is one promoted field value.
type Value struct {
	Kind  Kind
	Bool  bool
	Int   int64
	Float float64
	// Str holds string values, and for KindJSON the raw JSON text.
	Str string
}

// Null, Bool, Int, Float, Str and JSON build values of each kind.
func Null() Value            { return Value{Kind: KindNull} }
func Bool(v bool) Value      { return Value{Kind: KindBool, Bool: v} }
func Int(v int64) Value      { return Value{Kind: KindInt, Int: v} }
func Float(v float64) Value  { return Value{Kind: KindFloat, Float: v} }
func Str(v string) Value     { return Value{Kind: KindString, Str: v} }
func JSON(text string) Value { return Value{Kind: KindJSON, Str: text} }

// MarshalJSON renders the value as its natural JSON counterpart, so a promoted
// field survives a round trip through the API unchanged.
func (v Value) MarshalJSON() ([]byte, error) {
	switch v.Kind {
	case KindBool:
		return json.Marshal(v.Bool)
	case KindInt:
		return strconv.AppendInt(nil, v.Int, 10), nil
	case KindFloat:
		return json.Marshal(v.Float)
	case KindString:
		return json.Marshal(v.Str)
	case KindJSON:
		return []byte(v.Str), nil
	default:
		return []byte("null"), nil
	}
}

// Text renders the value for full-text matching and for plain-text display.
func (v Value) Text() string {
	switch v.Kind {
	case KindBool:
		return strconv.FormatBool(v.Bool)
	case KindInt:
		return strconv.FormatInt(v.Int, 10)
	case KindFloat:
		return strconv.FormatFloat(v.Float, 'g', -1, 64)
	case KindString, KindJSON:
		return v.Str
	default:
		return ""
	}
}

// Record is one log event.
type Record struct {
	// Ts is event time — from the payload when we could find one, else the
	// moment we received it.
	Ts time.Time
	// IngestTs is when the server received it. Never taken from the client, so
	// it is the one clock we can reason about.
	IngestTs time.Time

	Stream string
	// Seq is monotonic per store. Paired with Ts it gives a unique, totally
	// ordered key — which is what keyset pagination needs in P2.
	Seq uint64

	Host string
	Port int

	// Level is normalized to an upper-case name (INFO, ERROR, ...), empty when
	// the line carried nothing level-shaped.
	Level string
	// Msg is the human-readable part: the extracted message field for JSON
	// lines, the whole line for text ones.
	Msg string

	// Raw is the line as we received it. It is the safety net: any key we
	// failed to promote is still reachable from here.
	Raw string
	// JSON is the parsed payload when the line was valid JSON, else nil.
	JSON json.RawMessage
	// Type mirrors JavaScript's typeof for the payload, which is what the
	// existing webapp switches on to decide how to render a line.
	Type string

	// Fields holds the promoted root-level keys. Only objects promote; a
	// bare array or scalar payload has none.
	Fields map[string]Value
}

// IsObject reports whether the payload was a JSON object, and so whether
// Fields could be populated.
func (r *Record) IsObject() bool { return "object" == r.Type && nil != r.JSON }

/*!
 * Wire format.
 *
 * A superset of what v1's socket.io emitted: the six original keys are
 * untouched so the existing webapp keeps rendering without a change, and the
 * v2 envelope rides alongside for the new UI to pick up in P4.
 */
type wireRecord struct {
	Timestamp int64            `json:"timestamp"`
	StreamID  string           `json:"streamid"`
	Host      string           `json:"host"`
	Port      int              `json:"port"`
	Content   json.RawMessage  `json:"content"`
	Type      string           `json:"type"`
	Seq       uint64           `json:"seq"`
	IngestTs  int64            `json:"ingest_ts"`
	Level     string           `json:"level,omitempty"`
	Msg       string           `json:"msg,omitempty"`
	Fields    map[string]Value `json:"fields,omitempty"`
}

func (r *Record) MarshalJSON() ([]byte, error) {
	content := r.JSON
	if nil == content {
		// A text line's payload is the line itself, as a JSON string.
		encoded, err := json.Marshal(r.Raw)
		if nil != err {
			return nil, err
		}
		content = encoded
	}

	return json.Marshal(wireRecord{
		Timestamp: r.Ts.UnixMilli(),
		StreamID:  r.Stream,
		Host:      r.Host,
		Port:      r.Port,
		Content:   content,
		Type:      r.Type,
		Seq:       r.Seq,
		IngestTs:  r.IngestTs.UnixMilli(),
		Level:     r.Level,
		Msg:       r.Msg,
		Fields:    r.Fields,
	})
}
