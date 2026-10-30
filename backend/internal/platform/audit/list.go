package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/page"
)

// Record denetim kaydının okunan halidir.
type Record struct {
	ID        uuid.UUID       `json:"id"`
	At        time.Time       `json:"at"`
	ActorID   *uuid.UUID      `json:"actor_id"`
	ActorRole string          `json:"actor_role"`
	Action    string          `json:"action"`
	Entity    string          `json:"entity"`
	EntityID  *string         `json:"entity_id"`
	OldValue  json.RawMessage `json:"old_value"`
	NewValue  json.RawMessage `json:"new_value"`
	RequestID *string         `json:"request_id"`
}

// Filter listeleme süzgecidir; boş alanlar uygulanmaz.
type Filter struct {
	Entity   string
	EntityID string
}

type cursor struct {
	ID uuid.UUID `json:"id"`
}

// Service denetim kaydını okur.
type Service struct {
	db db.DBTX
}

// NewService yeni bir Service oluşturur.
func NewService(q db.DBTX) *Service { return &Service{db: q} }

// List kayıtları en yeniden eskiye, keyset sayfalı döndürür.
func (s *Service) List(ctx context.Context, f Filter, req page.Request) (page.Page[Record], error) {
	return listRecords(ctx, s.db, f, req)
}

// listRecords (repo) — UUIDv7 kimlikler zamana göre sıralı olduğundan sıralama anahtarı doğrudan
// kimliktir; birincil anahtar dizini yeterlidir.
func listRecords(ctx context.Context, q db.DBTX, f Filter, req page.Request) (page.Page[Record], error) {
	var after *uuid.UUID
	if req.Cursor != "" {
		var c cursor
		if err := page.Decode(req.Cursor, &c); err != nil {
			return page.Page[Record]{}, err
		}
		after = &c.ID
	}

	rows, err := q.Query(ctx,
		`SELECT id, at, actor_id, actor_role, action, entity, entity_id, old_value, new_value, request_id
		   FROM audit_log
		  WHERE ($1::uuid IS NULL OR id < $1)
		    AND ($2 = '' OR entity = $2)
		    AND ($3 = '' OR entity_id = $3)
		  ORDER BY id DESC
		  LIMIT $4`,
		after, f.Entity, f.EntityID, req.Limit+1)
	if err != nil {
		return page.Page[Record]{}, fmt.Errorf("audit: list: %w", err)
	}
	records, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Record, error) {
		var r Record
		err := row.Scan(&r.ID, &r.At, &r.ActorID, &r.ActorRole, &r.Action, &r.Entity, &r.EntityID,
			&r.OldValue, &r.NewValue, &r.RequestID)
		return r, err
	})
	if err != nil {
		return page.Page[Record]{}, fmt.Errorf("audit: list: %w", err)
	}
	return page.New(records, req.Limit, func(r Record) any { return cursor{ID: r.ID} })
}

// Handler GET /api/v1/audit-log ucudur. İzin kontrolü (audit:read) router'da yapılır.
type Handler struct {
	svc *Service
}

// NewHandler yeni bir Handler oluşturur.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// List: GET /api/v1/audit-log?entity=&entity_id=&limit=&cursor=
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.ParsePage(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := Filter{Entity: r.URL.Query().Get("entity"), EntityID: r.URL.Query().Get("entity_id")}
	if len(f.Entity) > 100 || len(f.EntityID) > 100 {
		httpx.WriteError(w, r, apperr.ErrValidation.WithMessage("entity ve entity_id en fazla 100 karakter olmalı."))
		return
	}
	p, err := h.svc.List(r.Context(), f, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}
