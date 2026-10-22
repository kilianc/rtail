/*!
 * Recovery when part of it cannot happen.
 *
 * The durability tests cover the crash windows of a flush that works. These
 * cover the ones around a flush that does not: a stream that cannot be
 * written, a segment that cannot be read, a rotation that keeps failing — and
 * a replay arriving after compaction and garbage collection have already
 * moved its records on.
 */

package logstore_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/catalog"
	"github.com/kilianc/rtail/v2/internal/compact"
	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/model"
)

// hourAgo is a clean hour boundary two hours back: old enough for an L0 merge
// to be due, far too young for an L1 one.
func hourAgo() time.Time {
	return time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
}

func recAt(stream, payload string, ts time.Time) *model.Record {
	r := rec(stream, payload)
	r.Ts = ts
	return r
}

// mergeL0 compacts every due L0 group into an L1.
func mergeL0(t *testing.T, store *logstore.Durable) {
	t.Helper()

	compactor := compact.New(store.Catalog(), store.Backend(), compact.Options{
		L1After: time.Nanosecond, L1MinFiles: 1000, KeepRaw: true, Log: discardLogger(),
	})
	if _, err := compactor.RunOnce(context.Background()); nil != err {
		t.Fatalf("compacting: %v", err)
	}
}

func openDurableErr(dir string) (*logstore.Durable, error) {
	return logstore.OpenDurable(context.Background(), dir, quietOptions(10))
}

// unwritableStream makes every file of one stream fail to be written, until
// the returned func runs.
func unwritableStream(t *testing.T, dir, stream string) func() {
	t.Helper()

	path := filepath.Join(dir, "streams", stream)
	if err := os.MkdirAll(path, 0o755); nil != err {
		t.Fatal(err)
	}

	return failWrites(t, path)
}

/*!
 * A segment that stays on disk after its records were flushed — its removal
 * failed, then the process died — is replayed at the next boot. By then
 * compaction may have merged its file into an L1 and garbage collection
 * removed the L0. Forgetting the L0's row as well meant the replay found
 * nothing registered and wrote the records again beside the L1; and the
 * next merge, naming its output after the same first record, overwrote the
 * live L1 and lost what only it held.
 */
func TestReplayAfterCompactionAndCollectionDuplicatesNothing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base := hourAgo()

	store := openDurable(t, dir, quietOptions(10))

	store.Append(ctx, recAt("api", `{"x":1}`, base))
	if err := store.FlushKeepingWALForTest(ctx); nil != err {
		t.Fatal(err)
	}

	store.Append(ctx, recAt("api", `{"y":1}`, base.Add(time.Second)))
	if err := store.Flush(ctx); nil != err {
		t.Fatal(err)
	}

	mergeL0(t, store)
	if err := store.CollectForTest(ctx, time.Now().Add(2*time.Hour)); nil != err {
		t.Fatal(err)
	}

	store.AbandonForTest()

	reopened := openDurable(t, dir, quietOptions(10))
	defer reopened.Close()

	reopened.Append(ctx, recAt("api", `{"z":1}`, base.Add(2*time.Second)))
	if err := reopened.Flush(ctx); nil != err {
		t.Fatal(err)
	}
	mergeL0(t, reopened)

	counts := rawSet(readAll(t, dir, "api"))
	for _, raw := range []string{`{"x":1}`, `{"y":1}`, `{"z":1}`} {
		if 1 != counts[raw] {
			t.Errorf("record %s appears %d times, want once; all: %v", raw, counts[raw], counts)
		}
	}
}

// The same, through the retry list instead of a restart: a segment kept for
// one stream that cannot be written is replayed by every flush.
func TestRetryAfterCompactionAndCollectionDuplicatesNothing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base := hourAgo()

	store := openDurable(t, dir, quietOptions(10))
	defer store.Close()

	restore := unwritableStream(t, dir, "stuck")

	store.Append(ctx, recAt("api", `{"x":1}`, base))
	store.Append(ctx, recAt("stuck", `{"p":1}`, base))
	if err := store.Flush(ctx); nil == err {
		t.Skip("the directory stayed writable; running as root?")
	}

	store.Append(ctx, recAt("api", `{"y":1}`, base.Add(time.Second)))
	store.Flush(ctx)

	mergeL0(t, store)
	store.CollectForTest(ctx, time.Now().Add(2*time.Hour))

	// This flush's retry replays the first segment again.
	store.Append(ctx, recAt("api", `{"z":1}`, base.Add(2*time.Second)))
	store.Flush(ctx)
	mergeL0(t, store)

	restore()
	if err := store.Flush(ctx); nil != err {
		t.Fatalf("flush once the stream could be written: %v", err)
	}

	counts := rawSet(readAll(t, dir, "api"))
	for _, raw := range []string{`{"x":1}`, `{"y":1}`, `{"z":1}`} {
		if 1 != counts[raw] {
			t.Errorf("record %s appears %d times, want once; all: %v", raw, counts[raw], counts)
		}
	}
	if got := rawSet(readAll(t, dir, "stuck")); 1 != got[`{"p":1}`] {
		t.Errorf("the stuck stream holds %v, want its record once", got)
	}
}

/*!
 * A stream id longer than a file name may be — `rtail --id`, or a query
 * string, can carry anything — failed every write with "file name too long".
 * Its segment never flushed, and after a restart the server refused to boot.
 */
func TestOverlongStreamIdsAreStored(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	long := strings.Repeat("a", 300)

	store := openDurable(t, dir, quietOptions(10))
	store.Append(ctx, rec("api", `{"i":0}`))
	store.Append(ctx, rec(long, `{"i":1}`))
	if err := store.Flush(ctx); nil != err {
		t.Fatalf("flushing a stream with a %d-character id: %v", len(long), err)
	}

	// And through a replay.
	store.Append(ctx, rec(long, `{"i":2}`))
	store.SyncForTest()
	store.AbandonForTest()

	reopened := openDurable(t, dir, quietOptions(10))
	reopened.Close()

	if got := len(rawSet(readAll(t, dir, long))); 2 != got {
		t.Errorf("the long stream holds %d records, want 2", got)
	}
	if got := len(readAll(t, dir, "api")); 1 != got {
		t.Errorf("api holds %d records, want 1", got)
	}
}

/*!
 * One stream that cannot be written does not keep the others out of the
 * catalog, and the retry that follows does not write them again: only the
 * failing stream is retried.
 */
func TestOneUnwritableStreamDoesNotHoldBackTheOthers(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	defer store.Close()

	restore := unwritableStream(t, dir, "stuck")

	store.Append(ctx, rec("api", `{"i":0}`))
	store.Append(ctx, rec("stuck", `{"i":1}`))
	if err := store.Flush(ctx); nil == err {
		t.Skip("the directory stayed writable; running as root?")
	}

	files, err := store.Catalog().Prune(ctx, catalog.Query{Stream: "api"})
	if nil != err || 1 != len(files) {
		t.Fatalf("api files = %v (err %v), want its file registered despite the other stream", files, err)
	}

	object := filepath.Join(dir, filepath.FromSlash(files[0].Path))
	before, err := os.Stat(object)
	if nil != err {
		t.Fatal(err)
	}

	if err := store.Flush(ctx); nil == err {
		t.Error("the retry of a stream that still cannot be written reported no error")
	}

	after, err := os.Stat(object)
	if nil != err {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("the retry wrote the stream that had already landed again")
	}

	restore()
	if err := store.Flush(ctx); nil != err {
		t.Fatalf("flush once the stream could be written: %v", err)
	}

	for _, stream := range []string{"api", "stuck"} {
		if got := len(readAll(t, dir, stream)); 1 != got {
			t.Errorf("%s holds %d records, want 1", stream, got)
		}
	}
}

/*!
 * A segment that cannot be persisted at boot used to stop the server
 * starting: "recovering: persisting wal-…", every time. It goes on the retry
 * list instead, and everything else in it is served.
 */
func TestBootSurvivesASegmentItCannotPersist(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	store.Append(ctx, rec("api", `{"i":0}`))
	store.Append(ctx, rec("stuck", `{"i":1}`))
	store.SyncForTest()
	store.AbandonForTest()

	restore := unwritableStream(t, dir, "stuck")

	reopened, err := openDurableErr(dir)
	if nil != err {
		t.Fatalf("booting with a segment that cannot be persisted: %v", err)
	}
	defer reopened.Close()

	if got := len(readAll(t, dir, "api")); 1 != got {
		t.Errorf("api holds %d records after boot, want 1", got)
	}

	// The unpersisted records still count towards seq.
	next := rec("api", `{"i":2}`)
	if err := reopened.Append(ctx, next); nil != err {
		t.Fatal(err)
	}
	if next.Seq <= 2 {
		t.Errorf("the first record after boot got seq %d, colliding with the segment's", next.Seq)
	}

	restore()
	if err := reopened.Flush(ctx); nil != err {
		t.Fatalf("flush once the stream could be written: %v", err)
	}
	if got := len(readAll(t, dir, "stuck")); 1 != got {
		t.Errorf("stuck holds %d records, want the one from the retried segment", got)
	}
}

/*!
 * A segment damaged part way through used to stop every boot. Its readable
 * records are recovered and the segment is quarantined: out of the log, kept
 * on disk for inspection.
 */
func TestBootQuarantinesADamagedSegment(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store := openDurable(t, dir, quietOptions(10))
	for i := range 3 {
		store.Append(ctx, rec("api", fmt.Sprintf(`{"i":%d}`, i)))
	}
	store.SyncForTest()
	store.AbandonForTest()

	segments, err := filepath.Glob(filepath.Join(dir, "wal", "wal-*.log"))
	if nil != err || 1 != len(segments) {
		t.Fatalf("segments = %v (err %v)", segments, err)
	}

	// A zero length followed by more data is damage, not a torn tail.
	file, err := os.OpenFile(segments[0], os.O_APPEND|os.O_WRONLY, 0)
	if nil != err {
		t.Fatal(err)
	}
	file.Write(make([]byte, 8))
	file.Write([]byte("not zeros"))
	file.Close()

	reopened, err := openDurableErr(dir)
	if nil != err {
		t.Fatalf("booting with a damaged segment: %v", err)
	}
	reopened.Close()

	if got := len(rawSet(readAll(t, dir, "api"))); 3 != got {
		t.Errorf("recovered %d records, want the 3 before the damage", got)
	}

	quarantined, err := filepath.Glob(filepath.Join(dir, "wal", "quarantine", "wal-*"))
	if nil != err || 1 != len(quarantined) {
		t.Errorf("quarantined = %v (err %v), want the damaged segment", quarantined, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "wal", "wal-*.log")); 1 != len(left) {
		t.Errorf("segments left in the log = %v, want only the new active one", left)
	}
}

// messages counts log records by message.
type messages struct {
	mu     sync.Mutex
	counts map[string]int
}

func (m *messages) Enabled(context.Context, slog.Level) bool { return true }
func (m *messages) WithAttrs([]slog.Attr) slog.Handler       { return m }
func (m *messages) WithGroup(string) slog.Handler            { return m }

func (m *messages) Handle(_ context.Context, record slog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.counts[record.Message]++
	return nil
}

func (m *messages) count(message string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.counts[message]
}

/*!
 * A rotation that keeps failing leaves the batch over the size threshold, and
 * every append after it used to start another flush the moment the last one
 * gave up — each re-reading the retry list and attempting an fsync under the
 * ingest lock. The size trigger backs off instead.
 */
func TestAFailingRotationBacksOffTheSizeTrigger(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	logged := &messages{counts: map[string]int{}}

	opts := quietOptions(10)
	opts.FlushRows = 1
	opts.Log = slog.New(logged)

	store := openDurable(t, dir, opts)

	restore := failWrites(t, filepath.Join(dir, "wal"))

	for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); {
		if err := store.Append(ctx, rec("api", `{"i":0}`)); nil != err {
			t.Fatal(err)
		}
	}

	// Let a flush already started finish and log.
	time.Sleep(100 * time.Millisecond)

	failed := logged.count("flushing")
	if 0 == failed {
		restore()
		store.Close()
		t.Skip("the directory stayed writable; running as root?")
	}
	if failed > 2 {
		t.Errorf("%d size-triggered flushes failed in 300ms, want the trigger to back off after the first", failed)
	}

	restore()
	if err := store.Close(); nil != err {
		t.Fatalf("closing once the rotation could succeed: %v", err)
	}
}
