package transfer

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/idempotency"
)

// Handler transfer HTTP uçlarıdır. Uç izinleri router'da, nesne düzeyi yetki serviste.
type Handler struct {
	svc *Service
}

// NewHandler yeni bir Handler oluşturur.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Create: POST /api/v1/transfers [Idempotency-Key] (izin: transfers:create)
//
// 201: banka içi transfer tamamlandı. 202: dış transfer kabul edildi (RESERVED); sonucu GET /transfers/{id} ile izlenir. 422 INSUFFICIENT_FUNDS: yetersiz bakiye; bu da bir sonuçtur ve REJECTED bir transfer
// kaydı yazılır (kimliği yanıt başlığında).
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	key, err := idempotency.FromRequest(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var in CreateInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	t, replayed, err := h.svc.Create(r.Context(), p, key, in)
	idempotency.MarkReplayed(w, replayed)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if t.State == PendingApproval { // 10.000 TL üzeri: dört göz onayı bekliyor, para hareket etmedi
		httpx.WriteJSON(w, http.StatusAccepted, t)
		return
	}
	if t.State == Reserved { // dış transfer: kabul edildi, bankaya gönderim arka planda (worker) yapılır
		httpx.WriteJSON(w, http.StatusAccepted, t)
		return
	}
	if t.State == Rejected {
		w.Header().Set("X-Transfer-ID", t.ID.String())
		httpx.WriteError(w, r, apperr.New(http.StatusUnprocessableEntity, *t.FailureCode, "Hesapta yeterli kullanılabilir bakiye yok."))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, t)
}

// List: GET /api/v1/transfers?state=&limit=&cursor= (izin: own_banking ya da ledger:read)
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.ParsePage(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var state *State
	if v := r.URL.Query().Get("state"); v != "" {
		st := State(v)
		if _, known := transitions[st]; !known && st != Completed && st != Rejected && st != Reversed {
			httpx.WriteError(w, r, apperr.ErrValidation.WithMessage("state geçerli bir transfer durumu olmalı."))
			return
		}
		state = &st
	}
	p, _ := auth.PrincipalFrom(r.Context())
	out, err := h.svc.List(r.Context(), p, state, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// Get: GET /api/v1/transfers/{id} (izin: own_banking ya da ledger:read)
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrValidation.WithMessage("id geçerli bir UUID olmalı."))
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	d, err := h.svc.Get(r.Context(), p, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

// BankStatus: POST /api/v1/webhooks/bank/transfer-status (izin: system:callbacks). Bankanın sonuç bildirimi; çift cevap sorun değildir.
func (h *Handler) BankStatus(w http.ResponseWriter, r *http.Request) {
	var in BankResult
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.svc.ApplyBankResult(r.Context(), in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Incoming: POST /api/v1/webhooks/bank/incoming (izin: system:callbacks). Bankadan gelen transferi müşteriye yazar;
// aynı banka referansı tekrar gelirse ikinci kez yazılmaz (duplicate: true).
func (h *Handler) Incoming(w http.ResponseWriter, r *http.Request) {
	var in IncomingTransfer
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	dup, err := h.svc.ReceiveIncoming(r.Context(), in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true, "duplicate": dup})
}

func decisionPrep(w http.ResponseWriter, r *http.Request) (uuid.UUID, string, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrValidation.WithMessage("id geçerli bir UUID olmalı."))
		return uuid.Nil, "", false
	}
	key, err := idempotency.FromRequest(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return uuid.Nil, "", false
	}
	return id, key, true
}

// Approve: POST /api/v1/transfers/{id}/approve [Idempotency-Key] (izin: transfers:approve; isteyen kişi onaylayamaz)
func (h *Handler) Approve(w http.ResponseWriter, r *http.Request) {
	id, key, ok := decisionPrep(w, r)
	if !ok {
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	t, replayed, err := h.svc.Approve(r.Context(), p, key, id)
	idempotency.MarkReplayed(w, replayed)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

// Reject: POST /api/v1/transfers/{id}/reject [Idempotency-Key] (izin: transfers:approve)
func (h *Handler) Reject(w http.ResponseWriter, r *http.Request) {
	id, key, ok := decisionPrep(w, r)
	if !ok {
		return
	}
	var in RejectInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	t, replayed, err := h.svc.Reject(r.Context(), p, key, id, in)
	idempotency.MarkReplayed(w, replayed)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}
