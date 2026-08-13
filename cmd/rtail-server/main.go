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
	"github.com/kilianc/rtail/v2/internal/config"
	"github.com/kilianc/rtail/v2/internal/ingest"
	"github.com/kilianc/rtail/v2/internal/logstore"
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
	var store logstore.Store

	if "" == cfg.DataDir {
		store = logstore.NewMemory(cfg.Backlog)
		log.Info("running in memory; pass --data to keep logs across restarts")
	} else {
		durable, err := logstore.OpenDurable(ctx, cfg.DataDir, logstore.DurableOptions{
			Backlog: cfg.Backlog,
			KeepRaw: true,
			Log:     log,
		})
		if nil != err {
			return fmt.Errorf("opening the data directory: %w", err)
		}

		store = durable
		log.Info("storing logs as parquet", "data", cfg.DataDir)
	}

	defer store.Close()

	udp, err := ingest.ListenUDP(cfg.UDPHost, cfg.UDPPort, store, log)
	if nil != err {
		return fmt.Errorf("binding UDP: %w", err)
	}
	defer udp.Close()

	server := api.New(api.Options{
		Store:   store,
		Log:     log,
		Version: version,
		UDP:     udp.Stats(),
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
