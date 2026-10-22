/*!
 * Building a Parquet schema from a batch of records.
 *
 * This is docs/proposal-logging-system.md §3 made real: a fixed envelope that
 * is always present and always the same type, plus one column per root-level
 * key the batch happened to contain.
 *
 * Schemas are derived per file, not globally. A file is one flush — minutes of
 * data — so its key diversity is bounded and inference converges fast. Two
 * files disagreeing about a key's type is expected and fine: the reader
 * reconciles by name, and compaction resolves the disagreement permanently by
 * widening to the common supertype.
 */

package parquetio

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/parquet-go/parquet-go"
)

// Envelope column names. These are reserved: a promoted key can never collide
// with one, because promoted keys all carry the prefix below.
const (
	ColTs       = "ts"
	ColIngestTs = "ingest_ts"
	ColStream   = "stream"
	ColSeq      = "seq"
	ColLevel    = "level"
	ColMsg      = "msg"
	ColHost     = "host"
	ColIsJSON   = "is_json"
	ColRaw      = "raw"

	// FieldPrefix namespaces promoted keys away from the envelope. A payload
	// with its own "ts" key becomes a_ts and does not fight the real one.
	FieldPrefix = "a_"
)

// MaxColumns caps how many keys a single file will promote. Past this the
// remainder stay reachable through raw — a pathological producer emitting a
// unique key per line must not be able to create a 50,000-column file.
const MaxColumns = 512

// Column describes one promoted field in a written file.
type Column struct {
	// Name is the physical Parquet column, e.g. a_user_id.
	Name string
	// SourceKey is the original JSON key, e.g. user_id — or "http.status-code",
	// which the UI must display and autocomplete even though the column is
	// called a_http_status_code.
	SourceKey string
	Kind      model.Kind
	// Polymorphic records that the batch disagreed about this key's type and
	// it was widened to string.
	Polymorphic bool
}

// Schema is a Parquet schema plus the mapping back to source keys.
type Schema struct {
	parquet *parquet.Schema
	columns []Column
	// leaf maps a physical column name to its leaf index, which is what
	// RowBuilder addresses.
	leaf map[string]int
	// dropped counts keys that did not fit under MaxColumns.
	dropped int
}

// Parquet exposes the underlying schema.
func (s *Schema) Parquet() *parquet.Schema { return s.parquet }

// Columns lists the promoted fields, sorted by name.
func (s *Schema) Columns() []Column { return s.columns }

// Dropped counts root keys left out because the file hit MaxColumns.
func (s *Schema) Dropped() int { return s.dropped }

/*!
 * kindSet accumulates the kinds observed for one key across a batch.
 *
 * Widening happens once at the end rather than pairwise as values arrive, so
 * the result does not depend on the order records were seen in — which matters
 * because two identical batches must produce identical schemas.
 */
type kindSet struct {
	seen  map[model.Kind]bool
	count int
}

func (k *kindSet) add(kind model.Kind) {
	if nil == k.seen {
		k.seen = make(map[model.Kind]bool, 4)
	}
	k.seen[kind] = true
	if model.KindNull != kind {
		k.count++
	}
}

/*!
 * resolve picks the column type for a key, per §3.2.
 *
 * Returns false when the key was only ever null in this batch: there is no
 * type to give it, and a column of nothing but nulls is pure overhead. The
 * catalog still records that the key was seen, so autocomplete knows about it.
 */
func (k *kindSet) resolve() (model.Kind, bool, bool) {
	if 0 == k.count {
		return model.KindNull, false, false
	}

	kinds := make([]model.Kind, 0, len(k.seen))
	for kind := range k.seen {
		if model.KindNull != kind {
			kinds = append(kinds, kind)
		}
	}

	if 1 == len(kinds) {
		return kinds[0], false, true
	}

	// Integers seen alongside floats widen to float rather than to string:
	// a count that is sometimes written 1 and sometimes 1.5 is still a number,
	// and turning it into text would make range filters stop working.
	onlyNumeric := true
	for _, kind := range kinds {
		if model.KindInt != kind && model.KindFloat != kind {
			onlyNumeric = false
			break
		}
	}
	if onlyNumeric {
		return model.KindFloat, false, true
	}

	// Anything else genuinely mixed becomes text, and is flagged so the UI can
	// warn rather than silently mis-cast.
	return model.KindString, true, true
}

/*!
 * BuildSchema derives a schema from a batch of records.
 *
 * Keys are ranked by how often they appear so that, if the batch exceeds
 * MaxColumns, the ones that survive are the ones queries are most likely to
 * touch. Ties break on name so the result is deterministic.
 */
func BuildSchema(records []*model.Record) *Schema {
	observed := make(map[string]*kindSet)

	for _, rec := range records {
		for key, value := range rec.Fields {
			set, ok := observed[key]
			if !ok {
				set = &kindSet{}
				observed[key] = set
			}
			set.add(value.Kind)
		}
	}

	type candidate struct {
		key         string
		kind        model.Kind
		polymorphic bool
		count       int
	}

	candidates := make([]candidate, 0, len(observed))
	for key, set := range observed {
		kind, polymorphic, ok := set.resolve()
		if !ok {
			continue
		}
		candidates = append(candidates, candidate{key, kind, polymorphic, set.count})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].count != candidates[j].count {
			return candidates[i].count > candidates[j].count
		}
		return candidates[i].key < candidates[j].key
	})

	dropped := 0
	if len(candidates) > MaxColumns {
		dropped = len(candidates) - MaxColumns
		candidates = candidates[:MaxColumns]
	}

	// The envelope. Only ts, ingest_ts, stream and seq are required: a line
	// with no level, no message and no host is perfectly normal.
	group := parquet.Group{
		ColTs:       parquet.Timestamp(parquet.Microsecond),
		ColIngestTs: parquet.Timestamp(parquet.Microsecond),
		ColStream:   parquet.Encoded(parquet.String(), &parquet.RLEDictionary),
		ColSeq:      parquet.Int(64),
		ColLevel:    parquet.Optional(parquet.Encoded(parquet.String(), &parquet.RLEDictionary)),
		ColMsg:      parquet.Optional(parquet.String()),
		ColHost:     parquet.Optional(parquet.Encoded(parquet.String(), &parquet.RLEDictionary)),
		ColIsJSON:   parquet.Leaf(parquet.BooleanType),
		ColRaw:      parquet.Optional(parquet.String()),
	}

	taken := map[string]bool{}
	for name := range group {
		taken[name] = true
	}

	columns := make([]Column, 0, len(candidates))

	for _, c := range candidates {
		name := columnName(c.key, taken)
		taken[name] = true

		group[name] = parquet.Optional(leafFor(c.kind))
		columns = append(columns, Column{
			Name:        name,
			SourceKey:   c.key,
			Kind:        c.kind,
			Polymorphic: c.polymorphic,
		})
	}

	sort.Slice(columns, func(i, j int) bool { return columns[i].Name < columns[j].Name })

	schema := parquet.NewSchema("rtail_log", group)

	leaf := make(map[string]int, len(group))
	for _, path := range schema.Columns() {
		if column, ok := schema.Lookup(path...); ok && 1 == len(path) {
			leaf[path[0]] = column.ColumnIndex
		}
	}

	return &Schema{parquet: schema, columns: columns, leaf: leaf, dropped: dropped}
}

// leafFor maps an inferred kind onto a Parquet leaf node.
func leafFor(kind model.Kind) parquet.Node {
	switch kind {
	case model.KindBool:
		return parquet.Leaf(parquet.BooleanType)
	case model.KindInt:
		return parquet.Int(64)
	case model.KindFloat:
		return parquet.Leaf(parquet.DoubleType)
	case model.KindJSON:
		/*!
		 * Nested objects and arrays keep their original JSON text — but as a
		 * plain UTF8 column, deliberately *not* annotated with the JSON
		 * logical type.
		 *
		 * The annotation would be self-describing and is the obvious choice.
		 * It is not used because readers disagree about it: DuckDB's embedded
		 * build validates a JSON-annotated column at scan time and mis-parses
		 * a dictionary-encoded page, reading every distinct value in the page
		 * as one document and failing the whole query. The standalone CLI of
		 * the same version reads the identical file without complaint.
		 *
		 * A file that only some readers can read is worse than a file that
		 * describes itself less precisely, because "any tool reads these"
		 * is the entire point. The text is identical either way, `->>` works
		 * on VARCHAR, and the catalog records the key's real kind — so
		 * nothing downstream loses information.
		 */
		return parquet.String()
	default:
		return parquet.String()
	}
}

/*!
 * columnName mangles a JSON key into a column name.
 *
 * JSON keys can be anything at all — dots, dashes, spaces, emoji, the empty
 * string. The mangled name is what goes on disk; the catalog keeps the
 * original, so "http.status-code" still displays and autocompletes correctly
 * even though the column is a_http_status_code.
 */
func columnName(key string, taken map[string]bool) string {
	var sb strings.Builder
	sb.WriteString(FieldPrefix)

	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', '_' == r:
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}

	base := sb.String()

	// Every character was rejected, or the key was empty.
	if FieldPrefix == base {
		base = FieldPrefix + "field"
	}

	// Two different keys can mangle to the same name ("a.b" and "a-b"). The
	// loser gets a numeric suffix rather than silently overwriting.
	name := base
	for i := 2; taken[name]; i++ {
		name = base + "_" + strconv.Itoa(i)
	}

	return name
}

// leafIndex resolves a physical column name to its leaf index.
func (s *Schema) leafIndex(name string) (int, error) {
	index, ok := s.leaf[name]
	if !ok {
		return 0, fmt.Errorf("no column %q in schema", name)
	}
	return index, nil
}
