package verify

// F-3 ve T-2 kontrolleri: dış banka transferleri (gerçek banka sahtesine karşı: %20 hata, %10 çift cevap), idempotency ve saga.

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

// externalIBAN geçerli kontrol haneli, bankanın dışında bir TR IBAN üretir (account.NewIBAN ile aynı biçim; verify sistemi dışarıdan dener).
func externalIBAN(n int64) string {
	bban := fmt.Sprintf("009990%016d", n)
	var digits strings.Builder
	for _, r := range bban + "TR00" {
		if r >= 'A' && r <= 'Z' {
			fmt.Fprintf(&digits, "%d", r-'A'+10)
		} else {
			digits.WriteRune(r)
		}
	}
	v, _ := new(big.Int).SetString(digits.String(), 10)
	return fmt.Sprintf("TR%02d%s", 98-new(big.Int).Mod(v, big.NewInt(97)).Int64(), bban)
}

// pickSimAccounts simülatör müşterilerinden (demo hesaplar hariç) bakiyesi yeterli vadesiz TRY hesapları seçer.
func (e *Env) pickSimAccounts(ctx context.Context, ops, demo *Session, n int) ([]acct, error) {
	demoIDs := map[string]bool{}
	if err := e.accountsPage(ctx, "/api/v1/accounts", demo.AccessToken, func(a acct) bool { demoIDs[a.ID] = true; return false }); err != nil {
		return nil, err
	}
	var out []acct
	err := e.accountsPage(ctx, "/api/v1/accounts", ops.AccessToken, func(a acct) bool {
		if a.CustomerID != nil && a.Kind == "CHECKING" && a.Currency == "TRY" && a.BalanceMinor >= 10_000_00 && !demoIDs[a.ID] {
			out = append(out, a)
		}
		return len(out) == n
	})
	if err == nil && len(out) < n {
		err = fmt.Errorf("simülatör verisinde %d uygun hesap var, %d gerekli", len(out), n)
	}
	return out, err
}

// internalBalance bankanın iç hesabının bakiyesi (ör. EXTERNAL_CLEARING, NOSTRO; TRY).
func (e *Env) internalBalance(ctx context.Context, ops, kind string) (int64, error) {
	var bal int64
	found := false
	err := e.accountsPage(ctx, "/api/v1/accounts", ops, func(a acct) bool {
		if a.CustomerID == nil && a.Kind == kind && a.Currency == "TRY" {
			bal, found = a.BalanceMinor, true
		}
		return found
	})
	if err == nil && !found {
		err = fmt.Errorf("%s/TRY iç hesabı bulunamadı", kind)
	}
	return bal, err
}

type xfer struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	FromAccountID string `json:"from_account_id"`
	AmountMinor   int64  `json:"amount_minor"`
}

// sendExternal dış banka transferi başlatır (202 RESERVED beklenir).
func (e *Env) sendExternal(ctx context.Context, token, key, typ, fromID, iban string, minor int64) (xfer, error) {
	resp, err := e.postKeyed(ctx, "/api/v1/transfers", token, key,
		map[string]any{"type": typ, "from_account_id": fromID, "to_iban": iban, "amount_minor": minor, "currency": "TRY"})
	if err != nil {
		return xfer{}, err
	}
	if err := expectStatus("dış transfer", resp, http.StatusAccepted, ""); err != nil {
		return xfer{}, err
	}
	var t xfer
	if err := resp.JSON(&t); err != nil {
		return xfer{}, err
	}
	if t.State != "RESERVED" {
		return t, fmt.Errorf("yeni dış transfer RESERVED olmalı, %s", t.State)
	}
	return t, nil
}

// awaitSettled bütün transferler COMPLETED ya da REVERSED olana kadar bekler; son durumları döndürür.
func (e *Env) awaitSettled(ctx context.Context, token string, ts []xfer, limit time.Duration) (map[string]string, error) {
	deadline := time.Now().Add(limit)
	final := map[string]string{}
	for {
		var pending []string
		var mu sync.Mutex
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(16)
		for _, t := range ts {
			if final[t.ID] != "" {
				continue
			}
			g.Go(func() error {
				var cur xfer
				if err := e.getJSON(gctx, "GET /transfers/"+t.ID, http.MethodGet, "/api/v1/transfers/"+t.ID, token, nil, http.StatusOK, &cur); err != nil {
					return err
				}
				mu.Lock()
				defer mu.Unlock()
				if cur.State == "COMPLETED" || cur.State == "REVERSED" {
					final[t.ID] = cur.State
				} else {
					pending = append(pending, cur.State)
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
		if len(pending) == 0 {
			return final, nil
		}
		if time.Now().After(deadline) {
			return final, fmt.Errorf("%d transfer %s içinde sonuçlanmadı (durumlar: %v)", len(pending), limit, pending)
		}
		select {
		case <-ctx.Done():
			return final, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func sumCompleted(ts []xfer, final map[string]string) (completed, reversed int, perAccount map[string]int64) {
	perAccount = map[string]int64{}
	for _, t := range ts {
		if final[t.ID] == "COMPLETED" {
			completed++
			perAccount[t.FromAccountID] += t.AmountMinor
		} else {
			reversed++
		}
	}
	return
}

func checkF3(ctx context.Context, env *Env) (string, error) {
	ops, err := env.Login(ctx, "operasyon@parakopru.test")
	if err != nil {
		return "", err
	}
	demo, err := env.Login(ctx, "musteri@parakopru.test")
	if err != nil {
		return "", err
	}
	sys, err := env.Login(ctx, "sistem@parakopru.test")
	if err != nil {
		return "", err
	}
	accs, err := env.pickSimAccounts(ctx, ops, demo, 2)
	if err != nil {
		return "", err
	}
	a, b := accs[0], accs[1]
	run := time.Now().UnixNano()

	// Dış transfer (EFT ve FAST): ayrılır, bankaya gönderilir, sonuçlanır; iade edilenlerin parası döner.
	var ts []xfer
	for i, typ := range []string{"EFT", "FAST"} {
		t, err := env.sendExternal(ctx, ops.AccessToken, fmt.Sprintf("f3-%d-%d", run, i), typ, a.ID, externalIBAN(800_000_000+run%1000+int64(i)), 1_234_56)
		if err != nil {
			return "", err
		}
		ts = append(ts, t)
	}
	final, err := env.awaitSettled(ctx, ops.AccessToken, ts, 90*time.Second)
	if err != nil {
		return "", err
	}
	completed, reversed, paid := sumCompleted(ts, final)
	after, err := env.account(ctx, ops.AccessToken, a.ID)
	if err != nil {
		return "", err
	}
	if want := a.BalanceMinor - paid[a.ID]; after.BalanceMinor != want {
		return "", fmt.Errorf("gönderen bakiyesi %d, beklenen %d (tamamlanan %d, iade %d)", after.BalanceMinor, want, completed, reversed)
	}

	// Kurallar: bozuk IBAN 400, farklı para birimi 422, yetersiz bakiye 422.
	for _, tc := range []struct {
		what   string
		body   map[string]any
		status int
		code   string
	}{
		{"bozuk IBAN", map[string]any{"from_account_id": a.ID, "to_iban": "TR000000000000000000000000", "amount_minor": 100, "currency": "TRY"}, http.StatusBadRequest, "INVALID_IBAN"},
		{"farklı para birimi", map[string]any{"from_account_id": a.ID, "to_iban": externalIBAN(900_000_001), "amount_minor": 100, "currency": "USD"}, http.StatusUnprocessableEntity, "CURRENCY_MISMATCH"},
		{"yetersiz bakiye", map[string]any{"from_account_id": a.ID, "to_iban": externalIBAN(900_000_002), "amount_minor": 999_999_999_99, "currency": "TRY"}, http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS"},
	} {
		resp, err := env.postKeyed(ctx, "/api/v1/transfers", ops.AccessToken, fmt.Sprintf("f3r-%d-%s", run, tc.code), tc.body)
		if err != nil {
			return "", err
		}
		if err := expectStatus(tc.what, resp, tc.status, tc.code); err != nil {
			return "", err
		}
	}

	// Gelen dış transfer: alıcı alacaklanır; banka aynı bildirimi iki kez gönderirse bir kez yazılır; yalnız sistem rolü.
	inc := map[string]any{"reference": fmt.Sprintf("f3-in-%d", run), "beneficiary_iban": *b.IBAN, "amount_minor": 777_00, "currency": "TRY"}
	for i, wantDup := range []bool{false, true} {
		var out struct {
			Duplicate bool `json:"duplicate"`
		}
		if err := env.getJSON(ctx, "gelen transfer", http.MethodPost, "/api/v1/webhooks/bank/incoming", sys.AccessToken, inc, http.StatusOK, &out); err != nil {
			return "", err
		}
		if out.Duplicate != wantDup {
			return "", fmt.Errorf("gelen transfer %d. bildirim: duplicate=%v, beklenen %v", i+1, out.Duplicate, wantDup)
		}
	}
	bAfter, err := env.account(ctx, ops.AccessToken, b.ID)
	if err != nil {
		return "", err
	}
	if bAfter.BalanceMinor != b.BalanceMinor+777_00 {
		return "", fmt.Errorf("alıcı bakiyesi %d, beklenen %d (çift bildirim iki kez yazılmış olabilir)", bAfter.BalanceMinor, b.BalanceMinor+777_00)
	}
	forbidden, err := env.API(ctx, http.MethodPost, "/api/v1/webhooks/bank/incoming", ops.AccessToken, inc)
	if err != nil {
		return "", err
	}
	if err := expectStatus("operasyon banka bildirimi", forbidden, http.StatusForbidden, "FORBIDDEN"); err != nil {
		return "", err
	}
	if err := env.requireInvariants(ctx, ops.AccessToken); err != nil {
		return "", err
	}
	return fmt.Sprintf("EFT + FAST dış transfer sonuçlandı (%d tamamlandı, %d iade); bozuk IBAN 400, para birimi/bakiye 422; gelen transfer çift bildirimde bir kez yazıldı; bildirim yalnız sistem rolünde", completed, reversed), nil
}

func checkT2(ctx context.Context, env *Env) (string, error) {
	const n, parallel, sources = 100, 24, 10
	ops, err := env.Login(ctx, "operasyon@parakopru.test")
	if err != nil {
		return "", err
	}
	demo, err := env.Login(ctx, "musteri@parakopru.test")
	if err != nil {
		return "", err
	}
	sys, err := env.Login(ctx, "sistem@parakopru.test")
	if err != nil {
		return "", err
	}
	accs, err := env.pickSimAccounts(ctx, ops, demo, sources+2)
	if err != nil {
		return "", err
	}
	run := time.Now().UnixNano()

	// 1. Aynı Idempotency-Key ile 20 eşzamanlı istek: tek transfer, tek para hareketi; farklı gövde 422; anahtar yoksa 400.
	from, to := accs[sources], accs[sources+1]
	body := map[string]any{"from_account_id": from.ID, "to_iban": *to.IBAN, "amount_minor": 321_00, "currency": "TRY"}
	var fresh atomic.Int32
	ids := make([]string, 20)
	g, gctx := errgroup.WithContext(ctx)
	for i := range ids {
		g.Go(func() error {
			resp, err := env.postKeyed(gctx, "/api/v1/transfers", ops.AccessToken, fmt.Sprintf("t2-same-%d", run), body)
			if err != nil {
				return err
			}
			if err := expectStatus("aynı anahtar", resp, http.StatusCreated, ""); err != nil {
				return err
			}
			if resp.Header.Get("Idempotent-Replayed") != "true" {
				fresh.Add(1)
			}
			var t xfer
			if err := resp.JSON(&t); err != nil {
				return err
			}
			ids[i] = t.ID
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return "", err
	}
	for _, id := range ids {
		if id != ids[0] {
			return "", fmt.Errorf("aynı anahtar farklı transfer kimlikleri döndürdü: %s ≠ %s", id, ids[0])
		}
	}
	if fresh.Load() != 1 {
		return "", fmt.Errorf("işlemi yapan istek sayısı %d (1 olmalı; diğerleri Idempotent-Replayed)", fresh.Load())
	}
	fromAfter, err := env.account(ctx, ops.AccessToken, from.ID)
	if err != nil {
		return "", err
	}
	if fromAfter.BalanceMinor != from.BalanceMinor-321_00 {
		return "", fmt.Errorf("20 istekten sonra bakiye %d → %d: para bir kez düşmeliydi", from.BalanceMinor, fromAfter.BalanceMinor)
	}
	body["amount_minor"] = 322_00
	reuse, err := env.postKeyed(ctx, "/api/v1/transfers", ops.AccessToken, fmt.Sprintf("t2-same-%d", run), body)
	if err != nil {
		return "", err
	}
	if err := expectStatus("aynı anahtar, farklı gövde", reuse, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED"); err != nil {
		return "", err
	}
	nokey, err := env.API(ctx, http.MethodPost, "/api/v1/transfers", ops.AccessToken, body)
	if err != nil {
		return "", err
	}
	if err := expectStatus("anahtarsız istek", nokey, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED"); err != nil {
		return "", err
	}

	// 2. Saga: 100 dış transfer eşzamanlı; banka %20 hata ve %10 çift cevap üretir. Hepsi COMPLETED ya da REVERSED olmalı.
	clearing0, err := env.internalBalance(ctx, ops.AccessToken, "EXTERNAL_CLEARING")
	if err != nil {
		return "", err
	}
	nostro0, err := env.internalBalance(ctx, ops.AccessToken, "NOSTRO")
	if err != nil {
		return "", err
	}
	srcs := accs[:sources]
	ts := make([]xfer, n)
	var next atomic.Int64
	g, gctx = errgroup.WithContext(ctx)
	for range parallel {
		g.Go(func() error {
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return nil
				}
				t, err := env.sendExternal(gctx, ops.AccessToken, fmt.Sprintf("t2-%d-%d", run, i), "FAST", srcs[i%sources].ID, externalIBAN(700_000_000+int64(i)), int64(100+i)*100)
				if err != nil {
					return err
				}
				ts[i] = t
			}
		})
	}
	if err := g.Wait(); err != nil {
		return "", err
	}
	final, err := env.awaitSettled(ctx, ops.AccessToken, ts, 3*time.Minute)
	if err != nil {
		return "", err
	}
	completed, reversed, paid := sumCompleted(ts, final)

	// Para korunumu: her kaynak hesap yalnız tamamlanan transferler kadar azaldı; takas (uçuştakiler) 0; nostro tamamlananlar kadar azaldı.
	var total int64
	for _, a := range srcs {
		now, err := env.account(ctx, ops.AccessToken, a.ID)
		if err != nil {
			return "", err
		}
		if want := a.BalanceMinor - paid[a.ID]; now.BalanceMinor != want {
			return "", fmt.Errorf("hesap %s: bakiye %d, beklenen %d", a.ID, now.BalanceMinor, want)
		}
	}
	for _, v := range paid {
		total += v
	}
	clearing1, err := env.internalBalance(ctx, ops.AccessToken, "EXTERNAL_CLEARING")
	if err != nil {
		return "", err
	}
	nostro1, err := env.internalBalance(ctx, ops.AccessToken, "NOSTRO")
	if err != nil {
		return "", err
	}
	if clearing1 != clearing0 {
		return "", fmt.Errorf("EXTERNAL_CLEARING %d → %d: sonuçlanan transferlerden sonra değişmemeli", clearing0, clearing1)
	}
	if nostro1 != nostro0-total {
		return "", fmt.Errorf("NOSTRO %d → %d, beklenen %d", nostro0, nostro1, nostro0-total)
	}

	// 3. Çift ve çelişkili bildirim: sonuçlanmış transfer değişmez; tutar uyuşmazlığı 422; operasyon bildirim gönderemez.
	var done xfer
	for _, t := range ts {
		if final[t.ID] == "COMPLETED" {
			done = t
			break
		}
	}
	if done.ID == "" {
		return "", fmt.Errorf("100 transferin hiçbiri tamamlanmadı (%d iade)", reversed)
	}
	for _, status := range []string{"COMPLETED", "REJECTED"} {
		hook := map[string]any{"reference": done.ID, "status": status, "amount_minor": done.AmountMinor, "currency": "TRY"}
		if err := env.getJSON(ctx, "banka bildirimi "+status, http.MethodPost, "/api/v1/webhooks/bank/transfer-status", sys.AccessToken, hook, http.StatusOK, nil); err != nil {
			return "", err
		}
	}
	bad, err := env.API(ctx, http.MethodPost, "/api/v1/webhooks/bank/transfer-status", sys.AccessToken,
		map[string]any{"reference": done.ID, "status": "COMPLETED", "amount_minor": done.AmountMinor + 1, "currency": "TRY"})
	if err != nil {
		return "", err
	}
	if err := expectStatus("tutarı uyuşmayan bildirim", bad, http.StatusUnprocessableEntity, "BANK_RESULT_MISMATCH"); err != nil {
		return "", err
	}
	var cur xfer
	if err := env.getJSON(ctx, "transfer", http.MethodGet, "/api/v1/transfers/"+done.ID, ops.AccessToken, nil, http.StatusOK, &cur); err != nil {
		return "", err
	}
	if cur.State != "COMPLETED" {
		return "", fmt.Errorf("çelişkili bildirimden sonra transfer %s (COMPLETED kalmalı)", cur.State)
	}
	nostro2, err := env.internalBalance(ctx, ops.AccessToken, "NOSTRO")
	if err != nil {
		return "", err
	}
	if nostro2 != nostro1 {
		return "", fmt.Errorf("tekrar bildirimler para hareketi yaptı: NOSTRO %d → %d", nostro1, nostro2)
	}
	if err := env.requireInvariants(ctx, ops.AccessToken); err != nil {
		return "", err
	}
	return fmt.Sprintf("aynı anahtarla 20 eşzamanlı istek tek transfer; %d dış transfer banka hatalarına rağmen sonuçlandı (%d tamamlandı, %d telafi/ters kayıt); takas 0, nostro ve bakiyeler tutuyor; çift/çelişkili bildirim para hareketi yapmadı", n, completed, reversed), nil
}
