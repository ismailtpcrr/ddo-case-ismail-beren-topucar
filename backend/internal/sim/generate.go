// Package sim simülatörün tohumlu veri üreticisidir (proje.md §8): aynı tohum aynı müşterileri, hesapları,
// kimlikleri ve açılış bakiyelerini üretir. Üretim saf bir fonksiyondur (veritabanına dokunmaz); seed.go sonucu
// COPY ile toplu olarak yazar.
package sim

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"parakopru/internal/account"
	"parakopru/internal/money"
)

// Params üretimin girdileridir.
type Params struct {
	Seed      int64
	Customers int
}

// Customer üretilen müşteridir.
type Customer struct {
	ID         uuid.UUID
	FullName   string
	NationalID string
	Email      string
}

// Account üretilen müşteri hesabıdır; Opening > 0 ise açılış kaydı (borç OPENING_EQUITY / alacak hesap) yazılır.
type Account struct {
	ID           uuid.UUID
	CustomerID   uuid.UUID
	Kind         account.Kind
	Currency     money.Currency
	Name         string
	Product      string
	MaturityDate *time.Time
	Opening      int64
	EntryID      uuid.UUID // açılış kaydı (Opening > 0 ise)
	DebitID      uuid.UUID // kaydın borç satırı (OPENING_EQUITY)
	CreditID     uuid.UUID // kaydın alacak satırı (hesap)
}

// Dataset üretilen verinin tamamıdır.
type Dataset struct {
	Customers []Customer
	Accounts  []Account
}

var firstNames = []string{"Ahmet", "Mehmet", "Mustafa", "Ali", "Hüseyin", "Hasan", "İbrahim", "Yusuf", "Emre", "Burak", "Can", "Cem", "Deniz", "Kerem",
	"Ayşe", "Fatma", "Emine", "Hatice", "Zeynep", "Elif", "Merve", "Selin", "Esra", "Büşra", "Derya", "Ceren", "Gizem", "Sena", "İrem", "Pınar"}

var lastNames = []string{"Yılmaz", "Kaya", "Demir", "Şahin", "Çelik", "Yıldız", "Yıldırım", "Öztürk", "Aydın", "Özdemir", "Arslan", "Doğan", "Kılıç",
	"Aslan", "Çetin", "Kara", "Koç", "Kurt", "Özkan", "Şimşek", "Polat", "Erdoğan", "Güneş", "Aksoy", "Tekin", "Bulut", "Acar", "Korkmaz", "Taş", "Uçar"}

// idSource zamana göre sıralı, tohumdan deterministik UUIDv7 üretir: zaman alanı sabit bir tarihten başlayıp her
// kimlikte bir milisaniye artar, kalan bitler tohumlu rastgele kaynaktan gelir.
type idSource struct {
	rng *rand.Rand
	ms  uint64
}

func (s *idSource) next() uuid.UUID {
	s.ms++
	var u uuid.UUID
	// Üst 8 bayt: 48 bit zaman, sürüm 7, 12 bit rastgele. Alt 8 bayt: RFC 4122 varyantı, 62 bit rastgele.
	binary.BigEndian.PutUint64(u[0:8], s.ms<<16|0x7000|s.rng.Uint64()&0x0fff)
	binary.BigEndian.PutUint64(u[8:16], s.rng.Uint64()&0x3fff_ffff_ffff_ffff|0x8000_0000_0000_0000)
	return u
}

// seedEpoch kimliklerin zaman alanının başlangıcıdır (sabit; üretim saatinden bağımsız → aynı tohum aynı kimlik).
var seedEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// NationalID i. müşterinin kurgusal kimlik numarasıdır. "5" ile başlar: demo müşterinin numarasıyla
// ("10000000146") ve doğrulamanın ürettiği ("9…") numaralarla çakışmaz. 7919 asaldır, 10¹⁰ ile aralarında asaldır;
// bu yüzden i → numara eşlemesi çarpışmasızdır.
func NationalID(i int) string { return fmt.Sprintf("5%010d", int64(i)*7919%10_000_000_000) }

// Generate tohumdan veri kümesini üretir. Aynı Params her zaman aynı sonucu verir.
func Generate(p Params) Dataset {
	rng := rand.New(rand.NewPCG(uint64(p.Seed), 0x9e3779b97f4a7c15)) //nolint:gosec // güvenlik amaçlı değil, tohumlu test verisi
	ids := &idSource{rng: rng, ms: uint64(seedEpoch.UnixMilli())}
	ds := Dataset{Customers: make([]Customer, 0, p.Customers), Accounts: make([]Account, 0, p.Customers*2)}

	for i := 0; i < p.Customers; i++ {
		c := Customer{
			ID:         ids.next(),
			FullName:   firstNames[rng.IntN(len(firstNames))] + " " + lastNames[rng.IntN(len(lastNames))],
			NationalID: NationalID(i),
			Email:      fmt.Sprintf("musteri%d@sim.parakopru.test", i),
		}
		ds.Customers = append(ds.Customers, c)

		// Herkesin vadesiz TRY hesabı var; bazılarının döviz ve vadeli hesabı da.
		add := func(kind account.Kind, cur money.Currency, maxOpening int64) {
			a := Account{ID: ids.next(), CustomerID: c.ID, Kind: kind, Currency: cur, Product: account.ProductChecking}
			a.Name = "Vadesiz " + string(cur)
			if kind == account.TimeDeposit {
				a.Product, a.Name = account.ProductTimeDeposit, "Vadeli "+string(cur)
				d := time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, rng.IntN(540))
				a.MaturityDate = &d
			}
			// Bakiyelerin %10'u sıfır; geri kalanı 1 kuruş ile maxOpening arasında.
			if rng.IntN(10) != 0 {
				a.Opening = 1 + rng.Int64N(maxOpening)
			}
			if a.Opening > 0 {
				a.EntryID, a.DebitID, a.CreditID = ids.next(), ids.next(), ids.next()
			}
			ds.Accounts = append(ds.Accounts, a)
		}
		add(account.Checking, money.TRY, 200_000_00)
		if rng.IntN(100) < 25 {
			add(account.Checking, money.USD, 10_000_00)
		}
		if rng.IntN(100) < 15 {
			add(account.Checking, money.EUR, 10_000_00)
		}
		if rng.IntN(100) < 20 {
			add(account.TimeDeposit, money.TRY, 500_000_00)
		}
	}
	return ds
}
