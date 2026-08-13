/*!
 * Proving the anti-lock-in claim.
 *
 * "Your logs are just Parquet files; the day you leave, you already have
 * everything" is the core promise of the whole design — and a promise nobody
 * checks is a promise that quietly stops being true the first time someone
 * reaches for a clever encoding.
 *
 * So: write a file the way the flush path does, then read it with a completely
 * unrelated implementation. Skips when duckdb is not installed, which keeps it
 * from being a hard dependency of `go test`, and runs in CI where it is.
 */

package parquetio_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/storage"
)

func duckdb(t *testing.T, query string) string {
	t.Helper()

	binary, err := exec.LookPath("duckdb")
	if nil != err {
		t.Skip("duckdb not installed; skipping the interop check")
	}

	out, err := exec.Command(binary, "-noheader", "-list", "-c", query).CombinedOutput()
	if nil != err {
		t.Fatalf("duckdb failed: %v\nquery: %s\noutput: %s", err, query, out)
	}

	return strings.TrimSpace(string(out))
}

func TestFilesAreReadableWithoutRtail(t *testing.T) {
	dir := t.TempDir()
	backend, err := storage.NewLocal(dir)
	if nil != err {
		t.Fatal(err)
	}

	records := []*model.Record{
		record(t, 1, `{"level":"error","msg":"upstream timeout","service":"api","user_id":4471,"latency_ms":30004.2,"req":{"path":"/v1/orders","method":"POST"}}`),
		record(t, 2, `{"level":"info","msg":"ok","service":"api","user_id":12,"latency_ms":3.5}`),
		record(t, 3, `{"level":"error","msg":"connection reset","service":"worker","user_id":88,"latency_ms":120}`),
		record(t, 4, `a plain text line with no structure at all`),
	}

	writeAll(t, backend, "logs.parquet", records)
	path := filepath.Join(dir, "logs.parquet")

	// The envelope is queryable as ordinary columns.
	if got := duckdb(t, "SELECT count(*) FROM '"+path+"'"); "4" != got {
		t.Errorf("row count = %s, want 4", got)
	}

	// Promoted root keys are real, typed columns — not blobs to be parsed.
	got := duckdb(t, `
		SELECT a_service, count(*)
		FROM '`+path+`'
		WHERE level = 'ERROR' AND a_latency_ms > 100
		GROUP BY 1 ORDER BY 1`)

	if "api|1\nworker|1" != got {
		t.Errorf("aggregate = %q, want api|1 and worker|1", got)
	}

	// Nested objects are annotated JSON, so path extraction works with no
	// help from us.
	if got := duckdb(t, "SELECT a_req ->> '$.path' FROM '"+path+"' WHERE seq = 1"); "/v1/orders" != got {
		t.Errorf("json path = %q, want /v1/orders", got)
	}

	// Timestamps arrive as timestamps, not as integers someone has to divide.
	if got := duckdb(t, "SELECT typeof(ts) FROM '"+path+"' LIMIT 1"); !strings.HasPrefix(got, "TIMESTAMP") {
		t.Errorf("typeof(ts) = %q, want a TIMESTAMP type", got)
	}

	// A row with no promoted fields reads as null, not as empty string.
	if got := duckdb(t, "SELECT a_service IS NULL FROM '"+path+"' WHERE seq = 4"); "true" != got {
		t.Errorf("missing field = %q, want null", got)
	}

	// And raw is there as the escape hatch for anything not promoted.
	if got := duckdb(t, "SELECT raw FROM '"+path+"' WHERE seq = 4"); "a plain text line with no structure at all" != got {
		t.Errorf("raw = %q", got)
	}
}

// Files written at different times have different columns. Reading a whole
// directory has to reconcile them by name rather than failing.
func TestHeterogeneousFilesUnionByName(t *testing.T) {
	dir := t.TempDir()
	backend, err := storage.NewLocal(dir)
	if nil != err {
		t.Fatal(err)
	}

	writeAll(t, backend, "L0-monday.parquet", []*model.Record{
		record(t, 1, `{"msg":"old","service":"api"}`),
	})
	writeAll(t, backend, "L0-tuesday.parquet", []*model.Record{
		record(t, 2, `{"msg":"new","service":"api","trace_id":"abc","retries":3}`),
	})

	glob := filepath.Join(dir, "*.parquet")

	got := duckdb(t, `
		SELECT seq, coalesce(a_trace_id, '-')
		FROM read_parquet('`+glob+`', union_by_name := true)
		ORDER BY seq`)

	if "1|-\n2|abc" != got {
		t.Errorf("union = %q, want the older file's missing column to read as null", got)
	}
}
