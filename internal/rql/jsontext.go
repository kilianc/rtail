/*!
 * JSON as DuckDB reads it back.
 *
 * A dotted path into a nested field is `->>` in SQL, and `->>` does not hand
 * back the bytes of the document: DuckDB parses it with yyjson and writes the
 * value out again. The live predicate has to produce that same text, or
 * `req.size="1.0"` and `req.body:"<a"` answer differently on the tail than in
 * history. It used to decode to Go values and re-marshal, which sorted keys,
 * escaped HTML, rounded big integers through a float and wrote 1.0 as 1.
 *
 * Every rule below was read off DuckDB itself; internal/query's agreement
 * test holds the cases that pin them.
 */

package rql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/kilianc/rtail/v2/internal/model"
)

/*!
 * member returns the raw value of an object's member called key.
 *
 * The first one, when a key repeats: that is the one DuckDB's path lookup
 * finds, where decoding into a map keeps the last.
 */
func member(document json.RawMessage, key string) (json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(document))

	if open, err := decoder.Token(); nil != err || json.Delim('{') != open {
		return nil, false
	}

	for decoder.More() {
		name, err := decoder.Token()
		if nil != err {
			return nil, false
		}

		var value json.RawMessage
		if err := decoder.Decode(&value); nil != err {
			return nil, false
		}

		if key == name {
			return value, true
		}
	}

	return nil, false
}

/*!
 * jsonValue is what `->>` returns for a raw JSON value: a scalar as its text,
 * a container as compact JSON, and null as nothing at all.
 */
func jsonValue(raw json.RawMessage) (model.Value, bool) {
	trimmed := bytes.TrimSpace(raw)
	if 0 == len(trimmed) {
		return model.Value{}, false
	}

	switch trimmed[0] {
	case 'n':
		return model.Value{}, false

	case 't', 'f':
		return model.Str(string(trimmed)), true

	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); nil != err {
			return model.Value{}, false
		}
		return model.Str(text), true

	case '{', '[':
		var sb strings.Builder

		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()

		if err := writeJSON(&sb, decoder); nil != err {
			return model.Value{}, false
		}
		return model.JSON(sb.String()), true
	}

	return model.Str(jsonNumber(string(trimmed))), true
}

// writeJSON re-renders the next value in decoder the way yyjson writes it:
// compact, members in their order and repeats kept, numbers as jsonNumber
// has them, strings as jsonString does.
func writeJSON(sb *strings.Builder, decoder *json.Decoder) error {
	token, err := decoder.Token()
	if nil != err {
		return err
	}

	switch value := token.(type) {
	case json.Delim:
		closing := byte('}')
		if '[' == value {
			closing = ']'
		}

		sb.WriteByte(byte(value))

		for i := 0; decoder.More(); i++ {
			if i > 0 {
				sb.WriteByte(',')
			}

			if '{' == value {
				key, err := decoder.Token()
				if nil != err {
					return err
				}
				jsonString(sb, key.(string))
				sb.WriteByte(':')
			}

			if err := writeJSON(sb, decoder); nil != err {
				return err
			}
		}

		// The closing delimiter, which More has already seen.
		if _, err := decoder.Token(); nil != err {
			return err
		}

		sb.WriteByte(closing)

	case string:
		jsonString(sb, value)

	case json.Number:
		sb.WriteString(jsonNumber(value.String()))

	case bool:
		sb.WriteString(strconv.FormatBool(value))

	case nil:
		sb.WriteString("null")
	}

	return nil
}

/*!
 * jsonString escapes as little as JSON allows, which is what yyjson does.
 *
 * The quote, the backslash and the control characters, with the short forms
 * where JSON has one and upper-case hex where it does not. Nothing else: not
 * `<`, `>` and `&`, which encoding/json escapes for HTML, nor U+2028 and
 * U+2029, which it escapes for JavaScript.
 */
func jsonString(sb *strings.Builder, text string) {
	sb.WriteByte('"')

	for i := 0; i < len(text); i++ {
		switch c := text[i]; c {
		case '"', '\\':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(sb, `\u%04X`, c)
			} else {
				sb.WriteByte(c)
			}
		}
	}

	sb.WriteByte('"')
}

/*!
 * jsonNumber writes a JSON number as DuckDB's `->>` does.
 *
 * An integer that fits in 64 bits is that integer, so -0 is 0. A real is the
 * shortest decimal that reads back as the same double, always with a
 * fraction — 1e2 is 100.0 — and in exponent form only outside 1e-6 up to
 * 1e21, which is where JavaScript draws the same line: 1e-7 and 1e21 stay as
 * they are, 1e20 is written out in full. A number too large for either keeps
 * the text it was written with, because DuckDB keeps such a number raw
 * rather than lose it to a float.
 */
func jsonNumber(text string) string {
	if !strings.ContainsAny(text, ".eE") {
		if n, err := strconv.ParseInt(text, 10, 64); nil == err {
			return strconv.FormatInt(n, 10)
		}
		if n, err := strconv.ParseUint(text, 10, 64); nil == err {
			return strconv.FormatUint(n, 10)
		}
		return text
	}

	f, err := strconv.ParseFloat(text, 64)
	if nil != err {
		return text
	}

	// The shortest digits that round-trip, and where the decimal point goes
	// relative to the first of them.
	mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(math.Abs(f), 'e', -1, 64), "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	power, _ := strconv.Atoi(exponent)
	point := power + 1

	var sb strings.Builder
	if math.Signbit(f) {
		sb.WriteByte('-')
	}

	switch {
	case point <= -6 || point > 21:
		sb.WriteString(digits[:1])
		if len(digits) > 1 {
			sb.WriteString("." + digits[1:])
		}
		sb.WriteString("e" + strconv.Itoa(power))

	case point <= 0:
		sb.WriteString("0." + strings.Repeat("0", -point) + digits)

	case len(digits) <= point:
		sb.WriteString(digits + strings.Repeat("0", point-len(digits)) + ".0")

	default:
		sb.WriteString(digits[:point] + "." + digits[point:])
	}

	return sb.String()
}
