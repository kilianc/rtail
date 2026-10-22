package wal_test

import (
	"encoding/json"
	"errors"
	"fmt"
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
