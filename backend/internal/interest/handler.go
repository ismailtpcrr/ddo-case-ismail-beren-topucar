package interest

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/httpx"
)

// Handler gün sonu HTTP uçlarıdır.
type Handler struct {
	svc *Service
}

// NewHandler yeni bir Handler oluşturur.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Start: POST /api/v1/eod/runs {"business_date":"YYYY-MM-DD"} (izin: eod:run; operasyon ve sistem). 202: başlatıldı
// (worker yürütür); 200: bu gün daha önce başlatılmış, mevcut çalıştırma döner (çift tahakkuk yok).
func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	var in RunInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	run, created, err := h.svc.Start(r.Context(), p, in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	httpx.WriteJSON(w, status, run)
}

// Get: GET /api/v1/eod/runs/{date} (izin: eod:run ya da ledger:read)
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	run, err := h.svc.Get(r.Context(), chi.URLParam(r, "date"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, run)
}
