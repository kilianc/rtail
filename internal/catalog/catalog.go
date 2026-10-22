/*!
 * The catalog.
 *
 * A transactional index over the Parquet files on disk: what exists, what is
 * in it, and which slice of time and sequence it covers. Iceberg, minus 999
 * pages of spec.
 *
 * SQLite rather than DuckDB, even though DuckDB is arriving in P2 anyway: this
 * workload is small frequent transactional writes and point lookups, which is
 * exactly what an OLAP engine is worst at and what SQLite has spent twenty
 * years being good at. They coexist — SQLite decides *which* files to read,
 * DuckDB reads them.
 *
 * Registration is one transaction covering the file, its columns, the stream
 * rollup and the key inventory. A file that exists on disk but half-exists in
 * the catalog is the corruption mode this whole design is built to avoid, so
 * it must never be reachable.
 */

package catalog

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// File states.
const (
	StateLive       = "live"
	StateCompacting = "compacting"
	StateTombstoned = "tombstoned"
)

// Levels in the LSM-ish hierarchy: L0 is a flush, L1 hourly, L2 daily.
const (
	LevelL0 = 0
	LevelL1 = 1
	LevelL2 = 2
)

// File describes one Parquet file.
type File struct {
	ID       int64
	Stream   string
	Path     string
	Level    int
	MinTs    time.Time
	MaxTs    time.Time
	MinSeq   uint64
	MaxSeq   uint64
	RowCount int64
	ByteSize int64
	HasRaw   bool
	State    string
	Created  time.Time
}

// Column describes one promoted column within a file.
type Column struct {
	Name        string
	SourceKey   string
	Kind        string
	Polymorphic bool
	NullCount   int64
	MinValue    string
	MaxValue    string
	HasRange    bool
}

// Stream is the per-stream rollup.
type Stream struct {
	Name      string
	FirstSeen time.Time
	LastSeen  time.Time
	RowCount  int64
	ByteSize  int64
	Dedicated bool
}

// SchemaKey is one entry in the autocomplete inventory.
type SchemaKey struct {
	Stream      string
	SourceKey   string
	Kind        string
	Polymorphic bool
	Occurrences int64
	FirstSeen   time.Time
	LastSeen    time.Time
}

// Catalog is the metadata store.
type Catalog struct {
	db *sql.DB
}

/*!
 * Open creates or opens the catalog at path.
 *
 * MaxOpenConns(1) is deliberate. SQLite in WAL mode allows one writer and many
 * readers, but reaching that safely through database/sql means separate read
 * and write pools, and the catalog is not a hot path — writes happen once per
 * flush, and a prune query is sub-millisecond. When P2 shows contention on
 * prune, the fix is a second read-only pool, not a rewrite.
 */
func Open(path string) (*Catalog, error) {
	// _txlock=immediate takes the write lock at BEGIN rather than on first
	// write, which turns a potential mid-transaction SQLITE_BUSY into a clean
	// wait at the start.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_txlock=immediate"

	db, err := sql.Open("sqlite", dsn)
	if nil != err {
		return nil, fmt.Errorf("opening catalog: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schemaSQL); nil != err {
		db.Close()
		return nil, fmt.Errorf("creating catalog schema: %w", err)
	}

	return &Catalog{db: db}, nil
}

// Close releases the database.
func (c *Catalog) Close() error { return c.db.Close() }

/*!
 * Register records a newly written file and everything derived from it.
 *
 * One transaction, so the file either exists in the catalog completely or not
 * at all. The caller must have committed the object to storage first: a
 * catalog row pointing at a file that is not there is unrecoverable, whereas a
 * file with no catalog row is merely garbage that the next GC pass collects.
 *
 * Registering a path that is already known is a no-op returning existed=true.
 * That is what makes crash recovery idempotent rather than merely
 * duplicate-tolerant: object names are derived from the batch's first record,
 * so replaying a WAL segment that was already flushed produces the same path,
 * lands on this branch, and adds nothing. Without it, the rollups in `streams`
 * would be double-counted every time a crash landed between the catalog commit
 * and the segment being retired.
 */
func (c *Catalog) Register(ctx context.Context, file File, columns []Column) (int64, bool, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if nil != err {
		return 0, false, err
	}
	defer tx.Rollback()

	id, existed, err := insertFileTx(ctx, tx, file, columns, rollupAdd|countOccurrences)
	if nil != err {
		return 0, false, err
	}

	if err := tx.Commit(); nil != err {
		return 0, false, fmt.Errorf("committing file registration: %w", err)
	}

	return id, existed, nil
}

/*!
 * insertMode tunes what a file insert does to the cumulative statistics.
 *
 * Compaction rewrites rows that are already counted, so it publishes files
 * without re-counting their keys: the occurrences in schema_keys drive
 * autocomplete ranking, and adding them again on every compaction would inflate
 * a key's apparent frequency without bound. The stream rollup is adjusted by a
 * delta instead — added here, subtracted when the inputs are tombstoned.
 */
type insertMode uint8

const (
	rollupAdd insertMode = 1 << iota
	countOccurrences
)

// insertFileTx inserts a file and its columns inside an existing transaction.
func insertFileTx(
	ctx context.Context,
	tx *sql.Tx,
	file File,
	columns []Column,
	mode insertMode,
) (int64, bool, error) {
	var existing int64
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM files WHERE path = ?`, file.Path).Scan(&existing); {
	case nil == err:
		return existing, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, err
	}

	if "" == file.State {
		file.State = StateLive
	}
	if file.Created.IsZero() {
		file.Created = time.Now().UTC()
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO files
			(stream, path, level, min_ts, max_ts, min_seq, max_seq,
			 row_count, byte_size, has_raw, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		file.Stream, file.Path, file.Level,
		file.MinTs.UnixMicro(), file.MaxTs.UnixMicro(),
		int64(file.MinSeq), int64(file.MaxSeq),
		file.RowCount, file.ByteSize, boolToInt(file.HasRaw),
		file.State, file.Created.UnixMicro(),
	)
	if nil != err {
		return 0, false, fmt.Errorf("inserting file: %w", err)
	}

	id, err := result.LastInsertId()
	if nil != err {
		return 0, false, err
	}

	for _, column := range columns {
		var min, max any
		if column.HasRange {
			min, max = column.MinValue, column.MaxValue
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO file_columns
				(file_id, column_name, source_key, kind, polymorphic, null_count, min_value, max_value)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, column.Name, column.SourceKey, column.Kind,
			boolToInt(column.Polymorphic), column.NullCount, min, max,
		); nil != err {
			return 0, false, fmt.Errorf("inserting column %s: %w", column.Name, err)
		}

		// The key inventory is cumulative across files. A key that was an int
		// in one file and a string in another is marked polymorphic forever,
		// which is what the UI needs to know to stop offering numeric
		// comparisons on it.
		//
		// occurrences is zero unless this insert represents genuinely new rows;
		// a compaction rewrites rows that were already counted.
		var occurrences int64
		if 0 != mode&countOccurrences {
			occurrences = file.RowCount - column.NullCount
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_keys
				(stream, source_key, kind, polymorphic, occurrences, first_seen, last_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(stream, source_key) DO UPDATE SET
				occurrences = occurrences + excluded.occurrences,
				last_seen   = max(last_seen, excluded.last_seen),
				polymorphic = polymorphic | excluded.polymorphic
					| (kind <> excluded.kind),
				kind        = CASE WHEN kind = excluded.kind THEN kind ELSE 'string' END`,
			file.Stream, column.SourceKey, column.Kind, boolToInt(column.Polymorphic),
			occurrences, file.MinTs.UnixMicro(), file.MaxTs.UnixMicro(),
		); nil != err {
			return 0, false, fmt.Errorf("recording schema key %s: %w", column.SourceKey, err)
		}
	}

	if 0 != mode&rollupAdd {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO streams (name, first_seen, last_seen, row_count, byte_size)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				first_seen = min(first_seen, excluded.first_seen),
				last_seen  = max(last_seen,  excluded.last_seen),
				row_count  = row_count + excluded.row_count,
				byte_size  = byte_size + excluded.byte_size`,
			file.Stream, file.MinTs.UnixMicro(), file.MaxTs.UnixMicro(), file.RowCount, file.ByteSize,
		); nil != err {
			return 0, false, fmt.Errorf("updating stream rollup: %w", err)
		}
	}

	if err := setMetaTx(ctx, tx, metaMaxSeq, file.MaxSeq); nil != err {
		return 0, false, err
	}

	return id, false, nil
}

/*!
 * Replacement is one output file of a compaction, with its columns.
 */
type Replacement struct {
	File    File
	Columns []Column
}

/*!
 * Replace publishes compacted files and retires their inputs, atomically.
 *
 * This is the operation the whole compactor is built around. Either the new
 * files are live and the old ones tombstoned, or nothing changed — there is no
 * window in which a row exists twice or not at all, because a reader only ever
 * sees `state = 'live'` and that flips for every file in one commit.
 *
 * The inputs are only *tombstoned*, never deleted. A query that snapshotted the
 * file list a moment ago still has its files on disk until the GC grace period
 * elapses; see Collectable.
 *
 * Idempotent, which is what makes crash recovery work: output paths are derived
 * from their content, so a compaction that died after writing the objects but
 * before committing rewrites the same files and lands here again. An output
 * whose path already exists is skipped, and an input that is already tombstoned
 * is not retired twice — both of which matter because the stream rollups are
 * counters, and applying either twice would corrupt them permanently.
 */
func (c *Catalog) Replace(ctx context.Context, outputs []Replacement, inputs []int64) ([]int64, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if nil != err {
		return nil, err
	}
	defer tx.Rollback()

	ids := make([]int64, 0, len(outputs))

	for _, output := range outputs {
		id, existed, err := insertFileTx(ctx, tx, output.File, output.Columns, rollupAdd)
		if nil != err {
			return nil, err
		}

		// Already published by an earlier attempt at this same compaction.
		if existed {
			ids = append(ids, id)
			continue
		}

		ids = append(ids, id)
	}

	now := time.Now().UTC().UnixMicro()

	for _, id := range inputs {
		result, err := tx.ExecContext(ctx, `
			UPDATE files SET state = ?, tombstoned_at = ?
			WHERE id = ? AND state = ?`,
			StateTombstoned, now, id, StateLive)
		if nil != err {
			return nil, fmt.Errorf("tombstoning %d: %w", id, err)
		}

		affected, err := result.RowsAffected()
		if nil != err {
			return nil, err
		}

		// Already retired: its contribution was subtracted the first time.
		if 0 == affected {
			continue
		}

		if err := adjustRollupTx(ctx, tx, id, -1); nil != err {
			return nil, err
		}
	}

	if err := tx.Commit(); nil != err {
		return nil, fmt.Errorf("committing replacement: %w", err)
	}

	return ids, nil
}

/*!
 * adjustRollupTx applies a file's row and byte counts to its stream, scaled by
 * sign. Compaction leaves rows unchanged and bytes smaller, so the rollup has
 * to move by the delta rather than be recomputed — recomputing would mean a
 * full scan of `files` on every compaction.
 */
func adjustRollupTx(ctx context.Context, tx *sql.Tx, fileID int64, sign int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE streams SET
			row_count = max(0, row_count + ? * (SELECT row_count FROM files WHERE id = ?)),
			byte_size = max(0, byte_size + ? * (SELECT byte_size FROM files WHERE id = ?))
		WHERE name = (SELECT stream FROM files WHERE id = ?)`,
		sign, fileID, sign, fileID, fileID)

	return err
}

/*!
 * Query describes a file-pruning request.
 *
 * This is the shape the P2 planner will build from an rQL predicate: a time
 * range, optionally a stream, optionally a requirement that a key exists.
 */
type Query struct {
	Stream string
	From   time.Time
	To     time.Time
	// RequireKey restricts to files that actually have a column for this
	// source key. Only safe when raw is not being consulted as a fallback.
	RequireKey string
	Levels     []int
	Limit      int
}

/*!
 * Prune returns the files that could contain matching rows.
 *
 * Overlap, not containment: files are explicitly allowed to overlap in time,
 * because a record that arrives three hours late lands in the current flush
 * and widens that file's range. A planner that assumed disjoint files would
 * silently miss it.
 */
func (c *Catalog) Prune(ctx context.Context, q Query) ([]File, error) {
	var (
		conditions = []string{"f.state = ?"}
		args       = []any{StateLive}
	)

	if "" != q.Stream {
		conditions = append(conditions, "f.stream = ?")
		args = append(args, q.Stream)
	}
	if !q.From.IsZero() {
		conditions = append(conditions, "f.max_ts >= ?")
		args = append(args, q.From.UnixMicro())
	}
	if !q.To.IsZero() {
		conditions = append(conditions, "f.min_ts <= ?")
		args = append(args, q.To.UnixMicro())
	}
	if 0 != len(q.Levels) {
		placeholders := make([]string, len(q.Levels))
		for i, level := range q.Levels {
			placeholders[i] = "?"
			args = append(args, level)
		}
		conditions = append(conditions, "f.level IN ("+strings.Join(placeholders, ",")+")")
	}
	if "" != q.RequireKey {
		conditions = append(conditions,
			"EXISTS (SELECT 1 FROM file_columns c WHERE c.file_id = f.id AND c.source_key = ?)")
		args = append(args, q.RequireKey)
	}

	query := `
		SELECT f.id, f.stream, f.path, f.level, f.min_ts, f.max_ts, f.min_seq, f.max_seq,
		       f.row_count, f.byte_size, f.has_raw, f.state, f.created_at
		FROM files f
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY f.min_ts, f.min_seq`

	if q.Limit > 0 {
		query += " LIMIT " + strconv.Itoa(q.Limit)
	}

	rows, err := c.db.QueryContext(ctx, query, args...)
	if nil != err {
		return nil, err
	}
	defer rows.Close()

	return scanFiles(rows)
}

// Columns lists the promoted columns of one file.
func (c *Catalog) Columns(ctx context.Context, fileID int64) ([]Column, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT column_name, source_key, kind, polymorphic, null_count, min_value, max_value
		FROM file_columns WHERE file_id = ? ORDER BY column_name`, fileID)
	if nil != err {
		return nil, err
	}
	defer rows.Close()

	var columns []Column

	for rows.Next() {
		var (
			column   Column
			poly     int
			min, max sql.NullString
		)

		if err := rows.Scan(&column.Name, &column.SourceKey, &column.Kind,
			&poly, &column.NullCount, &min, &max); nil != err {
			return nil, err
		}

		column.Polymorphic = 0 != poly
		column.MinValue, column.MaxValue = min.String, max.String
		column.HasRange = min.Valid && max.Valid

		columns = append(columns, column)
	}

	return columns, rows.Err()
}

// Streams lists every known stream, most recently active first.
func (c *Catalog) Streams(ctx context.Context) ([]Stream, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT name, first_seen, last_seen, row_count, byte_size, dedicated
		FROM streams ORDER BY last_seen DESC`)
	if nil != err {
		return nil, err
	}
	defer rows.Close()

	var streams []Stream

	for rows.Next() {
		var (
			stream      Stream
			first, last int64
			dedicated   int
		)

		if err := rows.Scan(&stream.Name, &first, &last,
			&stream.RowCount, &stream.ByteSize, &dedicated); nil != err {
			return nil, err
		}

		stream.FirstSeen = time.UnixMicro(first).UTC()
		stream.LastSeen = time.UnixMicro(last).UTC()
		stream.Dedicated = 0 != dedicated

		streams = append(streams, stream)
	}

	return streams, rows.Err()
}

// SchemaKeys returns the key inventory for a stream, or for every stream when
// stream is empty. This is what the search bar completes against.
func (c *Catalog) SchemaKeys(ctx context.Context, stream string) ([]SchemaKey, error) {
	query := `
		SELECT stream, source_key, kind, polymorphic, occurrences, first_seen, last_seen
		FROM schema_keys`
	var args []any

	if "" != stream {
		query += " WHERE stream = ?"
		args = append(args, stream)
	}

	query += " ORDER BY occurrences DESC, source_key"

	rows, err := c.db.QueryContext(ctx, query, args...)
	if nil != err {
		return nil, err
	}
	defer rows.Close()

	var keys []SchemaKey

	for rows.Next() {
		var (
			key         SchemaKey
			poly        int
			first, last int64
		)

		if err := rows.Scan(&key.Stream, &key.SourceKey, &key.Kind, &poly,
			&key.Occurrences, &first, &last); nil != err {
			return nil, err
		}

		key.Polymorphic = 0 != poly
		key.FirstSeen = time.UnixMicro(first).UTC()
		key.LastSeen = time.UnixMicro(last).UTC()

		keys = append(keys, key)
	}

	return keys, rows.Err()
}

/*!
 * Tombstone marks files as superseded without deleting anything.
 *
 * The grace period between this and Forget is the design's MVCC: a query that
 * snapshotted the file list five seconds ago still has its files on disk.
 */
func (c *Catalog) Tombstone(ctx context.Context, ids ...int64) error {
	if 0 == len(ids) {
		return nil
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if nil != err {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().UnixMicro()

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE files SET state = ?, tombstoned_at = ? WHERE id = ?`,
			StateTombstoned, now, id); nil != err {
			return err
		}
	}

	return tx.Commit()
}

// Collectable lists tombstoned files whose grace period has elapsed.
func (c *Catalog) Collectable(ctx context.Context, before time.Time) ([]File, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, stream, path, level, min_ts, max_ts, min_seq, max_seq,
		       row_count, byte_size, has_raw, state, created_at
		FROM files
		WHERE state = ? AND tombstoned_at IS NOT NULL AND tombstoned_at <= ?
		ORDER BY tombstoned_at`,
		StateTombstoned, before.UnixMicro())
	if nil != err {
		return nil, err
	}
	defer rows.Close()

	return scanFiles(rows)
}

// Forget removes a catalog row entirely, after its object has been deleted.
func (c *Catalog) Forget(ctx context.Context, id int64) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, id)
	return err
}

/*!
 * Meta values that have to survive a restart.
 */
const metaMaxSeq = "max_seq"

// MaxSeq returns the highest sequence number ever registered.
//
// Restarting the counter at 1 would make new records collide with old ones in
// the (ts, seq) ordering that pagination and backlog de-duplication both
// depend on, so this is recovered on boot rather than reset.
func (c *Catalog) MaxSeq(ctx context.Context) (uint64, error) {
	var value string

	err := c.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, metaMaxSeq).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if nil != err {
		return 0, err
	}

	return strconv.ParseUint(value, 10, 64)
}

func setMetaTx(ctx context.Context, tx *sql.Tx, key string, value uint64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value = CASE
				WHEN CAST(excluded.value AS INTEGER) > CAST(value AS INTEGER)
				THEN excluded.value ELSE value
			END`,
		key, strconv.FormatUint(value, 10))

	return err
}

// Stats summarises the whole catalog, for /healthz.
type Stats struct {
	Files   int64
	Rows    int64
	Bytes   int64
	Streams int64
}

func (c *Catalog) Stats(ctx context.Context) (Stats, error) {
	var stats Stats

	err := c.db.QueryRowContext(ctx, `
		SELECT
			count(*),
			coalesce(sum(row_count), 0),
			coalesce(sum(byte_size), 0),
			count(DISTINCT stream)
		FROM files WHERE state = ?`, StateLive).
		Scan(&stats.Files, &stats.Rows, &stats.Bytes, &stats.Streams)

	return stats, err
}

func scanFiles(rows *sql.Rows) ([]File, error) {
	var files []File

	for rows.Next() {
		var (
			file                  File
			minTs, maxTs, created int64
			minSeq, maxSeq        int64
			hasRaw                int
		)

		if err := rows.Scan(&file.ID, &file.Stream, &file.Path, &file.Level,
			&minTs, &maxTs, &minSeq, &maxSeq,
			&file.RowCount, &file.ByteSize, &hasRaw, &file.State, &created); nil != err {
			return nil, err
		}

		file.MinTs = time.UnixMicro(minTs).UTC()
		file.MaxTs = time.UnixMicro(maxTs).UTC()
		file.MinSeq, file.MaxSeq = uint64(minSeq), uint64(maxSeq)
		file.HasRaw = 0 != hasRaw
		file.Created = time.UnixMicro(created).UTC()

		files = append(files, file)
	}

	return files, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
