/*!
 * The in-memory store.
 *
 * This is v1's behaviour — a bounded ring of recent lines per stream, and
 * nothing on disk — expressed against the v2 record model and the v2 Store
 * interface. It stays in the tree after P1 as the zero-configuration mode:
 * `rtail-server` with no --data flag should still be the thing you can run in
 * one command and pipe into, with no directory to manage.
 */

package logstore

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/kilianc/rtail/v2/internal/model"
)

// DefaultBacklog matches v1's per-stream history.
const DefaultBacklog = 100

// Memory keeps a bounded ring of recent records per stream.
type Memory struct {
	fanout Fanout
	seq    atomic.Uint64

	mu      sync.RWMutex
	size    int
	order   []string
	streams map[string]*ring
}

var _ Store = (*Memory)(nil)

// NewMemory creates a store keeping size records per stream.
func NewMemory(size int) *Memory {
	if size < 1 {
		size = DefaultBacklog
	}

	return &Memory{size: size, streams: make(map[string]*ring)}
}

func (m *Memory) Append(_ context.Context, rec *model.Record) error {
	rec.Seq = m.seq.Add(1)

	m.mu.Lock()
	buffer, known := m.streams[rec.Stream]
	if !known {
		buffer = newRing(m.size)
		m.streams[rec.Stream] = buffer
		m.order = append(m.order, rec.Stream)
	}
	buffer.push(rec)

	// Snapshot the list while still holding the lock; publishing happens after
	// it is released so a slow subscriber cannot stall the next Append.
	var streams []string
	if !known {
		streams = append([]string(nil), m.order...)
	}
	m.mu.Unlock()

	if nil != streams {
		m.fanout.PublishStreams(streams)
	}

	m.fanout.PublishLine(rec)
	return nil
}

func (m *Memory) Streams(context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return append([]string(nil), m.order...), nil
}

/*!
 * Backlog returns recent records, merging every stream when none is named.
 *
 * The merge matters: "All streams" is the explorer's default view, and a tail
 * that opens with an empty screen and then trickles looks broken next to one
 * that opens with the last hundred lines already there.
 */
func (m *Memory) Backlog(_ context.Context, stream string, limit int) ([]*model.Record, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if "" != stream {
		buffer, ok := m.streams[stream]
		if !ok {
			return nil, nil
		}
		return buffer.slice(limit), nil
	}

	var merged []*model.Record
	for _, buffer := range m.streams {
		merged = append(merged, buffer.slice(0)...)
	}

	// Seq is monotonic across streams, so it is the merge order.
	sort.Slice(merged, func(i, j int) bool { return merged[i].Seq < merged[j].Seq })

	if limit <= 0 {
		limit = m.size
	}
	if len(merged) > limit {
		merged = merged[len(merged)-limit:]
	}

	return merged, nil
}

func (m *Memory) Subscribe(stream string, buffer int) *Subscription {
	return m.fanout.Subscribe(stream, buffer)
}

func (m *Memory) Close() error {
	m.fanout.Close()
	return nil
}

/*!
 * ring is a fixed-capacity circular buffer of records.
 *
 * v1 used shift() on a plain array, which is O(n) per line once the backlog is
 * full. At 100 lines that is invisible; at the 10k backlog a real logging
 * system wants it is not.
 */
type ring struct {
	items []*model.Record
	start int
	count int
}

func newRing(size int) *ring {
	return &ring{items: make([]*model.Record, size)}
}

func (r *ring) push(rec *model.Record) {
	end := (r.start + r.count) % len(r.items)
	r.items[end] = rec

	if r.count == len(r.items) {
		r.start = (r.start + 1) % len(r.items)
	} else {
		r.count++
	}
}

// slice returns up to limit of the newest records, oldest first.
func (r *ring) slice(limit int) []*model.Record {
	count := r.count
	if limit > 0 && limit < count {
		count = limit
	}

	out := make([]*model.Record, 0, count)
	for i := r.count - count; i < r.count; i++ {
		out = append(out, r.items[(r.start+i)%len(r.items)])
	}

	return out
}
