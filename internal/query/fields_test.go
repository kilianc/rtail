package query_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/kilianc/rtail/v2/internal/ingest"
	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/query"
)

/*!
 * A searched syslog line carries the fields its receiver parsed out of it.
 *
 * Its raw is the line as sent, not JSON, so nothing can be re-promoted from
 * it: the fields exist only as the promoted columns the flush wrote. The tail
 * showed them and search did not, so the field explorer and click-to-filter
 * had nothing to offer on any historical syslog page. End to end, through the
 * real receiver, because the receiver is what decides the record's shape.
 */
func TestSearchedSyslogKeepsItsFields(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	receiver, err := ingest.ListenSyslog("127.0.0.1", 0, ingest.SyslogOptions{
		Store: h.store,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if nil != err {
		t.Fatal(err)
	}

	serving, cancel := context.WithCancel(ctx)
	go receiver.Serve(serving)
	t.Cleanup(func() {
		cancel()
		receiver.Close()
	})

	conn, err := net.DialUDP("udp4", nil, receiver.Addr())
	if nil != err {
		t.Fatal(err)
	}
	defer conn.Close()

	// Priority 11 = facility 1 (user), severity 3 (ERROR). The SD-ID has an @
	// and the keys built from it a dot, so they are mangled on the way to
	// becoming columns, and have to be found again under their own names.
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := conn.Write([]byte(`<11>1 ` + stamp + ` web-01 nginx 4711 ID47 ` +
		`[exampleSDID@32473 iut="3" eventSource="Application"] upstream timed out`)); nil != err {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		backlog, err := h.store.Backlog(ctx, "nginx", 10)
		if nil != err {
			t.Fatal(err)
		}
		if 1 == len(backlog) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the syslog line never reached the store")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := h.store.Flush(ctx); nil != err {
		t.Fatal(err)
	}

	req := request("")
	req.Stream = "nginx"

	result, err := h.engine.Search(ctx, req)
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(result.Records) {
		t.Fatalf("found %d records, want 1", len(result.Records))
	}

	fields := result.Records[0].Fields
	for key, want := range map[string]string{
		"facility":                      "user",
		"severity":                      "ERROR",
		"hostname":                      "web-01",
		"app":                           "nginx",
		"pid":                           "4711",
		"msgid":                         "ID47",
		"exampleSDID@32473.iut":         "3",
		"exampleSDID@32473.eventSource": "Application",
	} {
		if got, ok := fields[key]; !ok || model.KindString != got.Kind || want != got.Str {
			t.Errorf("fields[%q] = %+v (present %v), want the string %q", key, got, ok, want)
		}
	}

	// It is still the line it was, not a JSON rendering of its fields.
	if "upstream timed out" != result.Records[0].Msg || "string" != result.Records[0].Type {
		t.Errorf("msg %q type %q", result.Records[0].Msg, result.Records[0].Type)
	}
}

// A JSON line written without raw — what --retention-raw leaves behind — has
// nothing to re-promote from either, and keeps its fields, with their kinds,
// the same way.
func TestSearchedRecordsWithoutRawKeepTheirFields(t *testing.T) {
	h := openHarness(t, false)

	h.ingest(t, "api", []string{
		`{"level":"warn","msg":"slow","service":"api","latency_ms":812,"ratio":0.5,"cached":false,"req":{"path":"/a"}}`,
		`{"level":"info","msg":"ok"}`,
	})

	result, err := h.engine.Search(context.Background(), request("msg:slow"))
	if nil != err {
		t.Fatal(err)
	}
	if 1 != len(result.Records) {
		t.Fatalf("found %d records, want 1", len(result.Records))
	}

	fields := result.Records[0].Fields
	for key, want := range map[string]model.Value{
		"service":    model.Str("api"),
		"latency_ms": model.Int(812),
		"ratio":      model.Float(0.5),
		"cached":     model.Bool(false),
		"req":        model.JSON(`{"path":"/a"}`),
	} {
		if got := fields[key]; want != got {
			t.Errorf("fields[%q] = %+v, want %+v", key, got, want)
		}
	}

	// A key this record never had is absent, not a null: the other line's
	// columns are not this one's fields.
	if _, ok := fields["nope"]; ok {
		t.Error("an absent key came back")
	}

	// Reading the fields takes a query slot of its own. With one slot, a
	// search that still held the slot its page was read with would wait for
	// itself for ever.
	single, err := query.Open(h.store.Catalog(), h.store.Backend(), query.Limits{Concurrency: 1})
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { single.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err = single.Search(ctx, request("msg:slow"))
	if nil != err {
		t.Fatalf("one query slot: %v", err)
	}
	if "api" != result.Records[0].Fields["service"].Str {
		t.Errorf("one query slot: fields = %v", result.Records[0].Fields)
	}
}
