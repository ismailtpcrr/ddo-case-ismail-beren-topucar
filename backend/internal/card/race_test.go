package card_test

// T-3 kanıtı (proje.md Görev 3): eşzamanlı provizyon / çekim / iade / süre aşımı yarışlarında
// kullanılabilir bakiye = bakiye − aktif bloklar her zaman doğru kalır, hiç eksiye düşmez ve para korunur.

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"parakopru/internal/card"
	"parakopru/internal/money"
)

func (f fixture) deadlocks(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()`).Scan(&n))
	return n
}

// Kullanılabilir bakiyeden fazlası bloke edilemez: 100 eşzamanlı provizyon, bakiye yalnız 20'sine yetiyor.
func TestConcurrentAuthorizationsNeverExceedAvailable(t *testing.T) {
	f := newFixture(t)
	c, a := f.newCard(t, money.TRY, 1_000_00)
	var ok, poor atomic.Int32
	var g errgroup.Group
	for range 100 {
		g.Go(func() error {
			_, _, err := f.cards.Authorize(t.Context(), f.sys, "race-auth-"+uuid.NewString(), c.ID,
				card.AuthorizeInput{AmountMinor: 50_00, Currency: money.TRY, Merchant: "Yarış"})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, card.ErrInsufficient):
				poor.Add(1)
			default:
				return err
			}
			return nil
		})
	}
	require.NoError(t, g.Wait())
	require.Equal(t, int32(20), ok.Load(), "1.000 TL / 50 TL = tam 20 provizyon")
	require.Equal(t, int32(80), poor.Load())
	got := f.acct(t, a.ID)
	require.Equal(t, int64(1_000_00), got.HeldMinor)
	require.Zero(t, got.AvailableMinor())
	f.requireInvariants(t)
}

// Karışık yarış: aynı hesapta eşzamanlı provizyon, kısmi/final çekim, iade ve süre aşımı. Her adımdan sonra değil, sonunda
// ve (ayrı bir izleyici ile) süren yarış sırasında kullanılabilir bakiye eksiye düşmemeli.
func TestMixedRaceKeepsAvailableBalanceCorrect(t *testing.T) {
	f := newFixture(t)
	const opening = 2_000_00
	c, a := f.newCard(t, money.TRY, opening)
	dl0 := f.deadlocks(t)

	stop := make(chan struct{})
	var violations atomic.Int32
	watcher := make(chan struct{})
	go func() { // izleyici: veritabanı kısıtı dışında, uygulama düzeyinde de eksi kullanılabilir bakiye görülmemeli
		defer close(watcher)
		for {
			select {
			case <-stop:
				return
			default:
			}
			got, err := f.accounts.ByID(context.Background(), nil, a.ID)
			if err == nil && (got.AvailableMinor() < 0 || got.HeldMinor < 0) {
				violations.Add(1)
			}
			time.Sleep(time.Millisecond)
		}
	}()

	var holdsMu = make(chan card.Hold, 4000)
	var captured, refunded atomic.Int64
	var g errgroup.Group
	for w := range 16 {
		g.Go(func() error {
			rng := rand.New(rand.NewPCG(uint64(w), 3)) //nolint:gosec // test verisi
			for range 60 {
				switch rng.IntN(5) {
				case 0, 1: // provizyon (bir kısmı kısa ömürlü: süre aşımı yarışı)
					ttl := 0
					if rng.IntN(3) == 0 {
						ttl = 1
					}
					h, _, err := f.cards.Authorize(t.Context(), f.sys, "mix-a-"+uuid.NewString(), c.ID,
						card.AuthorizeInput{AmountMinor: int64(1_00 + rng.IntN(80_00)), Currency: money.TRY, TTLSeconds: ttl})
					if err == nil {
						holdsMu <- h
					} else if !errors.Is(err, card.ErrInsufficient) {
						return err
					}
				case 2, 3: // çekim (kısmi ya da final) rastgele bir provizyondan
					select {
					case h := <-holdsMu:
						amt := int64(1 + rng.IntN(int(h.AmountMinor)))
						res, _, err := f.cards.Capture(t.Context(), f.sys, "mix-c-"+uuid.NewString(), h.ID, card.CaptureInput{AmountMinor: amt, Final: rng.IntN(2) == 0})
						switch {
						case err == nil:
							captured.Add(amt)
							if res.Hold.State == card.Active {
								holdsMu <- res.Hold
							}
							if rng.IntN(2) == 0 { // iade: çekilenin bir kısmı
								ra := int64(1 + rng.IntN(int(amt)))
								if _, _, err := f.cards.Refund(t.Context(), f.sys, "mix-r-"+uuid.NewString(), res.Capture.ID, card.RefundInput{AmountMinor: ra}); err != nil {
									return err
								}
								refunded.Add(ra)
							}
						case errors.Is(err, card.ErrHoldNotActive), errors.Is(err, card.ErrExceedsHold):
							// süresi dolmuş ya da başka işlem çekmiş: beklenen yarış sonucu
						default:
							return err
						}
					default:
					}
				default: // süre aşımı turu
					if _, err := f.cards.ExpireDue(t.Context()); err != nil {
						return err
					}
				}
			}
			return nil
		})
	}
	require.NoError(t, g.Wait())
	time.Sleep(1100 * time.Millisecond)
	_, err := f.cards.ExpireDue(t.Context())
	require.NoError(t, err)
	close(stop)
	<-watcher

	require.Zero(t, violations.Load(), "yarış sırasında kullanılabilir bakiye eksiye düşmedi")
	require.Zero(t, f.deadlocks(t)-dl0, "sıralı kilit: deadlock yok")
	got := f.acct(t, a.ID)
	require.Equal(t, int64(opening)-captured.Load()+refunded.Load(), got.BalanceMinor, "bakiye = açılış − çekilen + iade edilen")
	require.GreaterOrEqual(t, got.AvailableMinor(), int64(0))
	var activeSum int64
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT coalesce(sum(remaining_minor), 0) FROM holds WHERE account_id = $1 AND state = 'ACTIVE'`, a.ID).Scan(&activeSum))
	require.Equal(t, activeSum, got.HeldMinor, "held_minor = Σ aktif provizyon kalanı")
	var bad int
	require.NoError(t, f.env.App.QueryRow(t.Context(), `SELECT count(*) FROM holds WHERE account_id = $1 AND captured_minor + released_minor + remaining_minor <> amount_minor`, a.ID).Scan(&bad))
	require.Zero(t, bad)
	f.requireInvariants(t)
	t.Logf("çekilen %d, iade %d, bakiye %d, bloke %d, kullanılabilir %d; deadlock 0", captured.Load(), refunded.Load(), got.BalanceMinor, got.HeldMinor, got.AvailableMinor())
}

// Aynı provizyondan eşzamanlı çekimler: toplam çekilen provizyon tutarını geçemez.
func TestConcurrentCapturesCannotExceedHold(t *testing.T) {
	f := newFixture(t)
	c, a := f.newCard(t, money.TRY, 500_00)
	h := f.authorize(t, c, 100_00, "cap-race-auth")
	var ok atomic.Int32
	var g errgroup.Group
	for range 30 {
		g.Go(func() error {
			_, _, err := f.cards.Capture(t.Context(), f.sys, "cap-race-"+uuid.NewString(), h.ID, card.CaptureInput{AmountMinor: 10_00})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, card.ErrHoldNotActive), errors.Is(err, card.ErrExceedsHold):
			default:
				return err
			}
			return nil
		})
	}
	require.NoError(t, g.Wait())
	require.Equal(t, int32(10), ok.Load(), "100 TL / 10 TL = tam 10 çekim")
	got := f.acct(t, a.ID)
	require.Equal(t, int64(400_00), got.BalanceMinor)
	require.Zero(t, got.HeldMinor)
	f.requireInvariants(t)
}

// Aynı çekimden eşzamanlı iadeler: toplam iade çekilenden fazla olamaz.
func TestConcurrentRefundsCannotExceedCapture(t *testing.T) {
	f := newFixture(t)
	c, a := f.newCard(t, money.TRY, 500_00)
	h := f.authorize(t, c, 100_00, "ref-race-auth")
	res, _, err := f.cards.Capture(t.Context(), f.sys, "ref-race-cap", h.ID, card.CaptureInput{AmountMinor: 100_00, Final: true})
	require.NoError(t, err)
	var ok atomic.Int32
	var g errgroup.Group
	for range 30 {
		g.Go(func() error {
			_, _, err := f.cards.Refund(t.Context(), f.sys, "ref-race-"+uuid.NewString(), res.Capture.ID, card.RefundInput{AmountMinor: 15_00})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, card.ErrExceedsCapture):
			default:
				return err
			}
			return nil
		})
	}
	require.NoError(t, g.Wait())
	require.Equal(t, int32(6), ok.Load(), "100 TL / 15 TL = 6 iade (90 TL); 7.'si 10 TL kalanı aşar")
	require.Equal(t, int64(490_00), f.acct(t, a.ID).BalanceMinor)
	f.requireInvariants(t)
}
