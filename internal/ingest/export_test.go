/*!
 * Test hooks. Only compiled into tests.
 */

package ingest

import (
	"net"
	"time"
)

// Resolve3164ForTest exposes the BSD timestamp resolution so the year-boundary
// case can be tested without waiting for New Year's Eve.
func Resolve3164ForTest(stamp, now time.Time) time.Time { return resolve3164(stamp, now) }

// SetIdleForTest shortens a syslog receiver's idle timeout, so the deadline can
// be tested without waiting minutes for it. Call it before Serve.
func SetIdleForTest(s *Syslog, idle time.Duration) { s.idle = idle }

// SetMaxConnsForTest lowers how many TCP connections a syslog receiver serves
// at once, so the limit can be reached without opening a thousand. Call it
// before Serve.
func SetMaxConnsForTest(s *Syslog, n int) { s.slots = make(chan struct{}, n) }

// WrapTCPForTest puts wrap around a syslog receiver's TCP listener, so a test
// can make Accept fail. Call it before Serve.
func WrapTCPForTest(s *Syslog, wrap func(net.Listener) net.Listener) { s.tcp = wrap(s.tcp) }

// SetBodyTimeoutForTest shortens how long the HTTP receivers wait for a body,
// so the deadline can be tested without waiting minutes for it. Call it
// before serving.
func SetBodyTimeoutForTest(h *HTTP, timeout time.Duration) { h.timeout = timeout }

// MaxRecordForTest is the largest OTLP record rendering stored.
const MaxRecordForTest = maxRecord

// MaxLineForTest is the longest line the receivers store.
const MaxLineForTest = maxLine

// MaxInflationForTest is how far a compressed body may expand, in multiples
// of MaxBody.
const MaxInflationForTest = maxInflation
