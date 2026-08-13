/*!
 * Test hooks for simulating crashes.
 *
 * Only compiled into tests. The interesting failures in a compactor are all
 * "the process died between these two steps", and the only way to assert on
 * those deterministically is to stop at each step on purpose.
 */

package compact

// StopBeforePublishForTest makes the next merge abort after committing its
// output object and before the catalog transaction — reproducing a crash in
// the one window where a file exists that nothing points at.
func (c *Compactor) StopBeforePublishForTest(fn func() error) {
	c.beforePublish = fn
}
