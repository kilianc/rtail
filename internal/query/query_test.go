package query_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/catalog"
	"github.com/kilianc/rtail/v2/internal/query"
	"github.com/kilianc/rtail/v2/internal/rql"
)

func TestSearchReturnsNewestFirst(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{
		`{"msg":"one"}`, `{"msg":"two"}`, `{"msg":"three"}`,
	})

	result, err := h.engine.Search(ctx, request(""))
	if nil != err {
		t.Fatal(err)
	}

	if 3 != len(result.Records) {
		t.Fatalf("got %d records, want 3", len(result.Records))
	}
	if "three" != result.Records[0].Msg {
		t.Errorf("first record = %q, want the newest", result.Records[0].Msg)
	}

	req := request("")
	req.Ascending = true

	ascending, err := h.engine.Search(ctx, req)
	if nil != err {
		t.Fatal(err)
	}
	if "one" != ascending.Records[0].Msg {
		t.Errorf("ascending first = %q, want the oldest", ascending.Records[0].Msg)
	}
}

/*!
 * Keyset pagination must walk the whole set exactly once, with no gaps and no
 * repeats. That is the property OFFSET quietly loses when new records land
 * between pages.
 */
func TestPaginationIsCompleteAndUnique(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)

	payloads := make([]string, 50)
	for i := range payloads {
		payloads[i] = fmt.Sprintf(`{"msg":"line %d","i":%d}`, i, i)
	}
	h.ingest(t, "s", payloads)

	seen := map[uint64]bool{}
	pages := 0

	req := request("")
	req.Limit = 7

	for {
		result, err := h.engine.Search(ctx, req)
		if nil != err {
			t.Fatal(err)
		}

		for _, rec := range result.Records {
			if seen[rec.Seq] {
				t.Fatalf("seq %d appeared on two pages", rec.Seq)
			}
			seen[rec.Seq] = true
		}

		pages++
		if pages > 20 {
			t.Fatal("pagination did not terminate")
		}

		if nil == result.Next {
			break
		}
		req.Cursor = result.Next
	}

	if 50 != len(seen) {
		t.Errorf("saw %d of 50 records across %d pages", len(seen), pages)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	original := &query.Cursor{Ts: time.UnixMicro(1786633045123456).UTC(), Seq: 4471}

	parsed, err := query.ParseCursor(original.String())
	if nil != err {
		t.Fatal(err)
	}

	if !parsed.Ts.Equal(original.Ts) || parsed.Seq != original.Seq {
		t.Errorf("round trip gave %+v, want %+v", parsed, original)
	}

	if _, err := query.ParseCursor("garbage"); nil == err {
		t.Error("a malformed cursor was accepted")
	}
}

func TestHistogramBucketsBySeverity(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{
		`{"level":"error","msg":"a"}`,
		`{"level":"error","msg":"b"}`,
		`{"level":"info","msg":"c"}`,
	})

	buckets, width, err := h.engine.Histogram(ctx, request(""), 60)
	if nil != err {
		t.Fatal(err)
	}
	if 0 == len(buckets) {
		t.Fatal("no buckets")
	}
	if 0 == width {
		t.Error("bucket width was not reported")
	}

	var total int64
	levels := map[string]int64{}
	for _, bucket := range buckets {
		total += bucket.Total
		for level, count := range bucket.Level {
			levels[level] += count
		}
	}

	if 3 != total {
		t.Errorf("total = %d, want 3", total)
	}
	if 2 != levels["ERROR"] || 1 != levels["INFO"] {
		t.Errorf("levels = %v, want 2 ERROR and 1 INFO", levels)
	}
}

// The histogram honours the filter, or the chart above the results would
// describe a different query than the list below it.
func TestHistogramRespectsTheFilter(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{
		`{"level":"error","msg":"a"}`,
		`{"level":"info","msg":"b"}`,
	})

	buckets, _, err := h.engine.Histogram(ctx, request("level=ERROR"), 60)
	if nil != err {
		t.Fatal(err)
	}

	var total int64
	for _, bucket := range buckets {
		total += bucket.Total
	}

	if 1 != total {
		t.Errorf("total = %d, want 1", total)
	}
}

func TestFieldValuesRankByFrequency(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{
		`{"service":"api","msg":"a"}`,
		`{"service":"api","msg":"b"}`,
		`{"service":"api","msg":"c"}`,
		`{"service":"worker","msg":"d"}`,
		`{"msg":"no service"}`,
	})

	values, err := h.engine.FieldValues(ctx, request(""), "service", 10)
	if nil != err {
		t.Fatal(err)
	}

	if 2 != len(values) {
		t.Fatalf("values = %+v, want 2", values)
	}
	if "api" != values[0].Value || 3 != values[0].Count {
		t.Errorf("top value = %+v, want api x3", values[0])
	}
	// Shares are of the matched, non-null population.
	if values[0].Share <= values[1].Share {
		t.Errorf("shares are not ordered: %+v", values)
	}
	if delta := values[0].Share + values[1].Share - 1.0; delta > 0.001 || delta < -0.001 {
		t.Errorf("shares sum to %v, want 1", values[0].Share+values[1].Share)
	}
}

/*!
 * The explorer's per-field count is of the current query, not of all history,
 * which is the whole reason FieldCounts exists rather than reading the
 * catalog's lifetime tally.
 */
func TestFieldCountsAreScopedToTheQuery(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{
		`{"service":"api","region":"eu","msg":"a"}`,
		`{"service":"api","msg":"b"}`,
		`{"service":"worker","region":"us","msg":"c"}`,
		`{"msg":"plain"}`,
	})

	all, err := h.engine.FieldCounts(ctx, request(""), []string{"service", "region", "msg"})
	if nil != err {
		t.Fatal(err)
	}
	if 3 != all["service"] || 2 != all["region"] || 4 != all["msg"] {
		t.Errorf("unfiltered = %+v, want service 3, region 2, msg 4", all)
	}

	filtered, err := h.engine.FieldCounts(ctx, request(`service="api"`), []string{"service", "region", "msg"})
	if nil != err {
		t.Fatal(err)
	}
	if 2 != filtered["service"] || 1 != filtered["region"] || 2 != filtered["msg"] {
		t.Errorf("filtered = %+v, want service 2, region 1, msg 2", filtered)
	}
}

// A field no file carries is measured as zero rather than reported as unknown:
// no matching record has it, which is what the sidebar is asking.
func TestFieldCountsOnAnUnknownFieldIsZero(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{`{"msg":"a"}`})

	counts, err := h.engine.FieldCounts(ctx, request(""), []string{"nope"})
	if nil != err {
		t.Fatal(err)
	}
	if 0 != counts["nope"] {
		t.Errorf("counts = %+v, want zero", counts)
	}
}

func TestFieldValuesOnAnUnknownFieldIsEmpty(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{`{"msg":"a"}`})

	values, err := h.engine.FieldValues(ctx, request(""), "nope", 10)
	if nil != err {
		t.Fatal(err)
	}
	if 0 != len(values) {
		t.Errorf("values = %+v, want none", values)
	}
}

/*!
 * A fresh install has no files at all, and that is the first thing anyone
 * sees. Every entry point has to work in that state.
 */
func TestEmptyStoreAnswersEveryQuery(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)

	result, err := h.engine.Search(ctx, request("level=ERROR"))
	if nil != err {
		t.Fatalf("search: %v", err)
	}
	if 0 != len(result.Records) {
		t.Errorf("records = %v", result.Records)
	}

	if _, _, err := h.engine.Histogram(ctx, request(""), 60); nil != err {
		t.Fatalf("histogram: %v", err)
	}

	if _, err := h.engine.FieldValues(ctx, request(""), "service", 10); nil != err {
		t.Fatalf("field values: %v", err)
	}

	if _, _, _, err := h.engine.SQL(ctx, "SELECT count(*) FROM logs", request("")); nil != err {
		t.Fatalf("sql: %v", err)
	}
}

func TestSQLEscapeHatch(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{
		`{"level":"error","service":"api","latency_ms":900}`,
		`{"level":"error","service":"api","latency_ms":100}`,
		`{"level":"info","service":"worker","latency_ms":5}`,
	})

	columns, rows, scanned, err := h.engine.SQL(ctx,
		`SELECT a_service, count(*) AS n FROM logs WHERE level = 'ERROR' GROUP BY 1`, request(""))
	if nil != err {
		t.Fatal(err)
	}

	if 2 != len(columns) {
		t.Fatalf("columns = %v", columns)
	}
	if 1 != len(rows) {
		t.Fatalf("rows = %v, want 1", rows)
	}
	if 0 == scanned.Files {
		t.Error("scanned files were not reported")
	}
}

/*!
 * The sandbox. This is the real boundary for the raw-SQL endpoint, so it gets
 * tested rather than assumed.
 */
func TestSQLCannotEscapeTheDataDirectory(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{`{"msg":"a"}`})

	outside := filepath.Join(t.TempDir(), "secret.csv")
	if err := os.WriteFile(outside, []byte("a,b\n1,2\n"), 0o644); nil != err {
		t.Fatal(err)
	}

	attempts := []string{
		`SELECT * FROM read_csv('` + outside + `')`,
		`SELECT * FROM read_csv('/etc/passwd')`,
		`SELECT * FROM read_parquet('/etc/hosts')`,
	}

	for _, attempt := range attempts {
		if _, _, _, err := h.engine.SQL(ctx, attempt, request("")); nil == err {
			t.Errorf("reading outside the data directory was allowed: %s", attempt)
		}
	}
}

func TestSQLRejectsMutations(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{`{"msg":"a"}`})

	for _, attempt := range []string{
		`DROP TABLE logs`,
		`CREATE TABLE evil AS SELECT 1`,
		`ATTACH '/tmp/x.db' AS x`,
		`COPY (SELECT 1) TO '/tmp/out.csv'`,
		`INSTALL httpfs`,
	} {
		if _, _, _, err := h.engine.SQL(ctx, attempt, request("")); nil == err {
			t.Errorf("mutation was allowed: %s", attempt)
		}
	}
}

// The window is the partition pruner, so a query that does not bound itself
// gets one injected rather than being allowed to scan everything.
func TestUnboundedQueryGetsADefaultWindow(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{`{"msg":"a"}`})

	result, err := h.engine.Search(ctx, query.Request{Limit: 10})
	if nil != err {
		t.Fatal(err)
	}

	if result.Scanned.From.IsZero() || result.Scanned.To.IsZero() {
		t.Fatalf("no window was reported: %+v", result.Scanned)
	}
	if window := result.Scanned.To.Sub(result.Scanned.From); window > 25*time.Hour {
		t.Errorf("default window = %s, want about %s", window, query.DefaultWindow)
	}
}

// The time range prunes files, so a window covering nothing must read nothing.
func TestTimeRangePrunes(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{`{"msg":"a"}`})

	req := request("")
	req.From = time.Now().Add(-48 * time.Hour)
	req.To = time.Now().Add(-47 * time.Hour)

	result, err := h.engine.Search(ctx, req)
	if nil != err {
		t.Fatal(err)
	}

	if 0 != len(result.Records) {
		t.Errorf("records = %d, want none outside the window", len(result.Records))
	}
	if 0 != result.Scanned.Files {
		t.Errorf("scanned %d files, want 0 — the range should prune them", result.Scanned.Files)
	}
}

func TestSearchReportsParseErrors(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)

	if _, err := h.engine.Search(ctx, request(`(unclosed`)); nil == err {
		t.Error("a malformed query was accepted")
	} else if !strings.Contains(err.Error(), "position") {
		t.Errorf("error = %v, want a position", err)
	}
}

/*!
 * Files written at different times have different columns, and a query that
 * spans both has to reconcile them — the case the resolver's coalesce exists
 * for.
 */
func TestQueriesSpanHeterogeneousFiles(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)

	// Two flushes, so two files with genuinely different schemas.
	h.ingest(t, "s", []string{`{"msg":"old","service":"api"}`})
	h.ingest(t, "s", []string{`{"msg":"new","service":"api","trace_id":"abc"}`})

	result, err := h.engine.Search(ctx, request(""))
	if nil != err {
		t.Fatal(err)
	}
	if 2 != len(result.Records) {
		t.Fatalf("records = %d, want 2", len(result.Records))
	}
	if 2 != result.Scanned.Files {
		t.Fatalf("scanned %d files, want 2", result.Scanned.Files)
	}

	// A key only the newer file has.
	only, err := h.engine.Search(ctx, request("trace_id=abc"))
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(only.Records) || "new" != only.Records[0].Msg {
		t.Errorf("records = %+v, want just the newer one", only.Records)
	}

	// And a key both have.
	both, err := h.engine.Search(ctx, request("service=api"))
	if nil != err {
		t.Fatal(err)
	}
	if 2 != len(both.Records) {
		t.Errorf("records = %d, want 2", len(both.Records))
	}
}

/*!
 * The SQL filter: a raw boolean expression spliced into the WHERE clause.
 */
func TestSQLFilterMatchesTheSameRowsAsRQL(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{
		`{"level":"error","msg":"a","service":"api","latency_ms":900}`,
		`{"level":"error","msg":"b","service":"worker","latency_ms":100}`,
		`{"level":"info","msg":"c","service":"api","latency_ms":5}`,
	})

	// The same question in both languages has to give the same answer, or one
	// of the two is lying about what the data contains.
	pairs := []struct{ rql, sql string }{
		{`level=ERROR`, `level = 'ERROR'`},
		{`service=api`, `a_service = 'api'`},
		{`latency_ms>500`, `a_latency_ms > 500`},
		{`level=ERROR service=api`, `level = 'ERROR' AND a_service = 'api'`},
	}

	for _, pair := range pairs {
		viaRQL, err := h.engine.Search(ctx, request(pair.rql))
		if nil != err {
			t.Fatalf("rql %q: %v", pair.rql, err)
		}

		req := request(pair.sql)
		req.Lang = query.LangSQL

		viaSQL, err := h.engine.Search(ctx, req)
		if nil != err {
			t.Fatalf("sql %q: %v", pair.sql, err)
		}

		if len(viaRQL.Records) != len(viaSQL.Records) {
			t.Errorf("%q matched %d but %q matched %d",
				pair.rql, len(viaRQL.Records), pair.sql, len(viaSQL.Records))
		}
	}
}

// An empty SQL filter means everything, exactly as an empty rQL one does.
func TestEmptySQLFilterMatchesEverything(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{`{"msg":"a"}`, `{"msg":"b"}`})

	req := request("")
	req.Lang = query.LangSQL

	result, err := h.engine.Search(ctx, req)
	if nil != err {
		t.Fatal(err)
	}
	if 2 != len(result.Records) {
		t.Errorf("records = %d, want 2", len(result.Records))
	}
}

/*!
 * The expression is parenthesised, so it cannot defeat the clauses beside it.
 *
 * An `OR 1=1` spliced in bare would apply to the whole WHERE and return every
 * record in the store, ignoring both the time range and the stream. This is
 * the check that the parentheses are actually there.
 */
func TestSQLFilterCannotEscapeItsClause(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "wanted", []string{`{"msg":"a"}`, `{"msg":"b"}`})
	h.ingest(t, "other", []string{`{"msg":"c"}`, `{"msg":"d"}`})

	req := request(`msg = 'a' OR 1=1`)
	req.Lang = query.LangSQL
	req.Stream = "wanted"

	result, err := h.engine.Search(ctx, req)
	if nil != err {
		t.Fatal(err)
	}

	for _, rec := range result.Records {
		if "wanted" != rec.Stream {
			t.Fatalf("a record from %q leaked past the stream filter", rec.Stream)
		}
	}

	if 2 != len(result.Records) {
		t.Errorf("records = %d, want the 2 on the selected stream", len(result.Records))
	}
}

func TestSQLFilterRejectsStatementBreaks(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{`{"msg":"a"}`})

	for _, attempt := range []string{
		`msg = 'a'; DROP TABLE logs`,
		`msg = 'a'; SELECT 1`,
	} {
		req := request(attempt)
		req.Lang = query.LangSQL

		if _, err := h.engine.Search(ctx, req); nil == err {
			t.Errorf("accepted a filter with a statement break: %s", attempt)
		}
	}
}

// A malformed expression is the query's fault, and says so with a position.
func TestSQLFilterReportsItsOwnErrors(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{`{"msg":"a"}`})

	req := request(`level = = 'ERROR'`)
	req.Lang = query.LangSQL

	if _, err := h.engine.Search(ctx, req); nil == err {
		t.Error("a malformed SQL expression was accepted")
	}
}

// The histogram and the field explorer read the same filter as the results.
func TestSQLFilterAppliesToHistogramAndFields(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{
		`{"level":"error","service":"api"}`,
		`{"level":"error","service":"api"}`,
		`{"level":"info","service":"worker"}`,
	})

	req := request(`level = 'ERROR'`)
	req.Lang = query.LangSQL

	buckets, _, err := h.engine.Histogram(ctx, req, 60)
	if nil != err {
		t.Fatal(err)
	}

	var total int64
	for _, bucket := range buckets {
		total += bucket.Total
	}
	if 2 != total {
		t.Errorf("histogram total = %d, want 2", total)
	}

	values, err := h.engine.FieldValues(ctx, req, "service", 10)
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(values) || "api" != values[0].Value || 2 != values[0].Count {
		t.Errorf("field values = %+v, want api x2", values)
	}
}

/*!
 * A file without a key's column counts as missing it whichever order the
 * files arrive in.
 *
 * The planner hands them over oldest first, and counting only the files seen
 * after the key's first appearance left an older file that kept the key in
 * raw out of the count — so the key resolved to the bare column, which reads
 * NULL there, and that file's records stopped matching.
 */
func TestResolverCountsFilesBeforeTheKeyAppeared(t *testing.T) {
	files := []catalog.File{{ID: 1, HasRaw: true}, {ID: 2, HasRaw: true}}
	columns := map[int64][]catalog.Column{
		2: {{Name: "a_foo", SourceKey: "foo", Kind: "string"}},
	}

	for _, order := range [][]catalog.File{files, {files[1], files[0]}} {
		expr, _, ok := query.NewResolver(order, columns).Resolve("foo")
		if !ok || !strings.HasPrefix(expr, "coalesce(a_foo, ") {
			t.Errorf("files %d,%d: foo resolved to %q, want a coalesce with raw", order[0].ID, order[1].ID, expr)
		}
	}
}

// A limit over the cap gets the cap, not the default page — which it used to
// be, so asking for more than allowed got fewer than asking for nothing.
func TestLimitAboveTheCapGetsTheCap(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)

	payloads := make([]string, query.DefaultLimit+60)
	for i := range payloads {
		payloads[i] = fmt.Sprintf(`{"msg":"line %d"}`, i)
	}
	h.ingest(t, "s", payloads)

	maxRows := query.DefaultLimit + 50

	capped, err := query.Open(h.store.Catalog(), h.store.Backend(), query.Limits{MaxRows: maxRows})
	if nil != err {
		t.Fatal(err)
	}
	defer capped.Close()

	req := request("")
	req.Limit = 1000

	result, err := capped.Search(ctx, req)
	if nil != err {
		t.Fatal(err)
	}
	if maxRows != len(result.Records) || nil == result.Next {
		t.Errorf("records = %d, next = %v; want the cap of %d and a cursor", len(result.Records), result.Next, maxRows)
	}

	req.Limit = 0
	if result, err := capped.Search(ctx, req); nil != err || query.DefaultLimit != len(result.Records) {
		t.Errorf("no limit gave %d records (%v), want the default %d", len(result.Records), err, query.DefaultLimit)
	}
}

/*!
 * A later page reads the window the first one resolved.
 *
 * The time picker sends `-6h`, which the server resolves anew on every
 * request. Re-resolved per page, the window's start moved while the cursor
 * walked back, and the records in between were skipped.
 */
func TestTheCursorPinsTheWindow(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{`{"msg":"a"}`, `{"msg":"b"}`, `{"msg":"c"}`, `{"msg":"d"}`})

	req := request("")
	req.Limit = 2

	first, err := h.engine.Search(ctx, req)
	if nil != err {
		t.Fatal(err)
	}
	if nil == first.Next {
		t.Fatal("no cursor after the first page")
	}

	// Round-trip it as the API does, then ask with a window that has since
	// moved past every record.
	cursor, err := query.ParseCursor(first.Next.String())
	if nil != err {
		t.Fatal(err)
	}

	req.Cursor = cursor
	req.From = time.Now().Add(2 * time.Hour)
	req.To = time.Now().Add(3 * time.Hour)

	second, err := h.engine.Search(ctx, req)
	if nil != err {
		t.Fatal(err)
	}
	if 2 != len(second.Records) {
		t.Errorf("second page = %d records, want the 2 the first page's window still holds", len(second.Records))
	}
	if !second.Scanned.From.Equal(first.Scanned.From.Truncate(time.Microsecond)) {
		t.Errorf("second page read from %s, want the first page's %s", second.Scanned.From, first.Scanned.From)
	}
}

// Cursors written before they carried a window still parse.
func TestCursorWindowRoundTrip(t *testing.T) {
	original := &query.Cursor{
		Ts:   time.UnixMicro(1786633045123456).UTC(),
		Seq:  4471,
		From: time.UnixMicro(1786600000000000).UTC(),
		To:   time.UnixMicro(1786640000000000).UTC(),
	}

	parsed, err := query.ParseCursor(original.String())
	if nil != err {
		t.Fatal(err)
	}
	if !parsed.From.Equal(original.From) || !parsed.To.Equal(original.To) {
		t.Errorf("window round trip gave %+v, want %+v", parsed, original)
	}

	legacy, err := query.ParseCursor("1786633045123456:4471")
	if nil != err {
		t.Fatal(err)
	}
	if 4471 != legacy.Seq || !legacy.From.IsZero() {
		t.Errorf("legacy cursor = %+v", legacy)
	}

	for _, bad := range []string{"1:2:3", "1:2:x:4", "1:2:3:4:5"} {
		if _, err := query.ParseCursor(bad); nil == err {
			t.Errorf("ParseCursor(%q) was accepted", bad)
		}
	}
}

/*!
 * An unbalanced parenthesis in a SQL filter cannot reach the clauses we add.
 *
 * Spliced in beside them, `TRUE) OR (TRUE` closed the filter's own
 * parentheses and OR-ed the stream, the window and the cursor away. The
 * filter now runs over a subquery that already applied them.
 */
func TestSQLFilterCannotEscapeWithAParenthesis(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "wanted", []string{`{"msg":"a"}`, `{"msg":"b"}`, `{"msg":"c"}`})
	h.ingest(t, "other", []string{`{"msg":"d"}`, `{"msg":"e"}`})

	req := request(`TRUE) OR (TRUE`)
	req.Lang = query.LangSQL
	req.Stream = "wanted"
	req.Limit = 2

	seen := map[uint64]bool{}

	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("pagination did not terminate: the cursor is not applying")
		}

		result, err := h.engine.Search(ctx, req)
		if nil != err {
			t.Fatal(err)
		}

		for _, rec := range result.Records {
			if "wanted" != rec.Stream {
				t.Fatalf("a record from %q leaked past the stream filter", rec.Stream)
			}
			if seen[rec.Seq] {
				t.Fatalf("seq %d came back on a later page", rec.Seq)
			}
			seen[rec.Seq] = true
		}

		if nil == result.Next {
			break
		}
		req.Cursor = result.Next
	}

	if 3 != len(seen) {
		t.Errorf("saw %d records, want the 3 on the selected stream", len(seen))
	}
}

/*!
 * The caller's SQL being wrong is the caller's problem, reported as such, and
 * without the statement we wrapped around it.
 *
 * A fresh store has no promoted columns at all, so `a_service` does not
 * exist: that is a binder error in the user's SQL, not a server fault.
 */
func TestUserSQLErrorsAreTheirFault(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)

	_, _, _, err := h.engine.SQL(ctx, `SELECT a_service FROM logs`, request(""))

	var failure *query.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v (%T), want a *query.Failure", err, err)
	}
	if !failure.UserFault() {
		t.Errorf("a binder error in the caller's SQL is not their fault: %v", err)
	}
	if public := failure.Public(); !strings.Contains(public, "a_service") ||
		strings.Contains(public, "WITH logs") || strings.Contains(public, "LINE ") {
		t.Errorf("public message = %q, want DuckDB's message without our statement", public)
	}

	req := request(`nope = 1`)
	req.Lang = query.LangSQL
	if _, err := h.engine.Search(ctx, req); !errors.As(err, &failure) || !failure.UserFault() {
		t.Errorf("a lang=sql filter naming a missing column: %v, want a user fault", err)
	}

	// A mutation is refused before it reaches DuckDB, with a position.
	_, _, _, err = h.engine.SQL(ctx, `SELECT 1; DROP TABLE logs`, request(""))
	var parseErr *rql.Error
	if !errors.As(err, &parseErr) || parseErr.Position < 0 {
		t.Errorf("err = %v (%T), want an *rql.Error", err, err)
	}
}

// The data directory never appears in what a client is shown.
func TestFailuresDoNotNameTheDataDirectory(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{`{"msg":"a"}`})

	// The catalog still lists the file; DuckDB will fail to open it.
	removeParquet(t, h.dir)

	_, err := h.engine.Search(ctx, request(""))

	var failure *query.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v (%T), want a *query.Failure", err, err)
	}
	if failure.UserFault() {
		t.Error("an unreadable file is not the caller's fault")
	}
	if strings.Contains(failure.Public(), h.dir) {
		t.Errorf("public message names the data directory: %q", failure.Public())
	}
	if !strings.Contains(failure.Error(), "read_parquet") {
		t.Error("the operator's copy should still carry the statement")
	}
}

func removeParquet(t *testing.T, dir string) {
	t.Helper()

	removed := 0
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if nil == err && strings.HasSuffix(path, ".parquet") {
			removed++
			return os.Remove(path)
		}
		return err
	})
	if nil != err || 0 == removed {
		t.Fatalf("removing parquet files under %s: %v (%d removed)", dir, err, removed)
	}
}

/*!
 * Keys that differ only in case are one column to DuckDB.
 *
 * It matches identifiers without regard to case: inside a file a_user reads
 * a_User, and across files union_by_name folds the two into one. Both keys
 * are read from raw, where they are still different keys.
 */
func TestKeysThatDifferOnlyInCase(t *testing.T) {
	h := newHarness(t)

	// Both in one file.
	h.ingest(t, "one", []string{`{"msg":"upper","User":"bob"}`, `{"msg":"lower","user":"alice"}`})

	// One in each of two.
	h.ingest(t, "two", []string{`{"msg":"upper id","ID":"bob"}`})
	h.ingest(t, "two", []string{`{"msg":"lower id","id":"alice"}`})

	mean(t, h, map[string][]string{
		`user=alice`: {"lower"},
		`User=bob`:   {"upper"},
		`user=bob`:   nil,
		`User=alice`: nil,
		`user=*`:     {"lower"},
		`User=*`:     {"upper"},
		`id=alice`:   {"lower id"},
		`ID=bob`:     {"upper id"},
		`id=bob`:     nil,
		`ID=alice`:   nil,
	})
}

// Two keys that mangle to the same column in different files are one column
// once union_by_name has read them, the same way.
func TestResolverReadsSharedColumnNamesFromRaw(t *testing.T) {
	files := []catalog.File{{ID: 1, HasRaw: true}, {ID: 2, HasRaw: true}}

	for name, columns := range map[string]map[int64][]catalog.Column{
		"case": {
			1: {{Name: "a_User", SourceKey: "User", Kind: "string"}},
			2: {{Name: "a_user", SourceKey: "user", Kind: "string"}},
		},
		"mangling": {
			1: {{Name: "a_a_b", SourceKey: "a.b", Kind: "string"}},
			2: {{Name: "a_a_b", SourceKey: "a-b", Kind: "string"}},
		},
	} {
		resolver := query.NewResolver(files, columns)

		for _, file := range columns {
			key := file[0].SourceKey
			if expr, _, ok := resolver.Resolve(key); !ok || strings.Contains(expr, file[0].Name) || !strings.Contains(expr, "raw ->>") {
				t.Errorf("%s: %s resolved to %q, want raw alone", name, key, expr)
			}
		}
	}

	// A key alone with its column keeps it.
	alone := map[int64][]catalog.Column{1: {{Name: "a_user", SourceKey: "user", Kind: "string"}}, 2: nil}
	if expr, _, _ := query.NewResolver(files, alone).Resolve("user"); !strings.HasPrefix(expr, "coalesce(a_user, ") {
		t.Errorf("user resolved to %q, want its column", expr)
	}
}

/*!
 * A key is any string JSON allows, and a JSON path is not.
 *
 * `$.user[email]` was a syntax error DuckDB raised while binding, which made
 * a 500 of the search, the field's values and — because FieldCounts asks for
 * every field in one statement — the sidebar's count of every other field.
 */
func TestKeysThatAreNotJSONPaths(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)

	// Two files, so the key is read through raw in the one that lacks it.
	h.ingest(t, "k", []string{`{"msg":"a","user[email]":"x@y","w":{"*":1,"[0]":2}}`})
	h.ingest(t, "k", []string{`{"msg":"b","service":"api"}`})

	counts, err := h.engine.FieldCounts(ctx, request(""), []string{"msg", "service", "user[email]", "w.*", "w.[0]", "w.x."})
	if nil != err {
		t.Fatal(err)
	}
	if 2 != counts["msg"] || 1 != counts["service"] || 1 != counts["user[email]"] || 1 != counts["w.*"] || 1 != counts["w.[0]"] {
		t.Errorf("counts = %v", counts)
	}

	values, err := h.engine.FieldValues(ctx, request(""), "user[email]", 10)
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(values) || "x@y" != values[0].Value {
		t.Errorf("values = %+v, want x@y", values)
	}

	mean(t, h, map[string][]string{
		`user[email]=x@y`: {"a"},
		`w.*=1`:           {"a"},
		`w.[0]=2`:         {"a"},
		`w.x.=1`:          nil,
		`x..y=1`:          nil,
	})
}

/*!
 * The cap holds however the statement ends.
 *
 * It used to be spliced onto the same line as the LIMIT, so a statement
 * ending in `) --` commented the LIMIT out and returned every row.
 */
func TestSQLCannotCommentAwayTheCap(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)

	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf(`{"msg":"m%d"}`, i)
	}
	h.ingest(t, "s", lines)

	capped, err := query.Open(h.store.Catalog(), h.store.Backend(), query.Limits{MaxRows: 3})
	if nil != err {
		t.Fatal(err)
	}
	defer capped.Close()

	for _, statement := range []string{
		`SELECT * FROM logs`,
		`SELECT * FROM logs -- every row`,
		`SELECT * FROM logs) --`,
		`SELECT * FROM logs) /*`,
		`SELECT * FROM logs) UNION ALL (SELECT * FROM logs`,
	} {
		_, rows, _, err := capped.SQL(ctx, statement, request(""))
		if nil == err && len(rows) > 3 {
			t.Errorf("%q returned %d rows past a cap of 3", statement, len(rows))
		}
	}

	if _, rows, _, err := capped.SQL(ctx, `SELECT * FROM logs -- every row`, request("")); nil != err || 3 != len(rows) {
		t.Errorf("a trailing comment: %d rows, %v; want the cap", len(rows), err)
	}

	// A filter is spliced in too, and a comment at its end is its own.
	req := request(`msg LIKE 'm%' -- every m`)
	req.Lang = query.LangSQL
	if result, err := capped.Search(ctx, req); nil != err || 3 != len(result.Records) {
		t.Errorf("a filter with a trailing comment: %v", err)
	}
}

/*!
 * The read-only check reads words, not letters, and not what is quoted.
 *
 * It looked for substrings anywhere, so a search for an attachment, an
 * update or a download was refused as a mutation.
 */
func TestReadOnlyCheckIgnoresQuotedText(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	h.ingest(t, "s", []string{`{"msg":"update done"}`, `{"msg":"attachment saved"}`})

	for _, filter := range []string{
		`msg ILIKE '%attachment%'`,
		`msg = 'update done'`,
		`msg LIKE '%download %'`,
		`msg = 'installation'`,
		`msg = 'it''s a drop table'`,
		`"msg" = 'create ' -- delete later`,
		`msg = 'x' /* insert here */`,
		`msg LIKE '%exported%'`,
	} {
		req := request(filter)
		req.Lang = query.LangSQL

		if _, err := h.engine.Search(ctx, req); nil != err {
			t.Errorf("%q was refused: %v", filter, err)
		}
	}

	if _, rows, _, err := h.engine.SQL(ctx, `SELECT msg FROM logs WHERE msg = 'update done'`, request("")); nil != err || 1 != len(rows) {
		t.Errorf("an update in a literal: %d rows, %v", len(rows), err)
	}

	for _, statement := range []string{
		`drop table logs`,
		`SELECT 1; LOAD httpfs`,
		`PRAGMA version`,
		`CALL pragma_version()`,
		`SELECT 'quoted' FROM logs WHERE "msg" = 'x' OR 1 = 1 -- harmless
		 UNION ALL SELECT * FROM (INSERT INTO t VALUES (1))`,
	} {
		if _, _, _, err := h.engine.SQL(ctx, statement, request("")); nil == err {
			t.Errorf("%q was allowed", statement)
		}
	}
}

/*!
 * Every connection reads time in UTC.
 *
 * ts renders as text in the session's zone, so on a host anywhere else a
 * text match on ts disagreed with the tail, and the field explorer listed
 * local times. The setting is per connection unless made global, and the
 * pool opens several.
 */
func TestEveryConnectionIsInUTC(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	records := h.ingest(t, "tz", []string{`{"msg":"x"}`})

	// Held all at once, so the pool has to open every one of them.
	conns := make([]*sql.Conn, query.DefaultConcurrency)
	for i := range conns {
		conn, err := h.engine.DB().Conn(ctx)
		if nil != err {
			t.Fatal(err)
		}
		conns[i] = conn
	}

	for i, conn := range conns {
		var zone, text string
		err := conn.QueryRowContext(ctx,
			`SELECT current_setting('TimeZone'), CAST(TIMESTAMPTZ '2024-01-02 03:04:05.12+00' AS VARCHAR)`).Scan(&zone, &text)
		if nil != err {
			t.Fatal(err)
		}
		if "UTC" != zone || "2024-01-02 03:04:05.12+00" != text {
			t.Errorf("connection %d: zone %s renders %s", i, zone, text)
		}
	}

	for _, conn := range conns {
		conn.Close()
	}

	hour := records[0].Ts.UTC().Format("2006-01-02 15")
	mean(t, h, map[string][]string{fmt.Sprintf(`ts:"%s"`, hour): {"x"}})
}
