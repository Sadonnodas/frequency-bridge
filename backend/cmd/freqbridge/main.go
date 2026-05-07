// Command freqbridge runs the Frequency Bridge backend: an HTTP + WebSocket
// server that fronts vendor adapters for wireless audio receivers.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter"
	"github.com/Sadonnodas/frequency-bridge/internal/adapter/mock"
	"github.com/Sadonnodas/frequency-bridge/internal/api"
	"github.com/Sadonnodas/frequency-bridge/internal/api/ws"
	"github.com/Sadonnodas/frequency-bridge/internal/bus"
	"github.com/Sadonnodas/frequency-bridge/internal/db"
	"github.com/Sadonnodas/frequency-bridge/internal/events"
	"github.com/Sadonnodas/frequency-bridge/internal/state"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	bind := os.Getenv("FREQBRIDGE_BIND")
	if bind == "" {
		bind = "0.0.0.0:8080"
	}

	dbPath, err := db.DefaultPath()
	if err != nil {
		logger.Error("resolve db path", "err", err)
		os.Exit(1)
	}
	openCtx, openCancel := context.WithTimeout(context.Background(), 10*time.Second)
	database, err := db.Open(openCtx, dbPath)
	openCancel()
	if err != nil {
		logger.Error("open database", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer func() { _ = database.Close() }()
	if v, verr := db.CurrentVersion(context.Background(), database); verr == nil {
		logger.Info("database ready", "path", dbPath, "schema_version", v)
	}

	store := state.NewStore()
	b := bus.New()
	sink := events.New(store, b, logger, 1024)
	wsServer := ws.NewServer(b, store, sink, logger)

	mockAdapter := mock.New("mock:0", "Mock Receiver", 4)
	adapters := []adapter.Adapter{mockAdapter}

	srv := api.NewServer(api.Config{Bind: bind, WSHandler: wsServer.Handler()})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpServer := &http.Server{
		Addr:              bind,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	g, gctx := errgroup.WithContext(ctx)

	// Events sink: applies patches to the store and republishes to the bus.
	g.Go(func() error {
		sink.Run(gctx)
		return nil
	})

	// Adapters: connect to (or simulate) hardware and push events to the sink.
	for _, a := range adapters {
		a := a
		ident := a.Identity()
		g.Go(func() error {
			logger.Info("adapter starting", "instance", ident.InstanceID, "vendor", ident.Vendor)
			if err := a.Run(gctx, sink); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("adapter exited", "instance", ident.InstanceID, "err", err)
				return err
			}
			return nil
		})
	}

	// HTTP server.
	g.Go(func() error {
		logger.Info("server starting", "bind", bind)
		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})

	// Shutdown driver.
	g.Go(func() error {
		<-gctx.Done()
		logger.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "err", err)
		}
		return nil
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("exiting with error", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
