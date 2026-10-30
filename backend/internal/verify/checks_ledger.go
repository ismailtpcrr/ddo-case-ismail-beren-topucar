package verify

// F-1, F-2 ve T-1 kontrolleri (hesaplar, çift taraflı defter, para korunumu). Hepsi API üzerinden, gerçek istekle çalışır.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

type acct struct {
	ID           string  `json:"id"`
	CustomerID   *string `json:"customer_id"`
	Kind         string  `json:"kind"`
	Currency     string  `json:"currency"`
	Name         string  `json:"name"`
	IBAN         *string `json:"iban"`
	MaturityDate *string `json:"maturity_date"`
	BalanceMinor int64   `json:"balance_minor"`
}

type transferResp struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	FromAccountID string `json:"from_account_id"`
	ToAccountID   string `json:"to_account_id"`
	AmountMinor   int64  `json:"amount_minor"`
}

type entryRow struct {
	EntryID            string `json:"entry_id"`
	Kind               string `json:"kind"`
	Side               string `json:"side"`
	AmountMinor        int64  `json:"amount_minor"`
	BalanceEffectMinor int64  `json:"balance_effect_minor"`
}

var ibanPattern = regexp.MustCompile(`^TR[0-9]{24}$`)

// getJSON isteği gönderir, beklenen durumu doğrular ve gövdeyi v'ye çözer.
func (e *Env) getJSON(ctx context.Context, what, method, path, token string, body any, want int, v any) error {
	resp, err := e.API(ctx, method, path, token, body)
	if err != nil {
		return err
	}
	if err := expectStatus(what, resp, want, ""); err != nil {
		return err
	}
	if v != nil {
		return resp.JSON(v)
	}
	return nil
}

// postKeyed Idempotency-Key ile POST gönderir.
func (e *Env) postKeyed(ctx context.Context, path, token, key string, body any) (Response, error) {
	return e.Do(ctx, Request{Method: http.MethodPost, URL: e.APIURL + path, Token: token, Body: body, Header: http.Header{"Idempotency-Key": {key}}})
}

// pageAll bir liste ucunu bütün sayfalarıyla okur; her sayfadaki öğeleri fn'e verir.
func (e *Env) pageAll(ctx context.Context, path, token string, fn func(raw []byte) (stop bool, err error)) error {
	cursor := ""
	for {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		url := path + sep + "limit=200"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		resp, err := e.API(ctx, http.MethodGet, url, token, nil)
		if err != nil {
			return err
		}
		if err := expectStatus("GET "+path, resp, http.StatusOK, ""); err != nil {
			return err
		}
		stop, err := fn(resp.Body)
		if err != nil || stop {
			return err
		}
		var p struct {
			NextCursor *string `json:"next_cursor"`
		}
		if err := resp.JSON(&p); err != nil {
			return err
		}
		if p.NextCursor == nil {
			return nil
		}
		cursor = *p.NextCursor
	}
}

func (e *Env) accountsPage(ctx context.Context, path, token string, fn func(a acct) (stop bool)) error {
	return e.pageAll(ctx, path, token, func(raw []byte) (bool, error) {
		var p struct {
			Items []acct `json:"items"`
		}
		if err := (Response{Body: raw}).JSON(&p); err != nil {
			return false, err
		}
		for _, a := range p.Items {
			if fn(a) {
				return true, nil
			}
		}
		return false, nil
	})
}

func (e *Env) account(ctx context.Context, token, id string) (acct, error) {
	var a acct
	err := e.getJSON(ctx, "GET /accounts/"+id, http.MethodGet, "/api/v1/accounts/"+id, token, nil, http.StatusOK, &a)
	return a, err
}

// newNationalID her çalıştırmada benzersiz, "9" ile başlayan (simülatörün "5…" numaralarıyla çakışmayan) kimlik no üretir.
func newNationalID() string { return fmt.Sprintf("9%010d", time.Now().UnixNano()%10_000_000_000) }

// newCustomerWithAccount operasyon uzmanı olarak müşteri ve verilen türde hesap açar.
func (e *Env) newCustomerWithAccount(ctx context.Context, ops, currency string) (customerID string, a acct, err error) {
	var c struct {
		ID string `json:"id"`
	}
	if err = e.getJSON(ctx, "müşteri aç", http.MethodPost, "/api/v1/customers", ops,
		map[string]string{"full_name": "Doğrulama Müşterisi", "national_id": newNationalID()}, http.StatusCreated, &c); err != nil {
		return
	}
	err = e.getJSON(ctx, "hesap aç", http.MethodPost, "/api/v1/accounts", ops,
		map[string]string{"customer_id": c.ID, "kind": "CHECKING", "currency": currency}, http.StatusCreated, &a)
	return c.ID, a, err
}

func checkF1(ctx context.Context, env *Env) (string, error) {
	ops, err := env.Login(ctx, "operasyon@parakopru.test")
	if err != nil {
		return "", err
	}
	natID := newNationalID()
	var c struct {
		ID string `json:"id"`
	}
	body := map[string]string{"full_name": "Ayşe Doğrulama", "national_id": natID}
	if err := env.getJSON(ctx, "müşteri aç", http.MethodPost, "/api/v1/customers", ops.AccessToken, body, http.StatusCreated, &c); err != nil {
		return "", err
	}
	dup, err := env.API(ctx, http.MethodPost, "/api/v1/customers", ops.AccessToken, body)
	if err != nil {
		return "", err
	}
	if err := expectStatus("aynı kimlik no", dup, http.StatusConflict, "CUSTOMER_EXISTS"); err != nil {
		return "", err
	}

	maturity := time.Now().AddDate(1, 0, 0).Format(time.DateOnly)
	open := func(kind, cur, mat string) (acct, error) {
		in := map[string]string{"customer_id": c.ID, "kind": kind, "currency": cur}
		if mat != "" {
			in["maturity_date"] = mat
		}
		var a acct
		err := env.getJSON(ctx, kind+"/"+cur+" hesap aç", http.MethodPost, "/api/v1/accounts", ops.AccessToken, in, http.StatusCreated, &a)
		if err == nil && (a.Kind != kind || a.Currency != cur || a.IBAN == nil || !ibanPattern.MatchString(*a.IBAN) || a.BalanceMinor != 0) {
			err = fmt.Errorf("%s/%s hesabı beklenen biçimde değil: %+v", kind, cur, a)
		}
		return a, err
	}
	for _, tc := range []struct{ kind, cur, mat string }{
		{"CHECKING", "TRY", ""}, {"CHECKING", "USD", ""}, {"TIME_DEPOSIT", "EUR", maturity},
	} {
		a, err := open(tc.kind, tc.cur, tc.mat)
		if err != nil {
			return "", err
		}
		if (tc.kind == "TIME_DEPOSIT") != (a.MaturityDate != nil) {
			return "", fmt.Errorf("vade tarihi yalnız vadeli hesapta olmalı: %+v", a)
		}
	}

	for _, tc := range []struct {
		what string
		in   map[string]string
	}{
		{"vadeli hesap, vade yok", map[string]string{"customer_id": c.ID, "kind": "TIME_DEPOSIT", "currency": "TRY"}},
		{"vadesiz hesap, vade var", map[string]string{"customer_id": c.ID, "kind": "CHECKING", "currency": "TRY", "maturity_date": maturity}},
		{"desteklenmeyen para birimi", map[string]string{"customer_id": c.ID, "kind": "CHECKING", "currency": "GBP"}},
	} {
		resp, err := env.API(ctx, http.MethodPost, "/api/v1/accounts", ops.AccessToken, tc.in)
		if err != nil {
			return "", err
		}
		if err := expectStatus(tc.what, resp, http.StatusBadRequest, "VALIDATION_ERROR"); err != nil {
			return "", err
		}
	}

	n := 0
	if err := env.accountsPage(ctx, "/api/v1/accounts?customer_id="+c.ID, ops.AccessToken, func(acct) bool { n++; return false }); err != nil {
		return "", err
	}
	if n != 3 {
		return "", fmt.Errorf("müşterinin 3 hesabı olmalı, listede %d var", n)
	}

	demo, err := env.Login(ctx, "musteri@parakopru.test")
	if err != nil {
		return "", err
	}
	kinds := map[string]bool{}
	if err := env.accountsPage(ctx, "/api/v1/accounts", demo.AccessToken, func(a acct) bool { kinds[a.Kind+"/"+a.Currency] = true; return false }); err != nil {
		return "", err
	}
	for _, k := range []string{"CHECKING/TRY", "CHECKING/USD", "CHECKING/EUR", "TIME_DEPOSIT/TRY"} {
		if !kinds[k] {
			return "", fmt.Errorf("demo müşterinin %s hesabı yok", k)
		}
	}
	resp, err := env.API(ctx, http.MethodPost, "/api/v1/accounts", demo.AccessToken, map[string]string{"customer_id": c.ID, "kind": "CHECKING", "currency": "TRY"})
	if err != nil {
		return "", err
	}
	if err := expectStatus("müşteri hesap açmaya çalışıyor", resp, http.StatusForbidden, "FORBIDDEN"); err != nil {
		return "", err
	}

	// Simülatörün seed ettiği müşteriler: bütün sayfalar gezilir.
	total := 0
	if err := env.pageAll(ctx, "/api/v1/customers", ops.AccessToken, func(raw []byte) (bool, error) {
		var p struct {
			Items []struct{ ID string } `json:"items"`
		}
		if err := (Response{Body: raw}).JSON(&p); err != nil {
			return false, err
		}
		total += len(p.Items)
		return false, nil
	}); err != nil {
		return "", err
	}
	if total < env.MinCustomers {
		return "", fmt.Errorf("simülatör en az %d müşteri seed etmeliydi, listede %d var", env.MinCustomers, total)
	}
	return fmt.Sprintf("müşteri + vadesiz TRY/USD + vadeli EUR hesap açıldı (IBAN'lı), kurallar 400, tekrar kimlik 409, müşteri hesap açamaz 403; keyset sayfalamayla %d müşteri", total), nil
}

func checkF2(ctx context.Context, env *Env) (string, error) {
	ops, err := env.Login(ctx, "operasyon@parakopru.test")
	if err != nil {
		return "", err
	}
	demo, err := env.Login(ctx, "musteri@parakopru.test")
	if err != nil {
		return "", err
	}
	var from acct
	if err := env.accountsPage(ctx, "/api/v1/accounts", demo.AccessToken, func(a acct) bool {
		if a.Kind == "CHECKING" && a.Currency == "TRY" {
			from = a
		}
		return from.ID != ""
	}); err != nil || from.ID == "" {
		return "", fmt.Errorf("demo müşterinin vadesiz TRY hesabı bulunamadı: %w", err)
	}
	_, to, err := env.newCustomerWithAccount(ctx, ops.AccessToken, "TRY")
	if err != nil {
		return "", err
	}
	const amount = 12345 // 123,45 TL
	fromBefore := from.BalanceMinor
	if fromBefore < amount {
		return "", fmt.Errorf("demo hesabın bakiyesi yetersiz (%d); make reset-demo çalıştırın", fromBefore)
	}

	resp, err := env.postKeyed(ctx, "/api/v1/transfers", demo.AccessToken, fmt.Sprintf("f2-%d", time.Now().UnixNano()),
		map[string]any{"from_account_id": from.ID, "to_iban": *to.IBAN, "amount_minor": amount, "currency": "TRY"})
	if err != nil {
		return "", err
	}
	if err := expectStatus("havale", resp, http.StatusCreated, ""); err != nil {
		return "", err
	}
	var tr transferResp
	if err := resp.JSON(&tr); err != nil {
		return "", err
	}
	if tr.State != "COMPLETED" {
		return "", fmt.Errorf("transfer durumu %s", tr.State)
	}

	// Bakiye sorguları: iki taraf da tam tutar kadar değişti.
	fromAfter, err := env.account(ctx, demo.AccessToken, from.ID)
	if err != nil {
		return "", err
	}
	toAfter, err := env.account(ctx, ops.AccessToken, to.ID)
	if err != nil {
		return "", err
	}
	if fromAfter.BalanceMinor != fromBefore-amount || toAfter.BalanceMinor != amount {
		return "", fmt.Errorf("bakiyeler: gönderen %d → %d (beklenen %d), alıcı %d (beklenen %d)",
			fromBefore, fromAfter.BalanceMinor, fromBefore-amount, toAfter.BalanceMinor, amount)
	}

	// Hesap hareketleri: son hareket havale; hareketlerin toplamı bakiyeye eşit (her kuruş açıklanabilir).
	sumEffects := func(token, id string) (sum int64, top entryRow, err error) {
		first := true
		err = env.pageAll(ctx, "/api/v1/accounts/"+id+"/entries", token, func(raw []byte) (bool, error) {
			var p struct {
				Items []entryRow `json:"items"`
			}
			if err := (Response{Body: raw}).JSON(&p); err != nil {
				return false, err
			}
			for _, it := range p.Items {
				if first {
					top, first = it, false
				}
				sum += it.BalanceEffectMinor
			}
			return false, nil
		})
		return
	}
	sum, top, err := sumEffects(demo.AccessToken, from.ID)
	if err != nil {
		return "", err
	}
	if top.Kind != "TRANSFER" || top.Side != "DEBIT" || top.AmountMinor != amount || top.BalanceEffectMinor != -amount {
		return "", fmt.Errorf("gönderenin son hareketi beklenen havale değil: %+v", top)
	}
	if sum != fromAfter.BalanceMinor {
		return "", fmt.Errorf("gönderen hareketlerinin toplamı %d, bakiye %d", sum, fromAfter.BalanceMinor)
	}
	sum, top, err = sumEffects(ops.AccessToken, to.ID)
	if err != nil {
		return "", err
	}
	if top.Side != "CREDIT" || sum != toAfter.BalanceMinor || sum != amount {
		return "", fmt.Errorf("alıcı hareketleri: toplam %d, bakiye %d, son hareket %+v", sum, toAfter.BalanceMinor, top)
	}

	// Kaydın iki tarafı: borç toplamı = alacak toplamı.
	var entry struct {
		Postings []struct {
			AccountID   string `json:"account_id"`
			Side        string `json:"side"`
			AmountMinor int64  `json:"amount_minor"`
		} `json:"postings"`
	}
	if err := env.getJSON(ctx, "kayıt ayrıntısı", http.MethodGet, "/api/v1/ledger/entries/"+top.EntryID, ops.AccessToken, nil, http.StatusOK, &entry); err != nil {
		return "", err
	}
	var debit, credit int64
	for _, p := range entry.Postings {
		switch {
		case p.Side == "DEBIT" && p.AccountID == from.ID:
			debit += p.AmountMinor
		case p.Side == "CREDIT" && p.AccountID == to.ID:
			credit += p.AmountMinor
		default:
			return "", fmt.Errorf("beklenmeyen defter satırı: %+v", p)
		}
	}
	if len(entry.Postings) != 2 || debit != amount || credit != amount {
		return "", fmt.Errorf("kayıt dengesiz: borç %d, alacak %d", debit, credit)
	}

	// Yetersiz bakiye reddedilir; bakiye değişmez. Sürece ait kayıt elle ters çevrilemez; müşteri kayıt ayrıntısını göremez.
	short, err := env.postKeyed(ctx, "/api/v1/transfers", ops.AccessToken, fmt.Sprintf("f2-short-%d", time.Now().UnixNano()),
		map[string]any{"from_account_id": to.ID, "to_iban": *from.IBAN, "amount_minor": amount + 1, "currency": "TRY"})
	if err != nil {
		return "", err
	}
	if err := expectStatus("yetersiz bakiye", short, http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS"); err != nil {
		return "", err
	}
	rev, err := env.postKeyed(ctx, "/api/v1/ledger/entries/"+top.EntryID+"/reverse", ops.AccessToken, fmt.Sprintf("f2-rev-%d", time.Now().UnixNano()),
		map[string]string{"reason": "deneme"})
	if err != nil {
		return "", err
	}
	if err := expectStatus("transfer kaydını ters çevirme", rev, http.StatusUnprocessableEntity, "ENTRY_NOT_REVERSIBLE"); err != nil {
		return "", err
	}
	cust, err := env.API(ctx, http.MethodGet, "/api/v1/ledger/entries/"+top.EntryID, demo.AccessToken, nil)
	if err != nil {
		return "", err
	}
	if err := expectStatus("müşteri kayıt ayrıntısı", cust, http.StatusForbidden, "FORBIDDEN"); err != nil {
		return "", err
	}
	if err := env.requireInvariants(ctx, ops.AccessToken); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d kuruşluk havale: iki tarafın bakiyesi ve hareket toplamları tutuyor, kayıtta borç = alacak; yetersiz bakiye 422; /admin/invariants ok", amount), nil
}

type invariants struct {
	OK                bool  `json:"ok"`
	Postings          int64 `json:"postings"`
	UnbalancedEntries int64 `json:"unbalanced_entries"`
	SummaryMismatches int64 `json:"summary_mismatches"`
	Totals            []struct {
		Currency string `json:"currency"`
		SumMinor int64  `json:"sum_minor"`
	} `json:"totals_by_currency"`
}

func (e *Env) requireInvariants(ctx context.Context, token string) error {
	var inv invariants
	if err := e.getJSON(ctx, "GET /admin/invariants", http.MethodGet, "/api/v1/admin/invariants", token, nil, http.StatusOK, &inv); err != nil {
		return err
	}
	if !inv.OK || inv.UnbalancedEntries != 0 || inv.SummaryMismatches != 0 {
		return fmt.Errorf("defter değişmezleri bozuk: %+v", inv)
	}
	for _, t := range inv.Totals {
		if t.SumMinor != 0 {
			return fmt.Errorf("%s satırlarının toplamı %d (0 olmalı)", t.Currency, t.SumMinor)
		}
	}
	return nil
}

// checkT1 proje.md Görev 1: çok sayıda eşzamanlı transfer sonunda hesapların toplamı değişmez ve defter dengede kalır.
func checkT1(ctx context.Context, env *Env) (string, error) {
	const workers, pool = 48, 40
	ops, err := env.Login(ctx, "operasyon@parakopru.test")
	if err != nil {
		return "", err
	}
	demo, err := env.Login(ctx, "musteri@parakopru.test")
	if err != nil {
		return "", err
	}
	demoIDs := map[string]bool{}
	if err := env.accountsPage(ctx, "/api/v1/accounts", demo.AccessToken, func(a acct) bool { demoIDs[a.ID] = true; return false }); err != nil {
		return "", err
	}
	if err := env.requireInvariants(ctx, ops.AccessToken); err != nil {
		return "", fmt.Errorf("başlangıçta: %w", err)
	}

	// Simülatörün müşterilerinden 40 vadesiz TRY hesap (demo hesaplar hariç).
	var accs []acct
	if err := env.accountsPage(ctx, "/api/v1/accounts", ops.AccessToken, func(a acct) bool {
		if a.CustomerID != nil && a.Kind == "CHECKING" && a.Currency == "TRY" && a.BalanceMinor >= 10_000_00 && !demoIDs[a.ID] {
			accs = append(accs, a)
		}
		return len(accs) == pool
	}); err != nil {
		return "", err
	}
	if len(accs) < pool {
		return "", fmt.Errorf("simülatör verisinde %d uygun hesap bulundu, %d gerekli", len(accs), pool)
	}
	var totalBefore int64
	expected := map[string]int64{}
	for _, a := range accs {
		expected[a.ID] = a.BalanceMinor
		totalBefore += a.BalanceMinor
	}

	runID := time.Now().UnixNano()
	var next, completed, rejected atomic.Int64
	var mu sync.Mutex // expected haritası
	start := time.Now()
	g, gctx := errgroup.WithContext(ctx)
	for w := range workers {
		g.Go(func() error {
			rng := rand.New(rand.NewPCG(uint64(w), uint64(runID))) //nolint:gosec // test verisi
			for {
				i := next.Add(1)
				if i > int64(env.T1Transfers) {
					return nil
				}
				a := rng.IntN(pool)
				b := rng.IntN(pool - 1)
				if b >= a { // kendine gönderme yok; ters yönlü çiftler sık çıkar
					b++
				}
				amount := int64(1 + rng.IntN(5_000)) // 0,01 – 50 TL
				resp, err := env.postKeyed(gctx, "/api/v1/transfers", ops.AccessToken, fmt.Sprintf("t1-%d-%d", runID, i),
					map[string]any{"from_account_id": accs[a].ID, "to_iban": *accs[b].IBAN, "amount_minor": amount, "currency": "TRY"})
				if err != nil {
					return err
				}
				switch {
				case resp.Status == http.StatusCreated:
					var tr transferResp
					if err := resp.JSON(&tr); err != nil {
						return err
					}
					mu.Lock()
					expected[tr.FromAccountID] -= tr.AmountMinor
					expected[tr.ToAccountID] += tr.AmountMinor
					mu.Unlock()
					completed.Add(1)
				case resp.Status == http.StatusUnprocessableEntity && resp.ErrorCode() == "INSUFFICIENT_FUNDS":
					rejected.Add(1)
				default:
					return fmt.Errorf("transfer %d: beklenmeyen yanıt %d %s", i, resp.Status, short(resp.Body))
				}
			}
		})
	}
	if err := g.Wait(); err != nil {
		return "", err
	}
	elapsed := time.Since(start)

	var totalAfter int64
	for _, a := range accs {
		now, err := env.account(ctx, ops.AccessToken, a.ID)
		if err != nil {
			return "", err
		}
		if now.BalanceMinor != expected[a.ID] || now.BalanceMinor < 0 {
			return "", fmt.Errorf("hesap %s: bakiye %d, beklenen %d", a.ID, now.BalanceMinor, expected[a.ID])
		}
		totalAfter += now.BalanceMinor
	}
	if totalAfter != totalBefore {
		return "", fmt.Errorf("toplam para değişti: %d → %d", totalBefore, totalAfter)
	}
	if err := env.requireInvariants(ctx, ops.AccessToken); err != nil {
		return "", fmt.Errorf("sonunda: %w", err)
	}
	return fmt.Sprintf("%d eşzamanlı transfer (%d işçi, %s): %d tamamlandı, %d yetersiz bakiye; %d hesabın toplamı %d → %d değişmedi; defter dengede, özet = türetilmiş bakiye",
		env.T1Transfers, workers, elapsed.Round(100*time.Millisecond), completed.Load(), rejected.Load(), pool, totalBefore, totalAfter), nil
}
