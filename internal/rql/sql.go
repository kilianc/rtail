/*!
 * Compiling rQL to SQL.
 *
 * Two things make this more than a string-builder.
 *
 * First, values are always bound as parameters, never interpolated. A log
 * search box is about as exposed as an input gets, and the alternative is
 * quoting rules that are one edge case away from being an injection.
 *
 * Second, field references go through a Resolver rather than being written
 * literally. Which SQL expression a field becomes depends on which files are
 * being read, and only the planner knows that. See
 * internal/query/resolve.go.
 */

package rql

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/kilianc/rtail/v2/internal/normalize"
)

/*!
 * Resolver maps a source key onto the SQL expression that reads it.
 *
 * Kind is what the expression's type is, so the compiler knows whether a
 * comparison needs a cast. Ok is false for a field no file in the pruned set
 * could possibly have, which lets the compiler fold the comparison to FALSE
 * instead of emitting a scan that cannot match.
 */
type Resolver interface {
	Resolve(field string) (expr string, kind FieldKind, ok bool)
}

// FieldKind is the resolved type of a field expression.
type FieldKind uint8

const (
	FieldString FieldKind = iota
	FieldNumber
	FieldBool
	FieldJSON
	// FieldSeverity is `level`, which orders by severity rather than
	// alphabetically — so `level>=ERROR` means what a person means by it, and
	// not "any level whose name sorts after E".
	FieldSeverity
	// FieldTimestamp is ts / ingest_ts.
	FieldTimestamp
)

/*!
 * SeverityRank orders level names.
 *
 * Anything unrecognised ranks above DEBUG and below WARN: an unknown level is
 * more likely to be an application-specific info-ish level than a hidden
 * emergency, and ranking it high would make `level>=ERROR` noisy for everyone
 * who invented their own.
 */
var SeverityRank = map[string]int{
	"TRACE": 10, "DEBUG": 20, "INFO": 30, "NOTICE": 35,
	"WARN": 40, "ERROR": 50, "CRITICAL": 55, "ALERT": 58,
	"EMERGENCY": 60, "FATAL": 60,
}

// Levels names every ranked level, least severe first.
var Levels = []string{
	"TRACE", "DEBUG", "INFO", "NOTICE", "WARN",
	"ERROR", "CRITICAL", "ALERT", "EMERGENCY", "FATAL",
}

// DefaultSeverityRank is where an unrecognised level sits.
const DefaultSeverityRank = 33

// Rank returns a level's severity rank.
func Rank(level string) int {
	if rank, ok := SeverityRank[strings.ToUpper(strings.TrimSpace(level))]; ok {
		return rank
	}
	if "" == level {
		return 0
	}
	return DefaultSeverityRank
}

/*!
 * levelOf reads a level literal as the level ingest would have stored.
 *
 * Text goes through normalize.Level, so `warning` is WARN. A bare number is
 * pino's scale, because that is what ingest read it as: a line carrying
 * `"level":50` was stored as ERROR, and reading the literal 50 as the text
 * "50" meant `level=50` found none of them and `level>=40` ranked 40 as a
 * level nobody had heard of. A number off the scale stays text, as a quoted
 * one always does.
 */
func levelOf(literal Literal) string {
	if KindNumber == literal.Kind && literal.Num == math.Trunc(literal.Num) {
		if name, ok := normalize.PinoLevel(int64(literal.Num)); ok {
			return name
		}
	}
	return normalize.Level(literal.Str)
}

// SQL is a compiled predicate and its bound arguments.
type SQL struct {
	Expr string
	Args []any
}

type compiler struct {
	resolver Resolver
	args     []any
}

/*!
 * ToSQL compiles an AST into a WHERE-clause fragment.
 *
 * A nil node compiles to TRUE, so an empty search bar reads as "everything"
 * rather than as an error at the call site.
 */
func ToSQL(node Node, resolver Resolver) (*SQL, error) {
	c := &compiler{resolver: resolver}

	expr, err := c.compile(node)
	if nil != err {
		return nil, err
	}

	return &SQL{Expr: expr, Args: c.args}, nil
}

func (c *compiler) bind(value any) string {
	c.args = append(c.args, value)
	return "?"
}

func (c *compiler) compile(node Node) (string, error) {
	switch n := node.(type) {
	case nil:
		return "TRUE", nil

	case *And:
		return c.join(n.Children, " AND ")

	case *Or:
		return c.join(n.Children, " OR ")

	case *Not:
		inner, err := c.compile(n.Child)
		if nil != err {
			return "", err
		}
		// NOT of a NULL comparison is NULL, not TRUE, so a row where the field
		// is absent would vanish from `-level=ERROR`. coalesce makes absence
		// count as "did not match", which is what a person means by "not".
		return "NOT coalesce(" + inner + ", FALSE)", nil

	case *Text:
		return c.text(n.Value), nil

	case *Comparison:
		return c.comparison(n)
	}

	return "", fmt.Errorf("cannot compile %T", node)
}

func (c *compiler) join(children []Node, sep string) (string, error) {
	parts := make([]string, 0, len(children))

	for _, child := range children {
		expr, err := c.compile(child)
		if nil != err {
			return "", err
		}
		parts = append(parts, expr)
	}

	return "(" + strings.Join(parts, sep) + ")", nil
}

/*!
 * text compiles a bare word or phrase to a full-text match.
 *
 * msg first, then raw. Checking msg alone would miss anything the normalizer
 * did not lift out; checking raw alone would decompress the widest column in
 * the file for every query. Ordering them this way lets the engine short
 * circuit on the cheap one.
 */
func (c *compiler) text(value string) string {
	pattern := "%" + escapeLike(value) + "%"

	// Bound twice, not once: these are positional parameters, so two
	// placeholders need two arguments even when the value is identical.
	return "(msg ILIKE " + c.bind(pattern) + " ESCAPE '\\'" +
		" OR raw ILIKE " + c.bind(pattern) + " ESCAPE '\\')"
}

func (c *compiler) comparison(n *Comparison) (string, error) {
	expr, kind, ok := c.resolver.Resolve(n.Field)

	// No file in the pruned set has this field and none kept raw, so nothing
	// can match. Folding to FALSE beats scanning to discover the same thing.
	if !ok {
		if OpExists == n.Op {
			return "FALSE", nil
		}
		return "FALSE", nil
	}

	if OpExists == n.Op {
		return "(" + expr + " IS NOT NULL)", nil
	}

	if KindNull == n.Value.Kind {
		if OpEquals == n.Op {
			return "(" + expr + " IS NULL)", nil
		}
		return "", &Error{Message: "null can only be compared with ="}
	}

	switch n.Op {
	case OpContains:
		return "(" + castText(expr, kind) + " ILIKE " + c.bind("%"+escapeLike(n.Value.Str)+"%") + " ESCAPE '\\')", nil

	case OpMatches:
		// Checked here, as the predicate does, rather than left to DuckDB: a
		// bad pattern is the query's fault and deserves a 400 that says so,
		// not a 500 carrying the whole generated statement. Both are RE2.
		if _, err := regexp.Compile(n.Value.Str); nil != err {
			return "", &Error{Message: "invalid regular expression: " + err.Error()}
		}
		return "regexp_matches(" + castText(expr, kind) + ", " + c.bind(n.Value.Str) + ")", nil
	}

	return c.ordered(n, expr, kind)
}

// ordered compiles =, <, <=, > and >=.
func (c *compiler) ordered(n *Comparison, expr string, kind FieldKind) (string, error) {
	operator := map[Op]string{
		OpEquals:       "=",
		OpLess:         "<",
		OpLessEqual:    "<=",
		OpGreater:      ">",
		OpGreaterEqual: ">=",
	}[n.Op]

	switch kind {
	case FieldSeverity:
		/*!
		 * The level as written becomes the level as stored first: `warning`
		 * is WARN, because that is what ingest turned it into.
		 *
		 * Then equality compares names and only ordering compares ranks.
		 * Ranks are not names — FATAL and EMERGENCY share one, and so does
		 * every level nobody has heard of — so `level=AUDIT` by rank matched
		 * every other unknown level, and `level=FATAL` matched EMERGENCY.
		 */
		level := levelOf(n.Value)
		if OpEquals == n.Op {
			return "(" + expr + " = " + c.bind(level) + ")", nil
		}
		return "(" + severityCase(expr) + " " + operator + " " + c.bind(Rank(level)) + ")", nil

	case FieldNumber:
		if KindNumber != n.Value.Kind {
			// A numeric column compared against text: nothing can match, and
			// saying so is better than a cast error mid-scan.
			if OpEquals == n.Op {
				return "FALSE", nil
			}
			return "FALSE", nil
		}
		return "(" + expr + " " + operator + " " + c.bind(n.Value.Num) + ")", nil

	case FieldBool:
		if KindBool != n.Value.Kind {
			return "FALSE", nil
		}
		return "(" + expr + " " + operator + " " + c.bind(n.Value.Bool) + ")", nil

	case FieldTimestamp:
		// Parsed here rather than cast by DuckDB, so the live predicate reads
		// the same instant from the same text, and a value that is not a time
		// is a 400 with a reason rather than a cast error mid-scan.
		at, err := Timestamp(n.Value)
		if nil != err {
			return "", err
		}
		return "(" + expr + " " + operator + " " + c.bind(at) + ")", nil
	}

	/*!
	 * A string column compared with a bare number is read as a number — which
	 * is the common case for a key that went polymorphic, or one only
	 * reachable through raw. try_cast returns NULL rather than failing, so a
	 * row whose value is not numeric simply does not match.
	 *
	 * Equality included. `user_id=12` is the number twelve, as the docs say,
	 * so it matches 12 and "12.0" alike; comparing the text matched only the
	 * first, while the live predicate, which compares numbers, matched both.
	 * Quoting is how you ask for the exact text.
	 */
	if KindNumber == n.Value.Kind {
		return "(try_cast(" + expr + " AS DOUBLE) " + operator + " " + c.bind(n.Value.Num) + ")", nil
	}

	return "(" + expr + " " + operator + " " + c.bind(n.Value.Str) + ")", nil
}

/*!
 * severityCase turns a level name into its rank, inline.
 *
 * A record with no level has no rank: NULL, so it answers no ordered
 * comparison and `-level>=DEBUG` keeps it, exactly as the predicate does.
 * This used to be `CASE upper(level) ... WHEN NULL THEN 0`, and a simple CASE
 * compares with `=`, which is never true of NULL — so a missing level fell
 * through to the unknown rank and `level>=DEBUG` returned every line that had
 * none.
 */
func severityCase(expr string) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "CASE WHEN nullif(%s, '') IS NULL THEN NULL ELSE CASE upper(%s)", expr, expr)

	// Iterating a map would reorder the SQL between runs, which makes query
	// plans uncacheable and diffs unreadable.
	for _, level := range Levels {
		fmt.Fprintf(&sb, " WHEN '%s' THEN %d", level, SeverityRank[level])
	}

	fmt.Fprintf(&sb, " ELSE %d END END", DefaultSeverityRank)

	return sb.String()
}

// The forms a timestamp may be written in, tried in order. A time without a
// zone is UTC, which is what every stored timestamp is. The one with a bare
// hour offset is how DuckDB writes ts as text, so a value the field explorer
// shows can be filtered on as shown.
var timestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05.999999999-07",
	"2006-01-02T15:04",
	"2006-01-02",
}

/*!
 * Timestamp reads a literal compared against ts, time or ingest_ts.
 *
 * One parser for both compilers. The SQL side used to hand the text to
 * DuckDB's cast, which the predicate had no way to reproduce — so the live
 * tail did not answer `ts>2020-01-01` at all — and which ignores a UTC
 * offset rather than applying it. Microseconds, because that is what storage
 * keeps.
 */
func Timestamp(literal Literal) (time.Time, error) {
	for _, layout := range timestampLayouts {
		if at, err := time.Parse(layout, literal.Str); nil == err {
			return at.UTC().Truncate(time.Microsecond), nil
		}
	}

	return time.Time{}, &Error{
		Message: fmt.Sprintf("%q is not a timestamp: write it as 2024-01-02, 2024-01-02T15:04:05 or RFC 3339", literal.Str),
	}
}

// castText makes any field usable with a text operator.
func castText(expr string, kind FieldKind) string {
	if FieldString == kind {
		return expr
	}
	return "CAST(" + expr + " AS VARCHAR)"
}

// escapeLike neutralises the wildcards in a user-supplied substring, so
// searching for "100%" does not match everything starting with 100.
func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}
