package idempotency_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
	"parakopru/internal/platform/idempotency"
	"parakopru/internal/platform/ids"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestFromRequest(t *testing.T) {
	get := func(v string) (string, error) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
		if v != "" {
			r.Header.Set(idempotency.HeaderName, v)
		}
		return idempotency.FromRequest(r)
	}

	v, err := get("01HZX-transfer-42")
	require.NoError(t, err)
	require.Equal(t, "01HZX-transfer-42", v)

	_, err = get("")
	require.ErrorIs(t, err, idempotency.ErrKeyRequired)

	for _, bad := range []string{"boşluk var", "türkçe-ğ", strings.Repeat("a", 256)} {
		_, err = get(bad)
		require.ErrorIs(t, err, idempotency.ErrKeyInvalid, bad)
	}
}

// --- Testlerde idempotent korunan örnek işlem: bir sayaç tablosuna "depozito" ekler ---

type deposit struct {
	Account string `json:"account"`
	Amount  int64  `json:"amount_minor"`
}

type depositResult struct {
	ID      uuid.UUID `json:"id"`
	Balance int64     `json:"balance_minor"`
}

var errLimitExceeded = apperr.New(http.StatusUnprocessableEntity, "LIMIT_EXCEEDED", "Günlük limit aşıldı.")

type fixture struct {
	tm      *db.TxManager
	userID  uuid.UUID
	table   string
	runs    atomic.Int32 // işlemin gerçekten kaç kez çalıştığı
	env     *dbtest.Env
	failNow atomic.Bool // true ise işlem beklenmeyen (5xx) hata verir
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	env := dbtest.Get(t)
	ctx := t.Context()

	// idempotency_keys.user_id gerçek bir kullanıcıya bağlı (FK).
	authSvc := auth.NewService(db.NewTxManager(env.App), auth.Repo{}, auth.NewTokens(strings.Repeat("s", 32), 1, 2, clock.System{}), ids.V7{}, clock.System{})
	_, err := authSvc.SeedDemoUsers(ctx, "Demo.Parola.1")
	require.NoError(t, err)
	var userID uuid.UUID
	require.NoError(t, env.App.QueryRow(ctx, `SELECT id FROM users WHERE email = 'operasyon@parakopru.test'`).Scan(&userID))

	table := "deposits_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	_, err = env.Owner.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s (account text PRIMARY KEY, balance bigint NOT NULL)`, table))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = env.Owner.Exec(context.Background(), "DROP TABLE "+table) })

	return &fixture{tm: db.NewTxManager(env.App), userID: userID, table: table, env: env}
}

// do, PLAN.md'deki akışın aynısıdır: handler başlıktan anahtarı alır, servis işlemi idempotency.Run içinde yapar.
func (f *fixture) do(t *testing.T, key string, in deposit) (depositResult, bool, error) {
	t.Helper()
	return f.doAs(t, f.userID, key, in)
}

func (f *fixture) doAs(t *testing.T, userID uuid.UUID, key string, in deposit) (depositResult, bool, error) {
	t.Helper()
	k, err := idempotency.NewKey(userID, key, "deposit.create", in)
	require.NoError(t, err)
	return idempotency.Run(t.Context(), f.tm, k, func(ctx context.Context, tx db.DBTX) (depositResult, error) {
		f.runs.Add(1)
		var bal int64
		err := tx.QueryRow(ctx, fmt.Sprintf(
			`INSERT INTO %s (account, balance) VALUES ($1, $2)
			 ON CONFLICT (account) DO UPDATE SET balance = %s.balance + EXCLUDED.balance
			 RETURNING balance`, f.table, f.table), in.Account, in.Amount).Scan(&bal)
		if err != nil {
			return depositResult{}, err
		}
		if f.failNow.Load() {
			return depositResult{}, fmt.Errorf("beklenmeyen altyapı hatası")
		}
		if bal > 1_000_000 { // iş kuralı: yazma yapıldıktan sonra reddedilen işlem
			return depositResult{}, errLimitExceeded
		}
		return depositResult{ID: uuid.New(), Balance: bal}, nil
	})
}

func (f *fixture) balance(t *testing.T, account string) int64 {
	t.Helper()
	var b int64
	err := f.env.App.QueryRow(t.Context(), fmt.Sprintf(`SELECT coalesce(sum(balance), 0) FROM %s WHERE account = $1`, f.table), account).Scan(&b)
	require.NoError(t, err)
	return b
}

func TestSameKeyReturnsSameResultWithoutRepeating(t *testing.T) {
	f := newFixture(t)
	in := deposit{Account: "A", Amount: 500}

	first, replayed, err := f.do(t, "key-1", in)
	require.NoError(t, err)
	require.False(t, replayed)

	second, replayed, err := f.do(t, "key-1", in)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, first, second, "aynı anahtar aynı sonucu döndürmeli")
	require.Equal(t, int32(1), f.runs.Load(), "işlem yalnız bir kez yapılmalı")
	require.Equal(t, int64(500), f.balance(t, "A"))
}

func TestSameKeyDifferentRequestIsRejected(t *testing.T) {
	f := newFixture(t)
	_, _, err := f.do(t, "key-2", deposit{Account: "B", Amount: 100})
	require.NoError(t, err)

	_, _, err = f.do(t, "key-2", deposit{Account: "B", Amount: 999})
	require.ErrorIs(t, err, idempotency.ErrKeyReused)
	require.Equal(t, int64(100), f.balance(t, "B"))
}

func TestKeysAreScopedPerUser(t *testing.T) {
	f := newFixture(t)
	var otherUser uuid.UUID
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT id FROM users WHERE email = 'musteri@parakopru.test'`).Scan(&otherUser))

	_, _, err := f.do(t, "ortak-anahtar", deposit{Account: "C", Amount: 1})
	require.NoError(t, err)
	_, replayed, err := f.doAs(t, otherUser, "ortak-anahtar", deposit{Account: "C", Amount: 1})
	require.NoError(t, err)
	require.False(t, replayed, "başka kullanıcının aynı anahtar metni çakışmamalı")
	require.Equal(t, int64(2), f.balance(t, "C"))
}

// İş kuralı reddi de sonuçtur (PLAN.md §5.2): kaydedilir, tekrar istek aynı reddi alır; işlemin yarım
// yazmaları geri alınır.
func TestBusinessRejectionIsStoredAndPartialWritesUndone(t *testing.T) {
	f := newFixture(t)
	_, _, err := f.do(t, "key-ok", deposit{Account: "D", Amount: 900_000})
	require.NoError(t, err)

	_, _, err = f.do(t, "key-limit", deposit{Account: "D", Amount: 200_000})
	require.ErrorIs(t, err, errLimitExceeded)
	require.Equal(t, int64(900_000), f.balance(t, "D"), "reddedilen işlemin yazdığı geri alınmalı")

	_, replayed, err := f.do(t, "key-limit", deposit{Account: "D", Amount: 200_000})
	require.ErrorIs(t, err, errLimitExceeded)
	require.True(t, replayed)
	require.Equal(t, int32(2), f.runs.Load(), "ret kayıtlı olduğu için işlem tekrar çalışmamalı")
}

// Beklenmeyen hatada hiçbir şey kaydedilmez; istemci aynı anahtarla yeniden deneyebilir.
func TestUnexpectedErrorIsNotStored(t *testing.T) {
	f := newFixture(t)
	in := deposit{Account: "E", Amount: 10}

	f.failNow.Store(true)
	_, _, err := f.do(t, "key-5xx", in)
	require.Error(t, err)
	require.Equal(t, int64(0), f.balance(t, "E"))

	f.failNow.Store(false)
	res, replayed, err := f.do(t, "key-5xx", in)
	require.NoError(t, err)
	require.False(t, replayed)
	require.Equal(t, int64(10), res.Balance)
}

// Aynı anahtarla eşzamanlı 20 istek: işlem tam bir kez yapılır, hepsi aynı sonucu alır.
func TestConcurrentRequestsWithSameKey(t *testing.T) {
	f := newFixture(t)
	in := deposit{Account: "F", Amount: 250}

	const n = 20
	results := make([]depositResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { results[i], _, errs[i] = f.do(t, "eszamanli", in) })
	}
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i])
		require.Equal(t, results[0], results[i])
	}
	require.Equal(t, int32(1), f.runs.Load())
	require.Equal(t, int64(250), f.balance(t, "F"))
}

func TestMarkReplayed(t *testing.T) {
	rec := httptest.NewRecorder()
	idempotency.MarkReplayed(rec, false)
	require.Empty(t, rec.Header().Get(idempotency.ReplayedHeader))

	idempotency.MarkReplayed(rec, true)
	require.Equal(t, "true", rec.Header().Get(idempotency.ReplayedHeader))
}
