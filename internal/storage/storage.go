/*!
 * The blob storage abstraction.
 *
 * This interface exists in P0, with only a local-disk implementation and no
 * caller yet, on purpose. docs/proposal-logging-system.md §9.1 makes the point:
 * the cloud version of rTail is this same engine with S3 underneath, and that
 * is a six-week job if the seam exists from the start and a six-month one if
 * the compactor is written against os.Open.
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

	// Open reads an existing object. Seeking is required: Parquet readers seek
	// to the footer before reading anything else.
	Open(ctx context.Context, name string) (io.ReadSeekCloser, error)

	// Remove deletes an object. Removing a missing object is not an error —
	// the GC pass in §5 is expected to be retried.
	Remove(ctx context.Context, name string) error

	// List returns every object under a prefix, sorted by name.
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)

	// Stat describes a single object.
	Stat(ctx context.Context, name string) (ObjectInfo, error)

	// Close releases any resources held by the backend.
	Close() error
}
