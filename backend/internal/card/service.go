package card

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"parakopru/internal/account"
	"parakopru/internal/ledger"
	"parakopru/internal/money"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/idempotency"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/observability"
	"parakopru/internal/platform/page"
)

// KindCapture ve KindRefund kart işlemlerinin defter kaydı türleridir.
const (
	KindCapture ledger.Kind = "CARD_CAPTURE"
	KindRefund  ledger.Kind = "CARD_REFUND"
)

// Ledger kart modülünün defterden istedikleridir (arayüzü kullanan paket tanımlar).
type Ledger interface {
	Post(ctx context.Context, tx db.DBTX, e ledger.Entry) (ledger.EntryID, error)
	LockAccounts(ctx context.Context, tx db.DBTX, ids ...uuid.UUID) error
}

// Accounts kart modülünün hesap modülünden istedikleridir.
type Accounts interface {
	Get(ctx context.Context, p auth.Principal, id uuid.UUID) (account.Account, error)
	ByID(ctx context.Context, q db.DBTX, id uuid.UUID) (account.Account, error)
	InternalAccountID(ctx context.Context, q db.DBTX, kind account.Kind, c money.Currency) (uuid.UUID, error)
	OwnCustomerID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
}

// Service kart iş kuralları.
type Service struct {
	tx       *db.TxManager
	ids      ids.Generator
	clock    clock.Clock
	audit    *audit.Recorder
	ledger   Ledger
	accounts Accounts
}

// NewService yeni bir Service oluşturur.
func NewService(tx *db.TxManager, idGen ids.Generator, c clock.Clock, rec *audit.Recorder, l Ledger, a Accounts) *Service {
	return &Service{tx: tx, ids: idGen, clock: c, audit: rec, ledger: l, accounts: a}
}

type authorizeKey struct {
	CardID uuid.UUID      `json:"card_id"`
	Input  AuthorizeInput `json:"input"`
}

type holdKey[T any] struct {
	ID    uuid.UUID `json:"id"`
	Input T         `json:"input"`
}

// Authorize provizyon açar: kullanılabilir bakiye yetiyorsa tutarı bloke eder (koşullu UPDATE). Defter kaydı yazılmaz.
func (s *Service) Authorize(ctx context.Context, p auth.Principal, keyValue string, cardID uuid.UUID, in AuthorizeInput) (Hold, bool, error) {
	ctx = auth.WithPrincipal(ctx, p)
	key, err := idempotency.NewKey(p.UserID, keyValue, "card.authorize", authorizeKey{cardID, in})
	if err != nil {
		return Hold{}, false, err
	}
	return idempotency.Run(ctx, s.tx, key, func(ctx context.Context, tx db.DBTX) (Hold, error) {
		c, err := getCard(ctx, tx, cardID)
		if err != nil {
			return Hold{}, err
		}
		if c.Status != "ACTIVE" {
			return Hold{}, ErrCardBlocked
		}
		acc, err := s.accounts.ByID(ctx, tx, c.AccountID)
		if err != nil {
			return Hold{}, err
		}
		if acc.Currency != in.Currency {
			return Hold{}, ErrCurrencyMismatch // çok para birimli kart harcaması kur adımında (G1)
		}
		ok, err := blockFunds(ctx, tx, acc.ID, in.AmountMinor)
		if err != nil {
			return Hold{}, err
		}
		if !ok {
			return Hold{}, ErrInsufficient
		}
		ttl := DefaultTTL
		if in.TTLSeconds > 0 {
			ttl = time.Duration(in.TTLSeconds) * time.Second
		}
		h := Hold{ID: s.ids.NewID(), AccountID: acc.ID, CardID: c.ID, Merchant: in.Merchant, Currency: in.Currency,
			AmountMinor: in.AmountMinor, RemainingMinor: in.AmountMinor, State: Active, ExpiresAt: s.clock.Now().Add(ttl)}
		if err := insertHold(ctx, tx, h); err != nil {
			return Hold{}, err
		}
		h, err = getHold(ctx, tx, h.ID, false)
		if err != nil {
			return Hold{}, err
		}
		return h, s.audit.Record(ctx, tx, audit.Entry{Action: "card.authorize", Entity: "hold", EntityID: h.ID.String(), New: h})
	})
}

// Capture provizyondan çekim yapar (kısmi olabilir). final=true ise kalan blok serbest kalır. Bakiye düşer, blok azalır
// ve defter kaydı yazılır; hepsi tek transaction'da ve tek kilitli güncellemede (ledger.Line.ReleaseHold).
func (s *Service) Capture(ctx context.Context, p auth.Principal, keyValue string, holdID uuid.UUID, in CaptureInput) (CaptureResult, bool, error) {
	ctx = auth.WithPrincipal(ctx, p)
	key, err := idempotency.NewKey(p.UserID, keyValue, "card.capture", holdKey[CaptureInput]{holdID, in})
	if err != nil {
		return CaptureResult{}, false, err
	}
	return idempotency.Run(ctx, s.tx, key, func(ctx context.Context, tx db.DBTX) (CaptureResult, error) {
		h, err := getHold(ctx, tx, holdID, false) // hesabı öğrenmek için; kilitsiz
		if err != nil {
			return CaptureResult{}, err
		}
		settlement, err := s.accounts.InternalAccountID(ctx, tx, account.CardSettlement, h.Currency)
		if err != nil {
			return CaptureResult{}, err
		}
		// Kilit sırası: önce hesap özetleri (sıralı), sonra provizyon satırı.
		if err := s.ledger.LockAccounts(ctx, tx, h.AccountID, settlement); err != nil {
			return CaptureResult{}, err
		}
		if h, err = getHold(ctx, tx, holdID, true); err != nil {
			return CaptureResult{}, err
		}
		switch {
		case h.State != Active:
			return CaptureResult{}, ErrHoldNotActive
		case in.AmountMinor > h.RemainingMinor:
			return CaptureResult{}, ErrExceedsHold
		}
		release := in.AmountMinor
		h.CapturedMinor += in.AmountMinor
		h.RemainingMinor -= in.AmountMinor
		if in.Final || h.RemainingMinor == 0 {
			release += h.RemainingMinor
			h.ReleasedMinor += h.RemainingMinor
			h.RemainingMinor, h.State = 0, Captured
		}
		c := Capture{ID: s.ids.NewID(), HoldID: h.ID, AmountMinor: in.AmountMinor, Final: in.Final}
		amt := money.New(in.AmountMinor, h.Currency)
		line := ledger.Debit(h.AccountID, amt)
		line.ReleaseHold = release
		c.JournalEntryID, err = s.ledger.Post(ctx, tx, ledger.Entry{Kind: KindCapture, Reference: c.ID.String(),
			Description: "Kart çekimi: " + h.Merchant, Lines: []ledger.Line{line, ledger.Credit(settlement, amt)}})
		if err != nil {
			return CaptureResult{}, err
		}
		if err := updateHold(ctx, tx, h); err != nil {
			return CaptureResult{}, err
		}
		if err := insertCapture(ctx, tx, c); err != nil {
			return CaptureResult{}, err
		}
		if c, err = getCapture(ctx, tx, c.ID, false); err != nil {
			return CaptureResult{}, err
		}
		res := CaptureResult{Capture: c, Hold: h}
		return res, s.audit.Record(ctx, tx, audit.Entry{Action: "card.capture", Entity: "hold", EntityID: h.ID.String(), New: res})
	})
}

// Refund çekimin bir kısmını ya da tamamını müşteriye iade eder (toplam iade çekilenden fazla olamaz).
func (s *Service) Refund(ctx context.Context, p auth.Principal, keyValue string, captureID uuid.UUID, in RefundInput) (RefundResult, bool, error) {
	ctx = auth.WithPrincipal(ctx, p)
	key, err := idempotency.NewKey(p.UserID, keyValue, "card.refund", holdKey[RefundInput]{captureID, in})
	if err != nil {
		return RefundResult{}, false, err
	}
	return idempotency.Run(ctx, s.tx, key, func(ctx context.Context, tx db.DBTX) (RefundResult, error) {
		c, err := getCapture(ctx, tx, captureID, false)
		if err != nil {
			return RefundResult{}, err
		}
		h, err := getHold(ctx, tx, c.HoldID, false)
		if err != nil {
			return RefundResult{}, err
		}
		settlement, err := s.accounts.InternalAccountID(ctx, tx, account.CardSettlement, h.Currency)
		if err != nil {
			return RefundResult{}, err
		}
		if err := s.ledger.LockAccounts(ctx, tx, h.AccountID, settlement); err != nil {
			return RefundResult{}, err
		}
		if c, err = getCapture(ctx, tx, captureID, true); err != nil {
			return RefundResult{}, err
		}
		if in.AmountMinor > c.AmountMinor-c.RefundedMinor {
			return RefundResult{}, ErrExceedsCapture
		}
		r := Refund{ID: s.ids.NewID(), CaptureID: c.ID, AmountMinor: in.AmountMinor}
		amt := money.New(in.AmountMinor, h.Currency)
		r.JournalEntryID, err = s.ledger.Post(ctx, tx, ledger.Entry{Kind: KindRefund, Reference: r.ID.String(),
			Description: "Kart iadesi: " + h.Merchant, Lines: []ledger.Line{ledger.Debit(settlement, amt), ledger.Credit(h.AccountID, amt)}})
		if err != nil {
			return RefundResult{}, err
		}
		if err := insertRefund(ctx, tx, r); err != nil {
			return RefundResult{}, err
		}
		if c, err = getCapture(ctx, tx, captureID, false); err != nil {
			return RefundResult{}, err
		}
		res := RefundResult{Refund: r, Capture: c}
		return res, s.audit.Record(ctx, tx, audit.Entry{Action: "card.refund", Entity: "capture", EntityID: c.ID.String(), New: res})
	})
}

// ExpireDue süresi dolan aktif provizyonları serbest bırakır (worker periyodik çağırır). Her provizyon ayrı transaction'dadır:
// önce hesap özeti kilitlenir (çekim akışıyla aynı sıra), sonra provizyon SKIP LOCKED ile alınır; bir çekimle
// çakışırsa atlanır ve sonraki turda yeniden bakılır. Serbest bırakılan sayısını döndürür.
func (s *Service) ExpireDue(ctx context.Context) (int, error) {
	ctx = audit.WithSystemActor(ctx)
	now := s.clock.Now()
	due, err := dueHolds(ctx, s.tx.DB(), now, 200)
	if err != nil {
		return 0, err
	}
	released := 0
	for _, d := range due {
		err := s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
			if err := s.ledger.LockAccounts(ctx, tx, d.AccountID); err != nil {
				return err
			}
			h, ok, err := lockExpired(ctx, tx, d.ID, now)
			if err != nil || !ok {
				return err
			}
			if err := releaseFunds(ctx, tx, h.AccountID, h.RemainingMinor); err != nil {
				return err
			}
			h.ReleasedMinor += h.RemainingMinor
			h.RemainingMinor, h.State = 0, Expired
			if err := updateHold(ctx, tx, h); err != nil {
				return err
			}
			released++
			return s.audit.Record(ctx, tx, audit.Entry{Action: "card.hold_expire", Entity: "hold", EntityID: h.ID.String(), New: h})
		})
		if err != nil {
			return released, fmt.Errorf("card: expire hold %s: %w", d.ID, err)
		}
	}
	if released > 0 {
		observability.Logger(ctx).Info("holds expired", slog.Int("count", released))
	}
	return released, nil
}

// Cards kartları listeler; müşteri yalnız kendi kartlarını, ledger:read sahibi roller hepsini görür.
func (s *Service) Cards(ctx context.Context, p auth.Principal, req page.Request) (page.Page[Card], error) {
	var customerID *uuid.UUID
	if !p.Role.Has(auth.PermLedgerRead) {
		own, err := s.accounts.OwnCustomerID(ctx, p.UserID)
		if err != nil {
			return page.Page[Card]{}, err
		}
		customerID = &own
	}
	return listCards(ctx, s.tx.DB(), customerID, req)
}

// Holds hesabın provizyonlarını listeler (hesap yetkisi hesap modülünde: müşteri başkasının hesabını göremez).
func (s *Service) Holds(ctx context.Context, p auth.Principal, accountID uuid.UUID, state *State, req page.Request) (page.Page[Hold], error) {
	if _, err := s.accounts.Get(ctx, p, accountID); err != nil {
		return page.Page[Hold]{}, err
	}
	return listHolds(ctx, s.tx.DB(), accountID, state, req)
}

// MaskedPAN kart numarasının yalnız ilk ve son 4 hanesini içeren, saklanabilir biçimidir. Tam kart numarası hiçbir yerde tutulmaz.
func MaskedPAN(last4 int) string { return fmt.Sprintf("5400 **** **** %04d", last4%10000) }

// Issue hesaba bağlı yeni bir kart verir (seed ve ileride kart başvurusu için). Verilen transaction içinde çalışır.
func (s *Service) Issue(ctx context.Context, tx db.DBTX, accountID, customerID uuid.UUID) (Card, error) {
	id := s.ids.NewID()
	c := Card{ID: id, AccountID: accountID, CustomerID: customerID, PANMasked: MaskedPAN(int(id[15])<<8 | int(id[14])), Status: "ACTIVE"}
	if err := insertCard(ctx, tx, c); err != nil {
		return Card{}, err
	}
	return getCard(ctx, tx, id)
}
