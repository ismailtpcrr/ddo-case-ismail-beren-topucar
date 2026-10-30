package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"path"

	_ "github.com/jackc/pgx/v5/stdlib" // goose database/sql ister; pgx'in database/sql sürücüsü
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Migrate bekleyen migrasyonları sahip rolle uygular.
//
// Aynı anda birden fazla api kopyası açılırsa migrasyonların iki kez çalışmaması için PostgreSQL
// oturum düzeyinde advisory lock alınır (goose'un PostgreSQL kilitleyicisi). Kilidi alan kopya
// migrasyonları uygular, diğerleri bekler; sıra onlara geldiğinde uygulanacak migrasyon kalmamıştır.
// Bu yüzden ayrı, tek seferlik bir migrasyon servisine gerek yoktur.
func Migrate(ctx context.Context, url string, fsys fs.FS, log *slog.Logger) error {
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("migrate: open: %w", err)
	}
	defer func() { _ = sqlDB.Close() }() // migrasyon sonucu zaten döndü; kapanış hatası bir şey değiştirmez

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("migrate: locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("migrate: provider: %w", err)
	}

	results, err := provider.Up(ctx)
	for _, r := range results {
		log.Info("migration applied",
			slog.Int64("version", r.Source.Version),
			slog.String("file", path.Base(r.Source.Path)),
			slog.Int64("duration_ms", r.Duration.Milliseconds()))
	}
	if err != nil {
		return fmt.Errorf("migrate: up: %w", err)
	}

	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("migrate: version: %w", err)
	}
	log.Info("database schema up to date", slog.Int64("version", version))
	return nil
}
