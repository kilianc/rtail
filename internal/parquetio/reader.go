/*!
 * Reading records back out of a Parquet file.
 *
 * P2 replaces this with DuckDB for anything resembling a query — predicate
 * pushdown, aggregation, joins, union-by-name across heterogeneous schemas.
 * What stays useful here is the narrow case DuckDB is overkill for: pulling
 * the tail of a file back so that a restarted server has a backlog to show,
 * and reading a file back in tests to prove the writer round-trips.
 *
 * It reads the schema from the file rather than assuming the writer's, because
 * files written weeks apart have different columns and neither one is wrong.
 */

package parquetio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/storage"
	"github.com/parquet-go/parquet-go"
)

// leafInfo describes one column as found in a file being read.
type leafInfo struct {
	name string
	kind parquet.Kind
}

/*!
 * Read returns records from an object, newest last.
 *
 * limit caps the number returned, keeping the most recent — 0 means all of
 * them. Rows are read in file order, which for an L0 file is seq order.
 */
func Read(ctx context.Context, backend storage.Backend, name string, limit int) ([]*model.Record, error) {
	reader, err := backend.Open(ctx, name)
	if nil != err {
		return nil, fmt.Errorf("opening %s: %w", name, err)
	}
	defer reader.Close()

	info, err := backend.Stat(ctx, name)
	if nil != err {
		return nil, fmt.Errorf("stat %s: %w", name, err)
	}

	file, err := parquet.OpenFile(reader, info.Size)
	if nil != err {
		return nil, fmt.Errorf("reading parquet %s: %w", name, err)
	}

	schema := file.Schema()

	leaves := make(map[int]leafInfo)
	for _, path := range schema.Columns() {
		column, ok := schema.Lookup(path...)
		if !ok || 1 != len(path) {
			continue
		}

		leaves[column.ColumnIndex] = leafInfo{
			name: path[0],
			kind: column.Node.Type().Kind(),
		}
	}

	var records []*model.Record

	for _, group := range file.RowGroups() {
		rows := group.Rows()

		buffer := make([]parquet.Row, 256)
		for {
			n, err := rows.ReadRows(buffer)

			for i := range n {
				records = append(records, decodeRow(buffer[i], leaves))
			}

			if nil != err {
				break
			}
			if 0 == n {
				break
			}
		}

		rows.Close()
	}

	if limit > 0 && len(records) > limit {
		records = records[len(records)-limit:]
	}

	return records, nil
}

/*!
 * looksLikeJSON classifies a string value as a nested document.
 *
 * The writer stores nested values as plain UTF8 rather than annotating them
 * with the JSON logical type — see leafFor in schema.go for why — so there is
 * no annotation to read back. The catalog knows each key's real kind, but this
 * reader deliberately works without one: it is also what lets a bare directory
 * of Parquet files be read by anything.
 *
 * The check is cheap and conservative. A log field whose value is the literal
 * text "{}" is indistinguishable from an empty object, and treating it as the
 * object it almost certainly was is the better guess.
 */
func looksLikeJSON(value []byte) bool {
	trimmed := bytes.TrimSpace(value)

	if len(trimmed) < 2 {
		return false
	}
	if '{' != trimmed[0] && '[' != trimmed[0] {
		return false
	}

	return json.Valid(trimmed)
}

/*!
 * decodeRow rebuilds a record from one Parquet row.
 *
 * Values carry their own column index, so a row is walked rather than indexed
 * — which is also what makes this tolerant of files whose columns are in a
 * different order, or absent entirely.
 */
func decodeRow(row parquet.Row, leaves map[int]leafInfo) *model.Record {
	rec := &model.Record{Fields: map[string]model.Value{}}

	for _, value := range row {
		leaf, ok := leaves[value.Column()]
		if !ok || value.IsNull() {
			continue
		}

		switch leaf.name {
		case ColTs:
			rec.Ts = time.UnixMicro(value.Int64()).UTC()
		case ColIngestTs:
			rec.IngestTs = time.UnixMicro(value.Int64()).UTC()
		case ColStream:
			rec.Stream = string(value.ByteArray())
		case ColSeq:
			rec.Seq = uint64(value.Int64())
		case ColLevel:
			rec.Level = string(value.ByteArray())
		case ColMsg:
			rec.Msg = string(value.ByteArray())
		case ColHost:
			rec.Host = string(value.ByteArray())
		case ColRaw:
			rec.Raw = string(value.ByteArray())
		case ColIsJSON:
			if value.Boolean() {
				rec.Type = "object"
			} else {
				rec.Type = "string"
			}

		default:
			if !strings.HasPrefix(leaf.name, FieldPrefix) {
				continue
			}
			rec.Fields[strings.TrimPrefix(leaf.name, FieldPrefix)] = decodeValue(value, leaf)
		}
	}

	// The payload is reconstructed from raw rather than stored twice. A record
	// read back from disk with raw dropped by a retention policy still renders
	// — it just renders as its message.
	if "object" == rec.Type && "" != rec.Raw && json.Valid([]byte(rec.Raw)) {
		rec.JSON = json.RawMessage(rec.Raw)
	} else if "object" == rec.Type {
		rec.Type = "string"
	}

	if "" == rec.Type {
		rec.Type = "string"
	}

	return rec
}

func decodeValue(value parquet.Value, leaf leafInfo) model.Value {
	switch leaf.kind {
	case parquet.Boolean:
		return model.Bool(value.Boolean())
	case parquet.Int64:
		return model.Int(value.Int64())
	case parquet.Double:
		return model.Float(value.Double())
	case parquet.ByteArray:
		text := value.ByteArray()
		if looksLikeJSON(text) {
			return model.JSON(string(text))
		}
		return model.Str(string(text))
	default:
		return model.Null()
	}
}

/*!
 * SourceKey recovers the original JSON key from a physical column name.
 *
 * This is only correct for keys that needed no mangling. The catalog is the
 * authority — it stores the real key alongside the column — and this exists
 * for the read path, which has a file but not necessarily a catalog row.
 */
func SourceKey(column string) string {
	return strings.TrimPrefix(column, FieldPrefix)
}
