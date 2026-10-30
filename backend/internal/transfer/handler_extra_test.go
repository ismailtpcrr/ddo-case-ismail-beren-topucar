package transfer_test

// Onay, ret ve banka bildirimi uçlarının HTTP katmanı (durum kodları, doğrulama, Idempotency-Key).

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"parakopru/internal/account"
	"parakopru/internal/money"
	"parakopru/internal/platform/auth"
	"parakopru/internal/transfer"
)

func TestApproveRejectAndWebhookHandlers(t *testing.T) {
	a := newApproval(t, 60_000_00)
	h := transfer.NewHandler(a.transfer)
	r := chi.NewRouter()
	r.Post("/transfers", h.Create)
	r.Get("/transfers", h.List)
	r.Post("/transfers/{id}/approve", h.Approve)
	r.Post("/transfers/{id}/reject", h.Reject)
	r.Post("/webhooks/bank/transfer-status", h.BankStatus)
	r.Post("/webhooks/bank/incoming", h.Incoming)
	call := func(p auth.Principal, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(auth.WithPrincipal(t.Context(), p), http.MethodPost, path, strings.NewReader(body))
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	create := func(key string, minor int64) transfer.Transfer {
		t.Helper()
		rec := call(a.customer, "/transfers", key, fmt.Sprintf(`{"from_account_id":"%s","to_iban":"%s","amount_minor":%d,"currency":"TRY"}`, a.from.ID, *a.to.IBAN, minor))
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String(), "10.000 TL üzeri: 202 onay bekliyor")
		return a.mustGetTransfer(t, rec.Body.Bytes())
	}
	t1, t2 := create("h-1", 15_000_00), create("h-2", 16_000_00)

	// Onay: anahtar zorunlu, geçersiz id 400, isteyen 403 (servis kuralı), uyum 200, ikinci onay 409.
	require.Equal(t, http.StatusBadRequest, call(a.approver, "/transfers/"+t1.ID.String()+"/approve", "", "").Code)
	require.Equal(t, http.StatusBadRequest, call(a.approver, "/transfers/bozuk/approve", "k", "").Code)
	rec := call(a.customer, "/transfers/"+t1.ID.String()+"/approve", "k-self", "")
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "FOUR_EYES_VIOLATION")
	rec = call(a.approver, "/transfers/"+t1.ID.String()+"/approve", "k-ok", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"state":"COMPLETED"`)
	rec = call(a.approver, "/transfers/"+t1.ID.String()+"/approve", "k-ok", "")
	require.Equal(t, "true", rec.Header().Get("Idempotent-Replayed"))
	require.Equal(t, http.StatusConflict, call(a.approver, "/transfers/"+t1.ID.String()+"/approve", "k-again", "").Code)

	// Ret: gerekçe zorunlu.
	require.Equal(t, http.StatusBadRequest, call(a.approver, "/transfers/"+t2.ID.String()+"/reject", "k-r0", `{"reason":""}`).Code)
	rec = call(a.approver, "/transfers/"+t2.ID.String()+"/reject", "k-r1", `{"reason":"şüpheli işlem"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"state":"REJECTED"`)

	// Banka bildirimi: doğrulama ve bilinmeyen referans.
	sys := auth.Principal{UserID: uuid.New(), Role: auth.RoleSystem}
	require.Equal(t, http.StatusBadRequest, call(sys, "/webhooks/bank/transfer-status", "", `{"reference":"x"}`).Code)
	require.Equal(t, http.StatusNotFound, call(sys, "/webhooks/bank/transfer-status", "",
		fmt.Sprintf(`{"reference":"%s","status":"COMPLETED","amount_minor":100,"currency":"TRY"}`, uuid.New())).Code)

	// Gelen transfer: ilk 200 duplicate=false, tekrar duplicate=true; bilinmeyen alıcı 422.
	_, to := a.owned(t, money.TRY, 0)
	body := fmt.Sprintf(`{"reference":"hook-%s","beneficiary_iban":"%s","amount_minor":500,"currency":"TRY"}`, uuid.NewString(), *to.IBAN)
	rec = call(sys, "/webhooks/bank/incoming", "", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"duplicate":false`)
	require.Contains(t, call(sys, "/webhooks/bank/incoming", "", body).Body.String(), `"duplicate":true`)
	require.Equal(t, http.StatusUnprocessableEntity, call(sys, "/webhooks/bank/incoming", "",
		fmt.Sprintf(`{"reference":"x-%s","beneficiary_iban":"%s","amount_minor":5,"currency":"TRY"}`, uuid.NewString(), account.NewIBAN(777_777_777))).Code)

	// Liste süzgeci.
	req := httptest.NewRequestWithContext(auth.WithPrincipal(t.Context(), a.ops2), http.MethodGet, "/transfers?state=REJECTED", nil)
	lr := httptest.NewRecorder()
	r.ServeHTTP(lr, req)
	require.Equal(t, http.StatusOK, lr.Code)
	require.Contains(t, lr.Body.String(), t2.ID.String())
	req = httptest.NewRequestWithContext(auth.WithPrincipal(t.Context(), a.ops2), http.MethodGet, "/transfers?state=BOZUK", nil)
	lr = httptest.NewRecorder()
	r.ServeHTTP(lr, req)
	require.Equal(t, http.StatusBadRequest, lr.Code)
}
