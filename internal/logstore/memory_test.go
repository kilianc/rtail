package logstore

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
)

func record(stream, msg string) *model.Record {
	return &model.Record{Stream: stream, Msg: msg, Raw: msg, Type: "string", Ts: time.Now()}
}

func TestBacklogIsBoundedAndOrdered(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(3)

	for i := range 5 {
		if err := store.Append(ctx, record("api", fmt.Sprintf("line-%d", i))); nil != err {
			t.Fatal(err)
		}
	}

	backlog, err := store.Backlog(ctx, "api", 0)
	if nil != err {
		t.Fatal(err)
	}

	if 3 != len(backlog) {
		t.Fatalf("backlog = %d records, want 3", len(backlog))
	}

	// Oldest first, and the two earliest lines have been evicted.
	for i, want := range []string{"line-2", "line-3", "line-4"} {
		if backlog[i].Msg != want {
			t.Errorf("backlog[%d] = %q, want %q", i, backlog[i].Msg, want)
		}
	}
}

func TestBacklogLimit(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(10)

	for i := range 6 {
		store.Append(ctx, record("api", fmt.Sprintf("line-%d", i)))
	}

	backlog, _ := store.Backlog(ctx, "api", 2)
	if 2 != len(backlog) {
		t.Fatalf("backlog = %d records, want 2", len(backlog))
	}

	// A limit takes the newest, still oldest-first.
	if "line-4" != backlog[0].Msg || "line-5" != backlog[1].Msg {
		t.Errorf("backlog = %q, %q, want line-4, line-5", backlog[0].Msg, backlog[1].Msg)
	}
}

func TestSeqIsMonotonicAcrossStreams(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(10)

	first := record("api", "a")
	second := record("worker", "b")
	third := record("api", "c")

	for _, rec := range []*model.Record{first, second, third} {
		store.Append(ctx, rec)
	}

	if !(first.Seq < second.Seq && second.Seq < third.Seq) {
		t.Errorf("seq = %d, %d, %d — want strictly increasing", first.Seq, second.Seq, third.Seq)
	}
}

func TestUnknownStreamHasNoBacklog(t *testing.T) {
	backlog, err := NewMemory(10).Backlog(context.Background(), "nope", 0)
	if nil != err {
		t.Fatal(err)
	}
	if 0 != len(backlog) {
		t.Errorf("backlog = %v, want empty", backlog)
	}
}

func TestStreamsAreListedInFirstSeenOrder(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(10)

	store.Append(ctx, record("beta", "1"))
	store.Append(ctx, record("alpha", "2"))
	store.Append(ctx, record("beta", "3"))

	streams, _ := store.Streams(ctx)
	if 2 != len(streams) || "beta" != streams[0] || "alpha" != streams[1] {
		t.Errorf("streams = %v, want [beta alpha]", streams)
	}
}

func TestSubscriberReceivesLinesForItsStreamOnly(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(10)

	sub := store.Subscribe("api", 16)
	defer sub.Close()

	store.Append(ctx, record("api", "mine"))
	store.Append(ctx, record("other", "theirs"))
	store.Append(ctx, record("api", "also mine"))

	var lines []string

	// The first stream-list event arrives before the first line on a stream we
	// have not seen before, so read until both lines are in.
	deadline := time.After(2 * time.Second)
	for len(lines) < 2 {
		select {
		case event := <-sub.C:
			if EventLine == event.Kind {
				lines = append(lines, event.Record.Msg)
			}
		case <-deadline:
			t.Fatalf("timed out with %v", lines)
		}
	}

	if "mine" != lines[0] || "also mine" != lines[1] {
		t.Errorf("lines = %v, want [mine, also mine]", lines)
	}
}

func TestNewStreamsAreAnnouncedToEveryone(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(10)

	// An empty stream is how the webapp represents a paused tab: it still
	// wants to hear about new streams.
	sub := store.Subscribe("", 16)
	defer sub.Close()

	store.Append(ctx, record("api", "hello"))

	select {
	case event := <-sub.C:
		if EventStreams != event.Kind {
			t.Fatalf("kind = %v, want EventStreams", event.Kind)
		}
		if 1 != len(event.Streams) || "api" != event.Streams[0] {
			t.Errorf("streams = %v, want [api]", event.Streams)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no stream announcement")
	}

	// ... but no line events, having selected no stream.
	store.Append(ctx, record("api", "second"))

	select {
	case event := <-sub.C:
		t.Fatalf("unexpected event %v", event.Kind)
	case <-time.After(100 * time.Millisecond):
	}
}

// A browser that stops reading must never stall ingest.
func TestSlowSubscriberDropsInsteadOfBlocking(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(1000)

	sub := store.Subscribe("api", 4)
	defer sub.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			store.Append(ctx, record("api", fmt.Sprintf("line-%d", i)))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Append blocked on a subscriber that was not reading")
	}

	if 0 == sub.Dropped() {
		t.Error("expected drops to be counted")
	}

	// Everything is still in the backlog — only the live feed lost anything.
	backlog, _ := store.Backlog(ctx, "api", 0)
	if 200 != len(backlog) {
		t.Errorf("backlog = %d, want 200", len(backlog))
	}
}

func TestCloseIsIdempotentAndEndsTheFeed(t *testing.T) {
	store := NewMemory(10)
	sub := store.Subscribe("api", 4)

	sub.Close()
	sub.Close()

	select {
	case _, open := <-sub.C:
		if open {
			t.Error("expected the channel to be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("channel was not closed")
	}

	// Appending after a subscriber is gone must not panic on a closed channel.
	if err := store.Append(context.Background(), record("api", "after")); nil != err {
		t.Fatal(err)
	}
}

func TestStoreCloseDisconnectsSubscribers(t *testing.T) {
	store := NewMemory(10)
	sub := store.Subscribe("api", 4)

	store.Close()

	select {
	case _, open := <-sub.C:
		if open {
			t.Error("expected the channel to be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not disconnect the subscriber")
	}

	// Subscribing to a closed store yields an already-closed feed rather than
	// one that hangs forever.
	late := store.Subscribe("api", 4)
	if _, open := <-late.C; open {
		t.Error("expected a closed feed from a closed store")
	}
}

// Run with -race: this is the shape that finds fanout bugs.
func TestConcurrentAppendAndSubscribe(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(100)
	defer store.Close()

	var wg sync.WaitGroup

	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				store.Append(ctx, record(fmt.Sprintf("stream-%d", w), fmt.Sprintf("line-%d", i)))
			}
		}()
	}

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				sub := store.Subscribe("stream-0", 8)
				select {
				case <-sub.C:
				default:
				}
				sub.Close()
			}
		}()
	}

	wg.Wait()

	streams, _ := store.Streams(ctx)
	if 4 != len(streams) {
		t.Errorf("streams = %v, want 4", streams)
	}
}
