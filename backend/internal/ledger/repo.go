package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"parakopru/internal/account"
	"parakopru/internal/money"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/page"
)

// lockedBalance kilitlenmiş bir bakiye özeti satırı ve hesabın kayıt için gereken bilgileridir.
type lockedBalance struct {
	AccountID     uuid.UUID
	Currency      money.Currency
	NormalSide    account.Side
	AllowNegative bool
	BalanceMinor  int64
	HeldMinor     int64
}

// lockBalances verilen hesapların özet satırlarını tek sorguda, hesap kimliği sırasıyla kilitler.
//
// Her transaction kilitleri aynı genel sırayla aldığı için döngüsel bekleme (A, B'yi; B, A'yı bekler) oluşamaz:
// ters yönlü iki eşzamanlı transfer de önce küçük kimlikli hesabı kilitlemeye çalışır, biri bekler, deadlock olmaz.
// FOR UPDATE, beklemeden sonra satırın commit edilmiş en son halini döndürür; bakiye kontrolü güncel veriyle yapılır.
func lockBalances(ctx context.Context, q db.DBTX, ids []uuid.UUID) ([]lockedBalance, error) {
	rows, err := q.Query(ctx, `
		SELECT b.account_id, a.currency, a.normal_side, b.allow_negative, b.balance_minor, b.held_minor
		  FROM account_balances b
		  JOIN accounts a ON a.id = b.account_id
		 WHERE b.account_id = ANY($1)
		 ORDER BY b.account_id
		   FOR UPDATE OF b`, ids)
	if err != nil {
		return nil, fmt.Errorf("ledger: lock balances: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (lockedBalance, error) {
		var l lockedBalance
		err := r.Scan(&l.AccountID, &l.Currency, &l.NormalSide, &l.AllowNegative, &l.BalanceMinor, &l.HeldMinor)
		return l, err
	})
}

// insertEntry kaydı ekler; geçerlilik ve kayıt zamanlarını (veritabanı saati) döndürür.
func insertEntry(ctx context.Context, q db.DBTX, id EntryID, e Entry, reversalOf *EntryID) (effectiveAt, recordedAt time.Time, err error) {
	err = q.QueryRow(ctx, `
		INSERT INTO journal_entries (id, kind, description, reference, effective_at, reversal_of)
		VALUES ($1, $2, $3, NULLIF($4, ''), COALESCE($5, now()), $6)
		RETURNING effective_at, recorded_at`,
		id, e.Kind, e.Description, e.Reference, e.EffectiveAt, reversalOf).Scan(&effectiveAt, &recordedAt)
	switch {
	case db.IsUniqueViolation(err, "journal_entries_reversal_once"):
		return time.Time{}, time.Time{}, ErrAlreadyReversed
	case db.IsCheckViolation(err, "journal_entries_effective_not_future"):
		return time.Time{}, time.Time{}, ErrInvalidEffectiveAt
	case err != nil:
		return time.Time{}, time.Time{}, fmt.Errorf("ledger: insert entry: %w", err)
	}
	return effectiveAt, recordedAt, nil
}

// insertPostings kaydın satırlarını tek sorguda ekler. recorded_at'i trigger yazar (kayıtla aynı transaction saati).
func insertPostings(ctx context.Context, q db.DBTX, entryID EntryID, effectiveAt time.Time, ids []uuid.UUID, lines []Line) error {
	accounts := make([]uuid.UUID, len(lines))
	currencies := make([]string, len(lines))
	amounts := make([]int64, len(lines))
	for i, l := range lines {
		accounts[i], currencies[i], amounts[i] = l.AccountID, string(l.Amount.Currency), l.signed()
	}
	_, err := q.Exec(ctx, `
		INSERT INTO postings (id, entry_id, account_id, currency, amount_minor, effective_at)
		SELECT p.id, $1, p.account_id, p.currency, p.amount_minor, $2
		  FROM unnest($3::uuid[], $4::uuid[], $5::text[], $6::bigint[]) AS p(id, account_id, currency, amount_minor)`,
		entryID, effectiveAt, ids, accounts, currencies, amounts)
	if err != nil {
		return fmt.Errorf("ledger: insert postings: %w", err)
	}
	return nil
}

// applyDeltas bakiye özetlerine doğal işaretli değişimleri ekler. Satırlar lockBalances ile kilitlenmiş olmalıdır.
func applyDeltas(ctx context.Context, q db.DBTX, ids []uuid.UUID, deltas []int64) error {
	_, err := q.Exec(ctx, `
		UPDATE account_balances b
		   SET balance_minor = b.balance_minor + d.delta, updated_at = now()
		  FROM unnest($1::uuid[], $2::bigint[]) AS d(account_id, delta)
		 WHERE b.account_id = d.account_id`, ids, deltas)
	switch {
	case db.IsCheckViolation(err, "account_balances_available_non_negative"):
		return ErrInsufficientFunds // Go kontrolü bunu önceden yakalar; kısıt son güvencedir
	case err != nil:
		return fmt.Errorf("ledger: update balances: %w", err)
	}
	return nil
}

// getEntry kaydı satırlarıyla ve (varsa) onu ters çeviren kaydın kimliğiyle getirir.
func getEntry(ctx context.Context, q db.DBTX, id EntryID) (JournalEntry, error) {
	var e JournalEntry
	err := q.QueryRow(ctx, `
		SELECT e.id, e.kind, e.description, e.reference, e.effective_at, e.recorded_at, e.reversal_of,
		       (SELECT r.id FROM journal_entries r WHERE r.reversal_of = e.id)
		  FROM journal_entries e
		 WHERE e.id = $1`, id).
		Scan(&e.ID, &e.Kind, &e.Description, &e.Reference, &e.EffectiveAt, &e.RecordedAt, &e.ReversalOf, &e.ReversedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return JournalEntry{}, ErrEntryNotFound
	}
	if err != nil {
		return JournalEntry{}, fmt.Errorf("ledger: get entry: %w", err)
	}
	rows, err := q.Query(ctx,
		`SELECT id, account_id, currency, amount_minor FROM postings WHERE entry_id = $1 ORDER BY id`, id)
	if err != nil {
		return JournalEntry{}, fmt.Errorf("ledger: get postings: %w", err)
	}
	e.Postings, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Posting, error) {
		var p Posting
		err := r.Scan(&p.ID, &p.AccountID, &p.Currency, &p.AmountMinor)
		return p, err
	})
	if err != nil {
		return JournalEntry{}, fmt.Errorf("ledger: scan postings: %w", err)
	}
	return e, nil
}

func isReversed(ctx context.Context, q db.DBTX, id EntryID) (bool, error) {
	var reversed bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM journal_entries WHERE reversal_of = $1)`, id).Scan(&reversed)
	if err != nil {
		return false, fmt.Errorf("ledger: is reversed: %w", err)
	}
	return reversed, nil
}

type postingCursor struct {
	EffectiveAt time.Time `json:"t"`
	ID          uuid.UUID `json:"id"`
}

// listAccountPostings hesabın hareketlerini en yeni önce, keyset sayfalı listeler
// (postings_account_effective_idx dizini üzerinden geriye doğru okunur).
func listAccountPostings(ctx context.Context, q db.DBTX, accountID uuid.UUID, req page.Request) (page.Page[AccountPosting], error) {
	var (
		afterTime *time.Time
		afterID   *uuid.UUID
	)
	if req.Cursor != "" {
		var c postingCursor
		if err := page.Decode(req.Cursor, &c); err != nil {
			return page.Page[AccountPosting]{}, err
		}
		afterTime, afterID = &c.EffectiveAt, &c.ID
	}
	rows, err := q.Query(ctx, `
		SELECT p.id, p.entry_id, e.kind, e.description, e.reference, e.reversal_of,
		       p.currency, p.amount_minor, p.effective_at, p.recorded_at
		  FROM postings p
		  JOIN journal_entries e ON e.id = p.entry_id
		 WHERE p.account_id = $1
		   AND ($2::timestamptz IS NULL OR (p.effective_at, p.id) < ($2, $3::uuid))
		 ORDER BY p.effective_at DESC, p.id DESC
		 LIMIT $4`, accountID, afterTime, afterID, req.Limit+1)
	if err != nil {
		return page.Page[AccountPosting]{}, fmt.Errorf("ledger: list postings: %w", err)
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AccountPosting, error) {
		var p AccountPosting
		err := r.Scan(&p.ID, &p.EntryID, &p.Kind, &p.Description, &p.Reference, &p.ReversalOf,
			&p.Currency, &p.AmountMinor, &p.EffectiveAt, &p.RecordedAt)
		return p, err
	})
	if err != nil {
		return page.Page[AccountPosting]{}, fmt.Errorf("ledger: scan postings: %w", err)
	}
	return page.New(list, req.Limit, func(p AccountPosting) any {
		return postingCursor{EffectiveAt: p.EffectiveAt, ID: p.ID}
	})
}
