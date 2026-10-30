package money_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"parakopru/internal/money"
)

func TestRoundHalfEven(t *testing.T) {
	cases := []struct{ in, want string }{
		// Tam yarım: son basamak çift olana yuvarlanır.
		{"0.125", "0.12"},
		{"0.135", "0.14"},
		{"0.145", "0.14"},
		{"2.675", "2.68"},
		{"-0.125", "-0.12"},
		{"-0.135", "-0.14"},
		// Yarım değil: en yakına.
		{"0.1251", "0.13"},
		{"0.1249", "0.12"},
		{"-0.1251", "-0.13"},
		// Zaten 2 basamak.
		{"10.50", "10.5"},
		{"0", "0"},
	}
	for _, tc := range cases {
		got := money.RoundHalfEven(decimal.RequireFromString(tc.in), 2)
		require.True(t, got.Equal(decimal.RequireFromString(tc.want)), "%s → beklenen %s, gelen %s", tc.in, tc.want, got)
	}
}

// Tam sayıya yuvarlamada klasik banker's rounding örnekleri.
func TestRoundHalfEvenToInteger(t *testing.T) {
	for in, want := range map[string]int64{"0.5": 0, "1.5": 2, "2.5": 2, "3.5": 4, "-2.5": -2, "-3.5": -4} {
		require.Equal(t, want, money.RoundHalfEven(decimal.RequireFromString(in), 0).IntPart(), in)
	}
}

func TestFromDecimal(t *testing.T) {
	a, err := money.FromDecimal(decimal.RequireFromString("12.345"), money.TRY)
	require.NoError(t, err)
	require.Equal(t, money.New(1234, money.TRY), a)

	a, err = money.FromDecimal(decimal.RequireFromString("12.355"), money.TRY)
	require.NoError(t, err)
	require.Equal(t, money.New(1236, money.TRY), a)

	_, err = money.FromDecimal(decimal.RequireFromString("1e30"), money.TRY)
	require.ErrorIs(t, err, money.ErrOverflow)
}

func TestAddSubRequireSameCurrency(t *testing.T) {
	tl := money.New(1000, money.TRY)
	usd := money.New(1000, money.USD)

	_, err := tl.Add(usd)
	require.ErrorIs(t, err, money.ErrCurrencyMismatch)
	_, err = tl.Sub(usd)
	require.ErrorIs(t, err, money.ErrCurrencyMismatch)
	_, err = tl.Cmp(usd)
	require.ErrorIs(t, err, money.ErrCurrencyMismatch)

	sum, err := tl.Add(money.New(250, money.TRY))
	require.NoError(t, err)
	require.Equal(t, money.New(1250, money.TRY), sum)

	diff, err := tl.Sub(money.New(1250, money.TRY))
	require.NoError(t, err)
	require.Equal(t, money.New(-250, money.TRY), diff)
	require.True(t, diff.IsNegative())
}

func TestOverflowIsAnError(t *testing.T) {
	max := money.New(math.MaxInt64, money.TRY)
	_, err := max.Add(money.New(1, money.TRY))
	require.ErrorIs(t, err, money.ErrOverflow)

	min := money.New(math.MinInt64, money.TRY)
	_, err = min.Sub(money.New(1, money.TRY))
	require.ErrorIs(t, err, money.ErrOverflow)
	_, err = min.Neg()
	require.ErrorIs(t, err, money.ErrOverflow)
}

func TestCmp(t *testing.T) {
	for _, tc := range []struct {
		a, b int64
		want int
	}{{1, 2, -1}, {2, 2, 0}, {3, 2, 1}, {-5, 5, -1}} {
		got, err := money.New(tc.a, money.EUR).Cmp(money.New(tc.b, money.EUR))
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
}

func TestFormatAndString(t *testing.T) {
	cases := map[int64]string{0: "0.00", 5: "0.05", -5: "-0.05", 123456: "1234.56", -100: "-1.00", math.MinInt64: "-92233720368547758.08"}
	for minor, want := range cases {
		require.Equal(t, want, money.New(minor, money.TRY).Format())
	}
	require.Equal(t, "10.50 USD", money.New(1050, money.USD).String())
}

func TestParseAmount(t *testing.T) {
	ok := map[string]int64{
		"0": 0, "100": 10000, "1234.56": 123456, "1234.5": 123450, "-0.05": -5, "007.10": 710,
		"92233720368547758.07": math.MaxInt64,
	}
	for in, want := range ok {
		a, err := money.ParseAmount(in, money.TRY)
		require.NoError(t, err, in)
		require.Equal(t, money.New(want, money.TRY), a, in)
	}

	bad := []string{"", "-", "+5", "1,50", "1.234,56", "1.", ".5", "1.234", "1e3", " 1", "1 ", "NaN", "Inf", "0x10", "12.3.4", "--1"}
	for _, in := range bad {
		_, err := money.ParseAmount(in, money.TRY)
		require.ErrorIs(t, err, money.ErrInvalidAmount, "%q", in)
	}

	_, err := money.ParseAmount("92233720368547758.08", money.TRY)
	require.ErrorIs(t, err, money.ErrOverflow)
	_, err = money.ParseAmount("10", money.Currency("GBP"))
	require.ErrorIs(t, err, money.ErrUnknownCurrency)
}

func TestCurrency(t *testing.T) {
	for _, c := range []string{"TRY", "USD", "EUR"} {
		got, err := money.ParseCurrency(c)
		require.NoError(t, err)
		require.True(t, got.Valid())
	}
	for _, c := range []string{"", "try", "GBP", "TL"} {
		_, err := money.ParseCurrency(c)
		require.ErrorIs(t, err, money.ErrUnknownCurrency, c)
	}
}

// API sözleşmesi (PLAN.md §7): tutar her zaman tam sayı amount_minor + currency; ondalıklı sayı değil.
func TestJSONShape(t *testing.T) {
	b, err := json.Marshal(money.New(1050, money.TRY))
	require.NoError(t, err)
	require.JSONEq(t, `{"amount_minor":1050,"currency":"TRY"}`, string(b))
}
