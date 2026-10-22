/*!
 * Durability tests.
 *
 * Compaction correctness under crash is where storage engines go to die, and
 * it wants deterministic simulation rather than hoping. These are that: the
 * process is not really killed, but every
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
	"sync"
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

/*!
 * Appends racing flushes. A record whose WAL frame landed in the segment being
 * sealed, but whose batch insert came after the swap, went into the *next*
 * batch — and its segment was persisted without it and removed. A crash before
 * the following flush lost it.
 */
func TestAppendsRacingFlushesLoseNothing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))

	const writers, per = 8, 3000

	stop := make(chan struct{})
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		for {
			select {
			case <-stop:
				return
			default:
				if err := store.Flush(ctx); nil != err {
					t.Error(err)
				}
			}
		}
	}()

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range per {
				if err := store.Append(ctx, rec("api", fmt.Sprintf(`{"w":%d,"i":%d}`, w, i))); nil != err {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-flushed

	// kill -9 after an fsync: everything the WAL holds is durable.
	if err := store.SyncForTest(); nil != err {
		t.Fatal(err)
	}
	store.AbandonForTest()

	again := openDurable(t, dir, quietOptions(10))
	again.Close()

	recovered := readAll(t, dir, "api")

	seen := rawSet(recovered)
	if got := len(seen); writers*per != got {
		t.Fatalf("recovered %d distinct records, want %d (lost %d)", got, writers*per, writers*per-got)
	}
	if len(recovered) != len(seen) {
		t.Errorf("recovered %d records for %d distinct ones", len(recovered), len(seen))
	}
}

/*!
 * Flushing names a file after the first record of its batch; replay names it
 * after the first record in the segment. Recovery is idempotent only if those
 * are the same record — which needs the batch to hold the segment's records
 * in the segment's order, however many writers are appending.
 */
func TestConcurrentAppendsKeepRecoveryIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))

	const writers, per = 8, 500

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range per {
				stream := fmt.Sprintf("s%d", i%3)
				if err := store.Append(ctx, rec(stream, fmt.Sprintf(`{"w":%d,"i":%d}`, w, i))); nil != err {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()

	// Registered, and then the process dies before the segment is retired.
	if err := store.FlushKeepingWALForTest(ctx); nil != err {
		t.Fatal(err)
	}
	store.AbandonForTest()

	again := openDurable(t, dir, quietOptions(10))
	again.Close()

	total := 0
	for stream := range 3 {
		records := readAll(t, dir, fmt.Sprintf("s%d", stream))
		total += len(records)

		for raw, count := range rawSet(records) {
			if 1 != count {
				t.Errorf("record %s appeared %d times — replay named its file differently from the flush", raw, count)
			}
		}

		// And an L0 file declares seq order, so it must be in it.
		for i := 1; i < len(records); i++ {
			if records[i].Seq <= records[i-1].Seq {
				t.Fatalf("stream s%d: seq %d follows %d", stream, records[i].Seq, records[i-1].Seq)
			}
		}
	}

	if writers*per != total {
		t.Errorf("recovered %d records, want %d", total, writers*per)
	}
}

// Subscribers hear lines in seq order, which the live tail's de-duplication
// against the backlog depends on.
func TestDurablePublishOrderIsSeqOrder(t *testing.T) {
	ctx := context.Background()

	store := openDurable(t, t.TempDir(), quietOptions(10))
	defer store.Close()

	const writers, per = 8, 1000

	sub := store.Subscribe("", writers*per+writers)
	defer sub.Close()

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range per {
				store.Append(ctx, rec(fmt.Sprintf("s%d", w), fmt.Sprintf(`{"i":%d}`, i)))
			}
		}()
	}
	wg.Wait()

	var last uint64
	for seen := 0; seen < writers*per; {
		event := <-sub.C
		if logstore.EventLine != event.Kind {
			continue
		}
		if event.Record.Seq <= last {
			t.Fatalf("seq %d was published after %d", event.Record.Seq, last)
		}
		last = event.Record.Seq
		seen++
	}
}

/*!
 * Every append between crossing the size threshold and the swap sees the
 * batch as full. Each used to start its own flush: tens of thousands of
 * goroutines queued on the flush lock, each writing whatever little had
 * arrived since the last — a storm of tiny files.
 */
func TestSizeTriggerStartsOneFlushAtATime(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	opts := quietOptions(10)
	opts.FlushRows = 1000
	store := openDurable(t, dir, opts)

	const total = 50_000
	for i := range total {
		if err := store.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d}`, i))); nil != err {
			t.Fatal(err)
		}
	}
	store.Close()

	cat, err := catalog.Open(filepath.Join(dir, "catalog.sqlite"))
	if nil != err {
		t.Fatal(err)
	}
	defer cat.Close()

	files, err := cat.Prune(ctx, catalog.Query{Stream: "api"})
	if nil != err {
		t.Fatal(err)
	}

	// A size-triggered flush always takes a full batch; only the one Close
	// makes can be smaller.
	small, rows := 0, int64(0)
	for _, file := range files {
		rows += file.RowCount
		if file.RowCount < int64(opts.FlushRows) {
			small++
		}
	}

	if small > 1 {
		t.Errorf("%d of %d files are below the %d-row threshold, want at most the final one",
			small, len(files), opts.FlushRows)
	}
	if total != rows {
		t.Errorf("files hold %d rows, want %d", rows, total)
	}
}

// failWrites makes a directory unwritable until the returned func runs.
func failWrites(t *testing.T, dir string) func() {
	t.Helper()

	if err := os.Chmod(dir, 0o500); nil != err {
		t.Fatal(err)
	}

	restored := false
	restore := func() {
		if !restored {
			os.Chmod(dir, 0o755)
			restored = true
		}
	}
	t.Cleanup(restore)

	return restore
}

/*!
 * A flush whose Parquet write fails must not drop its batch. The records are
 * then only in the sealed segment: they used to stay unqueryable until a
 * restart replayed it, and now the next flush retries it.
 */
func TestFailedPersistIsRetriedWithoutARestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	defer store.Close()

	store.Append(ctx, rec("api", `{"i":0}`))
	if err := store.Flush(ctx); nil != err {
		t.Fatal(err)
	}

	files, err := store.Catalog().Prune(ctx, catalog.Query{Stream: "api"})
	if nil != err || 1 != len(files) {
		t.Fatalf("setup: files = %v, err %v", files, err)
	}

	// The day directory every file for today lands in.
	restore := failWrites(t, filepath.Join(dir, filepath.Dir(files[0].Path)))

	for i := 1; i <= 3; i++ {
		store.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d}`, i)))
	}
	if err := store.Flush(ctx); nil == err {
		t.Skip("the directory stayed writable; running as root?")
	}

	restore()

	store.Append(ctx, rec("api", `{"i":4}`))
	if err := store.Flush(ctx); nil != err {
		t.Fatalf("flush after the failure cleared: %v", err)
	}

	counts := rawSet(readAll(t, dir, "api"))
	if 5 != len(counts) {
		t.Errorf("queryable records = %d, want all 5 without a restart", len(counts))
	}
	for raw, count := range counts {
		if 1 != count {
			t.Errorf("record %s appeared %d times", raw, count)
		}
	}

	// And every segment that was flushed has been retired.
	segments, err := os.ReadDir(filepath.Join(dir, "wal"))
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(segments) {
		t.Errorf("wal holds %d segments, want only the active one", len(segments))
	}
}

/*!
 * A flush whose WAL rotation fails must not drop its batch either. It used to
 * swap the batch out first, so a failed rotation discarded it — and the next
 * successful flush then sealed and removed the segment those records were in.
 */
func TestFailedRotationKeepsTheBatch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))

	for i := range 3 {
		store.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d}`, i)))
	}

	restore := failWrites(t, filepath.Join(dir, "wal"))
	if err := store.Flush(ctx); nil == err {
		t.Skip("the directory stayed writable; running as root?")
	}
	restore()

	store.Append(ctx, rec("api", `{"i":3}`))
	if err := store.Flush(ctx); nil != err {
		t.Fatalf("flush after the failure cleared: %v", err)
	}

	// Nothing is left to replay: the abandoned store must already have it all.
	store.AbandonForTest()

	if got := len(rawSet(readAll(t, dir, "api"))); 4 != got {
		t.Errorf("records on disk = %d, want 4", got)
	}
}

/*!
 * The backlog is the most recent records by seq — what the ring held before
 * the restart. Picking the file that sorts last by min_ts restored the wrong
 * one whenever a flush full of late-arriving data sorted first.
 */
func TestBacklogIsRestoredBySeq(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(4))

	for i := range 5 {
		store.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d}`, i)))
	}
	store.Flush(ctx)

	// Late data: newer seq, older timestamps.
	for i := 5; i < 10; i++ {
		late := rec("api", fmt.Sprintf(`{"i":%d}`, i))
		late.Ts = time.Now().UTC().Add(-72 * time.Hour)
		store.Append(ctx, late)
	}
	store.Flush(ctx)
	store.Close()

	reopened := openDurable(t, dir, quietOptions(4))
	defer reopened.Close()

	backlog, err := reopened.Backlog(ctx, "api", 0)
	if nil != err {
		t.Fatal(err)
	}

	var seqs []uint64
	for _, r := range backlog {
		seqs = append(seqs, r.Seq)
	}
	if "[7 8 9 10]" != fmt.Sprint(seqs) {
		t.Errorf("restored seqs %v, want the four newest, [7 8 9 10]", seqs)
	}
}

// A restored backlog speaks in source keys, as the live one did.
func TestBacklogRestoreKeepsSourceKeys(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	store.Append(ctx, rec("api", `{"http.status":503,"text":"{}"}`))
	store.Flush(ctx)
	store.Close()

	reopened := openDurable(t, dir, quietOptions(10))
	defer reopened.Close()

	backlog, err := reopened.Backlog(ctx, "api", 0)
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(backlog) {
		t.Fatalf("backlog = %d records, want 1", len(backlog))
	}

	fields := backlog[0].Fields
	if status := fields["http.status"]; model.KindInt != status.Kind || 503 != status.Int {
		t.Errorf("http.status = %+v; fields %v", status, fields)
	}
	if text := fields["text"]; model.KindString != text.Kind || "{}" != text.Str {
		t.Errorf("text = %+v, want the string {}", text)
	}
}

/*!
 * GC collects objects the catalog never registered and temp files from writes
 * that never finished — and nothing else: not registered files, not anything
 * younger than the orphan grace, and not anything older than the catalog,
 * which is somebody's data from a catalog that was lost.
 */
func TestCollectSweepsOrphansButNotData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	defer store.Close()

	store.Append(ctx, rec("api", `{"i":0}`))
	if err := store.Flush(ctx); nil != err {
		t.Fatal(err)
	}

	write := func(name string, modified time.Time) string {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); nil != err {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("PAR1"), 0o644); nil != err {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); nil != err {
			t.Fatal(err)
		}
		return path
	}

	now := time.Now()

	orphan := write("streams/api/2026/10/21/L0-orphan.parquet", now)
	temp := write("streams/api/2026/10/21/.L0-x.parquet.tmp-123", now)
	fresh := write("streams/api/2026/10/21/L0-fresh.parquet", now.Add(47*time.Hour))
	ancient := write("streams/api/2020/01/01/L0-ancient.parquet", now.Add(-365*24*time.Hour))

	// Two days on: past the one-day orphan grace for everything written now.
	if err := store.CollectForTest(ctx, now.Add(48*time.Hour)); nil != err {
		t.Fatal(err)
	}

	for path, want := range map[string]bool{orphan: false, temp: false, fresh: true, ancient: true} {
		_, err := os.Stat(path)
		if got := nil == err; got != want {
			t.Errorf("%s exists = %v, want %v", path, got, want)
		}
	}

	if got := len(readAll(t, dir, "api")); 1 != got {
		t.Errorf("registered records = %d, want 1 — a live file was swept", got)
	}
}

// One tombstoned file that cannot be removed must not stop the rest being
// collected, every pass, forever.
func TestCollectContinuesPastAFailure(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	defer store.Close()

	cat := store.Catalog()

	register := func(name string, seq uint64) int64 {
		id, _, err := cat.Register(ctx, catalog.File{
			Stream: "api", Path: name, Level: catalog.LevelL0,
			MinTs: time.Now(), MaxTs: time.Now(), MinSeq: seq, MaxSeq: seq,
			RowCount: 1, ByteSize: 1,
		}, nil)
		if nil != err {
			t.Fatal(err)
		}
		return id
	}

	// A directory where the object should be: removing it fails.
	stuck := filepath.Join(dir, "streams", "api", "stuck.parquet")
	if err := os.MkdirAll(filepath.Join(stuck, "inside"), 0o755); nil != err {
		t.Fatal(err)
	}
	removable := filepath.Join(dir, "streams", "api", "removable.parquet")
	if err := os.WriteFile(removable, []byte("PAR1"), 0o644); nil != err {
		t.Fatal(err)
	}

	first := register("streams/api/stuck.parquet", 1)
	second := register("streams/api/removable.parquet", 2)

	// Tombstoned in that order, so the stuck one is met first.
	if err := cat.Tombstone(ctx, first); nil != err {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := cat.Tombstone(ctx, second); nil != err {
		t.Fatal(err)
	}

	if err := store.CollectForTest(ctx, time.Now().Add(2*time.Hour)); nil == err {
		t.Error("a pass that could not remove a file reported no error")
	}

	if _, err := os.Stat(removable); !os.IsNotExist(err) {
		t.Errorf("the removable file is still there: %v", err)
	}

	left, err := cat.Collectable(ctx, time.Now().Add(time.Hour))
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(left) || "streams/api/stuck.parquet" != left[0].Path {
		t.Errorf("still collectable = %v, want only the stuck file", left)
	}
}
