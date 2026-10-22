/*!
 * The log under a failing disk.
 *
 * A real full disk needs a filesystem small enough to fill (the logstore has
 * an env-gated test that uses one); these swap the segment file for one that
 * fails on demand, so every path through a failure is exercised on every run.
 */

package wal

import (
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
)

// faultyFile is a segment file that fails when told to.
type faultyFile struct {
	*os.File

	mu sync.Mutex
	// writeErr makes a write land half its bytes and then fail, the way a
	// filesystem that runs out of space part way through does.
	writeErr    error
	truncateErr error
	// gate, when set, holds Sync until it is closed; started is closed once
	// Sync is waiting on it.
	gate    chan struct{}
	started chan struct{}
}

func (f *faultyFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	err := f.writeErr
	f.mu.Unlock()

	if nil != err {
		n, _ := f.File.Write(p[:len(p)/2])
		return n, err
	}

	return f.File.Write(p)
}

func (f *faultyFile) Truncate(size int64) error {
	f.mu.Lock()
	err := f.truncateErr
	f.mu.Unlock()

	if nil != err {
		return err
	}

	return f.File.Truncate(size)
}

func (f *faultyFile) Sync() error {
	f.mu.Lock()
	gate, started := f.gate, f.started
	f.mu.Unlock()

	if nil != gate {
		close(started)
		<-gate
	}

	return f.File.Sync()
}

func (f *faultyFile) set(fn func(f *faultyFile)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fn(f)
}

func openFaulty(t *testing.T) (*Log, *faultyFile) {
	t.Helper()

	log, err := Open(t.TempDir(), Options{SyncInterval: -1})
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })

	log.mu.Lock()
	file := &faultyFile{File: log.file.(*os.File)}
	log.file = file
	log.mu.Unlock()

	return log, file
}

func walRecordOf(seq uint64, payload string) *model.Record {
	return &model.Record{Seq: seq, Stream: "api", Ts: time.Unix(0, 0).UTC(), Raw: payload}
}

// replayed reads back the one sealed segment as its records' seqs.
func replayed(t *testing.T, log *Log, segment string) []uint64 {
	t.Helper()

	var seqs []uint64
	if err := log.Replay(segment, func(rec *model.Record) error {
		seqs = append(seqs, rec.Seq)
		return nil
	}); nil != err {
		t.Fatalf("replaying %s: %v", segment, err)
	}

	return seqs
}

func equal(a, b []uint64) bool {
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

/*!
 * One ENOSPC used to wedge the log until a restart: bufio.Writer keeps its
 * first error, so every append, sync and rotation after it failed long after
 * the space was back. Now the failure is reported, the partial write is cut
 * off, and the next attempt writes the same frames whole — and a record the
 * log could not take is refused rather than dropped later.
 */
func TestAFailedWriteIsRetriedNotRemembered(t *testing.T) {
	log, file := openFaulty(t)

	if err := log.Append(walRecordOf(1, `{"i":1}`)); nil != err {
		t.Fatal(err)
	}
	if err := log.Sync(); nil != err {
		t.Fatal(err)
	}

	full := syscall.ENOSPC
	file.set(func(f *faultyFile) { f.writeErr = full })

	accepted := []uint64{1}

	// Buffered, so accepted; the sync that would cover it fails.
	if err := log.Append(walRecordOf(2, `{"i":2}`)); nil != err {
		t.Fatal(err)
	}
	accepted = append(accepted, 2)

	if err := log.Sync(); !errors.Is(err, full) {
		t.Fatalf("sync on a full disk = %v, want ENOSPC", err)
	}
	if _, err := log.Rotate(); !errors.Is(err, full) {
		t.Fatalf("rotate on a full disk = %v, want ENOSPC", err)
	}

	// Large records fill the buffer, and the one that cannot fit is refused.
	big := `{"pad":"` + strings.Repeat("x", 100<<10) + `"}`
	refused := false
	for seq := uint64(3); seq < 10; seq++ {
		if err := log.Append(walRecordOf(seq, big)); nil != err {
			if !errors.Is(err, full) {
				t.Fatalf("append %d: %v, want ENOSPC", seq, err)
			}
			refused = true
			break
		}
		accepted = append(accepted, seq)
	}
	if !refused {
		t.Fatal("the log kept accepting records it could not write")
	}

	// Space is back.
	file.set(func(f *faultyFile) { f.writeErr = nil })

	if err := log.Append(walRecordOf(10, `{"i":10}`)); nil != err {
		t.Fatalf("append after the disk recovered: %v", err)
	}
	accepted = append(accepted, 10)

	if err := log.Sync(); nil != err {
		t.Fatalf("sync after the disk recovered: %v", err)
	}

	sealed, err := log.Rotate()
	if nil != err {
		t.Fatalf("rotate after the disk recovered: %v", err)
	}

	if got := replayed(t, log, sealed); !equal(accepted, got) {
		t.Errorf("segment holds %v, want exactly the accepted records %v", got, accepted)
	}
}

/*!
 * A partial frame that could not be cut off blocks every later write until it
 * can be: a frame written after it would be unreadable, and so would every
 * frame after that.
 */
func TestAPartialFrameIsCutOffBeforeAnythingElseIsWritten(t *testing.T) {
	log, file := openFaulty(t)

	broken := errors.New("io error")
	file.set(func(f *faultyFile) { f.writeErr, f.truncateErr = syscall.ENOSPC, broken })

	if err := log.Append(walRecordOf(1, `{"i":1}`)); nil != err {
		t.Fatal(err)
	}
	if err := log.Sync(); nil == err {
		t.Fatal("sync succeeded on a failing disk")
	}

	// Writes work again, cutting still does not.
	file.set(func(f *faultyFile) { f.writeErr = nil })
	if err := log.Sync(); !errors.Is(err, broken) {
		t.Fatalf("sync with a partial frame in place = %v, want the truncate's error", err)
	}

	file.set(func(f *faultyFile) { f.truncateErr = nil })
	if err := log.Append(walRecordOf(2, `{"i":2}`)); nil != err {
		t.Fatal(err)
	}

	sealed, err := log.Rotate()
	if nil != err {
		t.Fatal(err)
	}

	if got := replayed(t, log, sealed); !equal([]uint64{1, 2}, got) {
		t.Errorf("segment holds %v, want [1 2]", got)
	}
}

/*!
 * An append waits for a copy into the page cache, not for an fsync. Sync used
 * to hold the log's lock across the fsync, and the store holds its ingest
 * lock across an append — so every sync an HTTP batch asked for stalled all
 * ingest for the length of a disk flush.
 */
func TestAppendsDoNotWaitForAnFsync(t *testing.T) {
	log, file := openFaulty(t)

	if err := log.Append(walRecordOf(1, `{"i":1}`)); nil != err {
		t.Fatal(err)
	}

	gate, started := make(chan struct{}), make(chan struct{})
	file.set(func(f *faultyFile) { f.gate, f.started = gate, started })

	synced := make(chan error, 1)
	go func() { synced <- log.Sync() }()

	<-started

	appended := make(chan error, 1)
	go func() { appended <- log.Append(walRecordOf(2, `{"i":2}`)) }()

	select {
	case err := <-appended:
		if nil != err {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(gate)
		t.Fatal("an append waited for an fsync in progress")
	}

	file.set(func(f *faultyFile) { f.gate = nil })
	close(gate)

	if err := <-synced; nil != err {
		t.Fatal(err)
	}

	// The record that arrived during the fsync was not covered by it.
	if 1 != log.Pending() {
		t.Errorf("pending = %d after the sync, want the 1 record appended during it", log.Pending())
	}

	sealed, err := log.Rotate()
	if nil != err {
		t.Fatal(err)
	}
	if got := replayed(t, log, sealed); !equal([]uint64{1, 2}, got) {
		t.Errorf("segment holds %v, want [1 2]", got)
	}
}
