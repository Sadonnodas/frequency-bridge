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
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter"
	"github.com/Sadonnodas/frequency-bridge/internal/adapter/mock"
	"github.com/Sadonnodas/frequency-bridge/internal/adapter/spectera"
	"github.com/Sadonnodas/frequency-bridge/internal/api"
	"github.com/Sadonnodas/frequency-bridge/internal/api/ws"
	"github.com/Sadonnodas/frequency-bridge/internal/bus"
	"github.com/Sadonnodas/frequency-bridge/internal/db"
	"github.com/Sadonnodas/frequency-bridge/internal/events"
	"github.com/Sadonnodas/frequency-bridge/internal/safety"
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
	locks := safety.New()
	wsServer := ws.NewServer(b, store, sink, locks, logger)

	adapters := []adapter.Adapter{
		mock.New("mock:0", "Mock Receiver", 4),
	}
	for _, sp := range loadSpecteraConfigs(logger) {
		adapters = append(adapters, spectera.New(sp, logger))
	}
	// Register adapters with the sink so WS RPC handlers can dispatch
	// channel.set_* writes to the right Commander.
	for _, a := range adapters {
		sink.RegisterAdapter(a)
	}

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

// loadSpecteraConfigs reads FREQBRIDGE_SPECTERA (comma-separated base URLs)
// and the related credential / TLS env vars, returning one Config per host.
//
//	FREQBRIDGE_SPECTERA               comma-separated full base URLs
//	                                  e.g. "https://192.168.6.50,http://127.0.0.1:8443"
//	FREQBRIDGE_SPECTERA_USER          basic-auth username (default "api")
//	FREQBRIDGE_SPECTERA_PASSWORD      basic-auth password (required if hosts set)
//	FREQBRIDGE_SPECTERA_INSECURE_TLS  "1" / "true" to accept self-signed certs
//
// If FREQBRIDGE_SPECTERA is empty, no Spectera adapters are configured.
func loadSpecteraConfigs(logger *slog.Logger) []spectera.Config {
	raw := strings.TrimSpace(os.Getenv("FREQBRIDGE_SPECTERA"))
	if raw == "" {
		return nil
	}
	password := os.Getenv("FREQBRIDGE_SPECTERA_PASSWORD")
	if password == "" {
		logger.Warn("FREQBRIDGE_SPECTERA set but FREQBRIDGE_SPECTERA_PASSWORD is empty; ignoring",
			"hosts", raw)
		return nil
	}
	user := os.Getenv("FREQBRIDGE_SPECTERA_USER")
	if user == "" {
		user = "api"
	}
	insecure := envTruthy("FREQBRIDGE_SPECTERA_INSECURE_TLS")

	var out []spectera.Config
	for _, h := range strings.Split(raw, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		host := strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
		host = strings.TrimSuffix(host, "/")
		out = append(out, spectera.Config{
			InstanceID:         "spectera:" + host,
			DisplayName:        "Spectera (" + host + ")",
			BaseURL:            h,
			Username:           user,
			Password:           password,
			InsecureSkipVerify: insecure,
		})
	}
	return out
}

func envTruthy(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
