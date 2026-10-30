// Package interest faiz tahakkuku ve stopajdır (proje.md Görev 4, F-6; PLAN.md §5.6).
//
// Bu dosyadaki hesaplar saf fonksiyonlardır (veritabanı ve saat bilmez); bu yüzden tablo tabanlı ve property testleriyle
// kanıtlanır. Kurallar:
//   - Günlük brüt (hassas) = esas × yıllık oran / gün tabanı. Basitte esas = anapara; bileşikte esas = anapara + birikmiş faiz.
//   - Gün tabanı: ACT_360 → 360, ACT_365 → 365, ACT_ACT → o günün yılının gün sayısı (artık yılda 366).
//   - Bir gün her zaman o gün geçerli oranla hesaplanır: oran değişince geçmiş günler etkilenmez.
//   - Kuruş dağıtımı: hassas kümülatif faiz tutulur; her gün yazılan = yuvarla(kümülatif) − şimdiye kadar yazılan.
//     Böylece günlük tutarların toplamı her zaman yuvarlanmış toplam faize tam eşittir (kuruş kaybolmaz, birikmez).
//   - Stopaj: vergi = yuvarla(brüt × oran), net = brüt − vergi → brüt = net + vergi her zaman.
//   - Yuvarlama her yerde banker's rounding (yarımda çifte).
package interest

import (
	"time"

	"github.com/shopspring/decimal"

	"parakopru/internal/money"
)

// Method faiz yöntemidir.
type Method string

const (
	Simple   Method = "SIMPLE"
	Compound Method = "COMPOUND"
)

// DayBasis gün sayım tabanıdır.
type DayBasis string

const (
	Act360 DayBasis = "ACT_360"
	Act365 DayBasis = "ACT_365"
	ActAct DayBasis = "ACT_ACT"
)

// Rate bir günde geçerli faiz koşuludur.
type Rate struct {
	Method     Method
	Basis      DayBasis
	AnnualRate decimal.Decimal // 0.42 = %42
}

// DaysInYear gün tabanıdır; ACT_ACT'te günün yılı artık yılsa 366.
func DaysInYear(b DayBasis, day time.Time) int64 {
	switch b {
	case Act360:
		return 360
	case ActAct:
		y := day.Year()
		if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
			return 366
		}
	}
	return 365
}

// precision hassas (yuvarlanmamış) faizin tutulduğu ondalık basamak sayısıdır (ana birimde).
const precision = 16

// State bir hesabın tahakkuk durumudur: hassas kümülatif brüt faiz ve şimdiye kadar yazılan (yuvarlanmış) brüt faiz.
type State struct {
	CumulativeExact decimal.Decimal // ana birimde, yuvarlanmamış
	PostedMinor     int64           // yazılan brüt faizin toplamı (kuruş)
}

// Day bir günün tahakkuk sonucudur.
type Day struct {
	ExactGross decimal.Decimal // o günün hassas brüt faizi (ana birimde)
	GrossMinor int64           // o gün yazılan brüt (kuruş dağıtımıyla)
	TaxMinor   int64
	NetMinor   int64
}

// Accrue bir günün faizini hesaplar. principalMinor anaparadır (birikmiş faiz hariç); bileşikte esas'a birikmiş hassas faiz eklenir.
// Dönen State bir sonraki günün girdisidir.
func Accrue(s State, principalMinor int64, c money.Currency, r Rate, taxRate decimal.Decimal, day time.Time) (State, Day, error) {
	base := money.New(principalMinor, c).Decimal()
	if r.Method == Compound {
		base = base.Add(s.CumulativeExact)
	}
	exact := base.Mul(r.AnnualRate).DivRound(decimal.NewFromInt(DaysInYear(r.Basis, day)), precision)
	if exact.IsNegative() {
		exact = decimal.Zero // eksi bakiye faiz kazanmaz
	}
	next := State{CumulativeExact: s.CumulativeExact.Add(exact)}
	rounded, err := money.FromDecimal(next.CumulativeExact, c)
	if err != nil {
		return State{}, Day{}, err
	}
	next.PostedMinor = rounded.Minor
	gross := rounded.Minor - s.PostedMinor
	tax, net, err := Withhold(gross, c, taxRate)
	if err != nil {
		return State{}, Day{}, err
	}
	return next, Day{ExactGross: exact, GrossMinor: gross, TaxMinor: tax, NetMinor: net}, nil
}

// Withhold stopajı ayırır: vergi = yuvarla(brüt × oran), net = brüt − vergi.
func Withhold(grossMinor int64, c money.Currency, rate decimal.Decimal) (tax, net int64, err error) {
	t, err := money.FromDecimal(money.New(grossMinor, c).Decimal().Mul(rate), c)
	if err != nil {
		return 0, 0, err
	}
	return t.Minor, grossMinor - t.Minor, nil
}
