package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestBackend(t *testing.T) *Local {
	t.Helper()

	backend, err := NewLocal(t.TempDir())
	if nil != err {
		t.Fatal(err)
	}

	return backend
}

func TestCommitPublishesTheObject(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	writer, err := backend.Create(ctx, "streams/api/2026/08/11/L0-abc.parquet")
	if nil != err {
		t.Fatal(err)
	}

	if _, err := writer.Write([]byte("PAR1")); nil != err {
		t.Fatal(err)
	}
	if err := writer.Commit(); nil != err {
		t.Fatal(err)
	}

	reader, err := backend.Open(ctx, "streams/api/2026/08/11/L0-abc.parquet")
	if nil != err {
		t.Fatal(err)
	}
	defer reader.Close()

	body, _ := io.ReadAll(reader)
	if "PAR1" != string(body) {
		t.Errorf("body = %q, want PAR1", body)
	}
}

/*!
 * The property the whole compactor depends on: a file the catalog does not yet
 * point at must not be visible, and must leave nothing behind when abandoned.
 */
func TestUncommittedObjectsAreInvisible(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	writer, err := backend.Create(ctx, "pending.parquet")
	if nil != err {
		t.Fatal(err)
	}
	writer.Write([]byte("half a file"))

	if _, err := backend.Open(ctx, "pending.parquet"); nil == err {
		t.Error("an uncommitted object was readable")
	}

	objects, err := backend.List(ctx, "")
	if nil != err {
		t.Fatal(err)
	}
	if 0 != len(objects) {
		t.Errorf("List = %v, want empty", objects)
	}

	if err := writer.Abort(); nil != err {
		t.Fatal(err)
	}

	// Abort leaves no temp file behind.
	entries, _ := os.ReadDir(backend.Root())
	if 0 != len(entries) {
		t.Errorf("%d files left after Abort, want 0", len(entries))
	}
}

// `defer w.Abort()` alongside an explicit Commit is the intended idiom, so
// Abort after Commit has to be a no-op rather than a deletion.
func TestAbortAfterCommitIsANoop(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	writer, _ := backend.Create(ctx, "kept.parquet")
	writer.Write([]byte("data"))

	if err := writer.Commit(); nil != err {
		t.Fatal(err)
	}
	if err := writer.Abort(); nil != err {
		t.Fatal(err)
	}

	if _, err := backend.Stat(ctx, "kept.parquet"); nil != err {
		t.Errorf("Abort after Commit removed the object: %v", err)
	}
}

func TestListIsSortedAndPrefixFiltered(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	for _, name := range []string{"b/2.parquet", "a/1.parquet", "b/1.parquet"} {
		writer, _ := backend.Create(ctx, name)
		writer.Write([]byte("x"))
		writer.Commit()
	}

	all, _ := backend.List(ctx, "")
	if 3 != len(all) {
		t.Fatalf("List = %d objects, want 3", len(all))
	}
	for i, want := range []string{"a/1.parquet", "b/1.parquet", "b/2.parquet"} {
		if all[i].Name != want {
			t.Errorf("List[%d] = %q, want %q", i, all[i].Name, want)
		}
	}

	under := func(prefix string) int {
		objects, _ := backend.List(ctx, prefix)
		return len(objects)
	}
	if 2 != under("b/") {
		t.Errorf("List(b/) = %d, want 2", under("b/"))
	}
}

// Object names come from the catalog, and will one day come from an API.
func TestNamesCannotEscapeTheRoot(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	for _, name := range []string{"../escape", "a/../../escape", "/etc/passwd", ""} {
		if _, err := backend.Create(ctx, name); nil == err {
			t.Errorf("Create(%q) was allowed", name)
		}
		if _, err := backend.Open(ctx, name); nil == err {
			t.Errorf("Open(%q) was allowed", name)
		}
		if err := backend.Remove(ctx, name); nil == err {
			t.Errorf("Remove(%q) was allowed", name)
		}
	}

	// A name that merely contains .. but stays inside is fine.
	writer, err := backend.Create(ctx, "a/b/../c.parquet")
	if nil != err {
		t.Fatalf("rejected an in-bounds name: %v", err)
	}
	writer.Write([]byte("x"))
	if err := writer.Commit(); nil != err {
		t.Fatal(err)
	}
	if _, err := backend.Stat(ctx, "a/c.parquet"); nil != err {
		t.Errorf("expected a/c.parquet: %v", err)
	}
}

// The GC pass in the compaction design is retried, so removing something that
// is already gone must not be an error.
func TestRemoveIsIdempotent(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	writer, _ := backend.Create(ctx, "doomed.parquet")
	writer.Write([]byte("x"))
	writer.Commit()

	if err := backend.Remove(ctx, "doomed.parquet"); nil != err {
		t.Fatal(err)
	}
	if err := backend.Remove(ctx, "doomed.parquet"); nil != err {
		t.Errorf("second Remove errored: %v", err)
	}
}

// Parquet readers seek to the footer before anything else.
func TestOpenSupportsSeeking(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	writer, _ := backend.Create(ctx, "seekable.parquet")
	writer.Write([]byte("PAR1....PAR1"))
	writer.Commit()

	reader, err := backend.Open(ctx, "seekable.parquet")
	if nil != err {
		t.Fatal(err)
	}
	defer reader.Close()

	if _, err := reader.Seek(-4, io.SeekEnd); nil != err {
		t.Fatal(err)
	}

	footer := make([]byte, 4)
	io.ReadFull(reader, footer)

	if "PAR1" != string(footer) {
		t.Errorf("footer = %q, want PAR1", footer)
	}
}

func TestNewLocalCreatesTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "data")

	backend, err := NewLocal(root)
	if nil != err {
		t.Fatal(err)
	}

	info, err := os.Stat(backend.Root())
	if nil != err || !info.IsDir() {
		t.Fatalf("root was not created: %v", err)
	}

	if !strings.HasSuffix(backend.Root(), filepath.Join("nested", "data")) {
		t.Errorf("root = %q", backend.Root())
	}
}
