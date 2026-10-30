package main

import (
	"context"
	"log/slog"
	"time"

	"parakopru/internal/account"
	"parakopru/internal/card"
	"parakopru/internal/customer"
	"parakopru/internal/fx"
	"parakopru/internal/ledger"
	"parakopru/internal/money"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
)

// demoCustomerNationalID demo müşterinin kurgusal kimlik numarasıdır (simülatör bu numarayı üretmez).
const demoCustomerNationalID = "10000000146"

// seedBase temel seed'dir (PLAN.md §6): api her açılışta çalıştırır; eksik olanı ekler, var olana dokunmaz.
//   - her rol için bir demo kullanıcı
//   - her para birimi için bankanın iç hesapları
//   - demo müşterinin müşteri kaydı, hesapları (vadesiz TRY/USD/EUR, vadeli TRY) ve açılış bakiyeleri
//
// Açılış bakiyeleri de defterden geçer (borç OPENING_EQUITY / alacak müşteri); seed sonrası da her para biriminde
// borç = alacak ve özet = defterden türetilen bakiye. 20.000 müşterilik toplu veri simülatörün işidir.
func seedBase(ctx context.Context, log *slog.Logger, txm *db.TxManager, clk clock.Clock, demoPassword string,
	authSvc *auth.Service, customers *customer.Service, accounts *account.Service, ledgerSvc *ledger.Service, cards *card.Service, fxSvc *fx.Service,
) error {
	if _, err := authSvc.SeedDemoUsers(ctx, demoPassword); err != nil {
		return err
	}
	// Başlangıç kurları (yoksa): 1 USD / 1 EUR kaç TL. Geçerlilikleri yıl başıdır; sonraki kurlar operasyon uzmanınca eklenir.
	if err := fxSvc.EnsureDefaults(ctx, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		map[money.Currency]string{money.USD: "41.5000", money.EUR: "48.2000"}); err != nil {
		return err
	}
	if _, err := accounts.EnsureInternalAccounts(audit.WithSystemActor(ctx)); err != nil {
		return err
	}

	u, err := authSvc.UserByEmail(ctx, "musteri@parakopru.test")
	if err != nil {
		return err
	}
	ctx = audit.WithSystemActor(ctx)
	return txm.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		c, created, err := customers.EnsureForUser(ctx, tx, u.ID, customer.CreateInput{
			FullName: "Demo Müşteri", NationalID: demoCustomerNationalID, Email: u.Email,
		})
		if err != nil || !created {
			return err
		}
		// Vadesiz TRY bakiyesi 10.000 TL'nin üstünde: dört göz onaylı transfer de demo hesapla denenebilir.
		for _, d := range []struct {
			in      account.OpenInput
			opening int64 // kuruş/sent
		}{
			{account.OpenInput{CustomerID: c.ID, Kind: account.Checking, Currency: money.TRY}, 50_000_00},
			{account.OpenInput{CustomerID: c.ID, Kind: account.Checking, Currency: money.USD}, 2_000_00},
			{account.OpenInput{CustomerID: c.ID, Kind: account.Checking, Currency: money.EUR}, 2_000_00},
			{account.OpenInput{CustomerID: c.ID, Kind: account.TimeDeposit, Currency: money.TRY,
				MaturityDate: clk.Now().AddDate(1, 0, 0).Format(time.DateOnly)}, 100_000_00},
		} {
			a, err := accounts.OpenTx(ctx, tx, d.in)
			if err != nil {
				return err
			}
			if err := postOpeningBalance(ctx, tx, accounts, ledgerSvc, a, money.New(d.opening, a.Currency)); err != nil {
				return err
			}
			if a.Kind == account.Checking && a.Currency == money.TRY { // demo müşterinin kartı vadesiz TRY hesabına bağlı
				if _, err := cards.Issue(ctx, tx, a.ID, c.ID); err != nil {
					return err
				}
			}
		}
		log.Info("demo customer seeded", slog.String("customer_id", c.ID.String()))
		return nil
	})
}

// postOpeningBalance hesabın açılış bakiyesini deftere yazar: borç OPENING_EQUITY / alacak müşteri hesabı.
func postOpeningBalance(ctx context.Context, tx db.DBTX, accounts *account.Service, ledgerSvc *ledger.Service,
	a account.Account, amount money.Amount,
) error {
	equity, err := accounts.InternalAccountID(ctx, tx, account.OpeningEquity, a.Currency)
	if err != nil {
		return err
	}
	_, err = ledgerSvc.Post(ctx, tx, ledger.Entry{
		Kind:        ledger.KindOpeningBalance,
		Description: "Açılış bakiyesi",
		Lines:       []ledger.Line{ledger.Debit(equity, amount), ledger.Credit(a.ID, amount)},
	})
	return err
}
