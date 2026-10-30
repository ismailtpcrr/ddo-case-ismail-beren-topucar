package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"parakopru/internal/account"
	"parakopru/internal/card"
	"parakopru/internal/customer"
	"parakopru/internal/fx"
	"parakopru/internal/interest"
	"parakopru/internal/ledger"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/health"
	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/ids"
	"parakopru/internal/transfer"
)

const healthTimeout = 2 * time.Second

// routerDeps router'ın ihtiyaç duyduğu her şeydir; main'de oluşturulup verilir.
type routerDeps struct {
	Logger         *slog.Logger
	IDs            ids.Generator
	RequestTimeout time.Duration
	HealthChecks   []health.Check
	Tokens         *auth.Tokens
	Auth           *auth.Handler
	Audit          *audit.Handler
	Customers      *customer.Handler
	Accounts       *account.Handler
	Ledger         *ledger.Handler
	Transfers      *transfer.Handler
	Cards          *card.Handler
	FX             *fx.Handler
	EOD            *interest.Handler
}

// newRouter tüm HTTP uçlarını ve ortak middleware zincirini kurar.
//
// Middleware sırası önemlidir (dıştan içe):
//  1. RequestID  — sonraki her log satırında request_id olsun diye en dışta
//  2. AccessLog  — panic'ten dönen 500 de loglansın diye Recover'ın dışında
//  3. Recover    — handler'daki panic süreci düşürmesin
//  4. Timeout    — her isteğe süre sınırı
//
// Yetki: kimlik doğrulamasız uçlar yalnız /health ve giriş uçlarıdır (DECISIONS.md K-03).
// Geri kalan her uç Authenticate + Require(izin) arkasındadır.
func newRouter(d routerDeps) http.Handler {
	r := chi.NewRouter()
	r.Use(
		httpx.RequestID(d.IDs, d.Logger),
		httpx.AccessLog,
		httpx.Recover,
		httpx.Timeout(d.RequestTimeout),
	)
	r.NotFound(httpx.NotFound)
	r.MethodNotAllowed(httpx.MethodNotAllowed)

	r.Get("/health", health.Handler(healthTimeout, d.HealthChecks...))

	r.Route("/api/v1", func(r chi.Router) {
		// Girişsiz: kullanıcı henüz access token'a sahip değil. Refresh ve logout HttpOnly çerezle doğrulanır.
		r.Post("/auth/login", d.Auth.Login)
		r.Post("/auth/refresh", d.Auth.Refresh)
		r.Post("/auth/logout", d.Auth.Logout)

		// Bu gruptaki her uç geçerli bir access token ve kendi izni ister.
		r.Group(func(r chi.Router) {
			r.Use(auth.Authenticate(d.Tokens))

			r.With(auth.Require(auth.PermProfileRead)).Get("/me", d.Auth.Me)
			r.With(auth.Require(auth.PermAuditRead)).Get("/audit-log", d.Audit.List)

			// Müşteri ve hesap yönetimi (F-1). Müşteri yalnız kendi hesaplarını görür (serviste kontrol edilir).
			r.With(auth.Require(auth.PermCustomersWrite)).Post("/customers", d.Customers.Create)
			r.With(auth.Require(auth.PermLedgerRead)).Get("/customers", d.Customers.List)
			r.With(auth.Require(auth.PermLedgerRead)).Get("/customers/{id}", d.Customers.Get)
			r.With(auth.Require(auth.PermCustomersWrite)).Post("/accounts", d.Accounts.Open)
			r.With(auth.RequireAny(auth.PermOwnBanking, auth.PermLedgerRead)).Get("/accounts", d.Accounts.List)
			r.With(auth.RequireAny(auth.PermOwnBanking, auth.PermLedgerRead)).Get("/accounts/{id}", d.Accounts.Get)

			// Çift taraflı defter (F-2, T-1, T-7). Müşteri yalnız kendi hesabının hareketlerini görür (serviste).
			// Elle ters kayıt para hareketidir: Idempotency-Key ister (handler'da).
			r.With(auth.RequireAny(auth.PermOwnBanking, auth.PermLedgerRead)).Get("/accounts/{id}/entries", d.Ledger.AccountEntries)
			// Bakiye ve hareketler zaman yolculuğu destekler (as_of = geçerlilik, known_at = kayıt zamanı); bu parametreler
			// yalnız ledger:read sahibi rollere açıktır (serviste kontrol edilir).
			r.With(auth.RequireAny(auth.PermOwnBanking, auth.PermLedgerRead)).Get("/accounts/{id}/balance", d.Ledger.Balance)
			r.With(auth.Require(auth.PermLedgerRead)).Get("/ledger/entries/{id}", d.Ledger.Entry)
			r.With(auth.Require(auth.PermLedgerReverse)).Post("/ledger/entries/{id}/reverse", d.Ledger.Reverse)

			// Defter değişmezleri: Σ satırlar = 0, özet = türetilmiş bakiye (T-1 kanıtı; verify ve kaos betikleri kullanır).
			r.With(auth.Require(auth.PermLedgerRead)).Get("/admin/invariants", d.Ledger.Invariants)

			// Transferler (F-3). Başlatma Idempotency-Key ister (handler'da); müşteri yalnız kendi hesabından gönderir (serviste).
			r.With(auth.Require(auth.PermTransfersCreate)).Post("/transfers", d.Transfers.Create)
			r.With(auth.RequireAny(auth.PermOwnBanking, auth.PermLedgerRead)).Get("/transfers", d.Transfers.List)
			r.With(auth.RequireAny(auth.PermOwnBanking, auth.PermLedgerRead)).Get("/transfers/{id}", d.Transfers.Get)

			// Kart (F-4, T-3). Kart ağı çağrıları (provizyon, çekim, iade) yalnız "sistem" rolünün ve Idempotency-Key ister;
			// müşteri kendi kartlarını ve hesabının provizyonlarını okur (nesne düzeyi yetki serviste).
			r.With(auth.Require(auth.PermSystemCallbacks)).Post("/cards/{id}/authorizations", d.Cards.Authorize)
			r.With(auth.Require(auth.PermSystemCallbacks)).Post("/holds/{id}/captures", d.Cards.Capture)
			r.With(auth.Require(auth.PermSystemCallbacks)).Post("/captures/{id}/refunds", d.Cards.Refund)
			r.With(auth.RequireAny(auth.PermOwnBanking, auth.PermLedgerRead)).Get("/cards", d.Cards.Cards)
			r.With(auth.RequireAny(auth.PermOwnBanking, auth.PermLedgerRead)).Get("/accounts/{id}/holds", d.Cards.Holds)

			// Kur yönetimi (F-5): kurları herkes okur (genel bilgi), yalnız operasyon uzmanı ekler (fx:write).
			r.With(auth.Require(auth.PermProfileRead)).Get("/fx/rates", d.FX.List)
			r.With(auth.Require(auth.PermFXWrite)).Post("/fx/rates", d.FX.Create)

			// Gün sonu (F-6, T-4): faiz tahakkuku ve süresi dolan provizyonlar; operasyon ya da sistem başlatır, worker yürütür.
			r.With(auth.Require(auth.PermEODRun)).Post("/eod/runs", d.EOD.Start)
			r.With(auth.RequireAny(auth.PermEODRun, auth.PermLedgerRead)).Get("/eod/runs/{date}", d.EOD.Get)

			// Dört göz onayı (10.000 TL üzeri): operasyon ve uyum; isteyen kişi onaylayamaz (serviste ve DB kısıtıyla).
			r.With(auth.Require(auth.PermTransfersApprove)).Post("/transfers/{id}/approve", d.Transfers.Approve)
			r.With(auth.Require(auth.PermTransfersApprove)).Post("/transfers/{id}/reject", d.Transfers.Reject)

			// Banka bildirimleri: yalnız "sistem" rolü (simülatör bu rolle giriş yapar).
			r.With(auth.Require(auth.PermSystemCallbacks)).Post("/webhooks/bank/transfer-status", d.Transfers.BankStatus)
			r.With(auth.Require(auth.PermSystemCallbacks)).Post("/webhooks/bank/incoming", d.Transfers.Incoming)
		})
	})

	return r
}
