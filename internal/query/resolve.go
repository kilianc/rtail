/*!
 * Field resolution.
 *
 * A user typing `user_id=42` should never have to know whether that key was
 * promoted to a column in every file being read, some of them, or none. The
 * resolver answers that per query, from the catalog, and emits the cheapest
 * expression that is still correct:
 *
 *     every file has a_foo   →  a_foo
 *     some files have it     →  coalesce(a_foo, try_cast(raw ->> '$.foo' AS T))
 *     no file has it         →  try_cast(raw ->> '$.foo' AS T)
 *     no file has it or raw  →  unresolvable; the comparison folds to FALSE
 *
 * Emitting the bare column when we can is not a micro-optimisation. It is the
 * difference between a scan that reads one dictionary-encoded column and one
 * that decompresses every raw byte in the range — which on a day of logs is
 * the difference between tens of milliseconds and tens of seconds.
 */

package query

import (
	"fmt"
	"slices"
	"strings"

	"github.com/kilianc/rtail/v2/internal/catalog"
	"github.com/kilianc/rtail/v2/internal/parquetio"
	"github.com/kilianc/rtail/v2/internal/rql"
)

/*!
 * Envelope fields are resolved directly and never fall back to raw.
 *
 * `msg` maps to the extracted message column rather than to a payload key
 * called msg, because the normalizer already lifted that one — and the live
 * predicate agrees, which is what keeps the two paths honest.
 */
var envelope = map[string]struct {
	column string
	kind   rql.FieldKind
}{
	"ts":        {parquetio.ColTs, rql.FieldTimestamp},
	"time":      {parquetio.ColTs, rql.FieldTimestamp},
	"ingest_ts": {parquetio.ColIngestTs, rql.FieldTimestamp},
	"stream":    {parquetio.ColStream, rql.FieldString},
	"seq":       {parquetio.ColSeq, rql.FieldNumber},
	"level":     {parquetio.ColLevel, rql.FieldSeverity},
	"severity":  {parquetio.ColLevel, rql.FieldSeverity},
	"msg":       {parquetio.ColMsg, rql.FieldString},
	"message":   {parquetio.ColMsg, rql.FieldString},
	"host":      {parquetio.ColHost, rql.FieldString},
	"raw":       {parquetio.ColRaw, rql.FieldString},
	"is_json":   {parquetio.ColIsJSON, rql.FieldBool},
}

// columnFacts is what the catalog knows about one source key across the files
// a query will read.
type columnFacts struct {
	column string
	kind   string
	// present counts the files that have this column; missing counts those
	// that do not. The pair is what decides whether a coalesce is needed.
	present int
	missing int
	// polymorphic anywhere means the key cannot be trusted to one type.
	polymorphic bool
}

/*!
 * Resolver answers field lookups for one query's file set.
 *
 * Built once per query rather than per comparison, because the interesting
 * facts — does every file have this column, do any of them keep raw — are
 * properties of the set, not of a field.
 */
type Resolver struct {
	facts   map[string]*columnFacts
	files   int
	withRaw int
}

var _ rql.Resolver = (*Resolver)(nil)

// NewResolver builds a resolver from the pruned files and their columns.
func NewResolver(files []catalog.File, columns map[int64][]catalog.Column) *Resolver {
	r := &Resolver{facts: map[string]*columnFacts{}, files: len(files)}

	// owners maps each column name, folded, to the first key seen using it;
	// shared names the keys that met another key there. See below.
	owners := map[string]string{}
	shared := map[string]bool{}

	for _, file := range files {
		if file.HasRaw {
			r.withRaw++
		}

		for _, column := range columns[file.ID] {
			folded := strings.ToLower(column.Name)
			if owner, ok := owners[folded]; !ok {
				owners[folded] = column.SourceKey
			} else if owner != column.SourceKey {
				shared[owner], shared[column.SourceKey] = true, true
			}

			facts, ok := r.facts[column.SourceKey]
			if !ok {
				facts = &columnFacts{column: column.Name, kind: column.Kind}
				r.facts[column.SourceKey] = facts
			}

			facts.present++

			// A key that is an int in one file and a string in another cannot
			// be read as either without a cast.
			if column.Polymorphic || (facts.kind != column.Kind) {
				facts.polymorphic = true
				facts.kind = "string"
			}

			// Two keys mangling to different column names across files would
			// make a bare reference ambiguous; fall back to raw for those.
			if facts.column != column.Name {
				facts.column = ""
			}
		}
	}

	/*!
	 * Files that lack a key still count, because that is what forces the
	 * coalesce — and every one of them counts, not only those after the key
	 * was first seen. Counting inside the loop missed the files read before
	 * it, and the planner hands them over oldest first: an older file that
	 * kept the key only in raw resolved to the bare column, which reads NULL
	 * there, and its records silently stopped matching.
	 */
	for _, facts := range r.facts {
		facts.missing = r.files - facts.present
	}

	/*!
	 * A column name that answers for more than one key reads as neither.
	 *
	 * DuckDB matches identifiers without regard to case, so "User" and
	 * "user", stored as a_User and a_user, are one column to it: inside a
	 * file it binds both names to whichever comes first, and across files
	 * union_by_name folds them together. `user=alice` read bob's column and
	 * `id=bob` found the record that had said "ID". Two keys that mangle to
	 * the very same name in different files — "a.b" here, "a-b" there — fold
	 * the same way. raw still tells them apart, so it is what such a key is
	 * read from; ToLower is enough, because a column name is ASCII.
	 */
	for key := range shared {
		r.facts[key].column = ""
	}

	return r
}

/*!
 * Resolve maps a source key onto a SQL expression.
 */
func (r *Resolver) Resolve(field string) (string, rql.FieldKind, bool) {
	if fixed, ok := envelope[field]; ok {
		return fixed.column, fixed.kind, true
	}

	// A dotted path: the head may be a promoted JSON column, in which case the
	// tail is a path into it. Otherwise the whole thing indexes into raw.
	if head, tail, dotted := strings.Cut(field, "."); dotted {
		if facts, ok := r.facts[head]; ok && "" != facts.column && "json" == facts.kind {
			expr := fmt.Sprintf("%s ->> %s", facts.column, quote(jsonPath(tail)))

			if facts.missing > 0 && r.withRaw > 0 {
				expr = fmt.Sprintf("coalesce(%s, %s)", expr, rawPath(field))
			}

			return expr, rql.FieldString, true
		}
	}

	facts, known := r.facts[field]

	if !known {
		if 0 == r.withRaw {
			return "", rql.FieldString, false
		}
		return rawPath(field), rql.FieldString, true
	}

	kind := kindOf(facts.kind)

	// The column name differs between files, so there is no single column to
	// name. raw is the only consistent way to read it.
	if "" == facts.column {
		if 0 == r.withRaw {
			return "", kind, false
		}
		return rawPath(field), rql.FieldString, true
	}

	// Every file has it: the cheap path.
	if 0 == facts.missing || 0 == r.withRaw {
		return facts.column, kind, true
	}

	// Some files have it and raw is available in the rest.
	return fmt.Sprintf("coalesce(%s, %s)", facts.column, castFromRaw(field, kind)), kind, true
}

// Kind reports a field's resolved type without building an expression, for the
// API's schema endpoint.
func (r *Resolver) Kind(field string) rql.FieldKind {
	if fixed, ok := envelope[field]; ok {
		return fixed.kind
	}
	if facts, ok := r.facts[field]; ok {
		return kindOf(facts.kind)
	}
	return rql.FieldString
}

func kindOf(kind string) rql.FieldKind {
	switch kind {
	case "int", "float":
		return rql.FieldNumber
	case "boolean":
		return rql.FieldBool
	case "json":
		return rql.FieldJSON
	default:
		return rql.FieldString
	}
}

/*!
 * rawPath extracts a key from the original line.
 *
 * The json_valid guard is not optional. Most log lines are not JSON at all —
 * `raw` holds whatever the process printed — and DuckDB's ->> raises on a
 * malformed document rather than returning NULL. Without the guard, a single
 * plain-text line in the range fails the entire query, which is the state
 * every mixed stream is in.
 */
func rawPath(field string) string {
	extract := fmt.Sprintf("%s ->> %s", parquetio.ColRaw, quote(jsonPath(field)))

	return fmt.Sprintf("CASE WHEN json_valid(%s) THEN %s END", parquetio.ColRaw, extract)
}

/*!
 * jsonPath writes a dotted field as a DuckDB JSON path, every key quoted.
 *
 * A key is any string JSON allows, and DuckDB parses a path when it binds
 * the statement: `$.user[email]` or `$.x.` is a syntax error that fails the
 * whole query — the search, and the sidebar count of every other field
 * asked for beside it. Quoted, a segment is a key, escaped the way DuckDB
 * unescapes it: `\\` and `\"`.
 *
 * Except two that quoting cannot name. DuckDB rejects `$.""`, and reads
 * `$."*"` as a wildcard all the same, which returns a list. A path with
 * either is a JSON pointer instead, where the empty key is nothing between
 * two slashes, `*` is only a star, and `~` and `/` are all that is escaped.
 */
func jsonPath(field string) string {
	segments := strings.Split(field, ".")

	if slices.Contains(segments, "") || slices.Contains(segments, "*") {
		escape := strings.NewReplacer("~", "~0", "/", "~1")
		for i, segment := range segments {
			segments[i] = escape.Replace(segment)
		}
		return "/" + strings.Join(segments, "/")
	}

	escape := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	for i, segment := range segments {
		segments[i] = `"` + escape.Replace(segment) + `"`
	}
	return "$." + strings.Join(segments, ".")
}

// castFromRaw reads a key out of raw as the type the column has, so the
// coalesce has one type rather than two.
func castFromRaw(field string, kind rql.FieldKind) string {
	switch kind {
	case rql.FieldNumber:
		return fmt.Sprintf("try_cast(%s AS DOUBLE)", rawPath(field))
	case rql.FieldBool:
		return fmt.Sprintf("try_cast(%s AS BOOLEAN)", rawPath(field))
	default:
		return rawPath(field)
	}
}

/*!
 * quote renders a SQL string literal.
 *
 * Only ever used for JSON paths built from a field name, never for user
 * values — those are bound as parameters by the rQL compiler. It exists
 * because a path cannot be a bind parameter in `->>`.
 */
func quote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", "''") + "'"
}
