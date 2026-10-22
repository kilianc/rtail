/*!
 * Package client is the rtail pipe: stdin in, UDP out, stdout through.
 *
 * This was cli/rtail-client.js, and it is the last thing in rtail that needed
 * a Node runtime. The server has been a Go binary since v2; leaving the pipe
 * in JavaScript meant anyone who wanted to *send* logs installed a second
 * language toolchain to run two hundred lines that read a file descriptor and
 * call sendto(2).
 *
 * The wire format is frozen and shared with the v1 clients still in the wild
 * — see internal/ingest/udp.go. One datagram per line:
 *
 *     {"id": "...", "timestamp": <epoch ms>, "content": <any JSON>}
 *
 * Everything here is structured for the test: Run takes its streams as
 * arguments rather than reaching for os.Stdin, so the whole pipe can be
 * exercised in-process against a loopback socket.
 */
package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Conn is the sending half of the socket, so a test can supply its own.
type Conn interface {
	io.Closer
	Write([]byte) (int, error)
}

// Env is everything Run touches that is not an argument.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Colour says whether stdout can render ANSI. False strips it, which is
	// what happens when the output is a pipe or a file rather than a terminal.
	Colour bool

	// Dial opens the datagram socket. Injected so a test can hand back a
	// socket it is already listening on.
	Dial func(host string, port int) (Conn, error)

	// Now is the clock the timestamp comes from.
	Now func() time.Time
}

/*!
 * Run is the whole program, minus the process.
 *
 * It returns an exit status rather than calling os.Exit so that a test can
 * assert on it, and so the deferred Close below actually runs.
 */
func Run(args []string, version string, env Env) int {
	opts, err := parseArgs(args)
	if nil != err {
		fmt.Fprintf(env.Stderr, "rtail: %v\n\n", err)
		fmt.Fprint(env.Stderr, usage)
		return 1
	}

	if opts.Help {
		fmt.Fprint(env.Stdout, usage)
		return 0
	}

	if opts.Version {
		fmt.Fprintln(env.Stdout, version)
		return 0
	}

	link := openLink(env, opts)
	defer link.close()

	// Colours survive only when the terminal can show them and --tty is on,
	// which is the rule the JavaScript client used: `!process.stdout.isTTY ||
	// !argv.tty`.
	strip := !env.Colour || !opts.TTY

	out := bufio.NewWriter(env.Stdout)
	defer out.Flush()

	// echo writes to stdout unless it is muted, without the escapes it cannot
	// show.
	echo := func(text string) {
		if opts.Mute {
			return
		}
		if strip {
			text = stripANSI(text)
		}
		out.WriteString(text)
	}

	// endLine finishes a line on stdout. Flushed per line: a pipe in the
	// middle of someone's shell should behave like `tee`, not like a buffer
	// that appears all at once when the producer exits.
	endLine := func() {
		if !opts.Mute {
			out.WriteByte('\n')
			out.Flush()
		}
	}

	/*!
	 * Lines are read with a bufio.Reader, not a Scanner, so that no line is
	 * too long to pass through.
	 *
	 * A Scanner stops at the first token past its buffer: the loop ends,
	 * nothing more reaches stdout or the socket, and in `cmd | rtail` the
	 * producer dies of SIGPIPE on its next write — a stack trace is enough to
	 * do it. Here a line is assembled up to maxLine, which is far past what a
	 * datagram can carry, and past that point it is streamed to stdout as it
	 * arrives instead and counted as too large to send. A producer that never
	 * prints a newline therefore cannot grow rtail without bound, and cannot
	 * stop the pipe either.
	 */
	reader := bufio.NewReaderSize(env.Stdin, 64*1024)

	var (
		line []byte
		// streaming is set once the line being read has outgrown maxLine:
		// what was read of it is on stdout already, and the rest follows as
		// it arrives. Stripping then works a chunk at a time, so an escape
		// sequence that straddles two chunks is passed through rather than
		// removed — the stripper's rule for anything it cannot see whole.
		streaming bool
		readErr   error
	)

	// Lines too long to fit one datagram, counted rather than reported one by
	// one: the condition is a property of the producer, so a pipeline that
	// hits it hits it on thousands of lines and a warning each would bury the
	// output it is warning about. Lines read while there is no socket yet are
	// counted the same way.
	oversize := 0
	unsent := 0

	for {
		chunk, more, err := reader.ReadLine()
		if nil != err {
			readErr = err
			break
		}

		if streaming {
			echo(string(chunk))
		} else {
			line = append(line, chunk...)

			if maxLine < len(line) {
				echo(string(line))
				line, streaming = line[:0], true
			}
		}

		if more {
			continue
		}

		if streaming {
			endLine()
			oversize++
			streaming = false
			continue
		}

		raw := strings.TrimSuffix(string(line), "\r")
		line = line[:0]

		echo(raw)
		endLine()

		payload := encodeDatagram(opts.ID, env.Now().UnixMilli(), encodeContent(raw))

		/*!
		 * A datagram larger than IPv4 UDP can carry is dropped here.
		 *
		 * Sending it fails with EMSGSIZE, and the v1 client simply ignored
		 * that: the line went to stdout, never arrived, and nothing anywhere
		 * said so. The read side has the same ceiling — internal/ingest/udp.go
		 * reads into a 65507-byte buffer — so this is a limit of the
		 * transport, not a choice, and the honest thing is to say how many
		 * lines it ate.
		 */
		if maxDatagram < len(payload) {
			oversize++
			continue
		}

		if !link.write(payload) {
			unsent++
		}
	}

	if 0 < oversize {
		fmt.Fprintf(env.Stderr,
			"rtail: %d line(s) over %d bytes were not sent — UDP cannot carry them\n",
			oversize, maxDatagram)
	}

	if 0 < unsent {
		fmt.Fprintf(env.Stderr,
			"rtail: %d line(s) were not sent — %s could not be dialled\n",
			unsent, net.JoinHostPort(opts.Host, fmt.Sprint(opts.Port)))
	}

	if !errors.Is(readErr, io.EOF) {
		fmt.Fprintf(env.Stderr, "rtail: %v\n", readErr)
		return 1
	}

	return 0
}

/*!
 * link is the socket, or the attempt to open one.
 *
 * Dialling resolves the host, and that is the one step of sending that can
 * fail before a line has been read. Failing there used to end the process,
 * which is the opposite of a side-channel: in `server | rtail --host logs`
 * the pipe closes, and the server dies of SIGPIPE the next time it writes,
 * over a DNS record. So a failed dial is a warning. stdin keeps passing
 * through, lines are not sent until a socket exists, and the dial is retried
 * every redialEvery — in the background, because a resolver that times out
 * rather than refusing would otherwise stall the pipe for seconds at a time.
 */
type link struct {
	mu   sync.Mutex
	conn Conn
	done chan struct{}
}

// redialEvery is how often a socket that could not be opened is tried again.
// A variable so that a test need not wait that long.
var redialEvery = 10 * time.Second

func openLink(env Env, opts options) *link {
	l := &link{done: make(chan struct{})}

	conn, err := env.Dial(opts.Host, opts.Port)
	if nil == err {
		l.conn = conn
		return l
	}

	// Read once, here: the goroutine below may outlive Run.
	every := redialEvery

	fmt.Fprintf(env.Stderr, "rtail: %v — passing stdin through, and retrying every %s\n", err, every)

	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()

		for {
			select {
			case <-l.done:
				return
			case <-ticker.C:
			}

			conn, err := env.Dial(opts.Host, opts.Port)
			if nil != err {
				continue
			}

			l.mu.Lock()
			defer l.mu.Unlock()

			// The pipe may have ended while this dial was in flight.
			select {
			case <-l.done:
				conn.Close()
			default:
				l.conn = conn
			}

			return
		}
	}()

	return l
}

// write sends one datagram, reporting false when there is no socket yet.
func (l *link) write(payload []byte) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if nil == l.conn {
		return false
	}

	// A failed send is not worth stopping the pipe for. rtail is a
	// side-channel: if the collector is down, the command whose output this
	// is must still run, and its output must still reach stdout.
	l.conn.Write(payload)

	return true
}

func (l *link) close() {
	l.mu.Lock()
	defer l.mu.Unlock()

	close(l.done)

	if nil != l.conn {
		l.conn.Close()
	}
}

const (
	// maxLine is the most of one line held in memory. Past it the line is
	// streamed to stdout rather than assembled; it could not have been sent
	// anyway, being sixteen times what a datagram carries.
	maxLine = 1024 * 1024

	// maxDatagram is the largest payload IPv4 UDP can carry, and the size of
	// the buffer the receiver reads into.
	maxDatagram = 65507
)

// datagram is the frozen v1 wire format — internal/ingest/udp.go reads it.
// encodeDatagram writes it.
type datagram struct {
	ID        string          `json:"id"`
	Timestamp int64           `json:"timestamp"`
	Content   json.RawMessage `json:"content"`
}

/*!
 * encodeDatagram writes one datagram, by hand.
 *
 * json.Marshal does not pass a RawMessage through: it compacts it, and
 * escapes <, > and & — and U+2028 and U+2029 — inside its strings. The
 * server keeps an object's bytes as the record's raw text, so a line that
 * printed {"url":"/a?x=1&y=2"} was stored with \u0026 in place of the &, and
 * a search for x=1&y=2 found it over HTTP and never through the pipe. content
 * is valid JSON already, encodeContent sees to that, and the id is the one
 * value here that still needs encoding.
 */
func encodeDatagram(id string, timestamp int64, content json.RawMessage) []byte {
	// Marshalling a string cannot fail.
	encodedID, _ := json.Marshal(id)

	payload := make([]byte, 0, 64+len(encodedID)+len(content))
	payload = append(payload, `{"id":`...)
	payload = append(payload, encodedID...)
	payload = append(payload, `,"timestamp":`...)
	payload = strconv.AppendInt(payload, timestamp, 10)
	payload = append(payload, `,"content":`...)
	payload = append(payload, content...)

	return append(payload, '}')
}

// DialUDP is the real socket.
func DialUDP(host string, port int) (Conn, error) {
	addr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, fmt.Sprint(port)))
	if nil != err {
		return nil, err
	}

	return net.DialUDP("udp4", nil, addr)
}
