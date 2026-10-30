// Package clock, zamanı enjekte edilebilir yapar.
//
// Faiz tahakkuku, provizyon süre aşımı ve zaman yolculuğu testlerinin deterministik olması için
// kod time.Now() yerine Clock arayüzünü kullanır; testlerde Manual saat verilir.
package clock

import (
	"sync"
	"time"
)

// Clock şimdiki zamanı verir.
type Clock interface {
	Now() time.Time
}

// System gerçek saattir. Zamanı UTC ve mikrosaniye hassasiyetinde döndürür; PostgreSQL
// timestamptz de mikrosaniye tuttuğu için yazılan ve geri okunan değer birebir aynı olur.
type System struct{}

// Now şimdiki zamanı döndürür.
func (System) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// Manual elle ilerletilen, eşzamanlı kullanıma güvenli test saatidir.
type Manual struct {
	mu  sync.Mutex
	now time.Time
}

// NewManual verilen anda duran bir saat oluşturur.
func NewManual(t time.Time) *Manual { return &Manual{now: t.UTC()} }

// Now saatin gösterdiği zamanı döndürür.
func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

// Set saati verilen ana ayarlar.
func (m *Manual) Set(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = t.UTC()
}

// Advance saati d kadar ileri alır.
func (m *Manual) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}
