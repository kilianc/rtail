package compact_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/catalog"
	"github.com/kilianc/rtail/v2/internal/compact"
	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/normalize"
	"github.com/kilianc/rtail/v2/internal/parquetio"
	"github.com/kilianc/rtail/v2/internal/storage"
)

/*!
 * The harness writes through the real flush path, so what compaction reads is
 * what the server actually produces.
 */
type harness struct {
	dir     string
	store   *logstore.Durable
	cat     *catalog.Catalog
	backend storage.Backend
}

func newHarness(t *testing.T, opts logstore.DurableOptions) *harness {
	t.Helper()

	dir := t.TempDir()

	if 0 == opts.Backlog {
		opts.Backlog = 1000
	}
	if 0 == opts.FlushInterval {
		opts.FlushInterval = time.Hour
	}
	if 0 == opts.SyncInterval {
		opts.SyncInterval = -1
	}
	if 0 == opts.Grace {
		opts.Grace = time.Hour
	}
	opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))

	store, err := logstore.OpenDurable(context.Background(), dir, opts)
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	return &harness{dir: dir, store: store, cat: store.Catalog(), backend: store.Backend()}
}

// flush writes one L0 file per stream from the given payloads.
func (h *harness) flush(t *testing.T, stream string, at time.Time, payloads []string) []*model.Record {
	t.Helper()

	ctx := context.Background()
	out := make([]*model.Record, 0, len(payloads))

	for i, payload := range payloads {
		rec := normalize.FromLine(stream, "10.0.0.1", 5000, []byte(payload))
		// Deterministic, spread out so bucketing and sorting are observable.
		rec.Ts = at.Add(time.Duration(i) * time.Second).UTC()

		if err := h.store.Append(ctx, rec); nil != err {
			t.Fatal(err)
		}
		out = append(out, rec)
	}

	if err := h.store.Flush(ctx); nil != err {
		t.Fatal(err)
	}

	return out
}

// flushAt is flush with explicit per-record timestamps, for the cases where
// the spread across a boundary is the thing under test.
func (h *harness) flushAt(t *testing.T, stream string, times []time.Time, payloads []string) []*model.Record {
	t.Helper()

	ctx := context.Background()
	out := make([]*model.Record, 0, len(payloads))

	for i, payload := range payloads {
		rec := normalize.FromLine(stream, "10.0.0.1", 5000, []byte(payload))
		rec.Ts = times[i].UTC()

		if err := h.store.Append(ctx, rec); nil != err {
			t.Fatal(err)
		}
		out = append(out, rec)
	}

	if err := h.store.Flush(ctx); nil != err {
		t.Fatal(err)
	}

	return out
}

func (h *harness) live(t *testing.T) []catalog.File {
	t.Helper()

	files, err := h.cat.Prune(context.Background(), catalog.Query{})
	if nil != err {
		t.Fatal(err)
	}

	return files
}

// allRecords reads every live file back, which is what a query would see.
func (h *harness) allRecords(t *testing.T) []*model.Record {
	t.Helper()

	var out []*model.Record

	for _, file := range h.live(t) {
		batch, err := parquetio.Read(context.Background(), h.backend, file.Path, 0)
		if nil != err {
			t.Fatalf("reading %s: %v", file.Path, err)
		}
		out = append(out, batch...)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })

	return out
}

func compactor(h *harness, opts compact.Options) *compact.Compactor {
	if nil == opts.Log {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return compact.New(h.cat, h.backend, opts)
}

// lines builds n payloads for a stream.
func lines(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		level := "info"
		if 0 == i%4 {
			level = "error"
		}
		out[i] = fmt.Sprintf(`{"level":%q,"msg":"%s-%d","service":"api","i":%d}`, level, prefix, i, i)
	}
	return out
}

/*!
 * The headline property: nothing is lost and nothing is duplicated.
 */
func TestMergePreservesEveryRecord(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-3 * time.Hour)

	var written []*model.Record
	for batch := range 6 {
		written = append(written, h.flush(t, "api", base.Add(time.Duration(batch)*time.Minute), lines("b", 5))...)
	}

	if 6 != len(h.live(t)) {
		t.Fatalf("expected 6 L0 files, got %d", len(h.live(t)))
	}

	before := h.allRecords(t)

	result, err := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2}).RunOnce(ctx)
	if nil != err {
		t.Fatal(err)
	}

	if 0 == result.Merged {
		t.Fatal("nothing was merged")
	}

	files := h.live(t)
	if 1 != len(files) {
		t.Fatalf("expected 1 file after compaction, got %d", len(files))
	}
	if catalog.LevelL1 != files[0].Level {
		t.Errorf("level = %d, want L1", files[0].Level)
	}

	after := h.allRecords(t)

	if len(before) != len(after) {
		t.Fatalf("record count changed: %d before, %d after", len(before), len(after))
	}
	if len(written) != len(after) {
		t.Fatalf("wrote %d records, read back %d", len(written), len(after))
	}

	for i := range before {
		if before[i].Seq != after[i].Seq {
			t.Fatalf("record %d: seq %d before, %d after", i, before[i].Seq, after[i].Seq)
		}
		if before[i].Msg != after[i].Msg {
			t.Errorf("record %d: msg %q before, %q after", i, before[i].Msg, after[i].Msg)
		}
		if !before[i].Ts.Equal(after[i].Ts) {
			t.Errorf("record %d: ts %s before, %s after", i, before[i].Ts, after[i].Ts)
		}
	}
}

// Sort order is the whole point of compacting, so it is checked rather than
// assumed: the output has to be ordered by ts on disk.
func TestOutputIsSortedByTime(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-3 * time.Hour)

	// Flush in an order that is not time order, so a pass-through would fail.
	h.flush(t, "api", base.Add(30*time.Minute), lines("late", 4))
	h.flush(t, "api", base.Add(10*time.Minute), lines("early", 4))
	h.flush(t, "api", base.Add(20*time.Minute), lines("mid", 4))

	if _, err := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	files := h.live(t)
	if 1 != len(files) {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	records, err := parquetio.Read(ctx, h.backend, files[0].Path, 0)
	if nil != err {
		t.Fatal(err)
	}

	for i := 1; i < len(records); i++ {
		if records[i].Ts.Before(records[i-1].Ts) {
			t.Fatalf("row %d is out of order: %s after %s", i, records[i].Ts, records[i-1].Ts)
		}
	}
}

/*!
 * Compaction is where a messy population of L0 schemas becomes one clean
 * union: keys only some files have, and keys whose type disagreed.
 */
func TestSchemasAreUnionedAndWidened(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-3 * time.Hour)

	h.flush(t, "api", base, []string{`{"msg":"a","code":200,"only_old":"x"}`})
	h.flush(t, "api", base.Add(time.Minute), []string{`{"msg":"b","code":"oops","only_new":1}`})
	h.flush(t, "api", base.Add(2*time.Minute), []string{`{"msg":"c","code":404}`})

	if _, err := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	files := h.live(t)
	if 1 != len(files) {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	columns, err := h.cat.Columns(ctx, files[0].ID)
	if nil != err {
		t.Fatal(err)
	}

	byKey := map[string]catalog.Column{}
	for _, column := range columns {
		byKey[column.SourceKey] = column
	}

	// A key that was an int in one file and a string in another widens to
	// string rather than losing a value.
	code, ok := byKey["code"]
	if !ok {
		t.Fatal("code column is missing from the merged file")
	}
	if "string" != code.Kind {
		t.Errorf("code kind = %q, want string after widening", code.Kind)
	}

	// Keys present in only one input survive the union.
	for _, key := range []string{"only_old", "only_new"} {
		if _, ok := byKey[key]; !ok {
			t.Errorf("%q was lost in the union", key)
		}
	}

	records, err := parquetio.Read(ctx, h.backend, files[0].Path, 0)
	if nil != err {
		t.Fatal(err)
	}
	if 3 != len(records) {
		t.Fatalf("records = %d, want 3", len(records))
	}

	var codes []string
	for _, rec := range records {
		codes = append(codes, rec.Fields["code"].Text())
	}
	sort.Strings(codes)

	if "200" != codes[0] || "404" != codes[1] || "oops" != codes[2] {
		t.Errorf("codes = %v, want every original value preserved", codes)
	}
}

/*!
 * The invariant: publish and retire happen together, or not at all.
 */
func TestReplaceIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-3 * time.Hour)

	for batch := range 3 {
		h.flush(t, "api", base.Add(time.Duration(batch)*time.Minute), lines("b", 4))
	}

	inputs := h.live(t)
	ids := make([]int64, 0, len(inputs))
	for _, file := range inputs {
		ids = append(ids, file.ID)
	}

	output := catalog.Replacement{
		File: catalog.File{
			Stream: "api", Path: "streams/api/merged.parquet", Level: catalog.LevelL1,
			MinTs: base, MaxTs: base.Add(time.Hour),
			MinSeq: 1, MaxSeq: 12, RowCount: 12, ByteSize: 999, HasRaw: true,
		},
	}

	first, err := h.cat.Replace(ctx, []catalog.Replacement{output}, ids)
	if nil != err {
		t.Fatal(err)
	}

	files := h.live(t)
	if 1 != len(files) || files[0].Path != output.File.Path {
		t.Fatalf("live files = %+v, want just the merged one", files)
	}

	// Replaying the identical operation — what a crashed-and-retried
	// compaction does — must change nothing at all.
	streamsBefore, _ := h.cat.Streams(ctx)

	second, err := h.cat.Replace(ctx, []catalog.Replacement{output}, ids)
	if nil != err {
		t.Fatal(err)
	}

	if 1 != len(second) || first[0] != second[0] {
		t.Errorf("replay returned %v, want the same id as %v", second, first)
	}

	if 1 != len(h.live(t)) {
		t.Errorf("replay changed the live set: %+v", h.live(t))
	}

	streamsAfter, _ := h.cat.Streams(ctx)
	if streamsBefore[0].RowCount != streamsAfter[0].RowCount {
		t.Errorf("replay double-counted rows: %d then %d",
			streamsBefore[0].RowCount, streamsAfter[0].RowCount)
	}
	if streamsBefore[0].ByteSize != streamsAfter[0].ByteSize {
		t.Errorf("replay double-counted bytes: %d then %d",
			streamsBefore[0].ByteSize, streamsAfter[0].ByteSize)
	}
}

/*!
 * The stream rollup is a counter, so compaction has to move it by the delta.
 * Getting this wrong is invisible until the numbers are absurd.
 */
func TestRollupSurvivesCompaction(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-3 * time.Hour)

	total := 0
	for batch := range 5 {
		total += len(h.flush(t, "api", base.Add(time.Duration(batch)*time.Minute), lines("b", 6)))
	}

	before, _ := h.cat.Streams(ctx)
	if int64(total) != before[0].RowCount {
		t.Fatalf("rollup = %d before compaction, want %d", before[0].RowCount, total)
	}

	// Compact repeatedly: a delta bug compounds, so once is not a fair test.
	for range 3 {
		if _, err := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2, L2After: -time.Hour}).RunOnce(ctx); nil != err {
			t.Fatal(err)
		}
	}

	after, _ := h.cat.Streams(ctx)
	if int64(total) != after[0].RowCount {
		t.Errorf("rollup = %d after compaction, want %d — the delta is wrong",
			after[0].RowCount, total)
	}

	var liveBytes int64
	for _, file := range h.live(t) {
		liveBytes += file.ByteSize
	}
	if liveBytes != after[0].ByteSize {
		t.Errorf("rollup bytes = %d, live files total %d", after[0].ByteSize, liveBytes)
	}
}

// Autocomplete ranks by occurrences, so compaction must not inflate them.
func TestSchemaKeyOccurrencesAreNotInflated(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-3 * time.Hour)

	for batch := range 4 {
		h.flush(t, "api", base.Add(time.Duration(batch)*time.Minute), lines("b", 5))
	}

	occurrences := func() int64 {
		keys, err := h.cat.SchemaKeys(ctx, "api")
		if nil != err {
			t.Fatal(err)
		}
		for _, key := range keys {
			if "service" == key.SourceKey {
				return key.Occurrences
			}
		}
		t.Fatal("service key is missing")
		return 0
	}

	before := occurrences()

	for range 3 {
		if _, err := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2, L2After: -time.Hour}).RunOnce(ctx); nil != err {
			t.Fatal(err)
		}
	}

	if after := occurrences(); after != before {
		t.Errorf("occurrences went from %d to %d across compactions", before, after)
	}
}

/*!
 * Tombstoned inputs stay readable until the grace period elapses. That window
 * is the only thing standing between a running query and a deleted file.
 */
func TestInputsSurviveUntilTheGracePeriodElapses(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true, Grace: time.Hour})
	base := time.Now().UTC().Add(-3 * time.Hour)

	for batch := range 3 {
		h.flush(t, "api", base.Add(time.Duration(batch)*time.Minute), lines("b", 4))
	}

	inputs := h.live(t)

	if _, err := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	// Gone from the live set ...
	for _, file := range h.live(t) {
		for _, old := range inputs {
			if file.ID == old.ID {
				t.Errorf("%s is still live after being compacted", file.Path)
			}
		}
	}

	// ... but still on disk, and still readable.
	for _, old := range inputs {
		if _, err := parquetio.Read(ctx, h.backend, old.Path, 0); nil != err {
			t.Errorf("a query holding %s could no longer read it: %v", old.Path, err)
		}
	}

	// Nothing is collectable yet.
	collectable, err := h.cat.Collectable(ctx, time.Now().Add(-time.Hour))
	if nil != err {
		t.Fatal(err)
	}
	if 0 != len(collectable) {
		t.Errorf("%d files were collectable inside the grace period", len(collectable))
	}

	// Past the grace period they are.
	collectable, err = h.cat.Collectable(ctx, time.Now().Add(time.Hour))
	if nil != err {
		t.Fatal(err)
	}
	if len(inputs) != len(collectable) {
		t.Errorf("collectable = %d, want %d", len(collectable), len(inputs))
	}
}

func TestRetentionDeletesWholeFilesPastTheHorizon(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})

	old := time.Now().UTC().Add(-72 * time.Hour)
	recent := time.Now().UTC().Add(-time.Minute)

	h.flush(t, "api", old, lines("old", 3))
	h.flush(t, "api", recent, lines("new", 3))

	result, err := compactor(h, compact.Options{
		KeepRaw:   true,
		Retention: compact.Retention{DeleteAfter: 24 * time.Hour},
	}).RunOnce(ctx)
	if nil != err {
		t.Fatal(err)
	}

	if 1 != result.Expired {
		t.Errorf("expired = %d, want 1", result.Expired)
	}

	for _, file := range h.live(t) {
		if file.MaxTs.Before(time.Now().Add(-24 * time.Hour)) {
			t.Errorf("%s is past the horizon and still live", file.Path)
		}
	}

	records := h.allRecords(t)
	for _, rec := range records {
		if rec.Ts.Before(time.Now().Add(-24 * time.Hour)) {
			t.Errorf("an expired record survived: %s", rec.Msg)
		}
	}
}

// A file straddling the horizon survives until all of it has expired —
// rewriting a file to delete a few rows costs far more than keeping them.
func TestRetentionKeepsStraddlingFiles(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})

	// One flush genuinely spanning the boundary: the oldest record is well
	// past the horizon, the newest is well inside it.
	h.flushAt(t, "api", []time.Time{
		time.Now().UTC().Add(-25 * time.Hour),
		time.Now().UTC().Add(-26 * time.Hour),
		time.Now().UTC().Add(-time.Minute),
	}, lines("s", 3))

	before := len(h.live(t))
	if 1 != before {
		t.Fatalf("expected 1 file, got %d", before)
	}

	if _, err := compactor(h, compact.Options{
		KeepRaw:   true,
		Retention: compact.Retention{DeleteAfter: 24 * time.Hour},
	}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	if before != len(h.live(t)) {
		t.Errorf("a straddling file was deleted: %d files became %d", before, len(h.live(t)))
	}
}

func TestRetentionDropsRawOnceOldEnough(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	old := time.Now().UTC().Add(-48 * time.Hour)

	for batch := range 3 {
		h.flush(t, "api", old.Add(time.Duration(batch)*time.Minute), lines("b", 4))
	}

	for _, file := range h.live(t) {
		if !file.HasRaw {
			t.Fatal("raw was not stored to begin with")
		}
	}

	if _, err := compactor(h, compact.Options{
		KeepRaw:    true,
		L1MinFiles: 2,
		Retention:  compact.Retention{DropRawAfter: 24 * time.Hour},
	}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	files := h.live(t)
	if 1 != len(files) {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].HasRaw {
		t.Error("raw was kept past the horizon")
	}

	// The promoted columns survive; only the original line is gone.
	records, err := parquetio.Read(ctx, h.backend, files[0].Path, 0)
	if nil != err {
		t.Fatal(err)
	}
	if 12 != len(records) {
		t.Fatalf("records = %d, want 12", len(records))
	}
	for _, rec := range records {
		if "" != rec.Raw {
			t.Errorf("raw survived: %q", rec.Raw)
		}
		if "" == rec.Msg {
			t.Error("msg was lost along with raw")
		}
		if _, ok := rec.Fields["service"]; !ok {
			t.Error("a promoted column was lost along with raw")
		}
	}
}

// Raw stays while any record in the file is still inside its window.
func TestRetentionKeepsRawWhileAnyRecordIsRecent(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})

	h.flush(t, "api", time.Now().UTC().Add(-48*time.Hour), lines("old", 3))
	h.flush(t, "api", time.Now().UTC().Add(-time.Minute), lines("new", 3))

	if _, err := compactor(h, compact.Options{
		KeepRaw:    true,
		L1MinFiles: 2,
		L1After:    -time.Hour,
		Retention:  compact.Retention{DropRawAfter: 24 * time.Hour},
	}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	for _, file := range h.live(t) {
		mixed := file.MaxTs.After(time.Now().Add(-24 * time.Hour))
		if mixed && !file.HasRaw {
			t.Errorf("%s holds recent records but lost raw", file.Path)
		}
	}
}

func TestDownsamplingDiscardsLowSeverityOldRecords(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	old := time.Now().UTC().Add(-48 * time.Hour)

	// lines() makes every fourth record an error.
	for batch := range 3 {
		h.flush(t, "api", old.Add(time.Duration(batch)*time.Minute), lines("b", 8))
	}

	if _, err := compactor(h, compact.Options{
		KeepRaw:    true,
		L1MinFiles: 2,
		Retention: compact.Retention{
			MinLevelAfter:   "WARN",
			DownsampleAfter: 24 * time.Hour,
		},
	}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	records := h.allRecords(t)
	if 0 == len(records) {
		t.Fatal("everything was discarded")
	}

	for _, rec := range records {
		if "ERROR" != rec.Level {
			t.Errorf("a record below WARN survived downsampling: level %q", rec.Level)
		}
	}

	// 6 of every 8 were INFO, so a quarter should remain.
	if 6 != len(records) {
		t.Errorf("kept %d records, want the 6 errors", len(records))
	}
}

// Recent records are never downsampled, whatever their level.
func TestDownsamplingSparesRecentRecords(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	now := time.Now().UTC().Add(-time.Minute)

	for batch := range 3 {
		h.flush(t, "api", now.Add(time.Duration(batch)*time.Second), lines("b", 8))
	}

	if _, err := compactor(h, compact.Options{
		KeepRaw:    true,
		L1MinFiles: 2,
		L1After:    -time.Hour,
		Retention: compact.Retention{
			MinLevelAfter:   "WARN",
			DownsampleAfter: 24 * time.Hour,
		},
	}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	if 24 != len(h.allRecords(t)) {
		t.Errorf("kept %d records, want all 24 — none are old enough to downsample",
			len(h.allRecords(t)))
	}
}

/*!
 * A quiet stream must still get compacted, or its L0 files accumulate forever.
 */
func TestQuietStreamsAreCompactedOnAge(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	old := time.Now().UTC().Add(-6 * time.Hour)

	// Two files: below the count threshold, but well past the age one.
	h.flush(t, "api", old, []string{`{"msg":"one"}`})
	h.flush(t, "api", old.Add(time.Minute), []string{`{"msg":"two"}`})

	if _, err := compactor(h, compact.Options{
		KeepRaw: true, L1MinFiles: 100, L1After: time.Hour,
	}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	files := h.live(t)
	if 1 != len(files) {
		t.Fatalf("files = %d, want 1 — an aged group should merge below the count threshold", len(files))
	}
	if catalog.LevelL1 != files[0].Level {
		t.Errorf("level = %d, want L1", files[0].Level)
	}
}

// A fresh group is left alone; merging it immediately would just churn.
func TestFreshGroupsAreLeftAlone(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	now := time.Now().UTC()

	h.flush(t, "api", now, []string{`{"msg":"one"}`})
	h.flush(t, "api", now.Add(time.Second), []string{`{"msg":"two"}`})

	result, err := compactor(h, compact.Options{
		KeepRaw: true, L1MinFiles: 10, L1After: time.Hour,
	}).RunOnce(ctx)
	if nil != err {
		t.Fatal(err)
	}

	if 0 != result.Merged {
		t.Errorf("merged %d fresh groups, want none", result.Merged)
	}
	if 2 != len(h.live(t)) {
		t.Errorf("files = %d, want the 2 originals untouched", len(h.live(t)))
	}
}

func TestStreamsAreCompactedIndependently(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-3 * time.Hour)

	for batch := range 3 {
		h.flush(t, "api", base.Add(time.Duration(batch)*time.Minute), lines("a", 3))
		h.flush(t, "worker", base.Add(time.Duration(batch)*time.Minute), lines("w", 3))
	}

	if _, err := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	byStream := map[string]int{}
	for _, file := range h.live(t) {
		byStream[file.Stream]++
	}

	if 1 != byStream["api"] || 1 != byStream["worker"] {
		t.Errorf("files per stream = %v, want one each", byStream)
	}

	for _, file := range h.live(t) {
		records, err := parquetio.Read(ctx, h.backend, file.Path, 0)
		if nil != err {
			t.Fatal(err)
		}
		for _, rec := range records {
			if rec.Stream != file.Stream {
				t.Errorf("%s contains a record from %q — streams were mixed", file.Path, rec.Stream)
			}
		}
	}
}

// L1 files promote to L2, sorted by the clustering key.
func TestPromotionToL2ClustersByLevel(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	old := time.Now().UTC().Add(-72 * time.Hour)

	for batch := range 4 {
		h.flush(t, "api", old.Add(time.Duration(batch)*time.Minute), lines("b", 6))
	}

	c := compactor(h, compact.Options{
		KeepRaw: true, L1MinFiles: 2, ClusterBy: "level",
	})

	// First pass makes L1, second promotes it.
	if _, err := c.RunOnce(ctx); nil != err {
		t.Fatal(err)
	}
	if _, err := c.RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	files := h.live(t)
	if 1 != len(files) {
		t.Fatalf("files = %d, want 1", len(files))
	}
	if catalog.LevelL2 != files[0].Level {
		t.Fatalf("level = %d, want L2", files[0].Level)
	}

	records, err := parquetio.Read(ctx, h.backend, files[0].Path, 0)
	if nil != err {
		t.Fatal(err)
	}
	if 24 != len(records) {
		t.Fatalf("records = %d, want 24", len(records))
	}

	// Clustered: all of one level, then all of the next.
	for i := 1; i < len(records); i++ {
		if records[i].Level < records[i-1].Level {
			t.Fatalf("row %d breaks the clustering: %q after %q",
				i, records[i].Level, records[i-1].Level)
		}
	}
}

// Compaction must be safe to run repeatedly with nothing to do.
func TestRepeatedPassesConverge(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-3 * time.Hour)

	for batch := range 4 {
		h.flush(t, "api", base.Add(time.Duration(batch)*time.Minute), lines("b", 4))
	}

	c := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2})

	if _, err := c.RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	settled := h.live(t)

	for pass := range 3 {
		result, err := c.RunOnce(ctx)
		if nil != err {
			t.Fatal(err)
		}
		if 0 != result.Merged {
			t.Errorf("pass %d merged %d groups, want a no-op", pass, result.Merged)
		}
	}

	if len(settled) != len(h.live(t)) {
		t.Errorf("the file set kept changing: %d then %d", len(settled), len(h.live(t)))
	}

	if 16 != len(h.allRecords(t)) {
		t.Errorf("records = %d, want 16", len(h.allRecords(t)))
	}
}

func TestEmptyCatalogIsANoop(t *testing.T) {
	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})

	result, err := compactor(h, compact.Options{KeepRaw: true}).RunOnce(context.Background())
	if nil != err {
		t.Fatal(err)
	}

	if 0 != result.Merged || 0 != result.Expired {
		t.Errorf("result = %+v, want everything zero", result)
	}
}
