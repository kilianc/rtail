/*!
 * The query engine.
 *
 * DuckDB, embedded, reading Parquet through an explicit file list the catalog
 * produced. The explicit list is the whole trick: a glob makes DuckDB open
 * every footer under a directory, whereas the catalog answers "which files
 * could possibly match" from an indexed SQLite query in microseconds and hands
 * over fourteen paths instead of nine thousand.
 *
 * Which is also why the UI's time picker is not decoration. It is the
 * partition pruner, it is always present, and a query without one gets the
 * default window injected rather than being allowed to scan everything.
 */

package query

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/kilianc/rtail/v2/internal/catalog"
	"github.com/kilianc/rtail/v2/internal/storage"
)

// Limits bound what a single query may consume.
type Limits struct {
	// Timeout cancels a query that overruns.
	Timeout time.Duration
	// MaxRows caps a result set.
	MaxRows int
	// Memory is DuckDB's memory_limit, e.g. "512MB".
	Memory string
	// Threads caps DuckDB's parallelism.
	Threads int
	// Concurrency bounds queries in flight, so one heavy scan cannot starve
	// live tail.
	Concurrency int
}

// Sensible defaults for a single-node server sharing a box with ingest.
const (
	DefaultTimeout     = 30 * time.Second
	DefaultMaxRows     = 10_000
	DefaultMemory      = "1GB"
	DefaultConcurrency = 4
	// DefaultWindow is the time range injected when a query does not bound
	// itself. Generous enough to be useful, small enough not to be a scan of
	// everything ever ingested.
	DefaultWindow = 24 * time.Hour
)

func (l *Limits) withDefaults() {
	if 0 == l.Timeout {
		l.Timeout = DefaultTimeout
	}
	if 0 == l.MaxRows {
		l.MaxRows = DefaultMaxRows
	}
	if "" == l.Memory {
		l.Memory = DefaultMemory
	}
	if 0 == l.Concurrency {
		l.Concurrency = DefaultConcurrency
	}
}

// Engine runs queries against the Parquet files a catalog knows about.
type Engine struct {
	db      *sql.DB
	cat     *catalog.Catalog
	root    string
	limits  Limits
	permits chan struct{}

	once sync.Once
}

/*!
 * Open prepares the engine.
 *
 * The DuckDB instance is in-memory and holds no state of its own — every query
 * names its files explicitly. That means it can be thrown away and rebuilt
 * without losing anything, and that a corrupt query cannot corrupt data.
 */
func Open(cat *catalog.Catalog, backend storage.Backend, limits Limits) (*Engine, error) {
	limits.withDefaults()

	root := ""
	if local, ok := backend.(*storage.Local); ok {
		root = local.Root()
	} else {
		return nil, fmt.Errorf("query engine needs a local backend for now")
	}

	db, err := sql.Open("duckdb", "")
	if nil != err {
		return nil, fmt.Errorf("opening duckdb: %w", err)
	}

	// Connections are stateful — the settings below are per-connection — so
	// the pool is capped and configured on creation rather than per query.
	db.SetMaxOpenConns(limits.Concurrency)
	db.SetMaxIdleConns(limits.Concurrency)

	/*!
	 * The sandbox.
	 *
	 * This is the real security boundary for the raw-SQL endpoint, and the
	 * order of these three statements is load-bearing:
	 *
	 *   1. allowlist the data directory. This has no effect on its own —
	 *      it is the set of *exceptions* to the restriction below.
	 *   2. disable external access. Now every path outside the allowlist is
	 *      denied, along with HTTP, S3 and ATTACH.
	 *   3. lock the configuration, so a query cannot undo either of them.
	 *
	 * Doing 2 before 1 fails: DuckDB refuses to re-enable external access
	 * once a database is running, so the allowlist would be unreachable and
	 * every read of our own Parquet would be denied.
	 */
	setup := []string{
		fmt.Sprintf("SET memory_limit='%s'", limits.Memory),
		fmt.Sprintf("SET allowed_directories=['%s']", strings.ReplaceAll(root, "'", "''")),
	}

	if limits.Threads > 0 {
		setup = append(setup, fmt.Sprintf("SET threads=%d", limits.Threads))
	}

	setup = append(setup,
		"SET enable_external_access=false",
		"SET lock_configuration=true",
	)

	for _, statement := range setup {
		if _, err := db.Exec(statement); nil != err {
			db.Close()
			return nil, fmt.Errorf("configuring duckdb (%s): %w", statement, err)
		}
	}

	permits := make(chan struct{}, limits.Concurrency)
	for range limits.Concurrency {
		permits <- struct{}{}
	}

	return &Engine{db: db, cat: cat, root: root, limits: limits, permits: permits}, nil
}

// Close releases the engine.
func (e *Engine) Close() error {
	var err error
	e.once.Do(func() { err = e.db.Close() })
	return err
}

/*!
 * acquire takes a concurrency permit.
 *
 * A bounded queue rather than an unbounded pool: under load it is better for
 * the fifth concurrent search to wait than for all five to thrash and for
 * ingest to lose datagrams behind them.
 */
func (e *Engine) acquire(ctx context.Context) (func(), error) {
	select {
	case <-e.permits:
		return func() { e.permits <- struct{}{} }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

/*!
 * Plan is a resolved query: which files to read, and how to read them.
 */
type Plan struct {
	Files    []catalog.File
	Resolver *Resolver
	// From and To are the effective window, after any default was injected.
	From time.Time
	To   time.Time
	// Rows and Bytes are what the planner expects to touch, from catalog
	// statistics. Reported so the UI can warn before a big scan rather than
	// after.
	Rows  int64
	Bytes int64
}

// Empty reports whether the plan can match anything at all.
func (p *Plan) Empty() bool { return 0 == len(p.Files) }

/*!
 * Plan prunes the catalog down to the files a query could match.
 */
func (e *Engine) Plan(ctx context.Context, stream string, from, to time.Time, fields []string) (*Plan, error) {
	if to.IsZero() {
		to = time.Now().UTC()
	}
	if from.IsZero() {
		from = to.Add(-DefaultWindow)
	}

	files, err := e.cat.Prune(ctx, catalog.Query{Stream: stream, From: from, To: to})
	if nil != err {
		return nil, fmt.Errorf("pruning: %w", err)
	}

	plan := &Plan{Files: files, From: from, To: to}

	columns := make(map[int64][]catalog.Column, len(files))
	for _, file := range files {
		list, err := e.cat.Columns(ctx, file.ID)
		if nil != err {
			return nil, fmt.Errorf("reading columns for %s: %w", file.Path, err)
		}

		columns[file.ID] = list
		plan.Rows += file.RowCount
		plan.Bytes += file.ByteSize
	}

	plan.Resolver = NewResolver(files, columns)

	return plan, nil
}

/*!
 * Source renders the FROM clause for a plan.
 *
 * union_by_name is what makes heterogeneous files readable as one table: a
 * column missing from an older file reads as NULL rather than failing the
 * query. It costs a footer read per file, which is why the file list is
 * pruned first and why it is explicit.
 */
func (e *Engine) Source(plan *Plan) string {
	if plan.Empty() {
		return emptySource
	}

	paths := make([]string, 0, len(plan.Files))
	for _, file := range plan.Files {
		paths = append(paths, quote(e.root+"/"+file.Path))
	}

	return "read_parquet([" + strings.Join(paths, ", ") + "], union_by_name := true)"
}

/*!
 * emptySource is a zero-row table with the envelope's shape.
 *
 * A plan that pruned to nothing still has to produce a valid query, or every
 * caller needs a special case for "no files yet" — which is the state a fresh
 * install is in, and therefore the first thing anyone sees.
 */
const emptySource = `(
	SELECT
		CAST(NULL AS TIMESTAMP) AS ts,
		CAST(NULL AS TIMESTAMP) AS ingest_ts,
		CAST(NULL AS VARCHAR)   AS stream,
		CAST(NULL AS BIGINT)    AS seq,
		CAST(NULL AS VARCHAR)   AS level,
		CAST(NULL AS VARCHAR)   AS msg,
		CAST(NULL AS VARCHAR)   AS host,
		CAST(NULL AS BOOLEAN)   AS is_json,
		CAST(NULL AS VARCHAR)   AS raw
	WHERE FALSE
)`

/*!
 * query runs a statement under the engine's limits.
 */
func (e *Engine) query(ctx context.Context, statement string, args ...any) (*sql.Rows, func(), error) {
	release, err := e.acquire(ctx)
	if nil != err {
		return nil, nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, e.limits.Timeout)

	rows, err := e.db.QueryContext(ctx, statement, args...)
	if nil != err {
		cancel()
		release()
		return nil, nil, fmt.Errorf("%w\n\nquery:\n%s", err, statement)
	}

	return rows, func() {
		rows.Close()
		cancel()
		release()
	}, nil
}
