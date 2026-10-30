package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash := HashPassword("ParaKopru.2026")
	require.True(t, strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$"))
	require.NotContains(t, hash, "ParaKopru.2026")

	ok, err := VerifyPassword("ParaKopru.2026", hash)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = VerifyPassword("parakopru.2026", hash)
	require.NoError(t, err)
	require.False(t, ok, "büyük/küçük harf farkı da yanlış paroladır")
}

func TestSamePasswordGetsDifferentHashes(t *testing.T) {
	require.NotEqual(t, HashPassword("aynı"), HashPassword("aynı"), "her özet rastgele tuz kullanmalı")
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	valid := HashPassword("x")
	parts := strings.Split(valid, "$")

	cases := map[string]string{
		"boş":                "",
		"bcrypt biçimi":      "$2a$10$abcdefghijklmnopqrstuuJ0N0e0x4lJ0qGk1aZ7n2o3p4q5r6s7t",
		"argon2i (id değil)": strings.Replace(valid, "argon2id", "argon2i", 1),
		"bilinmeyen sürüm":   strings.Replace(valid, "v=19", "v=16", 1),
		"aşırı bellek":       strings.Replace(valid, "m=19456", "m=4294967295", 1),
		"sıfır tur":          strings.Replace(valid, "t=2", "t=0", 1),
		"bozuk tuz":          strings.Join([]string{"", parts[1], parts[2], parts[3], "%%%", parts[5]}, "$"),
		"eksik parça":        strings.Join(parts[:5], "$"),
	}
	for name, hash := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := VerifyPassword("x", hash)
			require.ErrorIs(t, err, errMalformedHash)
			require.False(t, ok)
		})
	}
}

// Rastgele özet metinleri hiçbir zaman panic'e ya da aşırı bellek kullanımına yol açmamalı.
func FuzzVerifyPasswordNeverPanics(f *testing.F) {
	f.Add("parola", HashPassword("parola"))
	f.Add("x", "$argon2id$v=19$m=1,t=1,p=1$AAAA$AAAA")
	f.Add("", "$$$$$")
	f.Fuzz(func(t *testing.T, password, hash string) {
		_, _ = VerifyPassword(password, hash)
	})
}
