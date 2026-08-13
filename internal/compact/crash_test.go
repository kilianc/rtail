/*!
 * Compaction under crash.
 *
 * The proposal calls this out as where storage engines go to die, so these are
 * deterministic rather than hopeful: the compactor is stopped at the one
 * dangerous boundary on purpose, restarted, and the result checked against the
 * only invariant that matters — **no record is ever lost or duplicated, and
 * the catalog never points at a file that is not there**.
 *
 * There are three places a compaction can die:
 *
 *   1. before the output object is written  — nothing changed
 *   2. after the object, before the catalog — an orphan file nothing points at
 *   3. after the catalog, before GC         — inputs tombstoned, still on disk
 *
 * Only (2) is interesting, and it is the one with a hook.
 */

package compact_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/catalog"
	"github.com/kilianc/rtail/v2/internal/compact"
	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/query"
)

// fingerprint reduces the live data to something comparable across a
// compaction: every record's identity, in a stable order.
func fingerprint(records []*model.Record) []string {
	out := make([]string, 0, len(records))

	for _, rec := range records {
		out = append(out, fmt.Sprintf("%d|%s|%s|%s",
			rec.Seq, rec.Ts.UTC().Format(time.RFC3339Nano), rec.Level, rec.Msg))
	}

	sort.Strings(out)
	return out
}

func requireSame(t *testing.T, what string, before, after []string) {
	t.Helper()

	if len(before) != len(after) {
		t.Fatalf("%s: %d records before, %d after", what, len(before), len(after))
	}

	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("%s: record %d differs:\n  before %s\n  after  %s",
				what, i, before[i], after[i])
		}
	}
}

// objectsOnDisk lists every parquet file actually present.
func objectsOnDisk(t *testing.T, dir string) []string {
	t.Helper()

	var out []string

	err := filepath.WalkDir(filepath.Join(dir, "streams"), func(path string, entry os.DirEntry, err error) error {
		if nil != err {
			// No streams directory yet is not a failure.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".parquet") {
			out = append(out, path)
		}
		return nil
	})
	if nil != err && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	sort.Strings(out)
	return out
}

/*!
 * The dangerous window: the object is on disk and committed, and the catalog
 * has not been told. A crash here must lose nothing, and the retry must not
 * duplicate anything.
 */
func TestCrashBetweenWriteAndPublishLosesNothing(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-3 * time.Hour)

	for batch := range 5 {
		h.flush(t, "api", base.Add(time.Duration(batch)*time.Minute), lines("b", 6))
	}

	before := fingerprint(h.allRecords(t))
	inputs := h.live(t)

	// Crash.
	crashed := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2})
	boom := errors.New("power failure")
	crashed.StopBeforePublishForTest(func() error { return boom })

	if _, err := crashed.RunOnce(ctx); !errors.Is(err, boom) {
		t.Fatalf("expected the injected failure, got %v", err)
	}

	// The inputs are untouched and still live: a reader mid-query is fine.
	live := h.live(t)
	if len(inputs) != len(live) {
		t.Fatalf("live files went from %d to %d across a failed compaction",
			len(inputs), len(live))
	}
	requireSame(t, "after the crash", before, fingerprint(h.allRecords(t)))

	// The orphan object exists but nothing points at it, so it is invisible.
	orphans := len(objectsOnDisk(t, h.dir)) - len(live)
	if orphans < 1 {
		t.Fatal("expected an orphan object from the interrupted write")
	}

	// Retry. The output name is derived from content, so this rewrites the
	// same path rather than accumulating a second orphan.
	if _, err := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	requireSame(t, "after the retry", before, fingerprint(h.allRecords(t)))

	if 1 != len(h.live(t)) {
		t.Errorf("live files = %d, want 1 merged file", len(h.live(t)))
	}
}

// Every catalog row must point at a file that is really there.
func TestCatalogNeverPointsAtAMissingFile(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-3 * time.Hour)

	for batch := range 4 {
		h.flush(t, "api", base.Add(time.Duration(batch)*time.Minute), lines("b", 4))
	}

	crashed := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2})
	crashed.StopBeforePublishForTest(func() error { return errors.New("boom") })
	crashed.RunOnce(ctx)

	check := func(stage string) {
		t.Helper()

		for _, file := range h.live(t) {
			if _, err := h.backend.Stat(ctx, file.Path); nil != err {
				t.Errorf("%s: catalog lists %s but it is not on disk: %v", stage, file.Path, err)
			}
		}
	}

	check("after the crash")

	if _, err := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	check("after the retry")
}

/*!
 * Repeated crashes at the same point must still converge. A single retry
 * working is a much weaker claim than the operation being genuinely
 * idempotent.
 */
func TestRepeatedCrashesConverge(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-3 * time.Hour)

	for batch := range 5 {
		h.flush(t, "api", base.Add(time.Duration(batch)*time.Minute), lines("b", 5))
	}

	before := fingerprint(h.allRecords(t))

	for attempt := range 4 {
		crashed := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2})
		crashed.StopBeforePublishForTest(func() error { return errors.New("boom") })
		crashed.RunOnce(ctx)

		requireSame(t, fmt.Sprintf("after crash %d", attempt), before, fingerprint(h.allRecords(t)))
	}

	if _, err := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	requireSame(t, "after finally succeeding", before, fingerprint(h.allRecords(t)))

	if 1 != len(h.live(t)) {
		t.Errorf("live files = %d, want 1", len(h.live(t)))
	}
}

/*!
 * The end-to-end property compaction exists to preserve: a query returns the
 * same answer before and after, for every shape of query.
 *
 * This is the test that would catch a sort order that broke pruning, a widened
 * column that stopped comparing, or a dropped raw that made a fallback field
 * unreachable — none of which the record-level checks above would notice.
 */
func TestQueryResultsAreIdenticalAcrossCompaction(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-2 * time.Hour)

	// Heterogeneous on purpose: keys that appear late, a key whose type
	// disagrees, and lines that are not JSON at all.
	batches := [][]string{
		{
			`{"level":"error","msg":"upstream timeout","service":"api","user_id":1,"latency_ms":900}`,
			`{"level":"info","msg":"ok","service":"api","user_id":2,"latency_ms":5}`,
		},
		{
			`{"level":"warn","msg":"slow","service":"worker","user_id":3,"latency_ms":600,"trace_id":"abc"}`,
			`a plain line mentioning timeout`,
		},
		{
			`{"level":"error","msg":"boom","service":"worker","user_id":"four","req":{"path":"/v1/x"}}`,
			`{"level":"fatal","msg":"dead","service":"api"}`,
		},
	}

	for i, batch := range batches {
		h.flush(t, "api", base.Add(time.Duration(i)*time.Minute), batch)
	}

	engine, err := query.Open(h.cat, h.backend, query.Limits{})
	if nil != err {
		t.Fatal(err)
	}
	defer engine.Close()

	queries := []string{
		``,
		`level>=ERROR`,
		`level>=WARN`,
		`service=api`,
		`user_id=2`,
		`latency_ms>500`,
		`timeout`,
		`msg:slow`,
		`trace_id=*`,
		`-level=INFO`,
		`req.path=/v1/x`,
		`service=worker OR level=FATAL`,
	}

	request := func(q string) query.Request {
		return query.Request{
			Query: q,
			From:  base.Add(-time.Hour),
			To:    time.Now().UTC().Add(time.Hour),
			Limit: 1000,
		}
	}

	before := map[string][]string{}
	for _, q := range queries {
		result, err := engine.Search(ctx, request(q))
		if nil != err {
			t.Fatalf("query %q before compaction: %v", q, err)
		}
		before[q] = fingerprint(result.Records)
	}

	if 3 != len(h.live(t)) {
		t.Fatalf("expected 3 L0 files, got %d", len(h.live(t)))
	}

	if _, err := compactor(h, compact.Options{
		KeepRaw: true, L1MinFiles: 2, ClusterBy: "level",
	}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	if 1 != len(h.live(t)) {
		t.Fatalf("expected 1 file after compaction, got %d", len(h.live(t)))
	}

	for _, q := range queries {
		result, err := engine.Search(ctx, request(q))
		if nil != err {
			t.Fatalf("query %q after compaction: %v", q, err)
		}
		requireSame(t, "query "+q, before[q], fingerprint(result.Records))
	}
}

// GC only removes what is both tombstoned and past its grace period, and never
// removes anything a live catalog row points at.
func TestGarbageCollectionRemovesOnlyRetiredFiles(t *testing.T) {
	ctx := context.Background()

	// A long grace period so the store's own background collector stays out of
	// the way; this test drives collection itself, and a 1ms grace makes the
	// two race for the same tombstones.
	h := newHarness(t, logstore.DurableOptions{KeepRaw: true, Grace: time.Hour})
	base := time.Now().UTC().Add(-3 * time.Hour)

	for batch := range 4 {
		h.flush(t, "api", base.Add(time.Duration(batch)*time.Minute), lines("b", 4))
	}

	if _, err := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	live := h.live(t)
	if 1 != len(live) {
		t.Fatalf("live = %d, want 1", len(live))
	}

	collectable, err := h.cat.Collectable(ctx, time.Now().UTC())
	if nil != err {
		t.Fatal(err)
	}
	if 4 != len(collectable) {
		t.Fatalf("collectable = %d, want the 4 inputs", len(collectable))
	}

	for _, file := range collectable {
		if file.Path == live[0].Path {
			t.Fatal("the merged output was marked collectable")
		}

		if err := h.backend.Remove(ctx, file.Path); nil != err {
			t.Fatal(err)
		}
		if err := h.cat.Forget(ctx, file.ID); nil != err {
			t.Fatal(err)
		}
	}

	// The live file survived and still reads.
	if _, err := h.backend.Stat(ctx, live[0].Path); nil != err {
		t.Fatalf("the live file was collected: %v", err)
	}
	if 16 != len(h.allRecords(t)) {
		t.Errorf("records = %d, want 16 after collection", len(h.allRecords(t)))
	}

	remaining, err := h.cat.Prune(ctx, catalog.Query{})
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(remaining) {
		t.Errorf("catalog lists %d files, want 1", len(remaining))
	}
}

// Restarting the server after a compaction must see exactly the compacted set.
func TestCompactedStateSurvivesRestart(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, logstore.DurableOptions{KeepRaw: true})
	base := time.Now().UTC().Add(-3 * time.Hour)

	for batch := range 4 {
		h.flush(t, "api", base.Add(time.Duration(batch)*time.Minute), lines("b", 5))
	}

	before := fingerprint(h.allRecords(t))

	if _, err := compactor(h, compact.Options{KeepRaw: true, L1MinFiles: 2}).RunOnce(ctx); nil != err {
		t.Fatal(err)
	}

	h.store.Close()

	reopened, err := logstore.OpenDurable(ctx, h.dir, logstore.DurableOptions{
		Backlog: 1000, KeepRaw: true,
		FlushInterval: time.Hour, SyncInterval: -1, Grace: time.Hour,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if nil != err {
		t.Fatal(err)
	}
	defer reopened.Close()

	h.store, h.cat, h.backend = reopened, reopened.Catalog(), reopened.Backend()

	if 1 != len(h.live(t)) {
		t.Fatalf("after restart: live = %d, want 1", len(h.live(t)))
	}

	requireSame(t, "after restart", before, fingerprint(h.allRecords(t)))
}
