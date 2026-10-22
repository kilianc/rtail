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
	// Levels that share a rank, or have none anyone has heard of.
	`{"level":"emergency","msg":"disk on fire","service":"worker"}`,
	`{"level":"audit","msg":"login","service":"api"}`,
	`{"level":"custom","msg":"bespoke","service":"api"}`,
	// A key that is 12 here and "12.0" there, and a string that reads as NaN.
	`{"level":"info","msg":"twelve","service":"api","ref":12}`,
	`{"level":"info","msg":"twelve point oh","service":"api","ref":"12.0"}`,
	`{"level":"info","msg":"not a number","service":"api","name":"nan"}`,
	// Scalars nested in an object, which SQL reads back as text.
	`{"level":"info","msg":"nested","service":"api","req":{"path":"/v1/x","status":200,"ok":true}}`,
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

	// A record with no level has no rank, so it answers no ordering, and its
	// negation keeps it.
	`level>=DEBUG`,
	`-level>=DEBUG`,
	`level<WARN`,
	`-level<WARN`,
	// Aliases are what ingest stored, and equality compares names.
	`level=warning`,
	`level=Error`,
	`level>=warning`,
	`level=AUDIT`,
	`-level=AUDIT`,
	`level=FATAL`,
	`level=EMERGENCY`,
	`level>=FATAL`,
	`level=nonsense`,

	// The rest of the envelope.
	`ts>2020-01-01`,
	`ts<2020-01-01`,
	`time>=2020-01-01T00:00:00Z`,
	`ingest_ts>2020-01-01T00:00:00+02:00`,
	`-ts>2020-01-01`,
	`ts=*`,
	`ts:20`,
	`is_json=true`,
	`is_json=false`,

	// Quoting means the string, which a typed column does not hold.
	`user_id="12"`,
	`ok="true"`,
	`latency_ms="3.5"`,
	`service="api"`,
	// Booleans order false before true.
	`ok<true`,
	`ok>false`,
	`ok<=false`,
	// A bare number is a number, however the value was written.
	`ref=12`,
	`ref>11`,
	`ref="12.0"`,
	// Not `ref="12"`: ref went polymorphic, so storage holds the numeric 12 as
	// the text "12" and SQL matches it, while the live record is still a
	// number and does not. That is the gap predicate.go states.
	// nan is a word.
	`name=nan`,
	`name>5`,
	// Nested scalars are text to SQL, so they are text to the predicate.
	`req.status=200`,
	`req.status="200"`,
	`req.status>100`,
	`req.ok=true`,
	`req.path="/v1/x"`,

	// Negating a phrase and a group.
	`-"plain line"`,
	`-(service=api OR service=worker)`,
	`plain not line`,
}

func TestSQLAndPredicateAgree(t *testing.T) {
	h := newHarness(t)
	agree(t, h, h.ingest(t, "agree", corpus), queries)
}

/*!
 * textCorpus is where the two have to agree on text rather than on values.
 *
 * SQL reads a nested value back as the text DuckDB writes for it, a number
 * written as a string through try_cast, and ts through DuckDB's rendering of
 * a timestamp; the predicate has to produce the same text from the record in
 * hand. Each case here was a disagreement once, bar the levels, where the
 * two were wrong alike; TestTextMeansWhatItShould pins those.
 *
 * Two batches, so two files: a key only one of them has is read from raw in
 * the other, through a JSON path built from the field name.
 */
var textCorpus = [][]string{
	{
		// Numbers as text, read as DuckDB's try_cast reads them: no hex
		// floats, and past the largest double is infinity, not an error.
		`{"msg":"v hex float","v":"0x1p4"}`,
		`{"msg":"v hex","v":"0x10"}`,
		`{"msg":"v huge","v":"1e400"}`,
		`{"msg":"v negative huge","v":"-1e400"}`,
		`{"msg":"v spaced","v":" 12 "}`,
		// Nested values: members in their order, no HTML escaping, control
		// characters as yyjson writes them, the first of a repeated key.
		`{"msg":"order","o":{"inner":{"b":1,"a":2}}}`,
		`{"msg":"html","o":{"inner":{"h":"<a&b>"}}}`,
		`{"msg":"spaced array","o":{"inner":[1, 2.50]}}`,
		`{"msg":"controls","o":{"inner":{"c":"tab\there\u001f"}}}`,
		`{"msg":"repeated","o":{"inner":{"k":1,"k":2},"k":"first","k":"second"}}`,
		`{"msg":"escaped","o":{"s":"a\u00e9\"b"}}`,
		// pino's numeric levels, which ingest names; a quoted one it does not.
		`{"level":50,"msg":"pino error"}`,
		`{"level":40,"msg":"pino warn"}`,
		`{"level":"50","msg":"text fifty"}`,
	},
	{
		// Numbers nested, which DuckDB writes back its own way.
		`{"msg":"f 1.50","o":{"f":1.50}}`,
		`{"msg":"f 1e2","o":{"f":1e2}}`,
		`{"msg":"f 1.0","o":{"f":1.0}}`,
		`{"msg":"f 1e20","o":{"f":1e20}}`,
		`{"msg":"f 1e21","o":{"f":1e21}}`,
		`{"msg":"f 1e-7","o":{"f":1e-7}}`,
		`{"msg":"f 0.000001","o":{"f":0.000001}}`,
		`{"msg":"f -0.0","o":{"f":-0.0}}`,
		`{"msg":"f -0","o":{"f":-0}}`,
		`{"msg":"f int64","o":{"f":123456789012345678}}`,
		`{"msg":"f bignum","o":{"f":100000000000000000000}}`,
		// Keys no unquoted JSON path can name.
		`{"msg":"odd keys","user[email]":"x@y","w":{"*":2,"":{"z":3},"[0]":4}}`,
		`plain text`,
	},
}

var textQueries = []string{
	`v>5`, `v=16`, `v>1e300`, `v<-1e300`, `v=12`, `v=*`,

	`o.inner:~"^[{].b"`, `o.inner:"\"b\":1"`, `o.inner:"<a"`, `o.inner:"\\u003c"`,
	`o.inner:"1,2.5"`, `o.inner:"1, 2"`, `o.inner:"\\t"`, `o.inner:~"u001F"`,
	`o.inner:"\"k\":1,\"k\":2"`, `o.k=first`, `o.k=second`,
	`o.s:"é"`, `o.s:"\\u00e9"`, `o.s:"\""`,

	`o.f="1.5"`, `o.f="1.50"`, `o.f="100.0"`, `o.f="100"`, `o.f="1.0"`, `o.f="1"`,
	`o.f="100000000000000000000.0"`, `o.f="1e20"`, `o.f="1e21"`, `o.f="1e-7"`, `o.f="0.000001"`,
	`o.f="-0.0"`, `o.f="0"`, `o.f="-0"`, `o.f="123456789012345678"`, `o.f="100000000000000000000"`,
	`o.f:e`, `o.f:"e+"`, `o.f=100`, `o.f=1`, `o.f>1e19`, `o.f=0`,

	`user[email]=x@y`, `user[email]=*`, `w.*=2`, `w..z=3`, `w.[0]=4`, `w.x.=1`, `nope.x[0]=1`,

	`level=50`, `level=40`, `level>=40`, `level>=50`, `level<50`, `level="50"`, `-level=50`,

	`ts:~"[+]00$"`, `ts:~"[.][0-9]+[+]"`, `ts:~"^20[0-9][0-9]-"`, `ingest_ts:~"[+]00$"`, `ts:" "`,
}

func TestSQLAndPredicateAgreeOnText(t *testing.T) {
	h := newHarness(t)

	var records []*model.Record
	for _, batch := range textCorpus {
		records = append(records, h.ingest(t, "text", batch)...)
	}

	agree(t, h, records, textQueries)
}

// agree runs each query through both compilers and fails on any record they
// answer differently.
func agree(t *testing.T, h *harness, records []*model.Record, queries []string) {
	t.Helper()

	ctx := context.Background()

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

/*!
 * Agreeing is not the same as being right: both compilers could be wrong the
 * same way. These pin what the queries the reviewers found disagreeing are
 * actually supposed to return, by message.
 */
func TestQueriesMeanWhatTheyShould(t *testing.T) {
	h := newHarness(t)
	h.ingest(t, "agree", corpus)

	mean(t, h, map[string][]string{
		// No level is not a level at or above DEBUG.
		`level>=DEBUG -level>=INFO`: {"cache warm"},
		`-level>=DEBUG`:             {"no level here", "a plain line mentioning timeout", "another plain line"},
		`level=warning`:             {"slow query"},
		`level=AUDIT`:               {"login"},
		`level=FATAL`:               {"out of memory"},
		`level>=FATAL`:              {"out of memory", "disk on fire"},
		`user_id="12"`:              nil,
		`ok="true"`:                 nil,
		`ok<true`:                   {"upstream timeout"},
		`ref=12`:                    {"twelve", "twelve point oh"},
		`ref="12.0"`:                {"twelve point oh"},
		`name=nan`:                  {"not a number"},
		`req.status="200"`:          {"nested"},
		`-"plain line" -msg:~.`:     nil,
		`-(service=api OR service=worker OR service=cron) -"line mentioning"`: {"another plain line"},
	})
}

func TestTextMeansWhatItShould(t *testing.T) {
	h := newHarness(t)
	for _, batch := range textCorpus {
		h.ingest(t, "text", batch)
	}

	mean(t, h, map[string][]string{
		// Not a number to DuckDB, and infinity to it.
		`v=16`:    nil,
		`v>1e300`: {"v huge"},
		// DuckDB's text, not the document's.
		`o.f="100.0"`:                   {"f 1e2"},
		`o.f="1.0"`:                     {"f 1.0"},
		`o.f="100000000000000000000.0"`: {"f 1e20"},
		`o.f="100000000000000000000"`:   {"f bignum"},
		`o.f="0"`:                       {"f -0"},
		`o.inner:"<a"`:                  {"html"},
		`o.k=first`:                     {"repeated"},
		// Keys that are not paths.
		`user[email]=x@y`: {"odd keys"},
		`w.*=2`:           {"odd keys"},
		`w..z=3`:          {"odd keys"},
		// pino's 50 is ERROR, and 40 WARN; the text "50" is neither.
		`level=50`:   {"pino error"},
		`level>=40`:  {"pino error", "pino warn"},
		`level="50"`: {"text fifty"},
	})
}

// mean checks that each query matches exactly the records with these messages.
func mean(t *testing.T, h *harness, cases map[string][]string) {
	t.Helper()

	ctx := context.Background()

	for query, want := range cases {
		result, err := h.engine.Search(ctx, request(query))
		if nil != err {
			t.Errorf("%q: %v", query, err)
			continue
		}

		got := map[string]bool{}
		for _, rec := range result.Records {
			got[rec.Msg] = true
		}

		match := len(got) == len(want)
		for _, msg := range want {
			match = match && got[msg]
		}
		if !match {
			t.Errorf("%q matched %v, want %v", query, got, want)
		}
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
