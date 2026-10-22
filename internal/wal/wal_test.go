package wal_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/normalize"
	"github.com/kilianc/rtail/v2/internal/wal"
)

func open(t *testing.T, dir string) *wal.Log {
	t.Helper()

	// Sync explicitly in tests rather than racing a timer.
	log, err := wal.Open(dir, wal.Options{SyncInterval: -1})
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })

	return log
}

func record(seq uint64, payload string) *model.Record {
	rec := normalize.FromLine("api", "10.0.0.1", 5000, []byte(payload))
	rec.Seq = seq
	return rec
}

// drain replays every sealed segment, oldest first.
func drain(t *testing.T, log *wal.Log) []*model.Record {
	t.Helper()

	orphans, err := log.Orphans()
	if nil != err {
		t.Fatal(err)
	}

	var out []*model.Record
	for _, segment := range orphans {
		if err := log.Replay(segment, func(rec *model.Record) error {
			out = append(out, rec)
			return nil
		}); nil != err {
			t.Fatalf("replaying %s: %v", segment, err)
		}
	}

	return out
}

func TestAppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	log := open(t, dir)

	for i := range 5 {
		if err := log.Append(record(uint64(i+1), fmt.Sprintf(`{"i":%d,"msg":"line %d"}`, i, i))); nil != err {
			t.Fatal(err)
		}
	}

	if _, err := log.Rotate(); nil != err {
		t.Fatal(err)
	}

	replayed := drain(t, log)

	if 5 != len(replayed) {
		t.Fatalf("replayed %d records, want 5", len(replayed))
	}
	for i, rec := range replayed {
		if uint64(i+1) != rec.Seq {
			t.Errorf("record %d has seq %d", i, rec.Seq)
		}
	}
}

/*!
 * The encoding must not change the data. model.Record's wire format rounds
 * timestamps to milliseconds and renders 3.0 as `3`; the WAL cannot.
 */
func TestEncodingIsLossless(t *testing.T) {
	dir := t.TempDir()
	log := open(t, dir)

	original := record(1, `{"level":"error","msg":"boom","n":42,"exact":3.0,"flag":false,"nested":{"a":[1,2]}}`)
	original.Ts = time.Date(2026, time.August, 11, 14, 22, 9, 123456000, time.UTC)
	original.IngestTs = time.Date(2026, time.August, 11, 14, 22, 9, 654321000, time.UTC)

	if err := log.Append(original); nil != err {
		t.Fatal(err)
	}
	if _, err := log.Rotate(); nil != err {
		t.Fatal(err)
	}

	replayed := drain(t, log)
	if 1 != len(replayed) {
		t.Fatalf("replayed %d, want 1", len(replayed))
	}

	got := replayed[0]

	// Microsecond precision, not millisecond.
	if !got.Ts.Equal(original.Ts) {
		t.Errorf("ts = %s, want %s", got.Ts, original.Ts)
	}
	if !got.IngestTs.Equal(original.IngestTs) {
		t.Errorf("ingest_ts = %s, want %s", got.IngestTs, original.IngestTs)
	}

	// A float of 3.0 must not come back as an integer.
	if exact := got.Fields["exact"]; model.KindFloat != exact.Kind || 3.0 != exact.Float {
		t.Errorf("exact = %+v, want float 3.0", exact)
	}
	if n := got.Fields["n"]; model.KindInt != n.Kind || 42 != n.Int {
		t.Errorf("n = %+v, want int 42", n)
	}
	if flag := got.Fields["flag"]; model.KindBool != flag.Kind || flag.Bool {
		t.Errorf("flag = %+v, want bool false", flag)
	}
	if nested := got.Fields["nested"]; model.KindJSON != nested.Kind || `{"a":[1,2]}` != nested.Str {
		t.Errorf("nested = %+v", nested)
	}

	if got.Level != original.Level || got.Msg != original.Msg || got.Raw != original.Raw {
		t.Errorf("envelope drifted: %+v", got)
	}
	if !json.Valid(got.JSON) {
		t.Errorf("json = %s", got.JSON)
	}
}

/*!
 * The case that actually happens: the process dies mid-append. Everything
 * before the torn frame is intact and must be recovered; the torn frame never
 * existed and must not be an error.
 */
func TestTornFinalFrameIsRecoverable(t *testing.T) {
	dir := t.TempDir()

	log := open(t, dir)
	for i := range 4 {
		log.Append(record(uint64(i+1), fmt.Sprintf(`{"i":%d}`, i)))
	}
	sealed, err := log.Rotate()
	if nil != err {
		t.Fatal(err)
	}
	log.Close()

	path := filepath.Join(dir, sealed)
	full, err := os.ReadFile(path)
	if nil != err {
		t.Fatal(err)
	}

	// Chop the file at every byte from the header to the end. Each truncation
	// is a plausible crash point, and none of them may error.
	for cut := len(magicLen()); cut < len(full); cut++ {
		if err := os.WriteFile(path, full[:cut], 0o644); nil != err {
			t.Fatal(err)
		}

		reopened := open(t, dir)

		var count int
		err := reopened.Replay(sealed, func(*model.Record) error {
			count++
			return nil
		})
		reopened.Close()

		if nil != err {
			t.Fatalf("truncated at %d bytes: %v", cut, err)
		}
		if count > 4 {
			t.Fatalf("truncated at %d bytes: recovered %d records, more than were written", cut, count)
		}
	}
}

func magicLen() string { return "RTWAL\x01" }

// Damage in the middle of a file is not a torn write, and silently dropping
// records would lose data without telling anyone.
func TestCorruptionMidFileIsReported(t *testing.T) {
	dir := t.TempDir()

	log := open(t, dir)
	for i := range 6 {
		log.Append(record(uint64(i+1), fmt.Sprintf(`{"i":%d,"padding":"aaaaaaaaaaaaaaaaaaaaaaaaa"}`, i)))
	}
	sealed, _ := log.Rotate()
	log.Close()

	path := filepath.Join(dir, sealed)
	full, err := os.ReadFile(path)
	if nil != err {
		t.Fatal(err)
	}

	// Flip a bit in the first record's payload, well before the end.
	corrupted := append([]byte(nil), full...)
	corrupted[len(magicLen())+12] ^= 0xff

	if err := os.WriteFile(path, corrupted, 0o644); nil != err {
		t.Fatal(err)
	}

	reopened := open(t, dir)
	err = reopened.Replay(sealed, func(*model.Record) error { return nil })

	if !errors.Is(err, wal.ErrCorrupt) {
		t.Errorf("err = %v, want ErrCorrupt", err)
	}
}

func TestSegmentsAreOrderedAndRotate(t *testing.T) {
	dir := t.TempDir()
	log := open(t, dir)

	var sealed []string
	for round := range 3 {
		log.Append(record(uint64(round+1), fmt.Sprintf(`{"round":%d}`, round)))

		name, err := log.Rotate()
		if nil != err {
			t.Fatal(err)
		}
		sealed = append(sealed, name)
	}

	orphans, err := log.Orphans()
	if nil != err {
		t.Fatal(err)
	}

	if 3 != len(orphans) {
		t.Fatalf("orphans = %v, want 3", orphans)
	}
	for i, name := range sealed {
		if orphans[i] != name {
			t.Errorf("orphans[%d] = %q, want %q — segments must replay oldest first", i, orphans[i], name)
		}
	}

	// Replay order across segments is append order.
	replayed := drain(t, log)
	for i, rec := range replayed {
		if uint64(i+1) != rec.Seq {
			t.Errorf("record %d has seq %d, want %d", i, rec.Seq, i+1)
		}
	}
}

// Until a segment is gone it is the only durable copy of what it holds.
func TestActiveSegmentCannotBeRemoved(t *testing.T) {
	log := open(t, t.TempDir())

	if err := log.Remove(log.Active()); nil == err {
		t.Error("removing the active segment was allowed")
	}
}

// The GC path is retried after a crash, so removing something already gone is
// not an error.
func TestRemoveIsIdempotent(t *testing.T) {
	log := open(t, t.TempDir())

	log.Append(record(1, `{"a":1}`))
	sealed, _ := log.Rotate()

	if err := log.Remove(sealed); nil != err {
		t.Fatal(err)
	}
	if err := log.Remove(sealed); nil != err {
		t.Errorf("second Remove errored: %v", err)
	}

	orphans, _ := log.Orphans()
	if 0 != len(orphans) {
		t.Errorf("orphans = %v, want none", orphans)
	}
}

/*!
 * Reopening must never append into a segment a previous process left behind:
 * replay would then be racing the writer.
 */
func TestReopenStartsAFreshSegment(t *testing.T) {
	dir := t.TempDir()

	first := open(t, dir)
	first.Append(record(1, `{"first":true}`))
	first.Close()

	firstActive := first.Active()

	second := open(t, dir)
	if second.Active() == firstActive {
		t.Fatalf("reopened into the previous active segment %q", firstActive)
	}

	// The previous active segment is now an orphan, and its records survive.
	orphans, err := second.Orphans()
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(orphans) || orphans[0] != firstActive {
		t.Fatalf("orphans = %v, want [%s]", orphans, firstActive)
	}

	replayed := drain(t, second)
	if 1 != len(replayed) || 1 != replayed[0].Seq {
		t.Errorf("replayed = %+v, want the record from the previous process", replayed)
	}
}

// A segment killed before its header landed holds nothing, and is not damage.
func TestEmptySegmentIsNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	log := open(t, dir)

	path := filepath.Join(dir, "wal-000000000099.log")
	if err := os.WriteFile(path, nil, 0o644); nil != err {
		t.Fatal(err)
	}

	if err := log.Replay("wal-000000000099.log", func(*model.Record) error {
		t.Error("an empty segment yielded a record")
		return nil
	}); nil != err {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestAppendAfterCloseFails(t *testing.T) {
	log := open(t, t.TempDir())
	log.Close()

	if err := log.Append(record(1, `{"a":1}`)); nil == err {
		t.Error("append after close was allowed")
	}
}

func TestPlainTextRecordsSurvive(t *testing.T) {
	dir := t.TempDir()
	log := open(t, dir)

	log.Append(record(1, `a plain line with "quotes" and a \ backslash`))
	log.Rotate()

	replayed := drain(t, log)
	if 1 != len(replayed) {
		t.Fatalf("replayed %d, want 1", len(replayed))
	}
	if `a plain line with "quotes" and a \ backslash` != replayed[0].Raw {
		t.Errorf("raw = %q", replayed[0].Raw)
	}
	if "string" != replayed[0].Type {
		t.Errorf("type = %q", replayed[0].Type)
	}
}

/*!
 * JSON has no spelling for NaN or Infinity, and encoding/json refuses them
 * outright. OTLP carries IEEE doubles, so a value like that is one an exporter
 * can send — and an Append that fails on it drops the record entirely.
 */
func TestNonFiniteFloatsSurvive(t *testing.T) {
	log := open(t, t.TempDir())

	rec := record(1, `{"a":1}`)
	rec.Fields["nan"] = model.Float(math.NaN())
	rec.Fields["up"] = model.Float(math.Inf(1))
	rec.Fields["down"] = model.Float(math.Inf(-1))
	rec.Fields["finite"] = model.Float(2.5)

	if err := log.Append(rec); nil != err {
		t.Fatalf("Append with non-finite floats: %v", err)
	}
	if _, err := log.Rotate(); nil != err {
		t.Fatal(err)
	}

	replayed := drain(t, log)
	if 1 != len(replayed) {
		t.Fatalf("replayed %d, want 1", len(replayed))
	}

	fields := replayed[0].Fields
	if v := fields["nan"]; model.KindFloat != v.Kind || !math.IsNaN(v.Float) {
		t.Errorf("nan = %+v", v)
	}
	if v := fields["up"]; model.KindFloat != v.Kind || !math.IsInf(v.Float, 1) {
		t.Errorf("up = %+v", v)
	}
	if v := fields["down"]; model.KindFloat != v.Kind || !math.IsInf(v.Float, -1) {
		t.Errorf("down = %+v", v)
	}
	if v := fields["finite"]; model.KindFloat != v.Kind || 2.5 != v.Float {
		t.Errorf("finite = %+v", v)
	}
}

// json.Marshal rewrites a RawMessage's <, > and & as \u escapes, so replay
// handed back a payload whose bytes differed from the one appended.
func TestReplayKeepsThePayloadBytes(t *testing.T) {
	log := open(t, t.TempDir())

	original := record(1, `{"q":"a<b && c>d"}`)
	want := string(original.JSON)

	if err := log.Append(original); nil != err {
		t.Fatal(err)
	}
	if _, err := log.Rotate(); nil != err {
		t.Fatal(err)
	}

	replayed := drain(t, log)
	if 1 != len(replayed) {
		t.Fatalf("replayed %d, want 1", len(replayed))
	}
	if got := string(replayed[0].JSON); want != got {
		t.Errorf("json after replay = %s, want %s", got, want)
	}
	if original.Raw != replayed[0].Raw {
		t.Errorf("raw after replay = %q, want %q", replayed[0].Raw, original.Raw)
	}
}

// appendBytes extends a segment as a power loss can: past what was written.
func appendBytes(t *testing.T, path string, tail []byte) {
	t.Helper()

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if nil != err {
		t.Fatal(err)
	}
	defer file.Close()

	if _, err := file.Write(tail); nil != err {
		t.Fatal(err)
	}
}

/*!
 * A filesystem can persist a file's new size before the data that filled it,
 * so after a power loss the tail reads back as zeros. That is a torn write
 * like any other — replay must keep everything before it and boot — rather
 * than corruption that stops the server starting.
 */
func TestZeroFilledTailIsATornWrite(t *testing.T) {
	dir := t.TempDir()
	log := open(t, dir)

	for i := range 3 {
		if err := log.Append(record(uint64(i+1), fmt.Sprintf(`{"i":%d}`, i))); nil != err {
			t.Fatal(err)
		}
	}
	sealed, err := log.Rotate()
	if nil != err {
		t.Fatal(err)
	}

	path := filepath.Join(dir, sealed)
	full, err := os.ReadFile(path)
	if nil != err {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		// The zeros start exactly where the next frame would have.
		"at a frame boundary": append(append([]byte(nil), full...), make([]byte, 4096)...),
		// The zeros start inside the last frame's payload, and run past it.
		"inside a frame": append(append([]byte(nil), full[:len(full)-4]...), make([]byte, 4096)...),
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, body, 0o644); nil != err {
				t.Fatal(err)
			}

			var count int
			if err := log.Replay(sealed, func(*model.Record) error {
				count++
				return nil
			}); nil != err {
				t.Fatalf("replay: %v", err)
			}

			if count < 2 || count > 3 {
				t.Errorf("recovered %d records, want the intact ones before the zeros", count)
			}
		})
	}
}

// A zero-length frame with real data after it is not a torn tail: something
// overwrote the middle of the file, and skipping it would lose what follows.
func TestZeroLengthFrameMidFileIsCorrupt(t *testing.T) {
	dir := t.TempDir()
	log := open(t, dir)

	if err := log.Append(record(1, `{"a":1}`)); nil != err {
		t.Fatal(err)
	}
	sealed, err := log.Rotate()
	if nil != err {
		t.Fatal(err)
	}

	path := filepath.Join(dir, sealed)
	appendBytes(t, path, make([]byte, 8))
	appendBytes(t, path, []byte("not zeros"))

	err = log.Replay(sealed, func(*model.Record) error { return nil })
	if !errors.Is(err, wal.ErrCorrupt) {
		t.Errorf("err = %v, want ErrCorrupt", err)
	}
}

/*!
 * A rotation that fails must leave the log as it was. The store pairs each
 * sealed segment with exactly the batch it holds; a rotation that closed the
 * old file without opening a new one left every later Append failing, and a
 * later rotation sealing a segment the store believed was still active.
 */
func TestFailedRotateChangesNothing(t *testing.T) {
	dir := t.TempDir()
	log := open(t, dir)

	if err := log.Append(record(1, `{"a":1}`)); nil != err {
		t.Fatal(err)
	}

	active := log.Active()

	// The next segment cannot be created in a directory nobody may write to.
	if err := os.Chmod(dir, 0o500); nil != err {
		t.Fatal(err)
	}
	_, err := log.Rotate()
	if err := os.Chmod(dir, 0o755); nil != err {
		t.Fatal(err)
	}
	if nil == err {
		t.Skip("the directory stayed writable; running as root?")
	}

	if log.Active() != active {
		t.Errorf("active segment moved to %s after a failed rotation", log.Active())
	}
	if err := log.Append(record(2, `{"a":2}`)); nil != err {
		t.Fatalf("append after a failed rotation: %v", err)
	}

	sealed, err := log.Rotate()
	if nil != err {
		t.Fatal(err)
	}
	if active != sealed {
		t.Errorf("sealed %s, want the segment that was active all along, %s", sealed, active)
	}

	replayed := drain(t, log)
	if 2 != len(replayed) {
		t.Errorf("replayed %d records, want both", len(replayed))
	}
}

/*!
 * Floor is the first seq a replay of what is on disk could produce: the
 * oldest sealed segment's first record, skipping empty segments and never
 * counting the active one, whose records no file holds yet.
 */
func TestFloorIsTheOldestSealedRecord(t *testing.T) {
	dir := t.TempDir()
	log := open(t, dir)

	floor := func() (uint64, bool) {
		t.Helper()

		seq, ok, err := log.Floor()
		if nil != err {
			t.Fatal(err)
		}
		return seq, ok
	}

	log.Append(record(5, `{"a":5}`))
	if _, ok := floor(); ok {
		t.Error("the active segment counted towards the floor")
	}

	empty, _ := log.Rotate()
	if seq, ok := floor(); !ok || 5 != seq {
		t.Errorf("floor = %d, %v; want 5", seq, ok)
	}

	log.Append(record(6, `{"a":6}`))
	second, _ := log.Rotate()

	log.Remove(empty)
	if seq, ok := floor(); !ok || 6 != seq {
		t.Errorf("floor = %d, %v after removing the oldest; want 6", seq, ok)
	}

	// An empty sealed segment is passed over.
	third, _ := log.Rotate()
	log.Remove(second)
	if _, ok := floor(); ok {
		t.Errorf("an empty segment %s produced a floor", third)
	}
}

/*!
 * A quarantined segment leaves the log — it is never replayed again — but
 * stays on disk, and a second one with the same number does not replace it.
 */
func TestQuarantineMovesASegmentAside(t *testing.T) {
	dir := t.TempDir()
	log := open(t, dir)

	log.Append(record(1, `{"a":1}`))
	sealed, err := log.Rotate()
	if nil != err {
		t.Fatal(err)
	}

	if _, err := log.Quarantine(log.Active()); nil == err {
		t.Error("the active segment was quarantined")
	}

	moved, err := log.Quarantine(sealed)
	if nil != err {
		t.Fatal(err)
	}

	if _, err := os.Stat(moved); nil != err {
		t.Errorf("the quarantined segment is not at %s: %v", moved, err)
	}
	if filepath.Join(dir, "quarantine") != filepath.Dir(moved) {
		t.Errorf("quarantined to %s, want under %s", moved, filepath.Join(dir, "quarantine"))
	}

	orphans, err := log.Orphans()
	if nil != err {
		t.Fatal(err)
	}
	if 0 != len(orphans) {
		t.Errorf("orphans = %v, want none once quarantined", orphans)
	}

	// The next segment of the same number, quarantined in turn, sits beside
	// the first rather than over it.
	if err := os.WriteFile(filepath.Join(dir, sealed), []byte(magicLen()), 0o644); nil != err {
		t.Fatal(err)
	}
	again, err := log.Quarantine(sealed)
	if nil != err {
		t.Fatal(err)
	}
	if again == moved {
		t.Fatalf("a second quarantine of %s replaced the first", sealed)
	}
	if _, err := os.Stat(moved); nil != err {
		t.Errorf("the first quarantined copy is gone: %v", err)
	}
}
