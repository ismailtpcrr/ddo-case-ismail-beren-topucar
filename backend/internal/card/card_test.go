package card_test

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

	"parakopru/internal/account"
	"parakopru/internal/card"
	"parakopru/internal/customer"
	"parakopru/internal/ledger"
	"parakopru/internal/money"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/page"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type fixture struct {
	env      *dbtest.Env
	txm      *db.TxManager
	accounts *account.Service
	ledger   *ledger.Service
	cards    *card.Service
	custs    *customer.Service
	sys      auth.Principal // kart ağı (sistem rolü)
	ops      context.Context
}

var seq atomic.Int64

func newFixture(t *testing.T) fixture {
	t.Helper()
	env := dbtest.Get(t)
	txm := db.NewTxManager(env.App)
	rec := audit.NewRecorder(ids.V7{})
	custs := customer.NewService(txm, ids.V7{}, rec)
	accs := account.NewService(txm, custs, ids.V7{}, clock.System{}, rec)
	_, err := accs.EnsureInternalAccounts(audit.WithSystemActor(t.Context()))
	require.NoError(t, err)
	led := ledger.NewService(txm, ids.V7{}, rec, accs)
	id := uuid.New() // idempotency anahtarı gerçek bir kullanıcıya bağlıdır
	_, err = env.App.Exec(t.Context(), `INSERT INTO users (id, email, password_hash, role, full_name) VALUES ($1, $2, 'x', 'SYSTEM', 'Kart Ağı')`, id, id.String()+"@parakopru.test")
	require.NoError(t, err)
	return fixture{env: env, txm: txm, accounts: accs, ledger: led, custs: custs,
		cards: card.NewService(txm, ids.V7{}, clock.System{}, rec, led, accs),
		sys:   auth.Principal{UserID: id, Role: auth.RoleSystem},
		ops:   auth.WithPrincipal(t.Context(), auth.Principal{UserID: uuid.New(), Role: auth.RoleOperations})}
}

// newCard bakiyeli bir hesap ve ona bağlı kart açar.
func (f fixture) newCard(t *testing.T, cur money.Currency, balance int64) (card.Card, account.Account) {
	t.Helper()
	cu, err := f.custs.Create(f.ops, customer.CreateInput{FullName: "Kart Test", NationalID: fmt.Sprintf("6%010d", time.Now().UnixNano()%1e9*10+seq.Add(1)%10)})
	require.NoError(t, err)
	a, err := f.accounts.Open(f.ops, account.OpenInput{CustomerID: cu.ID, Kind: account.Checking, Currency: cur})
	require.NoError(t, err)
	if balance > 0 {
		eq, err := f.accounts.InternalAccountID(t.Context(), nil, account.OpeningEquity, cur)
		require.NoError(t, err)
		amt := money.New(balance, cur)
		require.NoError(t, f.txm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
			_, err := f.ledger.Post(ctx, tx, ledger.Entry{Kind: ledger.KindOpeningBalance, Description: "açılış",
				Lines: []ledger.Line{ledger.Debit(eq, amt), ledger.Credit(a.ID, amt)}})
			return err
		}))
	}
	var c card.Card
	require.NoError(t, f.txm.WithTx(audit.WithSystemActor(t.Context()), func(ctx context.Context, tx db.DBTX) error {
		var err error
		c, err = f.cards.Issue(ctx, tx, a.ID, cu.ID)
		return err
	}))
	return c, a
}

func (f fixture) acct(t *testing.T, id uuid.UUID) account.Account {
	t.Helper()
	a, err := f.accounts.ByID(t.Context(), nil, id)
	require.NoError(t, err)
	return a
}

func (f fixture) authorize(t *testing.T, c card.Card, minor int64, key string) card.Hold {
	t.Helper()
	h, _, err := f.cards.Authorize(t.Context(), f.sys, key, c.ID, card.AuthorizeInput{AmountMinor: minor, Currency: money.TRY, Merchant: "Market"})
	require.NoError(t, err)
	return h
}

func (f fixture) requireInvariants(t *testing.T) {
	t.Helper()
	inv, err := f.ledger.CheckInvariants(t.Context())
	require.NoError(t, err)
	require.True(t, inv.OK, "%+v", inv)
	require.NotNil(t, inv.HoldMismatches)
	require.Zero(t, *inv.HoldMismatches, "held_minor = Σ aktif provizyon")
}

func TestAuthorizeBlocksFundsWithoutLedgerEntry(t *testing.T) {
	f := newFixture(t)
	c, a := f.newCard(t, money.TRY, 1_000_00)
	var entriesBefore int
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT count(*) FROM postings WHERE account_id = $1`, a.ID).Scan(&entriesBefore))

	h := f.authorize(t, c, 300_00, "a1")
	require.Equal(t, card.Active, h.State)
	require.Equal(t, int64(300_00), h.RemainingMinor)
	got := f.acct(t, a.ID)
	require.Equal(t, int64(1_000_00), got.BalanceMinor, "bloke ≠ harcama: bakiye değişmez")
	require.Equal(t, int64(300_00), got.HeldMinor)
	require.Equal(t, int64(700_00), got.AvailableMinor())
	var entriesAfter int
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT count(*) FROM postings WHERE account_id = $1`, a.ID).Scan(&entriesAfter))
	require.Equal(t, entriesBefore, entriesAfter, "provizyon defter kaydı yazmaz")

	// Kullanılabilir bakiyeyi aşan provizyon reddedilir; blok değişmez.
	_, _, err := f.cards.Authorize(t.Context(), f.sys, "a2", c.ID, card.AuthorizeInput{AmountMinor: 700_01, Currency: money.TRY})
	require.ErrorIs(t, err, card.ErrInsufficient)
	f.authorize(t, c, 700_00, "a3") // tam kalan tutar
	require.Equal(t, int64(0), f.acct(t, a.ID).AvailableMinor())
	f.requireInvariants(t)
}

func TestAuthorizeRules(t *testing.T) {
	f := newFixture(t)
	c, _ := f.newCard(t, money.TRY, 100_00)
	_, _, err := f.cards.Authorize(t.Context(), f.sys, "r1", c.ID, card.AuthorizeInput{AmountMinor: 1_00, Currency: money.USD})
	require.ErrorIs(t, err, card.ErrCurrencyMismatch)
	_, _, err = f.cards.Authorize(t.Context(), f.sys, "r2", uuid.New(), card.AuthorizeInput{AmountMinor: 1_00, Currency: money.TRY})
	require.ErrorIs(t, err, card.ErrCardNotFound)
	_, err = f.env.Owner.Exec(t.Context(), `UPDATE cards SET status = 'BLOCKED' WHERE id = $1`, c.ID)
	require.NoError(t, err)
	_, _, err = f.cards.Authorize(t.Context(), f.sys, "r3", c.ID, card.AuthorizeInput{AmountMinor: 1_00, Currency: money.TRY})
	require.ErrorIs(t, err, card.ErrCardBlocked)

	// Aynı anahtar: tek provizyon.
	c2, a2 := f.newCard(t, money.TRY, 100_00)
	h1 := f.authorize(t, c2, 10_00, "same")
	h2, replayed, err := f.cards.Authorize(t.Context(), f.sys, "same", c2.ID, card.AuthorizeInput{AmountMinor: 10_00, Currency: money.TRY, Merchant: "Market"})
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, h1.ID, h2.ID)
	require.Equal(t, int64(10_00), f.acct(t, a2.ID).HeldMinor)
}

func TestPartialAndFinalCapture(t *testing.T) {
	f := newFixture(t)
	c, a := f.newCard(t, money.TRY, 1_000_00)
	settlement, err := f.accounts.InternalAccountID(t.Context(), nil, account.CardSettlement, money.TRY)
	require.NoError(t, err)
	var s0 int64
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT balance_minor FROM account_balances WHERE account_id = $1`, settlement).Scan(&s0))

	h := f.authorize(t, c, 400_00, "c-auth")
	// Kısmi çekim 150 TL: bakiye 850, blok 250 kalır, hold aktif.
	r1, _, err := f.cards.Capture(t.Context(), f.sys, "c1", h.ID, card.CaptureInput{AmountMinor: 150_00})
	require.NoError(t, err)
	require.Equal(t, card.Active, r1.Hold.State)
	require.Equal(t, int64(250_00), r1.Hold.RemainingMinor)
	got := f.acct(t, a.ID)
	require.Equal(t, int64(850_00), got.BalanceMinor)
	require.Equal(t, int64(250_00), got.HeldMinor)
	require.Equal(t, int64(600_00), got.AvailableMinor())

	// Final çekim 100 TL: kalan 150 TL serbest kalır.
	r2, _, err := f.cards.Capture(t.Context(), f.sys, "c2", h.ID, card.CaptureInput{AmountMinor: 100_00, Final: true})
	require.NoError(t, err)
	require.Equal(t, card.Captured, r2.Hold.State)
	require.Equal(t, int64(250_00), r2.Hold.CapturedMinor)
	require.Equal(t, int64(150_00), r2.Hold.ReleasedMinor)
	require.Zero(t, r2.Hold.RemainingMinor)
	got = f.acct(t, a.ID)
	require.Equal(t, int64(750_00), got.BalanceMinor)
	require.Zero(t, got.HeldMinor)
	var s1 int64
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT balance_minor FROM account_balances WHERE account_id = $1`, settlement).Scan(&s1))
	require.Equal(t, s0+250_00, s1, "çekimler kart takas hesabında birikir")

	// Defter kaydı: borç müşteri / alacak takas, çekim kimliğine bağlı.
	e, err := f.ledger.Entry(t.Context(), r1.Capture.JournalEntryID)
	require.NoError(t, err)
	require.Equal(t, card.KindCapture, e.Kind)
	require.Equal(t, r1.Capture.ID.String(), *e.Reference)

	// Aktif olmayan provizyondan çekim, kalandan fazla çekim.
	_, _, err = f.cards.Capture(t.Context(), f.sys, "c3", h.ID, card.CaptureInput{AmountMinor: 1_00})
	require.ErrorIs(t, err, card.ErrHoldNotActive)
	h2 := f.authorize(t, c, 50_00, "c-auth2")
	_, _, err = f.cards.Capture(t.Context(), f.sys, "c4", h2.ID, card.CaptureInput{AmountMinor: 50_01})
	require.ErrorIs(t, err, card.ErrExceedsHold)
	// Tamamını çekmek final saymadan da hold'u kapatır.
	r3, _, err := f.cards.Capture(t.Context(), f.sys, "c5", h2.ID, card.CaptureInput{AmountMinor: 50_00})
	require.NoError(t, err)
	require.Equal(t, card.Captured, r3.Hold.State)
	_, _, err = f.cards.Capture(t.Context(), f.sys, "c6", uuid.New(), card.CaptureInput{AmountMinor: 1})
	require.ErrorIs(t, err, card.ErrHoldNotFound)
	f.requireInvariants(t)
}

func TestRefund(t *testing.T) {
	f := newFixture(t)
	c, a := f.newCard(t, money.TRY, 500_00)
	h := f.authorize(t, c, 200_00, "rf-auth")
	cap, _, err := f.cards.Capture(t.Context(), f.sys, "rf-c", h.ID, card.CaptureInput{AmountMinor: 200_00, Final: true})
	require.NoError(t, err)
	require.Equal(t, int64(300_00), f.acct(t, a.ID).BalanceMinor)

	r1, _, err := f.cards.Refund(t.Context(), f.sys, "rf1", cap.Capture.ID, card.RefundInput{AmountMinor: 80_00})
	require.NoError(t, err)
	require.Equal(t, int64(80_00), r1.Capture.RefundedMinor)
	require.Equal(t, int64(380_00), f.acct(t, a.ID).BalanceMinor)
	_, _, err = f.cards.Refund(t.Context(), f.sys, "rf2", cap.Capture.ID, card.RefundInput{AmountMinor: 120_01})
	require.ErrorIs(t, err, card.ErrExceedsCapture, "toplam iade çekilenden fazla olamaz")
	r3, _, err := f.cards.Refund(t.Context(), f.sys, "rf3", cap.Capture.ID, card.RefundInput{AmountMinor: 120_00})
	require.NoError(t, err)
	require.Equal(t, int64(200_00), r3.Capture.RefundedMinor)
	require.Equal(t, int64(500_00), f.acct(t, a.ID).BalanceMinor, "tam iade: bakiye başa döner")
	_, _, err = f.cards.Refund(t.Context(), f.sys, "rf4", cap.Capture.ID, card.RefundInput{AmountMinor: 1})
	require.ErrorIs(t, err, card.ErrExceedsCapture)
	_, _, err = f.cards.Refund(t.Context(), f.sys, "rf5", uuid.New(), card.RefundInput{AmountMinor: 1})
	require.ErrorIs(t, err, card.ErrCaptureNotFound)
	// Aynı anahtar: ikinci iade yok.
	_, replayed, err := f.cards.Refund(t.Context(), f.sys, "rf1", cap.Capture.ID, card.RefundInput{AmountMinor: 80_00})
	require.NoError(t, err)
	require.True(t, replayed)
	f.requireInvariants(t)
}

// Süre aşımı: worker aktif ama süresi dolmuş provizyonu serbest bırakır; sonrasında çekim yapılamaz.
func TestExpiredHoldIsReleased(t *testing.T) {
	f := newFixture(t)
	c, a := f.newCard(t, money.TRY, 100_00)
	short, _, err := f.cards.Authorize(t.Context(), f.sys, "exp-1", c.ID, card.AuthorizeInput{AmountMinor: 60_00, Currency: money.TRY, TTLSeconds: 1})
	require.NoError(t, err)
	long := f.authorize(t, c, 30_00, "exp-2")
	require.Equal(t, int64(90_00), f.acct(t, a.ID).HeldMinor)

	n, err := f.cards.ExpireDue(t.Context())
	require.NoError(t, err)
	require.Zero(t, n, "süre henüz dolmadı")
	time.Sleep(1100 * time.Millisecond)
	n, err = f.cards.ExpireDue(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got := f.acct(t, a.ID)
	require.Equal(t, int64(30_00), got.HeldMinor, "yalnız süresi dolan blok serbest kaldı")
	require.Equal(t, int64(70_00), got.AvailableMinor())
	hs, err := f.cards.Holds(t.Context(), auth.Principal{UserID: uuid.New(), Role: auth.RoleOperations}, a.ID, nil, page.Request{Limit: 10})
	require.NoError(t, err)
	states := map[uuid.UUID]card.State{}
	for _, h := range hs.Items {
		states[h.ID] = h.State
	}
	require.Equal(t, card.Expired, states[short.ID])
	require.Equal(t, card.Active, states[long.ID])
	_, _, err = f.cards.Capture(t.Context(), f.sys, "exp-c", short.ID, card.CaptureInput{AmountMinor: 1_00})
	require.ErrorIs(t, err, card.ErrHoldNotActive)
	n, err = f.cards.ExpireDue(t.Context())
	require.NoError(t, err)
	require.Zero(t, n, "ikinci tur bir şey yapmaz")
	f.requireInvariants(t)
}

func TestHandlersAndObjectAccess(t *testing.T) {
	f := newFixture(t)
	c, a := f.newCard(t, money.TRY, 100_00)
	h := card.NewHandler(f.cards)
	r := chi.NewRouter()
	r.Post("/cards/{id}/authorizations", h.Authorize)
	r.Post("/holds/{id}/captures", h.Capture)
	r.Post("/captures/{id}/refunds", h.Refund)
	r.Get("/cards", h.Cards)
	r.Get("/accounts/{id}/holds", h.Holds)
	call := func(p auth.Principal, method, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(auth.WithPrincipal(t.Context(), p), method, path, strings.NewReader(body))
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	rec := call(f.sys, http.MethodPost, "/cards/"+c.ID.String()+"/authorizations", "", `{"amount_minor":500,"currency":"TRY"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "IDEMPOTENCY_KEY_REQUIRED")
	require.Equal(t, http.StatusBadRequest, call(f.sys, http.MethodPost, "/cards/"+c.ID.String()+"/authorizations", "k0", `{"amount_minor":0,"currency":"TRY"}`).Code)
	rec = call(f.sys, http.MethodPost, "/cards/"+c.ID.String()+"/authorizations", "k1", `{"amount_minor":500,"currency":"TRY","merchant":"Kafe"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var hold card.Hold
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &hold))
	rec = call(f.sys, http.MethodPost, "/holds/"+hold.ID.String()+"/captures", "k2", `{"amount_minor":300,"final":true}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var res card.CaptureResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	rec = call(f.sys, http.MethodPost, "/captures/"+res.Capture.ID.String()+"/refunds", "k3", `{"amount_minor":999}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "REFUND_EXCEEDS_CAPTURE")
	rec = call(f.sys, http.MethodPost, "/captures/"+res.Capture.ID.String()+"/refunds", "k3", `{"amount_minor":999}`)
	require.Equal(t, "true", rec.Header().Get("Idempotent-Replayed"), "ret de saklanır")
	require.Equal(t, http.StatusBadRequest, call(f.sys, http.MethodPost, "/holds/bozuk/captures", "k4", `{"amount_minor":1}`).Code)

	// Okuma: sahibi kartını ve hesabın provizyonlarını görür; başkası göremez.
	ops := auth.Principal{UserID: uuid.New(), Role: auth.RoleOperations}
	rec = call(ops, http.MethodGet, "/accounts/"+a.ID.String()+"/holds?state=CAPTURED", "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), hold.ID.String())
	require.Equal(t, http.StatusBadRequest, call(ops, http.MethodGet, "/accounts/"+a.ID.String()+"/holds?state=X", "", "").Code)
	rec = call(ops, http.MethodGet, "/cards?limit=5", "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	stranger := auth.Principal{UserID: uuid.New(), Role: auth.RoleCustomer}
	require.Equal(t, http.StatusForbidden, call(stranger, http.MethodGet, "/accounts/"+a.ID.String()+"/holds", "", "").Code)
}
