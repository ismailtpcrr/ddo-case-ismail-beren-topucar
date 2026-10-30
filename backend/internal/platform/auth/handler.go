package auth

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"parakopru/internal/platform/httpx"
)

const (
	refreshCookieName = "pk_refresh"
	// Çerez yalnız /api/v1/auth altındaki isteklere gider; diğer API çağrılarında taşınmaz.
	refreshCookiePath = "/api/v1/auth"
)

// Handler kimlik doğrulama HTTP uçlarıdır.
type Handler struct {
	svc          *Service
	cookieSecure bool
}

// NewHandler yeni bir Handler oluşturur. cookieSecure, refresh çerezinin yalnız HTTPS üzerinden
// gönderilmesini sağlar; yerel demo (http://localhost) için kapalıdır.
func NewHandler(svc *Service, cookieSecure bool) *Handler {
	return &Handler{svc: svc, cookieSecure: cookieSecure}
}

// E-posta biçimi girişte ayrıca doğrulanmaz: e-posta normalleştirilip (boşluk, büyük harf) aranır;
// biçimi bozuk bir e-posta zaten kayıtlı olamayacağından sonuç "e-posta ya da parola hatalı" olur.
type loginRequest struct {
	Email    string `json:"email"    validate:"required,max=254"`
	Password string `json:"password" validate:"required,max=128"`
}

// UserResponse kullanıcının dışarıya gösterilen halidir; parola özeti asla dönmez.
type UserResponse struct {
	ID          uuid.UUID    `json:"id"`
	Email       string       `json:"email"`
	FullName    string       `json:"full_name"`
	Role        Role         `json:"role"`
	Permissions []Permission `json:"permissions"`
}

type sessionResponse struct {
	AccessToken string       `json:"access_token"`
	TokenType   string       `json:"token_type"`
	ExpiresAt   time.Time    `json:"expires_at"`
	User        UserResponse `json:"user"`
}

func toUserResponse(u User) UserResponse {
	return UserResponse{ID: u.ID, Email: u.Email, FullName: u.FullName, Role: u.Role, Permissions: u.Role.Permissions()}
}

// Login: POST /api/v1/auth/login
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sess, err := h.svc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writeSession(w, sess)
}

// Refresh: POST /api/v1/auth/refresh — refresh token çerezden okunur.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(refreshCookieName)
	if err != nil {
		httpx.WriteError(w, r, ErrSessionExpired)
		return
	}
	sess, err := h.svc.Refresh(r.Context(), c.Value)
	if err != nil {
		h.clearCookie(w)
		httpx.WriteError(w, r, err)
		return
	}
	h.writeSession(w, sess)
}

// Logout: POST /api/v1/auth/logout — refresh token iptal edilir, çerez silinir.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(refreshCookieName); err == nil {
		if err := h.svc.Logout(r.Context(), c.Value); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// Me: GET /api/v1/me
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context()) // Authenticate + Require sonrasında her zaman vardır
	u, err := h.svc.Me(r.Context(), p.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toUserResponse(u))
}

func (h *Handler) writeSession(w http.ResponseWriter, s Session) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure, COOKIE_SECURE ile açılır; yerel demo http://localhost üzerinde çalışır
		Name:     refreshCookieName,
		Value:    s.RefreshToken,
		Path:     refreshCookiePath,
		Expires:  s.RefreshExpiresAt,
		HttpOnly: true,                    // JavaScript okuyamaz (XSS ile çalınamaz)
		SameSite: http.SameSiteStrictMode, // başka siteden gelen isteklerde gönderilmez (CSRF)
		Secure:   h.cookieSecure,
	})
	// Token içeren yanıt hiçbir ara katmanda önbelleklenmemeli.
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, sessionResponse{
		AccessToken: s.AccessToken,
		TokenType:   "Bearer",
		ExpiresAt:   s.AccessExpiresAt,
		User:        toUserResponse(s.User),
	})
}

func (h *Handler) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure, COOKIE_SECURE ile açılır; yerel demo http://localhost üzerinde çalışır
		Name:     refreshCookieName,
		Value:    "",
		Path:     refreshCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   h.cookieSecure,
	})
}
