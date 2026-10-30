// Package card kart provizyonunu, çekimi ve iadeyi yönetir (proje.md Görev 3, F-4; PLAN.md §5.4).
//
//   - Provizyon (hold) bakiyeyi bloke eder: account_balances.held_minor artar, defter kaydı yazılmaz (bloke ≠ harcama).
//   - Çekim (capture) kısmi olabilir; final çekimde kalan blok serbest kalır. Defter: borç müşteri / alacak CARD_SETTLEMENT.
//   - İade, çekilenden fazla olamaz. Defter: borç CARD_SETTLEMENT / alacak müşteri.
//   - Süresi dolan provizyonları worker serbest bırakır (EXPIRED).
//
// Kullanılabilir bakiye = bakiye − held_minor her zaman O(1) okunur. Kilit sırası her yerde: önce hesap özetleri
// (hesap kimliğine göre sıralı), sonra provizyon/çekim satırı; böylece eşzamanlı işlemler birbirini kilitlemez.
package card

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
)

// Hold durumları.
type State string

const (
	Active   State = "ACTIVE"
	Captured State = "CAPTURED" // tamamen çekildi ya da final çekimle kalan serbest bırakıldı
	Expired  State = "EXPIRED"  // süre doldu, kalan blok serbest bırakıldı
)

// Card bir banka kartıdır (numarası maskeli saklanır).
type Card struct {
	ID         uuid.UUID `json:"id"`
	AccountID  uuid.UUID `json:"account_id"`
	CustomerID uuid.UUID `json:"customer_id"`
	PANMasked  string    `json:"pan_masked"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

// Hold bir kart provizyonudur.
type Hold struct {
	ID             uuid.UUID      `json:"id"`
	AccountID      uuid.UUID      `json:"account_id"`
	CardID         uuid.UUID      `json:"card_id"`
	Merchant       string         `json:"merchant"`
	Currency       money.Currency `json:"currency"`
	AmountMinor    int64          `json:"amount_minor"`
	CapturedMinor  int64          `json:"captured_minor"`
	ReleasedMinor  int64          `json:"released_minor"`
	RemainingMinor int64          `json:"remaining_minor"`
	State          State          `json:"state"`
	ExpiresAt      time.Time      `json:"expires_at"`
	CreatedAt      time.Time      `json:"created_at"`
}

// Capture bir çekimdir.
type Capture struct {
	ID             uuid.UUID `json:"id"`
	HoldID         uuid.UUID `json:"hold_id"`
	AmountMinor    int64     `json:"amount_minor"`
	RefundedMinor  int64     `json:"refunded_minor"`
	Final          bool      `json:"final"`
	JournalEntryID uuid.UUID `json:"journal_entry_id"`
	CreatedAt      time.Time `json:"created_at"`
}

// Refund bir iadedir.
type Refund struct {
	ID             uuid.UUID `json:"id"`
	CaptureID      uuid.UUID `json:"capture_id"`
	AmountMinor    int64     `json:"amount_minor"`
	JournalEntryID uuid.UUID `json:"journal_entry_id"`
	CreatedAt      time.Time `json:"created_at"`
}

// AuthorizeInput provizyon isteğidir (kart ağı: sistem rolü).
type AuthorizeInput struct {
	AmountMinor int64          `json:"amount_minor" validate:"required,gt=0"`
	Currency    money.Currency `json:"currency"     validate:"required,oneof=TRY USD EUR"`
	Merchant    string         `json:"merchant"     validate:"max=100"`
	TTLSeconds  int            `json:"ttl_seconds"  validate:"omitempty,gte=1,lte=2592000"` // varsayılan 7 gün, en fazla 30 gün
}

// CaptureInput çekim isteğidir. Final çekimde provizyonun kalanı serbest bırakılır.
type CaptureInput struct {
	AmountMinor int64 `json:"amount_minor" validate:"required,gt=0"`
	Final       bool  `json:"final"`
}

// RefundInput iade isteğidir.
type RefundInput struct {
	AmountMinor int64 `json:"amount_minor" validate:"required,gt=0"`
}

// CaptureResult çekim sonucu: çekim ve güncel provizyon.
type CaptureResult struct {
	Capture Capture `json:"capture"`
	Hold    Hold    `json:"hold"`
}

// RefundResult iade sonucu: iade ve güncel çekim.
type RefundResult struct {
	Refund  Refund  `json:"refund"`
	Capture Capture `json:"capture"`
}

// DefaultTTL provizyonun varsayılan geçerlilik süresidir.
const DefaultTTL = 7 * 24 * time.Hour

var (
	ErrCardNotFound     = apperr.ErrNotFound.WithMessage("Kart bulunamadı.")
	ErrHoldNotFound     = apperr.ErrNotFound.WithMessage("Provizyon bulunamadı.")
	ErrCaptureNotFound  = apperr.ErrNotFound.WithMessage("Çekim bulunamadı.")
	ErrCardBlocked      = apperr.New(http.StatusUnprocessableEntity, "CARD_BLOCKED", "Kart kullanıma kapalı.")
	ErrCurrencyMismatch = apperr.New(http.StatusUnprocessableEntity, "CURRENCY_MISMATCH", "İşlem para birimi, kartın hesabının para birimiyle aynı olmalı.")
	ErrInsufficient     = apperr.New(http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS", "Hesapta yeterli kullanılabilir bakiye yok.")
	ErrHoldNotActive    = apperr.New(http.StatusConflict, "HOLD_NOT_ACTIVE", "Provizyon artık aktif değil (çekilmiş ya da süresi dolmuş).")
	ErrExceedsHold      = apperr.New(http.StatusUnprocessableEntity, "CAPTURE_EXCEEDS_HOLD", "Çekim tutarı provizyonun kalanından büyük olamaz.")
	ErrExceedsCapture   = apperr.New(http.StatusUnprocessableEntity, "REFUND_EXCEEDS_CAPTURE", "İade tutarı çekilen tutardan (iade edilenler düşülerek) büyük olamaz.")
	ErrNotOwner         = apperr.ErrForbidden.WithMessage("Bu kart size ait değil.")
)
