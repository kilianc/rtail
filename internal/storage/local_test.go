package storage

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

// failSync makes every directory fsync fail with err for the rest of the test.
func failSync(t *testing.T, err error) {
	t.Helper()

	previous := fsync
	fsync = func(file *os.File) error {
		if info, statErr := file.Stat(); nil == statErr && info.IsDir() {
			return err
		}
		return previous(file)
	}
	t.Cleanup(func() { fsync = previous })
}

/*!
 * A directory fsync that fails means the rename it was meant to persist may
 * not survive a power loss, and the caller is about to register the object
 * in the catalog on the strength of Commit succeeding. That failure used to
 * be swallowed on every branch.
 */
func TestCommitReportsAFailedDirectorySync(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	writer, err := backend.Create(ctx, "streams/api/a.parquet")
	if nil != err {
		t.Fatal(err)
	}
	writer.Write([]byte("PAR1"))

	failSync(t, syscall.EIO)

	if err := writer.Commit(); !errors.Is(err, syscall.EIO) {
		t.Errorf("Commit = %v, want the EIO from syncing the directory", err)
	}
}

// A filesystem that cannot fsync a directory at all is not a failed write.
func TestCommitToleratesUnsupportedDirectorySync(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	writer, err := backend.Create(ctx, "streams/api/a.parquet")
	if nil != err {
		t.Fatal(err)
	}
	writer.Write([]byte("PAR1"))

	failSync(t, syscall.EINVAL)

	if err := writer.Commit(); nil != err {
		t.Errorf("Commit = %v, want nil when directory fsync is unsupported", err)
	}
}

/*!
 * Each directory Create makes is an entry in its parent, and an object whose
 * day directory vanished in a power loss is as lost as one never written —
 * so every parent of a new directory is synced, not only the object's own.
 */
func TestNewDirectoriesAreSyncedIntoTheirParents(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	var synced []string
	previous := fsync
	fsync = func(file *os.File) error {
		if info, err := file.Stat(); nil == err && info.IsDir() {
			synced = append(synced, file.Name())
		}
		return previous(file)
	}
	t.Cleanup(func() { fsync = previous })

	writer, err := backend.Create(ctx, "streams/api/2026/10/21/a.parquet")
	if nil != err {
		t.Fatal(err)
	}
	defer writer.Abort()

	root := backend.Root()
	for _, dir := range []string{"", "streams", "streams/api", "streams/api/2026", "streams/api/2026/10"} {
		want := filepath.Join(root, filepath.FromSlash(dir))

		found := false
		for _, name := range synced {
			if want == name {
				found = true
			}
		}
		if !found {
			t.Errorf("%s gained a directory but was never synced; synced %v", want, synced)
		}
	}
}

// A process killed mid-write leaves its temp file behind, and nothing else
// would ever remove it.
func TestSweepRemovesOnlyAbandonedTempFiles(t *testing.T) {
	ctx := context.Background()
	backend := newTestBackend(t)

	abandoned, err := backend.Create(ctx, "streams/api/old.parquet")
	if nil != err {
		t.Fatal(err)
	}
	abandoned.Write([]byte("half"))

	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(abandoned.(*localWriter).file.Name(), stale, stale); nil != err {
		t.Fatal(err)
	}

	inProgress, err := backend.Create(ctx, "streams/api/new.parquet")
	if nil != err {
		t.Fatal(err)
	}
	defer inProgress.Abort()
	inProgress.Write([]byte("still going"))

	committed, err := backend.Create(ctx, "streams/api/done.parquet")
	if nil != err {
		t.Fatal(err)
	}
	committed.Write([]byte("PAR1"))
	if err := committed.Commit(); nil != err {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(backend.Root(), "streams/api/done.parquet"), stale, stale); nil != err {
		t.Fatal(err)
	}

	removed, err := backend.Sweep(ctx, "streams/", time.Now().Add(-24*time.Hour))
	if nil != err {
		t.Fatal(err)
	}
	if 1 != removed {
		t.Errorf("removed %d temp files, want 1", removed)
	}

	if _, err := os.Stat(abandoned.(*localWriter).file.Name()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the abandoned temp file survived: %v", err)
	}
	if _, err := os.Stat(inProgress.(*localWriter).file.Name()); nil != err {
		t.Errorf("a write in progress lost its temp file: %v", err)
	}
	if _, err := backend.Stat(ctx, "streams/api/done.parquet"); nil != err {
		t.Errorf("a committed object was swept: %v", err)
	}
}

/*!
 * A stream id becomes a directory, so it has to be a name every filesystem
 * takes. A 300-character id failed every write with "file name too long", and
 * the segment holding it could never be flushed or, after a restart, replayed.
 */
func TestSafeSegmentIsAlwaysOneUsableName(t *testing.T) {
	long := strings.Repeat("a", 300)

	for in, want := range map[string]string{
		"":             "_unnamed",
		"api":          "api",
		"web-1.prod_A": "web-1.prod_A",
		"a/b c":        "a_b_c",
		"..":           "_..",
		".":            "_.",
		"ünï":          "_n_",
	} {
		if got := SafeSegment(in); want != got {
			t.Errorf("SafeSegment(%q) = %q, want %q", in, got, want)
		}
	}

	capped := SafeSegment(long)
	if len(capped) > maxSegment {
		t.Errorf("a %d-byte id became a %d-byte name, over the %d cap", len(long), len(capped), maxSegment)
	}
	if !strings.HasPrefix(capped, strings.Repeat("a", 64)) {
		t.Errorf("capped name %q lost the id's beginning", capped)
	}
	if capped != SafeSegment(long) {
		t.Error("the capped name is not stable")
	}

	// Two long ids with the same beginning stay apart.
	if SafeSegment(long) == SafeSegment(long+"b") {
		t.Error("two long ids that differ only past the cap share a name")
	}

	// And the name really is one a filesystem accepts.
	backend := newTestBackend(t)
	writer, err := backend.Create(context.Background(), "streams/"+capped+"/x.parquet")
	if nil != err {
		t.Fatal(err)
	}
	writer.Write([]byte("PAR1"))
	if err := writer.Commit(); nil != err {
		t.Fatalf("committing under the capped name: %v", err)
	}
}
