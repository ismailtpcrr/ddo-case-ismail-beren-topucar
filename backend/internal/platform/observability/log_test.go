package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/observability"
)

func TestLoggerWritesJSONAndRedactsSecrets(t *testing.T) {
	var buf bytes.Buffer
	log := observability.NewLogger(&buf, slog.LevelInfo, "api")

	log.Info("login",
		"email", "musteri@parakopru.test",
		"password", "gizli-parola",
		"refresh_token", "abc.def.ghi",
		"Authorization", "Bearer xyz",
	)

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line), "her satır geçerli JSON olmalı")
	require.Equal(t, "api", line["service"])
	require.Equal(t, "musteri@parakopru.test", line["email"])
	require.Equal(t, "[REDACTED]", line["password"])
	require.Equal(t, "[REDACTED]", line["refresh_token"])
	require.Equal(t, "[REDACTED]", line["Authorization"])
	require.NotContains(t, buf.String(), "gizli-parola")
	require.NotContains(t, buf.String(), "xyz")
}

func TestLoggerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	log := observability.NewLogger(&buf, slog.LevelWarn, "worker")

	log.Info("görünmemeli")
	require.Zero(t, buf.Len())

	log.Warn("görünmeli")
	require.NotZero(t, buf.Len())
}

func TestLoggerFromContext(t *testing.T) {
	var buf bytes.Buffer
	reqLog := observability.NewLogger(&buf, slog.LevelInfo, "api").With("request_id", "r-1")

	ctx := observability.WithLogger(context.Background(), reqLog)
	observability.Logger(ctx).Info("istek")
	require.Contains(t, buf.String(), `"request_id":"r-1"`)

	require.Same(t, slog.Default(), observability.Logger(context.Background()))
}
