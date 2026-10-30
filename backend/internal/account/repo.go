package account

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"parakopru/internal/money"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/page"
)

const selectAccount = `
SELECT a.id, a.customer_id, a.kind, a.currency, a.normal_side, a.allow_negative, a.name, a.iban,
       a.interest_product, a.maturity_date, a.status, a.opened_at, b.balance_minor, b.held_minor
  FROM accounts a
  JOIN account_balances b ON b.account_id = a.id`

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.CustomerID, &a.Kind, &a.Currency, &a.NormalSide, &a.AllowNegative, &a.Name, &a.IBAN,
		&a.InterestProduct, &a.MaturityDate, &a.Status, &a.OpenedAt, &a.BalanceMinor, &a.HeldMinor)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("account: scan: %w", err)
	}
	return a, nil
}

func getAccount(ctx context.Context, q db.DBTX, id uuid.UUID) (Account, error) {
	return scanAccount(q.QueryRow(ctx, selectAccount+` WHERE a.id = $1`, id))
}

// insertAccount hesabı ve sıfır bakiyeli özet satırını ekler.
func insertAccount(ctx context.Context, q db.DBTX, a Account) error {
	_, err := q.Exec(ctx,
		`INSERT INTO accounts (id, customer_id, kind, currency, normal_side, allow_negative, name, iban, interest_product, maturity_date)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		a.ID, a.CustomerID, a.Kind, a.Currency, a.NormalSide, a.AllowNegative, a.Name, a.IBAN, a.InterestProduct, a.MaturityDate)
	if err != nil {
		return fmt.Errorf("account: insert: %w", err)
	}
	_, err = q.Exec(ctx,
		`INSERT INTO account_balances (account_id, allow_negative) VALUES ($1, $2)`, a.ID, a.AllowNegative)
	if err != nil {
		return fmt.Errorf("account: insert balance: %w", err)
	}
	return nil
}

func nextAccountNumber(ctx context.Context, q db.DBTX) (int64, error) {
	var n int64
	if err := q.QueryRow(ctx, `SELECT nextval('account_number_seq')`).Scan(&n); err != nil {
		return 0, fmt.Errorf("account: next number: %w", err)
	}
	return n, nil
}

// insertInternalIfAbsent iç hesabı ekler; aynı tür ve para biriminde varsa dokunmaz.
func insertInternalIfAbsent(ctx context.Context, q db.DBTX, id uuid.UUID, kind Kind, side Side, c money.Currency, name string) (bool, error) {
	tag, err := q.Exec(ctx,
		`INSERT INTO accounts (id, kind, currency, normal_side, allow_negative, name)
		 VALUES ($1, $2, $3, $4, true, $5)
		 ON CONFLICT (kind, currency) WHERE customer_id IS NULL DO NOTHING`,
		id, kind, c, side, name)
	if err != nil {
		return false, fmt.Errorf("account: insert internal: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	_, err = q.Exec(ctx, `INSERT INTO account_balances (account_id, allow_negative) VALUES ($1, true)`, id)
	if err != nil {
		return false, fmt.Errorf("account: insert internal balance: %w", err)
	}
	return true, nil
}

func internalAccountID(ctx context.Context, q db.DBTX, kind Kind, c money.Currency) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx,
		`SELECT id FROM accounts WHERE customer_id IS NULL AND kind = $1 AND currency = $2`, kind, c).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("account: internal account %s/%s missing: %w", kind, c, ErrNotFound)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("account: internal account: %w", err)
	}
	return id, nil
}

type cursor struct {
	ID uuid.UUID `json:"id"`
}

// listAccounts hesapları kimlik sırasıyla, keyset sayfalı listeler. customerID verilirse yalnız o müşterinin hesapları.
func listAccounts(ctx context.Context, q db.DBTX, customerID *uuid.UUID, req page.Request) (page.Page[Account], error) {
	var after *uuid.UUID
	if req.Cursor != "" {
		var c cursor
		if err := page.Decode(req.Cursor, &c); err != nil {
			return page.Page[Account]{}, err
		}
		after = &c.ID
	}
	rows, err := q.Query(ctx, selectAccount+`
		 WHERE ($1::uuid IS NULL OR a.customer_id = $1)
		   AND ($2::uuid IS NULL OR a.id > $2)
		 ORDER BY a.id
		 LIMIT $3`, customerID, after, req.Limit+1)
	if err != nil {
		return page.Page[Account]{}, fmt.Errorf("account: list: %w", err)
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Account, error) { return scanAccount(r) })
	if err != nil {
		return page.Page[Account]{}, err
	}
	return page.New(list, req.Limit, func(a Account) any { return cursor{ID: a.ID} })
}
