package account_test

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
	"parakopru/internal/customer"
	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
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
	env       *dbtest.Env
	txm       *db.TxManager
	customers *customer.Service
	accounts  *account.Service
	ops       context.Context // operasyon uzmanı olarak
}

var nationalIDSeq atomic.Int64

// nidBase kimlik numaralarının rastgele başlangıcıdır; sayaçla artar, bu yüzden testler arasında çarpışmaz.
var nidBase = time.Now().UnixNano() % 1e9 * 10

// uniqueNationalID testler arasında çakışmayan 11 haneli kimlik numarası üretir.
func uniqueNationalID() string {
	return fmt.Sprintf("2%010d", nidBase+nationalIDSeq.Add(1))
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	env := dbtest.Get(t)
	txm := db.NewTxManager(env.App)
	rec := audit.NewRecorder(ids.V7{})
	customers := customer.NewService(txm, ids.V7{}, rec)
	accounts := account.NewService(txm, customers, ids.V7{}, clock.System{}, rec)
	ops := auth.WithPrincipal(context.Background(), auth.Principal{UserID: uuid.New(), Role: auth.RoleOperations})
	return fixture{env: env, txm: txm, customers: customers, accounts: accounts, ops: ops}
}

func (f fixture) newCustomer(t *testing.T) customer.Customer {
	t.Helper()
	c, err := f.customers.Create(f.ops, customer.CreateInput{FullName: "Test Müşteri", NationalID: uniqueNationalID()})
	require.NoError(t, err)
	return c
}

func (f fixture) open(t *testing.T, in account.OpenInput) account.Account {
	t.Helper()
	a, err := f.accounts.Open(f.ops, in)
	require.NoError(t, err)
	return a
}

func TestInternalAccountsArePerCurrencyAndIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := audit.WithSystemActor(t.Context())
	_, err := f.accounts.EnsureInternalAccounts(ctx)
	require.NoError(t, err)
	again, err := f.accounts.EnsureInternalAccounts(ctx)
	require.NoError(t, err)
	require.Zero(t, again, "ikinci çağrı hiçbir şey açmamalı")

	var n int
	require.NoError(t, f.env.App.QueryRow(t.Context(),
		`SELECT count(*) FROM accounts a JOIN account_balances b ON b.account_id = a.id WHERE a.customer_id IS NULL`).Scan(&n))
	require.Equal(t, 8*3, n, "3 para birimi × 8 iç hesap, her birinin bakiye özeti")

	nostro, err := f.accounts.InternalAccountID(t.Context(), nil, account.Nostro, money.USD)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, nostro)
}

func TestOpenCheckingAndTimeDeposit(t *testing.T) {
	f := newFixture(t)
	c := f.newCustomer(t)

	chk := f.open(t, account.OpenInput{CustomerID: c.ID, Kind: account.Checking, Currency: money.TRY})
	require.Equal(t, account.Checking, chk.Kind)
	require.True(t, account.ValidIBAN(*chk.IBAN), *chk.IBAN)
	require.Equal(t, account.ProductChecking, *chk.InterestProduct)
	require.Nil(t, chk.MaturityDate)
	require.Zero(t, chk.BalanceMinor)
	require.False(t, chk.AllowNegative)
	require.Equal(t, account.Credit, chk.NormalSide)

	maturity := time.Now().AddDate(0, 6, 0).Format(time.DateOnly)
	td := f.open(t, account.OpenInput{CustomerID: c.ID, Kind: account.TimeDeposit, Currency: money.USD, MaturityDate: maturity})
	require.Equal(t, maturity, *td.ToResponse().MaturityDate)
	require.Equal(t, account.ProductTimeDeposit, *td.InterestProduct)
	require.NotEqual(t, *chk.IBAN, *td.IBAN)

	// Denetim kaydı hesapla aynı transaction'da yazıldı.
	p, err := audit.NewService(f.env.App).List(t.Context(), audit.Filter{Entity: "account", EntityID: td.ID.String()}, page.Request{Limit: 5})
	require.NoError(t, err)
	require.Len(t, p.Items, 1)
	require.Equal(t, "account.open", p.Items[0].Action)
}

func TestOpenValidation(t *testing.T) {
	f := newFixture(t)
	c := f.newCustomer(t)
	yesterday := time.Now().AddDate(0, 0, -1).Format(time.DateOnly)

	cases := []struct {
		name string
		in   account.OpenInput
		want error
	}{
		{"vadeli, vade yok", account.OpenInput{CustomerID: c.ID, Kind: account.TimeDeposit, Currency: money.TRY}, account.ErrMaturityRequired},
		{"vadeli, vade geçmişte", account.OpenInput{CustomerID: c.ID, Kind: account.TimeDeposit, Currency: money.TRY, MaturityDate: yesterday}, account.ErrMaturityRequired},
		{"vadesiz, vade var", account.OpenInput{CustomerID: c.ID, Kind: account.Checking, Currency: money.TRY, MaturityDate: "2030-01-01"}, account.ErrMaturityNotAllowed},
		{"müşteri yok", account.OpenInput{CustomerID: uuid.New(), Kind: account.Checking, Currency: money.TRY}, customer.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.accounts.Open(f.ops, tc.in)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

// Veritabanı güvencesi: müşteri hesabının kullanılabilir bakiyesi (bakiye − bloke) eksiye düşemez;
// uygulamada bir hata olsa bile kısıt yanlış veriyi reddeder.
func TestCustomerBalanceCannotGoNegative(t *testing.T) {
	f := newFixture(t)
	c := f.newCustomer(t)
	a := f.open(t, account.OpenInput{CustomerID: c.ID, Kind: account.Checking, Currency: money.TRY})

	_, err := f.env.App.Exec(t.Context(), `UPDATE account_balances SET balance_minor = -1 WHERE account_id = $1`, a.ID)
	require.True(t, db.IsCheckViolation(err, "account_balances_available_non_negative"), "%v", err)

	_, err = f.env.App.Exec(t.Context(), `UPDATE account_balances SET balance_minor = 100, held_minor = 101 WHERE account_id = $1`, a.ID)
	require.True(t, db.IsCheckViolation(err, "account_balances_available_non_negative"), "bloke bakiyeyi aşamaz: %v", err)

	// İç hesap eksiye düşebilir (ör. açılış özkaynağı).
	equity, err := f.accounts.InternalAccountID(t.Context(), nil, account.OpeningEquity, money.TRY)
	if err != nil { // iç hesaplar bu testten önce açılmamış olabilir
		_, err = f.accounts.EnsureInternalAccounts(audit.WithSystemActor(t.Context()))
		require.NoError(t, err)
		equity, err = f.accounts.InternalAccountID(t.Context(), nil, account.OpeningEquity, money.TRY)
		require.NoError(t, err)
	}
	_, err = f.env.App.Exec(t.Context(), `UPDATE account_balances SET balance_minor = balance_minor - 1 WHERE account_id = $1`, equity)
	require.NoError(t, err)
	_, err = f.env.App.Exec(t.Context(), `UPDATE account_balances SET balance_minor = balance_minor + 1 WHERE account_id = $1`, equity)
	require.NoError(t, err)
}

// Müşteri yalnız kendi hesaplarını görür (PLAN.md §5.10); ledger:read sahibi roller hepsini görür.
func TestObjectLevelAccess(t *testing.T) {
	f := newFixture(t)
	_, err := auth.NewService(f.txm, auth.Repo{}, auth.NewTokens(strings.Repeat("x", 32), time.Minute, time.Hour, clock.System{}), ids.V7{}, clock.System{}).
		SeedDemoUsers(t.Context(), "Demo.Parola.1")
	require.NoError(t, err)
	var userID uuid.UUID
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT id FROM users WHERE email = 'musteri@parakopru.test'`).Scan(&userID))

	// Kullanıcıya bağlı müşteri + bir hesap; ayrıca başka bir müşterinin hesabı.
	var mine customer.Customer
	require.NoError(t, f.txm.WithTx(audit.WithSystemActor(t.Context()), func(ctx context.Context, tx db.DBTX) error {
		var err error
		mine, _, err = f.customers.EnsureForUser(ctx, tx, userID, customer.CreateInput{FullName: "Ben", NationalID: uniqueNationalID()})
		return err
	}))
	myAcc := f.open(t, account.OpenInput{CustomerID: mine.ID, Kind: account.Checking, Currency: money.EUR})
	other := f.newCustomer(t)
	otherAcc := f.open(t, account.OpenInput{CustomerID: other.ID, Kind: account.Checking, Currency: money.EUR})

	me := auth.Principal{UserID: userID, Role: auth.RoleCustomer}
	got, err := f.accounts.Get(t.Context(), me, myAcc.ID)
	require.NoError(t, err)
	require.Equal(t, myAcc.ID, got.ID)

	_, err = f.accounts.Get(t.Context(), me, otherAcc.ID)
	require.ErrorIs(t, err, apperr.ErrForbidden, "başka müşterinin hesabı 403")

	// customer_id süzgeci başka müşteriyi gösterse de müşteri yalnız kendi hesaplarını alır.
	list, err := f.accounts.List(t.Context(), me, &other.ID, page.Request{Limit: 50})
	require.NoError(t, err)
	for _, a := range list.Items {
		require.Equal(t, mine.ID, *a.CustomerID)
	}

	auditor := auth.Principal{UserID: uuid.New(), Role: auth.RoleAuditor}
	_, err = f.accounts.Get(t.Context(), auditor, otherAcc.ID)
	require.NoError(t, err, "denetçi her hesabı görebilir")
	list, err = f.accounts.List(t.Context(), auditor, &other.ID, page.Request{Limit: 50})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)

	// Müşteri kaydı olmayan müşteri rolündeki kullanıcı hiçbir hesabı göremez.
	stranger := auth.Principal{UserID: uuid.New(), Role: auth.RoleCustomer}
	_, err = f.accounts.List(t.Context(), stranger, nil, page.Request{Limit: 50})
	require.ErrorIs(t, err, apperr.ErrForbidden)
}

func TestDuplicateNationalIDIsConflict(t *testing.T) {
	f := newFixture(t)
	id := uniqueNationalID()
	_, err := f.customers.Create(f.ops, customer.CreateInput{FullName: "Bir", NationalID: id})
	require.NoError(t, err)
	_, err = f.customers.Create(f.ops, customer.CreateInput{FullName: "İki", NationalID: id})
	require.ErrorIs(t, err, customer.ErrExists)
}

func TestListPaginationCoversAllAccounts(t *testing.T) {
	f := newFixture(t)
	c := f.newCustomer(t)
	for _, cur := range money.Currencies {
		f.open(t, account.OpenInput{CustomerID: c.ID, Kind: account.Checking, Currency: cur})
	}
	ops := auth.Principal{UserID: uuid.New(), Role: auth.RoleOperations}
	req := page.Request{Limit: 2}
	var seen []uuid.UUID
	for {
		p, err := f.accounts.List(t.Context(), ops, &c.ID, req)
		require.NoError(t, err)
		for _, a := range p.Items {
			seen = append(seen, a.ID)
		}
		if p.NextCursor == nil {
			break
		}
		req.Cursor = *p.NextCursor
	}
	require.Len(t, seen, 3)
}

func TestHandlers(t *testing.T) {
	f := newFixture(t)
	c := f.newCustomer(t)
	h := account.NewHandler(f.accounts)
	r := chi.NewRouter()
	r.Post("/accounts", h.Open)
	r.Get("/accounts", h.List)
	r.Get("/accounts/{id}", h.Get)

	call := func(p auth.Principal, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(auth.WithPrincipal(t.Context(), p), method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	ops := auth.Principal{UserID: uuid.New(), Role: auth.RoleOperations}

	rec := call(ops, http.MethodPost, "/accounts", `{"customer_id":"`+c.ID.String()+`","kind":"CHECKING","currency":"USD"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created account.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.Zero(t, created.AvailableMinor)
	require.Contains(t, rec.Body.String(), `"balance_minor":0`)

	rec = call(ops, http.MethodPost, "/accounts", `{"customer_id":"`+c.ID.String()+`","kind":"SAVINGS","currency":"USD"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = call(ops, http.MethodGet, "/accounts/"+created.ID.String(), "")
	require.Equal(t, http.StatusOK, rec.Code)

	rec = call(ops, http.MethodGet, "/accounts?customer_id="+c.ID.String(), "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), created.ID.String())

	rec = call(ops, http.MethodGet, "/accounts/bozuk-id", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	stranger := auth.Principal{UserID: uuid.New(), Role: auth.RoleCustomer}
	rec = call(stranger, http.MethodGet, "/accounts/"+created.ID.String(), "")
	require.Equal(t, http.StatusForbidden, rec.Code)
}
