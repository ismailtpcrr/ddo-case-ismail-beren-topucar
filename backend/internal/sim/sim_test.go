package sim_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"parakopru/internal/account"
	"parakopru/internal/customer"
	"parakopru/internal/ledger"
	"parakopru/internal/money"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
	"parakopru/internal/platform/ids"
	"parakopru/internal/sim"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestGenerateIsDeterministic(t *testing.T) {
	a, b := sim.Generate(sim.Params{Seed: 42, Customers: 500}), sim.Generate(sim.Params{Seed: 42, Customers: 500})
	require.Equal(t, a, b, "aynı tohum aynı veri")
	c := sim.Generate(sim.Params{Seed: 43, Customers: 500})
	require.NotEqual(t, a.Customers, c.Customers, "farklı tohum farklı veri")
}

func TestGenerateIdentifiersAreUniqueAndAvoidDemo(t *testing.T) {
	ds := sim.Generate(sim.Params{Seed: 1, Customers: 20_000})
	require.Len(t, ds.Customers, 20_000)
	nat, ids := map[string]bool{}, map[string]bool{}
	for _, c := range ds.Customers {
		require.Regexp(t, `^[0-9]{11}$`, c.NationalID)
		require.NotEqual(t, "10000000146", c.NationalID, "demo müşterinin numarası")
		require.False(t, nat[c.NationalID], "kimlik no çakıştı")
		nat[c.NationalID] = true
		ids[c.ID.String()] = true
	}
	var withMoney int
	for _, a := range ds.Accounts {
		ids[a.ID.String()] = true
		if a.Opening > 0 {
			withMoney++
			ids[a.EntryID.String()], ids[a.DebitID.String()], ids[a.CreditID.String()] = true, true, true
		}
		if a.Kind == account.TimeDeposit {
			require.NotNil(t, a.MaturityDate)
		}
	}
	require.Greater(t, len(ds.Accounts), 20_000)
	require.Equal(t, len(ds.Customers)+len(ds.Accounts)+4*withMoney-withMoney, len(ids)-0, "tüm UUID'ler benzersiz")
	for _, c := range ds.Customers[:50] {
		require.Equal(t, byte(7), c.ID[6]>>4, "UUIDv7")
	}
}

func TestSeedWritesBalancedLedgerAndIsIdempotent(t *testing.T) {
	env := dbtest.Get(t)
	txm := db.NewTxManager(env.App)
	rec := audit.NewRecorder(ids.V7{})
	custSvc := customer.NewService(txm, ids.V7{}, rec)
	accSvc := account.NewService(txm, custSvc, ids.V7{}, clock.System{}, rec)
	_, err := accSvc.EnsureInternalAccounts(audit.WithSystemActor(t.Context()))
	require.NoError(t, err)
	ledgerSvc := ledger.NewService(txm, ids.V7{}, rec, accSvc)

	p := sim.Params{Seed: 7, Customers: 300}
	skipped, err := sim.Seed(t.Context(), txm, rec, p)
	require.NoError(t, err)
	require.False(t, skipped)

	ds := sim.Generate(p)
	var customers, accounts int
	require.NoError(t, env.App.QueryRow(t.Context(), `SELECT count(*) FROM customers`).Scan(&customers))
	require.NoError(t, env.App.QueryRow(t.Context(), `SELECT count(*) FROM accounts WHERE customer_id IS NOT NULL`).Scan(&accounts))
	require.Equal(t, 300, customers)
	require.Equal(t, len(ds.Accounts), accounts)

	// Seed sonrası da defter dengede ve her özet defterden türetilen bakiyeye eşit.
	inv, err := ledgerSvc.CheckInvariants(t.Context())
	require.NoError(t, err)
	require.True(t, inv.OK, "%+v", inv)
	require.Positive(t, inv.Postings)

	// Açılış bakiyesi API'den görünen bakiyedir ve IBAN'lar geçerli.
	first := ds.Accounts[0]
	got, err := accSvc.Get(t.Context(), authOps(), first.ID)
	require.NoError(t, err)
	require.Equal(t, first.Opening, got.BalanceMinor)
	require.True(t, account.ValidIBAN(*got.IBAN))

	// OPENING_EQUITY, dağıtılan toplam kadar negatif.
	var equity int64
	require.NoError(t, env.App.QueryRow(t.Context(), `
		SELECT b.balance_minor FROM account_balances b JOIN accounts a ON a.id = b.account_id
		 WHERE a.kind = 'OPENING_EQUITY' AND a.currency = 'TRY'`).Scan(&equity))
	var total int64
	for _, a := range ds.Accounts {
		if a.Currency == money.TRY {
			total += a.Opening
		}
	}
	require.Equal(t, -total, equity)

	// Aynı tohumla ikinci çalıştırma hiçbir şey yazmaz.
	skipped, err = sim.Seed(context.Background(), txm, rec, p)
	require.NoError(t, err)
	require.True(t, skipped)
	require.NoError(t, env.App.QueryRow(t.Context(), `SELECT count(*) FROM customers`).Scan(&customers))
	require.Equal(t, 300, customers)
}

func authOps() auth.Principal { return auth.Principal{UserID: uuid.New(), Role: auth.RoleOperations} }
