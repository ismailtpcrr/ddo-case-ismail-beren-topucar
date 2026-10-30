package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parametreleri: OWASP Password Storage Cheat Sheet önerisi (m=19 MiB, t=2, p=1).
// Daha yüksek bellek (ör. RFC 9106'daki 64 MiB) tek girişte daha güçlüdür ama yük testinde yüzlerce
// eşzamanlı girişte belleği tüketir; OWASP değeri bu dengeyi kurar.
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonKeyLen    = 32
	argonSaltLen   = 16
)

// Saklanan özetten okunan parametreler bu sınırların dışındaysa özet reddedilir. Bozuk ya da
// kötü niyetle değiştirilmiş bir özetin (ör. m=4 TiB) sunucuyu bellek tüketerek düşürmesini önler.
const (
	maxMemoryKiB = 256 * 1024
	maxTime      = 10
	maxThreads   = 16
)

var errMalformedHash = errors.New("auth: malformed password hash")

// HashPassword parolanın argon2id özetini PHC biçiminde döndürür:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<tuz>$<özet>
//
// Parametreler özetin içinde saklanır; ileride değiştirilirse eski özetler doğrulanmaya devam eder.
func HashPassword(password string) string {
	salt := make([]byte, argonSaltLen)
	_, _ = rand.Read(salt) // crypto/rand.Read hata döndürmez (Go 1.24+)
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// VerifyPassword parolanın özetle eşleşip eşleşmediğini sabit sürede karşılaştırır.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errMalformedHash
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, errMalformedHash
	}
	if memory < 8 || memory > maxMemoryKiB || time < 1 || time > maxTime || threads < 1 || threads > maxThreads {
		return false, errMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, errMalformedHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, errMalformedHash
	}

	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want))) //nolint:gosec // uzunluk 16..64 aralığında denetlendi
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
