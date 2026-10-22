/*!
 * The blob storage abstraction.
 *
 * This interface exists with only a local-disk implementation behind it, on
 * purpose. An object store is the one other backend this engine would
 * plausibly ever want, and swapping local disk for S3 is a small job if the
 * seam is here from the start and a very large one if the compactor is written
 * against os.Open.
 *
 * The one non-obvious thing in here is Commit/Abort. Parquet files must appear
 * whole or not at all — a half-written file that the catalog already points at
 * is a corrupt table. S3 gets that for free because PUT is atomic; a local
 * filesystem needs write-to-temp, fsync, rename, fsync-parent. Putting the
 * semantic in the interface means the compactor never has to know which one it
 * is talking to.
 */

package storage

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"time"
)

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Name     string
	Size     int64
	Modified time.Time
}

/*!
 * Reader is a readable object.
 *
 * ReaderAt is part of the contract rather than a type assertion at the call
 * site because Parquet is fundamentally a random-access format — a reader
 * seeks to the footer before it reads anything else, then jumps between column
 * chunks. A backend that can only stream cannot serve Parquet without buffering
 * whole files into memory, so it is better to say so in the interface. S3
 * implements this with ranged GETs.
 */
type Reader interface {
	io.ReaderAt
	io.ReadSeekCloser
}

/*!
 * Writer is an in-progress object.
 *
 * Commit and Abort rather than Close: `defer w.Abort()` alongside an explicit
 * `w.Commit()` is safe, whereas a deferred Close cannot tell the difference
 * between "done" and "bailing out halfway through" and will happily publish a
 * truncated file.
 */
type Writer interface {
	io.Writer

	// Commit flushes, durably persists, and makes the object visible under its
	// final name. After a successful Commit the object is readable.
	Commit() error

	// Abort discards the object. It is a no-op after a successful Commit, so
	// it is always safe to defer.
	Abort() error
}

// Backend is a flat, prefix-addressed object store.
type Backend interface {
	// Create begins writing an object. It is not visible until Commit.
	Create(ctx context.Context, name string) (Writer, error)

	// Open reads an existing object.
	Open(ctx context.Context, name string) (Reader, error)

	// Remove deletes an object. Removing a missing object is not an error —
	// the compactor's GC pass is expected to be retried.
	Remove(ctx context.Context, name string) error

	// List returns every object under a prefix, sorted by name.
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)

	// Stat describes a single object.
	Stat(ctx context.Context, name string) (ObjectInfo, error)

	// Sweep removes uncommitted objects under a prefix that were abandoned
	// before the cutoff — what a write killed before Commit or Abort leaves
	// behind — and returns how many it removed. On S3 this is aborting stale
	// multipart uploads.
	Sweep(ctx context.Context, prefix string, before time.Time) (int, error)

	// Close releases any resources held by the backend.
	Close() error
}

// maxSegment bounds one segment of an object name, well under the 255 bytes
// nearly every filesystem allows a name.
const maxSegment = 128

/*!
 * SafeSegment turns an arbitrary string into one path segment every backend
 * accepts. Stream ids are the reason: they come from `rtail --id` and from a
 * query string, which is to say from anywhere at all.
 *
 * Anything outside [A-Za-z0-9._-] becomes an underscore, and a name made of
 * dots gains one in front so it cannot climb the tree. A name longer than
 * maxSegment keeps its beginning and ends in a hash of the whole of it.
 * Without the cap, a 300-character id failed every write with "file name too
 * long": its segment never flushed, the segment holding it was retried on
 * every cycle, and a restart refused to boot. The hash keeps two long ids that
 * share a prefix in different directories.
 *
 * The flush and the compactor both name objects through it, so a stream's
 * files share one directory whichever of them wrote the file.
 */
func SafeSegment(name string) string {
	if "" == name {
		return "_unnamed"
	}

	out := []rune(name)
	for i, r := range out {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			'-' == r, '_' == r, '.' == r:
		default:
			out[i] = '_'
		}
	}

	// A name of dots would escape the directory tree.
	if "." == string(out) || ".." == string(out) {
		return "_" + string(out)
	}

	// Every rune is ASCII by now, so this counts bytes.
	if len(out) > maxSegment {
		hash := fnv.New32a()
		hash.Write([]byte(name))

		return fmt.Sprintf("%s-%08x", string(out[:maxSegment-9]), hash.Sum32())
	}

	return string(out)
}
