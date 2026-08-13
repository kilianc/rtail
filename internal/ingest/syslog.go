/*!
 * The syslog receiver.
 *
 * RFC5424 over UDP and TCP, falling back to RFC3164 for anything that predates
 * it — which is most appliances, and nginx, and anything piping through
 * logger(1). Nobody chooses syslog any more; plenty of things emit it and
 * cannot be changed, which is the entire reason to speak it.
 *
 * The mapping is the useful part: priority carries a real severity, so a
 * syslog line arrives with a level already set and `level>=ERROR` works on it
 * without anyone writing a parser.
 */

package ingest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/model"
)

/*!
 * severities are RFC5424's, which run the opposite way to everyone else's:
 * 0 is the worst. Getting this backwards is the classic syslog bug, and it is
 * silent — every alert becomes a debug line.
 */
var severities = [8]string{
	"EMERGENCY", "ALERT", "CRITICAL", "ERROR", "WARN", "NOTICE", "INFO", "DEBUG",
}

var facilities = [24]string{
	"kern", "user", "mail", "daemon", "auth", "syslog", "lpr", "news",
	"uucp", "cron", "authpriv", "ftp", "ntp", "audit", "alert", "clock",
	"local0", "local1", "local2", "local3", "local4", "local5", "local6", "local7",
}

// SyslogStats counts what the receiver has seen.
type SyslogStats struct {
	Received atomic.Uint64
	Invalid  atomic.Uint64
	Bytes    atomic.Uint64
}

// SyslogOptions configures the receiver.
type SyslogOptions struct {
	Store logstore.Store
	Log   *slog.Logger
	// Default is the stream for messages whose APP-NAME is absent.
	Default string
}

// Syslog receives RFC5424 and RFC3164 messages.
type Syslog struct {
	opts  SyslogOptions
	udp   *net.UDPConn
	tcp   net.Listener
	stats SyslogStats
}

// DefaultSyslogStream names messages that carry no app-name.
const DefaultSyslogStream = "syslog"

/*!
 * ListenSyslog binds both transports on the same port.
 *
 * Both, because senders disagree: UDP is the traditional default and TCP is
 * what anything that minds losing a message uses. Binding one and not the
 * other produces a receiver that works for half the fleet, silently.
 */
func ListenSyslog(host string, port int, opts SyslogOptions) (*Syslog, error) {
	if "" == opts.Default {
		opts.Default = DefaultSyslogStream
	}
	if nil == opts.Log {
		opts.Log = slog.Default()
	}

	address := net.JoinHostPort(host, strconv.Itoa(port))

	udpAddr, err := net.ResolveUDPAddr("udp4", address)
	if nil != err {
		return nil, err
	}

	udp, err := net.ListenUDP("udp4", udpAddr)
	if nil != err {
		return nil, fmt.Errorf("binding syslog UDP: %w", err)
	}

	tcp, err := net.Listen("tcp4", address)
	if nil != err {
		udp.Close()
		return nil, fmt.Errorf("binding syslog TCP: %w", err)
	}

	return &Syslog{opts: opts, udp: udp, tcp: tcp}, nil
}

// Addr reports the bound UDP address, which is how tests find an ephemeral port.
func (s *Syslog) Addr() *net.UDPAddr { return s.udp.LocalAddr().(*net.UDPAddr) }

// TCPAddr reports the bound TCP address.
func (s *Syslog) TCPAddr() net.Addr { return s.tcp.Addr() }

// Stats exposes the counters.
func (s *Syslog) Stats() *SyslogStats { return &s.stats }

// Serve reads both transports until the context is cancelled.
func (s *Syslog) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.udp.Close()
		s.tcp.Close()
	}()

	errs := make(chan error, 2)

	go func() { errs <- s.serveUDP(ctx) }()
	go func() { errs <- s.serveTCP(ctx) }()

	if err := <-errs; nil != err {
		return err
	}

	return <-errs
}

func (s *Syslog) serveUDP(ctx context.Context) error {
	buffer := make([]byte, maxDatagram)

	for {
		n, remote, err := s.udp.ReadFromUDP(buffer)
		if nil != err {
			if nil != ctx.Err() || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		s.stats.Bytes.Add(uint64(n))
		s.handle(ctx, buffer[:n], remote.IP.String())
	}
}

func (s *Syslog) serveTCP(ctx context.Context) error {
	for {
		conn, err := s.tcp.Accept()
		if nil != err {
			if nil != ctx.Err() || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		go s.serveConn(ctx, conn)
	}
}

/*!
 * serveConn reads one TCP connection.
 *
 * RFC6587 allows two framings on a stream and gives no way to negotiate which,
 * so the first byte decides: a digit means octet counting ("123 <13>1 ..."),
 * anything else means newline-delimited. Guessing per message rather than per
 * connection would be worse — a message body starting with a digit is common.
 */
func (s *Syslog) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	reader := bufio.NewReaderSize(conn, 64<<10)

	for {
		first, err := reader.Peek(1)
		if nil != err {
			return
		}

		if first[0] >= '0' && first[0] <= '9' {
			if err := s.readCounted(ctx, reader, host); nil != err {
				return
			}
			continue
		}

		line, err := reader.ReadBytes('\n')
		if 0 != len(line) {
			s.stats.Bytes.Add(uint64(len(line)))
			s.handle(ctx, line, host)
		}
		if nil != err {
			return
		}
	}
}

// readCounted reads one octet-counted frame: "<length> <message>".
func (s *Syslog) readCounted(ctx context.Context, reader *bufio.Reader, host string) error {
	header, err := reader.ReadString(' ')
	if nil != err {
		return err
	}

	length, err := strconv.Atoi(strings.TrimSpace(header))
	if nil != err || length <= 0 || length > maxLine {
		s.stats.Invalid.Add(1)
		return fmt.Errorf("bad octet count %q", strings.TrimSpace(header))
	}

	message := make([]byte, length)
	if _, err := io.ReadFull(reader, message); nil != err {
		return err
	}

	s.stats.Bytes.Add(uint64(length))
	s.handle(ctx, message, host)

	return nil
}

func (s *Syslog) handle(ctx context.Context, raw []byte, host string) {
	rec := parseSyslog(raw, host, s.opts.Default)
	if nil == rec {
		s.stats.Invalid.Add(1)
		s.opts.Log.Debug("dropping unparseable syslog message", "from", host, "bytes", len(raw))
		return
	}

	s.stats.Received.Add(1)

	if err := s.opts.Store.Append(ctx, rec); nil != err {
		s.opts.Log.Error("appending a syslog record", "stream", rec.Stream, "err", err)
	}
}

// Close releases both listeners.
func (s *Syslog) Close() error {
	s.udp.Close()
	return s.tcp.Close()
}

/*!
 * parseSyslog reads RFC5424, falling back to RFC3164.
 *
 * Returns nil for anything that is not syslog at all. A message with a valid
 * priority but a body we cannot dissect is still kept — the text is the point,
 * and half a parse beats a dropped line.
 */
func parseSyslog(raw []byte, host, defaultStream string) *model.Record {
	text := strings.TrimRight(string(raw), "\r\n\x00")

	now := time.Now().UTC()

	rec := &model.Record{
		Stream:   defaultStream,
		Host:     host,
		Ts:       now,
		IngestTs: now,
		Raw:      text,
		Type:     "string",
		Msg:      text,
		Fields:   map[string]model.Value{},
	}

	priority, rest, ok := readPriority(text)
	if !ok {
		return nil
	}

	rec.Level = severities[priority%8]
	if facility := priority / 8; facility < len(facilities) {
		rec.Fields["facility"] = model.Str(facilities[facility])
	}
	rec.Fields["severity"] = model.Str(rec.Level)

	// RFC5424 announces itself with a version right after the priority.
	if strings.HasPrefix(rest, "1 ") {
		parse5424(rec, strings.TrimPrefix(rest, "1 "))
	} else {
		parse3164(rec, rest)
	}

	if "" == rec.Msg {
		rec.Msg = text
	}

	return rec
}

// readPriority reads "<13>" and returns the number and the remainder.
func readPriority(text string) (int, string, bool) {
	if !strings.HasPrefix(text, "<") {
		return 0, "", false
	}

	end := strings.IndexByte(text, '>')
	if end < 2 || end > 4 {
		return 0, "", false
	}

	priority, err := strconv.Atoi(text[1:end])
	if nil != err || priority < 0 || priority > 191 {
		return 0, "", false
	}

	return priority, text[end+1:], true
}

/*!
 * parse5424 reads the modern format:
 *
 *   TIMESTAMP HOSTNAME APP-NAME PROCID MSGID [SD] MSG
 *
 * A NILVALUE is "-" in every position, which is why each field is checked
 * rather than assigned blindly.
 */
func parse5424(rec *model.Record, rest string) {
	fields := make([]string, 0, 6)

	for range 5 {
		field, remainder, found := strings.Cut(rest, " ")
		fields = append(fields, field)
		rest = remainder
		if !found {
			break
		}
	}

	if len(fields) < 5 {
		rec.Msg = strings.TrimSpace(rest)
		return
	}

	if ts, err := time.Parse(time.RFC3339Nano, fields[0]); nil == err {
		rec.Ts = ts.UTC()
	}

	if "-" != fields[1] && "" != fields[1] {
		rec.Fields["hostname"] = model.Str(fields[1])
	}

	// APP-NAME is the program, which is the natural stream.
	if "-" != fields[2] && "" != fields[2] {
		rec.Stream = fields[2]
		rec.Fields["app"] = model.Str(fields[2])
	}

	if "-" != fields[3] && "" != fields[3] {
		rec.Fields["pid"] = model.Str(fields[3])
	}

	if "-" != fields[4] && "" != fields[4] {
		rec.Fields["msgid"] = model.Str(fields[4])
	}

	rec.Msg = strings.TrimSpace(readStructuredData(rec, rest))
}

/*!
 * readStructuredData consumes `[id key="value" ...]` groups and promotes their
 * parameters, returning whatever follows as the message.
 *
 * This is the one part of syslog that carries structure, so it is worth
 * unpacking: an nginx or systemd line with SD attached arrives with real
 * fields rather than a wall of text.
 */
func readStructuredData(rec *model.Record, rest string) string {
	rest = strings.TrimLeft(rest, " ")

	if strings.HasPrefix(rest, "-") {
		return strings.TrimPrefix(rest, "-")
	}

	for strings.HasPrefix(rest, "[") {
		end := findElementEnd(rest)
		if end < 0 {
			return rest
		}

		parseElement(rec, rest[1:end])
		rest = strings.TrimLeft(rest[end+1:], " ")
	}

	return rest
}

// findElementEnd locates the ] closing an element, honouring escapes inside
// quoted values.
func findElementEnd(text string) int {
	quoted := false

	for i := 1; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++
		case '"':
			quoted = !quoted
		case ']':
			if !quoted {
				return i
			}
		}
	}

	return -1
}

func parseElement(rec *model.Record, element string) {
	id, params, _ := strings.Cut(element, " ")
	if "" == id {
		return
	}

	for _, pair := range splitParams(params) {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			continue
		}

		value = strings.Trim(value, `"`)
		value = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\]`, `]`).Replace(value)

		// Namespaced by the element id, because two elements may use the same
		// parameter name for different things.
		rec.Fields[id+"."+key] = model.Str(value)
	}
}

// splitParams splits on spaces that are not inside quotes.
func splitParams(params string) []string {
	var (
		out     []string
		current strings.Builder
		quoted  bool
	)

	for i := 0; i < len(params); i++ {
		switch char := params[i]; {
		case '\\' == char && i+1 < len(params):
			current.WriteByte(char)
			i++
			current.WriteByte(params[i])
		case '"' == char:
			quoted = !quoted
			current.WriteByte(char)
		case ' ' == char && !quoted:
			if current.Len() > 0 {
				out = append(out, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(char)
		}
	}

	if current.Len() > 0 {
		out = append(out, current.String())
	}

	return out
}

/*!
 * parse3164 reads the BSD format:
 *
 *   MMM dd hh:mm:ss HOSTNAME TAG[pid]: MSG
 *
 * Loosely, because it was never really specified — implementations disagree
 * about nearly every field. Anything that does not fit is left as the message,
 * which is the only part that always exists.
 */
func parse3164(rec *model.Record, rest string) {
	rest = strings.TrimLeft(rest, " ")

	// "Jan  2 15:04:05" is fifteen characters, with a space-padded day.
	if len(rest) > 15 {
		if ts, err := time.Parse(time.Stamp, rest[:15]); nil == err {
			rec.Ts = resolve3164(ts, time.Now())
			rest = strings.TrimLeft(rest[15:], " ")
		}
	}

	hostname, remainder, found := strings.Cut(rest, " ")
	if !found {
		rec.Msg = rest
		return
	}

	rec.Fields["hostname"] = model.Str(hostname)

	tag, message, found := strings.Cut(remainder, ": ")
	if !found {
		rec.Msg = remainder
		return
	}

	// "nginx[1234]" — the program is the stream, the pid a field.
	if open := strings.IndexByte(tag, '['); open > 0 && strings.HasSuffix(tag, "]") {
		rec.Fields["pid"] = model.Str(tag[open+1 : len(tag)-1])
		tag = tag[:open]
	}

	if "" != tag {
		rec.Stream = tag
		rec.Fields["app"] = model.Str(tag)
	}

	rec.Msg = message
}

/*!
 * resolve3164 turns a zoneless BSD timestamp into an instant.
 *
 * Two things the format cannot tell us, and both have a conventional answer:
 *
 * **No timezone.** RFC3164 says the stamp is the sending device's local time,
 * which a receiver has no way to know. rsyslog and syslog-ng resolve it in the
 * *receiver's* zone, on the reasoning that a box shipping BSD syslog is
 * usually near the box collecting it — so that is what we do. Interpreting it
 * as UTC instead silently shifts every record by the operator's offset, which
 * is invisible until someone compares two streams and finds them hours apart.
 *
 * **No year.** The current one is nearly always right. The exception is the
 * turn of the year, where a message sent on 31 December arrives on 1 January
 * and lands eleven months in the future; anything implausibly ahead of now is
 * therefore treated as last year's.
 */
func resolve3164(stamp, now time.Time) time.Time {
	at := time.Date(now.Year(), stamp.Month(), stamp.Day(),
		stamp.Hour(), stamp.Minute(), stamp.Second(), 0, time.Local)

	// A day of slack, so ordinary clock skew is not mistaken for a rollover.
	if at.Sub(now) > 25*time.Hour {
		at = at.AddDate(-1, 0, 0)
	}

	return at.UTC()
}
