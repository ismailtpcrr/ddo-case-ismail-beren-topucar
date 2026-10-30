// Package transfer havale/EFT/FAST transferlerini yönetir (proje.md Görev 2, F-3; PLAN.md §5.2).
//
// Bu adımda banka içi (INTERNAL) transfer vardır: tek transaction'da hem defter kaydı hem transfer kaydı hem
// denetim kaydı yazılır. Dış banka akışı (RESERVED → SENT → …) sonraki adımlarda aynı durum makinesine eklenir.
package transfer

import (
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
)

// Type transferin türüdür.
type Type string

const (
	Internal Type = "INTERNAL" // banka içi havale
	EFT      Type = "EFT"      // dış banka
	FAST     Type = "FAST"     // dış banka, anlık
)

// State transferin durumudur.
type State string

const (
	Created         State = "CREATED"
	UnderReview     State = "UNDER_REVIEW"
	PendingApproval State = "PENDING_APPROVAL"
	Reserved        State = "RESERVED"
	Sent            State = "SENT"
	Unknown         State = "UNKNOWN"
	Completed       State = "COMPLETED"
	Failed          State = "FAILED"
	Reversed        State = "REVERSED"
	Rejected        State = "REJECTED"
)

// transitions geçerli durum geçişlerinin tek kaynağıdır (PLAN.md §5.2). Tabloda olmayan geçiş reddedilir.
var transitions = map[State][]State{
	Created:         {UnderReview, PendingApproval, Reserved, Completed, Rejected},
	UnderReview:     {PendingApproval, Reserved, Completed, Rejected},
	PendingApproval: {Reserved, Completed, Rejected},
	Reserved:        {Sent},
	Sent:            {Completed, Failed, Unknown},
	Unknown:         {Completed, Failed},
	Failed:          {Reversed},
}

// CanTransition from → to geçişinin geçerli olup olmadığını söyler.
func CanTransition(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Transfer bir transfer kaydıdır.
type Transfer struct {
	ID            uuid.UUID      `json:"id"`
	Type          Type           `json:"type"`
	State         State          `json:"state"`
	FromAccountID uuid.UUID      `json:"from_account_id"`
	ToAccountID   *uuid.UUID     `json:"to_account_id"`
	ToIBAN        string         `json:"to_iban"`
	AmountMinor   int64          `json:"amount_minor"`
	Currency      money.Currency `json:"currency"`
	Description   string         `json:"description"`
	// Çok para birimli (banka içi) transferde alıcının aldığı tutar ve kayda yazılan kur (1 gönderen para birimi = FXRate alıcı para birimi).
	ToAmountMinor     *int64           `json:"to_amount_minor"`
	ToCurrency        *money.Currency  `json:"to_currency"`
	FXRate            *decimal.Decimal `json:"fx_rate"`
	RequestedBy       uuid.UUID        `json:"requested_by"`
	JournalEntryID    *uuid.UUID       `json:"journal_entry_id"`
	SettlementEntryID *uuid.UUID       `json:"settlement_entry_id"`
	ReversalEntryID   *uuid.UUID       `json:"reversal_entry_id"`
	FailureCode       *string          `json:"failure_code"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

// Event bir durum geçişidir.
type Event struct {
	From *State    `json:"from_state"`
	To   State     `json:"to_state"`
	Note *string   `json:"note"`
	At   time.Time `json:"at"`
}

// Detail transfer ve geçmişidir.
type Detail struct {
	Transfer
	Events []Event `json:"events"`
}

// CreateInput transfer isteğidir.
type CreateInput struct {
	Type          Type           `json:"type"            validate:"omitempty,oneof=EFT FAST"` // yalnız dış transferde; varsayılan FAST
	FromAccountID uuid.UUID      `json:"from_account_id" validate:"required"`
	ToIBAN        string         `json:"to_iban"         validate:"required,len=26"`
	AmountMinor   int64          `json:"amount_minor"    validate:"required,gt=0"`
	Currency      money.Currency `json:"currency"        validate:"required,oneof=TRY USD EUR"`
	Description   string         `json:"description"     validate:"max=200"`
}

var (
	ErrNotFound         = apperr.ErrNotFound.WithMessage("Transfer bulunamadı.")
	ErrInvalidIBAN      = apperr.New(http.StatusBadRequest, "INVALID_IBAN", "Alıcı IBAN'ı geçersiz.")
	ErrBeneficiary      = apperr.New(http.StatusUnprocessableEntity, "BENEFICIARY_NOT_FOUND", "Alıcı hesap bulunamadı.")
	ErrFourEyes         = apperr.New(http.StatusForbidden, "FOUR_EYES_VIOLATION", "Transferi isteyen kişi onaylayamaz; farklı bir yetkili onaylamalı.")
	ErrNotPending       = apperr.New(http.StatusConflict, "INVALID_STATE_TRANSITION", "Transfer onay beklemiyor.")
	ErrBankMismatch     = apperr.New(http.StatusUnprocessableEntity, "BANK_RESULT_MISMATCH", "Banka bildirimi transfer kaydıyla uyuşmuyor.")
	ErrNotExternal      = apperr.New(http.StatusUnprocessableEntity, "NOT_EXTERNAL_TRANSFER", "Bu işlem yalnız dış banka transferleri içindir.")
	ErrCurrencyMismatch = apperr.New(http.StatusUnprocessableEntity, "CURRENCY_MISMATCH", "Transfer para birimi, gönderen hesabın para birimiyle aynı olmalı.")
	ErrSameAccount      = apperr.New(http.StatusUnprocessableEntity, "SAME_ACCOUNT", "Gönderen ve alıcı hesap aynı olamaz.")
	ErrAccountInactive  = apperr.New(http.StatusUnprocessableEntity, "ACCOUNT_NOT_ACTIVE", "Hesap kapalı.")
	ErrNotInternalAcct  = apperr.New(http.StatusUnprocessableEntity, "ACCOUNT_NOT_ALLOWED", "Bu hesap transfer için kullanılamaz.")
	// ErrInvalidTransition geçersiz durum geçişidir; bir hata değil bir tutarsızlık işaretidir (loglanır).
	ErrInvalidTransition = apperr.New(http.StatusConflict, "INVALID_STATE_TRANSITION", "Transfer bu durumdan istenen duruma geçemez.")
)

func invalidTransition(from, to State) error {
	return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, from, to)
}

// ApprovalThresholdMinor dört göz onayı eşiğidir: 10.000,00 TL. Tutar eşiğin **üzerindeyse** (> ) onay gerekir; tam 10.000,00 TL gerekmez.
const ApprovalThresholdMinor int64 = 10_000_00

// RejectInput onay bekleyen transferi reddetme isteğidir.
type RejectInput struct {
	Reason string `json:"reason" validate:"required,min=3,max=400"`
}

// Approval bir dört göz onay kaydıdır.
type Approval struct {
	ID          uuid.UUID  `json:"id"`
	TransferID  uuid.UUID  `json:"transfer_id"`
	RequestedBy uuid.UUID  `json:"requested_by"`
	Status      string     `json:"status"`
	DecidedBy   *uuid.UUID `json:"decided_by"`
	Reason      *string    `json:"reason"`
	CreatedAt   time.Time  `json:"created_at"`
	DecidedAt   *time.Time `json:"decided_at"`
}
