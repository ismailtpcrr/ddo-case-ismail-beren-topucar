package sim

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"parakopru/internal/account"
	"parakopru/internal/card"
	"parakopru/internal/ledger"
	"parakopru/internal/money"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/observability"
)

// seedLockKey, eşzamanlı iki simülatörün aynı anda seed yapmasını engelleyen advisory lock anahtarıdır.
const seedLockKey = 0x70617261_6b6f7072 // "parakopr"

const seedName = "simulator"

// Seed veri kümesini tek bir transaction'da COPY ile yazar. Aynı veritabanında daha önce seed yapılmışsa hiçbir şey
// yapmaz (skipped = true). Açılış bakiyeleri de defterden geçer: her biri için borç OPENING_EQUITY / alacak müşteri
// hesabı kaydı yazılır ve OPENING_EQUITY özeti toplam kadar düşer; seed sonrasında da her para biriminde borç = alacak.
func Seed(ctx context.Context, tm *db.TxManager, rec *audit.Recorder, p Params) (skipped bool, err error) {
	ctx = audit.WithSystemActor(ctx)
	log := observability.Logger(ctx)
	err = tm.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		skipped = false
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(seedLockKey)); err != nil {
			return fmt.Errorf("sim: lock: %w", err)
		}
		var prevSeed int64
		var prevCustomers int
		switch err := tx.QueryRow(ctx, `SELECT seed, customers FROM seed_runs WHERE name = $1`, seedName).Scan(&prevSeed, &prevCustomers); {
		case err == nil:
			if prevSeed != p.Seed || prevCustomers != p.Customers {
				log.Warn("simulator data already seeded with different parameters; skipping",
					slog.Int64("seeded_with", prevSeed), slog.Int64("requested", p.Seed))
			}
			skipped = true
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("sim: seed_runs: %w", err)
		}
		return write(ctx, tx, rec, p, Generate(p))
	})
	return skipped, err
}

func write(ctx context.Context, tx db.DBTX, rec *audit.Recorder, p Params, ds Dataset) error {
	equity, err := equityAccounts(ctx, tx)
	if err != nil {
		return err
	}
	var now time.Time // transaction saati: trigger'ın recorded_at için yazdığı değerle aynı
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return fmt.Errorf("sim: now: %w", err)
	}

	// IBAN'lar hesap numarası dizisinden (demo hesaplarla çakışmaz).
	rows, err := tx.Query(ctx, `SELECT nextval('account_number_seq') FROM generate_series(1, $1) ORDER BY 1`, len(ds.Accounts))
	if err != nil {
		return fmt.Errorf("sim: account numbers: %w", err)
	}
	numbers, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("sim: account numbers: %w", err)
	}

	customers := make([][]any, len(ds.Customers))
	for i, c := range ds.Customers {
		customers[i] = []any{c.ID, c.FullName, c.NationalID, c.Email}
	}
	accounts := make([][]any, len(ds.Accounts))
	balances := make([][]any, len(ds.Accounts))
	var entries, postings [][]any
	totals := map[money.Currency]int64{}
	for i, a := range ds.Accounts {
		accounts[i] = []any{a.ID, a.CustomerID, string(a.Kind), string(a.Currency), string(account.Credit), false, a.Name,
			account.NewIBAN(numbers[i]), a.Product, a.MaturityDate}
		balances[i] = []any{a.ID, a.Opening, false}
		if a.Opening == 0 {
			continue
		}
		totals[a.Currency] += a.Opening
		entries = append(entries, []any{a.EntryID, string(ledger.KindOpeningBalance), "Açılış bakiyesi", now})
		postings = append(postings,
			[]any{a.DebitID, a.EntryID, equity[a.Currency], string(a.Currency), a.Opening, now},
			[]any{a.CreditID, a.EntryID, a.ID, string(a.Currency), -a.Opening, now})
	}

	cards := make([][]any, len(ds.Cards))
	for i, c := range ds.Cards {
		cards[i] = []any{c.ID, c.AccountID, c.CustomerID, card.MaskedPAN(c.Last4)}
	}

	copies := []struct {
		table pgx.Identifier
		cols  []string
		rows  [][]any
	}{
		{pgx.Identifier{"customers"}, []string{"id", "full_name", "national_id", "email"}, customers},
		{pgx.Identifier{"accounts"}, []string{"id", "customer_id", "kind", "currency", "normal_side", "allow_negative", "name", "iban", "interest_product", "maturity_date"}, accounts},
		{pgx.Identifier{"cards"}, []string{"id", "account_id", "customer_id", "pan_masked"}, cards},
		{pgx.Identifier{"account_balances"}, []string{"account_id", "balance_minor", "allow_negative"}, balances},
		{pgx.Identifier{"journal_entries"}, []string{"id", "kind", "description", "effective_at"}, entries},
		{pgx.Identifier{"postings"}, []string{"id", "entry_id", "account_id", "currency", "amount_minor", "effective_at"}, postings},
	}
	for _, c := range copies {
		if _, err := tx.CopyFrom(ctx, c.table, c.cols, pgx.CopyFromRows(c.rows)); err != nil {
			return fmt.Errorf("sim: copy %s: %w", c.table[0], err)
		}
	}
	// OPENING_EQUITY (alacak doğal taraflı) borçlandıkça azalır.
	for cur, total := range totals {
		if _, err := tx.Exec(ctx, `UPDATE account_balances SET balance_minor = balance_minor - $2, updated_at = now() WHERE account_id = $1`,
			equity[cur], total); err != nil {
			return fmt.Errorf("sim: equity balance: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO seed_runs (name, seed, customers, accounts) VALUES ($1, $2, $3, $4)`,
		seedName, p.Seed, len(ds.Customers), len(ds.Accounts)); err != nil {
		return fmt.Errorf("sim: seed_runs insert: %w", err)
	}
	return rec.Record(ctx, tx, audit.Entry{Action: "simulator.seed", Entity: "seed_run", EntityID: seedName,
		New: map[string]any{"seed": p.Seed, "customers": len(ds.Customers), "accounts": len(ds.Accounts), "opening_entries": len(entries)}})
}

func equityAccounts(ctx context.Context, q db.DBTX) (map[money.Currency]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT currency, id FROM accounts WHERE kind = 'OPENING_EQUITY' AND customer_id IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("sim: equity accounts: %w", err)
	}
	defer rows.Close()
	out := map[money.Currency]uuid.UUID{}
	for rows.Next() {
		var c money.Currency
		var id uuid.UUID
		if err := rows.Scan(&c, &id); err != nil {
			return nil, err
		}
		out[c] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range money.Currencies {
		if _, ok := out[c]; !ok {
			return nil, fmt.Errorf("sim: OPENING_EQUITY/%s account missing (api must start first)", c)
		}
	}
	return out, nil
}
