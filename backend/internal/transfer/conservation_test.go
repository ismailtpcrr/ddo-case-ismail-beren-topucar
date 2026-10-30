package transfer_test

// T-1 kanıtı (proje.md Görev 1): 10.000 eşzamanlı transferden sonra tüm hesapların toplamı değişmez, bakiye özeti
// defterden türetilen bakiyeye eşittir ve deadlock oluşmaz. Property testi aynı değişmezleri rastgele işlem dizilerinde dener.

import (
	"context"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"pgregory.net/rapid"

	"parakopru/internal/account"
	"parakopru/internal/money"
	"parakopru/internal/transfer"
)

func (f fixture) deadlocks(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.env.App.QueryRow(t.Context(),
		`SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()`).Scan(&n))
	return n
}

// Her transfer yeni bir Idempotency-Key ile, tam servis yolundan (idempotency + defter + denetim) geçer.
// Deadlock sayacı PostgreSQL'in kendi istatistiğinden okunur: TxManager deadlock'u yeniden deneyip gizleyebileceği
// için hata sayısına değil sunucunun gördüğü deadlock sayısına bakılır.
func TestConservationUnder10000ConcurrentTransfers(t *testing.T) {
	if testing.Short() {
		t.Skip("uzun test")
	}
	const (
		accounts  = 40
		transfers = 10_000
		workers   = 64
		opening   = 5_000_00 // hesap başına 5.000 TL: bazı transferler bakiye yetmediği için reddedilir
	)
	f := newFixture(t)
	accs := make([]account.Account, accounts)
	for i := range accs {
		_, accs[i] = f.owned(t, money.TRY, opening)
	}
	// Bakiyesi sıfır ve para birimi farklı hesaplar yan etki: toplam yalnız bu 40 hesaptan ölçülür.
	want := int64(accounts) * opening
	deadlocksBefore := f.deadlocks(t)

	var completed, rejected atomic.Int64
	results := make([]transfer.Transfer, transfers)
	jobs := make(chan int)
	var g errgroup.Group
	start := time.Now()
	for w := range workers {
		g.Go(func() error {
			rng := rand.New(rand.NewPCG(uint64(w), 7)) //nolint:gosec // test verisi
			for i := range jobs {
				a := rng.IntN(accounts)
				b := rng.IntN(accounts - 1)
				if b >= a { // kendine gönderme yok; ters yönlü çiftler sık çıkar
					b++
				}
				tr, _, err := f.transfer.Create(t.Context(), f.ops, uuid.NewString(), transfer.CreateInput{
					FromAccountID: accs[a].ID, ToIBAN: *accs[b].IBAN, Currency: money.TRY,
					AmountMinor: int64(1 + rng.IntN(120_000)), // 0,01 – 1.200 TL
				})
				if err != nil {
					return err
				}
				results[i] = tr
				if tr.State == transfer.Completed {
					completed.Add(1)
				} else {
					rejected.Add(1)
				}
			}
			return nil
		})
	}
	for i := range transfers {
		jobs <- i
	}
	close(jobs)
	require.NoError(t, g.Wait())
	elapsed := time.Since(start)

	// 1. Hiç deadlock olmadı.
	require.Zero(t, f.deadlocks(t)-deadlocksBefore, "sıralı kilit varken deadlock olmamalı")

	// 2. Her transfer ya tamamlandı ya reddedildi; hiçbiri yarım kalmadı.
	require.Equal(t, int64(transfers), completed.Load()+rejected.Load())
	require.Positive(t, completed.Load())
	require.Positive(t, rejected.Load(), "bakiye yetersizliği de denenmiş olmalı")

	// 3. Tüm hesapların toplamı değişmedi.
	expected := map[uuid.UUID]int64{}
	for _, a := range accs {
		expected[a.ID] = opening
	}
	for _, tr := range results {
		if tr.State == transfer.Completed {
			expected[tr.FromAccountID] -= tr.AmountMinor
			expected[*tr.ToAccountID] += tr.AmountMinor
		}
	}
	var total int64
	for _, a := range accs {
		got := f.balance(t, a.ID)
		require.Equal(t, expected[a.ID], got, "hesap %s: bakiye = açılış − giden + gelen", a.ID)
		require.GreaterOrEqual(t, got, int64(0))
		total += got
	}
	require.Equal(t, want, total, "10.000 transferden sonra toplam para değişmedi")

	// 4. Defter dengeli ve özet = türetilmiş bakiye (tüm veritabanında).
	inv, err := f.ledger.CheckInvariants(t.Context())
	require.NoError(t, err)
	require.True(t, inv.OK, "%+v", inv)
	for _, a := range accs {
		require.Equal(t, f.balance(t, a.ID), derived(t, f, a.ID))
	}

	t.Logf("%d transfer, %d eşzamanlı işçi: %d tamamlandı, %d bakiye yetersiz → REJECTED; %s (%d transfer/sn); deadlock: 0; toplam %d → %d",
		transfers, workers, completed.Load(), rejected.Load(), elapsed.Round(time.Millisecond), int64(transfers)*1000/max(elapsed.Milliseconds(), 1), want, total)
}

func derived(t *testing.T, f fixture, id uuid.UUID) int64 {
	t.Helper()
	var b int64
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT coalesce(-sum(amount_minor), 0) FROM postings WHERE account_id = $1`, id).Scan(&b))
	return b // müşteri hesapları CREDIT doğal taraflı
}

// Property: rastgele (tutar, gönderen, alıcı) dizilerinde defter dengede kalır, toplam para değişmez, hiçbir müşteri
// hesabı eksiye düşmez, bir transfer ancak bakiye yetiyorsa tamamlanır ve özet her zaman defterden türetilen bakiyeye eşittir.
func TestPropertyRandomTransferSequencesConserveMoney(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(2, 5).Draw(rt, "hesap sayısı")
		accs := make([]account.Account, n)
		model := make([]int64, n) // beklenen bakiyeler
		for i := range accs {
			open := rapid.Int64Range(0, 100_000).Draw(rt, "açılış")
			_, accs[i] = f.owned(t, money.TRY, open)
			model[i] = open
		}
		var sum int64
		for _, m := range model {
			sum += m
		}
		steps := rapid.IntRange(1, 25).Draw(rt, "işlem sayısı")
		for range steps {
			from := rapid.IntRange(0, n-1).Draw(rt, "gönderen")
			to := rapid.IntRange(0, n-1).Filter(func(v int) bool { return v != from }).Draw(rt, "alıcı")
			amount := rapid.Int64Range(1, 150_000).Draw(rt, "tutar")
			tr, _, err := f.transfer.Create(ctx, f.ops, uuid.NewString(), transfer.CreateInput{
				FromAccountID: accs[from].ID, ToIBAN: *accs[to].IBAN, Currency: money.TRY, AmountMinor: amount})
			if err != nil {
				rt.Fatalf("beklenmeyen hata: %v", err)
			}
			if wantOK := model[from] >= amount; wantOK != (tr.State == transfer.Completed) {
				rt.Fatalf("bakiye %d, tutar %d: durum %s", model[from], amount, tr.State)
			}
			if tr.State == transfer.Completed {
				model[from] -= amount
				model[to] += amount
			}
		}
		var total int64
		for i, a := range accs {
			got := f.balance(t, a.ID)
			if got != model[i] || got < 0 || got != derived(t, f, a.ID) {
				rt.Fatalf("hesap %d: özet %d, model %d, defter %d", i, got, model[i], derived(t, f, a.ID))
			}
			total += got
		}
		if total != sum {
			rt.Fatalf("toplam değişti: %d → %d", sum, total)
		}
	})
	inv, err := f.ledger.CheckInvariants(ctx)
	require.NoError(t, err)
	require.True(t, inv.OK, "%+v", inv)
}
