package query_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/normalize"
	"github.com/kilianc/rtail/v2/internal/query"
)

/*!
 * harness runs records through the real write path.
 *
 * Not a fixture: the whole point of these tests is that what the writer
 * produces is what the reader can answer questions about, so anything that
 * shortcut the flush would be testing a fiction.
 */
type harness struct {
	dir    string
	store  *logstore.Durable
	engine *query.Engine
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	ctx := context.Background()
	dir := t.TempDir()

	store, err := logstore.OpenDurable(ctx, dir, logstore.DurableOptions{
		Backlog:       1000,
		KeepRaw:       true,
		FlushInterval: time.Hour,
		SyncInterval:  -1,
		Grace:         time.Hour,
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if nil != err {
		t.Fatal(err)
	}

	engine, err := query.Open(store.Catalog(), store.Backend(), query.Limits{})
	if nil != err {
		store.Close()
		t.Fatal(err)
	}

	t.Cleanup(func() {
		engine.Close()
		store.Close()
	})

	return &harness{dir: dir, store: store, engine: engine}
}

// ingest normalizes and flushes a batch, returning the records as written.
func (h *harness) ingest(t *testing.T, stream string, payloads []string) []*model.Record {
	t.Helper()

	ctx := context.Background()
	records := make([]*model.Record, 0, len(payloads))

	for _, payload := range payloads {
		rec := normalize.FromLine(stream, "10.0.0.1", 5000, []byte(payload))

		if err := h.store.Append(ctx, rec); nil != err {
			t.Fatal(err)
		}
		records = append(records, rec)
	}

	if err := h.store.Flush(ctx); nil != err {
		t.Fatal(err)
	}

	return records
}

// request builds a search over a window wide enough to include everything the
// tests write.
func request(q string) query.Request {
	now := time.Now().UTC()

	return query.Request{
		Query: q,
		From:  now.Add(-time.Hour),
		To:    now.Add(time.Hour),
		Limit: 1000,
	}
}
