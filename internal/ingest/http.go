/*!
 * The HTTP ingest endpoint.
 *
 *   POST /v1/ingest?stream=api
 *
 * UDP is at-most-once and capped at one datagram, which is exactly right for
 * `cmd | rtail` and wrong for anything that would mind losing a line. This is
 * the path that acknowledges: the response is written after the records are in
 * the write-ahead log, so a 200 means durable.
 *
 * Every response carries the same body, whatever its status:
 *
 *   {"accepted": 120, "rejected": 1, "errors": ["..."]}
 *
 * and `accepted` is exact — lines are appended in order and nothing after a
 * failure is attempted, so that many lines from the start of the body are
 * durable. A shipper that gets anything but a 200 resumes after them rather
 * than replaying lines already stored:
 *
 *   200  every line is stored. One longer than maxLine is truncated to it and
 *        stored all the same, and `errors` says how many were.
 *   400  the body could not be read: an unknown Content-Encoding, or gzip or
 *        zstd that stops decoding partway.
 *   413  the body is over MaxBody, or decompresses to more than maxInflation
 *        times that.
 *   503  storage refused a record, and the batch stopped there.
 *
 * `rejected` is 1 when the batch stopped early and 0 when it did not; `errors`
 * says why, and is empty on a clean 200.
 *
 * Content type is deliberately not consulted. Newline-delimited JSON and plain
 * text go through the same code, because the normalizer already decides per
 * line whether it is looking at an object or at something a process printed —
 * and a shipper that mislabels its payload should still work.
 */

package ingest

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
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

	// maxLine is the longest single log line stored. Beyond this the line is
	// truncated rather than the request failed: one pathological line should
	// not cost a shipper its whole batch. The rest of it is read and thrown
	// away, never buffered, so its length costs no memory.
	maxLine = 1 << 20

	// maxInflation bounds what a compressed body may decompress to, as a
	// multiple of MaxBody. MaxBody alone limits only the bytes on the wire,
	// and log text compresses so well that 32MB of gzip can carry sixteen
	// billion one-character lines. Shippers batch by uncompressed size, and
	// their defaults are a few megabytes, so eight times MaxBody — 256MB at
	// the default — is far past any batch a real one sends.
	maxInflation = 8

	// zstdMaxWindow is the most history a zstd frame may make the decoder
	// keep. The library's default trusts the frame header up to 512MB, which
	// it allocates before reading a single block — so ten bytes of body
	// could cost half a gigabyte. 8MB is the window the format recommends
	// every decoder support, the most the zstd CLI uses short of --ultra or
	// --long, and the default of Go's encoder, so nothing compressed with
	// stock settings is refused.
	zstdMaxWindow = 8 << 20
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
	Errors   []string `json:"errors"`
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
		h.fail(w, statusOf(err), err)
		return
	}
	defer body.Close()

	host := remoteHost(r)
	reader := bufio.NewReaderSize(body, 64<<10)

	var (
		out       result
		line      []byte
		cut       bool
		truncated int
	)

	// finish answers with what is stored so far, which is every response.
	finish := func(status int, err error) {
		if 0 != truncated {
			out.Errors = append(out.Errors,
				fmt.Sprintf("%d line(s) exceeded %d bytes and were truncated", truncated, maxLine))
		}

		if nil != err {
			out.Rejected++
			h.stats.Rejected.Add(1)
			out.Errors = append(out.Errors, err.Error())
		}

		writeResult(w, status, out)
	}

	for {
		line, cut, err = readLine(reader, line[:0])

		/*!
		 * A line ended by a failed read is not stored. It is a fragment, and
		 * counting it as accepted would have a resuming shipper skip the rest
		 * of it. The last line of a body needs no newline, so EOF is not a
		 * failure.
		 */
		if nil != err && !errors.Is(err, io.EOF) {
			finish(statusOf(err), fmt.Errorf("reading the body: %w", err))
			return
		}

		if 0 != len(bytes.TrimSpace(line)) {
			if cut {
				truncated++
			}

			h.stats.Bytes.Add(uint64(len(line)))

			rec := normalize.FromLine(stream, host, 0, line)

			if err := h.opts.Store.Append(r.Context(), rec); nil != err {
				h.opts.Log.Error("appending an ingested record", "stream", stream, "err", err)
				finish(http.StatusServiceUnavailable, fmt.Errorf("storing records: %w", err))
				return
			}

			out.Accepted++
			h.stats.Received.Add(1)
		}

		if nil != err {
			break
		}
	}

	finish(http.StatusOK, nil)
}

/*!
 * readLine reads the next line into line, without its newline, keeping at
 * most maxLine bytes of it.
 *
 * The rest of an over-long line is read and thrown away rather than
 * buffered, so however long a sender keeps writing one line the receiver
 * holds at most maxLine of it, and the next line still starts where it
 * should. The bool reports that this happened. The last line of the input
 * comes back with io.EOF, whether or not it ended in a newline.
 */
func readLine(reader *bufio.Reader, line []byte) ([]byte, bool, error) {
	cut := false

	for {
		chunk, err := reader.ReadSlice('\n')
		if nil == err {
			chunk = chunk[:len(chunk)-1]
		}

		if room := maxLine - len(line); len(chunk) > room {
			chunk, cut = chunk[:room], true
		}
		line = append(line, chunk...)

		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, cut, err
		}
	}
}

/*!
 * open unwraps the request body's encoding.
 *
 * A compressed body has two sizes, and each gets a limit. MaxBytesReader
 * bounds the bytes the client actually sends; inflateLimit bounds what they
 * expand to, which is the amount of work the request can demand. Neither is
 * what bounds memory — Lines holds one line at a time, at most maxLine, and
 * OTLP's readAll caps its buffer itself — but without the second, a body
 * well under MaxBody could still ask for billions of records.
 */
func (h *HTTP) open(w http.ResponseWriter, r *http.Request) (io.ReadCloser, error) {
	limited := http.MaxBytesReader(w, r.Body, h.opts.MaxBody)
	inflated := maxInflation * h.opts.MaxBody

	switch encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); encoding {
	case "", "identity":
		return limited, nil

	case "gzip":
		reader, err := gzip.NewReader(limited)
		if nil != err {
			return nil, fmt.Errorf("reading gzip body: %w", err)
		}
		return &inflateLimit{ReadCloser: reader, left: inflated, limit: inflated}, nil

	case "zstd":
		// Decoded synchronously, without the library's worker goroutines, and
		// holding at most zstdMaxWindow — see there for why the defaults will
		// not do. Closed with the body, by the caller's deferred Close.
		reader, err := zstd.NewReader(limited,
			zstd.WithDecoderMaxWindow(zstdMaxWindow),
			zstd.WithDecoderMaxMemory(zstdMaxWindow),
			zstd.WithDecoderConcurrency(1))
		if nil != err {
			return nil, fmt.Errorf("reading zstd body: %w", err)
		}
		return &inflateLimit{ReadCloser: reader.IOReadCloser(), left: inflated, limit: inflated}, nil

	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", encoding)
	}
}

// inflateLimit fails a decompressed stream that runs past its limit, the way
// http.MaxBytesReader fails the compressed one: the bytes up to the limit
// are delivered, then an error rather than a quiet EOF, so a body cut short
// is never mistaken for a complete one.
type inflateLimit struct {
	io.ReadCloser
	left  int64
	limit int64
}

func (l *inflateLimit) Read(p []byte) (int, error) {
	if l.left < 0 {
		return 0, &tooLargeError{what: "decompressed body", limit: l.limit}
	}

	// One byte past the limit is enough to know it was crossed.
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1]
	}

	n, err := l.ReadCloser.Read(p)
	if int64(n) <= l.left {
		l.left -= int64(n)
		return n, err
	}

	n, l.left = int(l.left), -1
	return n, &tooLargeError{what: "decompressed body", limit: l.limit}
}

// tooLargeError is a body past one of the size limits.
type tooLargeError struct {
	what  string
	limit int64
}

func (e *tooLargeError) Error() string { return e.what + " exceeds " + byteCount(e.limit) }

// statusOf is the status for a body that could not be read: 413 when it ran
// past a size limit, which the client fixes by sending less, and 400 for
// anything else wrong with it.
func statusOf(err error) int {
	var (
		overBody     *http.MaxBytesError
		overInflated *tooLargeError
	)

	if errors.As(err, &overBody) || errors.As(err, &overInflated) {
		return http.StatusRequestEntityTooLarge
	}

	return http.StatusBadRequest
}

// fail answers for a request refused before any of it was stored.
func (h *HTTP) fail(w http.ResponseWriter, status int, err error) {
	h.stats.Rejected.Add(1)
	writeResult(w, status, result{Rejected: 1, Errors: []string{err.Error()}})
}

func writeResult(w http.ResponseWriter, status int, payload result) {
	// Present even when empty, so every response has the same three keys.
	if nil == payload.Errors {
		payload.Errors = []string{}
	}

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
	// SplitHostPort rather than a cut at the first colon, which turns an IPv6
	// peer — "[::1]:54321" — into "[".
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if nil != err {
		return r.RemoteAddr
	}
	return host
}
