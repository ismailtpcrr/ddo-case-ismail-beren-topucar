package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/observability"
)

type principalKey struct{}

// WithPrincipal doğrulanmış kullanıcıyı context'e koyar.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom context'teki doğrulanmış kullanıcıyı döndürür.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

var (
	errMissingToken = apperr.ErrUnauthorized.WithMessage("Kimlik doğrulaması gerekli.")
	errBadToken     = apperr.ErrUnauthorized.WithMessage("Oturum geçersiz ya da süresi dolmuş.")
)

// Authenticate, Authorization: Bearer <access token> başlığını doğrular. Başlık yoksa ya da token
// geçersizse 401 döner. Geçerliyse kullanıcıyı context'e koyar ve sonraki log satırlarına user_id ekler.
func Authenticate(tokens *Tokens) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
				unauthorized(w, r, errMissingToken)
				return
			}
			p, err := tokens.ParseAccess(token)
			if err != nil {
				unauthorized(w, r, errBadToken)
				return
			}

			ctx := WithPrincipal(r.Context(), p)
			httpx.SetUserID(ctx, p.UserID.String())
			ctx = observability.WithLogger(ctx, observability.Logger(ctx).With(
				slog.String("user_id", p.UserID.String()), slog.String("role", string(p.Role))))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Require, isteği yapan kullanıcının rolünde izin yoksa 403 döner. Authenticate'ten sonra kullanılır;
// kullanıcı yoksa (yanlış sıralama) güvenli tarafta kalıp 401 döner.
//
// Bu yalnız uç seviyesindeki kontroldür. "Müşteri yalnız kendi hesabını görür" gibi nesne seviyesindeki
// kurallar servis içinde ayrıca kontrol edilir.
func Require(perm Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				unauthorized(w, r, errMissingToken)
				return
			}
			if !p.Role.Has(perm) {
				observability.Logger(r.Context()).Warn("permission denied", slog.String("permission", string(perm)))
				httpx.WriteError(w, r, apperr.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAny, verilen izinlerden en az birine sahip rolleri geçirir. Aynı uçta farklı kapsamla çalışan
// durumlar içindir: ör. GET /accounts — müşteri (own_banking) yalnız kendi hesaplarını, ledger:read sahibi
// roller tüm hesapları görür; kapsamı servis belirler.
func RequireAny(perms ...Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				unauthorized(w, r, errMissingToken)
				return
			}
			for _, perm := range perms {
				if p.Role.Has(perm) {
					next.ServeHTTP(w, r)
					return
				}
			}
			observability.Logger(r.Context()).Warn("permission denied", slog.Any("permissions", perms))
			httpx.WriteError(w, r, apperr.ErrForbidden)
		})
	}
}

func unauthorized(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="parakopru"`)
	httpx.WriteError(w, r, err)
}
