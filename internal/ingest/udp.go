/*!
 * The v1 UDP receiver.
 *
 * This protocol is frozen. `cmd | rtail` is rTail's entire onboarding story and
 * the npm client is already installed on machines we will never hear about, so
 * the datagram shape — {id, timestamp, content} — does not change, ever. New
 * capabilities arrive on new receivers (HTTP, syslog, OTLP in P5), not by
 * breaking this one.
 *
 * One consequence worth knowing about: the client parses the line with JSON5
 * before sending, so the original text is gone by the time it reaches us and
 * Record.Raw is a reconstruction. Receivers that get the real bytes produce a
 * faithful Raw; this one cannot.
 */

package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/normalize"
)

// maxDatagram is the largest payload IPv4 UDP can carry.
const maxDatagram = 65507

// UDPStats counts what the receiver has seen, for /healthz and later /metrics.
type UDPStats struct {
	Received atomic.Uint64
	Invalid  atomic.Uint64
	Bytes    atomic.Uint64
}

// UDP receives v1 datagrams and appends them to a store.
type UDP struct {
	conn  *net.UDPConn
	store logstore.Store
	log   *slog.Logger
	stats UDPStats
}

// datagram is the frozen v1 wire format.
type datagram struct {
	ID string `json:"id"`
	// Timestamp is epoch milliseconds, chosen by the client — which may have
	// extracted it from the line itself.
	Timestamp int64           `json:"timestamp"`
	Content   json.RawMessage `json:"content"`
}

// ListenUDP binds the receiver.
func ListenUDP(host string, port int, store logstore.Store, log *slog.Logger) (*UDP, error) {
	addr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, itoa(port)))
	if nil != err {
		return nil, err
	}

	conn, err := net.ListenUDP("udp4", addr)
	if nil != err {
		return nil, err
	}

	return &UDP{conn: conn, store: store, log: log}, nil
}

// Addr is the bound address, which is how tests discover an ephemeral port.
func (u *UDP) Addr() *net.UDPAddr { return u.conn.LocalAddr().(*net.UDPAddr) }

// Stats exposes the receiver's counters.
func (u *UDP) Stats() *UDPStats { return &u.stats }

/*!
 * Serve reads until the context is cancelled or the socket is closed.
 *
 * A malformed datagram is counted and dropped rather than logged at anything
 * louder than debug: this port is reachable by anything on the network, and a
 * stray packet must not be able to fill the operator's disk with log noise.
 */
func (u *UDP) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		u.conn.Close()
	}()

	buffer := make([]byte, maxDatagram)

	for {
		n, remote, err := u.conn.ReadFromUDP(buffer)
		if nil != err {
			if nil != ctx.Err() || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		u.stats.Received.Add(1)
		u.stats.Bytes.Add(uint64(n))

		var payload datagram
		if err := json.Unmarshal(buffer[:n], &payload); nil != err || "" == payload.ID {
			u.stats.Invalid.Add(1)
			u.log.Debug("dropping invalid datagram", "from", remote.String(), "bytes", n)
			continue
		}

		var clientTs time.Time
		if 0 != payload.Timestamp {
			clientTs = time.UnixMilli(payload.Timestamp).UTC()
		}

		rec := normalize.FromPayload(payload.ID, remote.IP.String(), remote.Port, clientTs, payload.Content)

		if err := u.store.Append(ctx, rec); nil != err {
			u.log.Error("appending record", "stream", rec.Stream, "err", err)
		}
	}
}

// Close releases the socket.
func (u *UDP) Close() error { return u.conn.Close() }

// itoa avoids pulling strconv in for a single call site.
func itoa(n int) string {
	if 0 == n {
		return "0"
	}

	var digits [20]byte
	i := len(digits)

	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}

	return string(digits[i:])
}
