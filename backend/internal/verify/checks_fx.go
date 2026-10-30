package verify

// F-5: kur yönetimi ve çok para birimli işlem (zaman geçerlilikli kurlar, tek ve dengeli defter kaydı, TL eşdeğeri eşiği).

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

type fxRate struct {
	Rate      string `json:"rate"`
	ValidFrom string `json:"valid_from"`
}

type fxTransfer struct {
	ID             string  `json:"id"`
	State          string  `json:"state"`
	ToAmountMinor  *int64  `json:"to_amount_minor"`
	ToCurrency     *string `json:"to_currency"`
	FXRate         *string `json:"fx_rate"`
	JournalEntryID *string `json:"journal_entry_id"`
}

// divHalfEven n / d'yi çifte yuvarlayarak (banker's rounding) tam sayı böler; n, d > 0.
func divHalfEven(n, d int64) int64 {
	q, r := n/d, n%d
	switch {
	case 2*r > d, 2*r == d && q%2 == 1:
		q++
	}
	return q
}

func checkF5(ctx context.Context, env *Env) (string, error) {
	ops, err := env.Login(ctx, "operasyon@parakopru.test")
	if err != nil {
		return "", err
	}
	demo, err := env.Login(ctx, "musteri@parakopru.test")
	if err != nil {
		return "", err
	}
	comp, err := env.Login(ctx, "uyum@parakopru.test")
	if err != nil {
		return "", err
	}

	// 1. Kur yönetimi: herkes okur, yalnız operasyon yazar; geçersiz kur reddedilir.
	var list struct {
		Items []fxRate `json:"items"`
	}
	if err := env.getJSON(ctx, "GET /fx/rates", http.MethodGet, "/api/v1/fx/rates?base=USD", demo.AccessToken, nil, http.StatusOK, &list); err != nil {
		return "", err
	}
	if len(list.Items) == 0 {
		return "", fmt.Errorf("başlangıç USD kuru yok")
	}
	deny, err := env.API(ctx, http.MethodPost, "/api/v1/fx/rates", demo.AccessToken, map[string]any{"base": "USD", "rate": "41.0000"})
	if err != nil {
		return "", err
	}
	if err := expectStatus("müşteri kur giriyor", deny, http.StatusForbidden, "FORBIDDEN"); err != nil {
		return "", err
	}
	for _, bad := range []map[string]any{{"base": "USD", "rate": "abc"}, {"base": "USD", "rate": "-1"}, {"base": "USD", "rate": "0"}, {"base": "TRY", "rate": "1"}, {"base": "USD", "rate": 41.5}} {
		r, err := env.API(ctx, http.MethodPost, "/api/v1/fx/rates", ops.AccessToken, bad)
		if err != nil {
			return "", err
		}
		if err := expectStatus(fmt.Sprintf("geçersiz kur %v", bad), r, http.StatusBadRequest, ""); err != nil {
			return "", err
		}
	}
	var r1 fxRate
	if err := env.getJSON(ctx, "kur ekle", http.MethodPost, "/api/v1/fx/rates", ops.AccessToken, map[string]any{"base": "USD", "rate": "41.1250"}, http.StatusCreated, &r1); err != nil {
		return "", err
	}

	// 2. Çok para birimli transfer: demo müşterinin TRY hesabından USD hesabına 1.000,00 TL.
	var accs []acct
	if err := env.accountsPage(ctx, "/api/v1/accounts", demo.AccessToken, func(a acct) bool { accs = append(accs, a); return false }); err != nil {
		return "", err
	}
	byName := map[string]acct{}
	for _, a := range accs {
		if a.Kind == "CHECKING" {
			byName[a.Currency] = a
		}
	}
	tryAcc, usdAcc, eurAcc := byName["TRY"], byName["USD"], byName["EUR"]
	if tryAcc.ID == "" || usdAcc.ID == "" || eurAcc.ID == "" {
		return "", fmt.Errorf("demo müşterinin TRY, USD ve EUR hesapları olmalı")
	}
	run := time.Now().UnixNano()
	send := func(key string, from, to acct, cur string, minor int64) (Response, error) {
		return env.postKeyed(ctx, "/api/v1/transfers", demo.AccessToken, fmt.Sprintf("f5-%d-%s", run, key),
			map[string]any{"from_account_id": from.ID, "to_iban": *to.IBAN, "amount_minor": minor, "currency": cur})
	}
	tryB, _ := env.balances(ctx, demo.AccessToken, tryAcc.ID)
	usdB, _ := env.balances(ctx, demo.AccessToken, usdAcc.ID)
	resp, err := send("t2u", tryAcc, usdAcc, "TRY", 1_000_00)
	if err != nil {
		return "", err
	}
	if err := expectStatus("TRY → USD transfer", resp, http.StatusCreated, ""); err != nil {
		return "", err
	}
	var tr fxTransfer
	if err := resp.JSON(&tr); err != nil {
		return "", err
	}
	// Beklenen: 100.000 kuruş / 41,125 = 2.431,61 sent → banker's rounding → 2.432.
	want := divHalfEven(1_000_00*100_000_000, 4_112_500_000)
	if tr.State != "COMPLETED" || tr.ToAmountMinor == nil || *tr.ToAmountMinor != want || tr.ToCurrency == nil || *tr.ToCurrency != "USD" || tr.FXRate == nil {
		return "", fmt.Errorf("çok para birimli transfer: %+v (beklenen alıcı tutarı %d USD sent)", tr, want)
	}
	rate, err := decimal.NewFromString(*tr.FXRate)
	if err != nil || !rate.Mul(decimal.RequireFromString("41.125")).Round(6).Equal(decimal.NewFromInt(1)) {
		return "", fmt.Errorf("kayda yazılan kur %s (1 TRY = 1/41,125 USD olmalı)", *tr.FXRate)
	}
	tryA, _ := env.balances(ctx, demo.AccessToken, tryAcc.ID)
	usdA, _ := env.balances(ctx, demo.AccessToken, usdAcc.ID)
	if tryA.Balance != tryB.Balance-1_000_00 || usdA.Balance != usdB.Balance+want {
		return "", fmt.Errorf("bakiyeler: TRY %d → %d (beklenen −100000), USD %d → %d (beklenen +%d)", tryB.Balance, tryA.Balance, usdB.Balance, usdA.Balance, want)
	}
	// Tek defter kaydı: dört satır, her para biriminde borç = alacak.
	var entry struct {
		Postings []struct {
			Side        string `json:"side"`
			AmountMinor int64  `json:"amount_minor"`
			Currency    string `json:"currency"`
		} `json:"postings"`
	}
	if err := env.getJSON(ctx, "kayıt", http.MethodGet, "/api/v1/ledger/entries/"+*tr.JournalEntryID, ops.AccessToken, nil, http.StatusOK, &entry); err != nil {
		return "", err
	}
	sums := map[string]int64{}
	for _, p := range entry.Postings {
		if p.Side == "DEBIT" {
			sums[p.Currency] += p.AmountMinor
		} else {
			sums[p.Currency] -= p.AmountMinor
		}
	}
	if len(entry.Postings) != 4 || len(sums) != 2 || sums["TRY"] != 0 || sums["USD"] != 0 {
		return "", fmt.Errorf("kayıt tek, dört satırlı ve her para biriminde dengeli olmalı: %d satır, toplamlar %v", len(entry.Postings), sums)
	}

	// 3. Kur değişince geçmiş etkilenmez: yeni kur eklenir; eski transferin kuru ve tutarı aynı; as_of ile eski kur okunur.
	if err := env.getJSON(ctx, "ikinci kur", http.MethodPost, "/api/v1/fx/rates", ops.AccessToken, map[string]any{"base": "USD", "rate": "42.0000"}, http.StatusCreated, nil); err != nil {
		return "", err
	}
	var again fxTransfer
	if err := env.getJSON(ctx, "transfer", http.MethodGet, "/api/v1/transfers/"+tr.ID, demo.AccessToken, nil, http.StatusOK, &again); err != nil {
		return "", err
	}
	if again.ToAmountMinor == nil || *again.ToAmountMinor != want || again.FXRate == nil || *again.FXRate != *tr.FXRate {
		return "", fmt.Errorf("kur değişince geçmiş transfer değişti: %+v", again)
	}
	var past struct {
		Items []fxRate `json:"items"`
	}
	if err := env.getJSON(ctx, "as_of", http.MethodGet, "/api/v1/fx/rates?base=USD&as_of="+r1.ValidFrom, demo.AccessToken, nil, http.StatusOK, &past); err != nil {
		return "", err
	}
	if len(past.Items) == 0 || !decimal.RequireFromString(past.Items[0].Rate).Equal(decimal.RequireFromString("41.125")) {
		return "", fmt.Errorf("as_of ile geçmiş kur 41,125 olmalı: %+v", past.Items)
	}

	// 4. Dört göz eşiği TL karşılığına göre: 300 USD × 42 = 12.600 TL → onay bekler; 100 USD = 4.200 TL → hemen tamamlanır.
	big, err := send("usd-big", usdAcc, eurAcc, "USD", 300_00)
	if err != nil {
		return "", err
	}
	if err := expectStatus("300 USD (TL karşılığı > 10.000)", big, http.StatusAccepted, ""); err != nil {
		return "", err
	}
	var bigT fxTransfer
	if err := big.JSON(&bigT); err != nil || bigT.State != "PENDING_APPROVAL" {
		return "", fmt.Errorf("300 USD transfer PENDING_APPROVAL olmalı: %s", bigT.State)
	}
	rej, err := env.postKeyed(ctx, "/api/v1/transfers/"+bigT.ID+"/reject", comp.AccessToken, fmt.Sprintf("f5-%d-rej", run), map[string]string{"reason": "Doğrulama"})
	if err != nil {
		return "", err
	}
	if err := expectStatus("ret", rej, http.StatusOK, ""); err != nil {
		return "", err
	}
	small, err := send("usd-small", usdAcc, eurAcc, "USD", 100_00)
	if err != nil {
		return "", err
	}
	if err := expectStatus("100 USD → EUR", small, http.StatusCreated, ""); err != nil {
		return "", err
	}
	if err := env.requireInvariants(ctx, ops.AccessToken); err != nil {
		return "", err
	}
	return fmt.Sprintf("kur yönetimi (yalnız operasyon yazar, geçersiz kur 400); 1.000,00 TL → %d USD sent (banker's rounding) tek dengeli kayıtla; kur değişince geçmiş transfer ve as_of kuru değişmedi; dört göz eşiği TL karşılığına göre (300 USD onay, 100 USD değil)", want), nil
}
