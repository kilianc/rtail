/*!
 * rQL — the filter bar language.
 *
 * docs/proposal-logging-system.md §6.2. Terse, Stackdriver-shaped, and what
 * 95% of use looks like:
 *
 *     level>=ERROR service="api" user_id=42 "connection timeout"
 *     -path:/health AND (region=us-east-1 OR region=us-west-2)
 *     latency_ms>500 req.path:~"^/v1/.*"
 *
 * The single most important property of this package is that one parse
 * produces one AST which compiles *two* ways: to SQL for historical search,
 * and to a Go predicate for live tail. That is what makes "stream with a
 * filter applied" feel correct rather than approximate — the two paths cannot
 * drift, because there is only one set of semantics to drift from.
 *
 * See sql.go and predicate.go for the two compilers.
 */

package rql

import (
	"fmt"
	"strings"
)

// Op is a comparison.
type Op uint8

const (
	// OpEquals is `k=v` — exact equality.
	OpEquals Op = iota
	// OpContains is `k:v` — case-insensitive substring.
	OpContains
	// OpMatches is `k:~re` — regular expression.
	OpMatches
	OpLess
	OpLessEqual
	OpGreater
	OpGreaterEqual
	// OpExists is `k=*` — the key is present and not null.
	OpExists
)

func (o Op) String() string {
	switch o {
	case OpContains:
		return ":"
	case OpMatches:
		return ":~"
	case OpLess:
		return "<"
	case OpLessEqual:
		return "<="
	case OpGreater:
		return ">"
	case OpGreaterEqual:
		return ">="
	case OpExists:
		return "=*"
	default:
		return "="
	}
}

// Node is one element of a parsed query.
type Node interface{ node() }

/*!
 * Comparison is `field op value`.
 *
 * Field is the *source key* as the user typed it — `user_id`, or a dotted path
 * like `req.path`. Mapping that onto a physical column is the resolver's job,
 * not the parser's: the parser has no idea which files are being read, and the
 * answer differs per query.
 */
type Comparison struct {
	Field string
	Op    Op
	Value Literal
}

func (*Comparison) node() {}

/*!
 * Text is a bare word or quoted phrase with no field — full-text across the
 * message, and the raw line behind it.
 */
type Text struct {
	Value string
}

func (*Text) node() {}

// And, Or and Not are the boolean structure.
type And struct{ Children []Node }
type Or struct{ Children []Node }
type Not struct{ Child Node }

func (*And) node() {}
func (*Or) node()  {}
func (*Not) node() {}

// Kind distinguishes the literal types the language can express.
type Kind uint8

const (
	KindString Kind = iota
	KindNumber
	KindBool
	KindNull
	// KindWildcard is the bare `*` in `k=*`.
	KindWildcard
)

// Literal is a parsed value.
type Literal struct {
	Kind Kind
	Str  string
	Num  float64
	Bool bool
	// Quoted records that the user wrote the value in quotes, which suppresses
	// the bareword coercions below — `status="200"` is the string "200", not
	// the number 200.
	Quoted bool
}

// String renders a literal the way it was written, for round-tripping a query
// back into the search bar.
func (l Literal) String() string {
	switch l.Kind {
	case KindNumber:
		return trimFloat(l.Num)
	case KindBool:
		if l.Bool {
			return "true"
		}
		return "false"
	case KindNull:
		return "null"
	case KindWildcard:
		return "*"
	default:
		if l.Quoted || needsQuoting(l.Str) {
			return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(l.Str) + `"`
		}
		return l.Str
	}
}

func needsQuoting(s string) bool {
	if "" == s {
		return true
	}
	return strings.ContainsAny(s, " \t\"'()<>=:-")
}

func trimFloat(f float64) string {
	text := fmt.Sprintf("%v", f)
	return text
}

/*!
 * String renders an AST back to rQL.
 *
 * This is not debug output — it is how the UI builds queries by clicking. A
 * "filter to this value" button appends a Comparison and re-renders, so the
 * search bar always shows something the user could have typed themselves.
 */
func String(node Node) string {
	switch n := node.(type) {
	case nil:
		return ""

	case *Comparison:
		if OpExists == n.Op {
			return n.Field + "=*"
		}
		return n.Field + n.Op.String() + n.Value.String()

	case *Text:
		if needsQuoting(n.Value) {
			return `"` + strings.ReplaceAll(n.Value, `"`, `\"`) + `"`
		}
		return n.Value

	case *Not:
		inner := String(n.Child)
		if _, simple := n.Child.(*Comparison); simple {
			return "-" + inner
		}
		if _, simple := n.Child.(*Text); simple {
			return "-" + inner
		}
		return "NOT (" + inner + ")"

	case *And:
		parts := make([]string, 0, len(n.Children))
		for _, child := range n.Children {
			parts = append(parts, group(child, false))
		}
		return strings.Join(parts, " ")

	case *Or:
		parts := make([]string, 0, len(n.Children))
		for _, child := range n.Children {
			parts = append(parts, group(child, true))
		}
		return strings.Join(parts, " OR ")

	default:
		return ""
	}
}

// group parenthesises a child when precedence would otherwise change meaning.
func group(node Node, insideOr bool) string {
	switch node.(type) {
	case *Or:
		return "(" + String(node) + ")"
	case *And:
		if insideOr {
			return "(" + String(node) + ")"
		}
	}
	return String(node)
}

/*!
 * Fields lists every field the query references, deduplicated.
 *
 * The planner uses this to decide which columns it needs to resolve, and the
 * catalog uses it to prune on key presence.
 */
func Fields(node Node) []string {
	seen := map[string]bool{}
	var out []string

	var walk func(Node)
	walk = func(n Node) {
		switch n := n.(type) {
		case *Comparison:
			if !seen[n.Field] {
				seen[n.Field] = true
				out = append(out, n.Field)
			}
		case *Not:
			walk(n.Child)
		case *And:
			for _, child := range n.Children {
				walk(child)
			}
		case *Or:
			for _, child := range n.Children {
				walk(child)
			}
		}
	}

	walk(node)
	return out
}
