package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"parakopru/internal/account"
	"parakopru/internal/bank"
	"parakopru/internal/ledger"
	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/idempotency"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/jobs"
	"parakopru/internal/platform/observability"
	"parakopru/internal/platform/page"
)

// KindTransfer transferin defter kaydı türüdür (ledger.Kind).
const KindTransfer ledger.Kind = "TRANSFER"

// LedgerPoster transfer modülünün defterden istediği tek şeydir (arayüzü kullanan paket tanımlar).
type LedgerPoster interface {
	Post(ctx context.Context, tx db.DBTX, e ledger.Entry) (ledger.EntryID, error)
	Reverse(ctx context.Context, tx db.DBTX, id ledger.EntryID, in ledger.ReverseInput) (ledger.EntryID, error)
}

// Accounts transfer modülünün hesap modülünden istedikleridir.
type Accounts interface {
	Get(ctx context.Context, p auth.Principal, id uuid.UUID) (account.Account, error)
	GetTx(ctx context.Context, q db.DBTX, p auth.Principal, id uuid.UUID) (account.Account, error)
	ByIBAN(ctx context.Context, q db.DBTX, iban string) (account.Account, error)
	OwnCustomerID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
	InternalAccountID(ctx context.Context, q db.DBTX, kind account.Kind, c money.Currency) (uuid.UUID, error)
}

// BankClient dış banka istemcisidir (gerçek HTTP istemcisi ve testteki sahte aynı arayüzü sağlar).
type BankClient interface {
	Send(ctx context.Context, r bank.SendRequest) (bank.Status, error)
	Inquire(ctx context.Context, reference string) (bank.Status, error)
}

// Service transfer iş kurallarıdır.
type Service struct {
	tx       *db.TxManager
	ids      ids.Generator
	audit    *audit.Recorder
	ledger   LedgerPoster
	accounts Accounts
	queue    *jobs.Queue
	bank     BankClient // api sürecinde nil olabilir: banka çağrılarını yalnız worker yapar
}

// NewService yeni bir Service oluşturur.
func NewService(tx *db.TxManager, idGen ids.Generator, rec *audit.Recorder, l LedgerPoster, a Accounts, q *jobs.Queue, b BankClient) *Service {
	return &Service{tx: tx, ids: idGen, audit: rec, ledger: l, accounts: a, queue: q, bank: b}
}

// Create transferi başlatır. Aynı Idempotency-Key ile gelen tekrar istek işlemi yeniden yapmaz, ilk sonucu döndürür
// (replayed = true). Yetersiz bakiye bir iş kuralı reddidir: REJECTED durumunda bir transfer kaydı yazılır ve
// sonuç olarak saklanır; tekrar istek aynı reddi alır.
func (s *Service) Create(ctx context.Context, p auth.Principal, keyValue string, in CreateInput) (Transfer, bool, error) {
	ctx = auth.WithPrincipal(ctx, p) // denetim kaydı işlemi yapanı buradan alır
	key, err := idempotency.NewKey(p.UserID, keyValue, "transfer.create", in)
	if err != nil {
		return Transfer{}, false, err
	}
	return idempotency.Run(ctx, s.tx, key, func(ctx context.Context, tx db.DBTX) (Transfer, error) {
		return s.create(ctx, tx, p, in)
	})
}

func (s *Service) create(ctx context.Context, tx db.DBTX, p auth.Principal, in CreateInput) (Transfer, error) {
	from, to, err := s.resolve(ctx, tx, p, in)
	if err != nil {
		return Transfer{}, err
	}
	t := Transfer{
		ID: s.ids.NewID(), Type: Internal, State: Created, FromAccountID: from.ID, ToIBAN: in.ToIBAN,
		AmountMinor: in.AmountMinor, Currency: in.Currency, Description: in.Description, RequestedBy: p.UserID,
	}
	if to != nil {
		t.ToAccountID = &to.ID
	} else if t.Type = in.Type; t.Type == "" {
		t.Type = FAST // EFT ve FAST arasında şartnamede kural farkı yok; tür kayıtta ve ekstrede görünür
	}
	if err := insert(ctx, tx, t); err != nil {
		return Transfer{}, err
	}
	if err := s.event(ctx, tx, t.ID, nil, Created, ""); err != nil {
		return Transfer{}, err
	}

	if s.needsApproval(in.AmountMinor, in.Currency) {
		// Dört göz: para bloke edilmez; yetersiz bakiye yürütme anında (onaydan sonra) kontrol edilir.
		if err := insertApproval(ctx, tx, s.ids.NewID(), t.ID, p.UserID); err != nil {
			return Transfer{}, err
		}
		if err := s.move(ctx, tx, &t, PendingApproval, nil, nil, "10.000 TL üzeri: dört göz onayı bekliyor"); err != nil {
			return Transfer{}, err
		}
		err := s.audit.Record(ctx, tx, audit.Entry{Action: "transfer.create", Entity: "transfer", EntityID: t.ID.String(), New: t})
		return t, err
	}
	if err := s.execute(ctx, tx, &t, from, to); err != nil {
		return Transfer{}, err
	}
	err = s.audit.Record(ctx, tx, audit.Entry{Action: "transfer.create", Entity: "transfer", EntityID: t.ID.String(), New: t})
	return t, err
}

// needsApproval tutarın dört göz onayı gerektirip gerektirmediğini söyler: TL karşılığı 10.000,00'ın üzerinde.
// Döviz için güncel kur kur adımında (G1) bağlanır; o zamana kadar muhafazakâr davranılır: 1 birim döviz ≥ 1 TL
// sayılır, yani döviz tutarı eşiği aşarsa onay istenir (gerçek TL karşılığı hiçbir zaman daha az değildir).
func (s *Service) needsApproval(minor int64, _ money.Currency) bool {
	return minor > ApprovalThresholdMinor
}

// execute transferi yürütür: defter kaydı, durum geçişi ve (dış transferde) gönderim işi. Banka içi tamamlanır;
// dış transfer RESERVED olur ve işi kuyruğa girer; yetersiz bakiye REJECTED olur (ret de bir sonuçtur).
func (s *Service) execute(ctx context.Context, tx db.DBTX, t *Transfer, from account.Account, to *account.Account) error {
	amt := money.New(t.AmountMinor, t.Currency)
	var entry ledger.Entry
	if to != nil { // banka içi: tek kayıtla hemen tamamlanır
		entry = ledger.Entry{Kind: KindTransfer, Reference: t.ID.String(), Description: "Havale: " + from.Name + " → " + to.Name,
			Lines: []ledger.Line{ledger.Debit(from.ID, amt), ledger.Credit(to.ID, amt)}}
	} else { // dış banka: para önce takas hesabına ayrılır (RESERVED); banka onaylayınca takas kapanır
		clearing, err := s.accounts.InternalAccountID(ctx, tx, account.ExternalClearing, t.Currency)
		if err != nil {
			return err
		}
		entry = ledger.Entry{Kind: KindTransfer, Reference: t.ID.String(), Description: "Dış transfer ayırma: " + from.Name,
			Lines: []ledger.Line{ledger.Debit(from.ID, amt), ledger.Credit(clearing, amt)}}
	}
	entryID, err := s.ledger.Post(ctx, tx, entry)
	switch {
	case errors.Is(err, ledger.ErrInsufficientFunds):
		// Defter hiçbir şey yazmadan döndü; transaction sağlam. Ret de bir sonuçtur.
		code := "INSUFFICIENT_FUNDS"
		if err := s.move(ctx, tx, t, Rejected, nil, &code, code); err != nil {
			return err
		}
	case err != nil:
		return err
	case to != nil:
		if err := s.move(ctx, tx, t, Completed, &entryID, nil, ""); err != nil {
			return err
		}
	default:
		if err := s.move(ctx, tx, t, Reserved, &entryID, nil, ""); err != nil {
			return err
		}
		// Outbox: gönderim işi, ayırma kaydıyla aynı transaction'da kuyruğa girer (ya ikisi var ya hiçbiri).
		if _, err := s.queue.Enqueue(ctx, tx, JobSend, SendPayload{TransferID: t.ID}); err != nil {
			return err
		}
	}
	return nil
}

// resolve gönderen ve (banka içiyse) alıcı hesabı çözer, isteğin iş kurallarını kontrol eder. Alıcı IBAN'ı bizim
// değilse transfer dış bankaya gider ve to nil döner.
func (s *Service) resolve(ctx context.Context, tx db.DBTX, p auth.Principal, in CreateInput) (from account.Account, to *account.Account, err error) {
	// Müşteri yalnız kendi hesabından gönderebilir (başkasınınki 403).
	if from, err = s.accounts.GetTx(ctx, tx, p, in.FromAccountID); err != nil {
		return
	}
	if !from.Kind.IsCustomer() {
		return from, nil, ErrNotInternalAcct
	}
	if from.Currency != in.Currency {
		return from, nil, ErrCurrencyMismatch
	}
	if !account.ValidIBAN(in.ToIBAN) {
		return from, nil, ErrInvalidIBAN
	}
	dest, err := s.accounts.ByIBAN(ctx, tx, in.ToIBAN)
	if errors.Is(err, account.ErrNotFound) {
		if from.Status != "ACTIVE" {
			return from, nil, ErrAccountInactive
		}
		return from, nil, nil // dış banka
	} else if err != nil {
		return
	}
	switch {
	case dest.ID == from.ID:
		err = ErrSameAccount
	case dest.Currency != from.Currency:
		err = ErrCurrencyMismatch // çok para birimli transfer G1'de
	case from.Status != "ACTIVE" || dest.Status != "ACTIVE":
		err = ErrAccountInactive
	}
	return from, &dest, err
}

// move durum geçişini yapar: geçiş tablosunu kontrol eder, koşullu UPDATE ile yazar ve olay satırı ekler.
func (s *Service) move(ctx context.Context, tx db.DBTX, t *Transfer, to State, entryID *uuid.UUID, failure *string, note string) error {
	if !CanTransition(t.State, to) {
		observability.Logger(ctx).Error("invalid transfer transition",
			slog.String("transfer_id", t.ID.String()), slog.String("from", string(t.State)), slog.String("to", string(to)))
		return invalidTransition(t.State, to)
	}
	if err := transition(ctx, tx, t.ID, t.State, to, entryID, failure); err != nil {
		return err
	}
	from := t.State
	if err := s.event(ctx, tx, t.ID, &from, to, note); err != nil {
		return err
	}
	t.State, t.JournalEntryID, t.FailureCode = to, orPrev(entryID, t.JournalEntryID), orPrev(failure, t.FailureCode)
	return nil
}

func orPrev[T any](v, prev *T) *T {
	if v != nil {
		return v
	}
	return prev
}

func (s *Service) event(ctx context.Context, tx db.DBTX, transferID uuid.UUID, from *State, to State, note string) error {
	return insertEvent(ctx, tx, s.ids.NewID(), transferID, from, to, note)
}

// Get transferi geçmişiyle getirir. Müşteri yalnız gönderen ya da alıcı olduğu transferi görür.
func (s *Service) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (Detail, error) {
	t, err := get(ctx, s.tx.DB(), id)
	if err != nil {
		return Detail{}, err
	}
	if !p.Role.Has(auth.PermLedgerRead) {
		_, err := s.accounts.Get(ctx, p, t.FromAccountID)
		if errors.Is(err, apperr.ErrForbidden) && t.ToAccountID != nil {
			_, err = s.accounts.Get(ctx, p, *t.ToAccountID)
		}
		if err != nil {
			return Detail{}, err
		}
	}
	ev, err := events(ctx, s.tx.DB(), id)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Transfer: t, Events: ev}, nil
}

// List transferleri en yeni önce listeler. Müşteri yalnız kendi hesaplarına ait olanları görür.
func (s *Service) List(ctx context.Context, p auth.Principal, state *State, req page.Request) (page.Page[Transfer], error) {
	var customerID *uuid.UUID
	if !p.Role.Has(auth.PermLedgerRead) {
		own, err := s.accounts.OwnCustomerID(ctx, p.UserID)
		if err != nil {
			return page.Page[Transfer]{}, fmt.Errorf("transfer: list scope: %w", err)
		}
		customerID = &own
	}
	return list(ctx, s.tx.DB(), customerID, state, req)
}

func decode(raw []byte, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("transfer: bad job payload: %w", err)
	}
	return nil
}

// Approve onay bekleyen transferi onaylar ve yürütür (dört göz). Onaylayan isteyen kişiden farklı olmalıdır
// (uygulamada ve veritabanı kısıtıyla); transfer satırı kilitli olduğu için eşzamanlı iki karar tek sonuç verir.
// Yürütme anında bakiye yetmezse transfer REJECTED olur. Sonuç Idempotency-Key ile korunur.
func (s *Service) Approve(ctx context.Context, p auth.Principal, keyValue string, id uuid.UUID) (Transfer, bool, error) {
	ctx = auth.WithPrincipal(ctx, p)
	key, err := idempotency.NewKey(p.UserID, keyValue, "transfer.approve", id)
	if err != nil {
		return Transfer{}, false, err
	}
	return idempotency.Run(ctx, s.tx, key, func(ctx context.Context, tx db.DBTX) (Transfer, error) {
		t, ap, err := s.lockPending(ctx, tx, id)
		if err != nil {
			return Transfer{}, err
		}
		if ap.RequestedBy == p.UserID {
			return Transfer{}, ErrFourEyes
		}
		if err := decideApproval(ctx, tx, id, p.UserID, "APPROVED", ""); err != nil {
			return Transfer{}, err
		}
		from, to, err := s.resolve(ctx, tx, p, CreateInput{FromAccountID: t.FromAccountID, ToIBAN: t.ToIBAN, AmountMinor: t.AmountMinor, Currency: t.Currency})
		if err != nil {
			return Transfer{}, err
		}
		before := t
		if err := s.execute(ctx, tx, &t, from, to); err != nil {
			return Transfer{}, err
		}
		return t, s.audit.Record(ctx, tx, audit.Entry{Action: "transfer.approve", Entity: "transfer", EntityID: t.ID.String(), Old: before, New: t})
	})
}

// Reject onay bekleyen transferi gerekçeyle reddeder; para hiç hareket etmez.
func (s *Service) Reject(ctx context.Context, p auth.Principal, keyValue string, id uuid.UUID, in RejectInput) (Transfer, bool, error) {
	ctx = auth.WithPrincipal(ctx, p)
	key, err := idempotency.NewKey(p.UserID, keyValue, "transfer.reject", struct {
		ID    uuid.UUID   `json:"id"`
		Input RejectInput `json:"input"`
	}{id, in})
	if err != nil {
		return Transfer{}, false, err
	}
	return idempotency.Run(ctx, s.tx, key, func(ctx context.Context, tx db.DBTX) (Transfer, error) {
		t, _, err := s.lockPending(ctx, tx, id)
		if err != nil {
			return Transfer{}, err
		}
		if err := decideApproval(ctx, tx, id, p.UserID, "REJECTED", in.Reason); err != nil {
			return Transfer{}, err
		}
		before := t
		code := "APPROVAL_REJECTED"
		if err := s.move(ctx, tx, &t, Rejected, nil, &code, in.Reason); err != nil {
			return Transfer{}, err
		}
		return t, s.audit.Record(ctx, tx, audit.Entry{Action: "transfer.reject", Entity: "transfer", EntityID: t.ID.String(), Old: before, New: t})
	})
}

// lockPending transferi ve onay kaydını kilitleyip PENDING_APPROVAL olduğunu doğrular (değilse 409).
func (s *Service) lockPending(ctx context.Context, tx db.DBTX, id uuid.UUID) (Transfer, *Approval, error) {
	t, err := lock(ctx, tx, id)
	if err != nil {
		return Transfer{}, nil, err
	}
	ap, err := getApproval(ctx, tx, id)
	if err != nil {
		return Transfer{}, nil, err
	}
	if t.State != PendingApproval || ap == nil || ap.Status != "PENDING" {
		return Transfer{}, nil, ErrNotPending
	}
	return t, ap, nil
}

// Pending onay bekleyen transferleri listeler (operasyon ve uyum onay kuyruğu).
func (s *Service) Pending(ctx context.Context, req page.Request) (page.Page[Transfer], error) {
	st := PendingApproval
	return list(ctx, s.tx.DB(), nil, &st, req)
}
