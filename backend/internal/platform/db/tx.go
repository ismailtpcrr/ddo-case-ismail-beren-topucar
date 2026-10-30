package db

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"parakopru/internal/platform/observability"
)

// DBTX, repo fonksiyonlarının aldığı ortak arayüzdür. Hem bağlantı havuzu (*pgxpool.Pool) hem de
// bir transaction (pgx.Tx) bunu sağlar. Böylece aynı repo fonksiyonu tek başına da, bir servis
// transaction'ının parçası olarak da çalışır; transaction sınırına repo değil servis karar verir.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error)
}

var (
	_ DBTX = (*pgxpool.Pool)(nil)
	_ DBTX = (pgx.Tx)(nil)
)

// TxFunc bir transaction içinde çalışan iş birimidir.
//
// Yeniden deneme olabileceği için fonksiyon transaction dışına yan etki bırakmamalıdır
// (ör. dış servise istek atmamalı); dış etkiler outbox (jobs tablosu) üzerinden yapılır.
type TxFunc func(ctx context.Context, tx DBTX) error

// PostgreSQL hata kodları (SQLSTATE).
const (
	sqlStateSerializationFailure = "40001"
	sqlStateDeadlockDetected     = "40P01"
)

// maxRetries, ilk denemeden sonra en fazla kaç kez yeniden deneneceğidir.
const maxRetries = 3

// TxManager transaction'ları başlatır, commit eder ve geçici hatalarda yeniden dener.
type TxManager struct {
	pool *pgxpool.Pool
}

// NewTxManager yeni bir TxManager oluşturur.
func NewTxManager(pool *pgxpool.Pool) *TxManager { return &TxManager{pool: pool} }

// DB transaction dışı okumalar için havuzu döndürür.
func (m *TxManager) DB() DBTX { return m.pool }

// WithTx fn'i varsayılan (READ COMMITTED) bir transaction içinde çalıştırır.
func (m *TxManager) WithTx(ctx context.Context, fn TxFunc) error {
	return m.WithTxOptions(ctx, pgx.TxOptions{}, fn)
}

// WithTxOptions fn'i verilen seçeneklerle bir transaction içinde çalıştırır.
//
// fn hata dönerse transaction geri alınır (rollback), dönmezse commit edilir. Serileştirme hatası
// (40001) ya da deadlock (40P01) olursa transaction baştan, en fazla maxRetries kez, artan ve rastgele
// bir beklemeyle yeniden denenir. Bu hatalar "işlem yanlış" değil "aynı anda başka bir işlemle çakıştı"
// demektir; yeniden denemek doğru sonucu verir. Diğer hatalar hemen döndürülür.
func (m *TxManager) WithTxOptions(ctx context.Context, opts pgx.TxOptions, fn TxFunc) error {
	for attempt := 0; ; attempt++ {
		err := pgx.BeginTxFunc(ctx, m.pool, opts, func(tx pgx.Tx) error { return fn(ctx, tx) })
		if err == nil {
			return nil
		}
		code, retryable := retryableCode(err)
		if !retryable || attempt == maxRetries {
			return err
		}
		observability.Logger(ctx).Warn("transaction retry",
			slog.Int("attempt", attempt+1), slog.String("sqlstate", code))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff(attempt)):
		}
	}
}

// IsRetryable, hatanın yeniden denenebilir bir çakışma (serileştirme hatası ya da deadlock) olup olmadığını söyler.
func IsRetryable(err error) bool {
	_, ok := retryableCode(err)
	return ok
}

func retryableCode(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return "", false
	}
	switch pgErr.Code {
	case sqlStateSerializationFailure, sqlStateDeadlockDetected:
		return pgErr.Code, true
	}
	return pgErr.Code, false
}

// backoff, n. yeniden deneme öncesi beklemedir: 5, 10, 20 ms taban + aynı büyüklükte rastgele ek.
// Rastgelelik, çakışan iki işlemin aynı anda yeniden deneyip tekrar çakışmasını önler.
func backoff(attempt int) time.Duration {
	base := 5 * time.Millisecond << attempt
	return base + rand.N(base) //nolint:gosec // güvenlik amaçlı değil, yalnız zamanlama dağıtımı
}
