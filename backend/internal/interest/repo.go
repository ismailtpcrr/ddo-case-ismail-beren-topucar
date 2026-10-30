package interest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"parakopru/internal/platform/db"
)

const runCols = `business_date::text, step, status, batches, accounts_accrued, attempts, started_at, finished_at`

func getRun(ctx context.Context, q db.DBTX, day time.Time, lock bool) (Run, *uuid.UUID, error) {
	sql := `SELECT ` + runCols + `, last_account_id FROM eod_runs WHERE business_date = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	var r Run
	var last *uuid.UUID
	err := q.QueryRow(ctx, sql, day).Scan(&r.BusinessDate, &r.Step, &r.Status, &r.Batches, &r.AccountsAccrued, &r.Attempts, &r.StartedAt, &r.FinishedAt, &last)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, nil, ErrRunNotFound
	}
	if err != nil {
		return Run{}, nil, fmt.Errorf("eod: get run: %w", err)
	}
	return r, last, nil
}

// nextBatch faiz işleyecek (oranı > 0) aktif müşteri hesaplarını kimlik sırasıyla, after'dan sonra döndürür; her hesabın
// o gün geçerli oranı da gelir.
func nextBatch(ctx context.Context, q db.DBTX, day time.Time, after *uuid.UUID, limit int) ([]accountRow, error) {
	rows, err := q.Query(ctx, `
		SELECT a.id, a.currency, b.balance_minor, r.method, r.day_basis, r.annual_rate::text
		  FROM accounts a
		  JOIN account_balances b ON b.account_id = a.id
		  JOIN LATERAL (SELECT method, day_basis, annual_rate FROM interest_rates ir
		                 WHERE ir.product = a.interest_product AND ir.currency = a.currency AND ir.valid_from <= $1
		                 ORDER BY ir.valid_from DESC LIMIT 1) r ON true
		 WHERE a.customer_id IS NOT NULL AND a.status = 'ACTIVE' AND a.opened_at::date <= $1
		   AND r.annual_rate > 0 AND ($2::uuid IS NULL OR a.id > $2)
		 ORDER BY a.id LIMIT $3`, day, after, limit)
	if err != nil {
		return nil, fmt.Errorf("eod: next batch: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (accountRow, error) {
		var a accountRow
		var rate string
		if err := r.Scan(&a.ID, &a.Currency, &a.BalanceMinor, &a.Rate.Method, &a.Rate.Basis, &rate); err != nil {
			return a, err
		}
		d, err := decimal.NewFromString(rate)
		a.Rate.AnnualRate = d
		return a, err
	})
}

func taxRateOn(ctx context.Context, q db.DBTX, day time.Time) (decimal.Decimal, error) {
	var s string
	err := q.QueryRow(ctx, `SELECT rate::text FROM tax_rates WHERE valid_from <= $1 ORDER BY valid_from DESC LIMIT 1`, day).Scan(&s)
	if errors.Is(err, pgx.ErrNoRows) {
		return decimal.Zero, nil
	}
	if err != nil {
		return decimal.Zero, fmt.Errorf("eod: tax rate: %w", err)
	}
	return decimal.NewFromString(s)
}

// accountState hesabın önceki günlerden gelen tahakkuk durumu ve şimdiye kadar alacaklanan net faiz; done = bu gün zaten tahakkuk etmiş.
func accountState(ctx context.Context, q db.DBTX, id uuid.UUID, day time.Time) (s State, netSoFar int64, done bool, err error) {
	if err = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM interest_accruals WHERE account_id = $1 AND accrual_date = $2)`, id, day).Scan(&done); err != nil || done {
		return
	}
	var exact string
	err = q.QueryRow(ctx, `
		SELECT cumulative_exact::text, cumulative_posted, (SELECT coalesce(sum(net_minor), 0)::bigint FROM interest_accruals WHERE account_id = $1)
		  FROM interest_accruals WHERE account_id = $1 AND accrual_date < $2 ORDER BY accrual_date DESC LIMIT 1`, id, day).
		Scan(&exact, &s.PostedMinor, &netSoFar)
	if errors.Is(err, pgx.ErrNoRows) {
		return State{CumulativeExact: decimal.Zero}, 0, false, nil
	}
	if err != nil {
		return
	}
	s.CumulativeExact, err = decimal.NewFromString(exact)
	return
}

func totals(ctx context.Context, q db.DBTX, day time.Time) ([]Total, error) {
	rows, err := q.Query(ctx, `
		SELECT a.currency, count(*), coalesce(sum(i.gross_minor), 0)::bigint, coalesce(sum(i.tax_minor), 0)::bigint, coalesce(sum(i.net_minor), 0)::bigint
		  FROM interest_accruals i JOIN accounts a ON a.id = i.account_id
		 WHERE i.accrual_date = $1 GROUP BY a.currency ORDER BY a.currency`, day)
	if err != nil {
		return nil, fmt.Errorf("eod: totals: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Total, error) {
		var t Total
		err := r.Scan(&t.Currency, &t.Accruals, &t.GrossMinor, &t.TaxMinor, &t.NetMinor)
		return t, err
	})
	if out == nil {
		out = []Total{}
	}
	return out, err
}
