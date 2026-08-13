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
 */

package logstore

import (
	"context"
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

	mu      sync.Mutex
	order   []string
	streams map[string]*ring
	batch   map[string][]*model.Record
	rows    int
	bytes   int64
	closed  bool

	// flushing serializes flush cycles without holding mu across the I/O.
	flushing sync.Mutex

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
		batch := map[string][]*model.Record{}
		count := 0

		if err := d.log.Replay(segment, func(rec *model.Record) error {
			batch[rec.Stream] = append(batch[rec.Stream], rec)
			count++

			if rec.Seq > d.seq.Load() {
				d.seq.Store(rec.Seq)
			}

			return nil
		}); nil != err {
			return fmt.Errorf("replaying %s: %w", segment, err)
		}

		if 0 == count {
			if err := d.log.Remove(segment); nil != err {
				return err
			}
			continue
		}

		if err := d.persist(ctx, batch); nil != err {
			return fmt.Errorf("persisting %s: %w", segment, err)
		}

		if err := d.log.Remove(segment); nil != err {
			return err
		}

		d.opts.Log.Info("replayed segment", "segment", segment, "records", count)
	}

	return nil
}

/*!
 * loadBacklog repopulates the in-memory ring from the newest files on disk, so
 * a restarted server shows history instead of an empty pane.
 *
 * This reads whole files, which is fine for L0 and would not be for L2. It is
 * bounded to the most recent file per stream for exactly that reason; P2's
 * query engine replaces it with something that can ask for the last N rows
 * without reading everything before them.
 */
func (d *Durable) loadBacklog(ctx context.Context) error {
	streams, err := d.cat.Streams(ctx)
	if nil != err {
		return err
	}

	for _, stream := range streams {
		files, err := d.cat.Prune(ctx, catalog.Query{Stream: stream.Name})
		if nil != err {
			return err
		}
		if 0 == len(files) {
			continue
		}

		newest := files[len(files)-1]

		records, err := parquetio.Read(ctx, d.backend, newest.Path, d.opts.Backlog)
		if nil != err {
			return err
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
			if err := d.collect(context.Background()); nil != err {
				d.opts.Log.Error("collecting tombstoned files", "err", err)
			}
		}
	}
}

func (d *Durable) Append(ctx context.Context, rec *model.Record) error {
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

	if full {
		// Flushing inline would stall ingest for the length of a Parquet
		// write; the size trigger is a ceiling, not a deadline.
		go func() {
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
 * Rotating the WAL first is what makes this safe to run while ingest
 * continues: records appended from here on land in the new segment, and the
 * sealed one corresponds exactly to the batch being written.
 */
func (d *Durable) Flush(ctx context.Context) error {
	return d.flush(ctx, true)
}

// flush does the work. retire is false only in tests, to reproduce a crash
// between registering a file and dropping the segment that produced it.
func (d *Durable) flush(ctx context.Context, retire bool) error {
	d.flushing.Lock()
	defer d.flushing.Unlock()

	d.mu.Lock()
	if d.closed || 0 == d.rows {
		d.mu.Unlock()
		return nil
	}
	batch := d.batch
	d.batch = make(map[string][]*model.Record)
	d.rows, d.bytes = 0, 0
	d.mu.Unlock()

	sealed, err := d.log.Rotate()
	if nil != err {
		return fmt.Errorf("rotating the write-ahead log: %w", err)
	}

	if err := d.persist(ctx, batch); nil != err {
		return err
	}

	if !retire {
		return nil
	}

	// Only now is the WAL segment redundant.
	if err := d.log.Remove(sealed); nil != err {
		d.opts.Log.Warn("could not remove a flushed wal segment", "segment", sealed, "err", err)
	}

	return nil
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
			// queries and the GC pass will collect it; the records are still
			// in the WAL and will be written again.
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

// collect deletes tombstoned objects whose grace period has elapsed.
func (d *Durable) collect(ctx context.Context) error {
	files, err := d.cat.Collectable(ctx, time.Now().Add(-d.opts.Grace))
	if nil != err {
		return err
	}

	for _, file := range files {
		if err := d.backend.Remove(ctx, file.Path); nil != err {
			return err
		}
		if err := d.cat.Forget(ctx, file.ID); nil != err {
			return err
		}
	}

	return nil
}

/*!
 * objectName builds the on-disk path, per §3.4.
 *
 * Hive-ish and human-navigable on purpose: handing someone a directory they
 * can open in DuckDB without rTail installed is the anti-lock-in argument, and
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
