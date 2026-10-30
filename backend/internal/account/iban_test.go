package account

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewIBANIsValid(t *testing.T) {
	for _, n := range []int64{1, 42, 123456789, 9999999999999999} {
		iban := NewIBAN(n)
		require.Len(t, iban, 26)
		require.True(t, ValidIBAN(iban), iban)
	}
	require.Equal(t, "TR", NewIBAN(7)[:2])
	require.NotEqual(t, NewIBAN(1), NewIBAN(2))
}

func TestValidIBAN(t *testing.T) {
	require.True(t, ValidIBAN("TR330006100519786457841326"), "yaygın kullanılan geçerli örnek TR IBAN")
	for _, bad := range []string{
		"TR330006100519786457841327", // son hane değişti: kontrol hanesi tutmaz
		"TR33000610051978645784132",  // eksik hane
		"DE89370400440532013000",     // başka ülke
		"TR33000610051978645784132X",
		"",
	} {
		require.False(t, ValidIBAN(bad), bad)
	}
}
