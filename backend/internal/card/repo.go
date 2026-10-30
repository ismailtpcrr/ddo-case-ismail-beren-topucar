package card

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"parakopru/internal/platform/db"
	"parakopru/internal/platform/page"
)

const holdCols = `id, account_id, card_id, merchant, currency, amount_minor, captured_minor, released_minor, remaining_minor, state, expires_at, created_at`

func scanHold(row pgx.Row) (Hold, error) {
	var h Hold
	err := row.Scan(&h.ID, &h.AccountID, &h.CardID, &h.Merchant, &h.Currency, &h.AmountMinor, &h.CapturedMinor, &h.ReleasedMinor,
		&h.RemainingMinor, &h.State, &h.ExpiresAt, &h.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Hold{}, ErrHoldNotFound
	}
	if err != nil {
		return Hold{}, fmt.Errorf("card: scan hold: %w", err)
	}
	return h, nil
}

func getHold(ctx context.Context, q db.DBTX, id uuid.UUID, lock bool) (Hold, error) {
	sql := `SELECT ` + holdCols + ` FROM holds WHERE id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	return scanHold(q.QueryRow(ctx, sql, id))
}

func getCard(ctx context.Context, q db.DBTX, id uuid.UUID) (Card, error) {
	var c Card
	err := q.QueryRow(ctx, `SELECT id, account_id, customer_id, pan_masked, status, created_at FROM cards WHERE id = $1`, id).
		Scan(&c.ID, &c.AccountID, &c.CustomerID, &c.PANMasked, &c.Status, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Card{}, ErrCardNotFound
	}
	if err != nil {
		return Card{}, fmt.Errorf("card: get card: %w", err)
	}
	return c, nil
}

// blockFunds koşullu UPDATE ile bakiyeyi bloke eder: yalnız kullanılabilir bakiye (bakiye − bloke) yetiyorsa satır güncellenir.
// UPDATE satırı kilitlediği için eşzamanlı provizyonlar sıraya girer ve her biri güncel değeri görür.
func blockFunds(ctx context.Context, q db.DBTX, accountID uuid.UUID, minor int64) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE account_balances SET held_minor = held_minor + $2, updated_at = now()
		 WHERE account_id = $1 AND (allow_negative OR balance_minor - held_minor >= $2)`, accountID, minor)
	if err != nil {
		return false, fmt.Errorf("card: block funds: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// releaseFunds blokeyi azaltır (süre aşımı). Satır önceden kilitlenmiş olmalıdır.
func releaseFunds(ctx context.Context, q db.DBTX, accountID uuid.UUID, minor int64) error {
	_, err := q.Exec(ctx, `UPDATE account_balances SET held_minor = held_minor - $2, updated_at = now() WHERE account_id = $1`, accountID, minor)
	if err != nil {
		return fmt.Errorf("card: release funds: %w", err)
	}
	return nil
}

func insertHold(ctx context.Context, q db.DBTX, h Hold) error {
	_, err := q.Exec(ctx, `
		INSERT INTO holds (id, account_id, card_id, merchant, currency, amount_minor, remaining_minor, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6, $7)`, h.ID, h.AccountID, h.CardID, h.Merchant, h.Currency, h.AmountMinor, h.ExpiresAt)
	if err != nil {
		return fmt.Errorf("card: insert hold: %w", err)
	}
	return nil
}

func updateHold(ctx context.Context, q db.DBTX, h Hold) error {
	_, err := q.Exec(ctx, `
		UPDATE holds SET captured_minor = $2, released_minor = $3, remaining_minor = $4, state = $5, updated_at = now() WHERE id = $1`,
		h.ID, h.CapturedMinor, h.ReleasedMinor, h.RemainingMinor, h.State)
	if err != nil {
		return fmt.Errorf("card: update hold: %w", err)
	}
	return nil
}

func scanCapture(row pgx.Row) (Capture, error) {
	var c Capture
	err := row.Scan(&c.ID, &c.HoldID, &c.AmountMinor, &c.RefundedMinor, &c.Final, &c.JournalEntryID, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Capture{}, ErrCaptureNotFound
	}
	if err != nil {
		return Capture{}, fmt.Errorf("card: scan capture: %w", err)
	}
	return c, nil
}

const captureCols = `id, hold_id, amount_minor, refunded_minor, final, journal_entry_id, created_at`

func getCapture(ctx context.Context, q db.DBTX, id uuid.UUID, lock bool) (Capture, error) {
	sql := `SELECT ` + captureCols + ` FROM captures WHERE id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	return scanCapture(q.QueryRow(ctx, sql, id))
}

func insertCapture(ctx context.Context, q db.DBTX, c Capture) error {
	_, err := q.Exec(ctx, `INSERT INTO captures (id, hold_id, amount_minor, final, journal_entry_id) VALUES ($1, $2, $3, $4, $5)`,
		c.ID, c.HoldID, c.AmountMinor, c.Final, c.JournalEntryID)
	if err != nil {
		return fmt.Errorf("card: insert capture: %w", err)
	}
	return nil
}

func insertRefund(ctx context.Context, q db.DBTX, r Refund) error {
	if _, err := q.Exec(ctx, `INSERT INTO refunds (id, capture_id, amount_minor, journal_entry_id) VALUES ($1, $2, $3, $4)`,
		r.ID, r.CaptureID, r.AmountMinor, r.JournalEntryID); err != nil {
		return fmt.Errorf("card: insert refund: %w", err)
	}
	if _, err := q.Exec(ctx, `UPDATE captures SET refunded_minor = refunded_minor + $2 WHERE id = $1`, r.CaptureID, r.AmountMinor); err != nil {
		return fmt.Errorf("card: update capture refunded: %w", err)
	}
	return nil
}

// dueHolds süresi dolmuş aktif provizyonları (kilitlemeden) listeler: yalnız aday; asıl kontrol kilit altında yapılır.
func dueHolds(ctx context.Context, q db.DBTX, now time.Time, limit int) ([]Hold, error) {
	rows, err := q.Query(ctx, `SELECT `+holdCols+` FROM holds WHERE state = 'ACTIVE' AND expires_at <= $1 ORDER BY expires_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("card: due holds: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Hold, error) { return scanHold(r) })
}

// lockExpired süresi dolmuş ve başka işlem tarafından kilitlenmemiş aktif provizyonu kilitler (SKIP LOCKED).
func lockExpired(ctx context.Context, q db.DBTX, id uuid.UUID, now time.Time) (Hold, bool, error) {
	h, err := scanHold(q.QueryRow(ctx, `SELECT `+holdCols+` FROM holds WHERE id = $1 AND state = 'ACTIVE' AND expires_at <= $2 FOR UPDATE SKIP LOCKED`, id, now))
	if errors.Is(err, ErrHoldNotFound) {
		return Hold{}, false, nil
	}
	return h, err == nil, err
}

type idCursor struct {
	ID uuid.UUID `json:"id"`
}

// listCards kartları keyset sayfalı listeler; customerID verilirse yalnız o müşterinin kartları.
func listCards(ctx context.Context, q db.DBTX, customerID *uuid.UUID, req page.Request) (page.Page[Card], error) {
	var after *uuid.UUID
	if req.Cursor != "" {
		var c idCursor
		if err := page.Decode(req.Cursor, &c); err != nil {
			return page.Page[Card]{}, err
		}
		after = &c.ID
	}
	rows, err := q.Query(ctx, `
		SELECT id, account_id, customer_id, pan_masked, status, created_at FROM cards
		 WHERE ($1::uuid IS NULL OR customer_id = $1) AND ($2::uuid IS NULL OR id > $2)
		 ORDER BY id LIMIT $3`, customerID, after, req.Limit+1)
	if err != nil {
		return page.Page[Card]{}, fmt.Errorf("card: list cards: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Card, error) {
		var c Card
		err := r.Scan(&c.ID, &c.AccountID, &c.CustomerID, &c.PANMasked, &c.Status, &c.CreatedAt)
		return c, err
	})
	if err != nil {
		return page.Page[Card]{}, fmt.Errorf("card: scan cards: %w", err)
	}
	return page.New(items, req.Limit, func(c Card) any { return idCursor{ID: c.ID} })
}

// listHolds hesabın provizyonlarını en yeni önce listeler; state verilirse süzer.
func listHolds(ctx context.Context, q db.DBTX, accountID uuid.UUID, state *State, req page.Request) (page.Page[Hold], error) {
	var after *uuid.UUID
	if req.Cursor != "" {
		var c idCursor
		if err := page.Decode(req.Cursor, &c); err != nil {
			return page.Page[Hold]{}, err
		}
		after = &c.ID
	}
	rows, err := q.Query(ctx, `
		SELECT `+holdCols+` FROM holds
		 WHERE account_id = $1 AND ($2::uuid IS NULL OR id < $2) AND ($4::text IS NULL OR state = $4)
		 ORDER BY id DESC LIMIT $3`, accountID, after, req.Limit+1, state)
	if err != nil {
		return page.Page[Hold]{}, fmt.Errorf("card: list holds: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Hold, error) { return scanHold(r) })
	if err != nil {
		return page.Page[Hold]{}, err
	}
	return page.New(items, req.Limit, func(h Hold) any { return idCursor{ID: h.ID} })
}

func insertCard(ctx context.Context, q db.DBTX, c Card) error {
	_, err := q.Exec(ctx, `INSERT INTO cards (id, account_id, customer_id, pan_masked) VALUES ($1, $2, $3, $4)`,
		c.ID, c.AccountID, c.CustomerID, c.PANMasked)
	if err != nil {
		return fmt.Errorf("card: insert card: %w", err)
	}
	return nil
}
