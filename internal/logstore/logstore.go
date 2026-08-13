/*!
 * The log store interface, and the subscriber fanout every implementation
 * shares.
 *
 * P0 ships only the in-memory store, which is v1's behaviour with a v2 record
 * model behind it. P1 adds the durable store — WAL, L0 Parquet flush, catalog
 * — behind this same interface, so the API layer above never learns which one
 * it is talking to.
 *
 * Append, Streams and Backlog take a context and return an error even though
 * the memory store can never use either. That is deliberate: the durable store
 * does I/O, and retrofitting a signature is the kind of change that touches
 * every caller.
 */

package logstore

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/kilianc/rtail/v2/internal/model"
)

// EventKind distinguishes the two things a subscriber hears about.
type EventKind uint8

const (
	// EventLine is a record on the stream the subscriber selected.
	EventLine EventKind = iota
	// EventStreams is the full stream list, sent when it changes.
	EventStreams
)

// Event is one message to a subscriber.
type Event struct {
	Kind    EventKind
	Record  *model.Record
	Streams []string
}

// Store is a durable (or not) home for log records.
type Store interface {
	// Append records one event. The store assigns its Seq.
	Append(ctx context.Context, rec *model.Record) error

	// Streams lists every known stream.
	Streams(ctx context.Context) ([]string, error)

	// Backlog returns up to limit of the most recent records on a stream, in
	// chronological order.
	Backlog(ctx context.Context, stream string, limit int) ([]*model.Record, error)

	// Subscribe opens a live feed. An empty stream means every stream, which
	// is what the explorer's "All streams" is.
	Subscribe(stream string, buffer int) *Subscription

	// Close releases resources and disconnects every subscriber.
	Close() error
}

/*!
 * Subscription is one live feed.
 *
 * Sends are non-blocking. A browser that stops reading must never be able to
 * stall ingest, so a full buffer drops the event and bumps a counter the API
 * layer can report — visible loss beats invisible backpressure.
 */
type Subscription struct {
	// C delivers events until the subscription is closed.
	C <-chan Event

	ch      chan Event
	stream  string
	dropped atomic.Uint64
	once    sync.Once
	unsub   func(*Subscription)
}

// Dropped counts events discarded because the subscriber was not keeping up.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Close detaches the subscription. Safe to call more than once.
func (s *Subscription) Close() {
	s.once.Do(func() { s.unsub(s) })
}

// send delivers without blocking, counting anything that does not fit.
func (s *Subscription) send(event Event) {
	select {
	case s.ch <- event:
	default:
		s.dropped.Add(1)
	}
}

/*!
 * Fanout is the shared subscriber registry.
 *
 * Publishing holds the read lock while sending; Close takes the write lock
 * before removing a subscription and closing its channel, so by the time the
 * channel is closed no publisher can still be inside the map. That ordering is
 * what makes "close the channel so the reader's range ends" safe here.
 */
type Fanout struct {
	mu     sync.RWMutex
	next   uint64
	subs   map[uint64]*Subscription
	ids    map[*Subscription]uint64
	closed bool
}

// Subscribe registers a feed for one stream, or for every stream when the
// name is empty.
func (f *Fanout) Subscribe(stream string, buffer int) *Subscription {
	if buffer < 1 {
		buffer = 256
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if nil == f.subs {
		f.subs = make(map[uint64]*Subscription)
		f.ids = make(map[*Subscription]uint64)
	}

	ch := make(chan Event, buffer)
	sub := &Subscription{C: ch, ch: ch, stream: stream, unsub: f.remove}

	if f.closed {
		close(ch)
		return sub
	}

	id := f.next
	f.next++
	f.subs[id] = sub
	f.ids[sub] = id

	return sub
}

func (f *Fanout) remove(sub *Subscription) {
	f.mu.Lock()
	defer f.mu.Unlock()

	id, ok := f.ids[sub]
	if !ok {
		return
	}

	delete(f.subs, id)
	delete(f.ids, sub)
	close(sub.ch)
}

/*!
 * PublishLine delivers a record to everyone watching its stream.
 *
 * An empty subscription matches everything. In v1 it meant the opposite —
 * "send me nothing" was how a paused tab was expressed — but v2 has no paused
 * subscription: switching to history closes the connection outright, and an
 * unfiltered tail across every stream is the explorer's default view.
 */
func (f *Fanout) PublishLine(rec *model.Record) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	for _, sub := range f.subs {
		if "" == sub.stream || sub.stream == rec.Stream {
			sub.send(Event{Kind: EventLine, Record: rec})
		}
	}
}

// PublishStreams delivers a stream list to every subscriber.
func (f *Fanout) PublishStreams(streams []string) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	for _, sub := range f.subs {
		sub.send(Event{Kind: EventStreams, Streams: streams})
	}
}

// Close disconnects every subscriber and rejects new ones.
func (f *Fanout) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return
	}
	f.closed = true

	for id, sub := range f.subs {
		delete(f.subs, id)
		delete(f.ids, sub)
		close(sub.ch)
	}
}
