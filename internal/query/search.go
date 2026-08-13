/*!
 * Search, histogram, and the field explorer.
 *
 * The three queries the interface in §7 is built from. They share a plan, so
 * asking for results and asking for the chart above them prunes once, and the
 * histogram touches two columns rather than the whole row.
 */

package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/normalize"
	"github.com/kilianc/rtail/v2/internal/rql"
)

// Request is one search.
type Request struct {
	Query  string
	Stream string
	From   time.Time
	To     time.Time
	Limit  int
	// Ascending returns oldest first. The default, newest first, is what a log
	// viewer wants.
	Ascending bool
	// Cursor continues a previous page.
	Cursor *Cursor
}

/*!
 * Cursor is a keyset pagination position.
 *
 * (ts, seq) rather than OFFSET, because OFFSET 50000 re-scans fifty thousand
 * rows to throw them away, whereas a keyset predicate lets the engine skip
 * whole row groups. The pair is unique and totally ordered, so pages stay
 * stable even while new records land.
 */
type Cursor struct {
	Ts  time.Time
	Seq uint64
}

// String encodes a cursor for a URL.
func (c *Cursor) String() string {
	return strconv.FormatInt(c.Ts.UnixMicro(), 10) + ":" + strconv.FormatUint(c.Seq, 10)
}

// ParseCursor reads a cursor back.
func ParseCursor(text string) (*Cursor, error) {
	if "" == text {
		return nil, nil
	}

	micros, seq, found := strings.Cut(text, ":")
	if !found {
		return nil, fmt.Errorf("malformed cursor")
	}

	ts, err := strconv.ParseInt(micros, 10, 64)
	if nil != err {
		return nil, fmt.Errorf("malformed cursor timestamp")
	}

	sequence, err := strconv.ParseUint(seq, 10, 64)
	if nil != err {
		return nil, fmt.Errorf("malformed cursor sequence")
	}

	return &Cursor{Ts: time.UnixMicro(ts).UTC(), Seq: sequence}, nil
}

// Result is one page of matches.
type Result struct {
	Records []*model.Record
	// Next continues from where this page stopped, nil when exhausted.
	Next *Cursor
	// Plan reports what the query actually read.
	Scanned Scanned
}

// Scanned describes the work a query implied.
type Scanned struct {
	Files   int
	Rows    int64
	Bytes   int64
	Elapsed time.Duration
	From    time.Time
	To      time.Time
}

/*!
 * Search runs a filter and returns a page of records.
 */
func (e *Engine) Search(ctx context.Context, req Request) (*Result, error) {
	started := time.Now()

	node, err := rql.Parse(req.Query)
	if nil != err {
		return nil, err
	}

	plan, err := e.Plan(ctx, req.Stream, req.From, req.To, rql.Fields(node))
	if nil != err {
		return nil, err
	}

	predicate, err := rql.ToSQL(node, plan.Resolver)
	if nil != err {
		return nil, err
	}

	limit := req.Limit
	if limit <= 0 || limit > e.limits.MaxRows {
		limit = min(e.limits.MaxRows, 200)
	}

	where := []string{"ts >= ? AND ts <= ?"}
	args := []any{plan.From, plan.To}

	if "" != req.Stream {
		where = append(where, "stream = ?")
		args = append(args, req.Stream)
	}

	where = append(where, predicate.Expr)
	args = append(args, predicate.Args...)

	direction, comparison := "DESC", "<"
	if req.Ascending {
		direction, comparison = "ASC", ">"
	}

	if nil != req.Cursor {
		/*!
		 * The keyset predicate, written out rather than as a row-value
		 * comparison.
		 *
		 * `(ts, seq) < (?, ?)` is the tidier spelling and works against a real
		 * table, but over read_parquet it silently matched every row — the
		 * parameters carry no type context there, and the comparison stopped
		 * meaning what it looks like it means. A predicate that quietly does
		 * nothing is the worst kind: pagination still "worked", it just
		 * returned page one forever.
		 */
		where = append(where, fmt.Sprintf("(ts %s ? OR (ts = ? AND seq %s ?))", comparison, comparison))
		args = append(args, req.Cursor.Ts, req.Cursor.Ts, int64(req.Cursor.Seq))
	}

	statement := fmt.Sprintf(`
		SELECT ts, ingest_ts, stream, seq, level, msg, host, is_json, raw
		FROM %s
		WHERE %s
		ORDER BY ts %s, seq %s
		LIMIT %d`,
		e.Source(plan),
		strings.Join(where, "\n\t\t  AND "),
		direction, direction,
		// One extra row tells us whether another page exists without a second
		// query or a count.
		limit+1,
	)

	rows, done, err := e.query(ctx, statement, args...)
	if nil != err {
		return nil, err
	}
	defer done()

	records, err := scanRecords(rows)
	if nil != err {
		return nil, err
	}

	result := &Result{
		Scanned: Scanned{
			Files:   len(plan.Files),
			Rows:    plan.Rows,
			Bytes:   plan.Bytes,
			Elapsed: time.Since(started),
			From:    plan.From,
			To:      plan.To,
		},
	}

	if len(records) > limit {
		last := records[limit-1]
		result.Next = &Cursor{Ts: last.Ts, Seq: last.Seq}
		records = records[:limit]
	}

	result.Records = records

	return result, nil
}

/*!
 * Bucket is one bar of the histogram.
 */
type Bucket struct {
	Ts    time.Time        `json:"ts"`
	Total int64            `json:"total"`
	Level map[string]int64 `json:"level,omitempty"`
}

/*!
 * Histogram counts matches over time, split by severity.
 *
 * The signature feature of the interface: it is how you navigate, not
 * decoration. It is also cheap — it touches ts and level and nothing else, so
 * a chart over a day of logs returns in single-digit milliseconds while the
 * result list is still being read.
 */
func (e *Engine) Histogram(ctx context.Context, req Request, buckets int) ([]Bucket, time.Duration, error) {
	if buckets < 1 {
		buckets = 60
	}

	node, err := rql.Parse(req.Query)
	if nil != err {
		return nil, 0, err
	}

	plan, err := e.Plan(ctx, req.Stream, req.From, req.To, rql.Fields(node))
	if nil != err {
		return nil, 0, err
	}

	predicate, err := rql.ToSQL(node, plan.Resolver)
	if nil != err {
		return nil, 0, err
	}

	// A bucket width that divides the window into roughly the requested number
	// of bars, rounded to something a person would choose.
	width := niceInterval(plan.To.Sub(plan.From) / time.Duration(buckets))

	where := []string{"ts >= ? AND ts <= ?"}
	args := []any{plan.From, plan.To}

	if "" != req.Stream {
		where = append(where, "stream = ?")
		args = append(args, req.Stream)
	}

	where = append(where, predicate.Expr)
	args = append(args, predicate.Args...)

	statement := fmt.Sprintf(`
		SELECT time_bucket(INTERVAL '%d microseconds', ts) AS bucket,
		       coalesce(level, '') AS level,
		       count(*) AS total
		FROM %s
		WHERE %s
		GROUP BY 1, 2
		ORDER BY 1`,
		width.Microseconds(),
		e.Source(plan),
		strings.Join(where, "\n\t\t  AND "),
	)

	rows, done, err := e.query(ctx, statement, args...)
	if nil != err {
		return nil, 0, err
	}
	defer done()

	byTime := map[int64]*Bucket{}
	var ordered []*Bucket

	for rows.Next() {
		var (
			at    time.Time
			level string
			total int64
		)

		if err := rows.Scan(&at, &level, &total); nil != err {
			return nil, 0, err
		}

		bucket, ok := byTime[at.UnixMicro()]
		if !ok {
			bucket = &Bucket{Ts: at.UTC(), Level: map[string]int64{}}
			byTime[at.UnixMicro()] = bucket
			ordered = append(ordered, bucket)
		}

		bucket.Total += total
		if "" != level {
			bucket.Level[level] += total
		}
	}

	if err := rows.Err(); nil != err {
		return nil, 0, err
	}

	out := make([]Bucket, 0, len(ordered))
	for _, bucket := range ordered {
		out = append(out, *bucket)
	}

	return out, width, nil
}

/*!
 * FieldValue is one entry in the field explorer.
 */
type FieldValue struct {
	Value string  `json:"value"`
	Count int64   `json:"count"`
	Share float64 `json:"share"`
}

/*!
 * FieldValues returns the most common values of a field within a result set.
 *
 * This is the sidebar that answers "what is even in here" before you know what
 * to search for — the best thing Stackdriver has, and the reason its explorer
 * is usable on a service you have never seen.
 */
func (e *Engine) FieldValues(ctx context.Context, req Request, field string, limit int) ([]FieldValue, error) {
	if limit < 1 {
		limit = 10
	}

	node, err := rql.Parse(req.Query)
	if nil != err {
		return nil, err
	}

	plan, err := e.Plan(ctx, req.Stream, req.From, req.To, append(rql.Fields(node), field))
	if nil != err {
		return nil, err
	}

	expr, _, ok := plan.Resolver.Resolve(field)
	if !ok {
		return nil, nil
	}

	predicate, err := rql.ToSQL(node, plan.Resolver)
	if nil != err {
		return nil, err
	}

	where := []string{"ts >= ? AND ts <= ?"}
	args := []any{plan.From, plan.To}

	if "" != req.Stream {
		where = append(where, "stream = ?")
		args = append(args, req.Stream)
	}

	where = append(where, predicate.Expr, "value IS NOT NULL")
	args = append(args, predicate.Args...)

	statement := fmt.Sprintf(`
		WITH matched AS (
			SELECT CAST(%s AS VARCHAR) AS value
			FROM %s
			WHERE %s
		)
		SELECT value, count(*) AS total, count(*) * 1.0 / sum(count(*)) OVER () AS share
		FROM matched
		GROUP BY 1
		ORDER BY total DESC, value
		LIMIT %d`,
		expr, e.Source(plan),
		strings.Join(where, "\n\t\t\t  AND "),
		limit,
	)

	rows, done, err := e.query(ctx, statement, args...)
	if nil != err {
		return nil, err
	}
	defer done()

	var values []FieldValue

	for rows.Next() {
		var value FieldValue
		if err := rows.Scan(&value.Value, &value.Count, &value.Share); nil != err {
			return nil, err
		}
		values = append(values, value)
	}

	return values, rows.Err()
}

/*!
 * SQL is the escape hatch: arbitrary DuckDB over a `logs` view.
 *
 * The time bounds are injected rather than trusted to the query, because an
 * arbitrary statement cannot be analysed for a ts predicate reliably and a
 * search box that can accidentally scan a year of data is a footgun. The
 * effective window is reported back so the UI can say so out loud.
 */
func (e *Engine) SQL(ctx context.Context, statement string, req Request) (columns []string, rows [][]any, scanned Scanned, err error) {
	started := time.Now()

	if err := rejectMutations(statement); nil != err {
		return nil, nil, Scanned{}, err
	}

	plan, planErr := e.Plan(ctx, req.Stream, req.From, req.To, nil)
	if nil != planErr {
		return nil, nil, Scanned{}, planErr
	}

	limit := req.Limit
	if limit <= 0 || limit > e.limits.MaxRows {
		limit = e.limits.MaxRows
	}

	wrapped := fmt.Sprintf(`
		WITH logs AS (
			SELECT * FROM %s WHERE ts >= ? AND ts <= ?
		)
		SELECT * FROM (%s) LIMIT %d`,
		e.Source(plan), statement, limit)

	result, done, queryErr := e.query(ctx, wrapped, plan.From, plan.To)
	if nil != queryErr {
		return nil, nil, Scanned{}, queryErr
	}
	defer done()

	columns, err = result.Columns()
	if nil != err {
		return nil, nil, Scanned{}, err
	}

	for result.Next() {
		cells := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range cells {
			targets[i] = &cells[i]
		}

		if err := result.Scan(targets...); nil != err {
			return nil, nil, Scanned{}, err
		}

		rows = append(rows, cells)
	}

	if err := result.Err(); nil != err {
		return nil, nil, Scanned{}, err
	}

	return columns, rows, Scanned{
		Files:   len(plan.Files),
		Rows:    plan.Rows,
		Bytes:   plan.Bytes,
		Elapsed: time.Since(started),
		From:    plan.From,
		To:      plan.To,
	}, nil
}

/*!
 * rejectMutations is a coarse guard, not the security boundary.
 *
 * The real boundary is the connection: DuckDB is configured with external
 * access disabled and the database is in-memory with no tables of its own, so
 * there is nothing to write to and no filesystem to reach. This exists to turn
 * "DROP TABLE" into a clear error rather than a confusing one.
 */
func rejectMutations(statement string) error {
	lowered := strings.ToLower(statement)

	for _, word := range []string{
		"attach", "copy ", "create ", "delete ", "drop ", "export",
		"insert ", "install", "load ", "pragma", "update ", "call ",
	} {
		if strings.Contains(lowered, word) {
			return fmt.Errorf("only read-only queries are allowed here (found %q)", strings.TrimSpace(word))
		}
	}

	return nil
}

// scanRecords reads the envelope columns back into records.
func scanRecords(rows *sql.Rows) ([]*model.Record, error) {
	var out []*model.Record

	for rows.Next() {
		var (
			rec              model.Record
			level, msg, host sql.NullString
			raw              sql.NullString
			isJSON           sql.NullBool
			seq              int64
		)

		if err := rows.Scan(&rec.Ts, &rec.IngestTs, &rec.Stream, &seq,
			&level, &msg, &host, &isJSON, &raw); nil != err {
			return nil, err
		}

		rec.Seq = uint64(seq)
		rec.Level, rec.Msg, rec.Host, rec.Raw = level.String, msg.String, host.String, raw.String
		rec.Ts, rec.IngestTs = rec.Ts.UTC(), rec.IngestTs.UTC()

		rec.Type = "string"
		if isJSON.Bool {
			rec.Type = "object"
			if json.Valid([]byte(rec.Raw)) {
				rec.JSON = json.RawMessage(rec.Raw)
			} else {
				// raw was dropped by a retention policy, or was never valid.
				// The record still renders as its message.
				rec.Type = "string"
			}
		}

		// Re-promote the root keys so a searched record carries the same
		// fields a tailed one does. Without this the field explorer and
		// click-to-filter have nothing to work with on historical results.
		normalize.Promote(&rec)

		out = append(out, &rec)
	}

	return out, rows.Err()
}

/*!
 * niceInterval rounds a bucket width to something a person would pick.
 *
 * A histogram with bars 47.3 seconds wide is technically correct and reads as
 * broken; the axis labels have to land on round numbers for the chart to be
 * legible.
 */
func niceInterval(d time.Duration) time.Duration {
	steps := []time.Duration{
		time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second,
		15 * time.Second, 30 * time.Second,
		time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute,
		15 * time.Minute, 30 * time.Minute,
		time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour,
		24 * time.Hour, 7 * 24 * time.Hour,
	}

	for _, step := range steps {
		if d <= step {
			return step
		}
	}

	return steps[len(steps)-1]
}
