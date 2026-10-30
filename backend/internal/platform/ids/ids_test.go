package ids_test

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/ids"
)

func TestV7IsVersion7AndOrdered(t *testing.T) {
	g := ids.V7{}
	prev := g.NewID()
	require.Equal(t, uuid.Version(7), prev.Version())

	for range 1000 {
		next := g.NewID()
		require.Equal(t, 1, bytes.Compare(next[:], prev[:]), "UUIDv7 kimlikler artan sırada olmalı")
		prev = next
	}
}

func TestSequenceIsDeterministic(t *testing.T) {
	var a, b ids.Sequence
	for range 5 {
		require.Equal(t, a.NewID(), b.NewID())
	}

	var s ids.Sequence
	first, second := s.NewID(), s.NewID()
	require.Equal(t, uuid.Version(7), first.Version())
	require.Equal(t, uuid.RFC4122, first.Variant())
	require.Equal(t, "00000000-0000-7000-8000-000000000001", first.String())
	require.Equal(t, 1, bytes.Compare(second[:], first[:]))
}
