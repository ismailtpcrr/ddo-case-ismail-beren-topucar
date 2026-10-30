package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/clock"
)

const testSecret = "test-secret-0123456789-0123456789-abcdef"

func newTestTokens() (*Tokens, *clock.Manual) {
	c := clock.NewManual(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	return NewTokens(testSecret, 15*time.Minute, 7*24*time.Hour, c), c
}

func TestAccessTokenRoundTrip(t *testing.T) {
	tokens, _ := newTestTokens()
	userID := uuid.New()

	s, exp, err := tokens.IssueAccess(userID, RoleOperations)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 1, 9, 15, 0, 0, time.UTC), exp)

	p, err := tokens.ParseAccess(s)
	require.NoError(t, err)
	require.Equal(t, Principal{UserID: userID, Role: RoleOperations}, p)
}

func TestAccessTokenExpires(t *testing.T) {
	tokens, clk := newTestTokens()
	s, _, err := tokens.IssueAccess(uuid.New(), RoleCustomer)
	require.NoError(t, err)

	clk.Advance(16 * time.Minute)
	_, err = tokens.ParseAccess(s)
	require.ErrorIs(t, err, errInvalidToken)
}

func TestTokenTypesAreNotInterchangeable(t *testing.T) {
	tokens, _ := newTestTokens()
	userID, tokenID := uuid.New(), uuid.New()

	refresh, _, err := tokens.IssueRefresh(userID, tokenID)
	require.NoError(t, err)
	_, err = tokens.ParseAccess(refresh)
	require.ErrorIs(t, err, errInvalidToken, "refresh token access yerine kullanılamamalı")

	access, _, err := tokens.IssueAccess(userID, RoleCustomer)
	require.NoError(t, err)
	_, _, err = tokens.ParseRefresh(access)
	require.ErrorIs(t, err, errInvalidToken, "access token refresh yerine kullanılamamalı")

	gotUser, gotID, err := tokens.ParseRefresh(refresh)
	require.NoError(t, err)
	require.Equal(t, userID, gotUser)
	require.Equal(t, tokenID, gotID)
}

func TestTokenWithWrongSecretRejected(t *testing.T) {
	tokens, clk := newTestTokens()
	other := NewTokens("baska-bir-anahtar-0123456789-0123456789", time.Minute, time.Hour, clk)

	s, _, err := other.IssueAccess(uuid.New(), RoleOperations)
	require.NoError(t, err)
	_, err = tokens.ParseAccess(s)
	require.ErrorIs(t, err, errInvalidToken)
}

func TestUnsignedTokenRejected(t *testing.T) {
	tokens, clk := newTestTokens()
	c := claims{Role: RoleOperations, Type: tokenTypeAccess, RegisteredClaims: jwt.RegisteredClaims{
		Issuer: issuer, Subject: uuid.NewString(), ExpiresAt: jwt.NewNumericDate(clk.Now().Add(time.Hour)),
	}}
	// "alg: none" saldırısı: imzasız token.
	s, err := jwt.NewWithClaims(jwt.SigningMethodNone, c).SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	_, err = tokens.ParseAccess(s)
	require.ErrorIs(t, err, errInvalidToken)
}

func TestTokenWithUnknownRoleRejected(t *testing.T) {
	tokens, _ := newTestTokens()
	s, _, err := tokens.IssueAccess(uuid.New(), Role("ADMIN"))
	require.NoError(t, err)

	_, err = tokens.ParseAccess(s)
	require.ErrorIs(t, err, errInvalidToken)
}
