// Package dbtest, entegrasyon testleri için gerçek bir PostgreSQL 16 konteyneri başlatır (testcontainers).
//
// Konteyner, compose'daki ile aynı imaj ve aynı rol başlangıç betiğiyle açılır; migrasyonlar uygulanmış
// ve iki bağlantı (sahip ve uygulama rolü) hazır gelir. Her test paketi TestMain'inde bir konteyner açar.
//
// Kullanım:
//
//	func TestMain(m *testing.M) { dbtest.Main(m) }
//	func TestX(t *testing.T)    { env := dbtest.Get(t); ... }
//
// `go test -short` ile konteyner açılmaz ve veritabanı isteyen testler atlanır.
package dbtest

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"parakopru/internal/platform/db"
	"parakopru/migrations"
)

const (
	image         = "postgres:16-alpine"
	database      = "parakopru"
	ownerUser     = "owner"
	ownerPassword = "owner-test-pw"
	appUser       = "app"
	appPassword   = "app-test-pw"
)

// Env bir test veritabanı ortamıdır.
type Env struct {
	OwnerURL string        // sahip rol: tablo oluşturabilir (test hazırlığı için)
	AppURL   string        // uygulama rolü: api/worker'ın gerçek yetkileri
	Owner    *pgxpool.Pool // sahip rol bağlantı havuzu
	App      *pgxpool.Pool // uygulama rolü bağlantı havuzu
}

// Start konteyneri açar, migrasyonları uygular ve bağlantıları kurar.
// Dönen fonksiyon her şeyi kapatır.
func Start(ctx context.Context) (*Env, func(), error) {
	ctr, err := postgres.Run(ctx, image,
		postgres.WithDatabase(database),
		postgres.WithUsername(ownerUser),
		postgres.WithPassword(ownerPassword),
		postgres.WithInitScripts(initScriptPath()),
		testcontainers.WithEnv(map[string]string{"APP_DB_USER": appUser, "APP_DB_PASSWORD": appPassword}),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("dbtest: start postgres: %w", err)
	}
	cleanup := func() { _ = testcontainers.TerminateContainer(ctr) }

	ownerURL, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("dbtest: connection string: %w", err)
	}
	u, err := url.Parse(ownerURL)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("dbtest: parse url: %w", err)
	}
	u.User = url.UserPassword(appUser, appPassword)
	appURL := u.String()

	if err := db.Migrate(ctx, ownerURL, migrations.FS, slog.New(slog.DiscardHandler)); err != nil {
		cleanup()
		return nil, nil, err
	}
	owner, err := db.Connect(ctx, ownerURL, 10)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	app, err := db.Connect(ctx, appURL, 50)
	if err != nil {
		owner.Close()
		cleanup()
		return nil, nil, err
	}

	env := &Env{OwnerURL: ownerURL, AppURL: appURL, Owner: owner, App: app}
	return env, func() {
		app.Close()
		owner.Close()
		cleanup()
	}, nil
}

// initScriptPath compose'un da kullandığı rol başlangıç betiğinin yoludur.
func initScriptPath() string {
	_, file, _, _ := runtime.Caller(0)
	// backend/internal/platform/db/dbtest → depo kökü
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..")
	return filepath.Join(root, "deploy", "postgres", "initdb", "01-app-role.sh")
}

var shared *Env

// Main bir paketin TestMain'inde çağrılır: konteyneri açar, testleri çalıştırır, konteyneri kapatır.
func Main(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	env, cleanup, err := Start(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	shared = env
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// Get paket konteynerinin ortamını döndürür; -short modunda testi atlar.
func Get(t testing.TB) *Env {
	t.Helper()
	if shared == nil {
		t.Skip("veritabanı testi; -short modunda atlanır")
	}
	return shared
}
