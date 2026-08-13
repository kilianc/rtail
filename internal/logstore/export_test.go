/*!
 * Test hooks for simulating crashes.
 *
 * Only compiled into tests. They exist because the interesting failures in a
 * storage engine are all "the process died between these two steps", and the
 * only way to assert on those deterministically is to be able to stop at each
 * step on purpose.
 */

package logstore

import "context"

// SyncForTest fsyncs the write-ahead log.
func (d *Durable) SyncForTest() error { return d.log.Sync() }

/*!
 * AbandonForTest simulates the process dying.
 *
 * Background work stops and file handles are released — which a real crash
 * gets from the OS, and which the next Open needs in order to take the SQLite
 * lock — but nothing buffered is flushed and no WAL segment is removed. The
 * on-disk state is exactly what a `kill -9` would have left.
 */
func (d *Durable) AbandonForTest() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	d.mu.Unlock()

	close(d.stop)
	<-d.done

	d.fanout.Close()
	d.closeParts()
}

// FlushKeepingWALForTest writes and registers the batch but leaves the sealed
// segment in place, reproducing a crash after the catalog commit and before
// the segment was retired.
func (d *Durable) FlushKeepingWALForTest(ctx context.Context) error {
	return d.flush(ctx, false)
}
