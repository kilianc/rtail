/*!
 * The search API.
 *
 *   GET /v1/search     matches, paginated
 *   GET /v1/histogram  counts over time, for the chart that is the navigation
 *   GET /v1/fields     top values of a field, for the explorer sidebar
 *   GET /v1/schema     every key ever seen, for autocomplete
 *   POST /v1/sql       the escape hatch
 *
 * All of them take the same query parameters, so a UI that has a time range
 * and a filter can drive the whole surface without special-casing any of it.
 * Every response reports the effective window and what the query implied
 * reading, because a search interface that hides how much work it just did
 * teaches people nothing about why it was slow.
 */

package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/kilianc/rtail/v2/internal/query"
	"github.com/kilianc/rtail/v2/internal/rql"
)

// searchRequest parses the query parameters shared by every search endpoint.
func (s *Server) searchRequest(r *http.Request) (query.Request, error) {
	values := r.URL.Query()

	req := query.Request{
		Query:     values.Get("q"),
		Stream:    values.Get("stream"),
		Ascending: "asc" == values.Get("order"),
	}

	// Anything other than an explicit sql is rQL, so a missing or misspelled
	// lang falls back to the safe, parseable language rather than handing the
	// text to DuckDB.
	if "sql" == values.Get("lang") {
		req.Lang = query.LangSQL
	}

	from, err := parseTime(values.Get("from"))
	if nil != err {
		return req, err
	}
	to, err := parseTime(values.Get("to"))
	if nil != err {
		return req, err
	}

	req.From, req.To = from, to

	if limit := values.Get("limit"); "" != limit {
		n, err := strconv.Atoi(limit)
		if nil != err || n < 1 {
			return req, badRequest("limit must be a positive integer")
		}
		req.Limit = n
	}

	cursor, err := query.ParseCursor(values.Get("cursor"))
	if nil != err {
		return req, badRequest(err.Error())
	}
	req.Cursor = cursor

	return req, nil
}

/*!
 * parseTime accepts an RFC3339 instant, an epoch in milliseconds, or a
 * relative offset like "-6h".
 *
 * The relative form is what the time picker actually sends, and it is
 * deliberately resolved server-side: a browser with a skewed clock asking for
 * "the last six hours" should get the server's six hours, not its own.
 */
func parseTime(value string) (time.Time, error) {
	if "" == value {
		return time.Time{}, nil
	}

	if '-' == value[0] || '+' == value[0] {
		offset, err := time.ParseDuration(value)
		if nil != err {
			return time.Time{}, badRequest("bad relative time " + strconv.Quote(value))
		}
		return time.Now().UTC().Add(offset), nil
	}

	if ts, err := time.Parse(time.RFC3339, value); nil == err {
		return ts.UTC(), nil
	}

	if millis, err := strconv.ParseInt(value, 10, 64); nil == err {
		return time.UnixMilli(millis).UTC(), nil
	}

	return time.Time{}, badRequest("bad time " + strconv.Quote(value))
}

// badRequest is an error the client caused, reported as 400 rather than 500.
type badRequest string

func (b badRequest) Error() string { return string(b) }

// scannedJSON is the "what did that cost" block on every response.
type scannedJSON struct {
	Files  int    `json:"files"`
	Rows   int64  `json:"rows"`
	Bytes  int64  `json:"bytes"`
	Millis int64  `json:"millis"`
	From   string `json:"from"`
	To     string `json:"to"`
}

func scannedOf(s query.Scanned) scannedJSON {
	return scannedJSON{
		Files:  s.Files,
		Rows:   s.Rows,
		Bytes:  s.Bytes,
		Millis: s.Elapsed.Milliseconds(),
		From:   s.From.Format(time.RFC3339Nano),
		To:     s.To.Format(time.RFC3339Nano),
	}
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	if nil == s.opts.Engine {
		s.noEngine(w)
		return
	}

	req, err := s.searchRequest(r)
	if nil != err {
		s.failQuery(w, err)
		return
	}

	result, err := s.opts.Engine.Search(r.Context(), req)
	if nil != err {
		s.failQuery(w, err)
		return
	}

	payload := map[string]any{
		"records": result.Records,
		"scanned": scannedOf(result.Scanned),
	}

	if nil != result.Next {
		payload["cursor"] = result.Next.String()
	}

	// records must be [] rather than null: the webapp iterates it.
	if nil == result.Records {
		payload["records"] = []any{}
	}

	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) histogram(w http.ResponseWriter, r *http.Request) {
	if nil == s.opts.Engine {
		s.noEngine(w)
		return
	}

	req, err := s.searchRequest(r)
	if nil != err {
		s.failQuery(w, err)
		return
	}

	buckets := 60
	if value := r.URL.Query().Get("buckets"); "" != value {
		n, err := strconv.Atoi(value)
		if nil != err || n < 1 || n > 1000 {
			s.failQuery(w, badRequest("buckets must be between 1 and 1000"))
			return
		}
		buckets = n
	}

	series, width, err := s.opts.Engine.Histogram(r.Context(), req, buckets)
	if nil != err {
		s.failQuery(w, err)
		return
	}

	if nil == series {
		series = []query.Bucket{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"buckets":     series,
		"interval_ms": width.Milliseconds(),
	})
}

func (s *Server) fields(w http.ResponseWriter, r *http.Request) {
	if nil == s.opts.Engine {
		s.noEngine(w)
		return
	}

	field := r.URL.Query().Get("field")
	if "" == field {
		s.failQuery(w, badRequest("field is required"))
		return
	}

	req, err := s.searchRequest(r)
	if nil != err {
		s.failQuery(w, err)
		return
	}

	limit := 10
	if value := r.URL.Query().Get("top"); "" != value {
		n, err := strconv.Atoi(value)
		if nil != err || n < 1 || n > 1000 {
			s.failQuery(w, badRequest("top must be between 1 and 1000"))
			return
		}
		limit = n
	}

	values, err := s.opts.Engine.FieldValues(r.Context(), req, field, limit)
	if nil != err {
		s.failQuery(w, err)
		return
	}

	if nil == values {
		values = []query.FieldValue{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"field": field, "values": values})
}

/*!
 * schema serves the key inventory that powers autocomplete.
 *
 * This is what makes the search bar's completion real rather than a guess: it
 * is every key that has actually appeared on this stream, with its type and
 * how often it was seen.
 */
func (s *Server) schema(w http.ResponseWriter, r *http.Request) {
	if nil == s.opts.Catalog {
		s.noEngine(w)
		return
	}

	keys, err := s.opts.Catalog.SchemaKeys(r.Context(), r.URL.Query().Get("stream"))
	if nil != err {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	type fieldJSON struct {
		Name        string `json:"name"`
		Kind        string `json:"kind"`
		Polymorphic bool   `json:"polymorphic,omitempty"`
		Occurrences int64  `json:"occurrences"`
		Stream      string `json:"stream"`
	}

	fields := make([]fieldJSON, 0, len(keys)+len(envelopeFields))

	// The envelope is always available and never appears in schema_keys,
	// but a user typing `lev` expects `level` to complete.
	for _, name := range envelopeFields {
		fields = append(fields, fieldJSON{Name: name, Kind: "envelope"})
	}

	/*!
	 * Keys are stored per stream, so asking across all of them returns the
	 * same name once per stream that ever had it. The explorer wants one entry
	 * per name — a sidebar listing `region` four times is noise, not detail —
	 * so they are merged: occurrences add up, and a key whose type differs
	 * between streams is polymorphic for the same reason it would be within
	 * one.
	 */
	merged := make(map[string]*fieldJSON, len(keys))
	order := make([]string, 0, len(keys))

	for _, key := range keys {
		existing, ok := merged[key.SourceKey]
		if !ok {
			merged[key.SourceKey] = &fieldJSON{
				Name:        key.SourceKey,
				Kind:        key.Kind,
				Polymorphic: key.Polymorphic,
				Occurrences: key.Occurrences,
				Stream:      key.Stream,
			}
			order = append(order, key.SourceKey)
			continue
		}

		existing.Occurrences += key.Occurrences
		if existing.Kind != key.Kind {
			existing.Polymorphic = true
			existing.Kind = "string"
		}
		existing.Polymorphic = existing.Polymorphic || key.Polymorphic
		// No single stream owns it any more.
		existing.Stream = ""
	}

	for _, name := range order {
		fields = append(fields, *merged[name])
	}

	writeJSON(w, http.StatusOK, map[string]any{"fields": fields})
}

var envelopeFields = []string{"ts", "level", "msg", "stream", "host", "raw", "seq"}

/*!
 * sql is the escape hatch: arbitrary read-only DuckDB over a `logs` view.
 *
 * POST rather than GET because a query is a body, not a URL, and because it
 * keeps a long analytical statement out of access logs.
 */
func (s *Server) sql(w http.ResponseWriter, r *http.Request) {
	if nil == s.opts.Engine {
		s.noEngine(w)
		return
	}

	var body struct {
		SQL    string `json:"sql"`
		Stream string `json:"stream"`
		From   string `json:"from"`
		To     string `json:"to"`
		Limit  int    `json:"limit"`
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); nil != err {
		s.failQuery(w, badRequest("body must be JSON with a sql field"))
		return
	}

	if "" == body.SQL {
		s.failQuery(w, badRequest("sql is required"))
		return
	}

	from, err := parseTime(body.From)
	if nil != err {
		s.failQuery(w, err)
		return
	}
	to, err := parseTime(body.To)
	if nil != err {
		s.failQuery(w, err)
		return
	}

	columns, rows, scanned, err := s.opts.Engine.SQL(r.Context(), body.SQL, query.Request{
		Stream: body.Stream,
		From:   from,
		To:     to,
		Limit:  body.Limit,
	})
	if nil != err {
		s.failQuery(w, err)
		return
	}

	if nil == columns {
		columns = []string{}
	}
	if nil == rows {
		rows = [][]any{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"columns": columns,
		"rows":    rows,
		"scanned": scannedOf(scanned),
	})
}

/*!
 * failQuery reports a client mistake as 400 and everything else as 500.
 *
 * A malformed rQL query is the normal state of a search bar, not a server
 * fault, and the position in the message is what lets the UI underline the
 * offending character.
 */
func (s *Server) failQuery(w http.ResponseWriter, err error) {
	payload := map[string]any{"error": err.Error()}
	status := http.StatusInternalServerError

	switch e := err.(type) {
	case badRequest:
		status = http.StatusBadRequest
	case *rql.Error:
		status = http.StatusBadRequest
		payload["position"] = e.Position
		payload["message"] = e.Message
	default:
		s.opts.Log.Error("query failed", "err", err)
		// The underlying engine error is useful and not sensitive — the data
		// directory is the operator's own — but it is verbose, so it goes in
		// a field of its own rather than in the headline.
		payload["error"] = "query failed"
		payload["detail"] = err.Error()
	}

	writeJSON(w, status, payload)
}

// noEngine explains that search needs durable storage rather than 404ing.
func (s *Server) noEngine(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error": "search needs durable storage; start rtail-server with --data",
	})
}
