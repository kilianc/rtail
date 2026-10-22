/*!
 * The durable store.
 *
 * Append writes to the WAL and to an in-memory batch. When a flush trigger
 * fires, the batch becomes one Parquet file per stream, those files are
 * registered in the catalog, and only then is the WAL segment deleted. The
 * ordering is the whole point:
 *
 *     WAL append  →  parquet commit  →  catalog register  →  WAL remove
 *
 * A crash between any two of those loses nothing:
 *
 *   - before the commit: the WAL still has the records, and the half-written
 *     object was never published because Create only publishes on Commit.
 *   - between commit and register: the object is an orphan no query can see.
 *     The records are still in the WAL, so replay writes them again — to the
 *     same path, overwriting the orphan.
 *   - between register and remove: replay writes the same object again and the
 *     catalog recognises the path and does nothing.
 *
 * The last two rely on object names being derived from the batch rather than
 * from a clock or a random id, which is what makes recovery idempotent instead
 * of merely duplicate-tolerant. Where duplicates remain possible — the same
 * records arriving twice over the wire — dedup on (stream, seq) is a
 * compaction concern and is not pretended to be solved here.
 *
 * And they rely on a sealed segment and the batch flushed from it holding the
 * same records in the same order, so that replaying the segment rebuilds the
 * batch exactly. Append and the rotation that seals a segment therefore share
 * one lock: see Append.
 */

package logstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kilianc/rtail/v2/internal/catalog"
	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/parquetio"
	"github.com/kilianc/rtail/v2/internal/storage"
	"github.com/kilianc/rtail/v2/internal/wal"
)

// Flush triggers. Whichever fires first wins.
const (
	DefaultFlushRows     = 200_000
	DefaultFlushBytes    = 64 << 20
	DefaultFlushInterval = 30 * time.Second

	// DefaultGrace is how long a tombstoned file stays on disk after being
	// superseded, so a query holding an older file list does not break.
	DefaultGrace = 5 * time.Minute

	// DefaultOrphanGrace is how old an object no catalog row points at has to
	// be before garbage collection deletes it. See sweep for why a day.
	DefaultOrphanGrace = 24 * time.Hour
)

// DurableOptions configure the store.
type DurableOptions struct {
	// Backlog is how many recent records per stream stay in memory to serve
	// the live view. Independent of what is on disk.
	Backlog int

	FlushRows     int
	FlushBytes    int64
	FlushInterval time.Duration

	// KeepRaw stores the original line alongside the promoted columns. Turning
	// it off roughly halves the footprint and makes unpromoted keys
	// unqueryable.
	KeepRaw bool

	SyncInterval time.Duration
	Grace        time.Duration
	OrphanGrace  time.Duration

	Log *slog.Logger
}

func (o *DurableOptions) withDefaults() {
	if o.Backlog < 1 {
		o.Backlog = DefaultBacklog
	}
	if o.FlushRows < 1 {
		o.FlushRows = DefaultFlushRows
	}
	if o.FlushBytes < 1 {
		o.FlushBytes = DefaultFlushBytes
	}
	if 0 == o.FlushInterval {
		o.FlushInterval = DefaultFlushInterval
	}
	if 0 == o.Grace {
		o.Grace = DefaultGrace
	}
	if 0 == o.OrphanGrace {
		o.OrphanGrace = DefaultOrphanGrace
	}
	if nil == o.Log {
		o.Log = slog.Default()
	}
}

// Durable is a Store that persists records as Parquet.
type Durable struct {
	opts    DurableOptions
	backend storage.Backend
	cat     *catalog.Catalog
	log     *wal.Log
	fanout  Fanout

	seq atomic.Uint64

	// ingest orders appends against each other and against the rotation that
	// seals a segment. See Append.
	ingest sync.Mutex

	mu      sync.Mutex
	order   []string
	streams map[string]*ring
	batch   map[string][]*model.Record
	rows    int
	bytes   int64
	closed  bool

	// flushing serializes flush cycles without holding mu across the I/O.
	flushing sync.Mutex

	// unflushed lists sealed segments whose batch could not be persisted, so
	// the next cycle retries them. Guarded by flushing.
	unflushed []string

	// kicked is set while a size-triggered flush is pending, so that only one
	// is ever started however many appends cross the threshold before it runs.
	kicked atomic.Bool

	stop chan struct{}
	done chan struct{}
}

var _ Store = (*Durable)(nil)

/*!
 * OpenDurable prepares a durable store rooted at dir.
 *
 * Recovery happens here, before the caller starts accepting traffic: any WAL
 * segment a previous process left behind is replayed into Parquet first. A
 * server that began ingesting while old segments were still pending would
 * interleave the two, and seq would stop being monotonic.
 */
func OpenDurable(ctx context.Context, dir string, opts DurableOptions) (*Durable, error) {
	opts.withDefaults()

	backend, err := storage.NewLocal(dir)
	if nil != err {
		return nil, err
	}

	cat, err := catalog.Open(path.Join(backend.Root(), "catalog.sqlite"))
	if nil != err {
		return nil, err
	}

	log, err := wal.Open(path.Join(backend.Root(), "wal"), wal.Options{SyncInterval: opts.SyncInterval})
	if nil != err {
		cat.Close()
		return nil, err
	}

	store := &Durable{
		opts:    opts,
		backend: backend,
		cat:     cat,
		log:     log,
		streams: make(map[string]*ring),
		batch:   make(map[string][]*model.Record),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}

	// Continue the sequence rather than restarting it: seq is the tiebreaker
	// in the (ts, seq) ordering, so a restart at 1 would collide with history.
	maxSeq, err := cat.MaxSeq(ctx)
	if nil != err {
		store.closeParts()
		return nil, fmt.Errorf("reading sequence: %w", err)
	}
	store.seq.Store(maxSeq)

	if err := store.recover(ctx); nil != err {
		store.closeParts()
		return nil, fmt.Errorf("recovering: %w", err)
	}

	if err := store.loadBacklog(ctx); nil != err {
		// A backlog we could not read back is a cosmetic failure — the data is
		// on disk and queryable — so it must not stop the server booting.
		opts.Log.Warn("could not restore the in-memory backlog", "err", err)
	}

	go store.run()

	return store, nil
}

/*!
 * recover replays orphaned WAL segments into Parquet.
 *
 * Records recovered this way keep the sequence numbers they were assigned
 * before the crash, so nothing shifts underneath a client that had already
 * seen them.
 */
func (d *Durable) recover(ctx context.Context) error {
	orphans, err := d.log.Orphans()
	if nil != err {
		return err
	}

	if 0 == len(orphans) {
		return nil
	}

	d.opts.Log.Info("replaying write-ahead log", "segments", len(orphans))

	for _, segment := range orphans {
		count, maxSeq, err := d.replay(ctx, segment)
		if nil != err {
			return err
		}

		if maxSeq > d.seq.Load() {
			d.seq.Store(maxSeq)
		}

		if count > 0 {
			d.opts.Log.Info("replayed segment", "segment", segment, "records", count)
		}
	}

	return nil
}

/*!
 * replay persists one sealed segment and then removes it.
 *
 * The records come back in the order they were appended, which — because a
 * segment is sealed under the same lock appends take — is the order the batch
 * flushed from it held them in. So the object names come out exactly as the
 * live flush's did, and anything that flush managed to register is recognised
 * rather than written a second time.
 */
func (d *Durable) replay(ctx context.Context, segment string) (int, uint64, error) {
	batch := map[string][]*model.Record{}

	var (
		count  int
		maxSeq uint64
	)

	if err := d.log.Replay(segment, func(rec *model.Record) error {
		batch[rec.Stream] = append(batch[rec.Stream], rec)
		count++

		if rec.Seq > maxSeq {
			maxSeq = rec.Seq
		}

		return nil
	}); nil != err {
		return 0, 0, fmt.Errorf("replaying %s: %w", segment, err)
	}

	if count > 0 {
		if err := d.persist(ctx, batch); nil != err {
			return 0, 0, fmt.Errorf("persisting %s: %w", segment, err)
		}
	}

	if err := d.log.Remove(segment); nil != err {
		return 0, 0, err
	}

	return count, maxSeq, nil
}

/*!
 * loadBacklog repopulates the in-memory ring from the newest files on disk, so
 * a restarted server shows history instead of an empty pane.
 */
func (d *Durable) loadBacklog(ctx context.Context) error {
	streams, err := d.cat.Streams(ctx)
	if nil != err {
		return err
	}

	for _, stream := range streams {
		records, err := d.newest(ctx, stream.Name)
		if nil != err {
			return err
		}
		if 0 == len(records) {
			continue
		}

		d.mu.Lock()
		buffer, known := d.streams[stream.Name]
		if !known {
			buffer = newRing(d.opts.Backlog)
			d.streams[stream.Name] = buffer
			d.order = append(d.order, stream.Name)
		}
		for _, rec := range records {
			buffer.push(rec)
		}
		d.mu.Unlock()
	}

	return nil
}

/*!
 * newest reads back the Backlog most recent records of one stream.
 *
 * Most recent by seq, which is the order the ring held them in before the
 * restart — not the order files sort by min_ts, where a flush full of late
 * data sorts first, and not the order rows sit in a compacted file, where an
 * L2 clustered by level keeps its newest record wherever its level falls.
 *
 * Files are visited highest max_seq first, which in practice is the L0 files
 * the last flushes left, and the walk stops once no file left can hold a
 * record newer than the oldest one kept. Each file is read with the backlog
 * as its limit, so a large L2 costs a scan but never its size in memory.
 */
func (d *Durable) newest(ctx context.Context, stream string) ([]*model.Record, error) {
	files, err := d.cat.Prune(ctx, catalog.Query{Stream: stream})
	if nil != err {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].MaxSeq != files[j].MaxSeq {
			return files[i].MaxSeq > files[j].MaxSeq
		}
		return files[i].Level < files[j].Level
	})

	var records []*model.Record

	for _, file := range files {
		if len(records) >= d.opts.Backlog && file.MaxSeq < records[0].Seq {
			break
		}

		columns, err := d.cat.Columns(ctx, file.ID)
		if nil != err {
			return nil, err
		}

		batch, err := parquetio.ReadWith(ctx, d.backend, file.Path, parquetio.ReadOptions{
			Columns: sourceColumns(columns),
			Limit:   d.opts.Backlog,
		})
		if nil != err {
			return nil, err
		}

		records = append(records, batch...)
		sort.Slice(records, func(i, j int) bool { return records[i].Seq < records[j].Seq })

		if len(records) > d.opts.Backlog {
			records = records[len(records)-d.opts.Backlog:]
		}
	}

	return records, nil
}

/*!
 * sourceColumns turns the catalog's record of a file's columns into what the
 * reader needs to map them back to their keys. A file alone spells
 * "http.status" a_http_status; the backlog has to say http.status.
 */
func sourceColumns(columns []catalog.Column) []parquetio.Column {
	out := make([]parquetio.Column, 0, len(columns))

	for _, column := range columns {
		out = append(out, parquetio.Column{
			Name:        column.Name,
			SourceKey:   column.SourceKey,
			Kind:        parquetio.ParseKind(column.Kind),
			Polymorphic: column.Polymorphic,
		})
	}

	return out
}

// run drives the periodic flush and WAL sync.
func (d *Durable) run() {
	defer close(d.done)

	flush := time.NewTicker(d.opts.FlushInterval)
	defer flush.Stop()

	syncInterval := d.log.SyncInterval()
	if syncInterval <= 0 {
		syncInterval = wal.DefaultSyncInterval
	}

	sync := time.NewTicker(syncInterval)
	defer sync.Stop()

	gc := time.NewTicker(d.opts.Grace)
	defer gc.Stop()

	for {
		select {
		case <-d.stop:
			return

		case <-sync.C:
			if err := d.log.Sync(); nil != err {
				d.opts.Log.Error("syncing the write-ahead log", "err", err)
			}

		case <-flush.C:
			if err := d.Flush(context.Background()); nil != err {
				d.opts.Log.Error("flushing", "err", err)
			}

		case <-gc.C:
			if err := d.collect(context.Background(), time.Now()); nil != err {
				d.opts.Log.Error("collecting garbage", "err", err)
			}
		}
	}
}

/*!
 * Append records one event.
 *
 * Everything from the sequence number to the publish happens under one lock,
 * ingest, which the rotation that seals a segment also takes. That makes four
 * orders the same order — seq, the WAL, the batch, and what subscribers hear —
 * and each of them is relied on:
 *
 *   - The WAL and the batch. A frame written to segment N whose batch insert
 *     lost the race to a flush landed in the *next* batch, after N had been
 *     sealed, persisted without it, and removed. A crash before the following
 *     flush lost the record outright.
 *   - The batch and seq. A flush names its files after the first record of
 *     each stream, and replay names them after the first record in the
 *     segment; the two only agree, and recovery is only idempotent, if those
 *     are the same record.
 *   - Publishing and seq. The live tail sends the backlog and then skips any
 *     line at or below its last seq, so a line published after a higher one
 *     was silently never shown.
 *
 * The lock is held across the WAL append, which costs a buffered write, not an
 * fsync: syncing stays on the WAL's own interval, one fsync for every record
 * that arrived in it.
 */
func (d *Durable) Append(ctx context.Context, rec *model.Record) error {
	d.ingest.Lock()
	defer d.ingest.Unlock()

	rec.Seq = d.seq.Add(1)

	// The WAL first. Everything after this point can fail and be recovered;
	// nothing before it can.
	if err := d.log.Append(rec); nil != err {
		return fmt.Errorf("appending to the write-ahead log: %w", err)
	}

	d.mu.Lock()

	buffer, known := d.streams[rec.Stream]
	if !known {
		buffer = newRing(d.opts.Backlog)
		d.streams[rec.Stream] = buffer
		d.order = append(d.order, rec.Stream)
	}
	buffer.push(rec)

	d.batch[rec.Stream] = append(d.batch[rec.Stream], rec)
	d.rows++
	d.bytes += int64(len(rec.Raw)) + 128

	var streams []string
	if !known {
		streams = append([]string(nil), d.order...)
	}

	full := d.rows >= d.opts.FlushRows || d.bytes >= d.opts.FlushBytes

	d.mu.Unlock()

	if nil != streams {
		d.fanout.PublishStreams(streams)
	}
	d.fanout.PublishLine(rec)

	/*!
	 * Flushing inline would stall ingest for the length of a Parquet write; the
	 * size trigger is a ceiling, not a deadline. But only one flush is started:
	 * every append between crossing the threshold and the swap sees the batch
	 * as full, and starting one each meant tens of thousands of goroutines,
	 * queued on the flush lock, each then writing whatever little had arrived
	 * since — a storm of tiny files.
	 */
	if full && d.kicked.CompareAndSwap(false, true) {
		go func() {
			defer d.kicked.Store(false)

			if err := d.Flush(context.Background()); nil != err {
				d.opts.Log.Error("flushing", "err", err)
			}
		}()
	}

	return nil
}

/*!
 * Flush writes everything buffered to Parquet and clears the WAL.
 *
 * Rotating the WAL is what makes this safe to run while ingest continues:
 * records appended from here on land in the new segment, and the sealed one
 * corresponds exactly to the batch being written.
 */
func (d *Durable) Flush(ctx context.Context) error {
	return d.flush(ctx, true)
}

/*!
 * flush does the work. retire is false only in tests, to reproduce a crash
 * between registering a file and dropping the segment that produced it.
 *
 * A cycle that fails never loses its batch. If the rotation fails, nothing
 * was swapped and the batch still matches the active segment. If persisting
 * fails, the sealed segment is the batch's only durable copy: it is kept, and
 * retried at the start of every cycle until it goes through. The batch used
 * to be swapped out first and dropped on either failure — leaving records
 * unqueryable until a restart replayed them, or, when the rotation was what
 * failed, still in the active segment that the next cycle sealed, persisted
 * without them, and removed.
 */
func (d *Durable) flush(ctx context.Context, retire bool) error {
	d.flushing.Lock()
	defer d.flushing.Unlock()

	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()

	if closed {
		return nil
	}

	retried := d.retry(ctx)

	sealed, batch, err := d.seal()
	if nil != err {
		return errors.Join(retried, fmt.Errorf("rotating the write-ahead log: %w", err))
	}
	if nil == batch {
		return retried
	}

	if err := d.persist(ctx, batch); nil != err {
		d.unflushed = append(d.unflushed, sealed)
		return errors.Join(retried, err)
	}

	if !retire {
		return retried
	}

	// Only now is the WAL segment redundant.
	if err := d.log.Remove(sealed); nil != err {
		d.opts.Log.Warn("could not remove a flushed wal segment", "segment", sealed, "err", err)
	}

	return retried
}

/*!
 * seal rotates the WAL and takes the batch, as one step as far as Append can
 * tell.
 *
 * The rotation fsyncs the segment it seals, and ingest waits for that — once
 * per flush, the same pause the WAL's own interval sync imposes ten times a
 * second. It cannot move outside the lock: the rotation and the swap have to
 * happen with no append between them, or segment and batch disagree.
 */
func (d *Durable) seal() (string, map[string][]*model.Record, error) {
	d.ingest.Lock()
	defer d.ingest.Unlock()

	d.mu.Lock()
	empty := d.closed || 0 == d.rows
	d.mu.Unlock()

	if empty {
		return "", nil, nil
	}

	sealed, err := d.log.Rotate()
	if nil != err {
		return "", nil, err
	}

	d.mu.Lock()
	batch := d.batch
	d.batch = make(map[string][]*model.Record)
	d.rows, d.bytes = 0, 0
	d.mu.Unlock()

	return sealed, batch, nil
}

/*!
 * retry persists segments an earlier cycle sealed but could not write.
 *
 * They are read back from the segment rather than kept in memory: a backend
 * that keeps failing would otherwise hold every batch since in RAM. Oldest
 * first, and a failure does not stop the rest — each segment is independent.
 */
func (d *Durable) retry(ctx context.Context) error {
	if 0 == len(d.unflushed) {
		return nil
	}

	var (
		errs      []error
		remaining []string
	)

	for _, segment := range d.unflushed {
		if _, _, err := d.replay(ctx, segment); nil != err {
			errs = append(errs, fmt.Errorf("retrying %s: %w", segment, err))
			remaining = append(remaining, segment)
		}
	}

	d.unflushed = remaining

	return errors.Join(errs...)
}

// persist writes one Parquet file per stream and registers each in the catalog.
func (d *Durable) persist(ctx context.Context, batch map[string][]*model.Record) error {
	for stream, records := range batch {
		if 0 == len(records) {
			continue
		}

		name := objectName(stream, records[0])

		schema, stats, err := parquetio.Write(ctx, d.backend, name, records,
			parquetio.WriteOptions{KeepRaw: d.opts.KeepRaw})
		if nil != err {
			return fmt.Errorf("writing %s: %w", name, err)
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

		_, existed, err := d.cat.Register(ctx, catalog.File{
			Stream:   stream,
			Path:     name,
			Level:    catalog.LevelL0,
			MinTs:    stats.MinTs,
			MaxTs:    stats.MaxTs,
			MinSeq:   stats.MinSeq,
			MaxSeq:   stats.MaxSeq,
			RowCount: stats.Rows,
			ByteSize: stats.Bytes,
			HasRaw:   stats.HasRaw,
		}, columns)
		if nil != err {
			// The object is committed but unregistered. It is invisible to
			// queries; the records are still in the WAL and the retry writes
			// them again under the same name, and if that never happens the
			// GC sweep collects it.
			return fmt.Errorf("registering %s: %w", name, err)
		}

		// Replay of a segment that had already been flushed. The object name
		// is derived from the batch, so it rewrote the same file with the same
		// contents and the catalog correctly ignored it.
		if existed {
			d.opts.Log.Debug("file was already registered; recovery was idempotent", "path", name)
			continue
		}

		if stats.Dropped > 0 {
			d.opts.Log.Warn("dropped columns past the per-file cap",
				"stream", stream, "dropped", stats.Dropped, "kept", len(schema.Columns()))
		}
	}

	return nil
}

/*!
 * collect deletes what no query can see any more.
 *
 * Tombstoned files whose grace period has elapsed, first; then objects the
 * catalog never knew about, in sweep. Each deletion is independent, so one
 * that fails is reported and the rest go ahead. Returning at the first meant
 * the same file failing first on every pass, in the same order, and nothing
 * behind it ever being collected again.
 */
func (d *Durable) collect(ctx context.Context, now time.Time) error {
	var errs []error

	files, err := d.cat.Collectable(ctx, now.Add(-d.opts.Grace))
	if nil != err {
		errs = append(errs, err)
	}

	for _, file := range files {
		if err := d.backend.Remove(ctx, file.Path); nil != err {
			errs = append(errs, fmt.Errorf("removing %s: %w", file.Path, err))
			continue
		}
		if err := d.cat.Forget(ctx, file.ID); nil != err {
			errs = append(errs, fmt.Errorf("forgetting %s: %w", file.Path, err))
		}
	}

	if err := d.sweep(ctx, now); nil != err {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

/*!
 * sweep removes objects no catalog row points at, and abandoned temp files.
 *
 * Both come from writes that did not finish: a flush or a compaction that
 * committed its object and then failed to register it, or one killed before
 * it committed at all. Nothing else would ever remove them, and anything
 * that globs the directory — DuckDB pointed at it by hand — reads the orphans
 * as duplicates.
 *
 * The danger is an object between its commit and its registration. It has no
 * row yet, and deleting it then leaves the catalog pointing at nothing, which
 * is the one state this design treats as unrecoverable. Three things rule it
 * out:
 *
 *   - Flushes are one of the two writers, and the sweep holds the flush lock,
 *     so no flush is between its commit and its Register.
 *   - Compaction is the other. It registers in the same call that wrote the
 *     object, a directory fsync and one transaction later, and an object is
 *     only swept once it is OrphanGrace old — a day by default, that window
 *     many thousands of times over. A rename keeps the temp file's mtime, so
 *     an object's age is the time since its last byte was written.
 *   - An object older than the catalog itself is never touched. One written
 *     before this catalog existed is not its orphan: it is what is left of a
 *     catalog that was lost or replaced, and it is somebody's data.
 *
 * All of this reasons about this process. Two servers sharing one data
 * directory was never safe — they would share a WAL — and this does not make
 * it so.
 */
func (d *Durable) sweep(ctx context.Context, now time.Time) error {
	d.flushing.Lock()
	defer d.flushing.Unlock()

	cutoff := now.Add(-d.opts.OrphanGrace)

	created, err := d.cat.Created(ctx)
	if nil != err {
		return fmt.Errorf("reading the catalog's age: %w", err)
	}

	objects, err := d.backend.List(ctx, "streams/")
	if nil != err {
		return err
	}

	// Read after listing, so an object registered in between is known.
	known, err := d.cat.Paths(ctx)
	if nil != err {
		return err
	}

	var errs []error

	for _, object := range objects {
		orphan := !known[object.Name] &&
			strings.HasSuffix(object.Name, ".parquet") &&
			object.Modified.Before(cutoff) &&
			!object.Modified.Before(created)
		if !orphan {
			continue
		}

		if err := d.backend.Remove(ctx, object.Name); nil != err {
			errs = append(errs, fmt.Errorf("removing orphan %s: %w", object.Name, err))
			continue
		}

		d.opts.Log.Info("removed an object no catalog row pointed at", "path", object.Name)
	}

	if removed, err := d.backend.Sweep(ctx, "streams/", cutoff); nil != err {
		errs = append(errs, err)
	} else if removed > 0 {
		d.opts.Log.Info("removed abandoned temp files", "count", removed)
	}

	return errors.Join(errs...)
}

/*!
 * objectName builds the on-disk path.
 *
 * Hive-ish and human-navigable on purpose: handing someone a directory they
 * can open in DuckDB without rtail installed is the anti-lock-in argument, and
 * it only holds if the layout makes sense to a person with `ls`.
 */
func objectName(stream string, first *model.Record) string {
	day := first.Ts.UTC()

	return fmt.Sprintf("streams/%s/%04d/%02d/%02d/L0-%020d-%d.parquet",
		safeSegment(stream),
		day.Year(), int(day.Month()), day.Day(),
		first.Seq, day.UnixMicro(),
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

	// A name of dots would escape the directory tree.
	if "." == string(out) || ".." == string(out) {
		return "_" + string(out)
	}

	return string(out)
}

func (d *Durable) Streams(context.Context) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.order...), nil
}

/*!
 * Backlog returns recent records, merging every stream when none is named.
 *
 * "All streams" is the explorer's default view, and a tail that opens empty
 * and then trickles looks broken next to one that opens with the last hundred
 * lines already there.
 */
func (d *Durable) Backlog(_ context.Context, stream string, limit int) ([]*model.Record, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if "" != stream {
		buffer, ok := d.streams[stream]
		if !ok {
			return nil, nil
		}
		return buffer.slice(limit), nil
	}

	var merged []*model.Record
	for _, buffer := range d.streams {
		merged = append(merged, buffer.slice(0)...)
	}

	// Seq is monotonic across streams, so it is the merge order.
	sort.Slice(merged, func(i, j int) bool { return merged[i].Seq < merged[j].Seq })

	if limit <= 0 {
		limit = d.opts.Backlog
	}
	if len(merged) > limit {
		merged = merged[len(merged)-limit:]
	}

	return merged, nil
}

func (d *Durable) Subscribe(stream string, buffer int) *Subscription {
	return d.fanout.Subscribe(stream, buffer)
}

// Catalog exposes the metadata store, which P2's query planner needs.
func (d *Durable) Catalog() *catalog.Catalog { return d.cat }

// Backend exposes the object store, which P2's reader needs.
func (d *Durable) Backend() storage.Backend { return d.backend }

/*!
 * Close flushes everything buffered and shuts down cleanly.
 *
 * A clean shutdown that left records only in the WAL would be correct — they
 * would replay on the next boot — but it would also mean a restart silently
 * rewrites work it had already done. Flushing here keeps restarts cheap.
 */
func (d *Durable) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	d.mu.Unlock()

	close(d.stop)
	<-d.done

	// Flush checks d.closed, so clear it for this last cycle.
	d.mu.Lock()
	d.closed = false
	d.mu.Unlock()

	err := d.Flush(context.Background())

	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()

	d.fanout.Close()

	if closeErr := d.closeParts(); nil == err {
		err = closeErr
	}

	return err
}

func (d *Durable) closeParts() error {
	var err error

	if nil != d.log {
		err = d.log.Close()
	}
	if nil != d.cat {
		if closeErr := d.cat.Close(); nil == err {
			err = closeErr
		}
	}
	if nil != d.backend {
		if closeErr := d.backend.Close(); nil == err {
			err = closeErr
		}
	}

	return err
}
