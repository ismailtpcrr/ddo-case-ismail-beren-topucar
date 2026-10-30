// Package money para tutarlarını temsil eder.
//
// Kurallar (proje.md §6-§7, PLAN.md §3.4):
//   - Saklanan ve taşınan her tutar int64 "minor" birimdir (kuruş, sent). Kayan nokta (float32/64) kullanılmaz;
//     bu paket ve para ile ilgili diğer paketlerde lint kuralıyla yasaktır. 0.1 + 0.2 ≠ 0.3 sorunu yaşanmaz.
//   - Farklı para birimlerindeki iki tutar toplanamaz/çıkarılamaz; hata döner.
//   - Faiz ve kur gibi ara hesaplar decimal (keyfi hassasiyetli ondalık) ile yapılır; sonuç kuruşa
//     banker's rounding (yarımda çifte yuvarlama) ile çevrilir. Yarımları hep yukarı yuvarlamak, çok sayıda
//     işlemde sistematik olarak bankanın aleyhine (ya da lehine) birikir; çifte yuvarlama bu sapmayı dengeler.
//   - Taşma (int64 sınırı) sessizce yanlış sonuç üretmez; hata döner.
package money

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// Currency ISO 4217 para birimi kodudur.
type Currency string

const (
	TRY Currency = "TRY"
	USD Currency = "USD"
	EUR Currency = "EUR"
)

// Currencies desteklenen para birimleridir (proje.md §5).
var Currencies = []Currency{TRY, USD, EUR}

// minorDigits bir para biriminin ondalık basamak sayısıdır. Desteklenen üçü de 2 basamaklıdır (kuruş, sent);
// başka basamaklı bir para birimi eklenirse yalnız burası değişir.
const minorDigits int32 = 2

var (
	ErrUnknownCurrency  = errors.New("money: unknown currency")
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
	ErrOverflow         = errors.New("money: amount out of range")
	ErrInvalidAmount    = errors.New("money: invalid amount")
)

// Valid para biriminin desteklenip desteklenmediğini söyler.
func (c Currency) Valid() bool { return slices.Contains(Currencies, c) }

// ParseCurrency metni para birimine çevirir.
func ParseCurrency(s string) (Currency, error) {
	c := Currency(s)
	if !c.Valid() {
		return "", fmt.Errorf("%w: %q", ErrUnknownCurrency, s)
	}
	return c, nil
}

// Amount belirli bir para birimindeki tutardır (minor birimde).
type Amount struct {
	Minor    int64    `json:"amount_minor"`
	Currency Currency `json:"currency"`
}

// New yeni bir tutar oluşturur.
func New(minor int64, c Currency) Amount { return Amount{Minor: minor, Currency: c} }

// Zero verilen para biriminde sıfır tutardır.
func Zero(c Currency) Amount { return Amount{Currency: c} }

// IsZero tutarın sıfır olup olmadığını söyler.
func (a Amount) IsZero() bool { return a.Minor == 0 }

// IsPositive tutarın sıfırdan büyük olup olmadığını söyler.
func (a Amount) IsPositive() bool { return a.Minor > 0 }

// IsNegative tutarın sıfırdan küçük olup olmadığını söyler.
func (a Amount) IsNegative() bool { return a.Minor < 0 }

// Add iki tutarı toplar. Para birimleri farklıysa ya da sonuç int64'e sığmıyorsa hata döner.
func (a Amount) Add(b Amount) (Amount, error) {
	if a.Currency != b.Currency {
		return Amount{}, fmt.Errorf("%w: %s + %s", ErrCurrencyMismatch, a.Currency, b.Currency)
	}
	if (b.Minor > 0 && a.Minor > math.MaxInt64-b.Minor) || (b.Minor < 0 && a.Minor < math.MinInt64-b.Minor) {
		return Amount{}, ErrOverflow
	}
	return Amount{Minor: a.Minor + b.Minor, Currency: a.Currency}, nil
}

// Sub a − b'yi hesaplar. Para birimleri farklıysa ya da sonuç int64'e sığmıyorsa hata döner.
func (a Amount) Sub(b Amount) (Amount, error) {
	nb, err := b.Neg()
	if err != nil {
		return Amount{}, err
	}
	return a.Add(nb)
}

// Neg tutarın ters işaretlisini döndürür (ör. ters kayıt).
func (a Amount) Neg() (Amount, error) {
	if a.Minor == math.MinInt64 {
		return Amount{}, ErrOverflow
	}
	return Amount{Minor: -a.Minor, Currency: a.Currency}, nil
}

// Cmp iki tutarı karşılaştırır: a < b ise -1, eşitse 0, büyükse 1. Para birimleri farklıysa hata döner.
func (a Amount) Cmp(b Amount) (int, error) {
	if a.Currency != b.Currency {
		return 0, fmt.Errorf("%w: %s ? %s", ErrCurrencyMismatch, a.Currency, b.Currency)
	}
	switch {
	case a.Minor < b.Minor:
		return -1, nil
	case a.Minor > b.Minor:
		return 1, nil
	}
	return 0, nil
}

// Decimal tutarı ana birimde (ör. TL) ondalık sayı olarak döndürür; ara hesaplar içindir.
func (a Amount) Decimal() decimal.Decimal { return decimal.New(a.Minor, -minorDigits) }

// FromDecimal ana birimdeki ondalık değeri banker's rounding ile minor birime yuvarlayarak tutara çevirir.
//
//	12.345 TL → 12.34 TL (yarım: çift olan 4'e)    12.355 TL → 12.36 TL (yarım: çift olan 6'ya)
func FromDecimal(d decimal.Decimal, c Currency) (Amount, error) {
	minor := RoundHalfEven(d, minorDigits).Shift(minorDigits)
	if minor.GreaterThan(decimal.NewFromInt(math.MaxInt64)) || minor.LessThan(decimal.NewFromInt(math.MinInt64)) {
		return Amount{}, ErrOverflow
	}
	return Amount{Minor: minor.IntPart(), Currency: c}, nil
}

// RoundHalfEven d'yi verilen basamağa banker's rounding ile yuvarlar: tam yarımda son basamağı çift olana,
// diğer durumlarda en yakına.
func RoundHalfEven(d decimal.Decimal, places int32) decimal.Decimal { return d.RoundBank(places) }

// String tutarı "1234.56 TRY" biçiminde yazar (loglar ve hata mesajları için).
func (a Amount) String() string { return a.Format() + " " + string(a.Currency) }

// Format tutarı ana birimde, binlik ayırıcısız ve nokta ondalık ayırıcıyla yazar: -1234.05
func (a Amount) Format() string {
	// İşaret bölmeden sonra çevrilir: MinInt64'te bile bölüm ve kalan int64'e sığar.
	units, cents := a.Minor/100, a.Minor%100
	sign := ""
	if a.Minor < 0 {
		sign, units, cents = "-", -units, -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, units, cents)
}

// ParseAmount "1234.56", "-0.5", "100" biçimindeki metni tutara çevirir (ör. banka ekstresi satırları).
// En fazla 2 ondalık basamak kabul edilir; üs (1e3), binlik ayırıcı, boşluk ve kayan nokta yorumu yoktur.
// Fazla basamak sessizce yuvarlanmaz, hata döner: ekstredeki bozuk satır görünür kalmalı.
func ParseAmount(s string, c Currency) (Amount, error) {
	if !c.Valid() {
		return Amount{}, fmt.Errorf("%w: %q", ErrUnknownCurrency, c)
	}
	invalid := fmt.Errorf("%w: %q", ErrInvalidAmount, s)

	neg := strings.HasPrefix(s, "-")
	body := strings.TrimPrefix(s, "-")
	whole, frac, hasDot := strings.Cut(body, ".")
	if whole == "" || !digitsOnly(whole) || (hasDot && (frac == "" || len(frac) > int(minorDigits) || !digitsOnly(frac))) {
		return Amount{}, invalid
	}
	for len(frac) < int(minorDigits) {
		frac += "0"
	}

	units, err := strconv.ParseUint(whole, 10, 64)
	if err != nil {
		return Amount{}, fmt.Errorf("%w: %q", ErrOverflow, s)
	}
	cents, _ := strconv.ParseUint(frac, 10, 64) // en fazla 2 rakam, hata olamaz
	if units > (math.MaxInt64-cents)/100 {
		return Amount{}, fmt.Errorf("%w: %q", ErrOverflow, s)
	}
	minor := int64(units*100 + cents) //nolint:gosec // üst sınır bir satır yukarıda denetlendi
	if neg {
		minor = -minor
	}
	return Amount{Minor: minor, Currency: c}, nil
}

func digitsOnly(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
