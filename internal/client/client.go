/*!
 * Package client is the rtail pipe: stdin in, UDP out, stdout through.
 *
 * This was cli/rtail-client.js, and it is the last thing in rTail that needed
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
	"fmt"
	"io"
	"net"
	"strings"
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

	conn, err := env.Dial(opts.Host, opts.Port)
	if nil != err {
		fmt.Fprintf(env.Stderr, "rtail: %v\n", err)
		return 1
	}
	defer conn.Close()

	// Colours survive only when the terminal can show them and --tty is on,
	// which is the rule the JavaScript client used: `!process.stdout.isTTY ||
	// !argv.tty`.
	strip := !env.Colour || !opts.TTY

	scanner := bufio.NewScanner(env.Stdin)

	/*!
	 * A line can be longer than bufio's 64KB default.
	 *
	 * A JSON log record carrying a stack trace or a request body runs past it
	 * easily, and the default behaviour is to stop the scan with an error —
	 * so one long line would silently truncate the rest of the stream. The
	 * datagram itself is capped on the server side at 64KB; this is larger so
	 * the line survives to be rejected there rather than vanishing here.
	 */
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)

	out := bufio.NewWriter(env.Stdout)
	defer out.Flush()

	// Lines too long to fit one datagram, counted rather than reported one by
	// one: the condition is a property of the producer, so a pipeline that
	// hits it hits it on thousands of lines and a warning each would bury the
	// output it is warning about.
	oversize := 0

	for scanner.Scan() {
		raw := strings.TrimSuffix(scanner.Text(), "\r")

		if !opts.Mute {
			if strip {
				out.WriteString(stripANSI(raw))
			} else {
				out.WriteString(raw)
			}
			out.WriteByte('\n')

			// Flushed per line: a pipe in the middle of someone's shell
			// should behave like `tee`, not like a buffer that appears all at
			// once when the producer exits.
			out.Flush()
		}

		payload, err := json.Marshal(datagram{
			ID:        opts.ID,
			Timestamp: env.Now().UnixMilli(),
			Content:   encodeContent(raw),
		})
		if nil != err {
			continue
		}

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

		// A failed send is not worth stopping the pipe for. rtail is a
		// side-channel: if the collector is down, the command whose output
		// this is must still run, and its output must still reach stdout.
		conn.Write(payload)
	}

	if 0 < oversize {
		fmt.Fprintf(env.Stderr,
			"rtail: %d line(s) over %d bytes were not sent — UDP cannot carry them\n",
			oversize, maxDatagram)
	}

	if err := scanner.Err(); nil != err {
		fmt.Fprintf(env.Stderr, "rtail: %v\n", err)
		return 1
	}

	return 0
}

const (
	// maxLine is the longest line the scanner will assemble, past which the
	// stream would otherwise stop dead rather than merely truncating.
	maxLine = 1024 * 1024

	// maxDatagram is the largest payload IPv4 UDP can carry, and the size of
	// the buffer the receiver reads into.
	maxDatagram = 65507
)

// datagram is the frozen v1 wire format — internal/ingest/udp.go reads it.
type datagram struct {
	ID        string          `json:"id"`
	Timestamp int64           `json:"timestamp"`
	Content   json.RawMessage `json:"content"`
}

// DialUDP is the real socket.
func DialUDP(host string, port int) (Conn, error) {
	addr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, fmt.Sprint(port)))
	if nil != err {
		return nil, err
	}

	return net.DialUDP("udp4", nil, addr)
}
