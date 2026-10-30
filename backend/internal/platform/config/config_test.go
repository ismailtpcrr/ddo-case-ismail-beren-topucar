package config_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/config"
)

func TestAPIDefaults(t *testing.T) {
	cfg, err := config.LoadFrom[config.API](map[string]string{})
	require.NoError(t, err)

	require.Equal(t, ":8080", cfg.HTTPAddr)
	require.Equal(t, slog.LevelInfo, cfg.LogLevel)
	require.Equal(t, 10*time.Second, cfg.RequestTimeout)
	require.Equal(t, 15*time.Second, cfg.ShutdownTimeout)
}

func TestAPIOverrides(t *testing.T) {
	cfg, err := config.LoadFrom[config.API](map[string]string{
		"HTTP_ADDR":       ":9090",
		"LOG_LEVEL":       "debug",
		"REQUEST_TIMEOUT": "3s",
	})
	require.NoError(t, err)

	require.Equal(t, ":9090", cfg.HTTPAddr)
	require.Equal(t, slog.LevelDebug, cfg.LogLevel)
	require.Equal(t, 3*time.Second, cfg.RequestTimeout)
}

func TestAPIRejectsInvalidValues(t *testing.T) {
	_, err := config.LoadFrom[config.API](map[string]string{"LOG_LEVEL": "çok-ayrıntılı"})
	require.Error(t, err)

	_, err = config.LoadFrom[config.API](map[string]string{"REQUEST_TIMEOUT": "on saniye"})
	require.Error(t, err)
}

func TestLoadReadsProcessEnvironment(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":7070")

	cfg, err := config.Load[config.API]()
	require.NoError(t, err)
	require.Equal(t, ":7070", cfg.HTTPAddr)
}
