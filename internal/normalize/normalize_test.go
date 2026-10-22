package normalize

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
)

func TestFromPayloadPromotesRootKeys(t *testing.T) {
	rec := FromPayload("api", "10.0.0.1", 5000, time.Time{}, json.RawMessage(
		`{"level":"error","user_id":4471,"latency_ms":30004.2,"ok":false,"req":{"path":"/v1/x"},"tags":["a","b"],"nothing":null}`,
	))

	if "object" != rec.Type {
		t.Fatalf("type = %q, want object", rec.Type)
	}

	want := map[string]model.Kind{
		"level":      model.KindString,
		"user_id":    model.KindInt,
		"latency_ms": model.KindFloat,
		"ok":         model.KindBool,
		"req":        model.KindJSON,
		"tags":       model.KindJSON,
		"nothing":    model.KindNull,
	}

	for key, kind := range want {
		got, ok := rec.Fields[key]
		if !ok {
			t.Errorf("field %q missing", key)
			continue
		}
		if got.Kind != kind {
			t.Errorf("field %q kind = %v, want %v", key, got.Kind, kind)
		}
	}

	if 4471 != rec.Fields["user_id"].Int {
		t.Errorf("user_id = %d, want 4471", rec.Fields["user_id"].Int)
	}

	if `{"path":"/v1/x"}` != rec.Fields["req"].Str {
		t.Errorf("req = %q, want the original JSON text", rec.Fields["req"].Str)
	}
}

// An id must not come back out of a float column with a decimal point.
func TestIntegersStayIntegers(t *testing.T) {
	rec := FromPayload("api", "", 0, time.Time{}, json.RawMessage(`{"id":9007199254740993,"ratio":1.0}`))

	if model.KindInt != rec.Fields["id"].Kind {
		t.Fatalf("id kind = %v, want int", rec.Fields["id"].Kind)
	}
	if 9007199254740993 != rec.Fields["id"].Int {
		t.Errorf("id = %d, want 9007199254740993", rec.Fields["id"].Int)
	}
	if model.KindFloat != rec.Fields["ratio"].Kind {
		t.Errorf("ratio kind = %v, want float", rec.Fields["ratio"].Kind)
	}
}

func TestLevelNormalization(t *testing.T) {
	cases := []struct {
		payload string
		want    string
	}{
		{`{"level":"warn"}`, "WARN"},
		{`{"level":"WARNING"}`, "WARN"},
		{`{"severity":"Err"}`, "ERROR"},
		{`{"lvl":"panic"}`, "FATAL"},
		{`{"level":30}`, "INFO"},
		{`{"level":50}`, "ERROR"},
		{`{"severity_text":"debug"}`, "DEBUG"},
		// Unknown names survive: an unrecognised level is still a level.
		{`{"level":"audit"}`, "AUDIT"},
		// Nothing level-shaped, and a numeric level outside pino's scale.
		{`{"msg":"hello"}`, ""},
		{`{"level":7}`, ""},
	}

	for _, tc := range cases {
		rec := FromPayload("api", "", 0, time.Time{}, json.RawMessage(tc.payload))
		if rec.Level != tc.want {
			t.Errorf("%s → level %q, want %q", tc.payload, rec.Level, tc.want)
		}
	}
}

func TestMessageExtraction(t *testing.T) {
	cases := []struct{ payload, want string }{
		{`{"msg":"connection reset"}`, "connection reset"},
		{`{"message":"boom"}`, "boom"},
		{`{"short_message":"gelf"}`, "gelf"},
		// Priority order: message beats msg.
		{`{"message":"first","msg":"second"}`, "first"},
	}

	for _, tc := range cases {
		rec := FromPayload("api", "", 0, time.Time{}, json.RawMessage(tc.payload))
		if rec.Msg != tc.want {
			t.Errorf("%s → msg %q, want %q", tc.payload, rec.Msg, tc.want)
		}
	}
}

func TestEventTimeFromPayload(t *testing.T) {
	client := time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.August, 11, 14, 22, 9, 0, time.UTC)
	epoch := want.Unix()

	// One instant, spelled five ways: RFC3339 and epoch at each of the four
	// precisions the magnitude heuristic has to tell apart.
	cases := []string{
		fmt.Sprintf(`{"timestamp":%q}`, want.Format(time.RFC3339)),
		fmt.Sprintf(`{"ts":%d}`, epoch),
		fmt.Sprintf(`{"time":%d}`, epoch*1e3),
		fmt.Sprintf(`{"@timestamp":%d}`, epoch*1e6),
		fmt.Sprintf(`{"ts":%d}`, epoch*1e9),
	}

	for _, payload := range cases {
		rec := FromPayload("api", "", 0, client, json.RawMessage(payload))
		if !rec.Ts.Equal(want) {
			t.Errorf("%s → ts %s, want %s", payload, rec.Ts, want)
		}
	}
}

// A field called `time` holding a duration must not be mistaken for a
// timestamp and drag the record back to 1970.
func TestImplausibleTimestampsAreIgnored(t *testing.T) {
	client := time.Date(2026, time.August, 11, 0, 0, 0, 0, time.UTC)

	for _, payload := range []string{`{"time":500}`, `{"ts":"not a date"}`, `{"time":0}`} {
		rec := FromPayload("api", "", 0, client, json.RawMessage(payload))
		if !rec.Ts.Equal(client) {
			t.Errorf("%s → ts %s, want the client timestamp %s", payload, rec.Ts, client)
		}
	}
}

// The webapp switches on `type`, which v1 computed with JavaScript's typeof.
// Arrays and null are "object" there, and that has to stay true.
func TestTypeMirrorsTypeof(t *testing.T) {
	cases := []struct{ payload, want string }{
		{`{"a":1}`, "object"},
		{`[1,2,3]`, "object"},
		{`null`, "object"},
		{`"hello"`, "string"},
		{`42`, "number"},
		{`true`, "boolean"},
	}

	for _, tc := range cases {
		rec := FromPayload("api", "", 0, time.Time{}, json.RawMessage(tc.payload))
		if rec.Type != tc.want {
			t.Errorf("%s → type %q, want %q", tc.payload, rec.Type, tc.want)
		}
	}
}

// A bare array is typeof "object" but has no root keys to promote.
func TestArraysPromoteNothing(t *testing.T) {
	rec := FromPayload("api", "", 0, time.Time{}, json.RawMessage(`[1,2,"hello"]`))

	if 0 != len(rec.Fields) {
		t.Errorf("fields = %v, want none", rec.Fields)
	}
}

func TestTextPayloadUnwraps(t *testing.T) {
	rec := FromPayload("api", "", 0, time.Time{}, json.RawMessage(`"plain log line"`))

	if "plain log line" != rec.Raw {
		t.Errorf("raw = %q, want the unquoted line", rec.Raw)
	}
	if "plain log line" != rec.Msg {
		t.Errorf("msg = %q, want the line", rec.Msg)
	}
}

func TestFromLineKeepsRawVerbatim(t *testing.T) {
	rec := FromLine("api", "", 0, []byte(`  {"msg":"structured","level":"info"}  `+"\n"))

	if "object" != rec.Type {
		t.Fatalf("type = %q, want object", rec.Type)
	}
	if "structured" != rec.Msg {
		t.Errorf("msg = %q", rec.Msg)
	}
	if "INFO" != rec.Level {
		t.Errorf("level = %q", rec.Level)
	}
	// Raw keeps the original spacing; only the trailing newline goes.
	if `  {"msg":"structured","level":"info"}  ` != rec.Raw {
		t.Errorf("raw = %q, want the original bytes", rec.Raw)
	}
}

func TestFromLineFallsBackToTheWholeLine(t *testing.T) {
	// Valid JSON object, but nothing message-shaped in it.
	rec := FromLine("api", "", 0, []byte(`{"a":1}`))
	if `{"a":1}` != rec.Msg {
		t.Errorf("msg = %q, want the whole line", rec.Msg)
	}

	// Not JSON at all.
	plain := FromLine("api", "", 0, []byte(`{ this is not json`))
	if "string" != plain.Type {
		t.Errorf("type = %q, want string", plain.Type)
	}
	if `{ this is not json` != plain.Msg {
		t.Errorf("msg = %q", plain.Msg)
	}
}

// The wire format is a superset of v1's: the six original keys must survive
// untouched or the existing webapp stops rendering.
func TestWireFormatIsBackwardsCompatible(t *testing.T) {
	rec := FromPayload("api", "10.0.0.1", 5000, time.UnixMilli(1786724529000).UTC(), json.RawMessage(`{"msg":"hi","level":"info"}`))
	rec.Seq = 7

	encoded, err := json.Marshal(rec)
	if nil != err {
		t.Fatal(err)
	}

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); nil != err {
		t.Fatal(err)
	}

	for _, key := range []string{"timestamp", "streamid", "host", "port", "content", "type"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("v1 wire key %q missing", key)
		}
	}

	if `1786724529000` != string(wire["timestamp"]) {
		t.Errorf("timestamp = %s, want epoch millis", wire["timestamp"])
	}
	if `"api"` != string(wire["streamid"]) {
		t.Errorf("streamid = %s", wire["streamid"])
	}
	if `{"msg":"hi","level":"info"}` != string(wire["content"]) {
		t.Errorf("content = %s, want the payload verbatim", wire["content"])
	}
}

// A text line's content must serialize as a JSON string, as v1's did.
func TestWireContentForTextLines(t *testing.T) {
	rec := FromPayload("api", "", 0, time.Now(), json.RawMessage(`"hello \"world\""`))

	encoded, err := json.Marshal(rec)
	if nil != err {
		t.Fatal(err)
	}

	var wire struct {
		Content string `json:"content"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(encoded, &wire); nil != err {
		t.Fatal(err)
	}

	if `hello "world"` != wire.Content {
		t.Errorf("content = %q", wire.Content)
	}
	if "string" != wire.Type {
		t.Errorf("type = %q", wire.Type)
	}
}
