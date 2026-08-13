/*!
 * The HTTP surface.
 *
 * v1 fanned lines out over socket.io. v2 uses server-sent events instead: the
 * traffic is one-directional (the only thing the client ever told the server
 * was which stream it wanted, and that is a URL), SSE reconnects on its own,
 * and it drops a 40KB client dependency along with the entire websocket
 * upgrade path. The trade is no binary frames, which log lines are not.
 */

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/kilianc/rtail/v2/internal/catalog"
	"github.com/kilianc/rtail/v2/internal/ingest"
	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/query"
	"github.com/kilianc/rtail/v2/web"
)

// Options configures the server.
type Options struct {
	Store   logstore.Store
	Log     *slog.Logger
	Version string

	// Engine and Catalog are set only when the server was started with
	// --data. Without them the search endpoints report 503 with an
	// explanation rather than 404ing, because "this build cannot search" and
	// "this server is not storing anything" are different problems.
	Engine  *query.Engine
	Catalog *catalog.Catalog

	// Ingest receivers, when set, are mounted and reported by /healthz.
	UDP    *ingest.UDPStats
	HTTPIn *ingest.HTTP
	Syslog *ingest.SyslogStats

	// WebRoot serves the webapp from disk instead of the embedded copy. The
	// asset watcher rewrites bundle.js in place during development, and a
	// binary that embedded it at compile time would never show the change.
	WebRoot string

	// Heartbeat is how often a tail connection emits a comment to keep
	// intermediaries from treating an idle stream as a dead one.
	Heartbeat time.Duration
}

// Server routes the API and the webapp.
type Server struct {
	opts Options
}

// DefaultHeartbeat is comfortably under the 60s idle timeout most proxies use.
const DefaultHeartbeat = 20 * time.Second

// New builds a server.
func New(opts Options) *Server {
	if nil == opts.Log {
		opts.Log = slog.Default()
	}
	if 0 == opts.Heartbeat {
		opts.Heartbeat = DefaultHeartbeat
	}

	return &Server{opts: opts}
}

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/streams", s.streams)
	mux.HandleFunc("GET /v1/tail", s.tail)

	mux.HandleFunc("GET /v1/search", s.search)
	mux.HandleFunc("GET /v1/histogram", s.histogram)
	mux.HandleFunc("GET /v1/fields", s.fields)
	mux.HandleFunc("GET /v1/schema", s.schema)
	mux.HandleFunc("POST /v1/sql", s.sql)

	if nil != s.opts.HTTPIn {
		mux.HandleFunc("POST /v1/ingest", s.opts.HTTPIn.Lines)
		// The path OTLP exporters default to, and our own alias for it.
		mux.HandleFunc("POST /v1/logs", s.opts.HTTPIn.OTLP)
		mux.HandleFunc("POST /v1/otlp/v1/logs", s.opts.HTTPIn.OTLP)
	}

	// Anything else is the webapp. Registered on the bare pattern so the
	// explicit routes above always win.
	if "" != s.opts.WebRoot {
		s.opts.Log.Info("serving webapp from disk", "root", s.opts.WebRoot)
		mux.Handle("/", noCache(http.FileServer(http.Dir(s.opts.WebRoot))))
	} else {
		mux.Handle("/", web.Handler())
	}

	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	streams, err := s.opts.Store.Streams(r.Context())
	if nil != err {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	payload := map[string]any{
		"status":  "ok",
		"version": s.opts.Version,
		"streams": len(streams),
	}

	if nil != s.opts.UDP {
		payload["udp"] = map[string]uint64{
			"received": s.opts.UDP.Received.Load(),
			"invalid":  s.opts.UDP.Invalid.Load(),
			"bytes":    s.opts.UDP.Bytes.Load(),
		}
	}

	if nil != s.opts.HTTPIn {
		stats := s.opts.HTTPIn.Stats()
		payload["http_ingest"] = map[string]uint64{
			"requests": stats.Requests.Load(),
			"received": stats.Received.Load(),
			"rejected": stats.Rejected.Load(),
			"bytes":    stats.Bytes.Load(),
		}
	}

	if nil != s.opts.Syslog {
		payload["syslog"] = map[string]uint64{
			"received": s.opts.Syslog.Received.Load(),
			"invalid":  s.opts.Syslog.Invalid.Load(),
			"bytes":    s.opts.Syslog.Bytes.Load(),
		}
	}

	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) streams(w http.ResponseWriter, r *http.Request) {
	streams, err := s.opts.Store.Streams(r.Context())
	if nil != err {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	if nil == streams {
		streams = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"streams": streams})
}

func (s *Server) fail(w http.ResponseWriter, status int, err error) {
	s.opts.Log.Error("request failed", "status", status, "err", err)
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if nil != err {
		http.Error(w, `{"error":"encoding response"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(body)
}

// noCache keeps the development server from serving a stale bundle, which is
// the single most confusing thing an asset watcher can do to you.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
