/*!
 * Durability tests.
 *
 * The proposal calls out compaction correctness under crash as where storage
 * engines go to die, and says to budget deterministic simulation rather than
 * hoping. These are that: the process is not really killed, but every
 * interesting interleaving of "WAL written / parquet committed / catalog
 * registered / segment removed" is reproduced by hand and asserted on.
 *
 * The invariant under test everywhere is the same one: **records are never
 * lost**. Duplicates after a crash are acceptable and expected.
 */

package logstore_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/catalog"
	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/normalize"
	"github.com/kilianc/rtail/v2/internal/parquetio"
	"github.com/kilianc/rtail/v2/internal/storage"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func quietOptions(backlog int) logstore.DurableOptions {
	return logstore.DurableOptions{
		Backlog: backlog,
		KeepRaw: true,
		// Never flush or sync on a timer; every test drives them explicitly so
		// nothing depends on wall-clock timing.
		FlushInterval: time.Hour,
		SyncInterval:  -1,
		Grace:         time.Hour,
		Log:           discardLogger(),
	}
}

func openDurable(t *testing.T, dir string, opts logstore.DurableOptions) *logstore.Durable {
	t.Helper()

	store, err := logstore.OpenDurable(context.Background(), dir, opts)
	if nil != err {
		t.Fatalf("OpenDurable: %v", err)
	}

	return store
}

func rec(stream, payload string) *model.Record {
	return normalize.FromLine(stream, "10.0.0.1", 5000, []byte(payload))
}

// readAll returns every record on disk for a stream, via the catalog.
func readAll(t *testing.T, dir, stream string) []*model.Record {
	t.Helper()

	ctx := context.Background()

	backend, err := storage.NewLocal(dir)
	if nil != err {
		t.Fatal(err)
	}

	cat, err := catalog.Open(filepath.Join(backend.Root(), "catalog.sqlite"))
	if nil != err {
		t.Fatal(err)
	}
	defer cat.Close()

	files, err := cat.Prune(ctx, catalog.Query{Stream: stream})
	if nil != err {
		t.Fatal(err)
	}

	var out []*model.Record
	for _, file := range files {
		records, err := parquetio.Read(ctx, backend, file.Path, 0)
		if nil != err {
			t.Fatalf("reading %s: %v", file.Path, err)
		}
		out = append(out, records...)
	}

	return out
}

func rawSet(records []*model.Record) map[string]int {
	seen := map[string]int{}
	for _, rec := range records {
		seen[rec.Raw]++
	}
	return seen
}

func TestRecordsSurviveAFlush(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))

	for i := range 5 {
		if err := store.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d,"msg":"line %d"}`, i, i))); nil != err {
			t.Fatal(err)
		}
	}

	if err := store.Flush(ctx); nil != err {
		t.Fatal(err)
	}
	store.Close()

	on := readAll(t, dir, "api")
	if 5 != len(on) {
		t.Fatalf("read %d records from disk, want 5", len(on))
	}
	for i, r := range on {
		if want := fmt.Sprintf("line %d", i); r.Msg != want {
			t.Errorf("record %d msg = %q, want %q", i, r.Msg, want)
		}
	}
}

/*!
 * The crash that matters most: records in the WAL, nothing flushed. Reopening
 * must replay them into Parquet before anything else happens.
 */
func TestCrashBeforeFlushLosesNothing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	for i := range 20 {
		store.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d}`, i)))
	}

	// Sync the WAL, then abandon the store without flushing — the process
	// dying between a sync and a flush.
	if err := store.SyncForTest(); nil != err {
		t.Fatal(err)
	}
	store.AbandonForTest()

	// Nothing reached Parquet.
	if files := readAll(t, dir, "api"); 0 != len(files) {
		t.Fatalf("found %d records on disk before recovery, want 0", len(files))
	}

	reopened := openDurable(t, dir, quietOptions(10))
	reopened.Close()

	recovered := readAll(t, dir, "api")
	if 20 != len(recovered) {
		t.Fatalf("recovered %d records, want 20", len(recovered))
	}
}

// Recovered records keep the sequence numbers they were given before the
// crash, so nothing shifts underneath a client that had already seen them.
func TestRecoveryPreservesSequence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	for i := range 5 {
		store.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d}`, i)))
	}
	store.SyncForTest()
	store.AbandonForTest()

	reopened := openDurable(t, dir, quietOptions(10))
	reopened.Close()

	recovered := readAll(t, dir, "api")
	for i, r := range recovered {
		if uint64(i+1) != r.Seq {
			t.Errorf("record %d has seq %d, want %d", i, r.Seq, i+1)
		}
	}
}

/*!
 * seq must continue across a restart. Restarting it at 1 would make new
 * records collide with old ones in the (ts, seq) ordering that pagination and
 * backlog de-duplication both depend on.
 */
func TestSequenceContinuesAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	first := openDurable(t, dir, quietOptions(10))
	for i := range 5 {
		first.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d}`, i)))
	}
	first.Flush(ctx)
	first.Close()

	second := openDurable(t, dir, quietOptions(10))

	next := rec("api", `{"after":"restart"}`)
	second.Append(ctx, next)
	second.Flush(ctx)
	second.Close()

	if next.Seq <= 5 {
		t.Errorf("seq restarted at %d; it must continue past the previous 5", next.Seq)
	}

	all := readAll(t, dir, "api")
	seen := map[uint64]bool{}
	for _, r := range all {
		if seen[r.Seq] {
			t.Errorf("duplicate seq %d across the restart", r.Seq)
		}
		seen[r.Seq] = true
	}
}

/*!
 * A crash after the object is committed but before the catalog knows about it.
 *
 * The orphaned file is invisible to queries, and the records come back through
 * the WAL — so nothing is lost, at the cost of a duplicate. That is the trade
 * this design makes on purpose.
 */
func TestCrashBetweenCommitAndRegisterLosesNothing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	for i := range 4 {
		store.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d}`, i)))
	}
	store.SyncForTest()
	store.AbandonForTest()

	// Simulate the orphan: a committed parquet file no catalog row points at.
	backend, err := storage.NewLocal(dir)
	if nil != err {
		t.Fatal(err)
	}
	orphan := "streams/api/2026/08/11/L0-orphan.parquet"
	writer, err := backend.Create(ctx, orphan)
	if nil != err {
		t.Fatal(err)
	}
	writer.Write([]byte("PAR1 not really a parquet file"))
	writer.Commit()

	reopened := openDurable(t, dir, quietOptions(10))
	reopened.Close()

	// Every record is present exactly once, and the orphan was ignored rather
	// than read.
	recovered := readAll(t, dir, "api")
	if 4 != len(recovered) {
		t.Fatalf("recovered %d records, want 4", len(recovered))
	}

	for raw, count := range rawSet(recovered) {
		if 1 != count {
			t.Errorf("record %q appeared %d times", raw, count)
		}
	}
}

/*!
 * A crash after the catalog registered the file but before the WAL segment was
 * removed.
 *
 * Replay rewrites the same object — object names are derived from the batch,
 * not from a clock — and the catalog recognises the path and does nothing. So
 * this recovers exactly once rather than merely without loss. Getting this
 * wrong double-counts the `streams` rollups on every crash, which is how the
 * first version of this failed.
 */
func TestCrashBetweenRegisterAndRemoveIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	for i := range 3 {
		store.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d}`, i)))
	}
	if err := store.FlushKeepingWALForTest(ctx); nil != err {
		t.Fatal(err)
	}
	store.AbandonForTest()

	before := readAll(t, dir, "api")
	if 3 != len(before) {
		t.Fatalf("flushed %d records, want 3", len(before))
	}

	reopened := openDurable(t, dir, quietOptions(10))
	reopened.Close()

	after := readAll(t, dir, "api")

	counts := rawSet(after)
	if 3 != len(counts) {
		t.Fatalf("after recovery there are %d distinct records, want 3", len(counts))
	}
	for raw, count := range counts {
		if 1 != count {
			t.Errorf("record %q appeared %d times, want exactly 1", raw, count)
		}
	}

	// And the stream rollup was not double-counted.
	store2 := openDurable(t, dir, quietOptions(10))
	defer store2.Close()

	streams, err := store2.Catalog().Streams(ctx)
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(streams) {
		t.Fatalf("streams = %v, want 1", streams)
	}
	if 3 != streams[0].RowCount {
		t.Errorf("stream row_count = %d, want 3 — recovery double-counted the rollup", streams[0].RowCount)
	}
}

// A torn final WAL frame is the ordinary crash. Everything before it survives.
func TestTornWALFrameStillRecoversTheRest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	for i := range 10 {
		store.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d}`, i)))
	}
	store.SyncForTest()
	store.AbandonForTest()

	// Chop the tail off the active segment.
	walDir := filepath.Join(dir, "wal")
	entries, err := os.ReadDir(walDir)
	if nil != err {
		t.Fatal(err)
	}

	var segment string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "wal-") {
			segment = entry.Name()
		}
	}
	if "" == segment {
		t.Fatal("no wal segment found")
	}

	path := filepath.Join(walDir, segment)
	body, err := os.ReadFile(path)
	if nil != err {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body[:len(body)-9], 0o644); nil != err {
		t.Fatal(err)
	}

	reopened := openDurable(t, dir, quietOptions(10))
	reopened.Close()

	recovered := readAll(t, dir, "api")

	// The torn record is gone — it was never durable — but everything before
	// it must be there.
	if len(recovered) < 9 {
		t.Errorf("recovered %d records, want at least 9", len(recovered))
	}
	if len(recovered) > 10 {
		t.Errorf("recovered %d records, want at most 10", len(recovered))
	}
}

// Restarting must show history rather than an empty pane.
func TestBacklogIsRestoredFromDisk(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(100))
	for i := range 7 {
		store.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d,"msg":"line %d"}`, i, i)))
	}
	store.Flush(ctx)
	store.Close()

	reopened := openDurable(t, dir, quietOptions(100))
	defer reopened.Close()

	streams, err := reopened.Streams(ctx)
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(streams) || "api" != streams[0] {
		t.Fatalf("streams = %v, want [api]", streams)
	}

	backlog, err := reopened.Backlog(ctx, "api", 0)
	if nil != err {
		t.Fatal(err)
	}
	if 7 != len(backlog) {
		t.Fatalf("backlog = %d records, want 7", len(backlog))
	}
	if "line 0" != backlog[0].Msg || "line 6" != backlog[6].Msg {
		t.Errorf("backlog is out of order: %q .. %q", backlog[0].Msg, backlog[6].Msg)
	}
}

func TestStreamsAreSeparateFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	store.Append(ctx, rec("api", `{"a":1}`))
	store.Append(ctx, rec("worker", `{"b":2}`))
	store.Append(ctx, rec("api", `{"a":3}`))
	store.Flush(ctx)
	store.Close()

	if got := len(readAll(t, dir, "api")); 2 != got {
		t.Errorf("api has %d records, want 2", got)
	}
	if got := len(readAll(t, dir, "worker")); 1 != got {
		t.Errorf("worker has %d records, want 1", got)
	}
}

// Stream ids come from `rtail --id`, which is to say from anywhere at all.
func TestHostileStreamNamesStayInsideTheDataDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))

	for _, name := range []string{"../../etc/passwd", "..", "a/b/c", ""} {
		if err := store.Append(ctx, rec(name, `{"x":1}`)); nil != err {
			t.Fatalf("append to %q: %v", name, err)
		}
	}

	if err := store.Flush(ctx); nil != err {
		t.Fatal(err)
	}
	store.Close()

	// Everything written stayed under the data directory.
	var escaped []string
	filepath.Walk(filepath.Dir(dir), func(path string, info os.FileInfo, err error) error {
		if nil != err || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".parquet") && !strings.HasPrefix(path, dir) {
			escaped = append(escaped, path)
		}
		return nil
	})

	if 0 != len(escaped) {
		t.Errorf("files escaped the data directory: %v", escaped)
	}
}

func TestFlushIsANoopWhenEmpty(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	defer store.Close()

	if err := store.Flush(ctx); nil != err {
		t.Fatal(err)
	}

	backend, _ := storage.NewLocal(dir)
	objects, _ := backend.List(ctx, "streams/")
	if 0 != len(objects) {
		t.Errorf("an empty flush wrote %v", objects)
	}
}

// Reopening an empty directory must work, and must not invent a stream.
func TestOpenOnAFreshDirectory(t *testing.T) {
	ctx := context.Background()

	store := openDurable(t, t.TempDir(), quietOptions(10))
	defer store.Close()

	streams, err := store.Streams(ctx)
	if nil != err {
		t.Fatal(err)
	}
	if 0 != len(streams) {
		t.Errorf("streams = %v, want none", streams)
	}
}

func TestSubscribersSeeLinesFromTheDurableStore(t *testing.T) {
	ctx := context.Background()

	store := openDurable(t, t.TempDir(), quietOptions(10))
	defer store.Close()

	sub := store.Subscribe("api", 16)
	defer sub.Close()

	store.Append(ctx, rec("api", `{"msg":"hello"}`))

	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-sub.C:
			if logstore.EventLine == event.Kind {
				if "hello" != event.Record.Msg {
					t.Errorf("msg = %q", event.Record.Msg)
				}
				return
			}
		case <-deadline:
			t.Fatal("no line event")
		}
	}
}
