// Package health, GET /health ucunu sağlar.
//
// Uç, kayıtlı her kontrolü (ör. veritabanı bağlantısı) çalıştırır. Hepsi geçerse 200, biri bile
// geçmezse 503 döner. Docker healthcheck ve `make verify` bu uca bakar; compose'da diğer servisler
// api'yi ancak bu uç 200 dönünce başlatır.
//
// Uç kimlik doğrulaması istemez: konteyner orkestrasyonu token taşımaz. Yanıtta yalnız
// "ok / unavailable" bilgisi vardır; hata ayrıntısı loga yazılır, dışarı verilmez.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/observability"
)

// Check tek bir bağımlılık kontrolüdür.
type Check struct {
	Name string
	Run  func(ctx context.Context) error
}

const (
	statusOK          = "ok"
	statusUnavailable = "unavailable"
)

// Response /health yanıtıdır.
type Response struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// Handler kontrolleri verilen süre sınırıyla çalıştıran bir handler döndürür.
func Handler(timeout time.Duration, checks ...Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		resp := Response{Status: statusOK, Checks: make(map[string]string, len(checks))}
		code := http.StatusOK
		for _, c := range checks {
			if err := c.Run(ctx); err != nil {
				observability.Logger(ctx).Warn("health check failed",
					slog.String("check", c.Name), slog.Any("error", err))
				resp.Checks[c.Name] = statusUnavailable
				resp.Status = statusUnavailable
				code = http.StatusServiceUnavailable
				continue
			}
			resp.Checks[c.Name] = statusOK
		}
		httpx.WriteJSON(w, code, resp)
	}
}
