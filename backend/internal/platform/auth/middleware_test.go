package auth

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/observability"
)

// Bir izin isteyen örnek uç: denetim kaydı (uyum ve denetçi görebilir, müşteri göremez).
func newProtectedRouter(tokens *Tokens, logs *bytes.Buffer) http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.RequestID(&ids.Sequence{}, observability.NewLogger(logs, slog.LevelDebug, "api")), httpx.AccessLog)
	r.With(Authenticate(tokens), Require(PermAuditRead)).Get("/audit-log", func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r.Context())
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"role": string(p.Role)})
	})
	return r
}

func callWithRole(t *testing.T, h http.Handler, tokens *Tokens, role Role, userID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	token, _, err := tokens.IssueAccess(userID, role)
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/audit-log", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRequireAllowsAndForbidsByPermission(t *testing.T) {
	tokens, _ := newTestTokens()
	h := newProtectedRouter(tokens, &bytes.Buffer{})

	for _, role := range Roles {
		rec := callWithRole(t, h, tokens, role, uuid.New())
		if role.Has(PermAuditRead) {
			require.Equal(t, http.StatusOK, rec.Code, role)
		} else {
			require.Equal(t, http.StatusForbidden, rec.Code, role)
			require.Contains(t, rec.Body.String(), `"code":"FORBIDDEN"`)
		}
	}
}

func TestRequireWithoutAuthenticateIsUnauthorized(t *testing.T) {
	h := Require(PermProfileRead)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler çalışmamalı")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAccessLogIncludesUserID(t *testing.T) {
	tokens, _ := newTestTokens()
	logs := &bytes.Buffer{}
	h := newProtectedRouter(tokens, logs)
	userID := uuid.New()

	rec := callWithRole(t, h, tokens, RoleAuditor, userID)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, logs.String(), `"user_id":"`+userID.String()+`"`)
	require.NotContains(t, logs.String(), "Bearer", "token loglanmamalı")
}
