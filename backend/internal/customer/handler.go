package customer

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/httpx"
)

// Handler müşteri HTTP uçlarıdır. İzin kontrolleri router'dadır.
type Handler struct {
	svc *Service
}

// NewHandler yeni bir Handler oluşturur.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Create: POST /api/v1/customers (izin: customers:write)
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.svc.Create(r.Context(), in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, c)
}

// List: GET /api/v1/customers (izin: ledger:read)
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.ParsePage(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.svc.List(r.Context(), req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

// Get: GET /api/v1/customers/{id} (izin: ledger:read)
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrValidation.WithMessage("id geçerli bir UUID olmalı."))
		return
	}
	c, err := h.svc.Get(r.Context(), nil, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}
