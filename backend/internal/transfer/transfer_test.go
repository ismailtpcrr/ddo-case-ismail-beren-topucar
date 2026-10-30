package transfer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"parakopru/internal/account"
	"parakopru/internal/customer"
	"parakopru/internal/ledger"
	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/page"
	"parakopru/internal/transfer"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type fixture struct {
	env      *dbtest.Env
	txm      *db.TxManager
	accounts *account.Service
	ledger   *ledger.Service
	transfer *transfer.Service
	ops      auth.Principal
	opsCtx   context.Context
	cust     *customer.Service
}

var seq atomic.Int64

func newFixture(t *testing.T) fixture {
	t.Helper()
	env := dbtest.Get(t)
	txm := db.NewTxManager(env.App)
	rec := audit.NewRecorder(ids.V7{})
	cust := customer.NewService(txm, ids.V7{}, rec)
	accs := account.NewService(txm, cust, ids.V7{}, clock.System{}, rec)
	_, err := accs.EnsureInternalAccounts(audit.WithSystemActor(t.Context()))
	require.NoError(t, err)
	led := ledger.NewService(txm, ids.V7{}, rec, accs)
	ops := newUser(t, env, auth.RoleOperations)
	return fixture{env: env, txm: txm, accounts: accs, ledger: led, cust: cust,
		transfer: transfer.NewService(txm, ids.V7{}, rec, led, accs),
		ops:      ops, opsCtx: auth.WithPrincipal(t.Context(), ops)}
}

// newUser idempotency anahtarının yabancı anahtarı için gerçek bir kullanıcı açar.
func newUser(t *testing.T, env *dbtest.Env, role auth.Role) auth.Principal {
	t.Helper()
	id := uuid.New()
	_, err := env.App.Exec(t.Context(),
		`INSERT INTO users (id, email, password_hash, role, full_name) VALUES ($1, $2, 'x', $3, 'Test')`,
		id, id.String()+"@parakopru.test", role)
	require.NoError(t, err)
	return auth.Principal{UserID: id, Role: role}
}

// owned müşteri rolünde bir kullanıcı, ona bağlı müşteri ve verilen bakiyeyle vadesiz hesap açar.
func (f fixture) owned(t *testing.T, c money.Currency, balance int64) (auth.Principal, account.Account) {
	t.Helper()
	p := newUser(t, f.env, auth.RoleCustomer)
	var cu customer.Customer
	require.NoError(t, f.txm.WithTx(audit.WithSystemActor(t.Context()), func(ctx context.Context, tx db.DBTX) error {
		var err error
		cu, _, err = f.cust.EnsureForUser(ctx, tx, p.UserID, customer.CreateInput{
			FullName: "Transfer Test", NationalID: fmt.Sprintf("4%010d", time.Now().UnixNano()%1e9*10+seq.Add(1)%10)})
		return err
	}))
	a, err := f.accounts.Open(f.opsCtx, account.OpenInput{CustomerID: cu.ID, Kind: account.Checking, Currency: c})
	require.NoError(t, err)
	if balance > 0 {
		eq, err := f.accounts.InternalAccountID(t.Context(), nil, account.OpeningEquity, c)
		require.NoError(t, err)
		amt := money.New(balance, c)
		require.NoError(t, f.txm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
			_, err := f.ledger.Post(ctx, tx, ledger.Entry{Kind: ledger.KindOpeningBalance, Description: "açılış",
				Lines: []ledger.Line{ledger.Debit(eq, amt), ledger.Credit(a.ID, amt)}})
			return err
		}))
	}
	return p, a
}

func (f fixture) balance(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	var b int64
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT balance_minor FROM account_balances WHERE account_id = $1`, id).Scan(&b))
	return b
}

func in(from, to account.Account, minor int64) transfer.CreateInput {
	return transfer.CreateInput{FromAccountID: from.ID, ToIBAN: *to.IBAN, AmountMinor: minor, Currency: from.Currency, Description: "test"}
}

func TestCreateInternalTransfer(t *testing.T) {
	f := newFixture(t)
	p, from := f.owned(t, money.TRY, 1_000_00)
	_, to := f.owned(t, money.TRY, 0)

	tr, replayed, err := f.transfer.Create(t.Context(), p, "k1", in(from, to, 250_00))
	require.NoError(t, err)
	require.False(t, replayed)
	require.Equal(t, transfer.Completed, tr.State)
	require.Equal(t, transfer.Internal, tr.Type)
	require.NotNil(t, tr.JournalEntryID)
	require.Equal(t, int64(750_00), f.balance(t, from.ID))
	require.Equal(t, int64(250_00), f.balance(t, to.ID))

	d, err := f.transfer.Get(t.Context(), p, tr.ID)
	require.NoError(t, err)
	require.Len(t, d.Events, 2)
	require.Nil(t, d.Events[0].From)
	require.Equal(t, transfer.Created, d.Events[0].To)
	require.Equal(t, transfer.Completed, d.Events[1].To)

	e, err := f.ledger.Entry(t.Context(), *tr.JournalEntryID)
	require.NoError(t, err)
	require.Equal(t, tr.ID.String(), *e.Reference, "defter kaydı transfere bağlı")
	require.Len(t, e.Postings, 2)

	logs, err := audit.NewService(f.env.App).List(t.Context(), audit.Filter{Entity: "transfer", EntityID: tr.ID.String()}, page.Request{Limit: 5})
	require.NoError(t, err)
	require.Len(t, logs.Items, 1)
	require.Equal(t, "transfer.create", logs.Items[0].Action)
}

func TestInsufficientFundsIsRecordedAsRejected(t *testing.T) {
	f := newFixture(t)
	p, from := f.owned(t, money.TRY, 100_00)
	_, to := f.owned(t, money.TRY, 0)

	tr, _, err := f.transfer.Create(t.Context(), p, "k-poor", in(from, to, 100_01))
	require.NoError(t, err, "ret bir sonuçtur, hata değil")
	require.Equal(t, transfer.Rejected, tr.State)
	require.Equal(t, "INSUFFICIENT_FUNDS", *tr.FailureCode)
	require.Nil(t, tr.JournalEntryID)
	require.Equal(t, int64(100_00), f.balance(t, from.ID))
	require.Zero(t, f.balance(t, to.ID))

	// Tekrar istek aynı reddi döndürür; ikinci bir transfer kaydı olmaz.
	again, replayed, err := f.transfer.Create(t.Context(), p, "k-poor", in(from, to, 100_01))
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, tr.ID, again.ID)
	var n int
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT count(*) FROM transfers WHERE from_account_id = $1`, from.ID).Scan(&n))
	require.Equal(t, 1, n)
}

func TestSameKeySameResultAndReuseRejected(t *testing.T) {
	f := newFixture(t)
	p, from := f.owned(t, money.TRY, 500_00)
	_, to := f.owned(t, money.TRY, 0)

	first, _, err := f.transfer.Create(t.Context(), p, "same", in(from, to, 10_00))
	require.NoError(t, err)
	second, replayed, err := f.transfer.Create(t.Context(), p, "same", in(from, to, 10_00))
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, int64(490_00), f.balance(t, from.ID), "para yalnız bir kez çıktı")

	_, _, err = f.transfer.Create(t.Context(), p, "same", in(from, to, 11_00))
	require.ErrorIs(t, err, apperr.New(0, "IDEMPOTENCY_KEY_REUSED", ""))
}

// Aynı anahtarla eşzamanlı 20 istek: tek transfer, tek para hareketi, hepsi aynı sonuç.
func TestConcurrentRequestsWithSameKey(t *testing.T) {
	f := newFixture(t)
	p, from := f.owned(t, money.TRY, 1_000_00)
	_, to := f.owned(t, money.TRY, 0)

	ids := make([]uuid.UUID, 20)
	var g errgroup.Group
	for i := range ids {
		g.Go(func() error {
			tr, _, err := f.transfer.Create(t.Context(), p, "race", in(from, to, 100_00))
			ids[i] = tr.ID
			return err
		})
	}
	require.NoError(t, g.Wait())
	for _, id := range ids {
		require.Equal(t, ids[0], id)
	}
	require.Equal(t, int64(900_00), f.balance(t, from.ID))
	require.Equal(t, int64(100_00), f.balance(t, to.ID))
}

func TestBusinessRules(t *testing.T) {
	f := newFixture(t)
	p, from := f.owned(t, money.TRY, 500_00)
	other, theirs := f.owned(t, money.TRY, 500_00)
	_, usd := f.owned(t, money.USD, 0)
	_, to := f.owned(t, money.TRY, 0)
	eq, err := f.accounts.InternalAccountID(t.Context(), nil, account.Nostro, money.TRY)
	require.NoError(t, err)
	bad := func(m func(*transfer.CreateInput)) transfer.CreateInput { c := in(from, to, 1_00); m(&c); return c }

	cases := []struct {
		name string
		p    auth.Principal
		in   transfer.CreateInput
		want error
	}{
		{"başkasının hesabından", p, in(theirs, to, 1_00), apperr.ErrForbidden},
		{"iç hesaptan", f.ops, transfer.CreateInput{FromAccountID: eq, ToIBAN: *to.IBAN, AmountMinor: 1, Currency: money.TRY}, transfer.ErrNotInternalAcct},
		{"para birimi uyumsuz", p, bad(func(c *transfer.CreateInput) { c.Currency = money.USD }), transfer.ErrCurrencyMismatch},
		{"alıcı başka para biriminde", p, in(from, usd, 1_00), transfer.ErrCurrencyMismatch},
		{"kendine", p, in(from, from, 1_00), transfer.ErrSameAccount},
		{"bozuk IBAN", p, bad(func(c *transfer.CreateInput) { c.ToIBAN = "TR000000000000000000000000" }), transfer.ErrInvalidIBAN},
		{"dış banka IBAN'ı", p, bad(func(c *transfer.CreateInput) { c.ToIBAN = account.NewIBAN(999_999_999) }), transfer.ErrExternalNotReady},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := f.transfer.Create(t.Context(), tc.p, fmt.Sprintf("rule-%d", i), tc.in)
			require.ErrorIs(t, err, tc.want)
		})
	}
	_ = other
	require.Equal(t, int64(500_00), f.balance(t, from.ID), "reddedilen istekler bakiyeyi değiştirmedi")

	// Operasyon uzmanı müşteri adına (herhangi bir hesaptan) transfer başlatabilir.
	tr, _, err := f.transfer.Create(t.Context(), f.ops, "ops-1", in(theirs, to, 5_00))
	require.NoError(t, err)
	require.Equal(t, transfer.Completed, tr.State)
}

func TestStateMachine(t *testing.T) {
	valid := [][2]transfer.State{
		{transfer.Created, transfer.Completed}, {transfer.Created, transfer.Rejected}, {transfer.Reserved, transfer.Sent},
		{transfer.Sent, transfer.Unknown}, {transfer.Unknown, transfer.Failed}, {transfer.Failed, transfer.Reversed},
	}
	for _, v := range valid {
		require.True(t, transfer.CanTransition(v[0], v[1]), "%s → %s", v[0], v[1])
	}
	invalid := [][2]transfer.State{
		{transfer.Completed, transfer.Created}, {transfer.Completed, transfer.Failed}, {transfer.Rejected, transfer.Completed},
		{transfer.Reversed, transfer.Completed}, {transfer.Created, transfer.Sent}, {transfer.Sent, transfer.Reserved},
		{transfer.Completed, transfer.Completed},
	}
	for _, v := range invalid {
		require.False(t, transfer.CanTransition(v[0], v[1]), "%s → %s", v[0], v[1])
	}
}

func TestObjectLevelReadAccess(t *testing.T) {
	f := newFixture(t)
	p, from := f.owned(t, money.TRY, 100_00)
	q, to := f.owned(t, money.TRY, 0)
	stranger, _ := f.owned(t, money.TRY, 0)
	tr, _, err := f.transfer.Create(t.Context(), p, "obj", in(from, to, 1_00))
	require.NoError(t, err)

	for _, who := range []auth.Principal{p, q, f.ops} {
		_, err := f.transfer.Get(t.Context(), who, tr.ID)
		require.NoError(t, err)
	}
	_, err = f.transfer.Get(t.Context(), stranger, tr.ID)
	require.ErrorIs(t, err, apperr.ErrForbidden)
	_, err = f.transfer.Get(t.Context(), p, uuid.New())
	require.ErrorIs(t, err, transfer.ErrNotFound)

	mine, err := f.transfer.List(t.Context(), stranger, page.Request{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, mine.Items)
	mine, err = f.transfer.List(t.Context(), q, page.Request{Limit: 10})
	require.NoError(t, err)
	require.Len(t, mine.Items, 1, "alıcı da transferi görür")
}

// Veritabanı: geçersiz durum adı ve olay satırlarının değiştirilmesi reddedilir.
func TestDatabaseGuards(t *testing.T) {
	f := newFixture(t)
	p, from := f.owned(t, money.TRY, 10_00)
	_, to := f.owned(t, money.TRY, 0)
	tr, _, err := f.transfer.Create(t.Context(), p, "db", in(from, to, 1_00))
	require.NoError(t, err)

	_, err = f.env.App.Exec(t.Context(), `UPDATE transfers SET state = 'BOGUS' WHERE id = $1`, tr.ID)
	require.Error(t, err)
	_, err = f.env.App.Exec(t.Context(), `UPDATE transfer_events SET note = 'x' WHERE transfer_id = $1`, tr.ID)
	require.ErrorContains(t, err, "permission denied")
}

func TestInvariantsDetectCorruption(t *testing.T) {
	f := newFixture(t)
	p, from := f.owned(t, money.EUR, 100_00)
	_, to := f.owned(t, money.EUR, 0)
	_, _, err := f.transfer.Create(t.Context(), p, "inv", in(from, to, 30_00))
	require.NoError(t, err)

	inv, err := f.ledger.CheckInvariants(t.Context())
	require.NoError(t, err)
	require.True(t, inv.OK, "%+v", inv)
	require.Zero(t, inv.UnbalancedEntries)
	require.Zero(t, inv.SummaryMismatches)
	for _, tot := range inv.TotalsByCurrency {
		require.Zero(t, tot.SumMinor, tot.Currency)
	}
	require.Positive(t, inv.Postings)

	// Özeti elle bozarsak ölçüm bunu yakalar (sahip rolle; uygulama rolü de UPDATE edebilir ama CHECK sınırı içinde).
	_, err = f.env.Owner.Exec(t.Context(), `UPDATE account_balances SET balance_minor = balance_minor + 1 WHERE account_id = $1`, to.ID)
	require.NoError(t, err)
	inv, err = f.ledger.CheckInvariants(t.Context())
	require.NoError(t, err)
	require.False(t, inv.OK)
	require.Equal(t, int64(1), inv.SummaryMismatches)
	_, err = f.env.Owner.Exec(t.Context(), `UPDATE account_balances SET balance_minor = balance_minor - 1 WHERE account_id = $1`, to.ID)
	require.NoError(t, err)
}

func TestHandlers(t *testing.T) {
	f := newFixture(t)
	p, from := f.owned(t, money.TRY, 100_00)
	_, to := f.owned(t, money.TRY, 0)
	h := transfer.NewHandler(f.transfer)
	r := chi.NewRouter()
	r.Post("/transfers", h.Create)
	r.Get("/transfers", h.List)
	r.Get("/transfers/{id}", h.Get)
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(auth.WithPrincipal(t.Context(), p), method, path, strings.NewReader(body))
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	body := func(minor int64) string {
		return fmt.Sprintf(`{"from_account_id":"%s","to_iban":"%s","amount_minor":%d,"currency":"TRY"}`, from.ID, *to.IBAN, minor)
	}

	rec := call(http.MethodPost, "/transfers", "", body(1_00))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "IDEMPOTENCY_KEY_REQUIRED")
	require.Equal(t, http.StatusBadRequest, call(http.MethodPost, "/transfers", "h0", body(0)).Code, "tutar > 0")
	require.Equal(t, http.StatusBadRequest, call(http.MethodPost, "/transfers", "h0b", `{"x":1}`).Code)

	rec = call(http.MethodPost, "/transfers", "h1", body(10_00))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var tr transfer.Transfer
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tr))
	require.Equal(t, transfer.Completed, tr.State)
	rec = call(http.MethodPost, "/transfers", "h1", body(10_00))
	require.Equal(t, "true", rec.Header().Get("Idempotent-Replayed"))
	require.Equal(t, http.StatusCreated, rec.Code)

	rec = call(http.MethodPost, "/transfers", "h2", body(10_000_00))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "INSUFFICIENT_FUNDS")
	require.NotEmpty(t, rec.Header().Get("X-Transfer-ID"))
	rec = call(http.MethodPost, "/transfers", "h2", body(10_000_00))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "tekrarda da aynı ret")
	require.Equal(t, "true", rec.Header().Get("Idempotent-Replayed"))

	rec = call(http.MethodGet, "/transfers/"+tr.ID.String(), "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"events"`)
	rec = call(http.MethodGet, "/transfers?limit=1", "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"next_cursor":"`)
	require.Equal(t, http.StatusBadRequest, call(http.MethodGet, "/transfers/x", "", "").Code)
}
