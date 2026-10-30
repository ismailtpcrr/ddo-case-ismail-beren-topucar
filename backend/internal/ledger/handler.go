package ledger

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/idempotency"
)

// Handler defter HTTP uçlarıdır. Uç seviyesindeki izinler router'da, nesne seviyesindeki kural serviste.
type Handler struct {
	svc *Service
}

// NewHandler yeni bir Handler oluşturur.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, apperr.ErrValidation.WithMessage("id geçerli bir UUID olmalı.")
	}
	return id, nil
}

// AccountEntries: GET /api/v1/accounts/{id}/entries?limit=&cursor= (izin: own_banking ya da ledger:read;
// müşteri yalnız kendi hesabı)
func (h *Handler) AccountEntries(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, err := httpx.ParsePage(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	list, err := h.svc.AccountPostings(r.Context(), p, id, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

// Entry: GET /api/v1/ledger/entries/{id} (izin: ledger:read)
func (h *Handler) Entry(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	e, err := h.svc.Entry(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e.ToResponse())
}

// Reverse: POST /api/v1/ledger/entries/{id}/reverse [Idempotency-Key] (izin: ledger:reverse)
func (h *Handler) Reverse(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	key, err := idempotency.FromRequest(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var in ReverseInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	res, replayed, err := h.svc.ReverseByRequest(r.Context(), p, key, id, in)
	idempotency.MarkReplayed(w, replayed) // kayıtlı bir ret de tekrar dönüyorsa işaretlenir
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, res)
}

// Invariants: GET /api/v1/admin/invariants (izin: ledger:read). Defter değişmezlerini ölçer (T-1 kanıtı).
func (h *Handler) Invariants(w http.ResponseWriter, r *http.Request) {
	inv, err := h.svc.CheckInvariants(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, inv)
}
