/*!
 * The test that justifies the two-compiler design.
 *
 * rQL compiles to SQL for historical search and to a Go predicate for live
 * tail. The entire argument for doing it that way — rather than filtering
 * client-side, or running the same records through DuckDB — is that both come
 * from one AST and therefore cannot drift.
 *
 * "Cannot drift" is a claim, so it is tested: the same corpus of records goes
 * through the real write path into Parquet and through the predicate in
 * memory, the same queries run against both, and the two answers must match
 * record for record. When they disagree, one of the two compilers is wrong and
 * this says which query found it.
 */

package query_test

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/rql"
)

// corpus is deliberately awkward: absent keys, mixed types, nested objects,
// text lines with no structure at all.
var corpus = []string{
	`{"level":"error","msg":"upstream timeout","service":"api","user_id":4471,"latency_ms":30004.2,"ok":false,"req":{"path":"/v1/orders","method":"POST"}}`,
	`{"level":"info","msg":"request ok","service":"api","user_id":12,"latency_ms":3.5,"ok":true,"req":{"path":"/v1/config","method":"GET"}}`,
	`{"level":"warn","msg":"slow query","service":"worker","user_id":88,"latency_ms":812,"ok":true}`,
	`{"level":"fatal","msg":"out of memory","service":"worker","latency_ms":1}`,
	`{"level":"debug","msg":"cache warm","service":"api","user_id":12}`,
	// No level at all.
	`{"msg":"no level here","service":"cron","user_id":7}`,
	// A key that is a string here and a number elsewhere.
	`{"level":"info","msg":"polymorphic","service":"api","code":"200"}`,
	`{"level":"info","msg":"polymorphic","service":"api","code":404}`,
	// Plain text.
	`a plain line mentioning timeout`,
	`another plain line`,
	// Values that stress the escaping.
	`{"level":"info","msg":"100% done","service":"api","note":"a_b"}`,
	`{"level":"info","msg":"has \"quotes\"","service":"api"}`,
}

// queries covering each operator, boolean form, and the awkward cases.
var queries = []string{
	``,
	`level=ERROR`,
	`level>=ERROR`,
	`level>=WARN`,
	`level<INFO`,
	`service=api`,
	`service=worker`,
	`user_id=12`,
	`user_id>50`,
	`user_id>=4471`,
	`latency_ms>500`,
	`latency_ms<10`,
	`ok=true`,
	`ok=false`,
	`msg:timeout`,
	`msg:TIMEOUT`,
	`service:ap`,
	`msg:~^upstream`,
	`msg:~query$`,
	`timeout`,
	`"plain line"`,
	`user_id=*`,
	`user_id=null`,
	`level=ERROR service=api`,
	`level=ERROR OR level=FATAL`,
	`-level=INFO`,
	`-service=api`,
	`NOT level=DEBUG`,
	`service=api -level=INFO`,
	`(level=ERROR OR level=WARN) service=api`,
	`req.path=/v1/orders`,
	`req.path:orders`,
	`req.method=GET`,
	`code=404`,
	`code=200`,
	`msg:100%`,
	`note=a_b`,
	`stream=agree`,
	`nonexistent=1`,
	`nonexistent=*`,
	`-nonexistent=1`,
}

func TestSQLAndPredicateAgree(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t)
	records := h.ingest(t, "agree", corpus)

	for _, query := range queries {
		t.Run(label(query), func(t *testing.T) {
			node, err := rql.Parse(query)
			if nil != err {
				t.Fatalf("parse: %v", err)
			}

			predicate, err := rql.ToPredicate(node)
			if nil != err {
				t.Fatalf("compiling predicate: %v", err)
			}

			var wantSeq []uint64
			for _, rec := range records {
				if predicate(rec) {
					wantSeq = append(wantSeq, rec.Seq)
				}
			}

			result, err := h.engine.Search(ctx, request(query))
			if nil != err {
				t.Fatalf("search: %v", err)
			}

			var gotSeq []uint64
			for _, rec := range result.Records {
				gotSeq = append(gotSeq, rec.Seq)
			}

			sort.Slice(wantSeq, func(i, j int) bool { return wantSeq[i] < wantSeq[j] })
			sort.Slice(gotSeq, func(i, j int) bool { return gotSeq[i] < gotSeq[j] })

			if !equalSeq(wantSeq, gotSeq) {
				t.Errorf("query %q disagrees:\n  predicate matched %v\n  sql matched       %v\n%s",
					query, wantSeq, gotSeq, explain(t, records, wantSeq, gotSeq))
			}
		})
	}
}

func equalSeq(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// explain names the records the two compilers disagreed about, so a failure
// says which line rather than which numbers.
func explain(t *testing.T, records []*model.Record, want, got []uint64) string {
	t.Helper()

	inGot := map[uint64]bool{}
	for _, seq := range got {
		inGot[seq] = true
	}
	inWant := map[uint64]bool{}
	for _, seq := range want {
		inWant[seq] = true
	}

	out := ""
	for _, rec := range records {
		switch {
		case inWant[rec.Seq] && !inGot[rec.Seq]:
			out += fmt.Sprintf("  only the predicate matched: %s\n", rec.Raw)
		case inGot[rec.Seq] && !inWant[rec.Seq]:
			out += fmt.Sprintf("  only SQL matched:           %s\n", rec.Raw)
		}
	}

	return out
}

func label(query string) string {
	if "" == query {
		return "empty"
	}
	return query
}
