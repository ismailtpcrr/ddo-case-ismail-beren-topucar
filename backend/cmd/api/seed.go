package main

import (
	"context"
	"log/slog"
	"time"

	"parakopru/internal/account"
	"parakopru/internal/customer"
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
//   - demo müşterinin müşteri kaydı ve hesapları (vadesiz TRY/USD/EUR, vadeli TRY)
//
// 20.000 müşterilik toplu veri simülatörün işidir.
func seedBase(ctx context.Context, log *slog.Logger, txm *db.TxManager, clk clock.Clock, demoPassword string,
	authSvc *auth.Service, customers *customer.Service, accounts *account.Service,
) error {
	if _, err := authSvc.SeedDemoUsers(ctx, demoPassword); err != nil {
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
		for _, in := range []account.OpenInput{
			{CustomerID: c.ID, Kind: account.Checking, Currency: money.TRY},
			{CustomerID: c.ID, Kind: account.Checking, Currency: money.USD},
			{CustomerID: c.ID, Kind: account.Checking, Currency: money.EUR},
			{CustomerID: c.ID, Kind: account.TimeDeposit, Currency: money.TRY,
				MaturityDate: clk.Now().AddDate(1, 0, 0).Format(time.DateOnly)},
		} {
			if _, err := accounts.OpenTx(ctx, tx, in); err != nil {
				return err
			}
		}
		log.Info("demo customer seeded", slog.String("customer_id", c.ID.String()))
		return nil
	})
}
