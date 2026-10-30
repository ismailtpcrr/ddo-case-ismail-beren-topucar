package customer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"parakopru/internal/platform/db"
)

const columns = `id, user_id, full_name, national_id, email, created_at`

func scan(row pgx.Row) (Customer, error) {
	var c Customer
	err := row.Scan(&c.ID, &c.UserID, &c.FullName, &c.NationalID, &c.Email, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, ErrNotFound
	}
	if err != nil {
		return Customer{}, fmt.Errorf("customer: scan: %w", err)
	}
	return c, nil
}

func insert(ctx context.Context, q db.DBTX, c Customer) error {
	_, err := q.Exec(ctx,
		`INSERT INTO customers (id, user_id, full_name, national_id, email) VALUES ($1, $2, $3, $4, $5)`,
		c.ID, c.UserID, c.FullName, c.NationalID, c.Email)
	if db.IsUniqueViolation(err, "customers_national_id_key") {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("customer: insert: %w", err)
	}
	return nil
}
