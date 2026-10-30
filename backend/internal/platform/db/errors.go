package db

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsUniqueViolation hatanın verilen benzersizlik kısıtının ihlali (23505) olup olmadığını söyler.
// constraint boşsa herhangi bir benzersizlik ihlali eşleşir.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && (constraint == "" || pgErr.ConstraintName == constraint)
}

// IsCheckViolation hatanın verilen CHECK kısıtının ihlali (23514) olup olmadığını söyler.
func IsCheckViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514" && (constraint == "" || pgErr.ConstraintName == constraint)
}
