/*!
 * Search, histogram, and the field explorer.
 *
 * The three queries the explorer is built from. They share a plan, so
 * asking for results and asking for the chart above them prunes once, and the
 * histogram touches two columns rather than the whole row.
 */

package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kilianc/rtail/v2/internal/catalog"
	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/normalize"
	"github.com/kilianc/rtail/v2/internal/parquetio"
	"github.com/kilianc/rtail/v2/internal/rql"
)

// Lang selects how Query is interpreted.
type Lang string

const (
	// LangRQL is the filter bar's own language.
	LangRQL Lang = "rql"
	// LangSQL is a raw SQL boolean expression, spliced into the WHERE clause.
	LangSQL Lang = "sql"
)

// Request is one search.
type Request struct {
	Query string
	// Lang defaults to rQL when empty.
	Lang   Lang
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
 *
 * It also carries the window the first page resolved, because the request
 * for the next one does not have it: the time picker sends `-6h`, which means
 * something later every time it is asked. Re-resolved per page, the window's
 * start moved forward while the cursor walked backward, and whatever lay
 * between the two was never shown.
 */
type Cursor struct {
	Ts  time.Time
	Seq uint64
	// From and To are the effective window of the page that issued this
	// cursor. Zero in a cursor written before they existed.
	From time.Time
	To   time.Time
}

// String encodes a cursor for a URL.
func (c *Cursor) String() string {
	text := strconv.FormatInt(c.Ts.UnixMicro(), 10) + ":" + strconv.FormatUint(c.Seq, 10)

	if !c.From.IsZero() || !c.To.IsZero() {
		text += ":" + strconv.FormatInt(c.From.UnixMicro(), 10) + ":" + strconv.FormatInt(c.To.UnixMicro(), 10)
	}

	return text
}

// ParseCursor reads a cursor back, with or without its window.
func ParseCursor(text string) (*Cursor, error) {
	if "" == text {
		return nil, nil
	}

	parts := strings.Split(text, ":")
	if 2 != len(parts) && 4 != len(parts) {
		return nil, fmt.Errorf("malformed cursor")
	}

	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if nil != err {
		return nil, fmt.Errorf("malformed cursor timestamp")
	}

	sequence, err := strconv.ParseUint(parts[1], 10, 64)
	if nil != err {
		return nil, fmt.Errorf("malformed cursor sequence")
	}

	cursor := &Cursor{Ts: time.UnixMicro(ts).UTC(), Seq: sequence}

	if 4 == len(parts) {
		from, fromErr := strconv.ParseInt(parts[2], 10, 64)
		to, toErr := strconv.ParseInt(parts[3], 10, 64)
		if nil != fromErr || nil != toErr {
			return nil, fmt.Errorf("malformed cursor window")
		}
		cursor.From, cursor.To = time.UnixMicro(from).UTC(), time.UnixMicro(to).UTC()
	}

	return cursor, nil
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
 * predicate turns a request's query into a WHERE fragment.
 *
 * One place decides, so search, the histogram and the field explorer cannot
 * disagree about what a query means — a chart that describes a different
 * filter than the list under it is worse than no chart.
 */
func (e *Engine) predicate(req Request, plan *Plan) (*rql.SQL, error) {
	if LangSQL == req.Lang {
		return sqlPredicate(req.Query)
	}

	node, err := rql.Parse(req.Query)
	if nil != err {
		return nil, err
	}

	return rql.ToSQL(node, plan.Resolver)
}

/*!
 * sqlPredicate accepts a raw boolean expression.
 *
 * The expression becomes the WHERE clause of a query over a subquery that
 * already holds the time range, the stream and the cursor — see scoped — so
 * an `OR 1=1`, or an unbalanced `TRUE) OR (TRUE`, can widen nothing but the
 * filter itself. A semicolon is rejected outright, because it is the one
 * character that could end our statement and begin another.
 *
 * This is deliberately not the security boundary. The connection is: DuckDB is
 * opened read-only over an in-memory database with external access disabled,
 * the data directory allowlisted and the configuration locked, so the worst a
 * hostile expression can do is fail. The checks here exist to turn a mistake
 * into a clear message rather than a confusing one.
 */
func sqlPredicate(expr string) (*rql.SQL, error) {
	trimmed := strings.TrimSpace(expr)

	if "" == trimmed {
		return &rql.SQL{Expr: "TRUE"}, nil
	}

	if strings.Contains(trimmed, ";") {
		return nil, &rql.Error{
			Message:  "a filter is a single expression, so it cannot contain ';'",
			Position: strings.Index(trimmed, ";"),
		}
	}

	if err := rejectMutations(trimmed); nil != err {
		return nil, err
	}

	// The newline keeps a trailing `-- comment` from eating the parenthesis.
	return &rql.SQL{Expr: "(" + trimmed + "\n)"}, nil
}

/*!
 * scoped renders the rows a request may see at all — its window, its stream
 * and, for a later page, its keyset position — as a subquery, with the
 * arguments it binds.
 *
 * The filter goes outside it, never beside it. Spliced in next to these
 * clauses, a lang=sql filter had only its own parentheses keeping it in, and
 * `TRUE) OR (TRUE` closes them: the window and the cursor stopped applying,
 * and every page was the first page of everything. Over this subquery the
 * worst a stray parenthesis can do is match the whole window. A filter set on
 * reading other rows can still find a way — so can /v1/sql, which is why the
 * sandbox and not this is the boundary.
 *
 * DuckDB flattens the subquery and pushes both sets of filters into the
 * Parquet scan, so row-group pruning is exactly what it was.
 */
func (e *Engine) scoped(plan *Plan, stream, keyset string, keysetArgs ...any) (string, []any) {
	where := []string{"ts >= ? AND ts <= ?"}
	args := []any{plan.From, plan.To}

	if "" != stream {
		where = append(where, "stream = ?")
		args = append(args, stream)
	}

	if "" != keyset {
		where = append(where, keyset)
		args = append(args, keysetArgs...)
	}

	return fmt.Sprintf("(\n\t\t\tSELECT * FROM %s\n\t\t\tWHERE %s\n\t\t)",
		e.Source(plan), strings.Join(where, "\n\t\t\t  AND ")), args
}

// DefaultLimit is a page when the request does not ask for a size.
const DefaultLimit = 200

/*!
 * Search runs a filter and returns a page of records.
 */
func (e *Engine) Search(ctx context.Context, req Request) (*Result, error) {
	started := time.Now()

	// A later page reads the window the first one did; see Cursor.
	from, to := req.From, req.To
	if nil != req.Cursor && !req.Cursor.From.IsZero() {
		from, to = req.Cursor.From, req.Cursor.To
	}

	plan, err := e.Plan(ctx, req.Stream, from, to)
	if nil != err {
		return nil, err
	}

	predicate, err := e.predicate(req, plan)
	if nil != err {
		return nil, err
	}

	// Asking for more than the cap gets the cap, not the default: a client
	// asking for 5,000 rows wants as many as it may have, and silently
	// handing back 200 looked like the end of the results.
	limit := req.Limit
	switch {
	case limit <= 0:
		limit = min(e.limits.MaxRows, DefaultLimit)
	case limit > e.limits.MaxRows:
		limit = e.limits.MaxRows
	}

	direction, comparison := "DESC", "<"
	if req.Ascending {
		direction, comparison = "ASC", ">"
	}

	var (
		keyset     string
		keysetArgs []any
	)

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
		keyset = fmt.Sprintf("(ts %s ? OR (ts = ? AND seq %s ?))", comparison, comparison)
		keysetArgs = []any{req.Cursor.Ts, req.Cursor.Ts, int64(req.Cursor.Seq)}
	}

	source, args := e.scoped(plan, req.Stream, keyset, keysetArgs...)
	args = append(args, predicate.Args...)

	statement := fmt.Sprintf(`
		SELECT ts, ingest_ts, stream, seq, level, msg, host, is_json, raw
		FROM %s
		WHERE %s
		ORDER BY ts %s, seq %s
		LIMIT %d`,
		source,
		predicate.Expr,
		direction, direction,
		// One extra row tells us whether another page exists without a second
		// query or a count.
		limit+1,
	)

	user := LangSQL == req.Lang

	rows, done, err := e.query(ctx, user, statement, args...)
	if nil != err {
		return nil, err
	}

	// Released as soon as the page is read, not when Search returns:
	// restoreFields below takes slots of its own, and holding this one while
	// it waits for them is a deadlock whenever every slot is a Search.
	records, err := scanRecords(rows, limit+1)
	done()
	if nil != err {
		return nil, e.failure(err, statement, user)
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
		result.Next = &Cursor{Ts: last.Ts, Seq: last.Seq, From: plan.From, To: plan.To}
		records = records[:limit]
	}

	if err := e.restoreFields(ctx, plan, records); nil != err {
		return nil, err
	}

	result.Records = records

	return result, nil
}

/*!
 * restoreFields reads back the fields of records whose raw cannot supply them.
 *
 * scanRecords re-promotes from raw, which covers every JSON line whose raw is
 * still there. Two kinds of record have no such raw: a syslog line, whose raw
 * is the line as sent and whose fields the receiver parsed out of it, and a
 * JSON line past --retention-raw, whose raw is gone. Both still have their
 * fields on disk as promoted columns — keeping those is the point of dropping
 * raw — but search reads only the envelope, so they came back with none. The
 * tail showed facility, app and structured data; the same line on a history
 * page had nothing for the field explorer or click-to-filter to work with.
 *
 * So the columns are read for those records alone: a file at a time, only
 * the files whose seq range holds one of them, only the columns each file
 * has, and only the rows by seq, which is unique per store. A page of JSON
 * lines with raw intact costs nothing extra. The catalog says what each
 * column was called before it was mangled and what kind it holds, so
 * `exampleSDID@32473.iut` comes back under its own name, and an int as an int.
 */
func (e *Engine) restoreFields(ctx context.Context, plan *Plan, records []*model.Record) error {
	missing := map[uint64]*model.Record{}
	for _, rec := range records {
		if nil == rec.Fields {
			missing[rec.Seq] = rec
		}
	}

	for _, file := range plan.Files {
		if 0 == len(missing) {
			return nil
		}

		columns := distinct(plan.Columns[file.ID])
		if 0 == len(columns) {
			continue
		}

		var seqs []any
		for seq, rec := range missing {
			if seq >= file.MinSeq && seq <= file.MaxSeq && rec.Stream == file.Stream {
				seqs = append(seqs, int64(seq))
			}
		}
		if 0 == len(seqs) {
			continue
		}

		if err := e.readFields(ctx, file, columns, seqs, missing); nil != err {
			return err
		}
	}

	return nil
}

/*!
 * distinct drops the columns of one file whose names differ only in case.
 *
 * DuckDB matches identifiers without regard to case, so of a_User and a_user
 * it reads whichever comes first under both names: the record with "User"
 * came back with a "user" it never had, and the one with "user" lost it. See
 * NewResolver, which reads such keys from raw instead. A record restored here
 * has no raw to read, and a missing field is the honest answer.
 */
func distinct(columns []catalog.Column) []catalog.Column {
	names := make(map[string]int, len(columns))
	for _, column := range columns {
		names[strings.ToLower(column.Name)]++
	}

	out := make([]catalog.Column, 0, len(columns))
	for _, column := range columns {
		if 1 == names[strings.ToLower(column.Name)] {
			out = append(out, column)
		}
	}

	return out
}

// readFields fills in the fields of the records at seqs from one file's
// promoted columns, and takes each record it found off missing.
func (e *Engine) readFields(
	ctx context.Context,
	file catalog.File,
	columns []catalog.Column,
	seqs []any,
	missing map[uint64]*model.Record,
) error {
	kinds := make([]model.Kind, len(columns))
	selects := make([]string, len(columns))

	/*!
	 * Each column is cast to the Go type its kind scans into. A JSON column
	 * is Parquet text annotated JSON, which the driver would otherwise hand
	 * back already decoded; the record keeps JSON as its text. A polymorphic
	 * key is stored as text, so it comes back as one — the same widening the
	 * writer did.
	 */
	for i, column := range columns {
		kind := parquetio.ParseKind(column.Kind)
		if column.Polymorphic {
			kind = model.KindString
		}

		cast := "VARCHAR"
		switch kind {
		case model.KindBool:
			cast = "BOOLEAN"
		case model.KindInt:
			cast = "BIGINT"
		case model.KindFloat:
			cast = "DOUBLE"
		}

		kinds[i] = kind
		selects[i] = fmt.Sprintf(`CAST("%s" AS %s)`, column.Name, cast)
	}

	marks := strings.TrimSuffix(strings.Repeat("?, ", len(seqs)), ", ")

	statement := fmt.Sprintf(`SELECT seq, %s FROM read_parquet(%s) WHERE seq IN (%s)`,
		strings.Join(selects, ", "), quote(e.root+"/"+file.Path), marks)

	rows, done, err := e.query(ctx, false, statement, seqs...)
	if nil != err {
		return err
	}
	defer done()

	values := make([]any, len(columns)+1)
	targets := make([]any, len(values))
	for i := range values {
		targets[i] = &values[i]
	}

	for rows.Next() {
		if err := rows.Scan(targets...); nil != err {
			return e.failure(err, statement, false)
		}

		seq, ok := values[0].(int64)
		if !ok {
			continue
		}

		rec := missing[uint64(seq)]
		if nil == rec {
			continue
		}

		// Absent and null are the same thing in a column: a key this
		// record never had is not one of its fields.
		rec.Fields = map[string]model.Value{}
		for i, column := range columns {
			if value, ok := valueOf(kinds[i], values[i+1]); ok {
				rec.Fields[column.SourceKey] = value
			}
		}

		delete(missing, uint64(seq))
	}

	if err := rows.Err(); nil != err {
		return e.failure(err, statement, false)
	}

	return nil
}

// valueOf builds a field value from what the driver scanned, reporting false
// for a null.
func valueOf(kind model.Kind, scanned any) (model.Value, bool) {
	switch value := scanned.(type) {
	case bool:
		return model.Bool(value), true
	case int64:
		return model.Int(value), true
	case float64:
		return model.Float(value), true
	case string:
		if model.KindJSON == kind {
			return model.JSON(value), true
		}
		return model.Str(value), true
	default:
		return model.Null(), false
	}
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

	plan, err := e.Plan(ctx, req.Stream, req.From, req.To)
	if nil != err {
		return nil, 0, err
	}

	predicate, err := e.predicate(req, plan)
	if nil != err {
		return nil, 0, err
	}

	// A bucket width that divides the window into roughly the requested number
	// of bars, rounded to something a person would choose.
	width := niceInterval(plan.To.Sub(plan.From) / time.Duration(buckets))

	source, args := e.scoped(plan, req.Stream, "")
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
		source,
		predicate.Expr,
	)

	user := LangSQL == req.Lang

	rows, done, err := e.query(ctx, user, statement, args...)
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
			return nil, 0, e.failure(err, statement, user)
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
		return nil, 0, e.failure(err, statement, user)
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

	plan, err := e.Plan(ctx, req.Stream, req.From, req.To)
	if nil != err {
		return nil, err
	}

	expr, _, ok := plan.Resolver.Resolve(field)
	if !ok {
		return nil, nil
	}

	predicate, err := e.predicate(req, plan)
	if nil != err {
		return nil, err
	}

	source, args := e.scoped(plan, req.Stream, "")
	args = append(args, predicate.Args...)

	// The null check is outside the filter's WHERE rather than ANDed into
	// it, so it holds whatever precedence the filter's own operators have.
	statement := fmt.Sprintf(`
		WITH matched AS (
			SELECT CAST(%s AS VARCHAR) AS value
			FROM %s
			WHERE %s
		)
		SELECT value, count(*) AS total, count(*) * 1.0 / sum(count(*)) OVER () AS share
		FROM matched
		WHERE value IS NOT NULL
		GROUP BY 1
		ORDER BY total DESC, value
		LIMIT %d`,
		expr, source,
		predicate.Expr,
		limit,
	)

	user := LangSQL == req.Lang

	rows, done, err := e.query(ctx, user, statement, args...)
	if nil != err {
		return nil, err
	}
	defer done()

	var values []FieldValue

	for rows.Next() {
		var value FieldValue
		if err := rows.Scan(&value.Value, &value.Count, &value.Share); nil != err {
			return nil, e.failure(err, statement, user)
		}
		values = append(values, value)
	}

	if err := rows.Err(); nil != err {
		return nil, e.failure(err, statement, user)
	}

	return values, nil
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

	plan, planErr := e.Plan(ctx, req.Stream, req.From, req.To)
	if nil != planErr {
		return nil, nil, Scanned{}, planErr
	}

	limit := req.Limit
	if limit <= 0 || limit > e.limits.MaxRows {
		limit = e.limits.MaxRows
	}

	/*!
	 * The statement ends on a line of its own, and the cap is enforced twice.
	 *
	 * Spliced in on one line with the LIMIT, a statement ending in `) --`
	 * commented the LIMIT out and returned every row in the window. The
	 * newline keeps a line comment to the caller's own text; the row loop
	 * below stops at the cap whatever the text managed to do to the LIMIT.
	 */
	wrapped := fmt.Sprintf(`
		WITH logs AS (
			SELECT * FROM %s WHERE ts >= ? AND ts <= ?
		)
		SELECT * FROM (%s
		) LIMIT %d`,
		e.Source(plan), statement, limit)

	result, done, queryErr := e.query(ctx, true, wrapped, plan.From, plan.To)
	if nil != queryErr {
		return nil, nil, Scanned{}, queryErr
	}
	defer done()

	columns, err = result.Columns()
	if nil != err {
		return nil, nil, Scanned{}, e.failure(err, wrapped, true)
	}

	for len(rows) < limit && result.Next() {
		cells := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range cells {
			targets[i] = &cells[i]
		}

		if err := result.Scan(targets...); nil != err {
			return nil, nil, Scanned{}, e.failure(err, wrapped, true)
		}

		rows = append(rows, cells)
	}

	if err := result.Err(); nil != err {
		return nil, nil, Scanned{}, e.failure(err, wrapped, true)
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
 * "DROP TABLE" into a clear error rather than a confusing one — and a 400
 * with a position, because it is the query's fault, not the server's.
 *
 * Whole words, outside quotes and comments. It used to look for substrings
 * anywhere, so `msg ILIKE '%attachment%'`, `msg = 'update done'` and
 * "installation" were all refused as mutations.
 */
func rejectMutations(statement string) error {
	if at := mutations.FindStringIndex(blankQuoted(statement)); nil != at {
		return &rql.Error{
			Message:  fmt.Sprintf("only read-only queries are allowed here (found %q)", strings.ToLower(statement[at[0]:at[1]])),
			Position: at[0],
		}
	}

	return nil
}

var mutations = regexp.MustCompile(
	`(?i)\b(attach|copy|create|delete|drop|export|insert|install|load|pragma|update|call)\b`)

/*!
 * blankQuoted blanks out string literals, quoted identifiers and comments,
 * leaving everything else where it was so a position still points into the
 * statement as written.
 *
 * A literal is '…' with '' for a quote inside it, an identifier "…" with "",
 * a comment `--` to the end of the line or `/*` to the next close. Anything
 * subtler — an E'…' string's backslashes, a $$-quoted one — can fool it
 * either way, which is acceptable in a guard whose job is a clear message:
 * the sandbox, not this, is what a query cannot get past.
 */
func blankQuoted(statement string) string {
	out := []byte(statement)

	for i := 0; i < len(out); {
		end := i + 1

		switch {
		case '\'' == statement[i] || '"' == statement[i]:
			quote := statement[i]
			for end < len(statement) {
				if quote != statement[end] {
					end++
				} else if end+1 < len(statement) && quote == statement[end+1] {
					end += 2
				} else {
					end++
					break
				}
			}

		case strings.HasPrefix(statement[i:], "--"):
			if at := strings.IndexByte(statement[i:], '\n'); at >= 0 {
				end = i + at
			} else {
				end = len(out)
			}

		case strings.HasPrefix(statement[i:], "/*"):
			if at := strings.Index(statement[i+2:], "*/"); at >= 0 {
				end = i + 2 + at + 2
			} else {
				end = len(out)
			}

		default:
			i++
			continue
		}

		for j := i; j < end; j++ {
			out[j] = ' '
		}
		i = end
	}

	return string(out)
}

// scanRecords reads the envelope columns back into records, at most limit of
// them: a lang=sql filter is spliced into the statement, so the LIMIT there is
// not the only thing holding the page to its size.
func scanRecords(rows *sql.Rows, limit int) ([]*model.Record, error) {
	var out []*model.Record

	for len(out) < limit && rows.Next() {
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

/*!
 * FieldCounts counts how many matching records carry each of the given fields.
 *
 * The explorer's per-field number used to come straight from the catalog,
 * which tallies every record ever written. That is a different question from
 * the one the sidebar is asked: "how much of this is in what I am looking at".
 * A field with four million lifetime occurrences and none in the last ten
 * minutes read as the most interesting thing on the list.
 *
 * One statement with one count() per field rather than a query each: the file
 * set and the predicate are identical for all of them, so the expensive part —
 * opening and filtering the Parquet — is done once.
 */
func (e *Engine) FieldCounts(ctx context.Context, req Request, fields []string) (map[string]int64, error) {
	if 0 == len(fields) {
		return map[string]int64{}, nil
	}

	plan, err := e.Plan(ctx, req.Stream, req.From, req.To)
	if nil != err {
		return nil, err
	}

	predicate, err := e.predicate(req, plan)
	if nil != err {
		return nil, err
	}

	/*!
	 * Fields the resolver cannot name are skipped rather than counted as
	 * zero. "Not in any file this plan reads" and "in the files and never
	 * set" are both legitimately zero, but a field the resolver rejects is
	 * one we cannot ask about at all, and reporting that as zero would be
	 * asserting something we did not measure.
	 */
	projections := make([]string, 0, len(fields))
	names := make([]string, 0, len(fields))

	for _, field := range fields {
		expr, _, ok := plan.Resolver.Resolve(field)
		if !ok {
			continue
		}

		projections = append(projections, fmt.Sprintf("count(%s)", expr))
		names = append(names, field)
	}

	if 0 == len(names) {
		return map[string]int64{}, nil
	}

	counts, err := e.countFields(ctx, req, plan, predicate, names, projections)
	if nil == err {
		return counts, nil
	}

	/*!
	 * One field DuckDB cannot read must not cost every other field its count.
	 *
	 * The sidebar leaves `matches` off altogether when this fails, so one odd
	 * key anywhere in a stream's history — a path DuckDB would not bind —
	 * took the numbers off every field beside it. So a statement that fails
	 * is checked a field at a time, bound against the same files with
	 * nothing to scan, and run again without the fields that fail on their
	 * own, which are then skipped as the unresolvable ones are. When none
	 * fails alone, or every one does, the fault is not a field's, and the
	 * error stands.
	 */
	var keptNames, kept []string
	for i, projection := range projections {
		if nil == e.binds(ctx, plan, projection) {
			keptNames = append(keptNames, names[i])
			kept = append(kept, projection)
		}
	}

	if 0 == len(kept) || len(kept) == len(projections) {
		return nil, err
	}

	return e.countFields(ctx, req, plan, predicate, keptNames, kept)
}

// countFields runs FieldCounts' one statement.
func (e *Engine) countFields(
	ctx context.Context,
	req Request,
	plan *Plan,
	predicate *rql.SQL,
	names, projections []string,
) (map[string]int64, error) {
	source, args := e.scoped(plan, req.Stream, "")
	args = append(args, predicate.Args...)

	statement := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s",
		strings.Join(projections, ", "),
		source,
		predicate.Expr,
	)

	user := LangSQL == req.Lang

	rows, done, err := e.query(ctx, user, statement, args...)
	if nil != err {
		return nil, err
	}
	defer done()

	counts := make(map[string]int64, len(names))

	if rows.Next() {
		cells := make([]int64, len(names))
		dest := make([]any, len(names))
		for i := range cells {
			dest[i] = &cells[i]
		}

		if err := rows.Scan(dest...); nil != err {
			return nil, e.failure(err, statement, user)
		}

		for i, name := range names {
			counts[name] = cells[i]
		}
	}

	if err := rows.Err(); nil != err {
		return nil, e.failure(err, statement, user)
	}

	return counts, nil
}

// binds reports whether DuckDB accepts a projection over a plan's files,
// without reading a row of them.
func (e *Engine) binds(ctx context.Context, plan *Plan, projection string) error {
	statement := fmt.Sprintf("SELECT %s FROM %s WHERE FALSE", projection, e.Source(plan))

	rows, done, err := e.query(ctx, false, statement)
	if nil != err {
		return err
	}
	defer done()

	for rows.Next() {
	}

	return rows.Err()
}
