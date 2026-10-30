package ledger

import (
	"net/http"
	"time"

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

// AccountEntries: GET /api/v1/accounts/{id}/entries?from=&to=&as_of=&known_at=&limit=&cursor= (izin: own_banking ya da ledger:read;
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
	var f PostingFilter
	q := r.URL.Query()
	if f.From, err = parseTime("from", q.Get("from"), false); err == nil {
		if f.To, err = parseTime("to", firstNonEmpty(q.Get("to"), q.Get("as_of")), true); err == nil {
			f.KnownAt, err = parseTime("known_at", q.Get("known_at"), true)
		}
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	list, err := h.svc.AccountPostings(r.Context(), p, id, f, req)
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

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// parseTime RFC 3339 zamanını ya da YYYY-MM-DD tarihini okur. Yalnız tarih verilirse UTC günün başı (endOfDay=false)
// ya da sonu (endOfDay=true) alınır: "as_of=2026-10-30" o günün kapanış bakiyesidir.
func parseTime(name, v string, endOfDay bool) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return &t, nil
	}
	d, err := time.Parse(time.DateOnly, v)
	if err != nil {
		return nil, apperr.ErrValidation.Messagef("%s RFC 3339 zamanı (2026-10-30T12:00:00Z) ya da YYYY-MM-DD tarihi olmalı.", name)
	}
	if endOfDay {
		d = d.Add(24*time.Hour - time.Microsecond)
	}
	return &d, nil
}

// Balance: GET /api/v1/accounts/{id}/balance?as_of=&known_at= (izin: own_banking ya da ledger:read; as_of ve known_at
// yalnız ledger:read: zaman yolculuğu). as_of geçerlilik, known_at kayıt zamanıdır.
func (h *Handler) Balance(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q := r.URL.Query()
	asOf, err := parseTime("as_of", q.Get("as_of"), true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	knownAt, err := parseTime("known_at", q.Get("known_at"), true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	out, err := h.svc.AccountBalance(r.Context(), p, id, asOf, knownAt)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
