/*!
 * rtail-server — the v2 server.
 *
 * See docs/proposal-logging-system.md for where this is going. Today it is P0:
 * the v1 server, in Go, at parity. UDP in on the frozen wire format, SSE out,
 * an in-memory backlog, and the existing webapp. Nothing is durable yet — that
 * is P1.
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

		go compactor.Run(ctx, cfg.CompactInterval)

		log.Info("storing logs as parquet",
			"data", cfg.DataDir,
			"compact_every", cfg.CompactInterval,
			"retention", cfg.Retention,
			"retention_raw", cfg.RetentionRaw)
	}

	defer store.Close()

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
	})

	listener, err := net.Listen("tcp", net.JoinHostPort(cfg.WebHost, strconv.Itoa(cfg.WebPort)))
	if nil != err {
		return fmt.Errorf("binding HTTP: %w", err)
	}

	http1 := &http.Server{
		Handler: server.Handler(),
		// No write timeout: a tail connection is meant to stay open for hours.
		// The heartbeat in the SSE handler is what detects a dead peer.
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	errs := make(chan error, 2)

	go func() { errs <- udp.Serve(ctx) }()

	go func() {
		if err := http1.Serve(listener); nil != err && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	log.Info("rtail-server listening",
		"version", version,
		"http", "http://"+listener.Addr().String(),
		"udp", udp.Addr().String(),
		"backlog", cfg.Backlog,
	)

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

	return http1.Shutdown(shutdown)
}
