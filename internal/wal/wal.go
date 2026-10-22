/*!
 * The write-ahead log.
 *
 * This is the difference between a logging system and a log viewer. Without
 * it, a flush buffer holds up to 30 seconds of everything and a crash takes
 * all of it; with it, the worst case is the sync interval.
 *
 * Shape, and where it diverges from docs/proposal-logging-system.md §4.2: the
 * proposal describes a WAL per stream-shard. This is a single log for every
 * stream, in numbered segments. The reason is truncation — a segment can only
 * be deleted once everything in it is durably in Parquet, so a shared log
 * means a flush cycle has to cover every buffered stream at once. That is a
 * feature rather than a compromise: it makes one fsync serve all streams
 * instead of N, and the tiny-file problem it could cause is the same one §3.4
 * already solves by pooling low-volume streams.
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
	"sort"
	"strconv"
	"strings"
	"sync"
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

// Log is an append-only record log in numbered segments.
type Log struct {
	dir      string
	interval time.Duration

	mu      sync.Mutex
	file    *os.File
	writer  *bufio.Writer
	active  uint64
	dirty   bool
	closed  bool
	pending int
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
	path := filepath.Join(l.dir, segmentName(number))

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if nil != err {
		return fmt.Errorf("opening wal segment: %w", err)
	}

	writer := bufio.NewWriterSize(file, 256<<10)

	if _, err := writer.WriteString(magic); nil != err {
		file.Close()
		return err
	}

	l.file, l.writer, l.active, l.dirty, l.pending = file, writer, number, true, 0
	return nil
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
 */
func (l *Log) Append(rec *model.Record) error {
	payload, err := json.Marshal(encode(rec))
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

	var header [8]byte
	binary.LittleEndian.PutUint32(header[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(header[4:8], crc32.Checksum(payload, castagnoli))

	if _, err := l.writer.Write(header[:]); nil != err {
		return err
	}
	if _, err := l.writer.Write(payload); nil != err {
		return err
	}

	l.dirty = true
	l.pending++

	return nil
}

// Sync flushes and fsyncs everything appended so far.
func (l *Log) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.syncLocked()
}

func (l *Log) syncLocked() error {
	if !l.dirty || l.closed {
		return nil
	}

	if err := l.writer.Flush(); nil != err {
		return err
	}
	if err := l.file.Sync(); nil != err {
		return err
	}

	l.dirty = false
	l.pending = 0

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
 */
func (l *Log) Rotate() (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return "", errors.New("wal: log is closed")
	}

	if err := l.syncLocked(); nil != err {
		return "", err
	}
	if err := l.file.Close(); nil != err {
		return "", err
	}

	sealed := segmentName(l.active)

	if err := l.openSegment(l.active + 1); nil != err {
		return "", err
	}

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

		if crc32.Checksum(payload, castagnoli) != checksum {
			// If nothing follows, this was the frame being written when the
			// process died. If something does, the file is genuinely damaged.
			if _, err := reader.Peek(1); errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("%w: checksum mismatch in %s", ErrCorrupt, segment)
		}

		var encoded walRecord
		if err := json.Unmarshal(payload, &encoded); nil != err {
			return fmt.Errorf("%w: %s: %v", ErrCorrupt, segment, err)
		}

		if err := fn(encoded.decode()); nil != err {
			return err
		}
	}
}

// Close syncs and releases the active segment. The segment stays on disk: it
// may hold records that are not in Parquet yet.
func (l *Log) Close() error {
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

func segmentName(number uint64) string {
	return fmt.Sprintf("%s%012d%s", segmentPrefix, number, segmentSuffix)
}

func segmentNumber(name string) (uint64, error) {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(name, segmentPrefix), segmentSuffix)
	return strconv.ParseUint(trimmed, 10, 64)
}
