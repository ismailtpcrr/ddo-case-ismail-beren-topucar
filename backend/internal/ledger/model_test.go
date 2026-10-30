package ledger

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"parakopru/internal/account"
	"parakopru/internal/money"
)

func try(minor int64) money.Amount { return money.New(minor, money.TRY) }
func usd(minor int64) money.Amount { return money.New(minor, money.USD) }

func TestValidateAcceptsBalancedEntries(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	cases := map[string][]Line{
		"iki satır":       {Debit(a, try(100)), Credit(b, try(100))},
		"bölünmüş alacak": {Debit(a, try(100)), Credit(b, try(60)), Credit(c, try(40))},
		// Kur dönüşümlü transfer (PLAN.md §5.5): her para biriminde ayrı ayrı borç = alacak.
		"iki para birimi":      {Debit(a, try(3_400_00)), Credit(b, try(3_400_00)), Debit(c, usd(100_00)), Credit(d, usd(100_00))},
		"aynı hesap iki satır": {Debit(a, try(50)), Debit(a, try(50)), Credit(b, try(100))},
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, Entry{Kind: KindOpeningBalance, Description: "test", Lines: lines}.Validate())
		})
	}
}

func TestValidateRejects(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	ok := []Line{Debit(a, try(100)), Credit(b, try(100))}
	cases := []struct {
		name string
		e    Entry
		want error
	}{
		{"tek satır", Entry{Kind: "X_Y", Description: "d", Lines: ok[:1]}, ErrInvalidEntry},
		{"tür yok", Entry{Description: "d", Lines: ok}, ErrInvalidEntry},
		{"açıklama yok", Entry{Kind: "X_Y", Lines: ok}, ErrInvalidEntry},
		{"sıfır tutar", Entry{Kind: "X_Y", Description: "d", Lines: []Line{Debit(a, try(0)), Credit(b, try(0))}}, ErrInvalidEntry},
		{"negatif tutar", Entry{Kind: "X_Y", Description: "d", Lines: []Line{Debit(a, try(-5)), Debit(b, try(5))}}, ErrInvalidEntry},
		{"hesap yok", Entry{Kind: "X_Y", Description: "d", Lines: []Line{Debit(uuid.Nil, try(1)), Credit(b, try(1))}}, ErrInvalidEntry},
		{"geçersiz yön", Entry{Kind: "X_Y", Description: "d", Lines: []Line{{AccountID: a, Side: "LEFT", Amount: try(1)}, Credit(b, try(1))}}, ErrInvalidEntry},
		{"bilinmeyen para birimi", Entry{Kind: "X_Y", Description: "d", Lines: []Line{Debit(a, money.New(1, "GBP")), Credit(b, money.New(1, "GBP"))}}, ErrInvalidEntry},
		{"taşma", Entry{Kind: "X_Y", Description: "d", Lines: []Line{Debit(a, try(math.MaxInt64)), Debit(a, try(1)), Credit(b, try(1))}}, ErrInvalidEntry},
		{"borç ≠ alacak", Entry{Kind: "X_Y", Description: "d", Lines: []Line{Debit(a, try(100)), Credit(b, try(99))}}, ErrUnbalanced},
		// Sayılar tutuyor ama para birimleri farklı: 100 TRY borç, 100 USD alacak dengeli değildir.
		{"para birimleri karışık", Entry{Kind: "X_Y", Description: "d", Lines: []Line{Debit(a, try(100)), Credit(b, usd(100))}}, ErrUnbalanced},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, tc.e.Validate(), tc.want)
		})
	}
}

// Bakiye etkisi hesabın doğal tarafına göredir: müşteri hesabı (CREDIT) alacaklanınca bakiye artar,
// NOSTRO (DEBIT) borçlanınca artar.
func TestNaturalDelta(t *testing.T) {
	require.Equal(t, int64(100), naturalDelta(account.Credit, Credit(uuid.New(), try(100)).signed()))
	require.Equal(t, int64(-100), naturalDelta(account.Credit, Debit(uuid.New(), try(100)).signed()))
	require.Equal(t, int64(100), naturalDelta(account.Debit, Debit(uuid.New(), try(100)).signed()))
	require.Equal(t, int64(-100), naturalDelta(account.Debit, Credit(uuid.New(), try(100)).signed()))
}

func TestToResponseShowsSideAndPositiveAmount(t *testing.T) {
	e := JournalEntry{Postings: []Posting{{AmountMinor: 250, Currency: money.TRY}, {AmountMinor: -250, Currency: money.TRY}}}
	r := e.ToResponse()
	require.Equal(t, account.Debit, r.Postings[0].Side)
	require.Equal(t, account.Credit, r.Postings[1].Side)
	require.Equal(t, int64(250), r.Postings[1].AmountMinor)

	ap := AccountPosting{AmountMinor: -250}.ToResponse(account.Credit)
	require.Equal(t, account.Credit, ap.Side)
	require.Equal(t, int64(250), ap.AmountMinor)
	require.Equal(t, int64(250), ap.BalanceEffectMinor, "müşteri hesabına alacak = para girişi")
}

func TestManuallyReversible(t *testing.T) {
	require.True(t, KindOpeningBalance.ManuallyReversible())
	require.False(t, KindReversal.ManuallyReversible())
	require.False(t, Kind("TRANSFER").ManuallyReversible())
}
