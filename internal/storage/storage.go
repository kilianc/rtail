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

	// Close releases any resources held by the backend.
	Close() error
}
