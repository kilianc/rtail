/*!
 * Behaviour that only a particular filesystem can show.
 *
 * Both tests skip unless an environment variable points them at one, because
 * neither filesystem is something `go test` can make for itself. Inside the
 * tools image:
 *
 *   # A case-insensitive directory: a bind mount from macOS (APFS) or
 *   # Windows (NTFS).
 *   docker run ... -e RTAIL_TEST_CASE_INSENSITIVE_DIR=/ci -v "$TMPDIR:/ci" \
 *     rtail-tools go test -run CaseInsensitive ./internal/logstore/
 *
 *   # A filesystem small enough to fill.
 *   docker run ... -e RTAIL_TEST_SMALL_FS=/fs \
 *     --tmpfs /fs:size=3m,uid=$(id -u),gid=$(id -g) \
 *     rtail-tools go test -run DiskFull ./internal/logstore/
 */

package logstore_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

/*!
 * On a case-insensitive filesystem, streams "api" and "API" share one
 * directory, spelled however the first of them spelled it. The orphan sweep
 * used to compare the names a directory listing returned with catalog paths
 * byte for byte, so every object of the other stream — its compacted files
 * too — matched no row and was deleted a day later.
 */
func TestCaseInsensitiveStreamsSurviveTheSweep(t *testing.T) {
	root := os.Getenv("RTAIL_TEST_CASE_INSENSITIVE_DIR")
	if "" == root {
		t.Skip("RTAIL_TEST_CASE_INSENSITIVE_DIR is not set")
	}

	ctx := context.Background()

	dir, err := os.MkdirTemp(root, "case-")
	if nil != err {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	if err := os.WriteFile(filepath.Join(dir, "probe"), nil, 0o644); nil != err {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "PROBE")); nil != err {
		t.Skipf("%s is case-sensitive", root)
	}

	base := hourAgo()

	store := openDurable(t, dir, quietOptions(10))
	defer store.Close()

	for i, stream := range []string{"api", "API", "api", "API"} {
		store.Append(ctx, recAt(stream, fmt.Sprintf(`{"i":%d}`, i), base.Add(time.Duration(i)*time.Second)))
		if err := store.Flush(ctx); nil != err {
			t.Fatal(err)
		}
	}

	// Compacted files as well as flushed ones.
	mergeL0(t, store)
	store.Append(ctx, recAt("API", `{"i":4}`, base.Add(4*time.Second)))
	if err := store.Flush(ctx); nil != err {
		t.Fatal(err)
	}

	if err := store.CollectForTest(ctx, time.Now().Add(48*time.Hour)); nil != err {
		t.Fatal(err)
	}

	for stream, want := range map[string]int{"api": 2, "API": 3} {
		if got := len(readAll(t, dir, stream)); want != got {
			t.Errorf("%s holds %d records after the sweep, want %d", stream, got, want)
		}
	}
}

/*!
 * One write that hit ENOSPC used to fail every append, sync and rotation from
 * then on — the WAL's bufio.Writer kept the error — until a restart, however
 * much space had been freed since. The sync that cannot be done is reported;
 * once there is room, everything accepted is written and nothing is lost or
 * doubled.
 */
func TestDiskFullIsSurvivedOnceThereIsRoomAgain(t *testing.T) {
	root := os.Getenv("RTAIL_TEST_SMALL_FS")
	if "" == root {
		t.Skip("RTAIL_TEST_SMALL_FS is not set")
	}

	ctx := context.Background()
	dir := filepath.Join(root, "data")
	defer os.RemoveAll(dir)

	store := openDurable(t, dir, quietOptions(10))

	for i := range 5 {
		store.Append(ctx, rec("api", fmt.Sprintf(`{"before":%d}`, i)))
	}
	if err := store.Flush(ctx); nil != err {
		t.Fatalf("setup flush: %v", err)
	}

	filler := filepath.Join(root, "filler")
	file, err := os.Create(filler)
	if nil != err {
		t.Fatal(err)
	}
	chunk := make([]byte, 64<<10)
	for {
		if _, err := file.Write(chunk); nil != err {
			break
		}
	}
	file.Close()
	defer os.Remove(filler)

	if err := store.Append(ctx, rec("api", `{"during":1}`)); nil != err {
		t.Fatalf("a buffered append failed: %v", err)
	}
	if err := store.SyncForTest(); nil == err {
		t.Skip("the filesystem did not fill up")
	}

	os.Remove(filler)

	if err := store.Append(ctx, rec("api", `{"after":1}`)); nil != err {
		t.Errorf("append once there was room again: %v", err)
	}
	if err := store.SyncForTest(); nil != err {
		t.Errorf("sync once there was room again: %v", err)
	}
	if err := store.Flush(ctx); nil != err {
		t.Errorf("flush once there was room again: %v", err)
	}
	if err := store.Close(); nil != err {
		t.Errorf("close: %v", err)
	}

	counts := rawSet(readAll(t, dir, "api"))
	if 7 != len(counts) {
		t.Errorf("records = %v, want all 7 accepted", counts)
	}
	for raw, count := range counts {
		if 1 != count {
			t.Errorf("record %s appears %d times", raw, count)
		}
	}
}
