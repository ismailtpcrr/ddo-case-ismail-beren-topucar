package apperr_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/apperr"
)

func TestWithMessageKeepsIdentity(t *testing.T) {
	err := apperr.ErrNotFound.WithMessage("Hesap bulunamadı.")

	require.ErrorIs(t, err, apperr.ErrNotFound)
	require.NotErrorIs(t, err, apperr.ErrConflict)
	require.Equal(t, http.StatusNotFound, err.Status)
	require.Equal(t, "Hesap bulunamadı.", err.Message)
	// Önceden tanımlı hata değişmemeli.
	require.Equal(t, "Kayıt bulunamadı.", apperr.ErrNotFound.Message)
}

func TestWrapKeepsCauseAndCode(t *testing.T) {
	cause := errors.New("bağlantı koptu")
	err := apperr.ErrUnavailable.Wrap(cause)

	require.ErrorIs(t, err, cause)
	require.ErrorIs(t, err, apperr.ErrUnavailable)
	require.Contains(t, err.Error(), "bağlantı koptu")
}

func TestAsFindsErrorInChain(t *testing.T) {
	wrapped := fmt.Errorf("servis: %w", apperr.ErrForbidden.Messagef("%s rolü bu işlemi yapamaz", "denetçi"))

	got, ok := apperr.As(wrapped)
	require.True(t, ok)
	require.Equal(t, "FORBIDDEN", got.Code)
	require.Equal(t, "denetçi rolü bu işlemi yapamaz", got.Message)

	_, ok = apperr.As(errors.New("sıradan hata"))
	require.False(t, ok)
}
