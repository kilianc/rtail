/*!
 * Compiling rQL to a Go predicate.
 *
 * The same AST that becomes SQL for historical search becomes a function over
 * a live record here, which is what makes filtered live tail fall out of the
 * design rather than being a second implementation to keep in sync. When
 * someone types a filter and switches to streaming, the semantics are
 * identical because there is one AST and one set of rules — the two compilers
 * differ only in what they emit.
 *
 * Where they cannot be identical is stated rather than hidden: see
 * internal/query/agreement_test.go, which runs the same corpus through both
 * and asserts they agree.
 *
 * The one known gap is a key that went polymorphic. Storage keeps such a
 * column as text, so SQL sees the number 200 and the string "200" as the same
 * three characters, while a live record still knows which one it holds:
 * `code="200"` matches a numeric 200 in history and not on the tail. Nothing
 * on the read side can recover the type the writer discarded.
 */

package rql

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/normalize"
)

// Predicate reports whether a record matches.
type Predicate func(*model.Record) bool

/*!
 * ToPredicate compiles an AST into a matcher.
 *
 * Regexes are compiled once, here, rather than per record — a tail doing
 * 100k lines a second cannot afford to rebuild a pattern for each one. A bad
 * pattern is a compile error, so the subscriber hears about it when it
 * subscribes instead of silently matching nothing forever.
 */
func ToPredicate(node Node) (Predicate, error) {
	switch n := node.(type) {
	case nil:
		return func(*model.Record) bool { return true }, nil

	case *And:
		children, err := compileAll(n.Children)
		if nil != err {
			return nil, err
		}
		return func(rec *model.Record) bool {
			for _, child := range children {
				if !child(rec) {
					return false
				}
			}
			return true
		}, nil

	case *Or:
		children, err := compileAll(n.Children)
		if nil != err {
			return nil, err
		}
		return func(rec *model.Record) bool {
			for _, child := range children {
				if child(rec) {
					return true
				}
			}
			return false
		}, nil

	case *Not:
		child, err := ToPredicate(n.Child)
		if nil != err {
			return nil, err
		}
		return func(rec *model.Record) bool { return !child(rec) }, nil

	case *Text:
		needle := strings.ToLower(n.Value)
		return func(rec *model.Record) bool {
			return strings.Contains(strings.ToLower(rec.Msg), needle) ||
				strings.Contains(strings.ToLower(rec.Raw), needle)
		}, nil

	case *Comparison:
		return comparisonPredicate(n)
	}

	return nil, fmt.Errorf("cannot compile %T", node)
}

func compileAll(nodes []Node) ([]Predicate, error) {
	out := make([]Predicate, 0, len(nodes))

	for _, node := range nodes {
		predicate, err := ToPredicate(node)
		if nil != err {
			return nil, err
		}
		out = append(out, predicate)
	}

	return out, nil
}

func comparisonPredicate(n *Comparison) (Predicate, error) {
	field := n.Field

	if OpExists == n.Op {
		return func(rec *model.Record) bool {
			_, ok := lookup(rec, field)
			return ok
		}, nil
	}

	if KindNull == n.Value.Kind {
		if OpEquals != n.Op {
			return nil, &Error{Message: "null can only be compared with ="}
		}
		return func(rec *model.Record) bool {
			_, ok := lookup(rec, field)
			return !ok
		}, nil
	}

	if OpMatches == n.Op {
		pattern, err := regexp.Compile(n.Value.Str)
		if nil != err {
			return nil, &Error{Message: "invalid regular expression: " + err.Error()}
		}
		return func(rec *model.Record) bool {
			value, ok := lookup(rec, field)
			return ok && pattern.MatchString(value.Text())
		}, nil
	}

	if OpContains == n.Op {
		needle := strings.ToLower(n.Value.Str)
		return func(rec *model.Record) bool {
			value, ok := lookup(rec, field)
			return ok && strings.Contains(strings.ToLower(value.Text()), needle)
		}, nil
	}

	// Severity, matching the SQL compiler: the level as stored, equality by
	// name and ordering by rank.
	if isSeverityField(field) {
		level := normalize.Level(n.Value.Str)
		want := Rank(level)
		return func(rec *model.Record) bool {
			value, ok := lookup(rec, field)
			if !ok {
				return false
			}
			if OpEquals == n.Op {
				return value.Text() == level
			}
			return compareInts(Rank(value.Text()), want, n.Op)
		}, nil
	}

	// Event and ingest time compare as instants, parsed by the same function
	// the SQL compiler binds.
	if isTimestampField(field) {
		at, err := Timestamp(n.Value)
		if nil != err {
			return nil, err
		}
		return func(rec *model.Record) bool {
			return compareTimes(timestampOf(rec, field), at, n.Op)
		}, nil
	}

	literal := n.Value

	return func(rec *model.Record) bool {
		value, ok := lookup(rec, field)
		if !ok {
			return false
		}
		return compareValue(value, literal, n.Op)
	}, nil
}

func isSeverityField(field string) bool {
	return "level" == field || "severity" == field
}

func isTimestampField(field string) bool {
	return "ts" == field || "time" == field || "ingest_ts" == field
}

// timestampOf reads ts or ingest_ts at the precision storage keeps, so a
// record compares the same live as it will once it is a Parquet row.
func timestampOf(rec *model.Record, field string) time.Time {
	if "ingest_ts" == field {
		return rec.IngestTs.UTC().Truncate(time.Microsecond)
	}
	return rec.Ts.UTC().Truncate(time.Microsecond)
}

// timestampText is DuckDB's rendering of a TIMESTAMP as VARCHAR, which is what
// `ts:2024-01` and `ts:~...` match against in SQL.
const timestampText = "2006-01-02 15:04:05.999999"

/*!
 * lookup reads a field off a record.
 *
 * Envelope fields resolve first, then promoted root keys, then dotted paths
 * into a nested JSON field. That is the same precedence the SQL resolver uses,
 * and the reason a payload key called `msg` does not shadow the extracted
 * message: the normalizer already lifted it, and both compilers agree on which
 * one wins.
 */
func lookup(rec *model.Record, field string) (model.Value, bool) {
	switch field {
	case "level", "severity":
		if "" == rec.Level {
			return model.Value{}, false
		}
		return model.Str(rec.Level), true

	case "msg", "message":
		if "" == rec.Msg {
			return model.Value{}, false
		}
		return model.Str(rec.Msg), true

	case "stream":
		return model.Str(rec.Stream), true

	case "host":
		if "" == rec.Host {
			return model.Value{}, false
		}
		return model.Str(rec.Host), true

	case "raw":
		return model.Str(rec.Raw), true

	case "seq":
		return model.Int(int64(rec.Seq)), true

	// The rest of the envelope the SQL resolver knows. Without them a tail
	// filtered on `ts>...` streamed nothing while search returned everything.
	case "ts", "time", "ingest_ts":
		return model.Str(timestampOf(rec, field).Format(timestampText)), true

	case "is_json":
		return model.Bool(rec.IsObject()), true
	}

	if value, ok := rec.Fields[field]; ok {
		if model.KindNull == value.Kind {
			return model.Value{}, false
		}
		return value, true
	}

	// A dotted path: the head names a promoted JSON field, the tail indexes
	// into it.
	if head, tail, found := strings.Cut(field, "."); found {
		if value, ok := rec.Fields[head]; ok && model.KindJSON == value.Kind {
			return jsonPath(value.Str, tail)
		}
	}

	return model.Value{}, false
}

/*!
 * jsonPath walks a dotted path into a JSON document.
 *
 * Deliberately minimal: no array indexing, no wildcards. The live-tail path
 * has to keep up with ingest, and the historical path — which is where deep
 * queries actually get run — has DuckDB's full JSON support behind it.
 *
 * A scalar comes back as text, because that is what SQL reads it as: `->>`
 * returns VARCHAR whatever the JSON held. So `req.status="200"` matches a
 * nested 200 in both, and `req.status>100` still compares numbers in both,
 * through the same read-as-a-number rule any text field gets.
 */
func jsonPath(document, path string) (model.Value, bool) {
	current := any(nil)

	if err := unmarshal(document, &current); nil != err {
		return model.Value{}, false
	}

	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return model.Value{}, false
		}

		current, ok = object[segment]
		if !ok {
			return model.Value{}, false
		}
	}

	switch value := current.(type) {
	case nil:
		return model.Value{}, false
	case bool:
		return model.Str(model.Bool(value).Text()), true
	case float64:
		if value == float64(int64(value)) {
			return model.Str(model.Int(int64(value)).Text()), true
		}
		return model.Str(model.Float(value).Text()), true
	case string:
		return model.Str(value), true
	default:
		encoded, err := marshal(value)
		if nil != err {
			return model.Value{}, false
		}
		return model.JSON(encoded), true
	}
}

/*!
 * compareValue orders a record value against a literal, by the rules the SQL
 * compiler applies to the column the value would be stored in.
 *
 * A number or a boolean answers only its own kind of literal, as a typed
 * column does in SQL, where anything else folds to FALSE. That is what makes
 * `status="200"` the string the docs say it is: it used to string-compare
 * against a numeric 200 here and match, while search returned nothing.
 *
 * Text — and JSON, which SQL reads as text — reads a bare number as a number,
 * so a value that arrived as "12" still answers `user_id=12` and range
 * queries. Anything else compares as written.
 */
func compareValue(value model.Value, literal Literal, op Op) bool {
	switch value.Kind {
	case model.KindInt, model.KindFloat:
		if KindNumber != literal.Kind {
			return false
		}
		number, _ := numericOf(value)
		return compareFloats(number, literal.Num, op)

	case model.KindBool:
		if KindBool != literal.Kind {
			return false
		}
		return compareBools(value.Bool, literal.Bool, op)
	}

	if KindNumber == literal.Kind {
		number, ok := numericOf(value)
		return ok && compareFloats(number, literal.Num, op)
	}

	return compareStrings(value.Text(), literal.Str, op)
}

func numericOf(value model.Value) (float64, bool) {
	switch value.Kind {
	case model.KindInt:
		return float64(value.Int), true
	case model.KindFloat:
		return value.Float, true
	case model.KindString, model.KindJSON:
		n, err := strconv.ParseFloat(strings.TrimSpace(value.Str), 64)
		return n, nil == err
	}
	return 0, false
}

/*!
 * compareFloats orders two numbers the way DuckDB does.
 *
 * Which differs from Go in one place: DuckDB puts NaN above every other
 * number and equal to itself, where Go makes every comparison with it false.
 * A NaN can reach a record — OTLP carries IEEE doubles, and "nan" reads as a
 * number — and `x>5` has to give the same answer live as in history.
 */
func compareFloats(a, b float64, op Op) bool {
	order := 0
	switch {
	case math.IsNaN(a) && math.IsNaN(b):
	case math.IsNaN(a), a > b:
		order = 1
	case math.IsNaN(b), a < b:
		order = -1
	}

	return compareOrder(order, op)
}

func compareOrder(order int, op Op) bool {
	switch op {
	case OpEquals:
		return 0 == order
	case OpLess:
		return order < 0
	case OpLessEqual:
		return order <= 0
	case OpGreater:
		return order > 0
	case OpGreaterEqual:
		return order >= 0
	}
	return false
}

func compareTimes(a, b time.Time, op Op) bool {
	return compareOrder(a.Compare(b), op)
}

func compareInts(a, b int, op Op) bool {
	return compareFloats(float64(a), float64(b), op)
}

func compareStrings(a, b string, op Op) bool {
	switch op {
	case OpEquals:
		return a == b
	case OpLess:
		return a < b
	case OpLessEqual:
		return a <= b
	case OpGreater:
		return a > b
	case OpGreaterEqual:
		return a >= b
	}
	return false
}

// compareBools orders false before true, as SQL does, so `ok<true` finds the
// false ones in both places rather than only in history.
func compareBools(a, b bool, op Op) bool {
	order := 0
	switch {
	case a && !b:
		order = 1
	case !a && b:
		order = -1
	}
	return compareOrder(order, op)
}

// Thin wrappers so the JSON dependency stays in one place.
func unmarshal(text string, out any) error { return json.Unmarshal([]byte(text), out) }

func marshal(value any) (string, error) {
	encoded, err := json.Marshal(value)
	return string(encoded), err
}
