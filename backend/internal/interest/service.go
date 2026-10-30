package interest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"parakopru/internal/account"
	"parakopru/internal/ledger"
	"parakopru/internal/money"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/jobs"
	"parakopru/internal/platform/observability"
)

// Ledger gün sonunun defterden istediğidir.
type Ledger interface {
	Post(ctx context.Context, tx db.DBTX, e ledger.Entry) (ledger.EntryID, error)
}

// Accounts iç hesap kimlikleri içindir.
type Accounts interface {
	InternalAccountID(ctx context.Context, q db.DBTX, kind account.Kind, c money.Currency) (uuid.UUID, error)
}

// HoldExpirer süresi dolan kart provizyonlarını serbest bırakır (gün sonu adımı).
type HoldExpirer interface {
	ExpireDue(ctx context.Context) (int, error)
}

// Config gün sonu ayarlarıdır.
type Config struct {
	BatchSize         int // her parti tek transaction (PLAN.md: 1.000)
	CrashAfterBatches int // > 0 ise bu kadar parti sonra çalıştırma "çöker" (çökme testi; EOD_CRASH_AFTER_BATCHES)
}

// Service gün sonu işidir.
type Service struct {
	tx       *db.TxManager
	ids      ids.Generator
	clock    clock.Clock
	audit    *audit.Recorder
	ledger   Ledger
	accounts Accounts
	holds    HoldExpirer
	queue    *jobs.Queue
	cfg      Config
}

// NewService yeni bir Service oluşturur.
func NewService(tx *db.TxManager, idGen ids.Generator, c clock.Clock, rec *audit.Recorder, l Ledger, a Accounts, h HoldExpirer, q *jobs.Queue, cfg Config) *Service {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1000
	}
	return &Service{tx: tx, ids: idGen, clock: c, audit: rec, ledger: l, accounts: a, holds: h, queue: q, cfg: cfg}
}

func parseDay(s string) (time.Time, error) {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("eod: bad date %q: %w", s, err)
	}
	return d, nil
}

// Start gün sonunu başlatır: çalıştırma satırını açar ve işi kuyruğa ekler (worker yapar). Aynı gün daha önce başlatılmışsa
// mevcut çalıştırmayı döndürür (created = false): tekrar istek ikinci kez tahakkuk yapmaz. Bugünden ileri tarih ya da
// daha sonraki bir günü tamamlanmış geçmiş tarih reddedilir.
func (s *Service) Start(ctx context.Context, p auth.Principal, in RunInput) (Run, bool, error) {
	day, err := parseDay(in.BusinessDate)
	if err != nil {
		return Run{}, false, err
	}
	if day.After(s.clock.Now().UTC()) {
		return Run{}, false, ErrFutureDate
	}
	ctx = auth.WithPrincipal(ctx, p)
	created := false
	err = s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		created = false
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7271)`); err != nil { // başlatmalar sıraya girer
			return err
		}
		if _, _, err := getRun(ctx, tx, day, false); err == nil {
			return nil
		}
		var later bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM eod_runs WHERE business_date > $1)`, day).Scan(&later); err != nil {
			return err
		}
		if later {
			return ErrOutOfOrder
		}
		if _, err := tx.Exec(ctx, `INSERT INTO eod_runs (business_date, requested_by) VALUES ($1, $2)`, day, p.UserID); err != nil {
			return fmt.Errorf("eod: insert run: %w", err)
		}
		if _, err := s.queue.Enqueue(ctx, tx, JobEOD, RunInput{BusinessDate: in.BusinessDate}); err != nil {
			return err
		}
		created = true
		return s.audit.Record(ctx, tx, audit.Entry{Action: "eod.start", Entity: "eod_run", EntityID: in.BusinessDate})
	})
	if err != nil {
		return Run{}, false, err
	}
	r, err := s.Get(ctx, in.BusinessDate)
	return r, created, err
}

// HandleJob worker'ın eod.run işidir.
func (s *Service) HandleJob(ctx context.Context, job jobs.Job) error {
	var in RunInput
	if err := json.Unmarshal(job.Payload, &in); err != nil {
		return jobs.Permanent(err)
	}
	return s.Run(audit.WithSystemActor(ctx), in.BusinessDate)
}

// Run gün sonunu kaldığı yerden çalıştırır. Faiz adımında hesaplar kimlik sırasıyla partiler halinde işlenir; her parti
// (tahakkuk satırları + defter kayıtları + ilerleme) tek transaction'dır. Çökme olursa tamamlanmış partiler kalır,
// yarım parti geri alınır; yeniden çalıştırma last_account_id'den devam eder. UNIQUE (account_id, accrual_date) ve
// "bu gün zaten tahakkuk etti mi" kontrolü çift tahakkuku imkânsız kılar.
func (s *Service) Run(ctx context.Context, date string) error {
	day, err := parseDay(date)
	if err != nil {
		return jobs.Permanent(err)
	}
	log := observability.Logger(ctx).With(slog.String("business_date", date))
	if _, err := s.tx.DB().Exec(ctx, `UPDATE eod_runs SET attempts = attempts + 1 WHERE business_date = $1`, day); err != nil {
		return err
	}
	tax, err := taxRateOn(ctx, s.tx.DB(), day)
	if err != nil {
		return err
	}
	batches := 0
	for {
		more := false
		err := s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
			run, last, err := getRun(ctx, tx, day, true) // tek çalıştırıcı: satır kilidi
			if err != nil || run.Step != StepInterest {
				return err
			}
			accs, err := nextBatch(ctx, tx, day, last, s.cfg.BatchSize)
			if err != nil {
				return err
			}
			if len(accs) == 0 {
				_, err := tx.Exec(ctx, `UPDATE eod_runs SET step = $2 WHERE business_date = $1`, day, StepHoldExpiry)
				return err
			}
			// Kayıt zamanı veritabanı saatidir; geçerlilik zamanı ondan ileri olamaz (K-13).
			var dbNow time.Time
			if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&dbNow); err != nil {
				return err
			}
			accrued := 0
			for _, a := range accs {
				ok, err := s.accrue(ctx, tx, a, day, tax, dbNow)
				if err != nil {
					return fmt.Errorf("eod: account %s: %w", a.ID, err)
				}
				if ok {
					accrued++
				}
			}
			more = true
			_, err = tx.Exec(ctx, `UPDATE eod_runs SET last_account_id = $2, batches = batches + 1, accounts_accrued = accounts_accrued + $3 WHERE business_date = $1`,
				day, accs[len(accs)-1].ID, accrued)
			return err
		})
		if err != nil {
			return err
		}
		if !more {
			break
		}
		batches++
		if s.cfg.CrashAfterBatches > 0 && batches >= s.cfg.CrashAfterBatches {
			log.Warn("simulated eod crash", slog.Int("batches", batches))
			return ErrSimulatedEnd
		}
	}

	run, _, err := getRun(ctx, s.tx.DB(), day, false)
	if err != nil {
		return err
	}
	if run.Step == StepHoldExpiry {
		if s.holds != nil {
			if _, err := s.holds.ExpireDue(ctx); err != nil {
				return err
			}
		}
		err = s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
			if _, err := tx.Exec(ctx, `UPDATE eod_runs SET step = $2, status = 'DONE', finished_at = now() WHERE business_date = $1 AND step = $3`,
				day, StepDone, StepHoldExpiry); err != nil {
				return err
			}
			return s.audit.Record(ctx, tx, audit.Entry{Action: "eod.finish", Entity: "eod_run", EntityID: date})
		})
		if err != nil {
			return err
		}
		log.Info("eod finished")
	}
	return nil
}

// accrue bir hesabın bir günlük faizini yazar; bu gün zaten tahakkuk etmişse false döner (dokunmaz).
func (s *Service) accrue(ctx context.Context, tx db.DBTX, a accountRow, day time.Time, taxRate decimal.Decimal, dbNow time.Time) (bool, error) {
	st, netSoFar, done, err := accountState(ctx, tx, a.ID, day)
	if err != nil || done {
		return false, err
	}
	c := money.Currency(a.Currency)
	principal := a.BalanceMinor - netSoFar // alacaklanmış faiz anaparaya dahil edilmez (bileşikte esas'a ayrıca eklenir)
	if principal < 0 {
		principal = 0
	}
	next, d, err := Accrue(st, principal, c, a.Rate, taxRate, day)
	if err != nil {
		return false, err
	}
	id := s.ids.NewID()
	var entryID *uuid.UUID
	if d.GrossMinor > 0 {
		expense, err := s.accounts.InternalAccountID(ctx, tx, account.InterestExpense, c)
		if err != nil {
			return false, err
		}
		wht, err := s.accounts.InternalAccountID(ctx, tx, account.WithholdingTax, c)
		if err != nil {
			return false, err
		}
		lines := []ledger.Line{ledger.Debit(expense, money.New(d.GrossMinor, c))}
		if d.NetMinor > 0 {
			lines = append(lines, ledger.Credit(a.ID, money.New(d.NetMinor, c)))
		}
		if d.TaxMinor > 0 {
			lines = append(lines, ledger.Credit(wht, money.New(d.TaxMinor, c)))
		}
		eff := day.Add(24*time.Hour - time.Microsecond) // günün sonu; bugünse şimdi (ileri tarihli kayıt yok)
		var effPtr *time.Time
		if eff.Before(dbNow) {
			effPtr = &eff
		}
		e, err := s.ledger.Post(ctx, tx, ledger.Entry{Kind: KindAccrual, Reference: id.String(), EffectiveAt: effPtr,
			Description: "Faiz tahakkuku " + day.Format(time.DateOnly), Lines: lines})
		if err != nil {
			return false, err
		}
		entryID = &e
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO interest_accruals (id, account_id, accrual_date, principal_minor, method, day_basis, annual_rate, tax_rate,
		                               gross_minor, tax_minor, net_minor, cumulative_exact, cumulative_posted, journal_entry_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9, $10, $11, $12::numeric, $13, $14)`,
		id, a.ID, day, principal, a.Rate.Method, a.Rate.Basis, a.Rate.AnnualRate.String(), taxRate.String(),
		d.GrossMinor, d.TaxMinor, d.NetMinor, next.CumulativeExact.String(), next.PostedMinor, entryID)
	if err != nil {
		return false, fmt.Errorf("eod: insert accrual: %w", err)
	}
	return true, nil
}

// Get çalıştırmayı ve o günün para birimi bazında tahakkuk toplamlarını döndürür.
func (s *Service) Get(ctx context.Context, date string) (Run, error) {
	day, err := parseDay(date)
	if err != nil {
		return Run{}, ErrRunNotFound
	}
	r, _, err := getRun(ctx, s.tx.DB(), day, false)
	if err != nil {
		return Run{}, err
	}
	r.Totals, err = totals(ctx, s.tx.DB(), day)
	return r, err
}
