package catalog

import (
	"path/filepath"
	"testing"
)

/*!
 * The store unlinks a WAL segment as soon as Register returns, so the commit
 * has to be on disk by then — not merely in SQLite's WAL file waiting for a
 * checkpoint. In WAL mode that takes synchronous=FULL; NORMAL survives a
 * process crash but not a power loss.
 */
func TestCommitsAreSyncedToDisk(t *testing.T) {
	cat, err := Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if nil != err {
		t.Fatal(err)
	}
	defer cat.Close()

	var level int
	if err := cat.db.QueryRow(`PRAGMA synchronous`).Scan(&level); nil != err {
		t.Fatal(err)
	}

	// 0 OFF, 1 NORMAL, 2 FULL, 3 EXTRA.
	if level < 2 {
		t.Errorf("synchronous = %d, want FULL (2) or stronger", level)
	}
}
