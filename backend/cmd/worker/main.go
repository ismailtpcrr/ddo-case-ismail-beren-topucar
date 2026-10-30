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

	"parakopru/internal/account"
	"parakopru/internal/bank"
	"parakopru/internal/card"
	"parakopru/internal/customer"
	"parakopru/internal/fx"
	"parakopru/internal/interest"
	"parakopru/internal/ledger"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/config"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/health"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/jobs"
	"parakopru/internal/platform/observability"
	"parakopru/internal/transfer"
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
	// Bileşenler composition root'ta bağlanır (api ile aynı servisler; banka istemcisi yalnız burada).
	clk := clock.System{}
	idGen := ids.V7{}
	txm := db.NewTxManager(pool)
	recorder := audit.NewRecorder(idGen)
	customerSvc := customer.NewService(txm, idGen, recorder)
	accountSvc := account.NewService(txm, customerSvc, idGen, clk, recorder)
	ledgerSvc := ledger.NewService(txm, idGen, recorder, accountSvc)
	transferSvc := transfer.NewService(txm, idGen, recorder, ledgerSvc, accountSvc, jobs.NewQueue(idGen, clk),
		bank.New(cfg.BankURL, cfg.BankTimeout), fx.NewService(txm, idGen, clk, recorder))

	cardSvc := card.NewService(txm, idGen, clk, recorder, ledgerSvc, accountSvc)

	eodSvc := interest.NewService(txm, idGen, clk, recorder, ledgerSvc, accountSvc, cardSvc, jobs.NewQueue(idGen, clk),
		interest.Config{BatchSize: cfg.EODBatchSize, CrashAfterBatches: cfg.EODCrashAfterBatches})

	// İş türleri.
	runner.Handle(transfer.JobSend, transferSvc.HandleSend)
	runner.Handle(interest.JobEOD, func(ctx context.Context, job jobs.Job) error {
		err := eodSvc.HandleJob(ctx, job)
		if errors.Is(err, interest.ErrSimulatedEnd) {
			// Çökme testi: süreç gerçekten ölür (yarım iş kirada kalır); yeniden başlayınca iş tekrar alınır ve kaldığı yerden sürer.
			log.Error("EOD_CRASH_AFTER_BATCHES reached: exiting to simulate a crash")
			os.Exit(3)
		}
		return err
	})

	// Sağlık ucu: Docker healthcheck için; veritabanı erişimini kontrol eder.
	mux := http.NewServeMux()
	mux.Handle("GET /health", health.Handler(2*time.Second, health.Check{Name: "database", Run: pool.Ping}))
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	// Süresi dolan kart provizyonları periyodik olarak serbest bırakılır (PLAN.md §5.4).
	expiryDone := make(chan struct{})
	go func() {
		defer close(expiryDone)
		t := time.NewTicker(cfg.HoldExpiryInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := cardSvc.ExpireDue(ctx); err != nil && ctx.Err() == nil {
					log.Error("hold expiry failed", slog.Any("error", err))
				}
			}
		}
	}()

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
	<-expiryDone
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
