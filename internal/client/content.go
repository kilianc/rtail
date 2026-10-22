package client

import (
	"encoding/json"
	"strings"

	"github.com/titanous/json5"
)

/*!
 * What a line becomes on the wire.
 *
 * Every line is offered to a JSON parser first, so a structured logger's
 * output arrives as an object the server can index by field rather than as a
 * string nobody can query. A line that is not JSON is sent as a JSON string,
 * which is why `0` arrives as a number and `zero` arrives as text.
 *
 * Three passes, in this order:
 *
 *   1. Strict JSON, sent **verbatim**. The JavaScript client parsed and
 *      re-serialized every record, which re-ordered its keys alphabetically,
 *      turned 1.0 into 1 and rounded any integer past 2^53 — a trace id or a
 *      ledger sequence, exactly the fields where a quiet rewrite is worst.
 *      Passing the bytes through is both faster and more honest.
 *   2. JSON5, for the sloppy-JSON lines people pipe by hand: unquoted keys,
 *      single quotes, trailing commas. This one has to be re-serialized,
 *      because JSON5 is not JSON and the server only reads JSON.
 *   3. A JSON string of the raw line.
 */
func encodeContent(raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)

	if "" != trimmed && json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}

	if "" != trimmed {
		var value any
		if err := json5.Unmarshal([]byte(trimmed), &value); nil == err {
			// Marshal can still fail here: JSON5 has NaN and Infinity and
			// JSON does not. Falling through to the string form keeps the
			// line rather than dropping it.
			if encoded, err := json.Marshal(value); nil == err {
				return encoded
			}
		}
	}

	// json.Marshal of a string cannot fail, and the raw line — not the
	// trimmed one — is what the reader typed.
	encoded, _ := json.Marshal(raw)

	return encoded
}
