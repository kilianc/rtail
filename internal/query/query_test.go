package query_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/query"
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
