package config_test

import (
	"log/slog"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/config"
)

// required, API ayarlarının çalışması için şart olan asgari değişkenlerdir.
var required = map[string]string{
	"DATABASE_URL":           "postgres://app:pw@db:5432/parakopru",
	"MIGRATION_DATABASE_URL": "postgres://owner:pw@db:5432/parakopru",
	"JWT_SECRET":             "test-secret-0123456789-0123456789-abcdef",
	"DEMO_PASSWORD":          "Demo.Parola.1",
}

func with(extra map[string]string) map[string]string {
	vars := maps.Clone(required)
	maps.Copy(vars, extra)
	return vars
}

func TestAPIDefaults(t *testing.T) {
	cfg, err := config.LoadFrom[config.API](with(nil))
	require.NoError(t, err)

	require.Equal(t, ":8080", cfg.HTTPAddr)
	require.Equal(t, slog.LevelInfo, cfg.LogLevel)
	require.Equal(t, 10*time.Second, cfg.RequestTimeout)
	require.Equal(t, 15*time.Second, cfg.ShutdownTimeout)
	require.Equal(t, int32(20), cfg.DBMaxConns)
	require.Equal(t, 15*time.Minute, cfg.AccessTokenTTL)
	require.Equal(t, 7*24*time.Hour, cfg.RefreshTokenTTL)
	require.False(t, cfg.CookieSecure)
}

func TestAPIOverrides(t *testing.T) {
	cfg, err := config.LoadFrom[config.API](with(map[string]string{
		"HTTP_ADDR":       ":9090",
		"LOG_LEVEL":       "debug",
		"REQUEST_TIMEOUT": "3s",
		"DB_MAX_CONNS":    "50",
	}))
	require.NoError(t, err)

	require.Equal(t, ":9090", cfg.HTTPAddr)
	require.Equal(t, slog.LevelDebug, cfg.LogLevel)
	require.Equal(t, 3*time.Second, cfg.RequestTimeout)
	require.Equal(t, int32(50), cfg.DBMaxConns)
}

func TestAPIRequiresSettings(t *testing.T) {
	for _, key := range []string{"DATABASE_URL", "MIGRATION_DATABASE_URL", "JWT_SECRET", "DEMO_PASSWORD"} {
		missing := with(nil)
		delete(missing, key)
		_, err := config.LoadFrom[config.API](missing)
		require.ErrorContains(t, err, key)

		_, err = config.LoadFrom[config.API](with(map[string]string{key: ""}))
		require.ErrorContains(t, err, key)
	}
}

func TestAPIRejectsInvalidValues(t *testing.T) {
	cases := map[string]map[string]string{
		"bilinmeyen log seviyesi": {"LOG_LEVEL": "çok-ayrıntılı"},
		"süre biçimi hatalı":      {"REQUEST_TIMEOUT": "on saniye"},
		"sıfır zaman aşımı":       {"REQUEST_TIMEOUT": "0s"},
		"negatif kapanış süresi":  {"SHUTDOWN_TIMEOUT": "-1s"},
		"sıfır bağlantı":          {"DB_MAX_CONNS": "0"},
		"kısa JWT anahtarı":       {"JWT_SECRET": "kisa"},
		"refresh access'ten kısa": {"ACCESS_TOKEN_TTL": "1h", "REFRESH_TOKEN_TTL": "30m"},
	}
	for name, vars := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := config.LoadFrom[config.API](with(vars))
			require.Error(t, err)
		})
	}
}

func TestLoadReadsProcessEnvironment(t *testing.T) {
	for k, v := range required {
		t.Setenv(k, v)
	}
	t.Setenv("HTTP_ADDR", ":7070")

	cfg, err := config.Load[config.API]()
	require.NoError(t, err)
	require.Equal(t, ":7070", cfg.HTTPAddr)
}

func TestWorkerDefaultsAndValidation(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://app:pw@db:5432/parakopru"}
	cfg, err := config.LoadFrom[config.Worker](base)
	require.NoError(t, err)
	require.Equal(t, 4, cfg.Concurrency)
	require.Equal(t, time.Second, cfg.PollInterval)
	require.Equal(t, 2*time.Minute, cfg.JobLease)

	_, err = config.LoadFrom[config.Worker](map[string]string{})
	require.ErrorContains(t, err, "DATABASE_URL")

	for name, extra := range map[string]map[string]string{
		"sıfır eşzamanlılık":       {"WORKER_CONCURRENCY": "0"},
		"bağlantı sayısı yetersiz": {"WORKER_CONCURRENCY": "10", "DB_MAX_CONNS": "10"},
		"sıfır kira süresi":        {"WORKER_JOB_LEASE": "0s"},
		"negatif bekleme aralığı":  {"WORKER_POLL_INTERVAL": "-1s"},
	} {
		t.Run(name, func(t *testing.T) {
			vars := maps.Clone(base)
			maps.Copy(vars, extra)
			_, err := config.LoadFrom[config.Worker](vars)
			require.Error(t, err)
		})
	}
}
