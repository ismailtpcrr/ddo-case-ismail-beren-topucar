// Package db, PostgreSQL bağlantı havuzunu, transaction yönetimini ve migrasyonları içerir.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	connectAttempts = 30
	connectBackoff  = time.Second
	pingTimeout     = 2 * time.Second
)

// Connect bağlantı havuzu oluşturur ve veritabanı yanıt verene kadar bekler.
//
// Compose'da api, postgres sağlıklı olduktan sonra başlar; yine de veritabanı yeniden başlıyorsa
// (kaos senaryosu) süreç hemen çökmek yerine bir süre yeniden dener. Havuz, çalışma sırasında kopan
// bağlantıları kendisi yeniler.
func Connect(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// pgx hata mesajında parolayı maskeler.
		return nil, fmt.Errorf("db: parse url: %w", err)
	}
	cfg.MaxConns = maxConns

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}

	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return pool, nil
		}
		if attempt == connectAttempts {
			pool.Close()
			return nil, fmt.Errorf("db: unreachable after %d attempts: %w", attempt, err)
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(connectBackoff):
		}
	}
}
