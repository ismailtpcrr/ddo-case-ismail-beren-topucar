// Command api, ParaKöprü HTTP API sürecidir.
//
// Bu dosya "composition root"tur: bütün bileşenler burada oluşturulur ve birbirine bağlanır
// (constructor injection). Başka hiçbir paket global bağlantı ya da singleton tutmaz.
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
	"parakopru/internal/card"
	"parakopru/internal/customer"
	"parakopru/internal/fx"
	"parakopru/internal/interest"
	"parakopru/internal/ledger"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/config"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/health"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/jobs"
	"parakopru/internal/platform/observability"
	"parakopru/internal/transfer"
	"parakopru/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api stopped with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load[config.API]()
	if err != nil {
		return err
	}
	log := observability.NewLogger(os.Stdout, cfg.LogLevel, "api")
	slog.SetDefault(log)

	// SIGTERM (docker stop) ya da Ctrl+C gelince ctx iptal olur ve düzgün kapanış başlar.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Önce veritabanının hazır olmasını bekle, sonra şemayı güncelle ve temel seed'i yap. HTTP sunucusu
	// (dolayısıyla /health) bunlar bitmeden açılmaz; compose'daki bağımlı servisler eksik veriyle başlamaz.
	pool, err := db.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, cfg.MigrationDatabaseURL, migrations.FS, log); err != nil {
		return err
	}

	// Ortak bağımlılıklar.
	clk := clock.System{}
	idGen := ids.V7{}
	txm := db.NewTxManager(pool)

	// Kimlik doğrulama.
	tokens := auth.NewTokens(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL, clk)
	authSvc := auth.NewService(txm, auth.Repo{}, tokens, idGen, clk)

	// İş modülleri.
	recorder := audit.NewRecorder(idGen)
	customerSvc := customer.NewService(txm, idGen, recorder)
	accountSvc := account.NewService(txm, customerSvc, idGen, clk, recorder)
	ledgerSvc := ledger.NewService(txm, idGen, recorder, accountSvc)
	// Dış bankaya gönderimi worker yapar; api yalnız işi kuyruğa ekler (outbox) ve banka bildirimlerini işler.
	fxSvc := fx.NewService(txm, idGen, clk, recorder)
	cardSvc := card.NewService(txm, idGen, clk, recorder, ledgerSvc, accountSvc)
	transferSvc := transfer.NewService(txm, idGen, recorder, ledgerSvc, accountSvc, jobs.NewQueue(idGen, clk), nil, fxSvc)

	// Temel seed: demo kullanıcılar, iç hesaplar, demo müşteri ve açılış bakiyeleri. Var olana dokunmaz; her açılışta
	// güvenle çalışır.
	if err := seedBase(ctx, log, txm, clk, cfg.DemoPassword, authSvc, customerSvc, accountSvc, ledgerSvc, cardSvc, fxSvc); err != nil {
		return err
	}

	handler := newRouter(routerDeps{
		Logger:         log,
		IDs:            idGen,
		RequestTimeout: cfg.RequestTimeout,
		HealthChecks:   []health.Check{{Name: "database", Run: pool.Ping}},
		Tokens:         tokens,
		Auth:           auth.NewHandler(authSvc, cfg.CookieSecure),
		Audit:          audit.NewHandler(audit.NewService(pool)),
		Customers:      customer.NewHandler(customerSvc),
		Accounts:       account.NewHandler(accountSvc),
		Ledger:         ledger.NewHandler(ledgerSvc),
		Transfers:      transfer.NewHandler(transferSvc),
		Cards:          card.NewHandler(cardSvc),
		FX:             fx.NewHandler(fxSvc),
		EOD:            interest.NewHandler(interest.NewService(txm, idGen, clk, recorder, ledgerSvc, accountSvc, cardSvc, jobs.NewQueue(idGen, clk), interest.Config{})),
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Yanıt yazma süresi istek zaman aşımından biraz uzun olmalı ki 504 cevabı yazılabilsin.
		WriteTimeout: cfg.RequestTimeout + 5*time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", slog.String("addr", cfg.HTTPAddr))
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	// Düzgün kapanış: yeni bağlantı kabul edilmez, süren istekler ShutdownTimeout'a kadar tamamlanır.
	// Ardından (defer) veritabanı havuzu kapanır.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http shutdown: %w", err)
	}
	log.Info("http server stopped")
	return nil
}
