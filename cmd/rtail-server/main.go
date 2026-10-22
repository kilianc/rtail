/*!
 * rtail-server — the v2 server.
 *
 * UDP in on the frozen v1 wire format, plus the HTTP, OTLP and syslog
 * receivers; SSE out to the embedded webapp. With --data every record is also
 * written to Parquet and becomes searchable; without it this is the v1
 * in-memory ring and nothing touches disk.
 */

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/kilianc/rtail/v2/internal/api"
	"github.com/kilianc/rtail/v2/internal/auth"
	"github.com/kilianc/rtail/v2/internal/catalog"
	"github.com/kilianc/rtail/v2/internal/compact"
	"github.com/kilianc/rtail/v2/internal/config"
	"github.com/kilianc/rtail/v2/internal/ingest"
	"github.com/kilianc/rtail/v2/internal/logstore"
	"github.com/kilianc/rtail/v2/internal/query"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "2.0.0-dev"

func main() {
	if err := run(); nil != err {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "rtail-server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Parse(os.Args[1:], version, os.Stderr)
	if nil != err {
		return err
	}

	level := slog.LevelInfo
	if cfg.Verbose {
		level = slog.LevelDebug
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// Ctrl-C and SIGTERM both unwind everything below through this context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	/*!
	 * Storage.
	 *
	 * Without --data the store is the in-memory ring, which is v1's behaviour
	 * and stays the zero-configuration mode: one command, nothing to manage,
	 * nothing left behind. With it, records are durable — the WAL is replayed
	 * before this returns, so the server never starts serving with unrecovered
	 * data still on disk.
	 */
	var (
		store  logstore.Store
		engine *query.Engine
		cat    *catalog.Catalog
		// compacted closes when the compactor has stopped, if there is one.
		compacted chan struct{}
	)

	if "" == cfg.DataDir {
		store = logstore.NewMemory(cfg.Backlog)
		log.Info("running in memory; pass --data to keep and search logs across restarts")
	} else {
		durable, err := logstore.OpenDurable(ctx, cfg.DataDir, logstore.DurableOptions{
			Backlog: cfg.Backlog,
			KeepRaw: true,
			Log:     log,
		})
		if nil != err {
			return fmt.Errorf("opening the data directory: %w", err)
		}

		store, cat = durable, durable.Catalog()

		engine, err = query.Open(cat, durable.Backend(), query.Limits{})
		if nil != err {
			durable.Close()
			return fmt.Errorf("opening the query engine: %w", err)
		}
		defer engine.Close()

		/*!
		 * Compaction runs in the background for the life of the process.
		 *
		 * Without it, files accumulate at one per flush per stream — thousands
		 * a day — and every query pays for all of them. It is an optimisation
		 * rather than a correctness requirement, so a failed pass is logged and
		 * the loop continues; --compact-interval 0 turns it off entirely.
		 */
		compactor := compact.New(cat, durable.Backend(), compact.Options{
			ClusterBy: cfg.ClusterBy,
			KeepRaw:   true,
			Retention: compact.Retention{
				DeleteAfter:     cfg.Retention,
				DropRawAfter:    cfg.RetentionRaw,
				MinLevelAfter:   cfg.DownsampleLevel,
				DownsampleAfter: cfg.DownsampleAfter,
			},
			Log: log,
		})

		compacted = make(chan struct{})
		go func() {
			defer close(compacted)
			compactor.Run(ctx, cfg.CompactInterval)
		}()

		log.Info("storing logs as parquet",
			"data", cfg.DataDir,
			"compact_every", cfg.CompactInterval,
			"retention", cfg.Retention,
			"retention_raw", cfg.RetentionRaw)
	}

	defer store.Close()

	/*!
	 * The compactor stops before the store and the engine close, not after.
	 *
	 * It merges through the catalog and the storage backend, and nothing
	 * waited for a pass in progress before store.Close() took both away
	 * from under it. Deferred after store.Close() so that it runs first; it
	 * cancels the context itself, because an early return — a port already
	 * in use — gets here with the signal context still live.
	 */
	if nil != compacted {
		defer func() {
			stop()
			<-compacted
		}()
	}

	udp, err := ingest.ListenUDP(cfg.UDPHost, cfg.UDPPort, store, log)
	if nil != err {
		return fmt.Errorf("binding UDP: %w", err)
	}
	defer udp.Close()

	/*!
	 * The v2 receivers.
	 *
	 * HTTP ingest and OTLP are always mounted — they cost nothing when unused
	 * and being available is the point. Syslog binds real ports, so it is
	 * opt-in: nobody should discover a listener they did not ask for.
	 */
	httpIn := ingest.NewHTTP(ingest.HTTPOptions{
		Store:   store,
		Log:     log,
		MaxBody: int64(cfg.MaxBody) << 20,
		Default: cfg.IngestStream,
	})

	var syslogStats *ingest.SyslogStats

	if cfg.SyslogPort > 0 {
		host := cfg.SyslogHost
		if "" == host {
			host = cfg.UDPHost
		}

		sys, err := ingest.ListenSyslog(host, cfg.SyslogPort, ingest.SyslogOptions{
			Store: store,
			Log:   log,
		})
		if nil != err {
			return fmt.Errorf("binding syslog: %w", err)
		}
		defer sys.Close()

		syslogStats = sys.Stats()

		go func() {
			if err := sys.Serve(ctx); nil != err {
				log.Error("syslog receiver stopped", "err", err)
			}
		}()

		log.Info("syslog listening", "udp", sys.Addr().String(), "tcp", sys.TCPAddr().String())
	}

	tls := "" != cfg.TLSCert

	guard := auth.New(cfg.AuthToken, tls)

	server := api.New(api.Options{
		Store:   store,
		Log:     log,
		Version: version,
		Engine:  engine,
		Catalog: cat,
		UDP:     udp.Stats(),
		HTTPIn:  httpIn,
		Syslog:  syslogStats,
		WebRoot: cfg.WebRoot,
		Guard:   guard,
	})

	listener, err := net.Listen("tcp", net.JoinHostPort(cfg.WebHost, strconv.Itoa(cfg.WebPort)))
	if nil != err {
		return fmt.Errorf("binding HTTP: %w", err)
	}

	/*!
	 * Requests run on a context of their own, cancelled only once the grace
	 * period is over.
	 *
	 * They used to run on the signal context, so SIGTERM cancelled every
	 * in-flight search at the same instant Shutdown began waiting for them to
	 * finish: nothing ever drained, it was all cut off. Tails are the
	 * exception that should end at once — they never finish by themselves —
	 * and the API is told to end them through RegisterOnShutdown.
	 */
	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	http1 := &http.Server{
		Handler: server.Handler(),
		// No write timeout: a tail connection is meant to stay open for hours.
		// The SSE handler bounds each write instead, and its heartbeat is
		// what notices a dead peer.
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return requests },
	}
	http1.RegisterOnShutdown(server.Drain)

	errs := make(chan error, 2)

	go func() { errs <- udp.Serve(ctx) }()

	go func() {
		// ServeTLS reads the key pair itself, so a bad path or a mismatched
		// pair surfaces here as a startup error rather than as a handshake
		// failure on the first request.
		serve := func() error { return http1.Serve(listener) }
		if tls {
			serve = func() error { return http1.ServeTLS(listener, cfg.TLSCert, cfg.TLSKey) }
		}

		if err := serve(); nil != err && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	scheme := "http://"
	if tls {
		scheme = "https://"
	}

	log.Info("rtail-server listening",
		"version", version,
		"http", scheme+listener.Addr().String(),
		"udp", udp.Addr().String(),
		"backlog", cfg.Backlog,
		"auth", guard.Enabled(),
	)

	/*!
	 * Say it out loud when the server is open on an address other machines
	 * can reach. config.Validate refuses this unless --insecure was passed,
	 * so reaching here means somebody chose it — and a choice made once at
	 * install time deserves a reminder every time it starts.
	 */
	if !guard.Enabled() && !auth.IsLoopback(cfg.WebHost) {
		log.Warn("no --auth-token: anyone who can reach this address can read every log",
			"host", cfg.WebHost,
		)
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errs:
		if nil != err {
			stop()
			return err
		}
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = http1.Shutdown(shutdown)
	cancelRequests()

	// Out of grace is not a failure of the server: whatever was still running
	// is cut off, as the signal asked, and the exit is still a clean one.
	if errors.Is(err, context.DeadlineExceeded) {
		log.Warn("requests still running after the grace period were cut off")
		http1.Close()
		return nil
	}

	return err
}
