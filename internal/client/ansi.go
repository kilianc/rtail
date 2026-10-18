package client

import "strings"

/*!
 * ANSI escape removal, for when stdout is not a terminal.
 *
 * A port of the `strip-ansi` package, which is a regular expression with two
 * alternatives. This is the same two alternatives as a scanner, because the
 * Go regexp engine would have to be fed every line of every pipe and this
 * does one pass with no allocation when there is nothing to strip.
 *
 *   1. CSI — ESC [ , parameter and intermediate bytes, then a final byte in
 *      0x40–0x7E. Colour, cursor movement, erase: almost everything.
 *   2. OSC — ESC ] , a payload, terminated by BEL or by ESC \. Window titles
 *      and hyperlinks, which carry arbitrary text and so cannot be matched by
 *      shape alone.
 *
 * A lone ESC that starts neither is left in place: it is data, and a pipe
 * that quietly eats bytes it does not recognise is worse than one that passes
 * them through.
 */
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}

	var out strings.Builder
	out.Grow(len(s))

	for i := 0; i < len(s); {
		if 0x1b != s[i] || i+1 >= len(s) {
			out.WriteByte(s[i])
			i++
			continue
		}

		switch s[i+1] {
		case '[':
			if end, ok := scanCSI(s, i+2); ok {
				i = end
				continue
			}
		case ']':
			if end, ok := scanOSC(s, i+2); ok {
				i = end
				continue
			}
		}

		out.WriteByte(s[i])
		i++
	}

	return out.String()
}

// scanCSI returns the index just past a CSI sequence beginning at from.
func scanCSI(s string, from int) (int, bool) {
	for i := from; i < len(s); i++ {
		// Parameter bytes 0x30–0x3F and intermediate bytes 0x20–0x2F may
		// repeat; the first byte outside both ranges ends the sequence and
		// must be a final byte to be one at all.
		if s[i] >= 0x20 && s[i] <= 0x3f {
			continue
		}
		if s[i] >= 0x40 && s[i] <= 0x7e {
			return i + 1, true
		}
		return 0, false
	}

	return 0, false
}

// scanOSC returns the index just past an OSC sequence beginning at from.
func scanOSC(s string, from int) (int, bool) {
	for i := from; i < len(s); i++ {
		if 0x07 == s[i] {
			return i + 1, true
		}
		if 0x1b == s[i] && i+1 < len(s) && '\\' == s[i+1] {
			return i + 2, true
		}
	}

	return 0, false
}
