package money_test

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"parakopru/internal/money"
)

func genCurrency() *rapid.Generator[money.Currency] {
	return rapid.SampledFrom(money.Currencies)
}

// Gerçekçi aralıkta tutar: ±1 trilyon TL. Toplamaların taşmadığı bölgede cebir kurallarını test eder.
func genAmount(c money.Currency) *rapid.Generator[money.Amount] {
	return rapid.Custom(func(t *rapid.T) money.Amount {
		return money.New(rapid.Int64Range(-100_000_000_000_000, 100_000_000_000_000).Draw(t, "minor"), c)
	})
}

// Toplama değişmeli ve birleşmeli; çıkarma toplamanın tersi. Para kaybolmaz, yoktan var olmaz.
func TestPropertyAddSubAlgebra(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := genCurrency().Draw(t, "currency")
		a, b, d := genAmount(c).Draw(t, "a"), genAmount(c).Draw(t, "b"), genAmount(c).Draw(t, "d")

		ab, _ := a.Add(b)
		ba, _ := b.Add(a)
		if ab != ba {
			t.Fatalf("değişme özelliği bozuk: %v + %v", a, b)
		}
		abd, _ := ab.Add(d)
		bd, _ := b.Add(d)
		abd2, _ := a.Add(bd)
		if abd != abd2 {
			t.Fatalf("birleşme özelliği bozuk")
		}
		back, _ := ab.Sub(b)
		if back != a {
			t.Fatalf("(a+b)-b = %v, beklenen %v", back, a)
		}
	})
}

// Herhangi bir int64 tutarda Add ya da doğru sonucu verir ya da ErrOverflow döner; asla sessizce sarmaz.
func TestPropertyAddNeverWrapsSilently(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		x, y := rapid.Int64().Draw(t, "x"), rapid.Int64().Draw(t, "y")
		sum, err := money.New(x, money.TRY).Add(money.New(y, money.TRY))

		exact := decimal.NewFromInt(x).Add(decimal.NewFromInt(y))
		fits := !exact.GreaterThan(decimal.NewFromInt(math.MaxInt64)) && !exact.LessThan(decimal.NewFromInt(math.MinInt64))
		switch {
		case fits && (err != nil || sum.Minor != exact.IntPart()):
			t.Fatalf("%d + %d: beklenen %s, gelen %v / %v", x, y, exact, sum, err)
		case !fits && err == nil:
			t.Fatalf("%d + %d taşmalıydı, gelen %v", x, y, sum)
		}
	})
}

// Tutar → metin → tutar ve tutar → decimal → tutar dönüşümleri kayıpsızdır.
func TestPropertyRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := genCurrency().Draw(t, "currency")
		a := money.New(rapid.Int64Range(math.MinInt64+1, math.MaxInt64).Draw(t, "minor"), c)

		parsed, err := money.ParseAmount(a.Format(), c)
		if err != nil || parsed != a {
			t.Fatalf("Format/Parse: %v → %q → %v, %v", a, a.Format(), parsed, err)
		}
		fromDec, err := money.FromDecimal(a.Decimal(), c)
		if err != nil || fromDec != a {
			t.Fatalf("Decimal/FromDecimal: %v → %v, %v", a, fromDec, err)
		}
	})
}

// Banker's rounding: sonuç en fazla 2 basamaklıdır, gerçek değerden en fazla yarım kuruş uzaktır ve
// tam yarımda son basamak çifttir.
func TestPropertyRoundHalfEven(t *testing.T) {
	half := decimal.RequireFromString("0.005")
	rapid.Check(t, func(t *rapid.T) {
		// 6 basamaklı rastgele ondalık: ör. 12.345678
		x := decimal.New(rapid.Int64Range(-1_000_000_000_000, 1_000_000_000_000).Draw(t, "x"), -6)
		r := money.RoundHalfEven(x, 2)

		if !r.Equal(r.Truncate(2)) {
			t.Fatalf("%s → %s: 2 basamaktan fazla", x, r)
		}
		diff := r.Sub(x).Abs()
		if diff.GreaterThan(half) {
			t.Fatalf("%s → %s: fark %s > yarım kuruş", x, r, diff)
		}
		if diff.Equal(half) { // tam yarım: son basamak çift olmalı
			lastDigit := r.Shift(2).Abs().IntPart() % 10
			if lastDigit%2 != 0 {
				t.Fatalf("%s → %s: tam yarımda tek basamağa yuvarlandı", x, r)
			}
		}
	})
}

// Rastgele metin ParseAmount'u asla panic'e sokmaz; kabul edilen her metin kanonik biçime dönüp aynı tutarı verir.
func FuzzParseAmount(f *testing.F) {
	for _, s := range []string{"0", "1234.56", "-0.05", "1e3", "1.234", "92233720368547758.08", "", "-", "١٢٣"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a, err := money.ParseAmount(s, money.TRY)
		if err != nil {
			return
		}
		again, err := money.ParseAmount(a.Format(), money.TRY)
		if err != nil || again != a {
			t.Fatalf("%q → %v → %q → %v (%v)", s, a, a.Format(), again, err)
		}
	})
}
