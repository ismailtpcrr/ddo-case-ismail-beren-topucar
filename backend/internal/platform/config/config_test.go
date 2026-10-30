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
var required = map[string]string{"DATABASE_URL": "postgres://app:pw@db:5432/parakopru"}

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

func TestAPIRequiresDatabaseURL(t *testing.T) {
	_, err := config.LoadFrom[config.API](map[string]string{})
	require.ErrorContains(t, err, "DATABASE_URL")

	_, err = config.LoadFrom[config.API](map[string]string{"DATABASE_URL": ""})
	require.ErrorContains(t, err, "DATABASE_URL")
}

func TestAPIRejectsInvalidValues(t *testing.T) {
	cases := map[string]map[string]string{
		"bilinmeyen log seviyesi": {"LOG_LEVEL": "çok-ayrıntılı"},
		"süre biçimi hatalı":      {"REQUEST_TIMEOUT": "on saniye"},
		"sıfır zaman aşımı":       {"REQUEST_TIMEOUT": "0s"},
		"negatif kapanış süresi":  {"SHUTDOWN_TIMEOUT": "-1s"},
		"sıfır bağlantı":          {"DB_MAX_CONNS": "0"},
	}
	for name, vars := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := config.LoadFrom[config.API](with(vars))
			require.Error(t, err)
		})
	}
}

func TestLoadReadsProcessEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", required["DATABASE_URL"])
	t.Setenv("HTTP_ADDR", ":7070")

	cfg, err := config.Load[config.API]()
	require.NoError(t, err)
	require.Equal(t, ":7070", cfg.HTTPAddr)
}
