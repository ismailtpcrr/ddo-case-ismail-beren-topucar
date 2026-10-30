// Package account hesapları ve bakiye özetini yönetir (proje.md F-1, F-2; PLAN.md §5.1).
//
// İki tür hesap vardır:
//   - Müşteri hesapları: vadesiz (CHECKING) ve vadeli (TIME_DEPOSIT); her biri tek para birimli (TRY/USD/EUR).
//   - Bankanın iç hesapları: her para birimi için bir tane NOSTRO, EXTERNAL_CLEARING, CARD_SETTLEMENT,
//     INTEREST_EXPENSE, WITHHOLDING_TAX, FX_POSITION, SUSPENSE, OPENING_EQUITY. Çift taraflı defterde paranın
//     müşteri hesabı dışındaki karşı tarafıdır; para bu hesaplar arasında yer değiştirir, yoktan var olmaz.
package account

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
)

// Kind hesap türüdür.
type Kind string

const (
	Checking    Kind = "CHECKING"     // vadesiz
	TimeDeposit Kind = "TIME_DEPOSIT" // vadeli

	Nostro           Kind = "NOSTRO"            // muhabir bankadaki paramız (dış transferlerin karşılığı)
	ExternalClearing Kind = "EXTERNAL_CLEARING" // dış bankaya gönderilmek üzere ayrılmış, yolda olan para
	CardSettlement   Kind = "CARD_SETTLEMENT"   // kart ağına ödenecek çekimler
	InterestExpense  Kind = "INTEREST_EXPENSE"  // müşterilere ödenen faiz gideri
	WithholdingTax   Kind = "WITHHOLDING_TAX"   // faizden kesilip devlete ödenecek stopaj
	FXPosition       Kind = "FX_POSITION"       // döviz alım-satım pozisyonu
	Suspense         Kind = "SUSPENSE"          // askı: mutabakat farkları gibi geçici kayıtlar
	OpeningEquity    Kind = "OPENING_EQUITY"    // açılış bakiyelerinin karşı tarafı (seed)
)

// IsCustomer müşteri hesabı türü olup olmadığını söyler.
func (k Kind) IsCustomer() bool { return k == Checking || k == TimeDeposit }

// Side bakiyenin doğal tarafıdır: DEBIT hesap borçlanınca, CREDIT hesap alacaklanınca bakiye artar.
type Side string

const (
	Debit  Side = "DEBIT"
	Credit Side = "CREDIT"
)

// internalChart bankanın iç hesap planıdır; her para birimi için bu hesaplar açılır.
// Hepsi eksiye düşebilir: iç hesapların doğruluğu bakiye kısıtıyla değil, defter değişmezleriyle
// (her kayıtta borç = alacak, özet = türetilmiş bakiye) korunur.
var internalChart = []struct {
	Kind Kind
	Side Side
	Name string
}{
	{Nostro, Debit, "Muhabir banka (nostro)"},
	{ExternalClearing, Credit, "Dış transfer takas"},
	{CardSettlement, Credit, "Kart takas"},
	{InterestExpense, Debit, "Faiz gideri"},
	{WithholdingTax, Credit, "Stopaj borcu"},
	{FXPosition, Debit, "Döviz pozisyonu"},
	{Suspense, Debit, "Askı hesabı"},
	{OpeningEquity, Credit, "Açılış özkaynağı"},
}

// Faiz ürünleri: müşteri hesabı açılırken türüne göre atanır; faiz oranları (H1) ürün + para birimine göre tanımlanır.
const (
	ProductChecking    = "CHECKING_STANDARD"
	ProductTimeDeposit = "TIME_DEPOSIT_STANDARD"
)

// Account bir hesap ve bakiye özetidir.
type Account struct {
	ID              uuid.UUID
	CustomerID      *uuid.UUID
	Kind            Kind
	Currency        money.Currency
	NormalSide      Side
	AllowNegative   bool
	Name            string
	IBAN            *string
	InterestProduct *string
	MaturityDate    *time.Time
	Status          string
	OpenedAt        time.Time
	BalanceMinor    int64 // doğal işaretli bakiye
	HeldMinor       int64 // aktif provizyonlar
}

// AvailableMinor kullanılabilir bakiyedir: bakiye − aktif provizyonlar.
func (a Account) AvailableMinor() int64 { return a.BalanceMinor - a.HeldMinor }

// Response hesabın API'deki halidir.
type Response struct {
	ID              uuid.UUID      `json:"id"`
	CustomerID      *uuid.UUID     `json:"customer_id"`
	Kind            Kind           `json:"kind"`
	Currency        money.Currency `json:"currency"`
	Name            string         `json:"name"`
	IBAN            *string        `json:"iban"`
	InterestProduct *string        `json:"interest_product"`
	MaturityDate    *string        `json:"maturity_date"` // YYYY-MM-DD
	Status          string         `json:"status"`
	OpenedAt        time.Time      `json:"opened_at"`
	BalanceMinor    int64          `json:"balance_minor"`
	HeldMinor       int64          `json:"held_minor"`
	AvailableMinor  int64          `json:"available_minor"`
}

// ToResponse hesabı API biçimine çevirir.
func (a Account) ToResponse() Response {
	r := Response{
		ID: a.ID, CustomerID: a.CustomerID, Kind: a.Kind, Currency: a.Currency, Name: a.Name, IBAN: a.IBAN,
		InterestProduct: a.InterestProduct, Status: a.Status, OpenedAt: a.OpenedAt,
		BalanceMinor: a.BalanceMinor, HeldMinor: a.HeldMinor, AvailableMinor: a.AvailableMinor(),
	}
	if a.MaturityDate != nil {
		d := a.MaturityDate.Format(time.DateOnly)
		r.MaturityDate = &d
	}
	return r
}

// OpenInput müşteri hesabı açma isteğidir.
type OpenInput struct {
	CustomerID   uuid.UUID      `json:"customer_id"   validate:"required"`
	Kind         Kind           `json:"kind"          validate:"required,oneof=CHECKING TIME_DEPOSIT"`
	Currency     money.Currency `json:"currency"      validate:"required,oneof=TRY USD EUR"`
	MaturityDate string         `json:"maturity_date" validate:"omitempty,datetime=2006-01-02"` // yalnız vadeli
}

var (
	ErrNotFound           = apperr.ErrNotFound.WithMessage("Hesap bulunamadı.")
	ErrNotOwner           = apperr.ErrForbidden.WithMessage("Bu hesap size ait değil.")
	ErrMaturityRequired   = apperr.New(http.StatusBadRequest, "VALIDATION_ERROR", "Vadeli hesap için ileri bir vade tarihi (maturity_date) gerekli.")
	ErrMaturityNotAllowed = apperr.New(http.StatusBadRequest, "VALIDATION_ERROR", "Vadesiz hesabın vade tarihi olmaz.")
)
