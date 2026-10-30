package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

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
}

// newRouter tüm HTTP uçlarını ve ortak middleware zincirini kurar.
//
// Middleware sırası önemlidir (dıştan içe):
//  1. RequestID  — sonraki her log satırında request_id olsun diye en dışta
//  2. AccessLog  — panic'ten dönen 500 de loglansın diye Recover'ın dışında
//  3. Recover    — handler'daki panic süreci düşürmesin
//  4. Timeout    — her isteğe süre sınırı
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

	return r
}
