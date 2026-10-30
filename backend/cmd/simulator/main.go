// Command simulator, ParaKöprü'nün veri üretici servisidir (proje.md §8).
//
// Ayrı bir compose servisidir. Açılışta tohumlu (aynı tohum → aynı veri) müşteri ve hesap verisini toplu olarak
// yazar; bitince /health 200 döner, böylece `verify` ve diğer bağımlı servisler seed hazır olduktan sonra başlar.
// Dış banka sahtesi, işlem trafiği ve ekstre üretimi sonraki adımlarda (C1, I1, K1) buraya eklenir.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/config"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/health"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/observability"
	"parakopru/internal/sim"
)

func main() {
	if err := run(); err != nil {
		slog.Error("simulator stopped with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load[config.Simulator]()
	if err != nil {
		return err
	}
	log := observability.NewLogger(os.Stdout, cfg.LogLevel, "simulator")
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Sağlık ucu seed bitene kadar 503 döner; compose "healthy" ancak veri hazır olunca olur.
	var seeded atomic.Bool
	mux := http.NewServeMux()
	mux.Handle("GET /health", health.Handler(2*time.Second,
		health.Check{Name: "database", Run: pool.Ping},
		health.Check{Name: "seed", Run: func(context.Context) error {
			if !seeded.Load() {
				return errors.New("seed not finished")
			}
			return nil
		}}))
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	start := time.Now()
	skipped, err := sim.Seed(ctx, db.NewTxManager(pool), audit.NewRecorder(ids.V7{}), sim.Params{Seed: cfg.Seed, Customers: cfg.Customers})
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	seeded.Store(true)
	log.Info("simulator ready", slog.Bool("seed_skipped", skipped), slog.Int64("seed", cfg.Seed),
		slog.Int("customers", cfg.Customers), slog.Int64("took_ms", time.Since(start).Milliseconds()))

	select {
	case err := <-serveErr:
		return fmt.Errorf("health server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("health shutdown: %w", err)
	}
	log.Info("simulator stopped")
	return nil
}
