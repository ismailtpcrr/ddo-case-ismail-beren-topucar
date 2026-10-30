package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/observability"
)

// RequestIDHeader her yanıtta dönen istek kimliği başlığıdır.
const RequestIDHeader = "X-Request-ID"

// İstemcinin gönderdiği kimlik yalnız bu biçimdeyse kabul edilir; aksi halde log satırına
// keyfi metin (ör. satır sonu) enjekte edilebilirdi.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type requestIDKey struct{}

// RequestIDFrom context'teki istek kimliğini döndürür.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// RequestID her isteğe bir kimlik verir: geçerli bir X-Request-ID geldiyse onu kullanır
// (çağıran servisle aynı kimlik izlenebilsin), yoksa yenisini üretir. Kimlik yanıt başlığına,
// context'e ve isteğin logger'ına eklenir.
func RequestID(gen ids.Generator, base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if !validRequestID.MatchString(id) {
				id = gen.NewID().String()
			}
			w.Header().Set(RequestIDHeader, id)

			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
			ctx = observability.WithLogger(ctx, base.With(slog.String("request_id", id)))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Sağlık ve metrik uçları birkaç saniyede bir yoklanır; başarılı olanlar yalnız debug seviyesinde loglanır.
var quietPaths = map[string]bool{"/health": true, "/metrics": true}

// AccessLog her isteği tek bir JSON satırında loglar: yöntem, yol, rota, durum, boyut, süre.
// İstek gövdesi ve başlıklar (Authorization, Cookie) loglanmaz.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		level := slog.LevelInfo
		switch {
		case status >= http.StatusInternalServerError:
			level = slog.LevelError
		case quietPaths[r.URL.Path]:
			level = slog.LevelDebug
		}
		route := ""
		if rc := chi.RouteContext(r.Context()); rc != nil {
			route = rc.RoutePattern()
		}
		observability.Logger(r.Context()).LogAttrs(r.Context(), level, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("route", route),
			slog.Int("status", status),
			slog.Int("bytes", ww.BytesWritten()),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
	})
}

// Recover, handler'daki panic'i yakalar: yığın izini loglar ve istemciye 500 INTERNAL döner.
// Süreç çökmez, diğer istekler etkilenmez.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// net/http'nin bağlantıyı kesmek için kullandığı özel panic; yutulmamalı.
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			observability.Logger(r.Context()).Error("panic recovered",
				slog.String("panic", fmt.Sprint(rec)),
				slog.String("stack", string(debug.Stack())),
			)
			writeAppError(w, apperr.ErrInternal)
		}()
		next.ServeHTTP(w, r)
	})
}

// Timeout her isteğin context'ine süre sınırı koyar. Süre dolunca veritabanı ve dış çağrılar
// context üzerinden iptal olur; handler'a dönen hata WriteError ile 504 TIMEOUT'a çevrilir.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// NotFound tanımsız yollar için tutarlı biçimde 404 döner.
func NotFound(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, apperr.ErrNotFound.WithMessage("Böyle bir uç nokta yok."))
}

// MethodNotAllowed, yol var ama yöntem desteklenmiyorsa tutarlı biçimde 405 döner.
func MethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, apperr.ErrMethodNotAllowed)
}
