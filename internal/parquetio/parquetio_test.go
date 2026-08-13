package parquetio_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/normalize"
	"github.com/kilianc/rtail/v2/internal/parquetio"
	"github.com/kilianc/rtail/v2/internal/storage"
)

func newBackend(t *testing.T) *storage.Local {
	t.Helper()

	backend, err := storage.NewLocal(t.TempDir())
	if nil != err {
		t.Fatal(err)
	}

	return backend
}

// record builds a normalized record from a JSON payload, the way ingest does.
func record(t *testing.T, seq uint64, payload string) *model.Record {
	t.Helper()

	rec := normalize.FromLine("api", "10.0.0.1", 5000, []byte(payload))
	rec.Seq = seq

	return rec
}

func writeAll(t *testing.T, backend storage.Backend, name string, records []*model.Record) *parquetio.Stats {
	t.Helper()

	_, stats, err := parquetio.Write(context.Background(), backend, name, records, parquetio.WriteOptions{KeepRaw: true})
	if nil != err {
		t.Fatalf("Write: %v", err)
	}

	return stats
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	backend := newBackend(t)

	records := []*model.Record{
		record(t, 1, `{"level":"error","msg":"boom","user_id":4471,"latency_ms":30004.2,"ok":false,"req":{"path":"/v1/x"}}`),
		record(t, 2, `{"level":"info","msg":"fine","user_id":12,"latency_ms":3.5,"ok":true,"req":{"path":"/v1/y"}}`),
	}

	stats := writeAll(t, backend, "streams/api/L0-test.parquet", records)

	if 2 != stats.Rows {
		t.Errorf("rows = %d, want 2", stats.Rows)
	}
	if 1 != stats.MinSeq || 2 != stats.MaxSeq {
		t.Errorf("seq range = %d..%d, want 1..2", stats.MinSeq, stats.MaxSeq)
	}
	if stats.Bytes <= 0 {
		t.Errorf("bytes = %d", stats.Bytes)
	}

	back, err := parquetio.Read(ctx, backend, "streams/api/L0-test.parquet", 0)
	if nil != err {
		t.Fatal(err)
	}

	if 2 != len(back) {
		t.Fatalf("read %d records, want 2", len(back))
	}

	first := back[0]

	if "api" != first.Stream {
		t.Errorf("stream = %q", first.Stream)
	}
	if 1 != first.Seq {
		t.Errorf("seq = %d, want 1", first.Seq)
	}
	if "ERROR" != first.Level {
		t.Errorf("level = %q, want ERROR", first.Level)
	}
	if "boom" != first.Msg {
		t.Errorf("msg = %q, want boom", first.Msg)
	}
	if "10.0.0.1" != first.Host {
		t.Errorf("host = %q", first.Host)
	}
	if !first.Ts.Equal(records[0].Ts) {
		t.Errorf("ts = %s, want %s", first.Ts, records[0].Ts)
	}

	// Promoted fields keep their types across the round trip.
	if got := first.Fields["user_id"]; model.KindInt != got.Kind || 4471 != got.Int {
		t.Errorf("user_id = %+v, want int 4471", got)
	}
	if got := first.Fields["latency_ms"]; model.KindFloat != got.Kind || 30004.2 != got.Float {
		t.Errorf("latency_ms = %+v, want float 30004.2", got)
	}
	if got := first.Fields["ok"]; model.KindBool != got.Kind || got.Bool {
		t.Errorf("ok = %+v, want bool false", got)
	}
	if got := first.Fields["req"]; model.KindJSON != got.Kind || `{"path":"/v1/x"}` != got.Str {
		t.Errorf("req = %+v, want the original JSON text", got)
	}
}

// A file is a plain Parquet file with the columns the proposal describes.
func TestSchemaShape(t *testing.T) {
	schema := parquetio.BuildSchema([]*model.Record{
		record(t, 1, `{"user_id":1,"req":{"a":1}}`),
	})

	text := schema.Parquet().String()

	for _, want := range []string{
		"ts", "ingest_ts", "stream", "seq", "level", "msg", "host", "is_json", "raw",
		"a_user_id", "a_req",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("schema is missing %q:\n%s", want, text)
		}
	}

	byKey := map[string]parquetio.Column{}
	for _, column := range schema.Columns() {
		byKey[column.SourceKey] = column
	}

	if got := byKey["user_id"]; "a_user_id" != got.Name || model.KindInt != got.Kind {
		t.Errorf("user_id column = %+v", got)
	}
	if got := byKey["req"]; model.KindJSON != got.Kind {
		t.Errorf("req column = %+v, want json", got)
	}
}

/*!
 * Type inference across a batch, per proposal §3.2.
 */
func TestTypeWidening(t *testing.T) {
	cases := []struct {
		name    string
		payload []string
		key     string
		want    model.Kind
		poly    bool
	}{
		{"all ints", []string{`{"n":1}`, `{"n":2}`}, "n", model.KindInt, false},
		// A value written sometimes as 1 and sometimes as 1.5 is still a
		// number; turning it into text would break range filters.
		{"ints and floats", []string{`{"n":1}`, `{"n":2.5}`}, "n", model.KindFloat, false},
		{"all strings", []string{`{"s":"a"}`, `{"s":"b"}`}, "s", model.KindString, false},
		{"all bools", []string{`{"b":true}`, `{"b":false}`}, "b", model.KindBool, false},
		{"objects", []string{`{"o":{"a":1}}`, `{"o":{"b":2}}`}, "o", model.KindJSON, false},
		{"genuinely mixed", []string{`{"x":1}`, `{"x":"text"}`}, "x", model.KindString, true},
		{"nulls do not count", []string{`{"n":null}`, `{"n":5}`}, "n", model.KindInt, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records := make([]*model.Record, len(tc.payload))
			for i, payload := range tc.payload {
				records[i] = record(t, uint64(i+1), payload)
			}

			schema := parquetio.BuildSchema(records)

			for _, column := range schema.Columns() {
				if column.SourceKey != tc.key {
					continue
				}
				if column.Kind != tc.want {
					t.Errorf("kind = %v, want %v", column.Kind, tc.want)
				}
				if column.Polymorphic != tc.poly {
					t.Errorf("polymorphic = %v, want %v", column.Polymorphic, tc.poly)
				}
				return
			}

			t.Errorf("no column for %q", tc.key)
		})
	}
}

// A key that is only ever null has no type to give it, and a column of nothing
// but nulls is pure overhead.
func TestAlwaysNullKeysAreOmitted(t *testing.T) {
	schema := parquetio.BuildSchema([]*model.Record{
		record(t, 1, `{"nothing":null,"something":1}`),
		record(t, 2, `{"nothing":null}`),
	})

	for _, column := range schema.Columns() {
		if "nothing" == column.SourceKey {
			t.Errorf("always-null key was promoted: %+v", column)
		}
	}
}

// JSON keys can be anything at all, and two of them can mangle to one name.
func TestColumnNameMangling(t *testing.T) {
	schema := parquetio.BuildSchema([]*model.Record{
		record(t, 1, `{"http.status-code":200,"http_status_code":201,"":1,"emoji🎉":2}`),
	})

	names := map[string]bool{}
	keys := map[string]string{}

	for _, column := range schema.Columns() {
		if names[column.Name] {
			t.Errorf("duplicate column name %q", column.Name)
		}
		names[column.Name] = true
		keys[column.SourceKey] = column.Name
	}

	// Every key survives, and the original is recoverable from the column.
	for _, key := range []string{"http.status-code", "http_status_code", "", "emoji🎉"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("key %q was dropped; got %v", key, keys)
		}
	}

	// The two colliding keys land on distinct columns.
	if keys["http.status-code"] == keys["http_status_code"] {
		t.Errorf("colliding keys shared a column: %v", keys)
	}
}

// A producer emitting a unique key per line must not create a 50,000-column
// file. The overflow stays reachable through raw.
func TestColumnCountIsCapped(t *testing.T) {
	var payload strings.Builder
	payload.WriteString(`{"kept":1`)
	for i := range 600 {
		fmt.Fprintf(&payload, `,"k%d":%d`, i, i)
	}
	payload.WriteString("}")

	schema := parquetio.BuildSchema([]*model.Record{record(t, 1, payload.String())})

	if len(schema.Columns()) > 512 {
		t.Errorf("promoted %d columns, want at most 512", len(schema.Columns()))
	}
	if 0 == schema.Dropped() {
		t.Error("dropped count was not reported")
	}
	if 601-512 != schema.Dropped() {
		t.Errorf("dropped = %d, want %d", schema.Dropped(), 601-512)
	}
}

func TestStatsCoverColumns(t *testing.T) {
	backend := newBackend(t)

	stats := writeAll(t, backend, "L0-stats.parquet", []*model.Record{
		record(t, 1, `{"n":5,"maybe":"a"}`),
		record(t, 2, `{"n":1}`),
		record(t, 3, `{"n":9,"maybe":"z"}`),
	})

	byKey := map[string]parquetio.ColumnStats{}
	for _, column := range stats.Columns {
		byKey[column.SourceKey] = column
	}

	n := byKey["n"]
	if !n.HasRange || "1" != n.MinValue || "9" != n.MaxValue {
		t.Errorf("n range = %q..%q (has=%v), want 1..9", n.MinValue, n.MaxValue, n.HasRange)
	}
	if 0 != n.NullCount {
		t.Errorf("n nulls = %d, want 0", n.NullCount)
	}

	maybe := byKey["maybe"]
	if 1 != maybe.NullCount {
		t.Errorf("maybe nulls = %d, want 1", maybe.NullCount)
	}
	if "a" != maybe.MinValue || "z" != maybe.MaxValue {
		t.Errorf("maybe range = %q..%q", maybe.MinValue, maybe.MaxValue)
	}
}

// Text lines have no fields to promote and must survive anyway.
func TestPlainTextLines(t *testing.T) {
	ctx := context.Background()
	backend := newBackend(t)

	records := []*model.Record{
		record(t, 1, `a plain log line`),
		record(t, 2, `another one`),
	}

	writeAll(t, backend, "L0-text.parquet", records)

	back, err := parquetio.Read(ctx, backend, "L0-text.parquet", 0)
	if nil != err {
		t.Fatal(err)
	}

	if 2 != len(back) {
		t.Fatalf("read %d, want 2", len(back))
	}
	if "a plain log line" != back[0].Raw || "string" != back[0].Type {
		t.Errorf("record = %+v", back[0])
	}
}

// Mixed batches are the normal case, not an edge case.
func TestMixedBatch(t *testing.T) {
	ctx := context.Background()
	backend := newBackend(t)

	records := []*model.Record{
		record(t, 1, `plain text`),
		record(t, 2, `{"level":"warn","msg":"structured"}`),
		record(t, 3, `{"totally":"different","keys":1}`),
	}

	writeAll(t, backend, "L0-mixed.parquet", records)

	back, err := parquetio.Read(ctx, backend, "L0-mixed.parquet", 0)
	if nil != err {
		t.Fatal(err)
	}

	if 3 != len(back) {
		t.Fatalf("read %d, want 3", len(back))
	}
	if "WARN" != back[1].Level {
		t.Errorf("level = %q, want WARN", back[1].Level)
	}
	// The first record has none of the third's keys, and reads back as null
	// rather than as an empty string.
	if _, ok := back[0].Fields["keys"]; ok {
		t.Errorf("record 0 gained a field it never had: %v", back[0].Fields)
	}
	if got := back[2].Fields["keys"]; model.KindInt != got.Kind || 1 != got.Int {
		t.Errorf("keys = %+v", got)
	}
}

func TestReadLimitKeepsTheNewest(t *testing.T) {
	ctx := context.Background()
	backend := newBackend(t)

	records := make([]*model.Record, 10)
	for i := range records {
		records[i] = record(t, uint64(i+1), fmt.Sprintf(`{"i":%d}`, i))
	}

	writeAll(t, backend, "L0-limit.parquet", records)

	back, err := parquetio.Read(ctx, backend, "L0-limit.parquet", 3)
	if nil != err {
		t.Fatal(err)
	}

	if 3 != len(back) {
		t.Fatalf("read %d, want 3", len(back))
	}
	if 8 != back[0].Seq || 10 != back[2].Seq {
		t.Errorf("seqs = %d..%d, want 8..10", back[0].Seq, back[2].Seq)
	}
}

// The record must survive a full trip through the wire format too, since that
// is what a restarted server will serve out of a backlog.
func TestRecordsReadBackAreSerializable(t *testing.T) {
	ctx := context.Background()
	backend := newBackend(t)

	writeAll(t, backend, "L0-wire.parquet", []*model.Record{
		record(t, 1, `{"level":"error","msg":"boom","user_id":42}`),
	})

	back, err := parquetio.Read(ctx, backend, "L0-wire.parquet", 0)
	if nil != err {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(back[0])
	if nil != err {
		t.Fatal(err)
	}

	var wire struct {
		StreamID string          `json:"streamid"`
		Type     string          `json:"type"`
		Level    string          `json:"level"`
		Content  json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(encoded, &wire); nil != err {
		t.Fatal(err)
	}

	if "api" != wire.StreamID || "object" != wire.Type || "ERROR" != wire.Level {
		t.Errorf("wire = %+v", wire)
	}
	if !json.Valid(wire.Content) || !strings.Contains(string(wire.Content), `"user_id":42`) {
		t.Errorf("content = %s", wire.Content)
	}
}

func TestWriteRejectsEmptyBatches(t *testing.T) {
	backend := newBackend(t)

	if _, _, err := parquetio.Write(context.Background(), backend, "empty.parquet", nil, parquetio.WriteOptions{}); nil == err {
		t.Error("writing an empty batch was allowed")
	}

	// And left nothing behind.
	objects, _ := backend.List(context.Background(), "")
	if 0 != len(objects) {
		t.Errorf("objects = %v, want none", objects)
	}
}

func TestKeepRawFalseDropsTheOriginal(t *testing.T) {
	ctx := context.Background()
	backend := newBackend(t)

	records := []*model.Record{record(t, 1, `{"msg":"hello","a":1}`)}

	if _, _, err := parquetio.Write(ctx, backend, "L0-noraw.parquet", records, parquetio.WriteOptions{KeepRaw: false}); nil != err {
		t.Fatal(err)
	}

	back, err := parquetio.Read(ctx, backend, "L0-noraw.parquet", 0)
	if nil != err {
		t.Fatal(err)
	}

	if "" != back[0].Raw {
		t.Errorf("raw = %q, want empty", back[0].Raw)
	}
	// The promoted columns are still there — that is the whole point of the
	// trade: half the footprint, unpromoted keys become unreachable.
	if got := back[0].Fields["a"]; model.KindInt != got.Kind {
		t.Errorf("promoted field lost: %+v", got)
	}
	if "hello" != back[0].Msg {
		t.Errorf("msg = %q", back[0].Msg)
	}
}

func TestTimestampPrecisionSurvives(t *testing.T) {
	ctx := context.Background()
	backend := newBackend(t)

	rec := record(t, 1, `{"msg":"x"}`)
	rec.Ts = time.Date(2026, time.August, 11, 14, 22, 9, 123456000, time.UTC)

	writeAll(t, backend, "L0-ts.parquet", []*model.Record{rec})

	back, err := parquetio.Read(ctx, backend, "L0-ts.parquet", 0)
	if nil != err {
		t.Fatal(err)
	}

	if !back[0].Ts.Equal(rec.Ts) {
		t.Errorf("ts = %s, want %s (microsecond precision)", back[0].Ts, rec.Ts)
	}
}
