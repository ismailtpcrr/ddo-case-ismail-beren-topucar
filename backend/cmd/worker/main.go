// Command worker, zamanlanmış işleri ve iş kuyruğunu çalıştıran süreçtir (proje.md §4: "Sistem (zamanlanmış işler)").
//
// api ile aynı imajdan, ayrı bir konteynerde çalışır. Ayrı olması, uzun süren işlerin (gün sonu, dış banka
// sorgulamaları) API yanıt sürelerini etkilememesini ve worker'ın tek başına durdurulup yeniden başlatılabilmesini
// sağlar ("gün sonu işi çökerse kaldığı yerden devam eder" gösterimi). Migrasyon çalıştırmaz; compose'da api
// sağlıklı olduktan sonra başlar, yani şema hazırdır.
//
// İş türleri (transfer gönderimi, süresi dolan provizyonlar, gün sonu) ilgili modüllerle birlikte burada kaydedilir.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/config"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/health"
	"parakopru/internal/platform/jobs"
	"parakopru/internal/platform/observability"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load[config.Worker]()
	if err != nil {
		return err
	}
	log := observability.NewLogger(os.Stdout, cfg.LogLevel, "worker")
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	runner := jobs.NewRunner(pool, clock.System{}, log, jobs.RunnerConfig{
		Concurrency:  cfg.Concurrency,
		PollInterval: cfg.PollInterval,
		Lease:        cfg.JobLease,
	})
	// İş türleri ilgili adımlarda burada kaydedilir (ör. runner.Handle("transfer.send", …)).

	// Sağlık ucu: Docker healthcheck için; veritabanı erişimini kontrol eder.
	mux := http.NewServeMux()
	mux.Handle("GET /health", health.Handler(2*time.Second, health.Check{Name: "database", Run: pool.Ping}))
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	runnerDone := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(runnerDone)
	}()
	log.Info("worker started", slog.Int("concurrency", cfg.Concurrency), slog.String("health_addr", cfg.HTTPAddr))

	select {
	case err := <-serveErr:
		return fmt.Errorf("health server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received; finishing running jobs")
	}

	// Düzgün kapanış: yeni iş alınmaz, çalışan işlerin bitmesi ShutdownTimeout kadar beklenir.
	// Süre dolarsa yarım kalan işler kira süreleri bitince başka bir worker tarafından yeniden alınır.
	select {
	case <-runnerDone:
		log.Info("all running jobs finished")
	case <-time.After(cfg.ShutdownTimeout):
		log.Warn("shutdown timeout; unfinished jobs will be retried after their lease expires")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("health shutdown: %w", err)
	}
	log.Info("worker stopped")
	return nil
}
