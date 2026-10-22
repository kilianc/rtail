/*!
 * Writing a batch of records as an L0 Parquet file.
 *
 * L0 files are what a flush produces: small, recent, sorted by seq, and short
 * lived. They are optimised for being written cheaply and read whole, not for
 * being read selectively — that is what compaction produces from them later,
 * with heavier compression, event-time ordering and bloom filters.
 *
 * The result is a plain Parquet file. `duckdb -c "select * from 'x.parquet'"`
 * works on it with rtail uninstalled, which is the whole anti-lock-in
 * argument and worth protecting.
 */

package parquetio

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
	"github.com/kilianc/rtail/v2/internal/storage"
	"github.com/parquet-go/parquet-go"
)

// Stats describe a written file, and are what the catalog stores so the query
// planner can prune without opening a single Parquet footer.
type Stats struct {
	Rows     int64
	Bytes    int64
	MinTs    time.Time
	MaxTs    time.Time
	MinSeq   uint64
	MaxSeq   uint64
	Columns  []ColumnStats
	Dropped  int
	HasRaw   bool
	Compress string
}

// ColumnStats describe one promoted column in a written file.
type ColumnStats struct {
	Column
	NullCount int64
	// MinValue and MaxValue are set for scalar columns only; JSON and
	// polymorphic columns have no useful ordering.
	MinValue string
	MaxValue string
	HasRange bool
}

// WriteOptions tune a single file.
type WriteOptions struct {
	// KeepRaw controls whether the original line is stored. Dropping it
	// roughly halves the footprint at the cost of making unpromoted keys
	// unqueryable — a retention-policy decision, not a default.
	KeepRaw bool

	/*!
	 * SortBy names the columns rows are ordered by, outermost first. Empty
	 * means seq, which is the order a flush already has.
	 *
	 * This is the single biggest lever on query latency, which is why it is a
	 * per-file decision rather than a constant. Sorting a compacted file by ts
	 * makes time-range pruning exact at row-group granularity instead of
	 * merely likely; adding a low-cardinality leading column (level, service)
	 * turns the most common filter into a row-group skip rather than a scan.
	 *
	 * Envelope columns are named as themselves. A promoted key is named
	 * FieldPrefix + its source key — "a_service.name" — and Write finds the
	 * column it was actually stored in, which mangling and collisions make
	 * impossible to spell from outside.
	 */
	SortBy []string
}

/*!
 * Write serializes records into a new object and returns its stats.
 *
 * Nothing is visible until Commit, and the write is aborted on any error, so a
 * failed flush leaves no file for the catalog to trip over.
 */
func Write(
	ctx context.Context,
	backend storage.Backend,
	name string,
	records []*model.Record,
	opts WriteOptions,
) (*Schema, *Stats, error) {
	if 0 == len(records) {
		return nil, nil, fmt.Errorf("refusing to write an empty file")
	}

	schema := BuildSchema(records)

	/*!
	 * The writer is told the order, and the rows are put in it here, because
	 * telling is all parquet-go's writer does: it records sorting_columns in
	 * the footer and writes rows exactly as it is given them. A reader that
	 * trusts the footer — to merge, to stop a scan early — would otherwise be
	 * trusting whatever order the caller happened to pass.
	 */
	keys := resolveSort(schema, opts)

	records = append([]*model.Record(nil), records...)
	sortRecords(records, keys)

	object, err := backend.Create(ctx, name)
	if nil != err {
		return nil, nil, fmt.Errorf("creating %s: %w", name, err)
	}
	defer object.Abort()

	counter := &countingWriter{inner: object}

	options := []parquet.WriterOption{schema.Parquet(), parquet.Compression(&parquet.Zstd)}

	if 0 != len(keys) {
		sorting := make([]parquet.SortingColumn, 0, len(keys))
		for _, key := range keys {
			sorting = append(sorting, parquet.Ascending(key.column))
		}
		options = append(options, parquet.SortingWriterConfig(parquet.SortingColumns(sorting...)))
	}

	writer := parquet.NewGenericWriter[any](counter, options...)

	stats := &Stats{
		MinSeq:   records[0].Seq,
		MaxSeq:   records[0].Seq,
		MinTs:    records[0].Ts,
		MaxTs:    records[0].Ts,
		HasRaw:   opts.KeepRaw,
		Dropped:  schema.Dropped(),
		Compress: "zstd",
	}

	accumulators := make([]*columnAccumulator, len(schema.Columns()))
	for i, column := range schema.Columns() {
		accumulators[i] = &columnAccumulator{Column: column}
	}

	builder := parquet.NewRowBuilder(schema.Parquet())
	rows := make([]parquet.Row, 0, len(records))

	for _, rec := range records {
		builder.Reset()

		if err := appendEnvelope(builder, schema, rec, opts); nil != err {
			return nil, nil, err
		}

		for i, column := range schema.Columns() {
			value, present := rec.Fields[column.SourceKey]
			if !present || model.KindNull == value.Kind {
				accumulators[i].nulls++
				continue
			}

			index, err := schema.leafIndex(column.Name)
			if nil != err {
				return nil, nil, err
			}

			parquetValue, ok := valueFor(column.Kind, value)
			if !ok {
				accumulators[i].nulls++
				continue
			}

			builder.Add(index, parquetValue)
			accumulators[i].observe(value)
		}

		rows = append(rows, builder.Row())

		if rec.Ts.Before(stats.MinTs) {
			stats.MinTs = rec.Ts
		}
		if rec.Ts.After(stats.MaxTs) {
			stats.MaxTs = rec.Ts
		}
		if rec.Seq < stats.MinSeq {
			stats.MinSeq = rec.Seq
		}
		if rec.Seq > stats.MaxSeq {
			stats.MaxSeq = rec.Seq
		}
	}

	if _, err := writer.WriteRows(rows); nil != err {
		return nil, nil, fmt.Errorf("writing rows: %w", err)
	}

	if err := writer.Close(); nil != err {
		return nil, nil, fmt.Errorf("finalizing parquet: %w", err)
	}

	if err := object.Commit(); nil != err {
		return nil, nil, fmt.Errorf("committing %s: %w", name, err)
	}

	stats.Rows = int64(len(records))
	stats.Bytes = counter.written
	stats.Columns = make([]ColumnStats, len(accumulators))
	for i, acc := range accumulators {
		stats.Columns[i] = acc.stats()
	}

	return schema, stats, nil
}

/*!
 * Order sorts records in place into the order Write would store them in.
 *
 * For callers that derive something from the first record of a file before
 * writing it — compaction names its output that way — so that the record they
 * pick is the one the file actually starts with.
 */
func Order(records []*model.Record, opts WriteOptions) {
	if 0 == len(records) {
		return
	}

	sortRecords(records, resolveSort(BuildSchema(records), opts))
}

/*!
 * sortKey is one column of a file's declared order, resolved against the
 * schema the file really has.
 */
type sortKey struct {
	// column is the physical name, which is what sorting_columns refers to.
	column string
	// kind compares values the way the column stores them: an INT64 column
	// orders 9 before 10, which comparing their text would not.
	kind parquet.Type
	// value is what the record holds in this column, or a null Value.
	value func(*model.Record) parquet.Value
}

/*!
 * resolveSort maps SortBy onto the file's columns.
 *
 * A name with no column in this file — a key that never had a value in the
 * batch, or was dropped past MaxColumns — is left out of the order rather
 * than declared. parquet-go would otherwise record it against column 0, which
 * is a claim that the file is sorted by ts when it is not.
 */
func resolveSort(schema *Schema, opts WriteOptions) []sortKey {
	sortBy := opts.SortBy
	if 0 == len(sortBy) {
		sortBy = []string{ColSeq}
	}

	text := func(field func(*model.Record) string) func(*model.Record) parquet.Value {
		return func(rec *model.Record) parquet.Value {
			if v := field(rec); "" != v {
				return parquet.ByteArrayValue([]byte(v))
			}
			return parquet.Value{}
		}
	}

	keys := make([]sortKey, 0, len(sortBy))

	for _, name := range sortBy {
		var key sortKey

		switch name {
		case ColTs:
			key.value = func(rec *model.Record) parquet.Value { return parquet.Int64Value(rec.Ts.UnixMicro()) }
		case ColIngestTs:
			key.value = func(rec *model.Record) parquet.Value { return parquet.Int64Value(rec.IngestTs.UnixMicro()) }
		case ColSeq:
			key.value = func(rec *model.Record) parquet.Value { return parquet.Int64Value(int64(rec.Seq)) }
		case ColIsJSON:
			key.value = func(rec *model.Record) parquet.Value { return parquet.BooleanValue(rec.IsObject()) }
		case ColStream:
			key.value = func(rec *model.Record) parquet.Value { return parquet.ByteArrayValue([]byte(rec.Stream)) }
		case ColLevel:
			key.value = text(func(rec *model.Record) string { return rec.Level })
		case ColMsg:
			key.value = text(func(rec *model.Record) string { return rec.Msg })
		case ColHost:
			key.value = text(func(rec *model.Record) string { return rec.Host })
		case ColRaw:
			if !opts.KeepRaw {
				continue
			}
			key.value = text(func(rec *model.Record) string { return rec.Raw })

		default:
			if !strings.HasPrefix(name, FieldPrefix) {
				continue
			}

			source := strings.TrimPrefix(name, FieldPrefix)

			found := false
			for _, column := range schema.Columns() {
				if column.SourceKey != source {
					continue
				}

				name, found = column.Name, true
				key.value = func(rec *model.Record) parquet.Value {
					value, present := rec.Fields[column.SourceKey]
					if !present || model.KindNull == value.Kind {
						return parquet.Value{}
					}
					if converted, ok := valueFor(column.Kind, value); ok {
						return converted
					}
					return parquet.Value{}
				}
				break
			}
			if !found {
				continue
			}
		}

		leaf, ok := schema.parquet.Lookup(name)
		if !ok {
			continue
		}

		key.column, key.kind = name, leaf.Node.Type()
		keys = append(keys, key)
	}

	return keys
}

// sortRecords orders records by keys: ascending, nulls last — which is what
// parquet.Ascending declares — and stable, so equal keys keep their order.
func sortRecords(records []*model.Record, keys []sortKey) {
	if 0 == len(keys) || len(records) < 2 {
		return
	}

	type entry struct {
		rec    *model.Record
		values []parquet.Value
	}

	entries := make([]entry, len(records))
	for i, rec := range records {
		values := make([]parquet.Value, len(keys))
		for k, key := range keys {
			values[k] = key.value(rec)
		}
		entries[i] = entry{rec: rec, values: values}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i].values, entries[j].values

		for k, key := range keys {
			switch aNull, bNull := a[k].IsNull(), b[k].IsNull(); {
			case aNull && bNull:
				continue
			case aNull:
				return false
			case bNull:
				return true
			}

			if order := key.kind.Compare(a[k], b[k]); 0 != order {
				return order < 0
			}
		}

		return false
	})

	for i := range entries {
		records[i] = entries[i].rec
	}
}

// appendEnvelope writes the fixed columns for one record.
func appendEnvelope(builder *parquet.RowBuilder, schema *Schema, rec *model.Record, opts WriteOptions) error {
	add := func(name string, value parquet.Value) error {
		index, err := schema.leafIndex(name)
		if nil != err {
			return err
		}
		builder.Add(index, value)
		return nil
	}

	// Timestamps go in as microseconds since the epoch, matching the logical
	// type declared on the column.
	if err := add(ColTs, parquet.Int64Value(rec.Ts.UnixMicro())); nil != err {
		return err
	}
	if err := add(ColIngestTs, parquet.Int64Value(rec.IngestTs.UnixMicro())); nil != err {
		return err
	}
	if err := add(ColStream, parquet.ByteArrayValue([]byte(rec.Stream))); nil != err {
		return err
	}
	if err := add(ColSeq, parquet.Int64Value(int64(rec.Seq))); nil != err {
		return err
	}
	if err := add(ColIsJSON, parquet.BooleanValue(rec.IsObject())); nil != err {
		return err
	}

	// Optional columns are simply not added when empty, which the row builder
	// renders as null rather than as an empty string. The distinction matters:
	// "this line had no level" is not "this line's level was ''".
	if "" != rec.Level {
		if err := add(ColLevel, parquet.ByteArrayValue([]byte(rec.Level))); nil != err {
			return err
		}
	}
	if "" != rec.Msg {
		if err := add(ColMsg, parquet.ByteArrayValue([]byte(rec.Msg))); nil != err {
			return err
		}
	}
	if "" != rec.Host {
		if err := add(ColHost, parquet.ByteArrayValue([]byte(rec.Host))); nil != err {
			return err
		}
	}
	if opts.KeepRaw && "" != rec.Raw {
		if err := add(ColRaw, parquet.ByteArrayValue([]byte(rec.Raw))); nil != err {
			return err
		}
	}

	return nil
}

/*!
 * valueFor converts a model value to the column's declared type.
 *
 * The column type was resolved across the whole batch, so a value may need
 * widening to reach it — an int in a column that widened to double, or
 * anything at all in a column that went polymorphic and became text. Returns
 * false when the value cannot be represented, which is recorded as a null
 * rather than failing the flush: one weird value must not cost a whole batch.
 */
func valueFor(kind model.Kind, value model.Value) (parquet.Value, bool) {
	switch kind {
	case model.KindBool:
		if model.KindBool != value.Kind {
			return parquet.Value{}, false
		}
		return parquet.BooleanValue(value.Bool), true

	case model.KindInt:
		if model.KindInt != value.Kind {
			return parquet.Value{}, false
		}
		return parquet.Int64Value(value.Int), true

	case model.KindFloat:
		switch value.Kind {
		case model.KindFloat:
			return parquet.DoubleValue(value.Float), true
		case model.KindInt:
			return parquet.DoubleValue(float64(value.Int)), true
		}
		return parquet.Value{}, false

	case model.KindJSON:
		if model.KindJSON != value.Kind {
			// A key that is usually an object and occasionally a scalar: keep
			// the scalar as its JSON representation so nothing is lost.
			return parquet.ByteArrayValue([]byte(jsonLiteral(value))), true
		}
		return parquet.ByteArrayValue([]byte(value.Str)), true

	default:
		return parquet.ByteArrayValue([]byte(value.Text())), true
	}
}

// jsonLiteral renders a scalar as valid JSON text, for the mixed-kind case
// above where the column is JSON but this particular value is not.
func jsonLiteral(value model.Value) string {
	switch value.Kind {
	case model.KindString:
		encoded, err := value.MarshalJSON()
		if nil != err {
			return `""`
		}
		return string(encoded)
	case model.KindNull:
		return "null"
	default:
		return value.Text()
	}
}

/*!
 * columnAccumulator collects per-column statistics as rows are built.
 *
 * Parquet already stores min/max per column chunk, but reading them back means
 * opening the footer. Keeping them in the catalog is what lets the planner
 * prune a thousand files with one indexed SQLite query.
 */
type columnAccumulator struct {
	Column
	nulls int64
	min   model.Value
	max   model.Value
	seen  bool
}

func (a *columnAccumulator) observe(value model.Value) {
	if model.KindJSON == a.Kind || a.Polymorphic {
		return
	}

	if !a.seen {
		a.min, a.max, a.seen = value, value, true
		return
	}

	if less(value, a.min) {
		a.min = value
	}
	if less(a.max, value) {
		a.max = value
	}
}

func (a *columnAccumulator) stats() ColumnStats {
	out := ColumnStats{Column: a.Column, NullCount: a.nulls}

	if a.seen {
		out.MinValue, out.MaxValue, out.HasRange = a.min.Text(), a.max.Text(), true
	}

	return out
}

// less orders two values of the same column. Only ever called for scalar,
// non-polymorphic columns, so the kinds agree apart from int/float widening.
func less(a, b model.Value) bool {
	switch {
	case model.KindInt == a.Kind && model.KindInt == b.Kind:
		return a.Int < b.Int
	case model.KindBool == a.Kind && model.KindBool == b.Kind:
		return !a.Bool && b.Bool
	case (model.KindFloat == a.Kind || model.KindInt == a.Kind) &&
		(model.KindFloat == b.Kind || model.KindInt == b.Kind):
		return numeric(a) < numeric(b)
	default:
		return a.Text() < b.Text()
	}
}

func numeric(v model.Value) float64 {
	if model.KindInt == v.Kind {
		return float64(v.Int)
	}
	return v.Float
}

// countingWriter measures the compressed size without a second stat call.
type countingWriter struct {
	inner   storage.Writer
	written int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.inner.Write(p)
	c.written += int64(n)
	return n, err
}
