package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"parakopru/internal/platform/db"
)

// User bir kullanıcı kaydıdır.
type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	Role         Role
	FullName     string
	CreatedAt    time.Time
}

type refreshToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	ExpiresAt time.Time
	RevokedAt *time.Time
}

var errNotFound = errors.New("auth: not found")

// Repo kullanıcı ve refresh token SQL sorgularıdır. Transaction başlatmaz; db.DBTX alır.
type Repo struct{}

const userColumns = `id, email, password_hash, role, full_name, created_at`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.FullName, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, errNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("auth: scan user: %w", err)
	}
	return u, nil
}

// UserByEmail e-postaya göre kullanıcıyı getirir (e-posta küçük harfle saklanır).
func (Repo) UserByEmail(ctx context.Context, q db.DBTX, email string) (User, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email))
}

// UserByID kimliğe göre kullanıcıyı getirir.
func (Repo) UserByID(ctx context.Context, q db.DBTX, id uuid.UUID) (User, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

// InsertUserIfAbsent kullanıcıyı ekler; aynı e-posta varsa hiçbir şey yapmaz. Eklendiyse true döner.
func (Repo) InsertUserIfAbsent(ctx context.Context, q db.DBTX, u User) (bool, error) {
	tag, err := q.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, role, full_name)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (email) DO NOTHING`,
		u.ID, u.Email, u.PasswordHash, u.Role, u.FullName)
	if err != nil {
		return false, fmt.Errorf("auth: insert user: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ExistingEmails verilen e-postalardan veritabanında olanları döndürür.
func (Repo) ExistingEmails(ctx context.Context, q db.DBTX, emails []string) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT email FROM users WHERE email = ANY($1)`, emails)
	if err != nil {
		return nil, fmt.Errorf("auth: existing emails: %w", err)
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("auth: existing emails: %w", err)
	}
	out := make(map[string]bool, len(found))
	for _, e := range found {
		out[e] = true
	}
	return out, nil
}

// InsertRefreshToken yeni bir refresh token kaydı ekler.
func (Repo) InsertRefreshToken(ctx context.Context, q db.DBTX, id, userID uuid.UUID, expiresAt time.Time) error {
	_, err := q.Exec(ctx,
		`INSERT INTO refresh_tokens (id, user_id, expires_at) VALUES ($1, $2, $3)`, id, userID, expiresAt)
	if err != nil {
		return fmt.Errorf("auth: insert refresh token: %w", err)
	}
	return nil
}

// LockRefreshToken token kaydını satır kilidiyle okur. Aynı token'la eşzamanlı iki yenileme isteğinden
// yalnız biri ilerler; diğeri kilidi bekler ve token'ı iptal edilmiş bulur.
func (Repo) LockRefreshToken(ctx context.Context, q db.DBTX, id uuid.UUID) (refreshToken, error) {
	var t refreshToken
	err := q.QueryRow(ctx,
		`SELECT id, user_id, expires_at, revoked_at FROM refresh_tokens WHERE id = $1 FOR UPDATE`, id).
		Scan(&t.ID, &t.UserID, &t.ExpiresAt, &t.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return refreshToken{}, errNotFound
	}
	if err != nil {
		return refreshToken{}, fmt.Errorf("auth: lock refresh token: %w", err)
	}
	return t, nil
}

// RevokeRefreshToken token'ı iptal eder (zaten iptalse dokunmaz). replacedBy, yenilemede yerine geçen token'dır.
func (Repo) RevokeRefreshToken(ctx context.Context, q db.DBTX, id uuid.UUID, at time.Time, replacedBy *uuid.UUID) error {
	_, err := q.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2, replaced_by = $3 WHERE id = $1 AND revoked_at IS NULL`,
		id, at, replacedBy)
	if err != nil {
		return fmt.Errorf("auth: revoke refresh token: %w", err)
	}
	return nil
}

// RevokeAllForUser kullanıcının açık bütün refresh token'larını iptal eder.
func (Repo) RevokeAllForUser(ctx context.Context, q db.DBTX, userID uuid.UUID, at time.Time) (int64, error) {
	tag, err := q.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, userID, at)
	if err != nil {
		return 0, fmt.Errorf("auth: revoke all: %w", err)
	}
	return tag.RowsAffected(), nil
}
