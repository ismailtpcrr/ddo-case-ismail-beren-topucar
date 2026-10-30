package interest

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"parakopru/internal/money"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func date(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }

func TestDaysInYear(t *testing.T) {
	cases := []struct {
		b    DayBasis
		day  time.Time
		want int64
	}{
		{Act360, date(2024, 2, 29), 360},
		{Act365, date(2024, 2, 29), 365},
		{ActAct, date(2024, 2, 29), 366}, // artık yıl
		{ActAct, date(2025, 2, 28), 365},
		{ActAct, date(2000, 6, 1), 366}, // 400'e bölünen
		{ActAct, date(2100, 6, 1), 365}, // 100'e bölünüp 400'e bölünmeyen
	}
	for _, c := range cases {
		require.Equal(t, c.want, DaysInYear(c.b, c.day), "%s %s", c.b, c.day.Format(time.DateOnly))
	}
}

// Gün tabanı tablosu: 100.000,00 TL, yıllık %36 basit faiz, bir gün.
func TestDailyGrossByBasis(t *testing.T) {
	cases := []struct {
		b    DayBasis
		day  time.Time
		want string // hassas brüt (TL)
	}{
		{Act360, date(2026, 3, 1), "100"},                  // 100000 × 0.36 / 360
		{Act365, date(2026, 3, 1), "98.6301369863013699"},  // / 365
		{ActAct, date(2024, 2, 29), "98.3606557377049180"}, // artık yıl: / 366
		{ActAct, date(2026, 2, 28), "98.6301369863013699"}, // normal yıl: / 365
	}
	for _, c := range cases {
		_, day, err := Accrue(State{}, 100_000_00, money.TRY, Rate{Simple, c.b, d("0.36")}, decimal.Zero, c.day)
		require.NoError(t, err)
		require.True(t, day.ExactGross.Round(16).Equal(d(c.want)), "%s: %s", c.b, day.ExactGross)
	}
}

// Kuruş dağıtımı: günlük 1/3 kuruşluk faiz kaybolmaz; 3 günde tam 1 kuruş yazılır.
func TestCentDistributionNeverLosesAPenny(t *testing.T) {
	// 0,01 × 365/... yerine doğrudan: 1.000,00 TL, yıllık oran öyle ki günlük faiz 0,0033… TL.
	r := Rate{Simple, Act360, d("0.0012")} // günlük = 1000 × 0.0012 / 360 = 0,003333… TL
	var s State
	var postedDaily []int64
	for i := range 3 {
		var day Day
		var err error
		s, day, err = Accrue(s, 1_000_00, money.TRY, r, decimal.Zero, date(2026, 1, 1+i))
		require.NoError(t, err)
		postedDaily = append(postedDaily, day.GrossMinor)
	}
	require.Equal(t, []int64{0, 1, 0}, postedDaily, "0,33 → 0; 0,67 → 1; 1,00 → 1 (kümülatif yuvarlama)")
	require.Equal(t, int64(1), s.PostedMinor)
	// Günlük yuvarlasaydık her gün 0 yazılır, 1 kuruş kaybolurdu.
}

func TestCompoundGrowsFasterThanSimple(t *testing.T) {
	run := func(m Method) State {
		var s State
		for i := range 365 {
			var err error
			s, _, err = Accrue(s, 100_000_00, money.TRY, Rate{m, Act365, d("0.42")}, decimal.Zero, date(2026, 1, 1).AddDate(0, 0, i))
			require.NoError(t, err)
		}
		return s
	}
	simple, compound := run(Simple), run(Compound)
	require.Equal(t, int64(42_000_00), simple.PostedMinor, "basit: tam %42")
	// Günlük bileşik: 100000 × ((1 + 0.42/365)^365 − 1) = 52.159,4109… TL → 52.159,41
	require.Equal(t, int64(52_159_41), compound.PostedMinor)
}

// Oran değişince geçmiş günler etkilenmez: her gün kendi oranıyla hesaplanır.
func TestRateChangeDoesNotAffectPastDays(t *testing.T) {
	rateOn := func(day time.Time) Rate {
		if day.Before(date(2026, 1, 11)) {
			return Rate{Simple, Act360, d("0.36")}
		}
		return Rate{Simple, Act360, d("0.72")}
	}
	var s State
	var first10 int64
	for i := range 20 {
		day := date(2026, 1, 1).AddDate(0, 0, i)
		var r Day
		var err error
		s, r, err = Accrue(s, 100_000_00, money.TRY, rateOn(day), decimal.Zero, day)
		require.NoError(t, err)
		if i < 10 {
			first10 += r.GrossMinor
		}
	}
	require.Equal(t, int64(1_000_00), first10, "ilk 10 gün %36: günde 100 TL")
	require.Equal(t, int64(3_000_00), s.PostedMinor, "sonraki 10 gün %72: günde 200 TL")
}

func TestWithholding(t *testing.T) {
	tax, net, err := Withhold(100_00, money.TRY, d("0.15"))
	require.NoError(t, err)
	require.Equal(t, int64(15_00), tax)
	require.Equal(t, int64(85_00), net)
	tax, net, err = Withhold(1_10, money.TRY, d("0.15")) // 16,5 kuruş → çifte: 16
	require.NoError(t, err)
	require.Equal(t, int64(16), tax)
	require.Equal(t, int64(94), net)
	tax, _, _ = Withhold(1_30, money.TRY, d("0.15")) // 19,5 → 20
	require.Equal(t, int64(20), tax)
}

// Property: herhangi bir gün dizisi, anapara, oran ve stopaj için
//   - günlük yazılan brütlerin toplamı = yuvarla(hassas toplam faiz) (kuruş dağıtımı tam),
//   - her gün brüt = net + vergi ve hiçbiri eksi değil.
func TestPropertyCentDistributionAndWithholding(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		principal := rapid.Int64Range(0, 10_000_000_00).Draw(rt, "anapara")
		rate := decimal.New(rapid.Int64Range(0, 9_999).Draw(rt, "oran"), -4) // 0 – %99,99
		tax := decimal.New(rapid.Int64Range(0, 9_999).Draw(rt, "stopaj"), -4)
		m := rapid.SampledFrom([]Method{Simple, Compound}).Draw(rt, "yöntem")
		b := rapid.SampledFrom([]DayBasis{Act360, Act365, ActAct}).Draw(rt, "taban")
		days := rapid.IntRange(1, 400).Draw(rt, "gün")
		start := date(2023, 12, 1).AddDate(0, 0, rapid.IntRange(0, 900).Draw(rt, "başlangıç"))

		var s State
		var sumGross int64
		for i := range days {
			var r Day
			var err error
			s, r, err = Accrue(s, principal, money.TRY, Rate{m, b, rate}, tax, start.AddDate(0, 0, i))
			if err != nil {
				rt.Fatal(err)
			}
			if r.GrossMinor < 0 || r.TaxMinor < 0 || r.NetMinor < 0 || r.GrossMinor != r.NetMinor+r.TaxMinor {
				rt.Fatalf("gün %d: brüt %d, vergi %d, net %d", i, r.GrossMinor, r.TaxMinor, r.NetMinor)
			}
			sumGross += r.GrossMinor
		}
		want, err := money.FromDecimal(s.CumulativeExact, money.TRY)
		if err != nil {
			rt.Fatal(err)
		}
		if sumGross != want.Minor || sumGross != s.PostedMinor {
			rt.Fatalf("Σ günlük %d ≠ yuvarla(toplam) %d", sumGross, want.Minor)
		}
	})
}
