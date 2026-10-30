package verify

// F-6 ve T-4: faiz tahakkuku ve vergi kesintisi, gün sonu işinin tekrar çalıştırmada çift tahakkuk yapmaması.

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type eodRun struct {
	BusinessDate    string `json:"business_date"`
	Status          string `json:"status"`
	AccountsAccrued int    `json:"accounts_accrued"`
	Totals          []struct {
		Currency   string `json:"currency"`
		Accruals   int    `json:"accruals"`
		GrossMinor int64  `json:"gross_minor"`
		TaxMinor   int64  `json:"tax_minor"`
		NetMinor   int64  `json:"net_minor"`
	} `json:"totals"`
}

// runEOD bugünün gün sonunu başlatır ve bitmesini bekler (worker yürütür).
func (e *Env) runEOD(ctx context.Context, token, day string) (eodRun, int, error) {
	resp, err := e.API(ctx, http.MethodPost, "/api/v1/eod/runs", token, map[string]string{"business_date": day})
	if err != nil {
		return eodRun{}, 0, err
	}
	if resp.Status != http.StatusAccepted && resp.Status != http.StatusOK {
		return eodRun{}, 0, fmt.Errorf("gün sonu başlatma: %d %s", resp.Status, short(resp.Body))
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var r eodRun
		if err := e.getJSON(ctx, "GET /eod/runs", http.MethodGet, "/api/v1/eod/runs/"+day, token, nil, http.StatusOK, &r); err != nil {
			return eodRun{}, 0, err
		}
		if r.Status == "DONE" {
			return r, resp.Status, nil
		}
		if time.Now().After(deadline) {
			return r, 0, fmt.Errorf("gün sonu 3 dk içinde bitmedi (durum %s)", r.Status)
		}
		time.Sleep(time.Second)
	}
}

func today() string { return time.Now().UTC().Format(time.DateOnly) }

func checkF6(ctx context.Context, env *Env) (string, error) {
	ops, err := env.Login(ctx, "operasyon@parakopru.test")
	if err != nil {
		return "", err
	}
	demo, err := env.Login(ctx, "musteri@parakopru.test")
	if err != nil {
		return "", err
	}
	deny, err := env.API(ctx, http.MethodPost, "/api/v1/eod/runs", demo.AccessToken, map[string]string{"business_date": today()})
	if err != nil {
		return "", err
	}
	if err := expectStatus("müşteri gün sonu başlatıyor", deny, http.StatusForbidden, "FORBIDDEN"); err != nil {
		return "", err
	}
	future, err := env.API(ctx, http.MethodPost, "/api/v1/eod/runs", ops.AccessToken, map[string]string{"business_date": time.Now().UTC().AddDate(0, 0, 2).Format(time.DateOnly)})
	if err != nil {
		return "", err
	}
	if err := expectStatus("ileri tarihli gün sonu", future, http.StatusUnprocessableEntity, "FUTURE_BUSINESS_DATE"); err != nil {
		return "", err
	}
	run, _, err := env.runEOD(ctx, ops.AccessToken, today())
	if err != nil {
		return "", err
	}
	var try *struct {
		Currency   string `json:"currency"`
		Accruals   int    `json:"accruals"`
		GrossMinor int64  `json:"gross_minor"`
		TaxMinor   int64  `json:"tax_minor"`
		NetMinor   int64  `json:"net_minor"`
	}
	for i := range run.Totals {
		t := &run.Totals[i]
		if t.GrossMinor != t.NetMinor+t.TaxMinor {
			return "", fmt.Errorf("%s: brüt %d ≠ net %d + vergi %d", t.Currency, t.GrossMinor, t.NetMinor, t.TaxMinor)
		}
		if t.Currency == "TRY" {
			try = t
		}
	}
	if try == nil || try.Accruals == 0 || try.GrossMinor <= 0 || try.TaxMinor <= 0 {
		return "", fmt.Errorf("TRY vadeli hesaplarda faiz ve stopaj olmalı: %+v", run.Totals)
	}

	// Demo müşterinin vadeli TRY hesabı: hareketlerde faiz kaydı, kayıtta borç faiz gideri = alacak müşteri (net) + stopaj.
	var td acct
	if err := env.accountsPage(ctx, "/api/v1/accounts", demo.AccessToken, func(a acct) bool {
		if a.Kind == "TIME_DEPOSIT" && a.Currency == "TRY" {
			td = a
		}
		return td.ID != ""
	}); err != nil || td.ID == "" {
		return "", fmt.Errorf("demo vadeli TRY hesabı yok: %w", err)
	}
	var rows struct {
		Items []entryRow `json:"items"`
	}
	if err := env.getJSON(ctx, "hareketler", http.MethodGet, "/api/v1/accounts/"+td.ID+"/entries?limit=20", demo.AccessToken, nil, http.StatusOK, &rows); err != nil {
		return "", err
	}
	var interestEntry entryRow
	for _, r := range rows.Items {
		if r.Kind == "INTEREST_ACCRUAL" {
			interestEntry = r
			break
		}
	}
	if interestEntry.EntryID == "" || interestEntry.BalanceEffectMinor <= 0 {
		return "", fmt.Errorf("demo vadeli hesapta faiz hareketi yok")
	}
	var entry struct {
		Postings []struct {
			AccountID   string `json:"account_id"`
			Side        string `json:"side"`
			AmountMinor int64  `json:"amount_minor"`
		} `json:"postings"`
	}
	if err := env.getJSON(ctx, "faiz kaydı", http.MethodGet, "/api/v1/ledger/entries/"+interestEntry.EntryID, ops.AccessToken, nil, http.StatusOK, &entry); err != nil {
		return "", err
	}
	var debit, credit, net int64
	for _, p := range entry.Postings {
		if p.Side == "DEBIT" {
			debit += p.AmountMinor
		} else {
			credit += p.AmountMinor
			if p.AccountID == td.ID {
				net = p.AmountMinor
			}
		}
	}
	tax := debit - net
	if debit != credit || len(entry.Postings) != 3 || net != interestEntry.BalanceEffectMinor || tax <= 0 || tax != divHalfEven(debit*15, 100) {
		return "", fmt.Errorf("faiz kaydı: brüt %d, net %d, stopaj %d (beklenen %%15 = %d), %d satır", debit, net, tax, divHalfEven(debit*15, 100), len(entry.Postings))
	}
	if err := env.requireInvariants(ctx, ops.AccessToken); err != nil {
		return "", err
	}
	return fmt.Sprintf("gün sonu %s: %d hesap; TRY brüt %d = net %d + stopaj %d; demo vadeli hesapta brüt %d → net %d + %%15 stopaj %d (tek dengeli kayıt); müşteri 403, ileri tarih 422",
		run.BusinessDate, run.AccountsAccrued, try.GrossMinor, try.NetMinor, try.TaxMinor, debit, net, tax), nil
}

func checkT4(ctx context.Context, env *Env) (string, error) {
	ops, err := env.Login(ctx, "operasyon@parakopru.test")
	if err != nil {
		return "", err
	}
	first, _, err := env.runEOD(ctx, ops.AccessToken, today())
	if err != nil {
		return "", err
	}
	// Aynı günü tekrar başlatmak: 200 (yeni çalıştırma yok), tahakkuklar ve tutarlar aynı (çift tahakkuk yok).
	again, status, err := env.runEOD(ctx, ops.AccessToken, today())
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("aynı gün ikinci başlatma 200 dönmeli (mevcut çalıştırma), %d döndü", status)
	}
	if fmt.Sprint(first.Totals) != fmt.Sprint(again.Totals) || first.AccountsAccrued != again.AccountsAccrued {
		return "", fmt.Errorf("tekrar çalıştırma tutarları değiştirdi: %v → %v", first.Totals, again.Totals)
	}
	// Daha sonraki gün yapılmışken geçmiş gün açılamaz.
	past, err := env.API(ctx, http.MethodPost, "/api/v1/eod/runs", ops.AccessToken, map[string]string{"business_date": time.Now().UTC().AddDate(0, 0, -400).Format(time.DateOnly)})
	if err != nil {
		return "", err
	}
	if past.Status != http.StatusOK { // o gün daha önce (eski bir verify'da) yapılmışsa 200; değilse 409 olmalı
		if err := expectStatus("geçmiş gün", past, http.StatusConflict, "EOD_OUT_OF_ORDER"); err != nil {
			return "", err
		}
	}
	if err := env.requireInvariants(ctx, ops.AccessToken); err != nil {
		return "", err
	}
	return fmt.Sprintf("aynı gün iki kez: %d hesap, tutarlar birebir aynı (UNIQUE hesap+gün, çift tahakkuk yok); sıra dışı geçmiş gün 409; defter dengede. Çökme/devam ve gün tabanı/kuruş dağıtımı kanıtı: go test ./internal/interest (TESTING.md)", first.AccountsAccrued), nil
}
