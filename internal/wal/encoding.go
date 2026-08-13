/*!
 * The WAL record encoding.
 *
 * Deliberately separate from model.Record's wire format, which is lossy in two
 * ways that do not matter to a browser and matter a great deal here: it rounds
 * timestamps to milliseconds, and it renders a float of 3.0 as `3`, which
 * reads back as an integer. A log that changes your data while replaying it is
 * worse than no log.
 *
 * Fields carry their kind explicitly rather than being recomputed from the
 * payload on replay. That costs a few bytes and buys an important property: a
 * segment means exactly what it meant when it was written, even if the
 * classification rules change underneath it in a later release.
 *
 * Keys are one letter because every record pays for them.
 */

package wal

import (
	"encoding/json"
	"time"

	"github.com/kilianc/rtail/v2/internal/model"
)

type walRecord struct {
	Ts       int64  `json:"t"`
	IngestTs int64  `json:"i"`
	Stream   string `json:"s"`
	Seq      uint64 `json:"q"`

	Level string `json:"l,omitempty"`
	Msg   string `json:"m,omitempty"`
	Host  string `json:"h,omitempty"`
	Port  int    `json:"p,omitempty"`

	Raw  string          `json:"r,omitempty"`
	Type string          `json:"y,omitempty"`
	JSON json.RawMessage `json:"j,omitempty"`

	Fields map[string]walValue `json:"f,omitempty"`
}

type walValue struct {
	Kind  uint8   `json:"k"`
	Bool  bool    `json:"b,omitempty"`
	Int   int64   `json:"i,omitempty"`
	Float float64 `json:"d,omitempty"`
	Str   string  `json:"s,omitempty"`
}

func encode(rec *model.Record) *walRecord {
	out := &walRecord{
		Ts:       rec.Ts.UnixMicro(),
		IngestTs: rec.IngestTs.UnixMicro(),
		Stream:   rec.Stream,
		Seq:      rec.Seq,
		Level:    rec.Level,
		Msg:      rec.Msg,
		Host:     rec.Host,
		Port:     rec.Port,
		Raw:      rec.Raw,
		Type:     rec.Type,
		JSON:     rec.JSON,
	}

	if 0 == len(rec.Fields) {
		return out
	}

	out.Fields = make(map[string]walValue, len(rec.Fields))
	for key, value := range rec.Fields {
		out.Fields[key] = walValue{
			Kind:  uint8(value.Kind),
			Bool:  value.Bool,
			Int:   value.Int,
			Float: value.Float,
			Str:   value.Str,
		}
	}

	return out
}

func (w *walRecord) decode() *model.Record {
	rec := &model.Record{
		Ts:       time.UnixMicro(w.Ts).UTC(),
		IngestTs: time.UnixMicro(w.IngestTs).UTC(),
		Stream:   w.Stream,
		Seq:      w.Seq,
		Level:    w.Level,
		Msg:      w.Msg,
		Host:     w.Host,
		Port:     w.Port,
		Raw:      w.Raw,
		Type:     w.Type,
		JSON:     w.JSON,
	}

	if 0 == len(w.Fields) {
		return rec
	}

	rec.Fields = make(map[string]model.Value, len(w.Fields))
	for key, value := range w.Fields {
		rec.Fields[key] = model.Value{
			Kind:  model.Kind(value.Kind),
			Bool:  value.Bool,
			Int:   value.Int,
			Float: value.Float,
			Str:   value.Str,
		}
	}

	return rec
}
