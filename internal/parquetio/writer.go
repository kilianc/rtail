/*!
 * Writing a batch of records as an L0 Parquet file.
 *
 * L0 files are what a flush produces: small, recent, sorted by seq, and short
 * lived. They are optimised for being written cheaply and read whole, not for
 * being read selectively — that is what compaction produces from them later,
 * with heavier compression, event-time ordering and bloom filters.
 *
 * The result is a plain Parquet file. `duckdb -c "select * from 'x.parquet'"`
 * works on it with rTail uninstalled, which is the whole anti-lock-in
 * argument and worth protecting.
 */

package parquetio

import (
	"context"
	"fmt"
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

	object, err := backend.Create(ctx, name)
	if nil != err {
		return nil, nil, fmt.Errorf("creating %s: %w", name, err)
	}
	defer object.Abort()

	counter := &countingWriter{inner: object}

	sortBy := opts.SortBy
	if 0 == len(sortBy) {
		// Records arrive in seq order, so this costs nothing and makes the
		// file's ordering explicit to any reader.
		sortBy = []string{ColSeq}
	}

	sorting := make([]parquet.SortingColumn, 0, len(sortBy))
	for _, column := range sortBy {
		sorting = append(sorting, parquet.Ascending(column))
	}

	writer := parquet.NewGenericWriter[any](counter,
		schema.Parquet(),
		parquet.Compression(&parquet.Zstd),
		parquet.SortingWriterConfig(parquet.SortingColumns(sorting...)),
	)

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
