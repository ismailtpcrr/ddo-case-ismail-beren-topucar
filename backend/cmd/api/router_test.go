package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"parakopru/internal/account"
	"parakopru/internal/customer"
	"parakopru/internal/ledger"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/health"
	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/observability"
)

const testSecret = "router-test-secret-0123456789-0123456789"

// Rolün izni olmayan uç 403 döner; servis hiç çağrılmaz (denetim kaydını yalnız uyum ve denetçi görür).
func TestRoleWithoutPermissionGets403(t *testing.T) {
	s := newTestServer(t, nil, nil)
	tokens := auth.NewTokens(testSecret, time.Minute, time.Hour, clock.System{})

	for _, role := range []auth.Role{auth.RoleCustomer, auth.RoleOperations, auth.RoleRiskAnalyst, auth.RoleSystem} {
		token, _, err := tokens.IssueAccess(uuid.New(), role)
		require.NoError(t, err)
		rec := s.do(t, http.MethodGet, "/api/v1/audit-log", http.Header{"Authorization": {"Bearer " + token}})
		require.Equal(t, http.StatusForbidden, rec.Code, role)
		require.Equal(t, "FORBIDDEN", errorCode(t, rec))
	}
}

// Hesap açma yalnız operasyon uzmanının işi; müşteri tüm müşteri listesini göremez (PLAN.md §5.10).
// Defter uçlarında da rolün izni yoksa servis hiç çağrılmadan 403 döner.
func TestCustomerAndAccountWritePermissions(t *testing.T) {
	s := newTestServer(t, nil, nil)
	tokens := auth.NewTokens(testSecret, time.Minute, time.Hour, clock.System{})
	for _, tc := range []struct {
		role         auth.Role
		method, path string
	}{
		{auth.RoleCustomer, http.MethodPost, "/api/v1/accounts"},
		{auth.RoleAuditor, http.MethodPost, "/api/v1/accounts"},
		{auth.RoleRiskAnalyst, http.MethodPost, "/api/v1/customers"},
		{auth.RoleCustomer, http.MethodGet, "/api/v1/customers"},
		{auth.RoleSystem, http.MethodGet, "/api/v1/accounts"},
		// Defter: kayıt ayrıntısı yalnız ledger:read; elle ters kayıt yalnız operasyon (denetçi her yerde salt okur).
		{auth.RoleCustomer, http.MethodGet, "/api/v1/ledger/entries/" + uuid.NewString()},
		{auth.RoleSystem, http.MethodGet, "/api/v1/accounts/" + uuid.NewString() + "/entries"},
		{auth.RoleCustomer, http.MethodPost, "/api/v1/ledger/entries/" + uuid.NewString() + "/reverse"},
		{auth.RoleAuditor, http.MethodPost, "/api/v1/ledger/entries/" + uuid.NewString() + "/reverse"},
		{auth.RoleCompliance, http.MethodPost, "/api/v1/ledger/entries/" + uuid.NewString() + "/reverse"},
		{auth.RoleRiskAnalyst, http.MethodPost, "/api/v1/ledger/entries/" + uuid.NewString() + "/reverse"},
	} {
		token, _, err := tokens.IssueAccess(uuid.New(), tc.role)
		require.NoError(t, err)
		rec := s.do(t, tc.method, tc.path, http.Header{"Authorization": {"Bearer " + token}})
		require.Equal(t, http.StatusForbidden, rec.Code, "%s %s %s", tc.role, tc.method, tc.path)
	}
}

// Korunan uçlar token olmadan ya da geçersiz token'la 401 döner; servis hiç çağrılmaz.
func TestProtectedRoutesRequireToken(t *testing.T) {
	s := newTestServer(t, nil, nil)

	rec := s.do(t, http.MethodGet, "/api/v1/me", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "UNAUTHORIZED", errorCode(t, rec))
	require.Contains(t, rec.Header().Get("WWW-Authenticate"), "Bearer")

	for _, header := range []string{"Bearer bozuk.token.degeri", "Basic dXNlcjpwYXNz", "Bearer"} {
		rec = s.do(t, http.MethodGet, "/api/v1/me", http.Header{"Authorization": {header}})
		require.Equal(t, http.StatusUnauthorized, rec.Code, header)
	}
}

type testServer struct {
	handler http.Handler
	logs    *bytes.Buffer
}

func newTestServer(t *testing.T, checks []health.Check, extra func(chi.Router)) testServer {
	t.Helper()
	logs := &bytes.Buffer{}
	h := newRouter(routerDeps{
		Logger:         observability.NewLogger(logs, slog.LevelDebug, "api"),
		IDs:            &ids.Sequence{},
		RequestTimeout: 50 * time.Millisecond,
		HealthChecks:   checks,
		Tokens:         auth.NewTokens(testSecret, time.Minute, time.Hour, clock.System{}),
		Auth:           auth.NewHandler(nil, false), // bu testler servis gerektiren uçlara ulaşmaz
		Audit:          audit.NewHandler(nil),
		Customers:      customer.NewHandler(nil),
		Accounts:       account.NewHandler(nil),
		Ledger:         ledger.NewHandler(nil),
	})
	if extra != nil {
		extra(h.(chi.Router))
	}
	return testServer{handler: h, logs: logs}
}

func (s testServer) do(t *testing.T, method, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEmpty(t, body.Error.Message)
	return body.Error.Code
}

func TestHealthOK(t *testing.T) {
	s := newTestServer(t, []health.Check{{Name: "database", Run: func(context.Context) error { return nil }}}, nil)

	rec := s.do(t, http.MethodGet, "/health", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"status":"ok","checks":{"database":"ok"}}`, rec.Body.String())
}

func TestHealthUnavailableWhenDatabaseDown(t *testing.T) {
	failing := health.Check{Name: "database", Run: func(context.Context) error {
		return errors.New("dial tcp 10.0.0.5:5432: connection refused")
	}}
	s := newTestServer(t, []health.Check{failing}, nil)

	rec := s.do(t, http.MethodGet, "/health", nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.JSONEq(t, `{"status":"unavailable","checks":{"database":"unavailable"}}`, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "10.0.0.5", "iç ayrıntı yanıtta olmamalı")
	require.Contains(t, s.logs.String(), "connection refused", "ayrıntı loga yazılmalı")
}

func TestUnknownRouteReturnsJSON404(t *testing.T) {
	s := newTestServer(t, nil, nil)

	rec := s.do(t, http.MethodGet, "/yok", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "NOT_FOUND", errorCode(t, rec))
}

func TestWrongMethodReturnsJSON405(t *testing.T) {
	s := newTestServer(t, nil, nil)

	rec := s.do(t, http.MethodDelete, "/health", nil)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, "METHOD_NOT_ALLOWED", errorCode(t, rec))
}

func TestRequestIDGeneratedOrPropagated(t *testing.T) {
	s := newTestServer(t, nil, nil)

	rec := s.do(t, http.MethodGet, "/health", nil)
	require.Equal(t, "00000000-0000-7000-8000-000000000001", rec.Header().Get(httpx.RequestIDHeader))

	rec = s.do(t, http.MethodGet, "/health", http.Header{"X-Request-Id": {"istemci-123"}})
	require.Equal(t, "istemci-123", rec.Header().Get(httpx.RequestIDHeader))

	// Satır sonu içeren kimlik log enjeksiyonu olabilir; yerine yenisi üretilir.
	rec = s.do(t, http.MethodGet, "/health", http.Header{"X-Request-Id": {"kötü\nsatır"}})
	require.NotContains(t, rec.Header().Get(httpx.RequestIDHeader), "\n")
	require.NotEmpty(t, rec.Header().Get(httpx.RequestIDHeader))
}

func TestPanicBecomesJSON500AndIsLogged(t *testing.T) {
	s := newTestServer(t, nil, func(r chi.Router) {
		r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("beklenmedik durum") })
	})

	rec := s.do(t, http.MethodGet, "/boom", nil)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "INTERNAL", errorCode(t, rec))
	require.NotContains(t, rec.Body.String(), "beklenmedik durum")
	require.Contains(t, s.logs.String(), "panic recovered")
	require.Contains(t, s.logs.String(), `"status":500`, "access log 500'ü kaydetmeli")

	// Süreç ayakta: sonraki istek normal çalışır.
	require.Equal(t, http.StatusOK, s.do(t, http.MethodGet, "/health", nil).Code)
}

func TestRequestTimeoutBecomes504(t *testing.T) {
	s := newTestServer(t, nil, func(r chi.Router) {
		r.Get("/slow", func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done() // ör. süren bir veritabanı sorgusu
			httpx.WriteError(w, r, r.Context().Err())
		})
	})

	rec := s.do(t, http.MethodGet, "/slow", nil)
	require.Equal(t, http.StatusGatewayTimeout, rec.Code)
	require.Equal(t, "TIMEOUT", errorCode(t, rec))
}

func TestAccessLogLine(t *testing.T) {
	s := newTestServer(t, nil, nil)
	s.do(t, http.MethodGet, "/yok?q=1", http.Header{"Authorization": {"Bearer gizli-token"}})

	var line map[string]any
	last := strings.TrimSpace(s.logs.String())
	require.NoError(t, json.Unmarshal([]byte(last[strings.LastIndex(last, "\n")+1:]), &line))
	require.Equal(t, "http request", line["msg"])
	require.Equal(t, "GET", line["method"])
	require.Equal(t, "/yok", line["path"])
	require.EqualValues(t, 404, line["status"])
	require.NotEmpty(t, line["request_id"])
	require.Contains(t, line, "duration_ms")
	require.NotContains(t, s.logs.String(), "gizli-token")
}
