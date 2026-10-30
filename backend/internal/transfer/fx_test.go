package transfer_test

// Çok para birimli transfer (F-5; PLAN.md §5.5): tek defter kaydı, her para biriminde borç = alacak, kur kayda yazılır.

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"parakopru/internal/account"
	"parakopru/internal/money"
	"parakopru/internal/transfer"
)

func TestCrossCurrencyTransferIsOneBalancedEntry(t *testing.T) {
	f := newFixture(t)
	p, from := f.owned(t, money.TRY, 10_000_00)
	_, to := f.owned(t, money.USD, 0)
	fxTRY, err := f.accounts.InternalAccountID(t.Context(), nil, account.FXPosition, money.TRY)
	require.NoError(t, err)
	tryBefore := f.balance(t, fxTRY)

	tr, _, err := f.transfer.Create(t.Context(), p, "fx-1", transfer.CreateInput{
		FromAccountID: from.ID, ToIBAN: *to.IBAN, AmountMinor: 1_000_00, Currency: money.TRY})
	require.NoError(t, err)
	require.Equal(t, transfer.Completed, tr.State)
	require.Equal(t, int64(25_00), *tr.ToAmountMinor, "1.000 TL / 40 = 25,00 USD")
	require.Equal(t, money.USD, *tr.ToCurrency)
	require.True(t, tr.FXRate.Equal(decimal.RequireFromString("0.025")), "1 TRY = 0,025 USD")
	require.Equal(t, int64(9_000_00), f.balance(t, from.ID))
	require.Equal(t, int64(25_00), f.balance(t, to.ID))

	// Tek kayıt, dört satır; her para biriminde borç = alacak. FX pozisyon hesapları iki para biriminde karşılık tutar.
	e, err := f.ledger.Entry(t.Context(), *tr.JournalEntryID)
	require.NoError(t, err)
	require.Len(t, e.Postings, 4)
	sums := map[money.Currency]int64{}
	for _, pst := range e.Postings {
		sums[pst.Currency] += pst.AmountMinor
	}
	require.Equal(t, int64(0), sums[money.TRY])
	require.Equal(t, int64(0), sums[money.USD])
	require.Equal(t, tryBefore-1_000_00, f.balance(t, fxTRY), "FX_POSITION_TRY (DEBIT doğal taraflı) alacaklanınca azalır")
	f.requireFXInvariants(t)

	// Kur sonradan değişse de geçmiş transfer etkilenmez (kur kayda yazılmıştı).
	_, err = f.env.Owner.Exec(t.Context(), `INSERT INTO fx_rates (id, base, rate, valid_from) VALUES (gen_random_uuid(), 'USD', 99, now())`)
	require.NoError(t, err)
	got, err := f.transfer.Get(t.Context(), f.ops, tr.ID)
	require.NoError(t, err)
	require.True(t, got.FXRate.Equal(decimal.RequireFromString("0.025")))
	require.Equal(t, int64(25_00), *got.ToAmountMinor)
}

func (f fixture) requireFXInvariants(t *testing.T) {
	t.Helper()
	inv, err := f.ledger.CheckInvariants(t.Context())
	require.NoError(t, err)
	require.True(t, inv.OK, "%+v", inv)
}

// Dört göz eşiği TL karşılığına göre uygulanır: 200 USD × 50 = tam 10.000,00 TL → onay yok; bir sent fazlası → onay.
func TestApprovalThresholdUsesTRYEquivalent(t *testing.T) {
	f := newFixture(t)
	p, from := f.owned(t, money.USD, 5_000_00)
	_, to := f.owned(t, money.USD, 0)
	_, err := f.env.Owner.Exec(t.Context(), `INSERT INTO fx_rates (id, base, rate, valid_from) VALUES (gen_random_uuid(), 'USD', 50, now())`)
	require.NoError(t, err)

	send := func(key string, minor int64) transfer.Transfer {
		tr, _, err := f.transfer.Create(t.Context(), p, key, in(from, to, minor))
		require.NoError(t, err)
		return tr
	}
	require.Equal(t, transfer.Completed, send("t-exact", 200_00).State, "200 USD × 50 = 10.000,00 TL: eşiğin üzeri değil")
	require.Equal(t, transfer.PendingApproval, send("t-over", 200_01).State, "10.000,50 TL: eşiğin üzeri")
	require.Equal(t, transfer.Completed, send("t-small", 1_00).State)
}
