package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/health"
	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/ids"
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
		})
	})

	return r
}
