// Package audit, eklemeli-yalnız denetim kaydını yazar ve okur (proje.md §7).
//
// Her kritik işlem, iş değişikliğiyle aynı transaction içinde Recorder.Record ile kaydedilir: işlem
// geri alınırsa denetim satırı da geri alınır, commit edilirse ikisi birlikte kalıcı olur. Tabloda
// UPDATE/DELETE/TRUNCATE hem yetkiyle hem trigger'la engellidir; kayıt sonradan değiştirilemez.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/httpx"
	"parakopru/internal/platform/ids"
)

// Entry bir denetim kaydıdır: neyin (Entity/EntityID) nasıl değiştiği (Action, Old → New).
// Kimin yaptığı ve hangi istekle yapıldığı context'ten alınır.
type Entry struct {
	Action   string // ör. transfer.approve
	Entity   string // ör. transfer
	EntityID string
	Old      any // JSON'a çevrilir; yoksa nil
	New      any
}

// Recorder denetim kaydı yazar.
type Recorder struct {
	ids ids.Generator
}

// NewRecorder yeni bir Recorder oluşturur.
func NewRecorder(idGen ids.Generator) *Recorder { return &Recorder{ids: idGen} }

type actorKey struct{}

// WithSystemActor, kullanıcı isteği olmadan çalışan süreçlerin (worker, zamanlanmış işler) kayıtlarının
// "SYSTEM" rolüyle yazılmasını sağlar.
func WithSystemActor(ctx context.Context) context.Context {
	return context.WithValue(ctx, actorKey{}, auth.RoleSystem)
}

// Record kaydı verilen transaction içinde yazar.
func (r *Recorder) Record(ctx context.Context, tx db.DBTX, e Entry) error {
	var actorID *uuid.UUID
	actorRole := string(auth.RoleSystem)
	if p, ok := auth.PrincipalFrom(ctx); ok {
		actorID, actorRole = &p.UserID, string(p.Role)
	} else if _, ok := ctx.Value(actorKey{}).(auth.Role); !ok {
		// Ne kullanıcı ne de sistem işaretli: kim yaptığı bilinmeyen bir kayıt yazılmaz.
		return fmt.Errorf("audit: no actor in context for %s", e.Action)
	}

	oldJSON, err := toJSON(e.Old)
	if err != nil {
		return err
	}
	newJSON, err := toJSON(e.New)
	if err != nil {
		return err
	}
	var requestID *string
	if id := httpx.RequestIDFrom(ctx); id != "" {
		requestID = &id
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (id, actor_id, actor_role, action, entity, entity_id, old_value, new_value, request_id)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9)`,
		r.ids.NewID(), actorID, actorRole, e.Action, e.Entity, e.EntityID, oldJSON, newJSON, requestID)
	if err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}

func toJSON(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("audit: marshal: %w", err)
	}
	return b, nil
}
