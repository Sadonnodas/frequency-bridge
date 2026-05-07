// Command fake-spectera runs the in-package SSCv2 fake server as a standalone
// HTTP service on a chosen port. Useful for manual end-to-end testing of the
// spectera adapter (and the rest of the stack) without real hardware.
//
// Example:
//
//	go run ./cmd/fake-spectera -addr 127.0.0.1:8443 -inputs 8 -pass dev
//	FREQBRIDGE_SPECTERA=http://127.0.0.1:8443 \
//	FREQBRIDGE_SPECTERA_PASSWORD=dev \
//	FREQBRIDGE_SPECTERA_INSECURE_TLS=1 \
//	  ./bin/freqbridge
//
// The fake serves plain HTTP for ease of local dev. Tests in
// internal/adapter/spectera exercise the TLS + cert-skip paths via
// httptest.NewTLSServer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Sadonnodas/frequency-bridge/internal/adapter/spectera/fakeserver"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8443", "listen address (host:port)")
	user := flag.String("user", "api", "HTTP Basic auth username")
	pass := flag.String("pass", "dev", "HTTP Basic auth password")
	inputs := flag.Int("inputs", 8, "number of audio inputs to seed")
	jitterMs := flag.Int("jitter-ms", 0, "if > 0, randomly tweak input frequency every <jitter-ms> milliseconds for liveness")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	fake := fakeserver.New(*user, *pass)
	baseFreq := 470_000 // kHz, see fakeserver ASSUMPTION_FREQ_KHZ
	for i := 1; i <= *inputs; i++ {
		fake.AddInput(i, fmt.Sprintf("Spec %d", i), baseFreq+i*5_275) // ~5.275 MHz spacing
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           fake.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("fake-spectera listening",
			"addr", *addr, "user", *user, "inputs", *inputs)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			stop()
		}
	}()

	if *jitterMs > 0 {
		go runJitter(ctx, fake, *inputs, time.Duration(*jitterMs)*time.Millisecond)
	}

	<-ctx.Done()
	logger.Info("shutdown signal received")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	logger.Info("shutdown complete")
}

// runJitter periodically tweaks each input's frequency by a small step so the
// adapter sees a steady stream of SSE events. Useful for visual UI testing.
func runJitter(ctx context.Context, fake *fakeserver.Server, n int, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	step := 25 // kHz per tick
	dir := 1
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for id := 1; id <= n; id++ {
				fake.UpdateInput(id, func(in *fakeserver.Input) { in.Frequency += step * dir })
			}
			dir = -dir
		}
	}
}
