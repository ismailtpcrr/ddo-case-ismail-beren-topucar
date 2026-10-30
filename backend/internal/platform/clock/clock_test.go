package clock_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/clock"
)

func TestSystemClockIsUTCMicrosecond(t *testing.T) {
	now := clock.System{}.Now()

	require.Equal(t, time.UTC, now.Location())
	require.Zero(t, now.Nanosecond()%1000, "mikrosaniyeye yuvarlanmalı")
}

func TestManualClock(t *testing.T) {
	start := time.Date(2024, 2, 28, 23, 0, 0, 0, time.UTC)
	c := clock.NewManual(start)
	require.Equal(t, start, c.Now())

	c.Advance(2 * time.Hour)
	require.Equal(t, time.Date(2024, 2, 29, 1, 0, 0, 0, time.UTC), c.Now())

	other := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Set(other)
	require.Equal(t, other, c.Now())
}
