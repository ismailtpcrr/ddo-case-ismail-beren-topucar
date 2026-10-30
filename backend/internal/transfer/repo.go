package transfer

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"parakopru/internal/platform/db"
	"parakopru/internal/platform/page"
)

const columns = `id, type, state, from_account_id, to_account_id, to_iban, amount_minor, currency, description,
	to_amount_minor, to_currency, fx_rate::text, requested_by, journal_entry_id, settlement_entry_id, reversal_entry_id, failure_code, created_at, updated_at`

func scan(row pgx.Row) (Transfer, error) {
	var t Transfer
	var rate *string
	err := row.Scan(&t.ID, &t.Type, &t.State, &t.FromAccountID, &t.ToAccountID, &t.ToIBAN, &t.AmountMinor, &t.Currency,
		&t.Description, &t.ToAmountMinor, &t.ToCurrency, &rate, &t.RequestedBy, &t.JournalEntryID, &t.SettlementEntryID, &t.ReversalEntryID, &t.FailureCode, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, ErrNotFound
	}
	if err != nil {
		return Transfer{}, fmt.Errorf("transfer: scan: %w", err)
	}
	if rate != nil {
		d, err := decimal.NewFromString(*rate)
		if err != nil {
			return Transfer{}, fmt.Errorf("transfer: bad fx rate %q: %w", *rate, err)
		}
		t.FXRate = &d
	}
	return t, nil
}

func insert(ctx context.Context, q db.DBTX, t Transfer) error {
	_, err := q.Exec(ctx, `
		INSERT INTO transfers (id, type, state, from_account_id, to_account_id, to_iban, amount_minor, currency, description, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		t.ID, t.Type, t.State, t.FromAccountID, t.ToAccountID, t.ToIBAN, t.AmountMinor, t.Currency, t.Description, t.RequestedBy)
	if err != nil {
		return fmt.Errorf("transfer: insert: %w", err)
	}
	return nil
}

// transition durumu yalnız beklenen durumdaysa değiştirir (UPDATE … WHERE state = $beklenen): yarışta tek kazanan olur.
func transition(ctx context.Context, q db.DBTX, id uuid.UUID, from, to State, entryID *uuid.UUID, failure *string) error {
	tag, err := q.Exec(ctx, `
		UPDATE transfers SET state = $3, journal_entry_id = COALESCE($4, journal_entry_id),
		       failure_code = COALESCE($5, failure_code), updated_at = now()
		 WHERE id = $1 AND state = $2`, id, from, to, entryID, failure)
	if err != nil {
		return fmt.Errorf("transfer: transition: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return invalidTransition(from, to)
	}
	return nil
}

func insertEvent(ctx context.Context, q db.DBTX, id, transferID uuid.UUID, from *State, to State, note string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO transfer_events (id, transfer_id, from_state, to_state, note) VALUES ($1, $2, $3, $4, NULLIF($5, ''))`,
		id, transferID, from, to, note)
	if err != nil {
		return fmt.Errorf("transfer: insert event: %w", err)
	}
	return nil
}

func get(ctx context.Context, q db.DBTX, id uuid.UUID) (Transfer, error) {
	return scan(q.QueryRow(ctx, `SELECT `+columns+` FROM transfers WHERE id = $1`, id))
}

func events(ctx context.Context, q db.DBTX, id uuid.UUID) ([]Event, error) {
	rows, err := q.Query(ctx, `SELECT from_state, to_state, note, at FROM transfer_events WHERE transfer_id = $1 ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("transfer: events: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var e Event
		err := r.Scan(&e.From, &e.To, &e.Note, &e.At)
		return e, err
	})
}

type cursor struct {
	ID uuid.UUID `json:"id"`
}

// list transferleri en yeni önce, keyset sayfalı listeler. customerID verilirse yalnız o müşterinin gönderdiği ya da aldıkları.
func list(ctx context.Context, q db.DBTX, customerID *uuid.UUID, state *State, req page.Request) (page.Page[Transfer], error) {
	var after *uuid.UUID
	if req.Cursor != "" {
		var c cursor
		if err := page.Decode(req.Cursor, &c); err != nil {
			return page.Page[Transfer]{}, err
		}
		after = &c.ID
	}
	rows, err := q.Query(ctx, `SELECT `+columns+` FROM transfers t
		 WHERE ($2::uuid IS NULL OR t.id < $2)
		   AND ($4::text IS NULL OR t.state = $4)
		   AND ($1::uuid IS NULL
		        OR t.from_account_id IN (SELECT id FROM accounts WHERE customer_id = $1)
		        OR t.to_account_id   IN (SELECT id FROM accounts WHERE customer_id = $1))
		 ORDER BY t.id DESC LIMIT $3`, customerID, after, req.Limit+1, state)
	if err != nil {
		return page.Page[Transfer]{}, fmt.Errorf("transfer: list: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Transfer, error) { return scan(r) })
	if err != nil {
		return page.Page[Transfer]{}, err
	}
	return page.New(items, req.Limit, func(t Transfer) any { return cursor{ID: t.ID} })
}

// lock transferi satır kilidiyle okur: bildirim (webhook) ile worker aynı transferi aynı anda sonuçlandırmaya
// çalışırsa biri bekler ve kilidi alınca güncel durumu görür.
func lock(ctx context.Context, q db.DBTX, id uuid.UUID) (Transfer, error) {
	return scan(q.QueryRow(ctx, `SELECT `+columns+` FROM transfers WHERE id = $1 FOR UPDATE`, id))
}

// setEntry transferin defter kaydı sütunlarından birini yazar (column sabit listedendir).
func setEntry(ctx context.Context, q db.DBTX, id uuid.UUID, column string, entryID uuid.UUID) error {
	switch column {
	case "settlement_entry_id", "reversal_entry_id":
	default:
		return fmt.Errorf("transfer: bad column %q", column)
	}
	_, err := q.Exec(ctx, `UPDATE transfers SET `+column+` = $2, updated_at = now() WHERE id = $1`, id, entryID)
	if err != nil {
		return fmt.Errorf("transfer: set %s: %w", column, err)
	}
	return nil
}

func insertApproval(ctx context.Context, q db.DBTX, id, transferID, requestedBy uuid.UUID) error {
	_, err := q.Exec(ctx, `INSERT INTO approvals (id, transfer_id, requested_by) VALUES ($1, $2, $3)`, id, transferID, requestedBy)
	if err != nil {
		return fmt.Errorf("transfer: insert approval: %w", err)
	}
	return nil
}

func decideApproval(ctx context.Context, q db.DBTX, transferID, decidedBy uuid.UUID, status, reason string) error {
	_, err := q.Exec(ctx, `
		UPDATE approvals SET status = $3, decided_by = $2, reason = NULLIF($4, ''), decided_at = now()
		 WHERE transfer_id = $1 AND status = 'PENDING'`, transferID, decidedBy, status, reason)
	if db.IsCheckViolation(err, "approvals_four_eyes") {
		return ErrFourEyes
	}
	if err != nil {
		return fmt.Errorf("transfer: decide approval: %w", err)
	}
	return nil
}

func getApproval(ctx context.Context, q db.DBTX, transferID uuid.UUID) (*Approval, error) {
	var a Approval
	err := q.QueryRow(ctx, `SELECT id, transfer_id, requested_by, status, decided_by, reason, created_at, decided_at FROM approvals WHERE transfer_id = $1`, transferID).
		Scan(&a.ID, &a.TransferID, &a.RequestedBy, &a.Status, &a.DecidedBy, &a.Reason, &a.CreatedAt, &a.DecidedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("transfer: get approval: %w", err)
	}
	return &a, nil
}

// setFX çok para birimli transferde alıcının aldığı tutarı ve kullanılan kuru kayda yazar.
func setFX(ctx context.Context, q db.DBTX, id uuid.UUID, toAmount int64, toCurrency string, rate decimal.Decimal) error {
	_, err := q.Exec(ctx, `UPDATE transfers SET to_amount_minor = $2, to_currency = $3, fx_rate = $4::numeric, updated_at = now() WHERE id = $1`,
		id, toAmount, toCurrency, rate.String())
	if err != nil {
		return fmt.Errorf("transfer: set fx: %w", err)
	}
	return nil
}
