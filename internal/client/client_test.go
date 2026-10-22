/*!
 * The pipe's behaviour, pinned.
 *
 * These are the twelve cases test/rtail-client.test.js carried, ported one
 * for one, plus the ones the port itself made worth asserting — verbatim JSON
 * and a line past bufio's default buffer. The JavaScript suite spawned a
 * process per case and waited on a socket; this runs Run in-process against a
 * loopback listener, which is why it is a hundred times faster and can assert
 * on the exit status.
 */
package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// listener collects datagrams on an ephemeral port.
type listener struct {
	conn *net.UDPConn
	mu   sync.Mutex
	raw  [][]byte
}

func listen(t *testing.T) *listener {
	t.Helper()

	addr, err := net.ResolveUDPAddr("udp4", "127.0.0.1:0")
	if nil != err {
		t.Fatalf("resolve: %v", err)
	}

	conn, err := net.ListenUDP("udp4", addr)
	if nil != err {
		t.Fatalf("listen: %v", err)
	}

	l := &listener{conn: conn}

	go func() {
		buffer := make([]byte, 2*1024*1024)
		for {
			n, err := conn.Read(buffer)
			if nil != err {
				return
			}
			l.mu.Lock()
			l.raw = append(l.raw, append([]byte(nil), buffer[:n]...))
			l.mu.Unlock()
		}
	}()

	t.Cleanup(func() { conn.Close() })

	return l
}

func (l *listener) port() int { return l.conn.LocalAddr().(*net.UDPAddr).Port }

// messages waits for at least want datagrams, then decodes them.
func (l *listener) messages(t *testing.T, want int) []datagram {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		l.mu.Lock()
		got := len(l.raw)
		l.mu.Unlock()

		if got >= want {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]datagram, 0, len(l.raw))
	for _, raw := range l.raw {
		var d datagram
		if err := json.Unmarshal(raw, &d); nil != err {
			t.Fatalf("undecodable datagram %q: %v", raw, err)
		}
		out = append(out, d)
	}

	return out
}

type result struct {
	code   int
	stdout string
	stderr string
}

// run pipes input through the client, pointed at l.
func run(t *testing.T, l *listener, args []string, input string, colour bool) result {
	t.Helper()

	var stdout, stderr bytes.Buffer

	full := append([]string{"--port", itoa(l.port())}, args...)

	code := Run(full, "9.9.9-test", Env{
		Stdin:  strings.NewReader(input),
		Stdout: &stdout,
		Stderr: &stderr,
		Colour: colour,
		Dial:   DialUDP,
		Now:    time.Now,
	})

	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func itoa(n int) string { return strconv.Itoa(n) }

// contents decodes each datagram's content into a Go value.
func contents(t *testing.T, messages []datagram) []any {
	t.Helper()

	out := make([]any, 0, len(messages))
	for _, m := range messages {
		var v any
		if err := json.Unmarshal(m.Content, &v); nil != err {
			t.Fatalf("undecodable content %q: %v", m.Content, err)
		}
		out = append(out, v)
	}

	return out
}

func TestSplitsStdinByNewline(t *testing.T) {
	l := listen(t)
	res := run(t, l, nil, "alpha\nbeta\ngamma\n", false)

	if 0 != res.code {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}

	messages := l.messages(t, 3)
	if 3 != len(messages) {
		t.Fatalf("got %d datagrams, want 3", len(messages))
	}

	want := []any{"alpha", "beta", "gamma"}
	for i, got := range contents(t, messages) {
		if want[i] != got {
			t.Errorf("content %d = %#v, want %#v", i, got, want[i])
		}
	}

	if "" == messages[0].ID {
		t.Error("no stream id")
	}

	if 0 == messages[0].Timestamp {
		t.Error("no timestamp")
	}
}

/*!
 * Every line is offered to a JSON parser first, so a line that happens to be
 * a bare number arrives as a number rather than as text. Long-standing
 * behaviour, pinned here because it is surprising.
 */
func TestParsesABareNumericLineAsANumber(t *testing.T) {
	l := listen(t)
	run(t, l, nil, "0\n1\n2\n", false)

	for i, got := range contents(t, l.messages(t, 3)) {
		n, ok := got.(float64)
		if !ok || float64(i) != n {
			t.Errorf("content %d = %#v, want the number %d", i, got, i)
		}
	}
}

func TestUsesACustomStreamName(t *testing.T) {
	l := listen(t)
	run(t, l, []string{"--name", "test"}, "a\nb\nc\n", false)

	for i, m := range l.messages(t, 3) {
		if "test" != m.ID {
			t.Errorf("id %d = %q, want %q", i, m.ID, "test")
		}
	}
}

func TestEchoesStdinToStdoutByDefault(t *testing.T) {
	l := listen(t)
	res := run(t, l, nil, "hello\n", false)

	if "hello\n" != res.stdout {
		t.Errorf("stdout = %q, want %q", res.stdout, "hello\n")
	}
}

func TestRespectsMute(t *testing.T) {
	l := listen(t)
	res := run(t, l, []string{"--mute"}, "0\n1\n2\n", false)

	if "" != res.stdout {
		t.Errorf("stdout = %q, want empty", res.stdout)
	}

	if 3 != len(l.messages(t, 3)) {
		t.Error("muting stdout must not mute the socket")
	}
}

func TestStripsAnsiColoursWhenStdoutIsNotATty(t *testing.T) {
	l := listen(t)
	res := run(t, l, nil, "hello \x1b[32mworld\x1b[0m\n", false)

	if "hello world\n" != res.stdout {
		t.Errorf("stdout = %q, want %q", res.stdout, "hello world\n")
	}
}

func TestKeepsAnsiColoursOnATty(t *testing.T) {
	l := listen(t)
	res := run(t, l, nil, "hello \x1b[32mworld\x1b[0m\n", true)

	if "hello \x1b[32mworld\x1b[0m\n" != res.stdout {
		t.Errorf("stdout = %q, want the escapes kept", res.stdout)
	}
}

func TestNoTtyStripsColoursEvenOnATty(t *testing.T) {
	l := listen(t)
	res := run(t, l, []string{"--no-tty"}, "hello \x1b[32mworld\x1b[0m\n", true)

	if "hello world\n" != res.stdout {
		t.Errorf("stdout = %q, want %q", res.stdout, "hello world\n")
	}
}

func TestParsesJSONLines(t *testing.T) {
	l := listen(t)
	run(t, l, nil, `{ "foo": "bar" }`+"\n", false)

	got, ok := contents(t, l.messages(t, 1))[0].(map[string]any)
	if !ok || "bar" != got["foo"] {
		t.Errorf("content = %#v, want {foo: bar}", got)
	}
}

func TestParsesJSON5Lines(t *testing.T) {
	l := listen(t)
	run(t, l, nil, `{ foo: "bar" }`+"\n", false)

	got, ok := contents(t, l.messages(t, 1))[0].(map[string]any)
	if !ok || "bar" != got["foo"] {
		t.Errorf("content = %#v, want {foo: bar}", got)
	}
}

/*!
 * Strict JSON goes out byte for byte.
 *
 * The JavaScript client parsed and re-serialized every record, which sorted
 * its keys and renormalized its numbers. An integer past 2^53 — a trace id, a
 * ledger sequence — came out a different number than went in.
 */
func TestSendsStrictJSONVerbatim(t *testing.T) {
	l := listen(t)
	line := `{"zebra":1,"alpha":2,"big":9007199254740993,"exact":1.0}`
	run(t, l, nil, line+"\n", false)

	got := string(l.messages(t, 1)[0].Content)
	if line != got {
		t.Errorf("content = %s\n           want %s", got, line)
	}
}

func TestEmitsALineForABlankInputLine(t *testing.T) {
	l := listen(t)
	run(t, l, nil, "a\n\nb\n", false)

	want := []any{"a", "", "b"}
	for i, got := range contents(t, l.messages(t, 3)) {
		if want[i] != got {
			t.Errorf("content %d = %#v, want %#v", i, got, want[i])
		}
	}
}

/*!
 * A date at the head of a line is text.
 *
 * It used to be extracted by chrono-node, used as the record's timestamp and
 * cut out of the message — a guess made on every line, with no way to be
 * right and no sign when it was wrong.
 */
func TestSendsALineBeginningWithADateUnchanged(t *testing.T) {
	l := listen(t)
	line := "2015-07-08T10:00:00Z something happened"
	before := time.Now().UnixMilli()

	run(t, l, nil, line+"\n", false)

	messages := l.messages(t, 1)

	if got := contents(t, messages)[0]; line != got {
		t.Errorf("content = %#v, want %q", got, line)
	}

	if messages[0].Timestamp < before || messages[0].Timestamp > time.Now().UnixMilli() {
		t.Errorf("timestamp %d is not arrival time", messages[0].Timestamp)
	}
}

/*!
 * A line longer than bufio's 64KB default must not stop the scan.
 *
 * Left at the default buffer, one long record does not merely truncate
 * itself: the scan ends, so every line after it is dropped and the pipe goes
 * quiet for the rest of the process's life. This is the regression that would
 * be invisible until a stack trace went through.
 */
func TestALongLineDoesNotStopTheScan(t *testing.T) {
	l := listen(t)
	long := strings.Repeat("x", 200*1024)

	res := run(t, l, nil, long+"\nafter\n", false)

	if !strings.HasPrefix(res.stdout, long) {
		t.Error("the long line did not reach stdout intact")
	}

	got := contents(t, l.messages(t, 1))
	if 1 != len(got) || "after" != got[0] {
		t.Fatalf("datagrams = %#v, want the line after the long one", got)
	}
}

/*!
 * A line too big for a datagram is counted out loud.
 *
 * IPv4 UDP tops out at 65507 bytes and the receiver reads into a buffer that
 * size, so an oversize line cannot be delivered however it is framed. The v1
 * client ignored the EMSGSIZE: the line appeared on stdout, never arrived,
 * and nothing anywhere said so.
 */
func TestReportsLinesTooLargeToSend(t *testing.T) {
	l := listen(t)
	long := strings.Repeat("x", 200*1024)

	res := run(t, l, nil, long+"\n", false)

	if !strings.Contains(res.stderr, "were not sent") {
		t.Errorf("stderr = %q, want it to say the line was dropped", res.stderr)
	}

	if 0 != res.code {
		t.Errorf("exit %d — an undeliverable line is not a failure of the pipe", res.code)
	}
}

/*!
 * A line past the read limit passes through as well.
 *
 * Past maxLine a line is no longer assembled in memory: it streams to stdout
 * as it is read. What must not happen is what a Scanner does there — stop
 * reading, so the producer upstream of `cmd | rtail` dies of SIGPIPE.
 */
func TestALineOverTheReadLimitPassesThrough(t *testing.T) {
	l := listen(t)
	long := strings.Repeat("y", 3*maxLine+17)

	res := run(t, l, nil, long+"\nafter\n", false)

	if 0 != res.code {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}

	if long+"\nafter\n" != res.stdout {
		t.Errorf("stdout is %d bytes, want the long line and the next one intact", len(res.stdout))
	}

	if !strings.Contains(res.stderr, "1 line(s) over") {
		t.Errorf("stderr = %q, want it to count the long line as not sent", res.stderr)
	}

	got := contents(t, l.messages(t, 1))
	if 1 != len(got) || "after" != got[0] {
		t.Fatalf("datagrams = %#v, want the line after the long one", got)
	}
}

/*!
 * A host that does not resolve is not a reason to stop.
 *
 * In `server | rtail --host logs` the server started rtail, and an rtail that
 * exits closes the server's stdout under it. So the failure is a warning, and
 * the pipe keeps working as a pipe.
 */
func TestKeepsPassingStdinThroughWhenTheHostCannotBeDialled(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Run([]string{"--host", "logs.invalid"}, "9.9.9-test", Env{
		Stdin:  strings.NewReader("one\ntwo\n"),
		Stdout: &stdout,
		Stderr: &stderr,
		Dial: func(string, int) (Conn, error) {
			return nil, errors.New("lookup logs.invalid: no such host")
		},
		Now: time.Now,
	})

	if 0 != code {
		t.Errorf("exit %d, want 0", code)
	}
	if "one\ntwo\n" != stdout.String() {
		t.Errorf("stdout = %q, want stdin passed through", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no such host") || !strings.Contains(stderr.String(), "2 line(s) were not sent") {
		t.Errorf("stderr = %q, want the failure and the count of unsent lines", stderr.String())
	}
}

// Once the host resolves, lines go out again, without a restart.
func TestStartsSendingOnceTheHostCanBeDialled(t *testing.T) {
	l := listen(t)

	defer func(every time.Duration) { redialEvery = every }(redialEvery)
	redialEvery = 10 * time.Millisecond

	var attempts atomic.Int32
	dial := func(host string, port int) (Conn, error) {
		if 1 == attempts.Add(1) {
			return nil, errors.New("lookup logs: no such host")
		}
		return DialUDP("127.0.0.1", l.port())
	}

	stdin, producer := io.Pipe()
	var stdout, stderr bytes.Buffer
	done := make(chan int)

	go func() {
		done <- Run(nil, "9.9.9-test", Env{
			Stdin: stdin, Stdout: &stdout, Stderr: &stderr, Dial: dial, Now: time.Now,
		})
	}()

	// One line while the host is down, then lines until one arrives.
	io.WriteString(producer, "early\n")

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		io.WriteString(producer, "late\n")

		l.mu.Lock()
		arrived := 0 != len(l.raw)
		l.mu.Unlock()

		if arrived {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	producer.Close()

	if code := <-done; 0 != code {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}

	got := contents(t, l.messages(t, 1))
	if 0 == len(got) {
		t.Fatal("nothing was sent once the host could be dialled")
	}
	for _, content := range got {
		if "late" != content {
			t.Errorf("sent %#v, want only lines read after the dial succeeded", content)
		}
	}
	if !strings.HasPrefix(stdout.String(), "early\nlate\n") {
		t.Errorf("stdout = %q, want every line passed through", stdout.String())
	}
}

func TestSupportsACustomHostAndPort(t *testing.T) {
	l := listen(t)
	res := run(t, l, []string{"--host", "127.0.0.1"}, `{ foo: "bar" }`+"\n", false)

	if 0 != res.code {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}

	got, ok := contents(t, l.messages(t, 1))[0].(map[string]any)
	if !ok || "bar" != got["foo"] {
		t.Errorf("content = %#v", got)
	}
}

func TestRejectsAnUnknownFlag(t *testing.T) {
	l := listen(t)
	res := run(t, l, []string{"--nope"}, "", false)

	if 1 != res.code {
		t.Errorf("exit %d, want 1", res.code)
	}

	if !strings.Contains(res.stderr, "unknown argument") {
		t.Errorf("stderr = %q, want it to name the unknown argument", res.stderr)
	}
}

func TestPrintsItsVersion(t *testing.T) {
	l := listen(t)
	res := run(t, l, []string{"--version"}, "", false)

	if "9.9.9-test\n" != res.stdout {
		t.Errorf("stdout = %q, want the version", res.stdout)
	}
}

func TestPrintsHelp(t *testing.T) {
	l := listen(t)
	res := run(t, l, []string{"--help"}, "", false)

	if !strings.Contains(res.stdout, "Usage: cmd | rtail") {
		t.Errorf("stdout = %q, want usage", res.stdout)
	}
}
