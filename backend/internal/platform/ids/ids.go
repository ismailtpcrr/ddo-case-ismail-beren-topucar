// Package ids, kayıt kimliklerinin üretimini enjekte edilebilir yapar.
//
// Kimlikler UUIDv7'dir: ilk 48 biti milisaniye cinsinden zamandır, bu yüzden üretim sırasına göre
// sıralanırlar. B-tree dizine hep sona eklendikleri için dizin dostudurlar ve keyset sayfalamada
// "id > son_görülen" koşuluyla doğrudan kullanılabilirler.
package ids

import (
	"encoding/binary"
	"sync/atomic"

	"github.com/google/uuid"
)

// Generator yeni kimlik üretir.
type Generator interface {
	NewID() uuid.UUID
}

// V7 gerçek UUIDv7 üreticisidir.
type V7 struct{}

// NewID yeni bir UUIDv7 döndürür. Yalnız işletim sisteminin rastgele kaynağı çalışmazsa
// hata oluşabilir; o durumda programın devam etmesi anlamsız olduğundan panic edilir.
func (V7) NewID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

// Sequence testler için deterministik üreticidir: 1, 2, 3… sayaçlı, UUIDv7 biçiminde ve
// artan sırada kimlikler verir. Eşzamanlı kullanıma güvenlidir.
type Sequence struct {
	n atomic.Uint64
}

// NewID sıradaki kimliği döndürür.
func (s *Sequence) NewID() uuid.UUID {
	var u uuid.UUID
	binary.BigEndian.PutUint64(u[8:], s.n.Add(1))
	u[6] = 0x70 // sürüm 7
	u[8] = 0x80 // RFC 4122 varyantı (sayaç 2^56'yı aşmadıkça değeri bozmaz)
	return u
}
