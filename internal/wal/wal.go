/*!
 * The write-ahead log.
 *
 * This is the difference between a logging system and a log viewer. Without
 * it, a flush buffer holds up to 30 seconds of everything and a crash takes
 * all of it; with it, the worst case is the sync interval.
 *
 * Shape: one log for every stream, in numbered segments, rather than a WAL
 * per stream-shard. The reason is truncation — a segment can only be deleted
 * once everything in it is durably in Parquet, so a shared log means a flush
 * cycle has to cover every buffered stream at once. That is a feature rather
 * than a compromise: it makes one fsync serve all streams instead of N, and
 * the tiny-file problem it could cause is the one that pooling low-volume
 * streams already solves.
 *
 * Durability: appends land in an OS buffer and are fsynced on an interval
 * (default 100ms), not per record. A crash loses at most that interval. Since
 * the v1 UDP path is already at-most-once, promising more than that on it
 * would be theatre — the HTTP ingest path in P5 is where per-batch durability
 * becomes meaningful, and Sync is exported so it can ask for it.
 */

package wal

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
)

const (
	// magic identifies the file and its format version. A future format
	// bumps the last byte rather than guessing at what it is reading.
	magic = "RTWAL\x01"

	segmentPrefix = "wal-"
	segmentSuffix = ".log"

	// maxFrame bounds a single record so a corrupt length cannot make replay
	// try to allocate a terabyte.
	maxFrame = 64 << 20
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// ErrCorrupt is returned when a frame fails its checksum mid-file — as opposed
// to a torn frame at the tail, which is expected after a crash and is not an
// error.
var ErrCorrupt = errors.New("wal: corrupt frame")

// Options configure a log.
type Options struct {
	// SyncInterval is how often buffered appends are fsynced. Zero means the
	// default; negative means never sync automatically, which is for tests.
	SyncInterval time.Duration
}

// DefaultSyncInterval bounds how much a crash can cost.
const DefaultSyncInterval = 100 * time.Millisecond

// bufferSize is how much the log holds in memory before writing it out.
const bufferSize = 256 << 10

// Log is an append-only record log in numbered segments.
type Log struct {
	dir      string
	interval time.Duration

	// syncing serializes fsyncs and is taken before mu. Sync holds only it
	// across the fsync itself, so an append — which needs mu — waits for a
	// copy into the page cache, never for the disk. Rotate and Close hold
	// both: a rotation's fsync and its swap have to happen with no append in
	// between.
	syncing sync.Mutex

	mu     sync.Mutex
	file   segmentFile
	active uint64
	closed bool

	// buffer holds whole frames not yet written to file. written is how much
	// of file holds whole frames, and synced how much of that is known to be
	// on disk. torn means a failed write may have left part of a frame past
	// written, which has to be cut off before anything else is written.
	buffer  []byte
	written int64
	synced  int64
	torn    bool
	pending int

	// listed records whether the active segment's directory entry is durable.
	// Until it is, fsyncing the segment's data proves nothing: a power loss
	// can take the file's name away and leave its blocks unreachable.
	listed bool
}

// segmentFile is what the log needs of an open segment: an *os.File, or in
// tests one that fails on demand.
type segmentFile interface {
	io.Writer
	Sync() error
	Truncate(size int64) error
	Close() error
}

/*!
 * Open prepares the log directory and starts a fresh active segment.
 *
 * Any segments already present are left alone: they are the ones a previous
 * process did not finish flushing, and the caller is expected to replay them
 * before serving. Starting a new segment rather than appending to an old one
 * means replay never races the writer.
 */
func Open(dir string, opts Options) (*Log, error) {
	if err := os.MkdirAll(dir, 0o755); nil != err {
		return nil, fmt.Errorf("creating wal dir: %w", err)
	}

	// The directory itself is an entry in its parent, and every segment lives
	// or dies with it.
	if err := syncDir(filepath.Dir(dir)); nil != err {
		return nil, err
	}

	interval := opts.SyncInterval
	if 0 == interval {
		interval = DefaultSyncInterval
	}

	log := &Log{dir: dir, interval: interval}

	existing, err := log.Segments()
	if nil != err {
		return nil, err
	}

	next := uint64(1)
	if n := len(existing); n > 0 {
		last, err := segmentNumber(existing[n-1])
		if nil != err {
			return nil, err
		}
		next = last + 1
	}

	if err := log.openSegment(next); nil != err {
		return nil, err
	}

	return log, nil
}

func (l *Log) openSegment(number uint64) error {
	file, err := l.createSegment(number)
	if nil != err {
		return err
	}

	l.file, l.active = file, number
	l.buffer = append(make([]byte, 0, bufferSize), magic...)
	l.written, l.synced, l.torn, l.pending, l.listed = 0, 0, false, 0, false

	return nil
}

// createSegment opens a new segment file. Its header is not written here: it
// goes at the front of the buffer, and out with the first frames.
func (l *Log) createSegment(number uint64) (*os.File, error) {
	path := filepath.Join(l.dir, segmentName(number))

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if nil != err {
		return nil, fmt.Errorf("opening wal segment: %w", err)
	}

	return file, nil
}

// Active is the current segment's file name.
func (l *Log) Active() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return segmentName(l.active)
}

/*!
 * Append writes one record.
 *
 * Frame layout is [u32 length][u32 crc32c][payload]. The checksum covers the
 * payload only; a length that survives but a payload that does not is exactly
 * the torn-write case replay has to notice.
 *
 * The frame joins the buffer, which is written out once full. A buffer that
 * cannot be written out refuses the frame, and the error says so: the record
 * was not accepted, the caller does not add it to its batch, and the segment
 * keeps holding exactly the records the caller believes it holds. Taking the
 * frame anyway would grow memory for as long as the disk stays full.
 */
func (l *Log) Append(rec *model.Record) error {
	payload, err := marshal(rec)
	if nil != err {
		return fmt.Errorf("encoding record: %w", err)
	}

	if len(payload) > maxFrame {
		return fmt.Errorf("record of %d bytes exceeds the %d byte frame limit", len(payload), maxFrame)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return errors.New("wal: log is closed")
	}

	if 0 != len(l.buffer) && len(l.buffer)+8+len(payload) > bufferSize {
		if err := l.writeLocked(); nil != err {
			return err
		}
	}

	var header [8]byte
	binary.LittleEndian.PutUint32(header[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(header[4:8], crc32.Checksum(payload, castagnoli))

	l.buffer = append(l.buffer, header[:]...)
	l.buffer = append(l.buffer, payload...)
	l.pending++

	return nil
}

/*!
 * writeLocked moves the buffer into the segment file.
 *
 * The frames stay in the buffer until all of them are written. A write that
 * fails part way — ENOSPC, EIO — leaves part of a frame in the file, so that
 * is cut off again, and the next attempt starts on a frame boundary and
 * writes the same frames whole. If the cut fails too, nothing more is written
 * until it succeeds: a frame appended after a torn one would be unreadable,
 * and so would everything after it.
 *
 * This used to be a bufio.Writer, which keeps the first error it sees and
 * returns it from every call after. One full disk failed every append, sync
 * and rotation from then on, until a restart, long after the space was back.
 *
 * Nothing appended is ever dropped to recover, either. The store flushes its
 * batch from memory and retries a batch that failed from its sealed segment,
 * and a replay names files after each stream's first record in the segment;
 * all of that relies on the segment holding exactly the batch's records. A
 * frame dropped from the segment while its record stayed in the batch would
 * be lost on a retry, and turn a replay's file names into new ones — the
 * records duplicated under them.
 */
func (l *Log) writeLocked() error {
	if l.torn {
		if err := l.file.Truncate(l.written); nil != err {
			return fmt.Errorf("cutting a partial frame off %s: %w", segmentName(l.active), err)
		}
		l.torn = false
	}

	if 0 == len(l.buffer) {
		return nil
	}

	n, err := l.file.Write(l.buffer)
	if nil != err {
		if n > 0 {
			if err := l.file.Truncate(l.written); nil != err {
				l.torn = true
			}
		}
		return err
	}

	l.written += int64(n)
	l.buffer = l.buffer[:0]

	// One oversized record should not pin its size for the segment's life.
	if cap(l.buffer) > 2*bufferSize {
		l.buffer = make([]byte, 0, bufferSize)
	}

	return nil
}

// dirtyLocked reports whether anything appended is not yet known to be on disk.
func (l *Log) dirtyLocked() bool {
	return 0 != len(l.buffer) || l.torn || l.written != l.synced || !l.listed
}

/*!
 * Sync writes and fsyncs everything appended so far.
 *
 * The fsync runs without mu, so appends carry on while the disk works: they
 * wait at most for the buffer to be copied into the page cache. Whatever they
 * append meanwhile is left for the next sync, which is all a sync promises —
 * the records appended before it was called.
 *
 * A failure leaves everything as it was, to be written and synced again by
 * the next call: the records stay in the segment, and in the store's batch,
 * and reach disk once the disk lets them. A caller told the sync failed — an
 * HTTP batch answered 503 — may send them again and get duplicates, which is
 * what at-least-once costs; it never gets an acknowledgement for a record
 * that was not on disk.
 */
func (l *Log) Sync() error {
	l.syncing.Lock()
	defer l.syncing.Unlock()

	l.mu.Lock()

	if l.closed || !l.dirtyLocked() {
		l.mu.Unlock()
		return nil
	}

	if err := l.writeLocked(); nil != err {
		l.mu.Unlock()
		return err
	}

	// Rotate and Close need syncing too, so none of this changes until the
	// fsync is done; only appends move on.
	file, upTo, listed, covered := l.file, l.written, l.listed, l.pending

	l.mu.Unlock()

	if err := l.persist(file, listed); nil != err {
		return err
	}

	l.mu.Lock()
	l.synced, l.listed = upTo, true
	l.pending -= covered
	l.mu.Unlock()

	return nil
}

// syncLocked is Sync for a caller that holds both locks.
func (l *Log) syncLocked() error {
	if !l.dirtyLocked() {
		return nil
	}

	if err := l.writeLocked(); nil != err {
		return err
	}
	if err := l.persist(l.file, l.listed); nil != err {
		return err
	}

	l.synced, l.listed, l.pending = l.written, true, 0

	return nil
}

/*!
 * persist fsyncs a segment's data, and the first time also the directory,
 * which is what makes the segment's name durable. Doing that here rather than
 * when the segment is created keeps the extra fsync off Rotate, which runs
 * while ingest waits, and costs nothing in durability: no record in the
 * segment is promised before this sync anyway.
 */
func (l *Log) persist(file segmentFile, listed bool) error {
	if err := file.Sync(); nil != err {
		return err
	}

	if !listed {
		return syncDir(l.dir)
	}

	return nil
}

// Pending counts records appended since the last sync.
func (l *Log) Pending() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.pending
}

/*!
 * Rotate seals the active segment and starts a new one.
 *
 * The sealed name is returned so the caller can replay it into Parquet and
 * then Remove it. Nothing else may delete a segment: until it is gone, it is
 * the only durable copy of the records it holds.
 *
 * A failed Rotate changes nothing. The caller pairs each sealed segment with
 * exactly the records it holds, so a rotation that half happened — the old
 * file closed, no new one open — left a closed file as the active segment,
 * and every later sync and rotation failing against it. The new segment is
 * therefore opened before the old one is let go.
 */
func (l *Log) Rotate() (string, error) {
	l.syncing.Lock()
	defer l.syncing.Unlock()

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return "", errors.New("wal: log is closed")
	}

	if err := l.syncLocked(); nil != err {
		return "", err
	}

	file, err := l.createSegment(l.active + 1)
	if nil != err {
		return "", err
	}

	sealed, previous := segmentName(l.active), l.file

	// The buffer is empty: syncLocked wrote it out.
	l.file, l.active = file, l.active+1
	l.buffer = append(l.buffer[:0], magic...)
	l.written, l.synced, l.torn, l.pending, l.listed = 0, 0, false, 0, false

	// The sealed segment's data and name are already durable — syncLocked
	// returned — so a failing close cannot lose anything, and reporting it
	// would make the caller treat a complete segment as one that failed.
	previous.Close()

	return sealed, nil
}

// Remove deletes a sealed segment. Removing one that is already gone is not an
// error, because the caller is expected to retry after a crash.
func (l *Log) Remove(segment string) error {
	l.mu.Lock()
	active := segmentName(l.active)
	l.mu.Unlock()

	if segment == active {
		return fmt.Errorf("wal: refusing to remove the active segment %s", segment)
	}

	if err := os.Remove(filepath.Join(l.dir, segment)); nil != err && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}

// Segments lists every segment on disk, oldest first, including the active one.
func (l *Log) Segments() ([]string, error) {
	entries, err := os.ReadDir(l.dir)
	if nil != err {
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, segmentPrefix) || !strings.HasSuffix(name, segmentSuffix) {
			continue
		}
		if _, err := segmentNumber(name); nil != err {
			continue
		}
		names = append(names, name)
	}

	sort.Slice(names, func(i, j int) bool {
		a, _ := segmentNumber(names[i])
		b, _ := segmentNumber(names[j])
		return a < b
	})

	return names, nil
}

// Orphans lists sealed segments left behind by a previous process — the ones
// that need replaying before the server starts accepting traffic.
func (l *Log) Orphans() ([]string, error) {
	segments, err := l.Segments()
	if nil != err {
		return nil, err
	}

	active := l.Active()

	orphans := make([]string, 0, len(segments))
	for _, name := range segments {
		if name != active {
			orphans = append(orphans, name)
		}
	}

	return orphans, nil
}

// errFound stops a replay at its first record.
var errFound = errors.New("wal: found")

/*!
 * Floor is the sequence number of the first record in the oldest sealed
 * segment on disk, and false when no sealed segment holds a record.
 *
 * Sequence numbers only grow from one segment to the next, so every record a
 * replay of what is on disk could still produce has a seq at least this —
 * which is what tells garbage collection which catalog rows a replay could
 * still need to recognise. A segment that disappears while being read was
 * just retired, and is passed over.
 */
func (l *Log) Floor() (uint64, bool, error) {
	sealed, err := l.Orphans()
	if nil != err {
		return 0, false, err
	}

	for _, segment := range sealed {
		var (
			first uint64
			found bool
		)

		err := l.Replay(segment, func(rec *model.Record) error {
			first, found = rec.Seq, true
			return errFound
		})
		if found {
			return first, true, nil
		}
		if nil != err && !errors.Is(err, os.ErrNotExist) {
			return 0, false, err
		}
	}

	return 0, false, nil
}

/*!
 * Quarantine moves a sealed segment out of the log, into quarantine/ beside
 * the segments, and returns where it went.
 *
 * For a segment that cannot be replayed, which would otherwise stop every
 * boot: out of the log it is never replayed again, and it is kept rather than
 * deleted because it still holds whatever could not be read. The name gains
 * the time, since segment numbers restart from the highest left in the log
 * and the next one quarantined could otherwise carry the same name.
 */
func (l *Log) Quarantine(segment string) (string, error) {
	if segment == l.Active() {
		return "", fmt.Errorf("wal: refusing to quarantine the active segment %s", segment)
	}

	dir := filepath.Join(l.dir, "quarantine")
	if err := os.MkdirAll(dir, 0o755); nil != err {
		return "", err
	}

	target := filepath.Join(dir, segment+"."+time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.Rename(filepath.Join(l.dir, segment), target); nil != err {
		return "", err
	}

	if err := syncDir(dir); nil != err {
		return target, err
	}

	return target, syncDir(l.dir)
}

/*!
 * Replay reads a segment, calling fn for each record in order.
 *
 * A short or checksum-failing frame at the very end of the file is a torn
 * write — the process died mid-append — and replay stops cleanly there,
 * because everything before it is intact and everything after it never
 * existed. The same failure with more bytes following it is real corruption
 * and is reported: silently skipping it would lose records without telling
 * anyone.
 */
func (l *Log) Replay(segment string, fn func(*model.Record) error) error {
	file, err := os.Open(filepath.Join(l.dir, segment))
	if nil != err {
		return err
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, 256<<10)

	header := make([]byte, len(magic))
	if _, err := io.ReadFull(reader, header); nil != err {
		// A segment that died before its header was written holds nothing.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		return err
	}
	if magic != string(header) {
		return fmt.Errorf("%w: %s has a bad header", ErrCorrupt, segment)
	}

	var frame [8]byte

	for {
		if _, err := io.ReadFull(reader, frame[:]); nil != err {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}

		length := binary.LittleEndian.Uint32(frame[0:4])
		checksum := binary.LittleEndian.Uint32(frame[4:8])

		if length > maxFrame {
			return fmt.Errorf("%w: %s declares a %d byte record", ErrCorrupt, segment, length)
		}

		payload := make([]byte, length)
		if _, err := io.ReadFull(reader, payload); nil != err {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}

		/*!
		 * A zero length is never a frame: the smallest record Append writes
		 * still encodes to a few bytes. It is what a filesystem leaves after a
		 * power loss when it had extended the file but not yet written the data
		 * — the tail reads back as zeros, which is a length of 0 and a checksum
		 * of 0, and crc32c of nothing happens to be 0 as well.
		 */
		if 0 == length || crc32.Checksum(payload, castagnoli) != checksum {
			// If nothing but zeros follows, this was the frame being written
			// when the process died, and no intact frame lies beyond it: a
			// real one never starts with a zero length. If anything else
			// follows, the file is genuinely damaged.
			torn, err := zeroToEnd(reader)
			if nil != err {
				return err
			}
			if torn {
				return nil
			}
			return fmt.Errorf("%w: checksum mismatch in %s", ErrCorrupt, segment)
		}

		var encoded walRecord
		if err := json.Unmarshal(payload, &encoded); nil != err {
			return fmt.Errorf("%w: %s: %v", ErrCorrupt, segment, err)
		}

		rec, err := encoded.decode()
		if nil != err {
			return fmt.Errorf("%w: %s: %v", ErrCorrupt, segment, err)
		}

		if err := fn(rec); nil != err {
			return err
		}
	}
}

// Close syncs and releases the active segment. The segment stays on disk: it
// may hold records that are not in Parquet yet.
func (l *Log) Close() error {
	l.syncing.Lock()
	defer l.syncing.Unlock()

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}

	err := l.syncLocked()
	l.closed = true

	if closeErr := l.file.Close(); nil == err {
		err = closeErr
	}

	return err
}

// Dir is the log's directory.
func (l *Log) Dir() string { return l.dir }

// SyncInterval is how often the caller should be calling Sync.
func (l *Log) SyncInterval() time.Duration { return l.interval }

// zeroToEnd reports whether everything left in r is zero bytes, including
// nothing at all.
func zeroToEnd(r io.Reader) (bool, error) {
	buffer := make([]byte, 32<<10)

	for {
		n, err := r.Read(buffer)

		for _, b := range buffer[:n] {
			if 0 != b {
				return false, nil
			}
		}

		if errors.Is(err, io.EOF) {
			return true, nil
		}
		if nil != err {
			return false, err
		}
	}
}

/*!
 * syncDir makes a directory's entries durable.
 *
 * Only a filesystem saying it cannot fsync a directory at all — EINVAL, or
 * ENOTSUP — is ignored. One saying it tried and failed is an error, because
 * the caller is about to rely on a segment's name surviving a power loss.
 */
func syncDir(dir string) error {
	if "windows" == runtime.GOOS {
		return nil
	}

	handle, err := os.Open(dir)
	if nil != err {
		return err
	}
	defer handle.Close()

	if err := handle.Sync(); nil != err &&
		!errors.Is(err, syscall.EINVAL) && !errors.Is(err, errors.ErrUnsupported) {
		return fmt.Errorf("syncing %s: %w", dir, err)
	}

	return nil
}

func segmentName(number uint64) string {
	return fmt.Sprintf("%s%012d%s", segmentPrefix, number, segmentSuffix)
}

func segmentNumber(name string) (uint64, error) {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(name, segmentPrefix), segmentSuffix)
	return strconv.ParseUint(trimmed, 10, 64)
}
