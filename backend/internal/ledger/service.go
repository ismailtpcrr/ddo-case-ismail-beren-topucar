package ledger

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"parakopru/internal/account"
	"parakopru/internal/money"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/idempotency"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/page"
)

// Accounts, defterin hesap modülünden ihtiyaç duyduğu tek şeydir: nesne düzeyinde yetkiyle hesap okuma
// (müşteri yalnız kendi hesabını görür; başkasınınki 403).
type Accounts interface {
	Get(ctx context.Context, p auth.Principal, id uuid.UUID) (account.Account, error)
}

// Service defter iş kurallarıdır.
type Service struct {
	tx       *db.TxManager
	ids      ids.Generator
	audit    *audit.Recorder
	accounts Accounts
}

// NewService yeni bir Service oluşturur.
func NewService(tx *db.TxManager, idGen ids.Generator, rec *audit.Recorder, accounts Accounts) *Service {
	return &Service{tx: tx, ids: idGen, audit: rec, accounts: accounts}
}

// Post kaydı ve satırlarını verilen transaction içinde yazar ve bakiye özetlerini günceller.
//
// Transaction'ı çağıran modül açar (ör. transfer: transfer kaydı + defter kaydı + denetim kaydı tek transaction);
// böylece ya hepsi yazılır ya hiçbiri. Sıra:
//  1. Kural kontrolü (Validate): en az iki satır, pozitif tutarlar, her para biriminde borç = alacak.
//  2. Kayda giren hesapların özet satırları kimlik sırasıyla kilitlenir (deadlock yok).
//  3. Hesap var mı, para birimi uyuyor mu, eksiye düşemeyen hesapta kullanılabilir bakiye yetiyor mu.
//  4. Kayıt, satırlar ve özet güncellemesi yazılır. Borç = alacak commit anında veritabanında da kontrol edilir.
func (s *Service) Post(ctx context.Context, tx db.DBTX, e Entry) (EntryID, error) {
	return s.post(ctx, tx, e, nil)
}

func (s *Service) post(ctx context.Context, tx db.DBTX, e Entry, reversalOf *EntryID) (EntryID, error) {
	if err := e.Validate(); err != nil {
		return uuid.Nil, err
	}

	// Hesap başına toplam değişim (aynı hesap bir kayıtta birden fazla satırda geçebilir).
	type change struct {
		currency money.Currency
		signed   money.Amount
		release  int64
	}
	changes := map[uuid.UUID]*change{}
	for _, l := range e.Lines {
		c, ok := changes[l.AccountID]
		if !ok {
			c = &change{currency: l.Amount.Currency, signed: money.Zero(l.Amount.Currency)}
			changes[l.AccountID] = c
		}
		if c.currency != l.Amount.Currency {
			return uuid.Nil, fmt.Errorf("%w: account %s used with %s and %s", ErrAccount, l.AccountID, c.currency, l.Amount.Currency)
		}
		sum, err := c.signed.Add(money.New(l.signed(), l.Amount.Currency))
		if err != nil {
			return uuid.Nil, fmt.Errorf("%w: %w", ErrInvalidEntry, err)
		}
		c.signed = sum
		c.release += l.ReleaseHold
	}
	accountIDs := make([]uuid.UUID, 0, len(changes))
	for id := range changes {
		accountIDs = append(accountIDs, id)
	}
	slices.SortFunc(accountIDs, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })

	locked, err := lockBalances(ctx, tx, accountIDs)
	if err != nil {
		return uuid.Nil, err
	}
	if len(locked) != len(accountIDs) {
		return uuid.Nil, fmt.Errorf("%w: %d of %d accounts found", ErrAccount, len(locked), len(accountIDs))
	}

	deltas := make([]int64, len(locked))
	releases := make([]int64, len(locked))
	for i, b := range locked {
		c := changes[b.AccountID]
		if b.Currency != c.currency {
			return uuid.Nil, fmt.Errorf("%w: account %s is %s, line is %s", ErrAccount, b.AccountID, b.Currency, c.currency)
		}
		delta := naturalDelta(b.NormalSide, c.signed.Minor)
		next, err := money.New(b.BalanceMinor, b.Currency).Add(money.New(delta, b.Currency))
		if err != nil {
			return uuid.Nil, fmt.Errorf("%w: balance of %s: %w", ErrInvalidEntry, b.AccountID, err)
		}
		if c.release < 0 || c.release > b.HeldMinor {
			return uuid.Nil, fmt.Errorf("%w: release %d exceeds hold %d on %s", ErrInvalidEntry, c.release, b.HeldMinor, b.AccountID)
		}
		if !b.AllowNegative && next.Minor-(b.HeldMinor-c.release) < 0 {
			return uuid.Nil, ErrInsufficientFunds
		}
		releases[i] = c.release
		// locked, accountIDs ile aynı sırada (ikisi de hesap kimliğine göre sıralı).
		deltas[i] = delta
	}

	id := s.ids.NewID()
	effectiveAt, _, err := insertEntry(ctx, tx, id, e, reversalOf)
	if err != nil {
		return uuid.Nil, err
	}
	postingIDs := make([]uuid.UUID, len(e.Lines))
	for i := range postingIDs {
		postingIDs[i] = s.ids.NewID()
	}
	if err := insertPostings(ctx, tx, id, effectiveAt, postingIDs, e.Lines); err != nil {
		return uuid.Nil, err
	}
	lockedIDs := make([]uuid.UUID, len(locked))
	for i, b := range locked {
		lockedIDs[i] = b.AccountID
	}
	if err := applyDeltas(ctx, tx, lockedIDs, deltas, releases); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// Reverse kaydı, satırlarının tersini yazan yeni bir kayıtla düzeltir ve ters kaydın kimliğini döndürür.
// Özgün kayıt değişmez. Bir kayıt yalnız bir kez ters çevrilebilir; ters kayıt yeniden ters çevrilemez.
// Modüller (ör. transfer sagasının telafi adımı) bunu kendi transaction'ları içinde çağırır.
func (s *Service) Reverse(ctx context.Context, tx db.DBTX, id EntryID, in ReverseInput) (EntryID, error) {
	orig, err := getEntry(ctx, tx, id)
	if err != nil {
		return uuid.Nil, err
	}
	if orig.Kind == KindReversal {
		return uuid.Nil, ErrReversalOfReversal
	}
	reversed, err := isReversed(ctx, tx, id)
	if err != nil {
		return uuid.Nil, err
	}
	if reversed {
		return uuid.Nil, ErrAlreadyReversed
	}
	if in.EffectiveAt != nil && in.EffectiveAt.Before(orig.EffectiveAt) {
		return uuid.Nil, ErrInvalidEffectiveAt
	}

	lines := make([]Line, len(orig.Postings))
	for i, p := range orig.Postings {
		// Borç satırı alacak, alacak satırı borç olur; tutar aynı.
		amount := money.New(abs(p.AmountMinor), p.Currency)
		if p.AmountMinor > 0 {
			lines[i] = Credit(p.AccountID, amount)
		} else {
			lines[i] = Debit(p.AccountID, amount)
		}
	}
	ref := ""
	if orig.Reference != nil {
		ref = *orig.Reference
	}
	return s.post(ctx, tx, Entry{
		Kind: KindReversal, Description: "Ters kayıt: " + in.Reason, Reference: ref, EffectiveAt: in.EffectiveAt, Lines: lines,
	}, &orig.ID)
}

// ReverseByRequest, operasyon uzmanının API'den istediği ters kayıttır (POST /ledger/entries/{id}/reverse).
// Idempotency-Key ile korunur: aynı anahtarla tekrar istek kaydı ikinci kez ters çevirmez, ilk sonucu döndürür.
// Yalnız bir sürece ait olmayan kayıtlar elle ters çevrilebilir (Kind.ManuallyReversible). Denetim kaydına yazılır.
func (s *Service) ReverseByRequest(ctx context.Context, p auth.Principal, keyValue string, id EntryID, in ReverseInput) (EntryResponse, bool, error) {
	key, err := idempotency.NewKey(p.UserID, keyValue, "ledger.reverse", struct {
		EntryID EntryID      `json:"entry_id"`
		Input   ReverseInput `json:"input"`
	}{id, in})
	if err != nil {
		return EntryResponse{}, false, err
	}
	return idempotency.Run(ctx, s.tx, key, func(ctx context.Context, tx db.DBTX) (EntryResponse, error) {
		orig, err := getEntry(ctx, tx, id)
		if err != nil {
			return EntryResponse{}, err
		}
		if orig.Kind == KindReversal {
			return EntryResponse{}, ErrReversalOfReversal
		}
		if !orig.Kind.ManuallyReversible() {
			return EntryResponse{}, ErrNotReversible
		}
		revID, err := s.Reverse(ctx, tx, id, in)
		if err != nil {
			return EntryResponse{}, err
		}
		rev, err := getEntry(ctx, tx, revID)
		if err != nil {
			return EntryResponse{}, err
		}
		err = s.audit.Record(ctx, tx, audit.Entry{
			Action: "ledger.reverse", Entity: "journal_entry", EntityID: id.String(),
			Old: orig.ToResponse(), New: rev.ToResponse(),
		})
		return rev.ToResponse(), err
	})
}

// Entry kaydı satırlarıyla getirir (izin: ledger:read; router'da kontrol edilir).
func (s *Service) Entry(ctx context.Context, id EntryID) (JournalEntry, error) {
	return getEntry(ctx, s.tx.DB(), id)
}

// AccountPostings hesabın hareketlerini en yeni önce listeler. Müşteri yalnız kendi hesabının hareketlerini görür
// (hesap modülü başkasının hesabına 403 döner).
func (s *Service) AccountPostings(ctx context.Context, p auth.Principal, accountID uuid.UUID, req page.Request) (page.Page[AccountPostingResponse], error) {
	acc, err := s.accounts.Get(ctx, p, accountID)
	if err != nil {
		return page.Page[AccountPostingResponse]{}, err
	}
	list, err := listAccountPostings(ctx, s.tx.DB(), accountID, req)
	if err != nil {
		return page.Page[AccountPostingResponse]{}, err
	}
	out := page.Page[AccountPostingResponse]{Items: make([]AccountPostingResponse, len(list.Items)), NextCursor: list.NextCursor}
	for i, ap := range list.Items {
		out.Items[i] = ap.ToResponse(acc.NormalSide)
	}
	return out, nil
}

// CheckInvariants defter değişmezlerini ölçer: her para biriminde Σ satırlar = 0, her kayıt dengeli,
// her hesapta özet = defterden türetilen bakiye. Salt okunur, REPEATABLE READ snapshot'ında çalışır.
func (s *Service) CheckInvariants(ctx context.Context) (Invariants, error) {
	var inv Invariants
	err := s.tx.WithTxOptions(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(ctx context.Context, tx db.DBTX) error {
			var err error
			inv, err = checkInvariants(ctx, tx)
			return err
		})
	if err != nil {
		return Invariants{}, err
	}
	inv.CheckedAt = time.Now().UTC()
	inv.OK = inv.UnbalancedEntries == 0 && inv.SummaryMismatches == 0 && (inv.HoldMismatches == nil || *inv.HoldMismatches == 0)
	for _, t := range inv.TotalsByCurrency {
		inv.OK = inv.OK && t.SumMinor == 0
	}
	return inv, nil
}

// LockAccounts verilen hesapların özet satırlarını Post ile aynı sırayla (hesap kimliğine göre) kilitler. Bir modül,
// defter kaydından önce kendi satırını (ör. provizyon) kilitlemek zorundaysa önce bunu çağırır: "önce hesap özetleri
// (sıralı), sonra modülün kendi satırı" kuralı her yerde aynı olduğu için deadlock oluşmaz.
func (s *Service) LockAccounts(ctx context.Context, tx db.DBTX, ids ...uuid.UUID) error {
	sorted := slices.Clone(ids)
	slices.SortFunc(sorted, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	sorted = slices.Compact(sorted)
	locked, err := lockBalances(ctx, tx, sorted)
	if err != nil {
		return err
	}
	if len(locked) != len(sorted) {
		return fmt.Errorf("%w: %d of %d accounts found", ErrAccount, len(locked), len(sorted))
	}
	return nil
}
