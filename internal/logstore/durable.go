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
 *   - between register and remove: replay finds the path already registered
 *     and writes nothing.
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

	// DefaultOrphanGrace is how long a write declared and never registered
	// has before garbage collection deletes its object. See sweep.
	DefaultOrphanGrace = 24 * time.Hour
)

// sweepInterval is how often garbage collection looks for writes that never
// finished. They are rare, and a day old before they qualify; looking every
// Grace, as it did, was a catalog query twelve times an hour for nothing.
const sweepInterval = time.Hour

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

	// quiet holds the size trigger off until this time, in Unix nanoseconds,
	// after a rotation failed; backoff is how long the last wait was, and is
	// guarded by flushing. See backOff.
	quiet   atomic.Int64
	backoff time.Duration

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
 *
 * A segment that cannot be persisted does not stop the server booting. It
 * goes on the retry list, as one the live path failed to persist would, and
 * every flush tries it again; one stream whose files could not be written —
 * an id too long to be a directory name was the case that did it — used to
 * keep every other stream from being served at all, on every restart. Its
 * records still count towards seq, so nothing appended from here on can sort
 * before them.
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

		if maxSeq > d.seq.Load() {
			d.seq.Store(maxSeq)
		}

		if nil != err {
			d.opts.Log.Error("could not replay a write-ahead log segment; every flush will retry it",
				"segment", segment, "err", err)
			d.unflushed = append(d.unflushed, segment)
			continue
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
 *
 * A segment damaged part way through cannot be replayed whole, ever, and used
 * to stop the server booting. The records before the damage are intact —
 * every frame is checksummed — so they are persisted, and the segment is
 * quarantined: moved out of the log, never replayed again, and kept for
 * whoever wants to look at what could not be read. The count and the highest
 * seq are returned whatever happens, so a caller can account for them.
 */
func (d *Durable) replay(ctx context.Context, segment string) (int, uint64, error) {
	batch := map[string][]*model.Record{}

	var (
		count  int
		maxSeq uint64
	)

	damaged := d.log.Replay(segment, func(rec *model.Record) error {
		batch[rec.Stream] = append(batch[rec.Stream], rec)
		count++

		if rec.Seq > maxSeq {
			maxSeq = rec.Seq
		}

		return nil
	})
	if nil != damaged && !errors.Is(damaged, wal.ErrCorrupt) {
		return count, maxSeq, fmt.Errorf("replaying %s: %w", segment, damaged)
	}

	if count > 0 {
		if err := d.persist(ctx, batch); nil != err {
			return count, maxSeq, fmt.Errorf("persisting %s: %w", segment, err)
		}
	}

	if nil != damaged {
		moved, err := d.log.Quarantine(segment)
		if nil != err {
			return count, maxSeq, errors.Join(damaged, fmt.Errorf("quarantining %s: %w", segment, err))
		}

		d.opts.Log.Error("quarantined a damaged write-ahead log segment: "+
			"the records before the damage were recovered, any after it were not",
			"segment", segment, "moved_to", moved, "recovered", count, "err", damaged)

		return count, maxSeq, nil
	}

	if err := d.log.Remove(segment); nil != err {
		return count, maxSeq, err
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

	sweep := time.NewTicker(sweepInterval)
	defer sweep.Stop()

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

		case <-sweep.C:
			if err := d.sweep(context.Background(), time.Now()); nil != err {
				d.opts.Log.Error("sweeping unfinished writes", "err", err)
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
 * The lock is held across the WAL append, which costs a copy into the WAL's
 * buffer and, once that is full, a write into the page cache — never an
 * fsync. A sync, whether the WAL's own interval or an HTTP batch asking for
 * one, runs its fsync without the lock appends need, so ingest carries on
 * underneath it. The one fsync appends do wait for is a rotation's, once per
 * flush; see seal.
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
	 * since — a storm of tiny files. And none is started while a failed
	 * rotation is being backed off from; see backOff.
	 */
	if full && time.Now().UnixNano() >= d.quiet.Load() && d.kicked.CompareAndSwap(false, true) {
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
		d.backOff()
		return errors.Join(retried, fmt.Errorf("rotating the write-ahead log: %w", err))
	}

	if nil == batch {
		return retried
	}

	d.backoff = 0
	d.quiet.Store(0)

	if err := d.persist(ctx, batch); nil != err {
		d.unflushed = append(d.unflushed, sealed)
		return errors.Join(retried, err)
	}

	if !retire {
		return retried
	}

	/*!
	 * Only now is the WAL segment redundant. One that cannot be removed goes
	 * on the retry list, where the next cycle finds every stream in it
	 * registered, writes nothing, and tries the removal again. Left alone, it
	 * waited for a restart — and while it is on disk, garbage collection has
	 * to keep the rows a replay of it would look for; see collect.
	 */
	if err := d.log.Remove(sealed); nil != err {
		d.opts.Log.Warn("could not remove a flushed wal segment", "segment", sealed, "err", err)
		d.unflushed = append(d.unflushed, sealed)
	}

	return retried
}

/*!
 * backOff holds the size trigger off after a rotation fails.
 *
 * A failed seal leaves the batch where it was, still over the threshold, so
 * every append after it saw a full batch and started another flush the
 * moment the last one gave up: each re-reading every segment on the retry
 * list and attempting an fsync under the ingest lock, as fast as appends
 * arrived. The wait starts at a second and doubles up to the flush interval,
 * where the timer takes over anyway; the first rotation that succeeds lifts
 * it. Called under flushing.
 */
func (d *Durable) backOff() {
	d.backoff = min(max(2*d.backoff, time.Second), d.opts.FlushInterval)
	d.quiet.Store(time.Now().Add(d.backoff).UnixNano())
}

/*!
 * seal rotates the WAL and takes the batch, as one step as far as Append can
 * tell.
 *
 * The rotation fsyncs the segment it seals, and ingest waits for that, once
 * per flush. It cannot move outside the lock: the rotation and the swap have
 * to happen with no append between them, or segment and batch disagree.
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
 * Within a segment, the streams that did land the first time are found
 * registered and not written again; only the ones that failed are retried.
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

/*!
 * persist writes one Parquet file per stream and registers each in the catalog.
 *
 * Every path is declared to the catalog before anything is written, which is
 * how garbage collection can later find an object that was committed and
 * never registered — see sweep — and how a replay learns which of its
 * segment's streams already landed. Those are not written again: rewriting
 * them was at best wasted work, and once their rows had been compacted and
 * collected it put the same records back on disk beside the compacted copy.
 *
 * A stream that fails does not stop the others, and the errors come back
 * together. Stopping at the first meant one stream that could never be
 * written kept every stream in the same batch out of the catalog, and a
 * retry wrote the healthy ones again on every cycle.
 */
func (d *Durable) persist(ctx context.Context, batch map[string][]*model.Record) error {
	streams := make([]string, 0, len(batch))
	names := make([]string, 0, len(batch))

	for stream, records := range batch {
		if 0 != len(records) {
			streams = append(streams, stream)
		}
	}

	// Map order is random; failures should not be.
	sort.Strings(streams)

	for _, stream := range streams {
		names = append(names, objectName(stream, batch[stream][0]))
	}

	if 0 == len(names) {
		return nil
	}

	registered, err := d.cat.Intend(ctx, names...)
	if nil != err {
		return fmt.Errorf("declaring the files about to be written: %w", err)
	}

	var errs []error

	for i, stream := range streams {
		// A replay of a segment that had already been flushed, at least this
		// far. The object name is derived from the batch, so this is the file
		// that flush registered.
		if registered[names[i]] {
			d.opts.Log.Debug("file was already registered; recovery was idempotent", "path", names[i])
			continue
		}

		if err := d.write(ctx, stream, names[i], batch[stream]); nil != err {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// write writes and registers the file for one stream of a batch.
func (d *Durable) write(ctx context.Context, stream, name string, records []*model.Record) error {
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

	if _, _, err := d.cat.Register(ctx, catalog.File{
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
	}, columns); nil != err {
		// The object is committed but unregistered. It is invisible to
		// queries; the records are still in the WAL and the retry writes
		// them again under the same name, and if that never happens the
		// declaration persist made lets the GC sweep collect it.
		return fmt.Errorf("registering %s: %w", name, err)
	}

	if stats.Dropped > 0 {
		d.opts.Log.Warn("dropped columns past the per-file cap",
			"stream", stream, "dropped", stats.Dropped, "kept", len(schema.Columns()))
	}

	return nil
}

/*!
 * collect deletes tombstoned files whose grace period has elapsed.
 *
 * Each deletion is independent, so one that fails is reported and the rest go
 * ahead. Returning at the first meant the same file failing first on every
 * pass, in the same order, and nothing behind it ever being collected again.
 *
 * The object always goes; the catalog row of an L0 file does not, while a
 * WAL segment still on disk could produce it again. Recovery is idempotent
 * only because a replay finds the files its segment already produced
 * registered, by path — so a row forgotten under a segment that is later
 * replayed (one on the retry list, or one a failed removal left behind) let
 * the replay register the same records again beside the compacted copy that
 * already held them. And the next compaction, naming its output after that
 * same first record, wrote over the live compacted file. Seq only grows from
 * one segment to the next, so a row whose max_seq is below the first record
 * of the oldest sealed segment on disk is one no replay can produce; the rest
 * stay, objects already gone, until their segment has been retired.
 */
func (d *Durable) collect(ctx context.Context, now time.Time) error {
	var errs []error

	files, err := d.cat.Collectable(ctx, now.Add(-d.opts.Grace))
	if nil != err {
		errs = append(errs, err)
	}

	floor, bounded, err := d.log.Floor()
	if nil != err {
		// Keep every row this pass rather than guess.
		errs = append(errs, fmt.Errorf("finding the oldest record a replay could produce: %w", err))
		floor, bounded = 0, true
	}

	for _, file := range files {
		if err := d.backend.Remove(ctx, file.Path); nil != err {
			errs = append(errs, fmt.Errorf("removing %s: %w", file.Path, err))
			continue
		}

		if catalog.LevelL0 == file.Level && bounded && file.MaxSeq >= floor {
			continue
		}

		if err := d.cat.Forget(ctx, file.ID); nil != err {
			errs = append(errs, fmt.Errorf("forgetting %s: %w", file.Path, err))
		}
	}

	return errors.Join(errs...)
}

/*!
 * sweep deletes what writes that never finished left behind, and nothing
 * else.
 *
 * Two kinds of thing: objects a flush or a compaction declared it was about
 * to write and then never registered — committed, and then the process died
 * or the catalog refused, before the transaction that would have made them
 * live — and temp files from writes killed before they committed, which only
 * Create ever makes. A declaration names the exact path, was made by the
 * writer itself, and is dropped by the transaction that registers the object;
 * see catalog.Intend. An object with no declaration is never deleted here,
 * whatever it looks like.
 *
 * It used to delete every object under streams/ that no catalog row pointed
 * at, comparing the names a directory listing returned with the paths in the
 * catalog, byte for byte. On a case-insensitive filesystem — macOS's default,
 * Windows, a Docker Desktop bind mount — streams "api" and "API" share one
 * directory, spelled however the first of them spelled it, so every object of
 * the other, compacted ones included, listed under a name no row had and was
 * deleted a day later. And a catalog restored from a backup, or one that lost
 * its -wal file, has no row for anything registered after that point: all of
 * it real data, its WAL long gone, all of it deleted.
 *
 * A declaration is acted on once it is OrphanGrace old, and catalog.Discard
 * checks it again under the catalog's write lock while the object is removed.
 * A write that retries the same path declares it again first, which makes it
 * young — so the sweep never deletes an object a write in progress is about
 * to register. No flush lock is needed for that, and none is held.
 *
 * What remains is narrow: a catalog restored from a backup taken in the
 * moment between one write's declaration and its registration still holds
 * that declaration, and that one object is deleted. Restoring the catalog on
 * its own was never supported — seq restarts behind the objects on disk —
 * but it no longer costs everything written since.
 *
 * All of this reasons about one process. Two servers sharing one data
 * directory was never safe — they would share a WAL — and this does not make
 * it so.
 */
func (d *Durable) sweep(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-d.opts.OrphanGrace)

	var errs []error

	abandoned, err := d.cat.Abandoned(ctx, cutoff)
	if nil != err {
		errs = append(errs, fmt.Errorf("listing unfinished writes: %w", err))
	}

	for _, path := range abandoned {
		removed, err := d.cat.Discard(ctx, path, cutoff, func() error {
			return d.backend.Remove(ctx, path)
		})
		if nil != err {
			errs = append(errs, fmt.Errorf("removing unfinished write %s: %w", path, err))
			continue
		}

		if removed {
			d.opts.Log.Info("removed an object that was written and never registered", "path", path)
		}
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
		storage.SafeSegment(stream),
		day.Year(), int(day.Month()), day.Day(),
		first.Seq, day.UnixMicro(),
	)
}

// Sync fsyncs the write-ahead log, so every record appended before the call is
// on disk when it returns — what the HTTP receivers wait for before they
// acknowledge. The periodic sync still runs for everything else, and a sync
// with nothing new to write costs a mutex.
func (d *Durable) Sync(context.Context) error { return d.log.Sync() }

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
