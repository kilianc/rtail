/*!
 * Test hooks. Only compiled into tests.
 */

package ingest

import "time"

// Resolve3164ForTest exposes the BSD timestamp resolution so the year-boundary
// case can be tested without waiting for New Year's Eve.
func Resolve3164ForTest(stamp, now time.Time) time.Time { return resolve3164(stamp, now) }
