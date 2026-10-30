package fx

import (
	"net/http"
	"strconv"
	"time"

	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/httpx"
)

// Handler kur HTTP uçlarıdır.
type Handler struct {
	svc *Service
}

// NewHandler yeni bir Handler oluşturur.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Create: POST /api/v1/fx/rates (izin: fx:write; operasyon uzmanı)
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	rate, err := h.svc.Create(r.Context(), p, in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, rate)
}

// List: GET /api/v1/fx/rates?base=&as_of=&limit= (izin: giriş yapmış her rol; kurlar herkese açık bilgidir)
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var base *money.Currency
	if v := q.Get("base"); v != "" {
		c := money.Currency(v)
		if c != money.USD && c != money.EUR {
			httpx.WriteError(w, r, apperr.ErrValidation.WithMessage("base USD ya da EUR olmalı."))
			return
		}
		base = &c
	}
	var asOf *time.Time
	if v := q.Get("as_of"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			httpx.WriteError(w, r, apperr.ErrValidation.WithMessage("as_of RFC 3339 zamanı olmalı."))
			return
		}
		asOf = &t
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			httpx.WriteError(w, r, apperr.ErrValidation.WithMessage("limit 1 ile 200 arasında olmalı."))
			return
		}
		limit = n
	}
	rates, err := h.svc.List(r.Context(), base, asOf, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if rates == nil {
		rates = []Rate{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": rates})
}
