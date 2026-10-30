package account

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/page"
)

// Handler hesap HTTP uçlarıdır. Uç seviyesindeki izinler router'da, nesne seviyesindeki kural serviste.
type Handler struct {
	svc *Service
}

// NewHandler yeni bir Handler oluşturur.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Open: POST /api/v1/accounts (izin: customers:write)
func (h *Handler) Open(w http.ResponseWriter, r *http.Request) {
	var in OpenInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a, err := h.svc.Open(r.Context(), in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, a.ToResponse())
}

// List: GET /api/v1/accounts?customer_id=&limit=&cursor= (izin: own_banking ya da ledger:read)
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.ParsePage(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var customerID *uuid.UUID
	if v := r.URL.Query().Get("customer_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.WriteError(w, r, apperr.ErrValidation.WithMessage("customer_id geçerli bir UUID olmalı."))
			return
		}
		customerID = &id
	}
	p, _ := auth.PrincipalFrom(r.Context())
	accounts, err := h.svc.List(r.Context(), p, customerID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := page.Page[Response]{Items: make([]Response, len(accounts.Items)), NextCursor: accounts.NextCursor}
	for i, a := range accounts.Items {
		out.Items[i] = a.ToResponse()
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// Get: GET /api/v1/accounts/{id} (izin: own_banking ya da ledger:read; müşteri yalnız kendi hesabı)
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.ErrValidation.WithMessage("id geçerli bir UUID olmalı."))
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	a, err := h.svc.Get(r.Context(), p, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.ToResponse())
}
