package auth_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/observability"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

const (
	demoPassword = "Demo.Parola.2026"
	secret       = "flow-test-secret-0123456789-0123456789-xyz"
)

type harness struct {
	svc     *auth.Service
	handler http.Handler
	clock   *clock.Manual
}

// newHarness gerçek veritabanına bağlı auth servisi ve compose'dakiyle aynı rota düzeni kurar.
func newHarness(t *testing.T) harness {
	t.Helper()
	env := dbtest.Get(t)
	clk := clock.NewManual(time.Now().UTC())
	tokens := auth.NewTokens(secret, 15*time.Minute, 7*24*time.Hour, clk)
	svc := auth.NewService(db.NewTxManager(env.App), auth.Repo{}, tokens, ids.V7{}, clk)
	_, err := svc.SeedDemoUsers(t.Context(), demoPassword)
	require.NoError(t, err)

	h := auth.NewHandler(svc, false)
	r := chi.NewRouter()
	r.Use(httpx.RequestID(ids.V7{}, observability.NewLogger(io.Discard, slog.LevelInfo, "test")))
	r.Post("/api/v1/auth/login", h.Login)
	r.Post("/api/v1/auth/refresh", h.Refresh)
	r.Post("/api/v1/auth/logout", h.Logout)
	r.With(auth.Authenticate(tokens), auth.Require(auth.PermProfileRead)).Get("/api/v1/me", h.Me)
	return harness{svc: svc, handler: r, clock: clk}
}

func (h harness) do(t *testing.T, method, path, body string, header http.Header, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

type sessionBody struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	User        struct {
		Email       string   `json:"email"`
		Role        string   `json:"role"`
		Permissions []string `json:"permissions"`
	} `json:"user"`
}

func (h harness) login(t *testing.T, email, password string) (*httptest.ResponseRecorder, sessionBody, *http.Cookie) {
	t.Helper()
	rec := h.do(t, http.MethodPost, "/api/v1/auth/login",
		`{"email":"`+email+`","password":"`+password+`"}`, nil)
	var body sessionBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body, refreshCookie(rec)
}

func refreshCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == "pk_refresh" {
			return c
		}
	}
	return nil
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body.Error.Code
}

func TestSeedCreatesOneUserPerRoleAndIsIdempotent(t *testing.T) {
	h := newHarness(t) // ilk seed burada
	created, err := h.svc.SeedDemoUsers(t.Context(), demoPassword)
	require.NoError(t, err)
	require.Zero(t, created, "ikinci seed hiçbir şey eklememeli")

	var roles []string
	rows, err := dbtest.Get(t).App.Query(t.Context(), `SELECT role FROM users WHERE email LIKE '%@parakopru.test' ORDER BY role`)
	require.NoError(t, err)
	for rows.Next() {
		var r string
		require.NoError(t, rows.Scan(&r))
		roles = append(roles, r)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"AUDITOR", "COMPLIANCE", "CUSTOMER", "OPERATIONS", "RISK_ANALYST", "SYSTEM"}, roles)
}

func TestEveryDemoAccountCanLogIn(t *testing.T) {
	h := newHarness(t)
	for _, d := range auth.DemoUsers {
		rec, body, _ := h.login(t, d.Email, demoPassword)
		require.Equal(t, http.StatusOK, rec.Code, d.Email)
		require.Equal(t, string(d.Role), body.User.Role)
	}
}

func TestLoginSuccessSetsSecureCookieAndMeWorks(t *testing.T) {
	h := newHarness(t)
	rec, body, cookie := h.login(t, "  Operasyon@ParaKopru.TEST ", demoPassword) // e-posta büyük/küçük harf ve boşluk duyarsız

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "Bearer", body.TokenType)
	require.NotEmpty(t, body.AccessToken)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.NotContains(t, rec.Body.String(), "password", "parola özeti yanıtta olmamalı")
	require.NotContains(t, rec.Body.String(), "argon2id")

	require.NotNil(t, cookie)
	require.True(t, cookie.HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	require.Equal(t, "/api/v1/auth", cookie.Path)

	me := h.do(t, http.MethodGet, "/api/v1/me", "", http.Header{"Authorization": {"Bearer " + body.AccessToken}})
	require.Equal(t, http.StatusOK, me.Code)
	require.Contains(t, me.Body.String(), `"role":"OPERATIONS"`)
	require.Contains(t, me.Body.String(), `"transfers:approve"`)
}

func TestWrongPasswordAndUnknownEmailLookTheSame(t *testing.T) {
	h := newHarness(t)

	wrong, _, wrongCookie := h.login(t, "musteri@parakopru.test", "yanlis-parola")
	unknown, _, _ := h.login(t, "yok@parakopru.test", demoPassword)

	for _, rec := range []*httptest.ResponseRecorder{wrong, unknown} {
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.Equal(t, "INVALID_CREDENTIALS", errorCode(t, rec))
	}
	require.JSONEq(t, wrong.Body.String(), unknown.Body.String(), "hangi bilginin yanlış olduğu belli olmamalı")
	require.Nil(t, wrongCookie)
}

func TestLoginValidation(t *testing.T) {
	h := newHarness(t)
	for _, body := range []string{`{"email":"","password":"x"}`, `{"email":"a@b.c","password":""}`, `{"email":"a@b.c"}`} {
		rec := h.do(t, http.MethodPost, "/api/v1/auth/login", body, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		require.Equal(t, "VALIDATION_ERROR", errorCode(t, rec))
	}
}

func TestRefreshRotatesAndDetectsReuse(t *testing.T) {
	h := newHarness(t)
	_, _, first := h.login(t, "risk@parakopru.test", demoPassword)

	// 1) Yenileme: yeni access ve yeni refresh token gelir.
	rec := h.do(t, http.MethodPost, "/api/v1/auth/refresh", "", nil, first)
	require.Equal(t, http.StatusOK, rec.Code)
	second := refreshCookie(rec)
	require.NotNil(t, second)
	require.NotEqual(t, first.Value, second.Value)

	// 2) Eski (artık iptal edilmiş) token tekrar kullanılırsa: reddedilir...
	rec = h.do(t, http.MethodPost, "/api/v1/auth/refresh", "", nil, first)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "SESSION_EXPIRED", errorCode(t, rec))

	// 3) ...ve çalınma şüphesiyle kullanıcının bütün oturumları kapanır; yeni token da artık geçersiz.
	rec = h.do(t, http.MethodPost, "/api/v1/auth/refresh", "", nil, second)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestLogoutRevokesRefreshToken(t *testing.T) {
	h := newHarness(t)
	_, _, cookie := h.login(t, "uyum@parakopru.test", demoPassword)

	rec := h.do(t, http.MethodPost, "/api/v1/auth/logout", "", nil, cookie)
	require.Equal(t, http.StatusNoContent, rec.Code)
	cleared := refreshCookie(rec)
	require.NotNil(t, cleared)
	require.Negative(t, cleared.MaxAge, "çerez silinmeli")

	rec = h.do(t, http.MethodPost, "/api/v1/auth/refresh", "", nil, cookie)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRefreshWithoutCookieOrAfterExpiry(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, http.MethodPost, "/api/v1/auth/refresh", "", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	_, _, cookie := h.login(t, "denetci@parakopru.test", demoPassword)
	h.clock.Advance(7*24*time.Hour + time.Second)
	rec = h.do(t, http.MethodPost, "/api/v1/auth/refresh", "", nil, cookie)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// Aynı refresh token'la eşzamanlı iki yenileme: satır kilidi sayesinde yalnız biri yeni token alır.
func TestConcurrentRefreshOnlyOneSucceeds(t *testing.T) {
	h := newHarness(t)
	_, _, cookie := h.login(t, "sistem@parakopru.test", demoPassword)

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = h.do(t, http.MethodPost, "/api/v1/auth/refresh", "", nil, cookie).Code
		}()
	}
	wg.Wait()

	require.ElementsMatch(t, []int{http.StatusOK, http.StatusUnauthorized}, codes)
}
