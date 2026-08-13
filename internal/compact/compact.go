/*!
 * Compaction and retention — docs/proposal-logging-system.md §5.
 *
 * Flushing produces a small file every thirty seconds per stream. Left alone
 * that is thousands of files a day, each with its own footer to open, and a
 * query planner that has to consider all of them. Compaction merges them into
 * progressively larger, better-sorted files:
 *
 *   L0  a flush          minutes    sorted by seq
 *   L1  an hour          hourly     sorted by ts
 *   L2  a day            daily      sorted by (level, ts)
 *
 * Sort order is the point, not file count. Sorting by ts makes time-range
 * pruning exact at row-group granularity rather than merely likely, and a
 * low-cardinality leading column turns the most common filter into a row-group
 * skip. Compaction is also where a messy population of L0 schemas becomes one
 * clean union: types that disagreed get widened, always-null columns disappear,
 * and retention policies are applied.
 *
 * The invariant everything rests on: a compaction either publishes its outputs
 * and retires its inputs, or changes nothing. Never both halves, never neither.
 * See catalog.Replace, and compact_test.go, which kills the process at every
 * step boundary and asserts exactly that.
 */

package compact

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/kilianc/rtail/v2/internal/catalog"
	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/parquetio"
	"github.com/kilianc/rtail/v2/internal/rql"
	"github.com/kilianc/rtail/v2/internal/storage"
)

// Options tune the compactor.
type Options struct {
	// L1MinFiles is how many L0 files in one hour trigger a merge.
	L1MinFiles int
	// L1After promotes an hour's L0 files once the newest is this old, so a
	// quiet stream is still compacted eventually rather than never.
	L1After time.Duration
	// L2After merges a day's L1 files once the newest is this old.
	L2After time.Duration

	// MaxInputFiles and MaxInputRows bound one compaction's memory. The merge
	// reads its inputs into memory: a streaming k-way merge would remove the
	// bound, and is worth doing when these limits start being hit rather than
	// before.
	MaxInputFiles int
	MaxInputRows  int64

	/*!
	 * ClusterBy is the leading sort column for L2 files.
	 *
	 * Empty means sort by ts alone. "level" is the useful default for logs —
	 * it makes `level>=ERROR` over a day skip most row groups outright — at
	 * the cost of making a pure time-range scan touch more of the file.
	 */
	ClusterBy string

	// Retention policies. Zero means "never".
	Retention Retention

	// KeepRaw is the default for compacted files, before Retention.DropRawAfter
	// has anything to say.
	KeepRaw bool

	Log *slog.Logger
}

/*!
 * Retention describes what happens to data as it ages.
 *
 * All three are rewrites driven by the catalog, using the same machinery as
 * compaction — which is why they are here rather than in a package of their
 * own. Deleting is the only one that does not rewrite anything.
 */
type Retention struct {
	// DeleteAfter tombstones files whose newest record is older than this.
	DeleteAfter time.Duration

	// DropRawAfter rewrites files without the original line. Roughly halves
	// the footprint; the cost is that keys which were never promoted to
	// columns stop being queryable, since raw was the fallback.
	DropRawAfter time.Duration

	// MinLevelAfter discards records below this severity once they are older
	// than DownsampleAfter. "WARN" keeps warnings and worse.
	MinLevelAfter   string
	DownsampleAfter time.Duration
}

// Defaults, chosen so a server started with no tuning behaves sensibly.
const (
	DefaultL1MinFiles    = 6
	DefaultL1After       = 90 * time.Minute
	DefaultL2After       = 36 * time.Hour
	DefaultMaxInputFiles = 64
	DefaultMaxInputRows  = 2_000_000
)

func (o *Options) withDefaults() {
	if 0 == o.L1MinFiles {
		o.L1MinFiles = DefaultL1MinFiles
	}
	if 0 == o.L1After {
		o.L1After = DefaultL1After
	}
	if 0 == o.L2After {
		o.L2After = DefaultL2After
	}
	if 0 == o.MaxInputFiles {
		o.MaxInputFiles = DefaultMaxInputFiles
	}
	if 0 == o.MaxInputRows {
		o.MaxInputRows = DefaultMaxInputRows
	}
	if nil == o.Log {
		o.Log = slog.Default()
	}
}

// Compactor merges and expires files.
type Compactor struct {
	cat     *catalog.Catalog
	backend storage.Backend
	opts    Options

	/*!
	 * beforePublish runs after the output object is committed and before the
	 * catalog transaction. Test-only, set through export_test.go.
	 *
	 * That gap is the one dangerous moment in the whole design — the only
	 * instant where a file exists on disk that nothing points at — so being
	 * able to stop there on purpose is the difference between testing the
	 * invariant and hoping it holds.
	 */
	beforePublish func() error
}

// New builds a compactor.
func New(cat *catalog.Catalog, backend storage.Backend, opts Options) *Compactor {
	opts.withDefaults()
	return &Compactor{cat: cat, backend: backend, opts: opts}
}

// Result summarises one pass.
type Result struct {
	Merged      int
	InputFiles  int
	OutputFiles int
	BytesBefore int64
	BytesAfter  int64
	RowsDropped int64
	Expired     int
}

/*!
 * RunOnce performs one full pass: expire, then merge L0→L1, then L1→L2.
 *
 * Expiry runs first so that a merge never spends effort on data that is about
 * to be deleted anyway.
 */
func (c *Compactor) RunOnce(ctx context.Context) (Result, error) {
	var result Result

	expired, err := c.expire(ctx)
	if nil != err {
		return result, fmt.Errorf("expiring: %w", err)
	}
	result.Expired = expired

	for _, level := range []int{catalog.LevelL0, catalog.LevelL1} {
		if err := c.mergeLevel(ctx, level, &result); nil != err {
			return result, fmt.Errorf("merging L%d: %w", level, err)
		}
	}

	return result, nil
}

/*!
 * expire tombstones whole files that retention has aged out.
 *
 * Whole files only. A file is deleted when its *newest* record is past the
 * horizon, so a file straddling the boundary survives until all of it is
 * expired — deleting rows inside a file would mean rewriting it, and keeping a
 * few hours of extra data is much cheaper than that.
 */
func (c *Compactor) expire(ctx context.Context) (int, error) {
	if 0 == c.opts.Retention.DeleteAfter {
		return 0, nil
	}

	cutoff := time.Now().UTC().Add(-c.opts.Retention.DeleteAfter)

	files, err := c.cat.Prune(ctx, catalog.Query{To: cutoff})
	if nil != err {
		return 0, err
	}

	var doomed []int64
	for _, file := range files {
		if file.MaxTs.Before(cutoff) {
			doomed = append(doomed, file.ID)
		}
	}

	if 0 == len(doomed) {
		return 0, nil
	}

	if err := c.cat.Tombstone(ctx, doomed...); nil != err {
		return 0, err
	}

	c.opts.Log.Info("expired files past the retention horizon",
		"files", len(doomed), "older_than", c.opts.Retention.DeleteAfter)

	return len(doomed), nil
}

// bucket is a set of files that will merge into one output.
type bucket struct {
	stream string
	key    time.Time
	files  []catalog.File
}

/*!
 * mergeLevel groups the files at one level and merges the eligible groups.
 */
func (c *Compactor) mergeLevel(ctx context.Context, level int, result *Result) error {
	files, err := c.cat.Prune(ctx, catalog.Query{Levels: []int{level}})
	if nil != err {
		return err
	}

	for _, group := range c.bucketize(files, level) {
		if !c.eligible(group, level) {
			continue
		}

		if err := c.merge(ctx, group, level+1, result); nil != err {
			return err
		}
	}

	return nil
}

/*!
 * bucketize groups files by stream and by the period the merge targets.
 *
 * Grouping is on min_ts, so a file holding late data groups with the hour that
 * data belongs to rather than the hour it arrived in. The resulting output may
 * span more than its bucket, which is fine and expected: the catalog records
 * each file's real range, and the planner has never assumed files are disjoint.
 */
func (c *Compactor) bucketize(files []catalog.File, level int) []bucket {
	period := time.Hour
	if catalog.LevelL1 == level {
		period = 24 * time.Hour
	}

	index := map[string]*bucket{}
	var order []*bucket

	for _, file := range files {
		key := file.MinTs.UTC().Truncate(period)
		id := file.Stream + "\x00" + key.Format(time.RFC3339)

		group, ok := index[id]
		if !ok {
			group = &bucket{stream: file.Stream, key: key}
			index[id] = group
			order = append(order, group)
		}

		group.files = append(group.files, file)
	}

	out := make([]bucket, 0, len(order))
	for _, group := range order {
		out = append(out, *group)
	}

	return out
}

/*!
 * eligible decides whether a group is worth merging yet.
 *
 * Two ways in: enough files to be worth the rewrite, or old enough that
 * waiting for more is pointless. Without the second, a stream that emits one
 * line an hour would keep its L0 files forever.
 */
func (c *Compactor) eligible(group bucket, level int) bool {
	if 0 == len(group.files) {
		return false
	}

	after := c.opts.L1After
	if catalog.LevelL1 == level {
		after = c.opts.L2After
	}

	newest := group.files[0].MaxTs
	for _, file := range group.files {
		if file.MaxTs.After(newest) {
			newest = file.MaxTs
		}
	}

	aged := time.Since(newest) > after

	if catalog.LevelL0 == level {
		return len(group.files) >= c.opts.L1MinFiles || aged
	}

	// Promoting a single L1 to L2 is still worth it once the day is closed:
	// it re-sorts by the clustering key and applies retention rewrites.
	return aged
}

/*!
 * merge rewrites a group of files into one file at the next level.
 */
func (c *Compactor) merge(ctx context.Context, group bucket, level int, result *Result) error {
	inputs := group.files

	if len(inputs) > c.opts.MaxInputFiles {
		inputs = inputs[:c.opts.MaxInputFiles]
	}

	var (
		records []*model.Record
		ids     []int64
		before  int64
		rows    int64
	)

	for _, file := range inputs {
		if rows > c.opts.MaxInputRows {
			break
		}

		batch, err := parquetio.Read(ctx, c.backend, file.Path, 0)
		if nil != err {
			return fmt.Errorf("reading %s: %w", file.Path, err)
		}

		records = append(records, batch...)
		ids = append(ids, file.ID)
		before += file.ByteSize
		rows += file.RowCount
	}

	if 0 == len(ids) {
		return nil
	}

	/*!
	 * A group whose inputs all expired down to nothing still has to retire
	 * them. Replacing with no outputs is a legitimate operation — it is how
	 * downsampling a file to zero rows is expressed — and Replace handles it.
	 */
	kept := c.applyRetention(records, group)
	dropped := int64(len(records) - len(kept))

	if 0 == len(kept) {
		if _, err := c.cat.Replace(ctx, nil, ids); nil != err {
			return err
		}

		result.Merged++
		result.InputFiles += len(ids)
		result.RowsDropped += dropped
		result.BytesBefore += before

		return nil
	}

	sortRecords(kept, level, c.opts.ClusterBy)

	name := outputName(group.stream, level, kept[0])
	keepRaw := c.keepRaw(kept)

	schema, stats, err := parquetio.Write(ctx, c.backend, name, kept, parquetio.WriteOptions{
		KeepRaw: keepRaw,
		SortBy:  sortColumns(level, c.opts.ClusterBy),
	})
	if nil != err {
		return fmt.Errorf("writing %s: %w", name, err)
	}

	if stats.Dropped > 0 {
		c.opts.Log.Warn("dropped columns past the per-file cap during compaction",
			"stream", group.stream, "dropped", stats.Dropped, "kept", len(schema.Columns()))
	}

	columns := make([]catalog.Column, 0, len(stats.Columns))
	for _, column := range stats.Columns {
		columns = append(columns, catalog.Column{
			Name:        column.Name,
			SourceKey:   column.SourceKey,
			Kind:        column.Kind.String(),
			Polymorphic: column.Polymorphic,
			NullCount:   column.NullCount,
			MinValue:    column.MinValue,
			MaxValue:    column.MaxValue,
			HasRange:    column.HasRange,
		})
	}

	output := catalog.Replacement{
		File: catalog.File{
			Stream:   group.stream,
			Path:     name,
			Level:    level,
			MinTs:    stats.MinTs,
			MaxTs:    stats.MaxTs,
			MinSeq:   stats.MinSeq,
			MaxSeq:   stats.MaxSeq,
			RowCount: stats.Rows,
			ByteSize: stats.Bytes,
			HasRaw:   stats.HasRaw,
		},
		Columns: columns,
	}

	if nil != c.beforePublish {
		if err := c.beforePublish(); nil != err {
			return err
		}
	}

	if _, err := c.cat.Replace(ctx, []catalog.Replacement{output}, ids); nil != err {
		// The object is committed but unregistered, and the inputs are still
		// live. Queries are unaffected — they cannot see the new file — and
		// the next pass rewrites it under the same deterministic name.
		return fmt.Errorf("publishing %s: %w", name, err)
	}

	result.Merged++
	result.InputFiles += len(ids)
	result.OutputFiles++
	result.BytesBefore += before
	result.BytesAfter += stats.Bytes
	result.RowsDropped += dropped

	c.opts.Log.Info("compacted",
		"stream", group.stream, "level", level,
		"inputs", len(ids), "rows", stats.Rows,
		"before", before, "after", stats.Bytes,
		"raw", keepRaw)

	return nil
}

/*!
 * applyRetention filters records that have aged past a downsampling policy.
 *
 * Applied at merge time rather than on a schedule of its own, because the file
 * is already being rewritten — doing it separately would mean reading and
 * rewriting the same data twice.
 */
func (c *Compactor) applyRetention(records []*model.Record, group bucket) []*model.Record {
	policy := c.opts.Retention

	if 0 == policy.DownsampleAfter || "" == policy.MinLevelAfter {
		return records
	}

	cutoff := time.Now().UTC().Add(-policy.DownsampleAfter)
	floor := rql.Rank(policy.MinLevelAfter)

	kept := make([]*model.Record, 0, len(records))

	for _, rec := range records {
		if rec.Ts.After(cutoff) || rql.Rank(rec.Level) >= floor {
			kept = append(kept, rec)
		}
	}

	return kept
}

// keepRaw decides whether the output keeps the original lines.
func (c *Compactor) keepRaw(records []*model.Record) bool {
	if !c.opts.KeepRaw {
		return false
	}
	if 0 == c.opts.Retention.DropRawAfter {
		return true
	}

	cutoff := time.Now().UTC().Add(-c.opts.Retention.DropRawAfter)

	// Raw goes only when every record in the file is past the horizon, since
	// it is a per-file decision and dropping it early would lose data that is
	// still inside its retention window.
	for _, rec := range records {
		if rec.Ts.After(cutoff) {
			return true
		}
	}

	return false
}

// sortColumns is the physical sort order for a level.
func sortColumns(level int, clusterBy string) []string {
	if catalog.LevelL2 == level && "" != clusterBy {
		return []string{columnFor(clusterBy), parquetio.ColTs, parquetio.ColSeq}
	}

	return []string{parquetio.ColTs, parquetio.ColSeq}
}

// columnFor maps a clustering key onto a physical column name.
func columnFor(key string) string {
	switch key {
	case "level", "severity":
		return parquetio.ColLevel
	case "stream":
		return parquetio.ColStream
	case "host":
		return parquetio.ColHost
	default:
		return parquetio.FieldPrefix + key
	}
}

/*!
 * sortRecords orders the merged records to match the file's declared sort.
 *
 * The Parquet writer is told the same order, but it sorts within row groups;
 * ordering the slice first is what makes the *file* sorted, which is what the
 * row-group statistics — and therefore pruning — actually depend on.
 */
func sortRecords(records []*model.Record, level int, clusterBy string) {
	byTime := func(i, j int) bool {
		if !records[i].Ts.Equal(records[j].Ts) {
			return records[i].Ts.Before(records[j].Ts)
		}
		return records[i].Seq < records[j].Seq
	}

	if catalog.LevelL2 != level || "" == clusterBy {
		sort.SliceStable(records, byTime)
		return
	}

	sort.SliceStable(records, func(i, j int) bool {
		a, b := clusterValue(records[i], clusterBy), clusterValue(records[j], clusterBy)
		if a != b {
			return a < b
		}
		return byTime(i, j)
	})
}

func clusterValue(rec *model.Record, key string) string {
	switch key {
	case "level", "severity":
		return rec.Level
	case "stream":
		return rec.Stream
	case "host":
		return rec.Host
	}

	if value, ok := rec.Fields[key]; ok {
		return value.Text()
	}

	return ""
}

/*!
 * outputName is derived from the merged content, not from the clock.
 *
 * That is what makes a retried compaction idempotent: a pass that wrote its
 * object and then died produces the identical path next time, so the rewrite
 * overwrites the same bytes and the catalog recognises it as already
 * published rather than adding a duplicate.
 */
func outputName(stream string, level int, first *model.Record) string {
	day := first.Ts.UTC()

	return fmt.Sprintf("streams/%s/%04d/%02d/%02d/L%d-%020d-%d.parquet",
		safeSegment(stream),
		day.Year(), int(day.Month()), day.Day(),
		level, first.Seq, day.UnixMicro(),
	)
}

// safeSegment keeps a stream name usable as a directory. Stream ids come from
// `rtail --id`, which is to say from anywhere at all.
func safeSegment(stream string) string {
	if "" == stream {
		return "_unnamed"
	}

	out := []rune(stream)
	for i, r := range out {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			'-' == r, '_' == r, '.' == r:
		default:
			out[i] = '_'
		}
	}

	if "." == string(out) || ".." == string(out) {
		return "_" + string(out)
	}

	return string(out)
}

/*!
 * Run compacts on an interval until the context is cancelled.
 *
 * A failed pass is logged and the loop continues rather than stopping the
 * server. Compaction is an optimisation: the data is correct without it, just
 * spread across more files than anyone wants, and taking an ingest process
 * down because a merge failed would trade a performance problem for an outage.
 */
func (c *Compactor) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		c.opts.Log.Info("compaction is disabled")
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			result, err := c.RunOnce(ctx)
			if nil != err {
				if nil != ctx.Err() {
					return
				}
				c.opts.Log.Error("compaction pass failed", "err", err)
				continue
			}

			if result.Merged > 0 || result.Expired > 0 {
				c.opts.Log.Info("compaction pass",
					"merged", result.Merged,
					"inputs", result.InputFiles,
					"outputs", result.OutputFiles,
					"expired", result.Expired,
					"rows_dropped", result.RowsDropped,
					"bytes_before", result.BytesBefore,
					"bytes_after", result.BytesAfter)
			}
		}
	}
}
