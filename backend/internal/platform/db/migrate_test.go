package db_test

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
	"parakopru/migrations"
)

// Aynı anda açılan üç api kopyasını taklit eder: boş bir veritabanında üç eşzamanlı Migrate.
// Advisory lock sayesinde hepsi hatasız biter ve her migrasyon tam bir kez uygulanır.
func TestMigrateConcurrentRunsApplyOnce(t *testing.T) {
	env := dbtest.Get(t)
	ctx := t.Context()
	_, err := env.Owner.Exec(ctx, "CREATE DATABASE migrate_race")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = env.Owner.Exec(context.Background(), "DROP DATABASE migrate_race WITH (FORCE)") })

	u, err := url.Parse(env.OwnerURL)
	require.NoError(t, err)
	u.Path = "/migrate_race"
	target := u.String()

	var g errgroup.Group
	for range 3 {
		g.Go(func() error { return db.Migrate(ctx, target, migrations.FS, slog.New(slog.DiscardHandler)) })
	}
	require.NoError(t, g.Wait())

	pool, err := db.Connect(ctx, target, 2)
	require.NoError(t, err)
	defer pool.Close()

	var applied, distinct int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*), count(DISTINCT version_id) FROM goose_db_version WHERE version_id > 0`).Scan(&applied, &distinct))
	require.Positive(t, applied)
	require.Equal(t, distinct, applied, "hiçbir migrasyon iki kez uygulanmamalı")
}

func TestAppRoleCannotChangeSchema(t *testing.T) {
	env := dbtest.Get(t)

	for _, stmt := range []string{
		"CREATE TABLE app_should_not_create (id int)",
		"DROP TABLE goose_db_version",
		"DELETE FROM goose_db_version",
	} {
		_, err := env.App.Exec(t.Context(), stmt)
		var pgErr *pgconn.PgError
		require.True(t, errors.As(err, &pgErr), stmt)
		require.Equal(t, "42501", pgErr.Code, "%s: yetki hatası (insufficient_privilege) beklenir", stmt)
	}
}

func TestAppRoleCanReadAndWriteNewTables(t *testing.T) {
	env := dbtest.Get(t)
	ctx := t.Context()
	counterTable(t, env, "privileges_check", 1)

	for _, stmt := range []string{
		"INSERT INTO privileges_check (id) VALUES (2)",
		"UPDATE privileges_check SET n = 1 WHERE id = 2",
		"SELECT * FROM privileges_check",
		"DELETE FROM privileges_check WHERE id = 2",
	} {
		_, err := env.App.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}
}

func TestConnectStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := db.Connect(ctx, "postgres://nobody:pw@127.0.0.1:1/none?sslmode=disable", 1)
	require.Error(t, err)
}
