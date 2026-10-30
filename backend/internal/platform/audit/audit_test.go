package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/page"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func asUser(role auth.Role) (context.Context, uuid.UUID) {
	id := uuid.New()
	return auth.WithPrincipal(context.Background(), auth.Principal{UserID: id, Role: role}), id
}

type transferChange struct {
	State string `json:"state"`
}

func TestRecordStoresWhoWhatOldAndNew(t *testing.T) {
	env := dbtest.Get(t)
	rec := audit.NewRecorder(ids.V7{})
	ctx, userID := asUser(auth.RoleCompliance)
	entityID := uuid.NewString()

	err := db.NewTxManager(env.App).WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		return rec.Record(ctx, tx, audit.Entry{
			Action: "transfer.approve", Entity: "transfer", EntityID: entityID,
			Old: transferChange{State: "PENDING_APPROVAL"}, New: transferChange{State: "RESERVED"},
		})
	})
	require.NoError(t, err)

	p, err := audit.NewService(env.App).List(t.Context(), audit.Filter{Entity: "transfer", EntityID: entityID}, page.Request{Limit: 10})
	require.NoError(t, err)
	require.Len(t, p.Items, 1)
	got := p.Items[0]
	require.Equal(t, &userID, got.ActorID)
	require.Equal(t, "COMPLIANCE", got.ActorRole)
	require.Equal(t, "transfer.approve", got.Action)
	require.JSONEq(t, `{"state":"PENDING_APPROVAL"}`, string(got.OldValue))
	require.JSONEq(t, `{"state":"RESERVED"}`, string(got.NewValue))
	require.False(t, got.At.IsZero())
}

func TestRecordAsSystemAndWithoutActor(t *testing.T) {
	env := dbtest.Get(t)
	rec := audit.NewRecorder(ids.V7{})
	entityID := uuid.NewString()

	// Worker gibi kullanıcısız süreçler sistem olarak kaydedilir.
	err := rec.Record(audit.WithSystemActor(t.Context()), env.App, audit.Entry{Action: "hold.expire", Entity: "hold", EntityID: entityID})
	require.NoError(t, err)
	p, err := audit.NewService(env.App).List(t.Context(), audit.Filter{EntityID: entityID}, page.Request{Limit: 10})
	require.NoError(t, err)
	require.Len(t, p.Items, 1)
	require.Equal(t, "SYSTEM", p.Items[0].ActorRole)
	require.Nil(t, p.Items[0].ActorID)

	// Kim yaptığı bilinmeyen bir kayıt yazılmaz.
	err = rec.Record(t.Context(), env.App, audit.Entry{Action: "x", Entity: "y"})
	require.ErrorContains(t, err, "no actor")
}

// Denetim satırı iş değişikliğiyle aynı transaction'dadır: işlem geri alınırsa kayıt da kalmaz.
func TestRecordIsRolledBackWithTheOperation(t *testing.T) {
	env := dbtest.Get(t)
	rec := audit.NewRecorder(ids.V7{})
	ctx, _ := asUser(auth.RoleOperations)
	entityID := uuid.NewString()

	err := db.NewTxManager(env.App).WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		if err := rec.Record(ctx, tx, audit.Entry{Action: "fx.rate.create", Entity: "fx_rate", EntityID: entityID}); err != nil {
			return err
		}
		return errors.New("işlem başarısız")
	})
	require.Error(t, err)

	var n int
	require.NoError(t, env.App.QueryRow(t.Context(), `SELECT count(*) FROM audit_log WHERE entity_id = $1`, entityID).Scan(&n))
	require.Zero(t, n)
}

func TestAuditLogIsAppendOnly(t *testing.T) {
	env := dbtest.Get(t)
	ctx, _ := asUser(auth.RoleOperations)
	require.NoError(t, audit.NewRecorder(ids.V7{}).Record(ctx, env.App, audit.Entry{Action: "a", Entity: "e"}))

	sqlState := func(err error) string {
		var pgErr *pgconn.PgError
		require.True(t, errors.As(err, &pgErr), "PostgreSQL hatası beklenir: %v", err)
		return pgErr.Code
	}

	// 1. güvence: uygulama rolünün UPDATE/DELETE/TRUNCATE yetkisi yok.
	for _, stmt := range []string{
		`UPDATE audit_log SET action = 'değişti'`,
		`DELETE FROM audit_log`,
		`TRUNCATE audit_log`,
	} {
		_, err := env.App.Exec(t.Context(), stmt)
		require.Equal(t, "42501", sqlState(err), "uygulama rolü: %s", stmt)
	}

	// 2. güvence: yetkisi olan sahip rol (süper kullanıcı) bile trigger'a takılır.
	for _, stmt := range []string{
		`UPDATE audit_log SET action = 'değişti'`,
		`DELETE FROM audit_log`,
		`TRUNCATE audit_log`,
	} {
		_, err := env.Owner.Exec(t.Context(), stmt)
		require.Equal(t, "23001", sqlState(err), "sahip rol: %s", stmt) // restrict_violation
	}
}

func TestListPaginatesNewestFirst(t *testing.T) {
	env := dbtest.Get(t)
	rec := audit.NewRecorder(ids.V7{})
	ctx, _ := asUser(auth.RoleOperations)
	entityID := uuid.NewString()
	for i := range 5 {
		require.NoError(t, rec.Record(ctx, env.App, audit.Entry{
			Action: "step", Entity: "pagination", EntityID: entityID, New: map[string]int{"n": i},
		}))
	}

	svc := audit.NewService(env.App)
	f := audit.Filter{Entity: "pagination", EntityID: entityID}
	var seen []int
	req := page.Request{Limit: 2}
	for {
		p, err := svc.List(t.Context(), f, req)
		require.NoError(t, err)
		for _, r := range p.Items {
			var v map[string]int
			require.NoError(t, json.Unmarshal(r.NewValue, &v))
			seen = append(seen, v["n"])
		}
		if p.NextCursor == nil {
			break
		}
		req.Cursor = *p.NextCursor
	}
	require.Equal(t, []int{4, 3, 2, 1, 0}, seen, "en yeni önce, eksiksiz ve tekrarsız")
}

func TestHandlerListAndValidation(t *testing.T) {
	env := dbtest.Get(t)
	h := audit.NewHandler(audit.NewService(env.App))

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/audit-log?limit=1", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"items":[`)

	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/audit-log?cursor=bozuk", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
