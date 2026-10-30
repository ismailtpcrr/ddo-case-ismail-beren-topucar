package db_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
)

// counterTable sahip rolle test tablosu oluşturur: (id int, n int), satırlar 1..rows, n = 0.
// Tablo migrasyondan sonra oluştuğu için uygulama rolü varsayılan yetkilerle okuyup yazabilir.
func counterTable(t *testing.T, env *dbtest.Env, name string, rows int) {
	t.Helper()
	ctx := t.Context()
	_, err := env.Owner.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE %s (id int PRIMARY KEY, n int NOT NULL DEFAULT 0);
		 INSERT INTO %s (id) SELECT generate_series(1, %d);`, name, name, rows))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = env.Owner.Exec(context.Background(), "DROP TABLE "+name) })
}

func sumN(t *testing.T, env *dbtest.Env, table string) int {
	t.Helper()
	var sum int
	require.NoError(t, env.App.QueryRow(t.Context(), "SELECT coalesce(sum(n), 0) FROM "+table).Scan(&sum))
	return sum
}

func TestWithTxCommits(t *testing.T) {
	env := dbtest.Get(t)
	counterTable(t, env, "tx_commit", 1)
	tm := db.NewTxManager(env.App)

	err := tm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
		_, err := tx.Exec(ctx, "UPDATE tx_commit SET n = n + 5 WHERE id = 1")
		return err
	})
	require.NoError(t, err)
	require.Equal(t, 5, sumN(t, env, "tx_commit"))
}

func TestWithTxRollsBackOnError(t *testing.T) {
	env := dbtest.Get(t)
	counterTable(t, env, "tx_rollback", 1)
	tm := db.NewTxManager(env.App)
	boom := errors.New("iş kuralı ihlali")

	err := tm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
		if _, err := tx.Exec(ctx, "UPDATE tx_rollback SET n = n + 5 WHERE id = 1"); err != nil {
			return err
		}
		return boom // yazma yapıldı ama hata döndü: hiçbir şey kalıcı olmamalı
	})
	require.ErrorIs(t, err, boom)
	require.Equal(t, 0, sumN(t, env, "tx_rollback"))
}

func TestWithTxRetriesSerializationFailure(t *testing.T) {
	env := dbtest.Get(t)
	counterTable(t, env, "tx_retry", 1)
	tm := db.NewTxManager(env.App)

	var attempts int
	err := tm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
		attempts++
		if _, err := tx.Exec(ctx, "UPDATE tx_retry SET n = n + 1 WHERE id = 1"); err != nil {
			return err
		}
		if attempts < 3 {
			return &pgconn.PgError{Code: "40001"} // serileştirme hatası
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, attempts)
	require.Equal(t, 1, sumN(t, env, "tx_retry"), "başarısız denemelerin yazdıkları geri alınmış olmalı")
}

func TestWithTxGivesUpAfterMaxRetries(t *testing.T) {
	env := dbtest.Get(t)
	tm := db.NewTxManager(env.App)

	var attempts int
	err := tm.WithTx(t.Context(), func(context.Context, db.DBTX) error {
		attempts++
		return &pgconn.PgError{Code: "40P01"} // her seferinde deadlock
	})
	require.True(t, db.IsRetryable(err))
	require.Equal(t, 4, attempts, "ilk deneme + en fazla 3 yeniden deneme")
}

func TestWithTxDoesNotRetryOtherErrors(t *testing.T) {
	env := dbtest.Get(t)
	tm := db.NewTxManager(env.App)

	var attempts int
	err := tm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
		attempts++
		_, err := tx.Exec(ctx, "SELECT 1/0") // 22012 division_by_zero: yeniden denemek düzeltmez
		return err
	})
	require.Error(t, err)
	require.False(t, db.IsRetryable(err))
	require.Equal(t, 1, attempts)
}

// İki transaction aynı iki satırı ters sırada kilitler → PostgreSQL gerçek bir deadlock tespit eder ve
// birini 40P01 ile iptal eder. TxManager iptal edileni yeniden dener; sonunda ikisi de başarılı olur.
func TestWithTxRecoversFromRealDeadlock(t *testing.T) {
	env := dbtest.Get(t)
	counterTable(t, env, "tx_deadlock", 2)
	tm := db.NewTxManager(env.App)

	var bothHoldFirstLock sync.WaitGroup
	bothHoldFirstLock.Add(2)
	var attempts atomic.Int32

	transfer := func(first, second int) func() error {
		return func() error {
			firstAttempt := true
			return tm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
				attempts.Add(1)
				if _, err := tx.Exec(ctx, "UPDATE tx_deadlock SET n = n + 1 WHERE id = $1", first); err != nil {
					return err
				}
				if firstAttempt { // ilk denemede iki taraf da ilk kilidi alana kadar bekle
					firstAttempt = false
					bothHoldFirstLock.Done()
					bothHoldFirstLock.Wait()
				}
				_, err := tx.Exec(ctx, "UPDATE tx_deadlock SET n = n + 1 WHERE id = $1", second)
				return err
			})
		}
	}

	var g errgroup.Group
	g.Go(transfer(1, 2))
	g.Go(transfer(2, 1))
	require.NoError(t, g.Wait())

	require.Equal(t, int32(3), attempts.Load(), "biri deadlock kurbanı olup bir kez yeniden denenmeli")
	require.Equal(t, 4, sumN(t, env, "tx_deadlock"), "iki transaction da iki satırı birer kez artırmış olmalı")
}
