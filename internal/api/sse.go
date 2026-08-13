/*!
 * The live tail endpoint.
 *
 *   GET /v1/tail?stream=api.example.com
 *
 * Emits three event types: `streams` whenever the stream list changes,
 * `backlog` once at the start, and `line` for each record after that.
 * Requesting no stream subscribes to `streams` only, which is how the webapp
 * represents a paused tab — v1 achieved the same thing by emitting
 * `select stream` with null.
 */

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/model"
)

// subscriberBuffer is generous because the cost of a dropped line is much
// higher than the cost of a few hundred pointers held briefly.
const subscriberBuffer = 1024

func (s *Server) tail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stream := r.URL.Query().Get("stream")

	/*!
	 * Subscribe before reading the backlog, never after.
	 *
	 * The other order has a hole: a record appended between the backlog read
	 * and the subscribe is in neither, and is silently lost. Subscribing first
	 * closes it, at the price of records that land in the gap appearing twice
	 * — which Seq lets us filter exactly, because it is monotonic per store.
	 */
	sub := s.opts.Store.Subscribe(stream, subscriberBuffer)
	defer sub.Close()

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	// nginx buffers proxied responses by default, which turns a live tail into
	// a tail that arrives in 4KB lumps.
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher := http.NewResponseController(w)

	streams, err := s.opts.Store.Streams(ctx)
	if nil != err {
		s.opts.Log.Error("reading streams for tail", "err", err)
		return
	}
	if nil == streams {
		streams = []string{}
	}

	if err := sendEvent(w, "streams", streams); nil != err {
		return
	}

	var floor uint64

	if "" != stream {
		backlog, err := s.opts.Store.Backlog(ctx, stream, 0)
		if nil != err {
			s.opts.Log.Error("reading backlog", "stream", stream, "err", err)
			return
		}

		if nil == backlog {
			backlog = []*model.Record{}
		}
		if n := len(backlog); n > 0 {
			floor = backlog[n-1].Seq
		}

		if err := sendEvent(w, "backlog", backlog); nil != err {
			return
		}
	}

	if err := flusher.Flush(); nil != err {
		return
	}

	ticker := time.NewTicker(s.opts.Heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			if _, err := io.WriteString(w, ": ping\n\n"); nil != err {
				return
			}

		case event, open := <-sub.C:
			if !open {
				return
			}

			switch event.Kind {
			case logstore.EventStreams:
				if err := sendEvent(w, "streams", event.Streams); nil != err {
					return
				}

			case logstore.EventLine:
				// Already delivered in the backlog.
				if event.Record.Seq <= floor {
					continue
				}
				if err := sendEvent(w, "line", event.Record); nil != err {
					return
				}
			}
		}

		if err := flusher.Flush(); nil != err {
			return
		}
	}
}

/*!
 * sendEvent writes one SSE frame.
 *
 * The payload goes out on a single data line, which is safe because JSON
 * encoding escapes every newline — a multi-line log message cannot break out
 * of the frame.
 */
func sendEvent(w io.Writer, name string, payload any) error {
	body, err := json.Marshal(payload)
	if nil != err {
		return err
	}

	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, body)
	return err
}
