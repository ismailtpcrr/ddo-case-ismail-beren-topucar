package transfer

// Dış banka transferinin sagası (proje.md Görev 2; PLAN.md §5.2):
//
//	RESERVED ─► SENT ─┬─► COMPLETED                      (banka onayladı: takas kapanır)
//	                  ├─► FAILED ─► REVERSED             (banka reddetti: ters kayıt, para müşteriye döner)
//	                  └─► UNKNOWN ─(sorgula)─► COMPLETED | FAILED ─► REVERSED
//
// Banka çağrısı zaman aşımı, hata ya da belirsiz sonuç verirse transfer UNKNOWN olur ve para tahmine göre hareket
// etmez: banka sorgulanır, kesin cevap gelince ya tamamlanır ya telafi (ters kayıt) yapılır. Bildirim (webhook) ile
// worker aynı transferi aynı anda sonuçlandırmaya kalkabilir; transfer satırı kilitlenir, ikinci gelen terminal durumu
// görüp sessizce çıkar (çift cevap).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"parakopru/internal/account"
	"parakopru/internal/bank"
	"parakopru/internal/ledger"
	"parakopru/internal/money"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/jobs"
	"parakopru/internal/platform/observability"
)

// JobSend dış transferin gönderim ve sorgulama işidir.
const JobSend = "transfer.send"

// Kayıt türleri.
const (
	KindSettle   ledger.Kind = "TRANSFER_SETTLE"   // dış transfer tamamlandı: borç takas / alacak nostro
	KindIncoming ledger.Kind = "INCOMING_TRANSFER" // gelen dış transfer: borç nostro / alacak müşteri
)

// SendPayload gönderim işinin yüküdür.
type SendPayload struct {
	TransferID uuid.UUID `json:"transfer_id"`
}

// queryRetry sorgulama başarısız olursa işin yeniden deneneceği bekleme (iş kuyruğu bunu denemeyle artan beklemeye çevirmez;
// bankanın sorgusu hata üretmediği için kısa tutulur).
const queryRetry = 2 * time.Second

// HandleSend worker'ın transfer.send işidir; tekrar çalıştırılmaya dayanıklıdır (iş kuyruğu en az bir kez çalıştırır).
func (s *Service) HandleSend(ctx context.Context, job jobs.Job) error {
	var p SendPayload
	if err := decode(job.Payload, &p); err != nil {
		return jobs.Permanent(err)
	}
	return s.ProcessSend(audit.WithSystemActor(ctx), p.TransferID)
}

// ProcessSend transferi bankaya gönderir ya da (belirsizse) bankaya sorar.
func (s *Service) ProcessSend(ctx context.Context, id uuid.UUID) error {
	t, err := get(ctx, s.tx.DB(), id)
	if err != nil {
		return jobs.Permanent(err)
	}
	log := observability.Logger(ctx).With(slog.String("transfer_id", id.String()))
	switch t.State {
	case Reserved, Sent:
		if t.State == Reserved {
			if err := s.mark(ctx, id, Reserved, Sent, "bankaya gönderiliyor"); err != nil {
				return err
			}
		}
		st, err := s.bank.Send(ctx, bank.SendRequest{
			Reference: id.String(), AmountMinor: t.AmountMinor, Currency: string(t.Currency), BeneficiaryIBAN: t.ToIBAN})
		if err != nil {
			// Belirsiz sonuç: banka işlemiş olabilir. Tahmin yok; UNKNOWN olur ve sorgulanır.
			log.Warn("bank send uncertain", slog.Any("error", err))
			if mErr := s.mark(ctx, id, Sent, Unknown, "banka cevabı belirsiz"); mErr != nil {
				return mErr
			}
			return jobs.RetryAfter(queryRetry, err)
		}
		return s.apply(ctx, id, st, "banka cevabı")
	case Unknown:
		st, err := s.bank.Inquire(ctx, id.String())
		if err != nil {
			log.Warn("bank inquiry failed", slog.Any("error", err))
			return jobs.RetryAfter(queryRetry, err)
		}
		return s.apply(ctx, id, st, "banka sorgusu")
	}
	return nil // zaten sonuçlanmış (çift çalıştırma ya da bildirim önce geldi)
}

// apply bankanın kesin cevabını işler: COMPLETED → tamamla; REJECTED/NOT_FOUND → telafi.
func (s *Service) apply(ctx context.Context, id uuid.UUID, st bank.Status, source string) error {
	switch st {
	case bank.Completed:
		return s.settle(ctx, id, true, source)
	case bank.Rejected, bank.NotFound:
		return s.settle(ctx, id, false, source+": "+string(st))
	}
	return fmt.Errorf("transfer: unexpected bank status %q", st)
}

// mark transferi from durumundaysa to durumuna geçirir (satır kilitli, geçiş tablosu kontrollü). Zaten ilerlemişse dokunmaz.
func (s *Service) mark(ctx context.Context, id uuid.UUID, from, to State, note string) error {
	return s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		t, err := lock(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.State != from {
			return nil
		}
		return s.move(ctx, tx, &t, to, nil, nil, note)
	})
}

// settle transferi sonuçlandırır. ok ise takas kapanır (COMPLETED); değilse FAILED → ters kayıt → REVERSED.
// Her şey tek transaction'dadır; transfer satırı kilitli olduğu için eşzamanlı ikinci sonuçlandırma bekler ve
// terminal durumu görüp sessizce çıkar.
func (s *Service) settle(ctx context.Context, id uuid.UUID, ok bool, note string) error {
	return s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		t, err := lock(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.Type == Internal {
			return ErrNotExternal
		}
		switch t.State {
		case Completed, Failed, Reversed, Rejected:
			observability.Logger(ctx).Info("duplicate bank result ignored",
				slog.String("transfer_id", id.String()), slog.String("state", string(t.State)))
			return nil
		}
		if t.State == Reserved { // sonuç gönderim kaydından önce geldi (bildirim): önce SENT'e geç
			if err := s.move(ctx, tx, &t, Sent, nil, nil, "bildirim önce geldi"); err != nil {
				return err
			}
		}
		amt := money.New(t.AmountMinor, t.Currency)
		if ok {
			clearing, err := s.accounts.InternalAccountID(ctx, tx, account.ExternalClearing, t.Currency)
			if err != nil {
				return err
			}
			nostro, err := s.accounts.InternalAccountID(ctx, tx, account.Nostro, t.Currency)
			if err != nil {
				return err
			}
			entryID, err := s.ledger.Post(ctx, tx, ledger.Entry{Kind: KindSettle, Reference: t.ID.String(),
				Description: "Dış transfer tamamlandı", Lines: []ledger.Line{ledger.Debit(clearing, amt), ledger.Credit(nostro, amt)}})
			if err != nil {
				return err
			}
			if err := setEntry(ctx, tx, t.ID, "settlement_entry_id", entryID); err != nil {
				return err
			}
			if err := s.move(ctx, tx, &t, Completed, nil, nil, note); err != nil {
				return err
			}
			return s.audit.Record(ctx, tx, audit.Entry{Action: "transfer.complete", Entity: "transfer", EntityID: t.ID.String(), New: t})
		}
		code := "BANK_REJECTED"
		if err := s.move(ctx, tx, &t, Failed, nil, &code, note); err != nil {
			return err
		}
		if t.JournalEntryID == nil {
			return fmt.Errorf("transfer %s has no reservation entry", t.ID)
		}
		revID, err := s.ledger.Reverse(ctx, tx, *t.JournalEntryID, ledger.ReverseInput{Reason: "dış transfer başarısız"})
		if err != nil {
			return err
		}
		if err := setEntry(ctx, tx, t.ID, "reversal_entry_id", revID); err != nil {
			return err
		}
		if err := s.move(ctx, tx, &t, Reversed, nil, nil, "ters kayıt yazıldı"); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{Action: "transfer.reverse", Entity: "transfer", EntityID: t.ID.String(), New: t})
	})
}

// BankResult bankanın bildirimidir (POST /webhooks/bank/transfer-status).
type BankResult struct {
	Reference   string `json:"reference"   validate:"required,uuid"`
	Status      string `json:"status"      validate:"required,oneof=COMPLETED REJECTED"`
	AmountMinor int64  `json:"amount_minor" validate:"required,gt=0"`
	Currency    string `json:"currency"    validate:"required,oneof=TRY USD EUR"`
}

// ApplyBankResult banka bildirimini işler. Aynı bildirimin tekrarı (çift cevap) sessizce yok sayılır.
func (s *Service) ApplyBankResult(ctx context.Context, r BankResult) error {
	ctx = audit.WithSystemActor(ctx) // istekte kullanıcı varsa (sistem rolü) o yazılır; yoksa sistem
	id, err := uuid.Parse(r.Reference)
	if err != nil {
		return ErrNotFound
	}
	t, err := get(ctx, s.tx.DB(), id)
	if err != nil {
		return err
	}
	if t.AmountMinor != r.AmountMinor || string(t.Currency) != r.Currency {
		observability.Logger(ctx).Error("bank result does not match transfer", slog.String("transfer_id", r.Reference))
		return ErrBankMismatch
	}
	return s.settle(ctx, id, r.Status == string(bank.Completed), "banka bildirimi: "+r.Status)
}

// IncomingTransfer bankadan gelen bir transferdir (POST /webhooks/bank/incoming).
type IncomingTransfer struct {
	Reference       string `json:"reference"        validate:"required,min=1,max=100"`
	BeneficiaryIBAN string `json:"beneficiary_iban" validate:"required,len=26"`
	AmountMinor     int64  `json:"amount_minor"     validate:"required,gt=0"`
	Currency        string `json:"currency"         validate:"required,oneof=TRY USD EUR"`
}

// ReceiveIncoming gelen dış transferi müşteriye yazar: borç NOSTRO / alacak müşteri hesabı. Banka referansı defterde tekildir;
// aynı bildirim iki kez gelirse ikinci kez alacak yazılmaz (duplicate = true).
func (s *Service) ReceiveIncoming(ctx context.Context, in IncomingTransfer) (duplicate bool, err error) {
	err = s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		duplicate = false
		to, err := s.accounts.ByIBAN(ctx, tx, in.BeneficiaryIBAN)
		if errors.Is(err, account.ErrNotFound) {
			return ErrBeneficiary
		} else if err != nil {
			return err
		}
		if !to.Kind.IsCustomer() || to.Status != "ACTIVE" {
			return ErrAccountInactive
		}
		if string(to.Currency) != in.Currency {
			return ErrCurrencyMismatch
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM journal_entries WHERE kind = $1 AND reference = $2)`,
			string(KindIncoming), in.Reference).Scan(&exists); err != nil {
			return fmt.Errorf("transfer: incoming lookup: %w", err)
		}
		if exists {
			duplicate = true
			return nil
		}
		nostro, err := s.accounts.InternalAccountID(ctx, tx, account.Nostro, to.Currency)
		if err != nil {
			return err
		}
		amt := money.New(in.AmountMinor, to.Currency)
		entryID, err := s.ledger.Post(ctx, tx, ledger.Entry{Kind: KindIncoming, Reference: in.Reference,
			Description: "Gelen dış transfer", Lines: []ledger.Line{ledger.Debit(nostro, amt), ledger.Credit(to.ID, amt)}})
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{Action: "transfer.incoming", Entity: "journal_entry", EntityID: entryID.String(), New: in})
	})
	return duplicate, err
}
