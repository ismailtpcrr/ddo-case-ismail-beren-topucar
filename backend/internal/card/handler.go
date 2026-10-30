package card

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/idempotency"
)

// Handler kart HTTP uçlarıdır. Uç izinleri router'da; nesne düzeyi yetki serviste.
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

// prepare her para hareketi isteği için ortak ön işlemdir: yol kimliği, Idempotency-Key, gövde.
func prepare(w http.ResponseWriter, r *http.Request, body any) (uuid.UUID, string, auth.Principal, bool) {
	id, err := pathID(r)
	if err == nil {
		var key string
		if key, err = idempotency.FromRequest(r); err == nil {
			if err = httpx.DecodeJSON(w, r, body); err == nil {
				p, _ := auth.PrincipalFrom(r.Context())
				return id, key, p, true
			}
		}
	}
	httpx.WriteError(w, r, err)
	return uuid.Nil, "", auth.Principal{}, false
}

// Authorize: POST /api/v1/cards/{id}/authorizations [Idempotency-Key] (izin: system:callbacks; kart ağı)
func (h *Handler) Authorize(w http.ResponseWriter, r *http.Request) {
	var in AuthorizeInput
	id, key, p, ok := prepare(w, r, &in)
	if !ok {
		return
	}
	res, replayed, err := h.svc.Authorize(r.Context(), p, key, id, in)
	idempotency.MarkReplayed(w, replayed)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, res)
}

// Capture: POST /api/v1/holds/{id}/captures [Idempotency-Key] (izin: system:callbacks)
func (h *Handler) Capture(w http.ResponseWriter, r *http.Request) {
	var in CaptureInput
	id, key, p, ok := prepare(w, r, &in)
	if !ok {
		return
	}
	res, replayed, err := h.svc.Capture(r.Context(), p, key, id, in)
	idempotency.MarkReplayed(w, replayed)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, res)
}

// Refund: POST /api/v1/captures/{id}/refunds [Idempotency-Key] (izin: system:callbacks)
func (h *Handler) Refund(w http.ResponseWriter, r *http.Request) {
	var in RefundInput
	id, key, p, ok := prepare(w, r, &in)
	if !ok {
		return
	}
	res, replayed, err := h.svc.Refund(r.Context(), p, key, id, in)
	idempotency.MarkReplayed(w, replayed)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, res)
}

// Cards: GET /api/v1/cards (izin: own_banking ya da ledger:read)
func (h *Handler) Cards(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.ParsePage(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	out, err := h.svc.Cards(r.Context(), p, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// Holds: GET /api/v1/accounts/{id}/holds?state= (izin: own_banking ya da ledger:read; müşteri yalnız kendi hesabı)
func (h *Handler) Holds(w http.ResponseWriter, r *http.Request) {
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
	var state *State
	if v := r.URL.Query().Get("state"); v != "" {
		st := State(v)
		if st != Active && st != Captured && st != Expired {
			httpx.WriteError(w, r, apperr.ErrValidation.WithMessage("state ACTIVE, CAPTURED ya da EXPIRED olmalı."))
			return
		}
		state = &st
	}
	p, _ := auth.PrincipalFrom(r.Context())
	out, err := h.svc.Holds(r.Context(), p, id, state, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
