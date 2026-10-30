package sim

// Dış banka sahtesi (proje.md Görev 2; PLAN.md §5.3). Gerçek bir dış bankanın başına gelen sorunları üretir:
// gecikme, zaman aşımı, hata, "işlendi mi bilmiyoruz" (belirsiz sonuç) ve çift bildirim. Varsayılan oranlar
// şartnamedeki gibidir: %20 hata, %10 çift cevap. Aynı tohum aynı davranışı verir: her çağrının kaderi
// (tohum, referans, o referansa kaçıncı çağrı) üçlüsünden türetilir.
//
// İşlenmiş bir transfer kalıcıdır: aynı referansla tekrar gönderim yeni işlem yapmaz, aynı sonucu verir (banka tarafında
// idempotans). Bu yüzden istemci referans olarak kendi transfer kimliğini gönderip güvenle yeniden deneyebilir.

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"parakopru/internal/account"
)

// BankConfig dış banka sahtesinin ayarlarıdır.
type BankConfig struct {
	Seed             int64
	FailurePercent   int           // çağrıların bu kadarı hata verir (%20)
	DuplicatePercent int           // bildirimlerin bu kadarı iki kez gönderilir (%10)
	MinLatency       time.Duration // her çağrının gecikmesi [Min, Max] arası
	MaxLatency       time.Duration
	Hang             time.Duration // zaman aşımı hatalarında bağlantının asılı kalma süresi (istemci zaman aşımından uzun)
}

// Notification bankanın bir transferin sonucunu bildirmesidir (API'ye webhook olarak gider).
type Notification struct {
	Reference   string `json:"reference"`
	Status      string `json:"status"` // COMPLETED | REJECTED
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

// Notifier bildirimi API'ye iletir.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// SendRequest bankaya gönderim isteğidir.
type SendRequest struct {
	Reference       string `json:"reference"`
	AmountMinor     int64  `json:"amount_minor"`
	Currency        string `json:"currency"`
	BeneficiaryIBAN string `json:"beneficiary_iban"`
}

// Bank dış banka sahtesidir.
type Bank struct {
	cfg      BankConfig
	notifier Notifier
	log      *slog.Logger

	mu        sync.Mutex
	processed map[string]Notification // reference → bankanın gerçek sonucu
	attempts  map[string]int
}

// NewBank yeni bir Bank oluşturur. notifier nil olabilir (bildirim gönderilmez).
func NewBank(cfg BankConfig, n Notifier, log *slog.Logger) *Bank {
	return &Bank{cfg: cfg, notifier: n, log: log, processed: map[string]Notification{}, attempts: map[string]int{}}
}

// Handler bankanın HTTP uçlarıdır: POST /v1/transfers, GET /v1/transfers/{reference}.
func (b *Bank) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/transfers", b.send)
	mux.HandleFunc("GET /v1/transfers/{reference}", b.status)
	return mux
}

type fault int

const (
	none          fault = iota
	timeoutBefore       // bağlantı asılır, transfer işlenmedi
	errorBefore         // 503, transfer işlenmedi
	errorAfter          // transfer işlendi ama 503 döner → belirsiz sonuç
	timeoutAfter        // transfer işlendi ama bağlantı asılır → belirsiz sonuç
)

// rngFor çağrıya özgü deterministik rastgele kaynak: (tohum, referans, çağrı sırası).
func (b *Bank) rngFor(reference string, attempt int) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(reference))
	return rand.New(rand.NewPCG(uint64(b.cfg.Seed), h.Sum64()^uint64(attempt)*0x9e3779b97f4a7c15)) //nolint:gosec // güvenlik amaçlı değil
}

func (b *Bank) send(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.Reference == "" || req.AmountMinor <= 0 {
		writeBank(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	b.mu.Lock()
	b.attempts[req.Reference]++
	attempt := b.attempts[req.Reference]
	b.mu.Unlock()
	rng := b.rngFor(req.Reference, attempt)

	f := none
	if rng.IntN(100) < b.cfg.FailurePercent {
		f = fault(1 + rng.IntN(4)) // dört hata türü eşit olasılıklı
	}
	b.sleep(r.Context(), b.latency(rng))

	process := f == none || f == errorAfter || f == timeoutAfter
	if process {
		b.process(req, rng)
	}
	switch f {
	case timeoutBefore, timeoutAfter:
		b.sleep(r.Context(), b.cfg.Hang) // istemci zaman aşımına uğrar
		panic(http.ErrAbortHandler)      // bağlantı yanıtsız kesilir (boş 200 dönmesin)
	case errorBefore, errorAfter:
		writeBank(w, http.StatusServiceUnavailable, map[string]string{"error": "bank temporarily unavailable"})
		return
	}
	b.mu.Lock()
	n := b.processed[req.Reference]
	b.mu.Unlock()
	writeBank(w, http.StatusOK, n)
}

// process transferi bir kez işler; aynı referans tekrar gelirse mevcut sonucu korur. Geçersiz alıcı IBAN'ı reddedilir.
func (b *Bank) process(req SendRequest, rng *rand.Rand) {
	b.mu.Lock()
	if _, done := b.processed[req.Reference]; done {
		b.mu.Unlock()
		return
	}
	n := Notification{Reference: req.Reference, Status: "COMPLETED", AmountMinor: req.AmountMinor, Currency: req.Currency}
	if !account.ValidIBAN(req.BeneficiaryIBAN) {
		n.Status = "REJECTED"
	}
	b.processed[req.Reference] = n
	b.mu.Unlock()

	if b.notifier == nil {
		return
	}
	copies := 1
	if rng.IntN(100) < b.cfg.DuplicatePercent {
		copies = 2 // çift cevap: aynı bildirim iki kez gelir
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		b.sleep(ctx, b.latency(rng)) // bildirim yanıttan bağımsız, biraz sonra gelir
		for range copies {
			if err := b.notifier.Notify(ctx, n); err != nil {
				b.log.Debug("bank notification failed", slog.String("reference", n.Reference), slog.Any("error", err))
			}
		}
	}()
}

// status bankanın gerçek durumunu verir: COMPLETED, REJECTED ya da NOT_FOUND (hiç işlenmedi). Sorgulamada hata üretilmez:
// belirsiz sonucu çözmenin bir yolu olmalı.
func (b *Bank) status(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("reference")
	b.sleep(r.Context(), b.latency(b.rngFor(ref, 0)))
	b.mu.Lock()
	n, ok := b.processed[ref]
	b.mu.Unlock()
	if !ok {
		writeBank(w, http.StatusOK, map[string]string{"reference": ref, "status": "NOT_FOUND"})
		return
	}
	writeBank(w, http.StatusOK, n)
}

func (b *Bank) latency(rng *rand.Rand) time.Duration {
	span := b.cfg.MaxLatency - b.cfg.MinLatency
	if span <= 0 {
		return b.cfg.MinLatency
	}
	return b.cfg.MinLatency + time.Duration(rng.Int64N(int64(span)))
}

func (b *Bank) sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func writeBank(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
