package interest

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"parakopru/internal/platform/apperr"
)

// KindAccrual faiz tahakkukunun defter kaydı türüdür: borç INTEREST_EXPENSE (brüt) / alacak müşteri (net) / alacak WITHHOLDING_TAX (vergi).
const KindAccrual = "INTEREST_ACCRUAL"

// JobEOD gün sonu işinin kuyruk türüdür.
const JobEOD = "eod.run"

// Gün sonu adımları (PLAN.md §5.6): faiz tahakkuku → süresi dolan provizyonlar. Mutabakat ve müşteri ekstresi adımları
// mutabakat modülüyle eklenir.
const (
	StepInterest   = "INTEREST"
	StepHoldExpiry = "HOLD_EXPIRY"
	StepDone       = "DONE"
)

// Run bir gün sonu çalıştırmasıdır (özetiyle).
type Run struct {
	BusinessDate    string     `json:"business_date"`
	Step            string     `json:"step"`
	Status          string     `json:"status"`
	Batches         int        `json:"batches"`
	AccountsAccrued int        `json:"accounts_accrued"`
	Attempts        int        `json:"attempts"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	Totals          []Total    `json:"totals"`
}

// Total bir para birimindeki tahakkuk toplamıdır (o gün için): brüt = net + vergi.
type Total struct {
	Currency   string `json:"currency"`
	Accruals   int    `json:"accruals"`
	GrossMinor int64  `json:"gross_minor"`
	TaxMinor   int64  `json:"tax_minor"`
	NetMinor   int64  `json:"net_minor"`
}

// RunInput gün sonu isteğidir.
type RunInput struct {
	BusinessDate string `json:"business_date" validate:"required,datetime=2006-01-02"`
}

type accountRow struct {
	ID           uuid.UUID
	Currency     string
	BalanceMinor int64
	Rate         Rate
}

var (
	ErrRunNotFound  = apperr.ErrNotFound.WithMessage("Bu tarih için gün sonu çalıştırması yok.")
	ErrFutureDate   = apperr.New(http.StatusUnprocessableEntity, "FUTURE_BUSINESS_DATE", "Gün sonu bugünden ileri bir tarih için çalıştırılamaz.")
	ErrOutOfOrder   = apperr.New(http.StatusConflict, "EOD_OUT_OF_ORDER", "Daha sonraki bir tarihin gün sonu zaten yapılmış; geçmiş bir gün yeniden açılamaz.")
	ErrSimulatedEnd = errors.New("eod: simulated crash (EOD_CRASH_AFTER_BATCHES)")
)
