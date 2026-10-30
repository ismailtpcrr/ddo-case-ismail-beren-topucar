package fx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"parakopru/internal/money"
	"parakopru/internal/platform/db"
)

const cols = `id, base, quote, rate::text, valid_from, created_by, created_at`

func scanRate(row pgx.Row) (Rate, error) {
	var r Rate
	var txt string
	if err := row.Scan(&r.ID, &r.Base, &r.Quote, &txt, &r.ValidFrom, &r.CreatedBy, &r.CreatedAt); err != nil {
		return Rate{}, err
	}
	d, err := decimal.NewFromString(txt)
	if err != nil {
		return Rate{}, fmt.Errorf("fx: bad rate %q: %w", txt, err)
	}
	r.Rate = d
	return r, nil
}

func insert(ctx context.Context, q db.DBTX, r Rate) error {
	_, err := q.Exec(ctx, `INSERT INTO fx_rates (id, base, quote, rate, valid_from, created_by) VALUES ($1, $2, $3, $4::numeric, $5, $6)`,
		r.ID, r.Base, r.Quote, r.Rate.StringFixed(8), r.ValidFrom, r.CreatedBy)
	if db.IsUniqueViolation(err, "fx_rates_valid_from_key") {
		return ErrRateExists
	}
	if err != nil {
		return fmt.Errorf("fx: insert: %w", err)
	}
	return nil
}

// current c para biriminin at anında geçerli TL kurunu döndürür (valid_from ≤ at olanların en yenisi).
func current(ctx context.Context, q db.DBTX, c money.Currency, at time.Time) (Rate, error) {
	r, err := scanRate(q.QueryRow(ctx, `SELECT `+cols+` FROM fx_rates WHERE base = $1 AND valid_from <= $2 ORDER BY valid_from DESC LIMIT 1`, c, at))
	if errors.Is(err, pgx.ErrNoRows) {
		return Rate{}, ErrRateUnavailable
	}
	return r, err
}

// history kurları en yeni geçerlilik önce listeler; base ve asOf isteğe bağlı süzgeçtir.
func history(ctx context.Context, q db.DBTX, base *money.Currency, asOf *time.Time, limit int) ([]Rate, error) {
	rows, err := q.Query(ctx, `
		SELECT `+cols+` FROM fx_rates
		 WHERE ($1::text IS NULL OR base = $1) AND ($2::timestamptz IS NULL OR valid_from <= $2)
		 ORDER BY valid_from DESC, id DESC LIMIT $3`, base, asOf, limit)
	if err != nil {
		return nil, fmt.Errorf("fx: history: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Rate, error) { return scanRate(r) })
}
