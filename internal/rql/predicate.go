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
 * predicate_test.go, which runs the same corpus through both and asserts they
 * agree.
 */

package rql

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kilianc/rtail/v2/internal/model"
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

	// Severity ordering, matching the SQL compiler.
	if isSeverityField(field) {
		want := Rank(n.Value.Str)
		return func(rec *model.Record) bool {
			value, ok := lookup(rec, field)
			if !ok {
				return false
			}
			return compareInts(Rank(value.Text()), want, n.Op)
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
		return model.Bool(value), true
	case float64:
		if value == float64(int64(value)) {
			return model.Int(int64(value)), true
		}
		return model.Float(value), true
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

// compareValue orders a record value against a literal.
func compareValue(value model.Value, literal Literal, op Op) bool {
	// Numbers compare numerically when both sides can be read as numbers, so
	// a value that arrived as text still answers a range query correctly.
	if KindNumber == literal.Kind {
		if number, ok := numericOf(value); ok {
			return compareFloats(number, literal.Num, op)
		}
		if OpEquals == op {
			return false
		}
		return false
	}

	if KindBool == literal.Kind {
		if model.KindBool != value.Kind {
			return false
		}
		return compareBools(value.Bool, literal.Bool, op)
	}

	return compareStrings(value.Text(), literal.Str, op)
}

func numericOf(value model.Value) (float64, bool) {
	switch value.Kind {
	case model.KindInt:
		return float64(value.Int), true
	case model.KindFloat:
		return value.Float, true
	case model.KindString:
		n, err := strconv.ParseFloat(strings.TrimSpace(value.Str), 64)
		return n, nil == err
	}
	return 0, false
}

func compareFloats(a, b float64, op Op) bool {
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

func compareBools(a, b bool, op Op) bool {
	if OpEquals == op {
		return a == b
	}
	return false
}

// Thin wrappers so the JSON dependency stays in one place.
func unmarshal(text string, out any) error { return json.Unmarshal([]byte(text), out) }

func marshal(value any) (string, error) {
	encoded, err := json.Marshal(value)
	return string(encoded), err
}
