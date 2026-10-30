package interest_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"parakopru/internal/account"
	"parakopru/internal/customer"
	"parakopru/internal/interest"
	"parakopru/internal/ledger"
	"parakopru/internal/money"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/jobs"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

var nid atomic.Int64

var nidBase = time.Now().UnixNano() % 1e9 * 10

type fixture struct {
	env      *dbtest.Env
	txm      *db.TxManager
	accounts *account.Service
	ledger   *ledger.Service
	custs    *customer.Service
	clk      *clock.Manual
	ops      auth.Principal
}

// Testler ayrı bir yılda çalışır: her test kendi tarihlerini kullanır (çalıştırmalar tarihe göre tekildir).
func newFixture(t *testing.T) fixture {
	t.Helper()
	env := dbtest.Get(t)
	txm := db.NewTxManager(env.App)
	rec := audit.NewRecorder(ids.V7{})
	custs := customer.NewService(txm, ids.V7{}, rec)
	accs := account.NewService(txm, custs, ids.V7{}, clock.System{}, rec)
	_, err := accs.EnsureInternalAccounts(audit.WithSystemActor(t.Context()))
	require.NoError(t, err)
	id := uuid.New()
	_, err = env.App.Exec(t.Context(), `INSERT INTO users (id, email, password_hash, role, full_name) VALUES ($1, $2, 'x', 'OPERATIONS', 'Ops')`, id, id.String()+"@parakopru.test")
	require.NoError(t, err)
	return fixture{env: env, txm: txm, accounts: accs, ledger: ledger.NewService(txm, ids.V7{}, rec, accs), custs: custs,
		clk: clock.NewManual(time.Date(2027, 12, 31, 12, 0, 0, 0, time.UTC)), ops: auth.Principal{UserID: id, Role: auth.RoleOperations}}
}

func (f fixture) service(cfg interest.Config) *interest.Service {
	rec := audit.NewRecorder(ids.V7{})
	return interest.NewService(f.txm, ids.V7{}, f.clk, rec, f.ledger, f.accounts, nil, jobs.NewQueue(ids.V7{}, f.clk), cfg)
}

// deposit vadeli TRY hesap açar ve bakiye yükler (açılış tarihi geçmişte olsun diye opened_at elle geriye alınır).
func (f fixture) deposit(t *testing.T, minor int64) account.Account {
	t.Helper()
	ops := auth.WithPrincipal(t.Context(), f.ops)
	cu, err := f.custs.Create(ops, customer.CreateInput{FullName: "Faiz Test", NationalID: fmt.Sprintf("7%010d", nidBase+nid.Add(1))})
	require.NoError(t, err)
	a, err := f.accounts.Open(ops, account.OpenInput{CustomerID: cu.ID, Kind: account.TimeDeposit, Currency: money.TRY, MaturityDate: "2030-01-01"})
	require.NoError(t, err)
	_, err = f.env.Owner.Exec(t.Context(), `UPDATE accounts SET opened_at = '2026-01-01' WHERE id = $1`, a.ID)
	require.NoError(t, err)
	eq, err := f.accounts.InternalAccountID(t.Context(), nil, account.OpeningEquity, money.TRY)
	require.NoError(t, err)
	amt := money.New(minor, money.TRY)
	require.NoError(t, f.txm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
		_, err := f.ledger.Post(ctx, tx, ledger.Entry{Kind: ledger.KindOpeningBalance, Description: "açılış",
			Lines: []ledger.Line{ledger.Debit(eq, amt), ledger.Credit(a.ID, amt)}})
		return err
	}))
	return a
}

func (f fixture) start(t *testing.T, svc *interest.Service, day string) {
	t.Helper()
	_, _, err := svc.Start(t.Context(), f.ops, interest.RunInput{BusinessDate: day})
	require.NoError(t, err)
}

func (f fixture) accrualCount(t *testing.T, a uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT count(*) FROM interest_accruals WHERE account_id = $1`, a).Scan(&n))
	return n
}

func (f fixture) balance(t *testing.T, a uuid.UUID) int64 {
	t.Helper()
	var b int64
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT balance_minor FROM account_balances WHERE account_id = $1`, a).Scan(&b))
	return b
}

// expected aynı günleri saf fonksiyonla hesaplar (gün sonu işinin sonucu bununla birebir aynı olmalı).
func expected(t *testing.T, principal int64, days []time.Time) (gross, net int64) {
	t.Helper()
	var s interest.State
	for _, d := range days {
		var r interest.Day
		var err error
		s, r, err = interest.Accrue(s, principal, money.TRY, interest.Rate{Method: interest.Compound, Basis: interest.Act365, AnnualRate: decimal.RequireFromString("0.42")},
			decimal.RequireFromString("0.15"), d)
		require.NoError(t, err)
		gross += r.GrossMinor
		net += r.NetMinor
	}
	return gross, net
}

func TestEODAccruesInterestWithTaxIntoLedger(t *testing.T) {
	f := newFixture(t)
	a := f.deposit(t, 100_000_00)
	svc := f.service(interest.Config{BatchSize: 1000})
	days := []string{"2027-03-01", "2027-03-02", "2027-03-03"}
	var dts []time.Time
	for _, d := range days {
		f.start(t, svc, d)
		require.NoError(t, svc.Run(audit.WithSystemActor(t.Context()), d))
		dt, _ := time.Parse(time.DateOnly, d)
		dts = append(dts, dt)
	}
	gross, net := expected(t, 100_000_00, dts)
	require.Equal(t, 3, f.accrualCount(t, a.ID))
	require.Equal(t, int64(100_000_00)+net, f.balance(t, a.ID), "müşteriye net faiz geçti")

	var g, tx, n int64
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT sum(gross_minor), sum(tax_minor), sum(net_minor) FROM interest_accruals WHERE account_id = $1`, a.ID).Scan(&g, &tx, &n))
	require.Equal(t, gross, g)
	require.Equal(t, g, n+tx, "brüt = net + vergi")
	require.Positive(t, tx)

	run, err := svc.Get(t.Context(), "2027-03-03")
	require.NoError(t, err)
	require.Equal(t, "DONE", run.Status)
	require.NotEmpty(t, run.Totals)

	// Defter: her kayıt dengeli; INTEREST_EXPENSE brüt kadar, WITHHOLDING_TAX vergi kadar.
	inv, err := f.ledger.CheckInvariants(t.Context())
	require.NoError(t, err)
	require.True(t, inv.OK, "%+v", inv)

	// Aynı günü tekrar başlatmak/çalıştırmak çift tahakkuk yapmaz.
	run2, created, err := svc.Start(t.Context(), f.ops, interest.RunInput{BusinessDate: "2027-03-03"})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, "DONE", run2.Status)
	require.NoError(t, svc.Run(audit.WithSystemActor(t.Context()), "2027-03-03"))
	require.Equal(t, 3, f.accrualCount(t, a.ID))
}

// Çökme testi: 7 hesap, partide 2 hesap; 2 parti sonra "çöker". Yeniden çalıştırma kaldığı yerden devam eder; her hesap
// tam bir kez tahakkuk eder ve tutarlar çökmesiz çalıştırmayla birebir aynıdır.
func TestEODResumesAfterCrashWithoutDoubleAccrual(t *testing.T) {
	f := newFixture(t)
	var accs []account.Account
	for i := range 7 {
		accs = append(accs, f.deposit(t, int64(10_000_00*(i+1))))
	}
	day := "2027-05-10"
	crashing := f.service(interest.Config{BatchSize: 2, CrashAfterBatches: 2})
	f.start(t, crashing, day)
	err := crashing.Run(audit.WithSystemActor(t.Context()), day)
	require.ErrorIs(t, err, interest.ErrSimulatedEnd)

	done := 0
	for _, a := range accs {
		done += f.accrualCount(t, a.ID)
	}
	require.Positive(t, done, "çökmeden önceki partiler kalıcı")
	require.Less(t, done, 7, "iş yarıda kaldı")
	run, err := crashing.Get(t.Context(), day)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", run.Status)

	// Yeniden başlat (çökme yok): kaldığı yerden devam.
	require.NoError(t, f.service(interest.Config{BatchSize: 2}).Run(audit.WithSystemActor(t.Context()), day))
	dt, _ := time.Parse(time.DateOnly, day)
	for i, a := range accs {
		require.Equal(t, 1, f.accrualCount(t, a.ID), "hesap %d tam bir kez", i)
		_, net := expected(t, int64(10_000_00*(i+1)), []time.Time{dt})
		require.Equal(t, int64(10_000_00*(i+1))+net, f.balance(t, a.ID))
	}
	run, err = crashing.Get(t.Context(), day)
	require.NoError(t, err)
	require.Equal(t, "DONE", run.Status)
	require.Equal(t, 2, run.Attempts)
	// Üçüncü çalıştırma hiçbir şey yapmaz.
	require.NoError(t, f.service(interest.Config{BatchSize: 2}).Run(audit.WithSystemActor(t.Context()), day))
	var total int
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT count(*) FROM interest_accruals WHERE accrual_date = $1`, day).Scan(&total))
	require.GreaterOrEqual(t, total, 7)
	inv, err := f.ledger.CheckInvariants(t.Context())
	require.NoError(t, err)
	require.True(t, inv.OK)
}

// Veritabanı güvencesi: aynı hesap/gün için ikinci tahakkuk satırı yazılamaz.
func TestAccrualUniquePerAccountAndDay(t *testing.T) {
	f := newFixture(t)
	a := f.deposit(t, 1_000_00)
	insert := func() error {
		_, err := f.env.App.Exec(t.Context(), `INSERT INTO interest_accruals (id, account_id, accrual_date, principal_minor, method, day_basis, annual_rate, tax_rate,
			gross_minor, tax_minor, net_minor, cumulative_exact, cumulative_posted) VALUES (gen_random_uuid(), $1, '2027-06-01', 0, 'SIMPLE', 'ACT_365', 0, 0, 0, 0, 0, 0, 0)`, a.ID)
		return err
	}
	require.NoError(t, insert())
	require.True(t, db.IsUniqueViolation(insert(), "interest_accruals_once"))
}

func TestStartRules(t *testing.T) {
	f := newFixture(t)
	svc := f.service(interest.Config{})
	_, _, err := svc.Start(t.Context(), f.ops, interest.RunInput{BusinessDate: "2099-01-01"})
	require.ErrorIs(t, err, interest.ErrFutureDate)
	f.start(t, svc, "2027-09-10")
	_, _, err = svc.Start(t.Context(), f.ops, interest.RunInput{BusinessDate: "2027-09-01"})
	require.ErrorIs(t, err, interest.ErrOutOfOrder, "daha sonraki gün yapılmışken geçmiş gün açılamaz")
	_, err = svc.Get(t.Context(), "2027-09-02")
	require.True(t, errors.Is(err, interest.ErrRunNotFound))
	var queued int
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE kind = 'eod.run' AND payload->>'business_date' = '2027-09-10'`).Scan(&queued))
	require.Equal(t, 1, queued, "outbox: iş kuyruğa girdi")
}
