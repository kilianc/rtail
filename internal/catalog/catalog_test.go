package catalog_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/catalog"
)

func open(t *testing.T) *catalog.Catalog {
	t.Helper()

	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })

	return cat
}

var epoch = time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)

// at returns a time offset from a fixed base, so tests never depend on now.
func at(minutes int) time.Time { return epoch.Add(time.Duration(minutes) * time.Minute) }

func register(t *testing.T, cat *catalog.Catalog, stream, path string, from, to int, seq uint64, columns ...catalog.Column) int64 {
	t.Helper()

	id, existed, err := cat.Register(context.Background(), catalog.File{
		Stream:   stream,
		Path:     path,
		Level:    catalog.LevelL0,
		MinTs:    at(from),
		MaxTs:    at(to),
		MinSeq:   seq,
		MaxSeq:   seq + 9,
		RowCount: 10,
		ByteSize: 1024,
		HasRaw:   true,
	}, columns)
	if nil != err {
		t.Fatalf("Register(%s): %v", path, err)
	}
	if existed {
		t.Fatalf("Register(%s) reported the file already existed", path)
	}

	return id
}

func paths(files []catalog.File) []string {
	out := make([]string, len(files))
	for i, file := range files {
		out[i] = file.Path
	}
	return out
}

/*!
 * Pruning by time must use overlap, not containment.
 *
 * Files are explicitly allowed to overlap, because a record arriving three
 * hours late lands in the current flush and widens that file's range. A
 * planner that assumed disjoint files would silently miss it.
 */
func TestPruneByTimeUsesOverlap(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	register(t, cat, "api", "a.parquet", 0, 10, 1)
	register(t, cat, "api", "b.parquet", 10, 20, 11)
	register(t, cat, "api", "c.parquet", 20, 30, 21)
	// A late-arriving batch whose range straddles all of them.
	register(t, cat, "api", "late.parquet", 5, 25, 31)

	files, err := cat.Prune(ctx, catalog.Query{From: at(12), To: at(14)})
	if nil != err {
		t.Fatal(err)
	}

	got := paths(files)
	if 2 != len(got) {
		t.Fatalf("pruned to %v, want b.parquet and late.parquet", got)
	}

	seen := map[string]bool{got[0]: true, got[1]: true}
	if !seen["b.parquet"] || !seen["late.parquet"] {
		t.Errorf("pruned to %v, want b.parquet and late.parquet", got)
	}
}

func TestPruneByStream(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	register(t, cat, "api", "api.parquet", 0, 10, 1)
	register(t, cat, "worker", "worker.parquet", 0, 10, 11)

	files, err := cat.Prune(ctx, catalog.Query{Stream: "api"})
	if nil != err {
		t.Fatal(err)
	}

	if 1 != len(files) || "api.parquet" != files[0].Path {
		t.Errorf("pruned to %v, want [api.parquet]", paths(files))
	}
}

// "Which files even have this key" is the second-cheapest way to prune.
func TestPruneByKeyPresence(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	register(t, cat, "api", "with.parquet", 0, 10, 1, catalog.Column{
		Name: "a_trace_id", SourceKey: "trace_id", Kind: "string",
	})
	register(t, cat, "api", "without.parquet", 0, 10, 11)

	files, err := cat.Prune(ctx, catalog.Query{RequireKey: "trace_id"})
	if nil != err {
		t.Fatal(err)
	}

	if 1 != len(files) || "with.parquet" != files[0].Path {
		t.Errorf("pruned to %v, want [with.parquet]", paths(files))
	}
}

func TestPruneOrdersByTime(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	register(t, cat, "api", "third.parquet", 20, 30, 21)
	register(t, cat, "api", "first.parquet", 0, 10, 1)
	register(t, cat, "api", "second.parquet", 10, 20, 11)

	files, err := cat.Prune(ctx, catalog.Query{})
	if nil != err {
		t.Fatal(err)
	}

	want := []string{"first.parquet", "second.parquet", "third.parquet"}
	for i, path := range paths(files) {
		if path != want[i] {
			t.Errorf("files[%d] = %s, want %s", i, path, want[i])
		}
	}
}

/*!
 * Registering a known path is a no-op. This is what makes crash recovery
 * idempotent, and getting it wrong double-counts the stream rollups on every
 * crash.
 */
func TestRegisteringTheSamePathTwiceIsANoop(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	first := register(t, cat, "api", "same.parquet", 0, 10, 1)

	second, existed, err := cat.Register(ctx, catalog.File{
		Stream: "api", Path: "same.parquet", Level: catalog.LevelL0,
		MinTs: at(0), MaxTs: at(10), MinSeq: 1, MaxSeq: 10,
		RowCount: 10, ByteSize: 1024,
	}, nil)
	if nil != err {
		t.Fatal(err)
	}

	if !existed {
		t.Error("existed = false, want true")
	}
	if second != first {
		t.Errorf("id = %d, want the original %d", second, first)
	}

	files, _ := cat.Prune(ctx, catalog.Query{})
	if 1 != len(files) {
		t.Errorf("files = %v, want one", paths(files))
	}

	streams, _ := cat.Streams(ctx)
	if 1 != len(streams) || 10 != streams[0].RowCount {
		t.Errorf("stream rollup = %+v, want row_count 10 — the second register double-counted", streams)
	}
}

func TestStreamRollupAccumulates(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	register(t, cat, "api", "one.parquet", 0, 10, 1)
	register(t, cat, "api", "two.parquet", 10, 20, 11)

	streams, err := cat.Streams(ctx)
	if nil != err {
		t.Fatal(err)
	}

	if 1 != len(streams) {
		t.Fatalf("streams = %d, want 1", len(streams))
	}
	if 20 != streams[0].RowCount || 2048 != streams[0].ByteSize {
		t.Errorf("rollup = %d rows / %d bytes, want 20 / 2048", streams[0].RowCount, streams[0].ByteSize)
	}
	if !streams[0].FirstSeen.Equal(at(0)) || !streams[0].LastSeen.Equal(at(20)) {
		t.Errorf("seen range = %s .. %s", streams[0].FirstSeen, streams[0].LastSeen)
	}
}

/*!
 * The key inventory is what the search bar completes against, so a key that
 * was an int in one file and a string in another has to be marked polymorphic
 * — otherwise the UI keeps offering numeric comparisons that cannot work.
 */
func TestSchemaKeysTrackPolymorphism(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	register(t, cat, "api", "one.parquet", 0, 10, 1,
		catalog.Column{Name: "a_id", SourceKey: "id", Kind: "int"},
		catalog.Column{Name: "a_name", SourceKey: "name", Kind: "string"},
	)
	register(t, cat, "api", "two.parquet", 10, 20, 11,
		catalog.Column{Name: "a_id", SourceKey: "id", Kind: "string"},
		catalog.Column{Name: "a_name", SourceKey: "name", Kind: "string"},
	)

	keys, err := cat.SchemaKeys(ctx, "api")
	if nil != err {
		t.Fatal(err)
	}

	byKey := map[string]catalog.SchemaKey{}
	for _, key := range keys {
		byKey[key.SourceKey] = key
	}

	id := byKey["id"]
	if !id.Polymorphic {
		t.Error("id should be polymorphic after being int then string")
	}
	if "string" != id.Kind {
		t.Errorf("id kind = %q, want string once widened", id.Kind)
	}

	name := byKey["name"]
	if name.Polymorphic {
		t.Error("name was string in both files and should not be polymorphic")
	}
	if 20 != name.Occurrences {
		t.Errorf("name occurrences = %d, want 20", name.Occurrences)
	}
}

func TestSchemaKeysCountNonNullOccurrences(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	register(t, cat, "api", "one.parquet", 0, 10, 1,
		catalog.Column{Name: "a_rare", SourceKey: "rare", Kind: "int", NullCount: 7},
	)

	keys, _ := cat.SchemaKeys(ctx, "api")
	if 1 != len(keys) {
		t.Fatalf("keys = %v", keys)
	}
	// 10 rows, 7 of them null.
	if 3 != keys[0].Occurrences {
		t.Errorf("occurrences = %d, want 3", keys[0].Occurrences)
	}
}

/*!
 * Tombstoning is the design's MVCC: a superseded file stays readable for a
 * grace period so a query holding an older file list does not break.
 */
func TestTombstonedFilesLeaveThePruneSetButStay(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	id := register(t, cat, "api", "old.parquet", 0, 10, 1)
	register(t, cat, "api", "new.parquet", 0, 10, 11)

	if err := cat.Tombstone(ctx, id); nil != err {
		t.Fatal(err)
	}

	files, _ := cat.Prune(ctx, catalog.Query{})
	if 1 != len(files) || "new.parquet" != files[0].Path {
		t.Errorf("prune = %v, want only new.parquet", paths(files))
	}

	// Not yet collectable — the grace period has not elapsed.
	collectable, err := cat.Collectable(ctx, time.Now().Add(-time.Hour))
	if nil != err {
		t.Fatal(err)
	}
	if 0 != len(collectable) {
		t.Errorf("collectable = %v, want none before the grace period", paths(collectable))
	}

	// Once it has.
	collectable, err = cat.Collectable(ctx, time.Now().Add(time.Hour))
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(collectable) || "old.parquet" != collectable[0].Path {
		t.Errorf("collectable = %v, want [old.parquet]", paths(collectable))
	}

	if err := cat.Forget(ctx, id); nil != err {
		t.Fatal(err)
	}

	// Forgetting cascades to the columns.
	columns, err := cat.Columns(ctx, id)
	if nil != err {
		t.Fatal(err)
	}
	if 0 != len(columns) {
		t.Errorf("columns survived the file: %v", columns)
	}
}

/*!
 * seq is the tiebreaker in the (ts, seq) ordering that pagination depends on,
 * so it must survive a restart rather than resetting to 1.
 */
func TestMaxSeqSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.sqlite")

	cat, err := catalog.Open(path)
	if nil != err {
		t.Fatal(err)
	}

	if seq, err := cat.MaxSeq(ctx); nil != err || 0 != seq {
		t.Fatalf("fresh MaxSeq = %d, %v; want 0", seq, err)
	}

	register(t, cat, "api", "a.parquet", 0, 10, 1)    // max_seq 10
	register(t, cat, "api", "b.parquet", 10, 20, 991) // max_seq 1000
	cat.Close()

	reopened, err := catalog.Open(path)
	if nil != err {
		t.Fatal(err)
	}
	defer reopened.Close()

	seq, err := reopened.MaxSeq(ctx)
	if nil != err {
		t.Fatal(err)
	}
	if 1000 != seq {
		t.Errorf("MaxSeq = %d, want 1000", seq)
	}
}

// Registering an older file must not walk the sequence backwards.
func TestMaxSeqOnlyMovesForward(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	register(t, cat, "api", "high.parquet", 0, 10, 991)
	register(t, cat, "api", "low.parquet", 10, 20, 1)

	seq, _ := cat.MaxSeq(ctx)
	if 1000 != seq {
		t.Errorf("MaxSeq = %d, want it to stay at 1000", seq)
	}
}

func TestColumnsRoundTrip(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	id := register(t, cat, "api", "a.parquet", 0, 10, 1,
		catalog.Column{
			Name: "a_latency_ms", SourceKey: "latency_ms", Kind: "float",
			NullCount: 2, MinValue: "1.5", MaxValue: "9000", HasRange: true,
		},
		catalog.Column{
			Name: "a_req", SourceKey: "req", Kind: "json", NullCount: 0,
		},
	)

	columns, err := cat.Columns(ctx, id)
	if nil != err {
		t.Fatal(err)
	}
	if 2 != len(columns) {
		t.Fatalf("columns = %d, want 2", len(columns))
	}

	latency := columns[0]
	if "latency_ms" != latency.SourceKey || "float" != latency.Kind {
		t.Errorf("column = %+v", latency)
	}
	if !latency.HasRange || "1.5" != latency.MinValue || "9000" != latency.MaxValue {
		t.Errorf("range = %q..%q (has=%v)", latency.MinValue, latency.MaxValue, latency.HasRange)
	}

	// A JSON column has no useful ordering, so no range was recorded.
	if columns[1].HasRange {
		t.Errorf("json column reported a range: %+v", columns[1])
	}
}

func TestStatsSummarise(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	register(t, cat, "api", "a.parquet", 0, 10, 1)
	register(t, cat, "worker", "b.parquet", 0, 10, 11)

	stats, err := cat.Stats(ctx)
	if nil != err {
		t.Fatal(err)
	}

	if 2 != stats.Files || 20 != stats.Rows || 2 != stats.Streams {
		t.Errorf("stats = %+v", stats)
	}
}

// The pruning query is the planner's hot path, so it must stay indexed rather
// than degrading into a table scan as files accumulate.
func TestPruneStaysFastWithManyFiles(t *testing.T) {
	ctx := context.Background()
	cat := open(t)

	for i := range 2000 {
		register(t, cat, fmt.Sprintf("stream-%d", i%20),
			fmt.Sprintf("file-%04d.parquet", i), i, i+1, uint64(i*10+1))
	}

	start := time.Now()
	files, err := cat.Prune(ctx, catalog.Query{Stream: "stream-3", From: at(100), To: at(200)})
	elapsed := time.Since(start)

	if nil != err {
		t.Fatal(err)
	}
	if 0 == len(files) {
		t.Fatal("pruned to nothing")
	}
	for _, file := range files {
		if "stream-3" != file.Stream {
			t.Fatalf("prune returned %s", file.Stream)
		}
	}

	// Generous: this is an indexed lookup and should be well under a
	// millisecond. The point is to catch the index being dropped.
	if elapsed > 100*time.Millisecond {
		t.Errorf("prune over 2000 files took %s, which suggests a table scan", elapsed)
	}
}
