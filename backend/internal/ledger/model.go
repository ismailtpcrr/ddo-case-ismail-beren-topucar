// Package ledger çift taraflı defterdir (proje.md Görev 1 ve 7, F-2; PLAN.md §5.1).
//
// Her iş olayı bir yevmiye kaydıdır (journal entry); kaydın satırları (postings) hesaplara borç ya da alacak yazar.
// Satır tutarı veritabanında işaretlidir: + borç, − alacak. Kurallar:
//   - Borç = alacak: her kayıtta, her para birimi için satırların toplamı 0'dır. Go'da kayıt yazılmadan önce
//     (Entry.Validate), veritabanında commit anında (DEFERRABLE constraint trigger) kontrol edilir.
//   - Bakiye defterden türetilebilir (Σ satırlar). Hızlı sorgu için account_balances özeti aynı transaction'da,
//     hesabın doğal tarafına (normal_side) göre güncellenir.
//   - Deadlock olmaması için kayda giren bütün hesapların özet satırları tek sorguda, hesap kimliği sırasıyla kilitlenir.
//   - Kayıtlar değiştirilemez ve silinemez; düzeltme ters kayıtla yapılır (bir kayıt yalnız bir kez).
package ledger

import (
	"errors"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"parakopru/internal/account"
	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
)

// EntryID bir yevmiye kaydının kimliğidir.
type EntryID = uuid.UUID

// Kind kaydın türüdür: hangi iş olayından doğduğunu söyler. Her modül kendi türünü buraya ekler.
type Kind string

const (
	KindOpeningBalance Kind = "OPENING_BALANCE" // açılış bakiyesi: borç OPENING_EQUITY / alacak müşteri
	KindReversal       Kind = "REVERSAL"        // ters kayıt: özgün kaydın satırlarının tersi
)

// ManuallyReversible kaydın API'den elle ters çevrilip çevrilemeyeceğini söyler.
//
// Bir sürece (transfer, kart, faiz) ait kayıtlar yalnız o sürecin kendi akışıyla düzeltilir. Aksi halde ör. transfer
// COMPLETED görünürken para geri dönmüş olur ve süreç kendi telafisini yapmaya kalktığında kayıt zaten ters çevrilmiş
// olur. Ters kaydın kendisi de ters çevrilemez.
func (k Kind) ManuallyReversible() bool { return k == KindOpeningBalance }

// Line yazılacak kaydın bir satırıdır: bir hesaba borç ya da alacak. Tutar her zaman pozitiftir; yönü Side belirler.
type Line struct {
	AccountID uuid.UUID
	Side      account.Side
	Amount    money.Amount
}

// Debit hesaba borç yazan satırdır.
func Debit(accountID uuid.UUID, a money.Amount) Line {
	return Line{AccountID: accountID, Side: account.Debit, Amount: a}
}

// Credit hesaba alacak yazan satırdır.
func Credit(accountID uuid.UUID, a money.Amount) Line {
	return Line{AccountID: accountID, Side: account.Credit, Amount: a}
}

// signed satırın veritabanındaki işaretli tutarıdır: + borç, − alacak.
func (l Line) signed() int64 {
	if l.Side == account.Credit {
		return -l.Amount.Minor
	}
	return l.Amount.Minor
}

// Entry yazılacak bir yevmiye kaydıdır.
type Entry struct {
	Kind        Kind
	Description string
	Reference   string     // isteğe bağlı: iş kaydının kimliği (ör. transfer ID)
	EffectiveAt *time.Time // geçerlilik zamanı; nil ise kaydın yazıldığı an (veritabanı saati)
	Lines       []Line
}

// Kayıt kurallarına aykırı bir kaydı yazmaya çalışmak çağıran modüldeki bir hatadır (istemcinin değil); bu yüzden
// bu hatalar apperr değildir ve istemciye 500 olarak döner, ayrıntısı loga yazılır.
var (
	ErrInvalidEntry = errors.New("ledger: invalid entry")
	ErrUnbalanced   = errors.New("ledger: entry is not balanced")
	ErrAccount      = errors.New("ledger: account missing or currency mismatch")
)

// İstemcinin isteğinden doğabilecek hatalar.
var (
	ErrInsufficientFunds = apperr.New(http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS",
		"Hesapta yeterli kullanılabilir bakiye yok.")
	ErrEntryNotFound   = apperr.ErrNotFound.WithMessage("Yevmiye kaydı bulunamadı.")
	ErrAlreadyReversed = apperr.New(http.StatusConflict, "ALREADY_REVERSED", "Bu kayıt zaten ters çevrilmiş.")
	ErrNotReversible   = apperr.New(http.StatusUnprocessableEntity, "ENTRY_NOT_REVERSIBLE",
		"Bu kayıt elle ters çevrilemez; bir işleme (ör. transfer) ait kayıtlar o işlem üzerinden düzeltilir.")
	ErrReversalOfReversal = ErrNotReversible.WithMessage("Ters kayıt yeniden ters çevrilemez.")
	ErrInvalidEffectiveAt = apperr.ErrValidation.WithMessage(
		"effective_at gelecekte ya da özgün kaydın geçerlilik zamanından önce olamaz.")
)

const maxDescriptionLen = 500

// Validate kaydın kurallara uyup uymadığını veritabanına gitmeden kontrol eder: en az iki satır, pozitif tutarlar,
// desteklenen para birimleri ve her para birimi için borç = alacak.
func (e Entry) Validate() error {
	if e.Kind == "" {
		return fmt.Errorf("%w: kind is empty", ErrInvalidEntry)
	}
	if n := utf8.RuneCountInString(e.Description); n == 0 || n > maxDescriptionLen {
		return fmt.Errorf("%w: description length %d", ErrInvalidEntry, n)
	}
	if len(e.Lines) < 2 {
		return fmt.Errorf("%w: %d line(s), at least 2 required", ErrInvalidEntry, len(e.Lines))
	}
	sums := map[money.Currency]money.Amount{}
	for i, l := range e.Lines {
		switch {
		case l.AccountID == uuid.Nil:
			return fmt.Errorf("%w: line %d has no account", ErrInvalidEntry, i)
		case l.Side != account.Debit && l.Side != account.Credit:
			return fmt.Errorf("%w: line %d has invalid side %q", ErrInvalidEntry, i, l.Side)
		case !l.Amount.Currency.Valid():
			return fmt.Errorf("%w: line %d has unknown currency %q", ErrInvalidEntry, i, l.Amount.Currency)
		case !l.Amount.IsPositive():
			return fmt.Errorf("%w: line %d amount must be positive", ErrInvalidEntry, i)
		}
		sum, ok := sums[l.Amount.Currency]
		if !ok {
			sum = money.Zero(l.Amount.Currency)
		}
		next, err := sum.Add(money.New(l.signed(), l.Amount.Currency))
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidEntry, err)
		}
		sums[l.Amount.Currency] = next
	}
	for c, sum := range sums {
		if !sum.IsZero() {
			return fmt.Errorf("%w: %s debit − credit = %d", ErrUnbalanced, c, sum.Minor)
		}
	}
	return nil
}

// JournalEntry veritabanındaki bir yevmiye kaydı ve satırlarıdır.
type JournalEntry struct {
	ID          EntryID
	Kind        Kind
	Description string
	Reference   *string
	EffectiveAt time.Time
	RecordedAt  time.Time
	ReversalOf  *EntryID // bu kayıt bir ters kayıtsa özgün kayıt
	ReversedBy  *EntryID // bu kayıt ters çevrildiyse ters kayıt
	Postings    []Posting
}

// Posting kaydın bir satırıdır. AmountMinor işaretlidir: + borç, − alacak.
type Posting struct {
	ID          uuid.UUID
	AccountID   uuid.UUID
	Currency    money.Currency
	AmountMinor int64
}

// side işaretli tutardan satırın yönünü çıkarır.
func side(signed int64) account.Side {
	if signed < 0 {
		return account.Credit
	}
	return account.Debit
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// PostingResponse kayıt satırının API'deki halidir: yön (borç/alacak) ve pozitif tutar.
type PostingResponse struct {
	ID          uuid.UUID      `json:"id"`
	AccountID   uuid.UUID      `json:"account_id"`
	Side        account.Side   `json:"side"`
	AmountMinor int64          `json:"amount_minor"`
	Currency    money.Currency `json:"currency"`
}

// EntryResponse yevmiye kaydının API'deki halidir.
type EntryResponse struct {
	ID          EntryID           `json:"id"`
	Kind        Kind              `json:"kind"`
	Description string            `json:"description"`
	Reference   *string           `json:"reference"`
	EffectiveAt time.Time         `json:"effective_at"`
	RecordedAt  time.Time         `json:"recorded_at"`
	ReversalOf  *EntryID          `json:"reversal_of"`
	ReversedBy  *EntryID          `json:"reversed_by"`
	Postings    []PostingResponse `json:"postings"`
}

// ToResponse kaydı API biçimine çevirir.
func (e JournalEntry) ToResponse() EntryResponse {
	r := EntryResponse{
		ID: e.ID, Kind: e.Kind, Description: e.Description, Reference: e.Reference, EffectiveAt: e.EffectiveAt,
		RecordedAt: e.RecordedAt, ReversalOf: e.ReversalOf, ReversedBy: e.ReversedBy,
		Postings: make([]PostingResponse, len(e.Postings)),
	}
	for i, p := range e.Postings {
		r.Postings[i] = PostingResponse{
			ID: p.ID, AccountID: p.AccountID, Side: side(p.AmountMinor), AmountMinor: abs(p.AmountMinor), Currency: p.Currency,
		}
	}
	return r
}

// AccountPosting bir hesabın hareketidir (hesap hareketleri listesi): satır ve ait olduğu kaydın bilgisi.
type AccountPosting struct {
	ID          uuid.UUID
	EntryID     EntryID
	Kind        Kind
	Description string
	Reference   *string
	ReversalOf  *EntryID
	Currency    money.Currency
	AmountMinor int64 // işaretli: + borç, − alacak
	EffectiveAt time.Time
	RecordedAt  time.Time
}

// AccountPostingResponse hesap hareketinin API'deki halidir. balance_effect_minor hareketin hesabın bakiyesine
// etkisidir (doğal işaretli): müşteri hesabında para girişi +, çıkışı −.
type AccountPostingResponse struct {
	ID                 uuid.UUID      `json:"id"`
	EntryID            EntryID        `json:"entry_id"`
	Kind               Kind           `json:"kind"`
	Description        string         `json:"description"`
	Reference          *string        `json:"reference"`
	ReversalOf         *EntryID       `json:"reversal_of"`
	Side               account.Side   `json:"side"`
	AmountMinor        int64          `json:"amount_minor"`
	BalanceEffectMinor int64          `json:"balance_effect_minor"`
	Currency           money.Currency `json:"currency"`
	EffectiveAt        time.Time      `json:"effective_at"`
	RecordedAt         time.Time      `json:"recorded_at"`
}

// naturalDelta işaretli satır tutarının, doğal tarafı verilen hesabın bakiyesine etkisidir.
// DEBIT hesap borçlanınca, CREDIT hesap alacaklanınca bakiye artar.
func naturalDelta(normal account.Side, signed int64) int64 {
	if normal == account.Credit {
		return -signed
	}
	return signed
}

// ToResponse hareketi, hesabın doğal tarafına göre API biçimine çevirir.
func (p AccountPosting) ToResponse(normal account.Side) AccountPostingResponse {
	return AccountPostingResponse{
		ID: p.ID, EntryID: p.EntryID, Kind: p.Kind, Description: p.Description, Reference: p.Reference,
		ReversalOf: p.ReversalOf, Side: side(p.AmountMinor), AmountMinor: abs(p.AmountMinor),
		BalanceEffectMinor: naturalDelta(normal, p.AmountMinor), Currency: p.Currency,
		EffectiveAt: p.EffectiveAt, RecordedAt: p.RecordedAt,
	}
}

// ReverseInput elle ters kayıt isteğidir (POST /ledger/entries/{id}/reverse).
type ReverseInput struct {
	Reason string `json:"reason" validate:"required,min=3,max=400"`
	// Ters kaydın geçerlilik zamanı; verilmezse şimdi. Geriye tarihli düzeltme için özgün kaydın zamanı verilebilir
	// (o andaki bakiye, bugün bilinenle düzeltilmiş görünür; o gün bilinen ise değişmez).
	EffectiveAt *time.Time `json:"effective_at"`
}
