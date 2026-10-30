package customer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"parakopru/internal/customer"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/page"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

var base = time.Now().UnixNano() % 1e9 * 10

func nid(i int64) string { return fmt.Sprintf("8%010d", base+i) }

func newSvc(t *testing.T) (*customer.Service, context.Context, *db.TxManager) {
	t.Helper()
	env := dbtest.Get(t)
	txm := db.NewTxManager(env.App)
	svc := customer.NewService(txm, ids.V7{}, audit.NewRecorder(ids.V7{}))
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{UserID: uuid.New(), Role: auth.RoleOperations})
	return svc, ctx, txm
}

func TestCreateGetListAndDuplicate(t *testing.T) {
	svc, ctx, _ := newSvc(t)
	c, err := svc.Create(ctx, customer.CreateInput{FullName: "Ayşe Test", NationalID: nid(1), Email: "ayse@example.test"})
	require.NoError(t, err)
	require.Equal(t, "Ayşe Test", c.FullName)
	require.NotNil(t, c.Email)

	got, err := svc.Get(ctx, nil, c.ID)
	require.NoError(t, err)
	require.Equal(t, c.ID, got.ID)
	_, err = svc.Get(ctx, nil, uuid.New())
	require.ErrorIs(t, err, customer.ErrNotFound)
	require.NoError(t, svc.Exists(ctx, nil, c.ID))
	require.ErrorIs(t, svc.Exists(ctx, nil, uuid.New()), customer.ErrNotFound)

	_, err = svc.Create(ctx, customer.CreateInput{FullName: "Aynı Kimlik", NationalID: nid(1)})
	require.ErrorIs(t, err, customer.ErrExists)

	// Keyset sayfalama: en yeni önce, sayfalar boşluksuz ve tekrarsız.
	for i := int64(2); i < 6; i++ {
		_, err := svc.Create(ctx, customer.CreateInput{FullName: "Sayfa Test", NationalID: nid(i)})
		require.NoError(t, err)
	}
	seen := map[uuid.UUID]bool{}
	req := page.Request{Limit: 2}
	for {
		p, err := svc.List(ctx, req)
		require.NoError(t, err)
		for _, it := range p.Items {
			require.False(t, seen[it.ID], "aynı müşteri iki sayfada")
			seen[it.ID] = true
		}
		if p.NextCursor == nil {
			break
		}
		req.Cursor = *p.NextCursor
	}
	require.GreaterOrEqual(t, len(seen), 5)
}

func TestEnsureForUserIsIdempotent(t *testing.T) {
	svc, ctx, txm := newSvc(t)
	env := dbtest.Get(t)
	uid := uuid.New()
	_, err := env.App.Exec(t.Context(), `INSERT INTO users (id, email, password_hash, role, full_name) VALUES ($1, $2, 'x', 'CUSTOMER', 'Ben')`, uid, uid.String()+"@parakopru.test")
	require.NoError(t, err)

	var first customer.Customer
	require.NoError(t, txm.WithTx(audit.WithSystemActor(ctx), func(ctx context.Context, tx db.DBTX) error {
		var created bool
		var err error
		first, created, err = svc.EnsureForUser(ctx, tx, uid, customer.CreateInput{FullName: "Ben", NationalID: nid(20)})
		require.True(t, created)
		return err
	}))
	require.NoError(t, txm.WithTx(audit.WithSystemActor(ctx), func(ctx context.Context, tx db.DBTX) error {
		again, created, err := svc.EnsureForUser(ctx, tx, uid, customer.CreateInput{FullName: "Ben", NationalID: nid(20)})
		require.False(t, created, "ikinci çağrı yeni müşteri açmaz")
		require.Equal(t, first.ID, again.ID)
		return err
	}))
	id, err := svc.IDForUser(ctx, nil, uid)
	require.NoError(t, err)
	require.Equal(t, first.ID, id)
	_, err = svc.IDForUser(ctx, nil, uuid.New())
	require.ErrorIs(t, err, customer.ErrNoCustomerProfile)
}

func TestHandlers(t *testing.T) {
	svc, ctx, _ := newSvc(t)
	h := customer.NewHandler(svc)
	r := chi.NewRouter()
	r.Post("/customers", h.Create)
	r.Get("/customers", h.List)
	r.Get("/customers/{id}", h.Get)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body)))
		return rec
	}
	rec := call(http.MethodPost, "/customers", `{"full_name":"Handler Test","national_id":"`+nid(30)+`"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var c customer.Customer
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c))
	require.Equal(t, http.StatusConflict, call(http.MethodPost, "/customers", `{"full_name":"Aynı","national_id":"`+nid(30)+`"}`).Code)
	require.Equal(t, http.StatusBadRequest, call(http.MethodPost, "/customers", `{"full_name":"A","national_id":"123"}`).Code)
	require.Equal(t, http.StatusBadRequest, call(http.MethodPost, "/customers", `{"full_name":"Ali","national_id":"abcdefghijk"}`).Code)
	require.Equal(t, http.StatusOK, call(http.MethodGet, "/customers/"+c.ID.String(), "").Code)
	require.Equal(t, http.StatusNotFound, call(http.MethodGet, "/customers/"+uuid.NewString(), "").Code)
	require.Equal(t, http.StatusBadRequest, call(http.MethodGet, "/customers/bozuk", "").Code)
	rec = call(http.MethodGet, "/customers?limit=1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"next_cursor":"`)
	require.Equal(t, http.StatusBadRequest, call(http.MethodGet, "/customers?limit=0", "").Code)
}
