package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"parakopru/internal/account"
	"parakopru/internal/customer"
	"parakopru/internal/ledger"
	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/page"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type fixture struct {
	env       *dbtest.Env
	txm       *db.TxManager
	customers *customer.Service
	accounts  *account.Service
	ledger    *ledger.Service
	ops       context.Context // operasyon uzmanı olarak
	opsP      auth.Principal
}

var nationalIDSeq atomic.Int64

// nidBase kimlik numaralarının rastgele başlangıcıdır; sayaçla artar, bu yüzden testler arasında çarpışmaz.
var nidBase = time.Now().UnixNano() % 1e9 * 10

func uniqueNationalID() string {
	return fmt.Sprintf("3%010d", nidBase+nationalIDSeq.Add(1))
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	env := dbtest.Get(t)
	txm := db.NewTxManager(env.App)
	rec := audit.NewRecorder(ids.V7{})
	customers := customer.NewService(txm, ids.V7{}, rec)
	accounts := account.NewService(txm, customers, ids.V7{}, clock.System{}, rec)
	_, err := accounts.EnsureInternalAccounts(audit.WithSystemActor(t.Context()))
	require.NoError(t, err)
	// Idempotency anahtarı gerçek bir kullanıcıya bağlıdır (yabancı anahtar): her test kendi operasyon kullanıcısını açar.
	opsP := auth.Principal{UserID: uuid.New(), Role: auth.RoleOperations}
	_, err = env.App.Exec(t.Context(),
		`INSERT INTO users (id, email, password_hash, role, full_name) VALUES ($1, $2, 'test', 'OPERATIONS', 'Test Operasyon')`,
		opsP.UserID, "ops-"+opsP.UserID.String()+"@parakopru.test")
	require.NoError(t, err)
	return fixture{
		env: env, txm: txm, customers: customers, accounts: accounts,
		ledger: ledger.NewService(txm, ids.V7{}, rec, accounts),
		ops:    auth.WithPrincipal(t.Context(), opsP), opsP: opsP,
	}
}

// customerAccount yeni bir müşteri ve verilen para biriminde vadesiz hesap açar.
func (f fixture) customerAccount(t *testing.T, c money.Currency) account.Account {
	t.Helper()
	cust, err := f.customers.Create(f.ops, customer.CreateInput{FullName: "Defter Test", NationalID: uniqueNationalID()})
	require.NoError(t, err)
	a, err := f.accounts.Open(f.ops, account.OpenInput{CustomerID: cust.ID, Kind: account.Checking, Currency: c})
	require.NoError(t, err)
	return a
}

func (f fixture) internal(t *testing.T, k account.Kind, c money.Currency) uuid.UUID {
	t.Helper()
	id, err := f.accounts.InternalAccountID(t.Context(), nil, k, c)
	require.NoError(t, err)
	return id
}

// post kaydı kendi transaction'ında yazar.
func (f fixture) post(t *testing.T, e ledger.Entry) (ledger.EntryID, error) {
	t.Helper()
	var id ledger.EntryID
	err := f.txm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
		var err error
		id, err = f.ledger.Post(ctx, tx, e)
		return err
	})
	return id, err
}

// fund hesaba açılış bakiyesi yazar: borç OPENING_EQUITY / alacak hesap.
func (f fixture) fund(t *testing.T, a account.Account, minor int64) ledger.EntryID {
	t.Helper()
	amt := money.New(minor, a.Currency)
	id, err := f.post(t, ledger.Entry{
		Kind: ledger.KindOpeningBalance, Description: "Açılış bakiyesi",
		Lines: []ledger.Line{ledger.Debit(f.internal(t, account.OpeningEquity, a.Currency), amt), ledger.Credit(a.ID, amt)},
	})
	require.NoError(t, err)
	return id
}

func (f fixture) balance(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	var b int64
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT balance_minor FROM account_balances WHERE account_id = $1`, id).Scan(&b))
	return b
}

// derived bakiyeyi defterden türetir: Σ satırlar, hesabın doğal tarafına göre işaretli.
func (f fixture) derived(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	var b int64
	require.NoError(t, f.env.App.QueryRow(t.Context(), `
		SELECT coalesce(sum(p.amount_minor), 0) * CASE a.normal_side WHEN 'CREDIT' THEN -1 ELSE 1 END
		  FROM accounts a LEFT JOIN postings p ON p.account_id = a.id
		 WHERE a.id = $1 GROUP BY a.normal_side`, id).Scan(&b))
	return b
}

func (f fixture) requireSummaryMatchesLedger(t *testing.T, ids ...uuid.UUID) {
	t.Helper()
	for _, id := range ids {
		require.Equal(t, f.derived(t, id), f.balance(t, id), "hesap %s: özet = defterden türetilen bakiye", id)
	}
}

func TestPostUpdatesSummaryByNormalSide(t *testing.T) {
	f := newFixture(t)
	cust := f.customerAccount(t, money.TRY)
	equity := f.internal(t, account.OpeningEquity, money.TRY)
	nostro := f.internal(t, account.Nostro, money.TRY)
	clearing := f.internal(t, account.ExternalClearing, money.TRY)
	equityBefore, nostroBefore, clearingBefore := f.balance(t, equity), f.balance(t, nostro), f.balance(t, clearing)

	f.fund(t, cust, 1_000_00)
	require.Equal(t, int64(1_000_00), f.balance(t, cust.ID), "müşteri hesabı (CREDIT) alacaklanınca bakiye artar")
	require.Equal(t, equityBefore-1_000_00, f.balance(t, equity), "açılış özkaynağı (CREDIT) borçlanınca azalır")

	// Dış transfer biçiminde iki kayıt: borç müşteri / alacak takas, sonra borç takas / alacak nostro.
	amt := money.New(300_00, money.TRY)
	_, err := f.post(t, ledger.Entry{Kind: "TEST_TRANSFER", Description: "rezerv", Lines: []ledger.Line{ledger.Debit(cust.ID, amt), ledger.Credit(clearing, amt)}})
	require.NoError(t, err)
	_, err = f.post(t, ledger.Entry{Kind: "TEST_TRANSFER", Description: "gönderildi", Lines: []ledger.Line{ledger.Debit(clearing, amt), ledger.Credit(nostro, amt)}})
	require.NoError(t, err)

	require.Equal(t, int64(700_00), f.balance(t, cust.ID))
	require.Equal(t, clearingBefore, f.balance(t, clearing), "takas hesabı sıfıra döner")
	require.Equal(t, nostroBefore-300_00, f.balance(t, nostro), "nostro (DEBIT) alacaklanınca azalır: para muhabir bankadan çıktı")
	f.requireSummaryMatchesLedger(t, cust.ID, equity, nostro, clearing)
}

func TestInsufficientFundsWritesNothing(t *testing.T) {
	f := newFixture(t)
	from, to := f.customerAccount(t, money.TRY), f.customerAccount(t, money.TRY)
	f.fund(t, from, 100_00)

	amt := money.New(100_01, money.TRY)
	_, err := f.post(t, ledger.Entry{Kind: "TEST_TRANSFER", Description: "fazla", Lines: []ledger.Line{ledger.Debit(from.ID, amt), ledger.Credit(to.ID, amt)}})
	require.ErrorIs(t, err, ledger.ErrInsufficientFunds)
	require.ErrorIs(t, err, apperr.New(0, "INSUFFICIENT_FUNDS", ""), "istemciye 422 INSUFFICIENT_FUNDS")

	require.Equal(t, int64(100_00), f.balance(t, from.ID))
	require.Zero(t, f.balance(t, to.ID))
	var n int
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT count(*) FROM postings WHERE account_id = $1`, to.ID).Scan(&n))
	require.Zero(t, n, "reddedilen kaydın satırı kalmamalı")
}

func TestPostRejectsBadEntriesBeforeWriting(t *testing.T) {
	f := newFixture(t)
	tryAcc, usdAcc := f.customerAccount(t, money.TRY), f.customerAccount(t, money.USD)
	f.fund(t, tryAcc, 10_00)

	_, err := f.post(t, ledger.Entry{Kind: "TEST", Description: "d", Lines: []ledger.Line{
		ledger.Debit(tryAcc.ID, money.New(5_00, money.TRY)), ledger.Credit(usdAcc.ID, money.New(5_00, money.TRY))}})
	require.ErrorIs(t, err, ledger.ErrAccount, "TRY satırı USD hesabına yazılamaz")

	_, err = f.post(t, ledger.Entry{Kind: "TEST", Description: "d", Lines: []ledger.Line{
		ledger.Debit(tryAcc.ID, money.New(5_00, money.TRY)), ledger.Credit(uuid.New(), money.New(5_00, money.TRY))}})
	require.ErrorIs(t, err, ledger.ErrAccount, "olmayan hesap")

	_, err = f.post(t, ledger.Entry{Kind: "TEST", Description: "d", Lines: []ledger.Line{
		ledger.Debit(tryAcc.ID, money.New(5_00, money.TRY)), ledger.Credit(usdAcc.ID, money.New(4_00, money.TRY))}})
	require.ErrorIs(t, err, ledger.ErrUnbalanced)
	require.Equal(t, int64(10_00), f.balance(t, tryAcc.ID))
}

// Veritabanı güvencesi: uygulama atlanıp doğrudan SQL yazılsa bile dengesiz kayıt commit edilemez.
func TestDatabaseRejectsUnbalancedEntry(t *testing.T) {
	f := newFixture(t)
	a, b := f.customerAccount(t, money.TRY), f.customerAccount(t, money.TRY)
	ctx := t.Context()

	// commit, ifadeleri tek transaction'da çalıştırıp commit eder; deferred trigger'ın hatası commit'te döner.
	commit := func(stmts ...string) error {
		return pgx.BeginTxFunc(ctx, f.env.App, pgx.TxOptions{}, func(tx pgx.Tx) error {
			for _, s := range stmts {
				if _, err := tx.Exec(ctx, s); err != nil {
					return err
				}
			}
			return nil
		})
	}
	entry := func(id uuid.UUID) string {
		return fmt.Sprintf(`INSERT INTO journal_entries (id, kind, description, effective_at) VALUES ('%s', 'TEST', 'sql', now())`, id)
	}
	posting := func(entry, acc uuid.UUID, amount int64) string {
		return fmt.Sprintf(`INSERT INTO postings (id, entry_id, account_id, currency, amount_minor, effective_at)
			SELECT gen_random_uuid(), id, '%s', 'TRY', %d, effective_at FROM journal_entries WHERE id = '%s'`, acc, amount, entry)
	}

	e1 := uuid.New()
	err := commit(entry(e1), posting(e1, a.ID, 100), posting(e1, b.ID, -99))
	require.True(t, db.IsCheckViolation(err, "journal_entries_balanced"), "borç ≠ alacak: %v", err)

	e2 := uuid.New()
	err = commit(entry(e2))
	require.True(t, db.IsCheckViolation(err, "journal_entries_min_two_postings"), "satırsız kayıt: %v", err)

	e3 := uuid.New()
	require.NoError(t, commit(entry(e3), posting(e3, a.ID, 100), posting(e3, b.ID, -100)), "dengeli kayıt geçer")

	// Commit edilmiş kayda sonradan (dengeli de olsa) satır eklenemez: satırın kayıt zamanı kaydınkiyle tutmaz.
	err = commit(posting(e3, a.ID, 5), posting(e3, b.ID, -5))
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23503", pgErr.Code, "postings_entry_fkey: %v", err)

	// Satırın para birimi hesabınkiyle aynı olmalı.
	usd := f.customerAccount(t, money.USD)
	e4 := uuid.New()
	err = commit(entry(e4), posting(e4, usd.ID, 100), posting(e4, b.ID, -100))
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "postings_account_currency_fkey", pgErr.ConstraintName)
}

// Defter satırları silinmez ve güncellenmez (proje.md Görev 7): uygulama rolünün yetkisi yok, sahip rol bile
// trigger'a takılır.
func TestLedgerIsAppendOnly(t *testing.T) {
	f := newFixture(t)
	a := f.customerAccount(t, money.EUR)
	entryID := f.fund(t, a, 5_00)

	for _, table := range []string{"journal_entries", "postings"} {
		col := "entry_id"
		if table == "journal_entries" {
			col = "id"
		}
		for _, stmt := range []string{
			fmt.Sprintf(`UPDATE %s SET effective_at = effective_at WHERE %s = '%s'`, table, col, entryID),
			fmt.Sprintf(`DELETE FROM %s WHERE %s = '%s'`, table, col, entryID),
			`TRUNCATE ` + table + ` CASCADE`,
		} {
			var pgErr *pgconn.PgError
			_, err := f.env.App.Exec(t.Context(), stmt)
			require.ErrorAs(t, err, &pgErr, stmt)
			require.Equal(t, "42501", pgErr.Code, "uygulama rolü: yetki yok: %s", stmt)

			_, err = f.env.Owner.Exec(t.Context(), stmt)
			require.ErrorAs(t, err, &pgErr, stmt)
			require.Equal(t, "23001", pgErr.Code, "sahip rol: trigger reddeder: %s", stmt)
		}
	}
	require.Equal(t, int64(5_00), f.derived(t, a.ID))
}

// Kayıt zamanını (recorded_at) uygulama ya da istemci belirleyemez; ileri tarihli kayıt yazılamaz.
func TestRecordedAtComesFromDatabase(t *testing.T) {
	f := newFixture(t)
	a := f.customerAccount(t, money.TRY)
	before := time.Now()
	id := f.fund(t, a, 1_00)

	e, err := f.ledger.Entry(t.Context(), id)
	require.NoError(t, err)
	require.WithinDuration(t, before, e.RecordedAt, 5*time.Second)
	require.True(t, e.EffectiveAt.Equal(e.RecordedAt), "geçerlilik zamanı verilmezse kayıt anı")

	// Elle verilen recorded_at yok sayılır (kayıt satırsız olduğu için transaction geri alınır).
	var recorded time.Time
	errRollback := errors.New("rollback")
	err = pgx.BeginTxFunc(t.Context(), f.env.App, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `
			INSERT INTO journal_entries (id, kind, description, effective_at, recorded_at)
			VALUES (gen_random_uuid(), 'TEST', 'x', '2000-01-01', '2000-01-01') RETURNING recorded_at`).Scan(&recorded); err != nil {
			return err
		}
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	require.WithinDuration(t, time.Now(), recorded, 5*time.Second)

	// Geriye tarihli kayıt olur; ileri tarihli olmaz.
	past := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Microsecond)
	amt := money.New(1, money.TRY)
	equity := f.internal(t, account.OpeningEquity, money.TRY)
	backdated, err := f.post(t, ledger.Entry{Kind: ledger.KindOpeningBalance, Description: "geriye tarihli", EffectiveAt: &past,
		Lines: []ledger.Line{ledger.Debit(equity, amt), ledger.Credit(a.ID, amt)}})
	require.NoError(t, err)
	e, err = f.ledger.Entry(t.Context(), backdated)
	require.NoError(t, err)
	require.True(t, e.EffectiveAt.Equal(past))

	future := time.Now().Add(time.Hour)
	_, err = f.post(t, ledger.Entry{Kind: ledger.KindOpeningBalance, Description: "ileri tarihli", EffectiveAt: &future,
		Lines: []ledger.Line{ledger.Debit(equity, amt), ledger.Credit(a.ID, amt)}})
	require.ErrorIs(t, err, ledger.ErrInvalidEffectiveAt)
}

func TestReverse(t *testing.T) {
	f := newFixture(t)
	a := f.customerAccount(t, money.USD)
	equity := f.internal(t, account.OpeningEquity, money.USD)
	equityBefore := f.balance(t, equity)
	orig := f.fund(t, a, 250_00)

	var revID ledger.EntryID
	require.NoError(t, f.txm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
		var err error
		revID, err = f.ledger.Reverse(ctx, tx, orig, ledger.ReverseInput{Reason: "hatalı açılış"})
		return err
	}))
	require.Zero(t, f.balance(t, a.ID), "ters kayıt bakiyeyi geri alır")
	require.Equal(t, equityBefore, f.balance(t, equity))
	f.requireSummaryMatchesLedger(t, a.ID, equity)

	o, err := f.ledger.Entry(t.Context(), orig)
	require.NoError(t, err)
	r, err := f.ledger.Entry(t.Context(), revID)
	require.NoError(t, err)
	require.Equal(t, ledger.KindReversal, r.Kind)
	require.Equal(t, &orig, r.ReversalOf)
	require.Equal(t, &revID, o.ReversedBy, "özgün kayıt silinmedi; kimin ters çevirdiği görünür")
	require.Len(t, o.Postings, 2, "özgün satırlar yerinde")
	sum := map[uuid.UUID]int64{}
	for _, p := range append(o.Postings, r.Postings...) {
		sum[p.AccountID] += p.AmountMinor
	}
	for acc, s := range sum {
		require.Zero(t, s, "hesap %s: özgün + ters = 0", acc)
	}

	reverse := func(id ledger.EntryID, in ledger.ReverseInput) error {
		return f.txm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
			_, err := f.ledger.Reverse(ctx, tx, id, in)
			return err
		})
	}
	require.ErrorIs(t, reverse(orig, ledger.ReverseInput{Reason: "ikinci kez"}), ledger.ErrAlreadyReversed)
	require.ErrorIs(t, reverse(revID, ledger.ReverseInput{Reason: "tersin tersi"}), ledger.ErrNotReversible)
	require.ErrorIs(t, reverse(uuid.New(), ledger.ReverseInput{Reason: "yok"}), ledger.ErrEntryNotFound)

	other := f.fund(t, a, 10_00)
	tooEarly := time.Now().Add(-time.Hour)
	require.ErrorIs(t, reverse(other, ledger.ReverseInput{Reason: "erken", EffectiveAt: &tooEarly}), ledger.ErrInvalidEffectiveAt,
		"ters kayıt özgün kayıttan önce geçerli olamaz")
}

// Aynı kaydı eşzamanlı ters çevirme: yalnız biri başarılı olur (UNIQUE reversal_of), para iki kez dönmez.
func TestConcurrentReversalsOnlyOneWins(t *testing.T) {
	f := newFixture(t)
	a := f.customerAccount(t, money.TRY)
	orig := f.fund(t, a, 77_00)

	var ok, already atomic.Int32
	var g errgroup.Group
	for range 10 {
		g.Go(func() error {
			err := f.txm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
				_, err := f.ledger.Reverse(ctx, tx, orig, ledger.ReverseInput{Reason: "yarış"})
				return err
			})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ledger.ErrAlreadyReversed):
				already.Add(1)
			default:
				return err
			}
			return nil
		})
	}
	require.NoError(t, g.Wait())
	require.Equal(t, int32(1), ok.Load())
	require.Equal(t, int32(9), already.Load())
	require.Zero(t, f.balance(t, a.ID))
	f.requireSummaryMatchesLedger(t, a.ID)
}

// Kilit sırası: ters yönlü eşzamanlı kayıtlar deadlock'a girmez. Transaction'lar yeniden deneme yapan TxManager
// olmadan, doğrudan açılır; tek bir deadlock (40P01) bile testi düşürür. Para korunur, özet = defter.
func TestOppositeConcurrentPostingsDoNotDeadlock(t *testing.T) {
	f := newFixture(t)
	accs := make([]account.Account, 4)
	for i := range accs {
		accs[i] = f.customerAccount(t, money.TRY)
		f.fund(t, accs[i], 1_000_000_00)
	}

	const workers, perWorker = 16, 25
	var g errgroup.Group
	for w := range workers {
		g.Go(func() error {
			rng := rand.New(rand.NewPCG(uint64(w), 42)) //nolint:gosec // test verisi
			for range perWorker {
				i, j := rng.IntN(len(accs)), rng.IntN(len(accs)-1)
				if j >= i {
					j++
				}
				amt := money.New(int64(1+rng.IntN(10_000)), money.TRY)
				err := pgx.BeginTxFunc(t.Context(), f.env.App, pgx.TxOptions{}, func(tx pgx.Tx) error {
					_, err := f.ledger.Post(t.Context(), tx, ledger.Entry{Kind: "TEST_TRANSFER", Description: "yarış",
						Lines: []ledger.Line{ledger.Debit(accs[i].ID, amt), ledger.Credit(accs[j].ID, amt)}})
					return err
				})
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	require.NoError(t, g.Wait(), "deadlock ya da başka bir hata olmamalı")

	var total int64
	for _, a := range accs {
		total += f.balance(t, a.ID)
		f.requireSummaryMatchesLedger(t, a.ID)
	}
	require.Equal(t, int64(4*1_000_000_00), total, "müşteri hesapları arasında para yoktan var olmaz, kaybolmaz")
}

func TestReverseByRequest(t *testing.T) {
	f := newFixture(t)
	a := f.customerAccount(t, money.EUR)
	orig := f.fund(t, a, 40_00)

	res, replayed, err := f.ledger.ReverseByRequest(f.ops, f.opsP, "rev-1", orig, ledger.ReverseInput{Reason: "yanlış tutar"})
	require.NoError(t, err)
	require.False(t, replayed)
	require.Equal(t, ledger.KindReversal, res.Kind)
	require.Equal(t, "Ters kayıt: yanlış tutar", res.Description)
	require.Zero(t, f.balance(t, a.ID))

	// Aynı anahtar: aynı sonuç, ikinci ters kayıt yok.
	again, replayed, err := f.ledger.ReverseByRequest(f.ops, f.opsP, "rev-1", orig, ledger.ReverseInput{Reason: "yanlış tutar"})
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, res.ID, again.ID)

	// Aynı anahtar, farklı istek → 422.
	_, _, err = f.ledger.ReverseByRequest(f.ops, f.opsP, "rev-1", orig, ledger.ReverseInput{Reason: "başka gerekçe"})
	require.ErrorIs(t, err, apperr.New(0, "IDEMPOTENCY_KEY_REUSED", ""))

	// Yeni anahtarla aynı kayıt → 409 ALREADY_REVERSED.
	_, _, err = f.ledger.ReverseByRequest(f.ops, f.opsP, "rev-2", orig, ledger.ReverseInput{Reason: "yanlış tutar"})
	require.ErrorIs(t, err, ledger.ErrAlreadyReversed)

	// Bir sürece ait kayıt elle ters çevrilemez.
	b := f.customerAccount(t, money.EUR)
	f.fund(t, b, 10_00)
	amt := money.New(1_00, money.EUR)
	transferEntry, err := f.post(t, ledger.Entry{Kind: "TRANSFER", Description: "iç transfer",
		Lines: []ledger.Line{ledger.Debit(b.ID, amt), ledger.Credit(a.ID, amt)}})
	require.NoError(t, err)
	_, _, err = f.ledger.ReverseByRequest(f.ops, f.opsP, "rev-3", transferEntry, ledger.ReverseInput{Reason: "olmaz"})
	require.ErrorIs(t, err, ledger.ErrNotReversible)

	// Denetim kaydı: kim, neyi, eski ve yeni değer.
	p, err := audit.NewService(f.env.App).List(t.Context(), audit.Filter{Entity: "journal_entry", EntityID: orig.String()}, page.Request{Limit: 10})
	require.NoError(t, err)
	require.Len(t, p.Items, 1)
	require.Equal(t, "ledger.reverse", p.Items[0].Action)
	require.Equal(t, &f.opsP.UserID, p.Items[0].ActorID)
	require.Contains(t, string(p.Items[0].NewValue), res.ID.String())
}

// Hesap hareketleri: en yeni önce, keyset sayfalı; müşteri yalnız kendi hesabını görür.
func TestAccountPostings(t *testing.T) {
	f := newFixture(t)
	_, err := auth.NewService(f.txm, auth.Repo{}, auth.NewTokens(strings.Repeat("x", 32), time.Minute, time.Hour, clock.System{}), ids.V7{}, clock.System{}).
		SeedDemoUsers(t.Context(), "Demo.Parola.1")
	require.NoError(t, err)
	var userID uuid.UUID
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT id FROM users WHERE email = 'musteri@parakopru.test'`).Scan(&userID))
	var mine customer.Customer
	require.NoError(t, f.txm.WithTx(audit.WithSystemActor(t.Context()), func(ctx context.Context, tx db.DBTX) error {
		var err error
		mine, _, err = f.customers.EnsureForUser(ctx, tx, userID, customer.CreateInput{FullName: "Ben", NationalID: uniqueNationalID()})
		return err
	}))
	myAcc, err := f.accounts.Open(f.ops, account.OpenInput{CustomerID: mine.ID, Kind: account.Checking, Currency: money.USD})
	require.NoError(t, err)
	other := f.customerAccount(t, money.USD)

	for i := range 5 {
		f.fund(t, myAcc, int64(i+1)*100)
	}
	amt := money.New(50, money.USD)
	_, err = f.post(t, ledger.Entry{Kind: "TEST_TRANSFER", Description: "giden", Lines: []ledger.Line{ledger.Debit(myAcc.ID, amt), ledger.Credit(other.ID, amt)}})
	require.NoError(t, err)

	me := auth.Principal{UserID: userID, Role: auth.RoleCustomer}
	req := page.Request{Limit: 4}
	var items []ledger.AccountPostingResponse
	for {
		p, err := f.ledger.AccountPostings(t.Context(), me, myAcc.ID, ledger.PostingFilter{}, req)
		require.NoError(t, err)
		items = append(items, p.Items...)
		if p.NextCursor == nil {
			break
		}
		req.Cursor = *p.NextCursor
	}
	require.Len(t, items, 6)
	require.Equal(t, "giden", items[0].Description, "en yeni önce")
	require.Equal(t, int64(-50), items[0].BalanceEffectMinor, "müşteri hesabından çıkış")
	require.Equal(t, account.Debit, items[0].Side)
	var sum int64
	for i, it := range items {
		sum += it.BalanceEffectMinor
		if i > 0 {
			require.False(t, it.EffectiveAt.After(items[i-1].EffectiveAt), "sıra bozuk")
		}
	}
	require.Equal(t, f.balance(t, myAcc.ID), sum, "hareketlerin toplamı bakiye")

	_, err = f.ledger.AccountPostings(t.Context(), me, other.ID, ledger.PostingFilter{}, page.Request{Limit: 10})
	require.ErrorIs(t, err, apperr.ErrForbidden, "başka müşterinin hareketleri 403")

	auditor := auth.Principal{UserID: uuid.New(), Role: auth.RoleAuditor}
	p, err := f.ledger.AccountPostings(t.Context(), auditor, other.ID, ledger.PostingFilter{}, page.Request{Limit: 10})
	require.NoError(t, err)
	require.Len(t, p.Items, 1)
	require.Equal(t, int64(50), p.Items[0].BalanceEffectMinor)
}

func TestHandlers(t *testing.T) {
	f := newFixture(t)
	a := f.customerAccount(t, money.TRY)
	orig := f.fund(t, a, 12_34)

	h := ledger.NewHandler(f.ledger)
	r := chi.NewRouter()
	r.Get("/accounts/{id}/entries", h.AccountEntries)
	r.Get("/ledger/entries/{id}", h.Entry)
	r.Post("/ledger/entries/{id}/reverse", h.Reverse)
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(f.ops, method, path, strings.NewReader(body))
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	rec := call(http.MethodGet, "/ledger/entries/"+orig.String(), "", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var e ledger.EntryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e))
	require.Len(t, e.Postings, 2)
	require.Equal(t, int64(12_34), e.Postings[0].AmountMinor)

	require.Equal(t, http.StatusNotFound, call(http.MethodGet, "/ledger/entries/"+uuid.NewString(), "", "").Code)
	require.Equal(t, http.StatusBadRequest, call(http.MethodGet, "/ledger/entries/bozuk", "", "").Code)

	rec = call(http.MethodGet, "/accounts/"+a.ID.String()+"/entries?limit=10", "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"balance_effect_minor":1234`)

	path := "/ledger/entries/" + orig.String() + "/reverse"
	body := `{"reason":"deneme"}`
	rec = call(http.MethodPost, path, "", body)
	require.Equal(t, http.StatusBadRequest, rec.Code, "Idempotency-Key yoksa 400")
	require.Contains(t, rec.Body.String(), "IDEMPOTENCY_KEY_REQUIRED")

	rec = call(http.MethodPost, path, "h-1", `{"reason":""}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "gerekçe zorunlu")

	rec = call(http.MethodPost, path, "h-1", body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	first := rec.Body.String()
	rec = call(http.MethodPost, path, "h-1", body)
	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, "true", rec.Header().Get("Idempotent-Replayed"))
	require.JSONEq(t, first, rec.Body.String(), "aynı anahtar aynı yanıt")

	rec = call(http.MethodPost, path, "h-2", body)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "ALREADY_REVERSED")
}

// postAt belirli geçerlilik zamanıyla bir açılış kaydı yazar ve kaydın kayıt zamanını (veritabanı saati) döndürür.
func (f fixture) postAt(t *testing.T, a account.Account, minor int64, effective time.Time) time.Time {
	t.Helper()
	amt := money.New(minor, a.Currency)
	id, err := f.post(t, ledger.Entry{Kind: ledger.KindOpeningBalance, Description: "zaman yolculuğu", EffectiveAt: &effective,
		Lines: []ledger.Line{ledger.Debit(f.internal(t, account.OpeningEquity, a.Currency), amt), ledger.Credit(a.ID, amt)}})
	require.NoError(t, err)
	e, err := f.ledger.Entry(t.Context(), id)
	require.NoError(t, err)
	time.Sleep(30 * time.Millisecond) // kayıt zamanları birbirinden ayrışsın
	return e.RecordedAt
}

// T-7: "işlem zamanı ile geçerlilik zamanı ayrımı". Geriye tarihli düzeltme yeni kayıttır (effective_at geçmişte,
// recorded_at şimdi): aynı as_of için known_at'e göre farklı bakiye çıkar.
func TestTimeTravelBalanceAsOfAndKnownAt(t *testing.T) {
	f := newFixture(t)
	a := f.customerAccount(t, money.TRY)
	ops := auth.Principal{UserID: uuid.New(), Role: auth.RoleOperations}
	now := time.Now().UTC().Truncate(time.Microsecond)

	rBefore := time.Now().UTC().Add(-time.Second)
	rA := f.postAt(t, a, 100_00, now.Add(-48*time.Hour)) // 2 gün önce, o gün kaydedilmiş gibi (ilk bilinen)
	rB := f.postAt(t, a, 50_00, now)                     // şimdi
	rC := f.postAt(t, a, 25_00, now.Add(-24*time.Hour))  // geriye tarihli düzeltme: dün geçerli, şimdi kaydedildi

	bal := func(asOf, knownAt *time.Time) int64 {
		t.Helper()
		r, err := f.ledger.AccountBalance(t.Context(), ops, a.ID, asOf, knownAt)
		require.NoError(t, err)
		return r.BalanceMinor
	}
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	require.Equal(t, int64(175_00), bal(nil, nil), "güncel bakiye")
	require.Equal(t, int64(100_00), bal(at(-36*time.Hour), nil), "36 saat önce: yalnız ilk kayıt geçerliydi")
	require.Equal(t, int64(125_00), bal(at(-12*time.Hour), nil), "12 saat önce: ilk kayıt + geriye tarihli düzeltme")
	require.Equal(t, int64(0), bal(at(-72*time.Hour), nil), "hesap açılmadan önce")

	// Aynı as_of, farklı known_at: düzeltme kaydedilmeden önce bildiğimiz bakiye farklıydı.
	require.Equal(t, int64(100_00), bal(at(-12*time.Hour), &rB), "düzeltmeden önceki bilgiyle")
	require.Equal(t, int64(125_00), bal(at(-12*time.Hour), &rC), "düzeltmeden sonraki bilgiyle")
	// Aynı known_at'te as_of ilerledikçe şimdi kaydı görünür.
	require.Equal(t, int64(100_00), bal(nil, &rA))
	require.Equal(t, int64(150_00), bal(nil, &rB))
	require.Equal(t, int64(175_00), bal(nil, &rC))
	require.Equal(t, int64(0), bal(nil, &rBefore), "hiçbir şey kaydedilmeden önce")
	// Defter hâlâ dengede: geçmişe bakmak veriyi değiştirmez.
	f.requireSummaryMatchesLedger(t, a.ID)

	// Hareketler aynı parametrelerle.
	list := func(f2 ledger.PostingFilter) []int64 {
		t.Helper()
		p, err := f.ledger.AccountPostings(t.Context(), ops, a.ID, f2, page.Request{Limit: 50})
		require.NoError(t, err)
		var out []int64
		for _, it := range p.Items {
			out = append(out, it.AmountMinor)
		}
		return out
	}
	require.ElementsMatch(t, []int64{50_00, 100_00, 25_00}, list(ledger.PostingFilter{}))
	require.ElementsMatch(t, []int64{100_00, 25_00}, list(ledger.PostingFilter{To: at(-12 * time.Hour)}))
	require.ElementsMatch(t, []int64{100_00}, list(ledger.PostingFilter{To: at(-12 * time.Hour), KnownAt: &rB}), "düzeltme henüz bilinmiyordu")
	require.ElementsMatch(t, []int64{25_00, 50_00}, list(ledger.PostingFilter{From: at(-30 * time.Hour)}))
	require.Equal(t, []int64{50_00, 25_00}, list(ledger.PostingFilter{From: at(-30 * time.Hour)}), "geçerlilik zamanına göre en yeni önce")
}

// Zaman yolculuğu yetkisi ledger:read sahiplerinindir; müşteri yalnız güncel bakiyesini görür (PLAN.md §5.10).
func TestTimeTravelPermissions(t *testing.T) {
	f := newFixture(t)
	owner := uuid.New()
	_, err := f.env.App.Exec(t.Context(), `INSERT INTO users (id, email, password_hash, role, full_name) VALUES ($1, $2, 'x', 'CUSTOMER', 'Ben')`, owner, owner.String()+"@parakopru.test")
	require.NoError(t, err)
	var mine customer.Customer
	require.NoError(t, f.txm.WithTx(audit.WithSystemActor(t.Context()), func(ctx context.Context, tx db.DBTX) error {
		var err error
		mine, _, err = f.customers.EnsureForUser(ctx, tx, owner, customer.CreateInput{FullName: "Ben", NationalID: uniqueNationalID()})
		return err
	}))
	a, err := f.accounts.Open(f.ops, account.OpenInput{CustomerID: mine.ID, Kind: account.Checking, Currency: money.TRY})
	require.NoError(t, err)
	f.fund(t, a, 10_00)

	me := auth.Principal{UserID: owner, Role: auth.RoleCustomer}
	now := time.Now()
	got, err := f.ledger.AccountBalance(t.Context(), me, a.ID, nil, nil)
	require.NoError(t, err)
	require.Equal(t, int64(10_00), got.BalanceMinor)
	_, err = f.ledger.AccountBalance(t.Context(), me, a.ID, &now, nil)
	require.ErrorIs(t, err, apperr.ErrForbidden)
	_, err = f.ledger.AccountBalance(t.Context(), me, a.ID, nil, &now)
	require.ErrorIs(t, err, apperr.ErrForbidden)
	_, err = f.ledger.AccountPostings(t.Context(), me, a.ID, ledger.PostingFilter{KnownAt: &now}, page.Request{Limit: 5})
	require.ErrorIs(t, err, apperr.ErrForbidden)
	_, err = f.ledger.AccountPostings(t.Context(), me, a.ID, ledger.PostingFilter{To: &now}, page.Request{Limit: 5})
	require.NoError(t, err, "ekstre aralığı (from/to) müşteriye açık")

	other := f.customerAccount(t, money.TRY)
	_, err = f.ledger.AccountBalance(t.Context(), me, other.ID, nil, nil)
	require.ErrorIs(t, err, apperr.ErrForbidden, "başkasının bakiyesi")
	auditor := auth.Principal{UserID: uuid.New(), Role: auth.RoleAuditor}
	_, err = f.ledger.AccountBalance(t.Context(), auditor, a.ID, &now, &now)
	require.NoError(t, err, "denetçi zaman yolculuğu yapabilir")
}

func TestBalanceAndEntriesHandlerParameters(t *testing.T) {
	f := newFixture(t)
	a := f.customerAccount(t, money.USD)
	f.fund(t, a, 5_00)
	h := ledger.NewHandler(f.ledger)
	r := chi.NewRouter()
	r.Get("/accounts/{id}/balance", h.Balance)
	r.Get("/accounts/{id}/entries", h.AccountEntries)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(f.ops, http.MethodGet, path, nil))
		return rec
	}
	id := a.ID.String()
	rec := get("/accounts/" + id + "/balance")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"balance_minor":500`)
	require.Contains(t, rec.Body.String(), `"as_of":null`)

	today := time.Now().UTC().Format(time.DateOnly)
	rec = get("/accounts/" + id + "/balance?as_of=" + today)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"balance_minor":500`, "yalnız tarih: o günün sonu")
	rec = get("/accounts/" + id + "/balance?as_of=2020-01-01&known_at=2020-01-01T00:00:00Z")
	require.Contains(t, rec.Body.String(), `"balance_minor":0`)
	require.Equal(t, http.StatusBadRequest, get("/accounts/"+id+"/balance?as_of=dün").Code)
	require.Equal(t, http.StatusBadRequest, get("/accounts/"+id+"/entries?known_at=x").Code)
	rec = get("/accounts/" + id + "/entries?to=2020-01-01")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"items":[]`)
	rec = get("/accounts/" + id + "/entries?as_of=" + today + "&from=" + today)
	require.Contains(t, rec.Body.String(), `"balance_effect_minor":500`)
}
