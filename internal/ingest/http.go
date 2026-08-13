/*!
 * The HTTP ingest endpoint.
 *
 *   POST /v1/ingest?stream=api
 *
 * UDP is at-most-once and capped at one datagram, which is exactly right for
 * `cmd | rtail` and wrong for anything that would mind losing a line. This is
 * the path that acknowledges: the response is written after the records are in
 * the write-ahead log, so a 200 means durable, and a client that gets anything
 * else can retry.
 *
 * Content type is deliberately not consulted. Newline-delimited JSON and plain
 * text go through the same code, because the normalizer already decides per
 * line whether it is looking at an object or at something a process printed —
 * and a shipper that mislabels its payload should still work.
 */

package ingest

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/klauspost/compress/zstd"

	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/normalize"
)

// Defaults for the HTTP receivers.
const (
	// DefaultMaxBody bounds one request. Generous enough for a batching
	// shipper, small enough that a stray upload cannot exhaust memory.
	DefaultMaxBody = 32 << 20

	// DefaultStream names records that arrive without one.
	DefaultStream = "http"

	// maxLine is the longest single log line accepted. Beyond this the line is
	// truncated rather than the request failed: one pathological line should
	// not cost a shipper its whole batch.
	maxLine = 1 << 20
)

// HTTPStats counts what the HTTP receivers have seen.
type HTTPStats struct {
	Received atomic.Uint64
	Rejected atomic.Uint64
	Bytes    atomic.Uint64
	Requests atomic.Uint64
}

// HTTPOptions configures the HTTP receivers.
type HTTPOptions struct {
	Store   logstore.Store
	Log     *slog.Logger
	MaxBody int64
	// Default is the stream for records that name none.
	Default string
}

func (o *HTTPOptions) withDefaults() {
	if 0 == o.MaxBody {
		o.MaxBody = DefaultMaxBody
	}
	if "" == o.Default {
		o.Default = DefaultStream
	}
	if nil == o.Log {
		o.Log = slog.Default()
	}
}

// HTTP receives batches over HTTP.
type HTTP struct {
	opts  HTTPOptions
	stats HTTPStats
}

// NewHTTP builds the HTTP receivers.
func NewHTTP(opts HTTPOptions) *HTTP {
	opts.withDefaults()
	return &HTTP{opts: opts}
}

// Stats exposes the counters.
func (h *HTTP) Stats() *HTTPStats { return &h.stats }

// result is what every ingest endpoint answers with.
type result struct {
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Errors   []string `json:"errors,omitempty"`
}

/*!
 * Lines handles POST /v1/ingest.
 */
func (h *HTTP) Lines(w http.ResponseWriter, r *http.Request) {
	h.stats.Requests.Add(1)

	stream := r.URL.Query().Get("stream")
	if "" == stream {
		stream = h.opts.Default
	}

	body, err := h.open(w, r)
	if nil != err {
		h.fail(w, http.StatusBadRequest, err)
		return
	}
	defer body.Close()

	host := remoteHost(r)

	var out result

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLine)

	for scanner.Scan() {
		line := scanner.Bytes()
		if 0 == len(strings.TrimSpace(string(line))) {
			continue
		}

		h.stats.Bytes.Add(uint64(len(line)))

		rec := normalize.FromLine(stream, host, 0, line)

		if err := h.opts.Store.Append(r.Context(), rec); nil != err {
			h.opts.Log.Error("appending an ingested record", "stream", stream, "err", err)
			h.fail(w, http.StatusServiceUnavailable, fmt.Errorf("storing records: %w", err))
			return
		}

		out.Accepted++
		h.stats.Received.Add(1)
	}

	/*!
	 * A line past the buffer limit ends the scan. Everything already appended
	 * stays — they are durable — and the response says how many made it, so a
	 * shipper can resume rather than replay the whole batch.
	 */
	if err := scanner.Err(); nil != err {
		out.Rejected++
		h.stats.Rejected.Add(1)

		if errors.Is(err, bufio.ErrTooLong) {
			out.Errors = append(out.Errors,
				fmt.Sprintf("a line exceeded %d bytes; the rest of the batch was not read", maxLine))
		} else {
			out.Errors = append(out.Errors, err.Error())
		}

		writeResult(w, http.StatusPartialContent, out)
		return
	}

	writeResult(w, http.StatusOK, out)
}

/*!
 * open unwraps the request body's encoding.
 *
 * The size limit is applied to the *compressed* stream, which is the only
 * thing that bounds memory: a limit on the decompressed side is applied after
 * the bytes already exist.
 */
func (h *HTTP) open(w http.ResponseWriter, r *http.Request) (io.ReadCloser, error) {
	limited := http.MaxBytesReader(w, r.Body, h.opts.MaxBody)

	switch encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); encoding {
	case "", "identity":
		return limited, nil

	case "gzip":
		reader, err := gzip.NewReader(limited)
		if nil != err {
			return nil, fmt.Errorf("reading gzip body: %w", err)
		}
		return reader, nil

	case "zstd":
		reader, err := zstd.NewReader(limited)
		if nil != err {
			return nil, fmt.Errorf("reading zstd body: %w", err)
		}
		return reader.IOReadCloser(), nil

	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", encoding)
	}
}

func (h *HTTP) fail(w http.ResponseWriter, status int, err error) {
	h.stats.Rejected.Add(1)
	writeResult(w, status, result{Errors: []string{err.Error()}})
}

func writeResult(w http.ResponseWriter, status int, payload result) {
	body, err := json.Marshal(payload)
	if nil != err {
		http.Error(w, `{"errors":["encoding response"]}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(body)
}

/*!
 * remoteHost is the peer's address, ignoring forwarding headers.
 *
 * X-Forwarded-For is attacker-controlled and this endpoint has no
 * authentication, so trusting it would let anyone write any host into the
 * stored record. An operator who wants the real client address behind a proxy
 * should have the proxy set it — which is a decision about their trust
 * boundary, not ours to assume.
 */
func remoteHost(r *http.Request) string {
	host, _, found := strings.Cut(r.RemoteAddr, ":")
	if !found {
		return r.RemoteAddr
	}
	return host
}
