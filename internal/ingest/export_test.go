/*!
 * Test hooks. Only compiled into tests.
 */

package ingest

import "time"

// Resolve3164ForTest exposes the BSD timestamp resolution so the year-boundary
// case can be tested without waiting for New Year's Eve.
func Resolve3164ForTest(stamp, now time.Time) time.Time { return resolve3164(stamp, now) }

// SetIdleForTest shortens a syslog receiver's idle timeout, so the deadline can
// be tested without waiting minutes for it. Call it before Serve.
func SetIdleForTest(s *Syslog, idle time.Duration) { s.idle = idle }

// MaxLineForTest is the longest line the receivers store.
const MaxLineForTest = maxLine

// MaxInflationForTest is how far a compressed body may expand, in multiples
// of MaxBody.
const MaxInflationForTest = maxInflation
