// Package observability log, metrik ve trace altyapısını toplar.
package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// Adında bu parçalardan biri geçen log alanlarının değeri yazılmaz.
// Asıl kural bunları hiç loglamamaktır; bu filtre yanlışlıkla eklenen bir alan için ikinci güvencedir.
var sensitiveKeyParts = []string{"password", "parola", "token", "secret", "authorization", "cookie"}

const redacted = "[REDACTED]"

// NewLogger, service alanı eklenmiş JSON log üreticisi oluşturur.
func NewLogger(w io.Writer, level slog.Leveler, service string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redactSensitive,
	})
	return slog.New(h).With(slog.String("service", service))
}

func redactSensitive(_ []string, a slog.Attr) slog.Attr {
	key := strings.ToLower(a.Key)
	for _, part := range sensitiveKeyParts {
		if strings.Contains(key, part) {
			return slog.String(a.Key, redacted)
		}
	}
	return a
}

type loggerKey struct{}

// WithLogger, isteğe özgü alanları (request_id, user_id…) taşıyan logger'ı context'e koyar.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// Logger context'teki logger'ı döndürür; yoksa varsayılan logger'ı.
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
