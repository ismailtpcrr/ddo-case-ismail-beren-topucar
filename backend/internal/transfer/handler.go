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
// 201: transfer tamamlandı. 422 INSUFFICIENT_FUNDS: yetersiz bakiye; bu da bir sonuçtur ve REJECTED bir transfer
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
	if t.State == Rejected {
		w.Header().Set("X-Transfer-ID", t.ID.String())
		httpx.WriteError(w, r, apperr.New(http.StatusUnprocessableEntity, *t.FailureCode, "Hesapta yeterli kullanılabilir bakiye yok."))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, t)
}

// List: GET /api/v1/transfers?limit=&cursor= (izin: own_banking ya da ledger:read)
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.ParsePage(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	out, err := h.svc.List(r.Context(), p, req)
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
