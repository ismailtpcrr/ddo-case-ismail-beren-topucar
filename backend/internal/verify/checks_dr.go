package verify

// T-7 (proje.md Görev 7): zaman yolculuğu (işlem zamanı ≠ geçerlilik zamanı), düzeltmenin ters kayıtla yapılması,
// dört göz onayı ve felaket kurtarma (PITR). PITR betiği (scripts/dr/pitr-demo.sh) make verify tarafından önce
// çalıştırılır ve sonucunu bir dosyaya yazar; T-7 yalnız hepsi geçerse PASS olur.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type entryDetail struct {
	ID          string `json:"id"`
	EffectiveAt string `json:"effective_at"`
	RecordedAt  string `json:"recorded_at"`
}

func (e *Env) balanceAt(ctx context.Context, token, accountID, query string) (int64, error) {
	var b struct {
		Balance int64 `json:"balance_minor"`
	}
	err := e.getJSON(ctx, "balance?"+query, http.MethodGet, "/api/v1/accounts/"+accountID+"/balance?"+query, token, nil, http.StatusOK, &b)
	return b.Balance, err
}

func (e *Env) countEntries(ctx context.Context, token, accountID, query string) (int, error) {
	n := 0
	err := e.pageAll(ctx, "/api/v1/accounts/"+accountID+"/entries?"+query, token, func(raw []byte) (bool, error) {
		var p struct {
			Items []entryRow `json:"items"`
		}
		if err := (Response{Body: raw}).JSON(&p); err != nil {
			return false, err
		}
		n += len(p.Items)
		return false, nil
	})
	return n, err
}

func checkT7(ctx context.Context, env *Env) (string, error) {
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
	sys, err := env.Login(ctx, "sistem@parakopru.test")
	if err != nil {
		return "", err
	}
	run := time.Now().UnixNano()

	// --- 1. Zaman yolculuğu: geriye tarihli düzeltme yeni kayıttır; as_of aynıyken known_at'e göre bakiye değişir.
	tt, err := env.pickSimAccountsMin(ctx, ops, demo, 120, 10_000_00)
	if err != nil {
		return "", err
	}
	a := tt[len(tt)-1]
	var opening struct {
		ID string
	}
	var rows struct {
		Items []entryRow `json:"items"`
	}
	if err := env.getJSON(ctx, "hareketler", http.MethodGet, "/api/v1/accounts/"+a.ID+"/entries?limit=50", ops.AccessToken, nil, http.StatusOK, &rows); err != nil {
		return "", err
	}
	for _, it := range rows.Items {
		if it.Kind == "OPENING_BALANCE" {
			opening.ID = it.EntryID
		}
	}
	if opening.ID == "" {
		return "", fmt.Errorf("hesapta açılış kaydı bulunamadı")
	}
	var orig entryDetail
	if err := env.getJSON(ctx, "açılış kaydı", http.MethodGet, "/api/v1/ledger/entries/"+opening.ID, ops.AccessToken, nil, http.StatusOK, &orig); err != nil {
		return "", err
	}
	effective, err := time.Parse(time.RFC3339Nano, orig.EffectiveAt)
	if err != nil {
		return "", err
	}
	b0, err := env.balanceAt(ctx, ops.AccessToken, a.ID, "")
	if err != nil {
		return "", err
	}
	// Geriye tarihli düzeltme: ters kayıt, özgün kaydın geçerlilik zamanıyla yazılır (effective_at geçmişte, recorded_at şimdi).
	rev, err := env.postKeyed(ctx, "/api/v1/ledger/entries/"+opening.ID+"/reverse", ops.AccessToken, fmt.Sprintf("t7-rev-%d", run),
		map[string]any{"reason": "Geriye tarihli düzeltme (doğrulama)", "effective_at": orig.EffectiveAt})
	if err != nil {
		return "", err
	}
	if err := expectStatus("geriye tarihli ters kayıt", rev, http.StatusCreated, ""); err != nil {
		return "", err
	}
	qEffective := "as_of=" + orig.EffectiveAt
	now, err := env.balanceAt(ctx, ops.AccessToken, a.ID, qEffective)
	if err != nil {
		return "", err
	}
	known, err := env.balanceAt(ctx, ops.AccessToken, a.ID, qEffective+"&known_at="+orig.RecordedAt)
	if err != nil {
		return "", err
	}
	before, err := env.balanceAt(ctx, ops.AccessToken, a.ID, "as_of="+effective.Add(-time.Second).Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	if now != 0 || known != b0 || before != 0 {
		return "", fmt.Errorf("zaman yolculuğu: as_of=E güncel bilgiyle %d (beklenen 0), known_at=kayıt anı %d (beklenen %d), E'den önce %d (beklenen 0)", now, known, b0, before)
	}
	oldCount, err := env.countEntries(ctx, ops.AccessToken, a.ID, "known_at="+orig.RecordedAt)
	if err != nil {
		return "", err
	}
	newCount, err := env.countEntries(ctx, ops.AccessToken, a.ID, "")
	if err != nil {
		return "", err
	}
	if oldCount != 1 || newCount != 2 {
		return "", fmt.Errorf("defter: düzeltmeden önceki bilgiyle %d hareket (beklenen 1), şimdi %d (beklenen 2); özgün satır silinmeden kaldı mı?", oldCount, newCount)
	}
	// Düzeltme yalnız bir kez; müşteri geçmişe bakamaz (PLAN §5.10).
	again, err := env.postKeyed(ctx, "/api/v1/ledger/entries/"+opening.ID+"/reverse", ops.AccessToken, fmt.Sprintf("t7-rev2-%d", run), map[string]any{"reason": "ikinci"})
	if err != nil {
		return "", err
	}
	if err := expectStatus("aynı kaydı ikinci kez ters çevirme", again, http.StatusConflict, "ALREADY_REVERSED"); err != nil {
		return "", err
	}
	var own []acct
	if err := env.accountsPage(ctx, "/api/v1/accounts", demo.AccessToken, func(x acct) bool { own = append(own, x); return len(own) == 1 }); err != nil {
		return "", err
	}
	deny, err := env.API(ctx, http.MethodGet, "/api/v1/accounts/"+own[0].ID+"/balance?as_of="+orig.EffectiveAt, demo.AccessToken, nil)
	if err != nil {
		return "", err
	}
	if err := expectStatus("müşteri geçmiş bakiye", deny, http.StatusForbidden, "FORBIDDEN"); err != nil {
		return "", err
	}

	// --- 2. Dört göz: 10.000 TL üzeri transfer ikinci bir yetkilinin onayını ister.
	src, err := env.pickSimAccountsMin(ctx, ops, demo, 2, 30_000_00)
	if err != nil {
		return "", err
	}
	from, to := src[0], src[1]
	send := func(key string, minor int64) (Response, error) {
		return env.postKeyed(ctx, "/api/v1/transfers", ops.AccessToken, fmt.Sprintf("t7-%d-%s", run, key),
			map[string]any{"from_account_id": from.ID, "to_iban": *to.IBAN, "amount_minor": minor, "currency": "TRY"})
	}
	exact, err := send("exact", 10_000_00)
	if err != nil {
		return "", err
	}
	if err := expectStatus("tam 10.000,00 TL", exact, http.StatusCreated, ""); err != nil {
		return "", err
	}
	bal1, err := env.balances(ctx, ops.AccessToken, from.ID)
	if err != nil {
		return "", err
	}
	big, err := send("big", 10_000_01)
	if err != nil {
		return "", err
	}
	if err := expectStatus("10.000,01 TL", big, http.StatusAccepted, ""); err != nil {
		return "", err
	}
	var tr xfer
	if err := big.JSON(&tr); err != nil || tr.State != "PENDING_APPROVAL" {
		return "", fmt.Errorf("10.000,01 TL transfer PENDING_APPROVAL olmalı: %s %w", tr.State, err)
	}
	if bal2, _ := env.balances(ctx, ops.AccessToken, from.ID); bal2.Balance != bal1.Balance {
		return "", fmt.Errorf("onay beklerken para hareket etti: %d → %d", bal1.Balance, bal2.Balance)
	}
	decide := func(who *Session, action, key string, body any) (Response, error) {
		return env.postKeyed(ctx, "/api/v1/transfers/"+tr.ID+"/"+action, who.AccessToken, fmt.Sprintf("t7-%d-%s", run, key), body)
	}
	self, err := decide(ops, "approve", "self", nil)
	if err != nil {
		return "", err
	}
	if err := expectStatus("isteyen kendi transferini onaylıyor", self, http.StatusForbidden, "FOUR_EYES_VIOLATION"); err != nil {
		return "", err
	}
	for _, who := range []*Session{sys, demo} {
		r, err := decide(who, "approve", "deny-"+who.Role, nil)
		if err != nil {
			return "", err
		}
		if err := expectStatus(who.Role+" onaylıyor", r, http.StatusForbidden, "FORBIDDEN"); err != nil {
			return "", err
		}
	}
	ok, err := decide(comp, "approve", "ok", nil)
	if err != nil {
		return "", err
	}
	if err := expectStatus("uyum onayı", ok, http.StatusOK, ""); err != nil {
		return "", err
	}
	var done xfer
	if err := ok.JSON(&done); err != nil || done.State != "COMPLETED" {
		return "", fmt.Errorf("onaydan sonra COMPLETED beklenir: %s %w", done.State, err)
	}
	if bal3, _ := env.balances(ctx, ops.AccessToken, from.ID); bal3.Balance != bal1.Balance-10_000_01 {
		return "", fmt.Errorf("onaydan sonra bakiye %d, beklenen %d", bal3.Balance, bal1.Balance-10_000_01)
	}
	twice, err := decide(comp, "approve", "twice", nil)
	if err != nil {
		return "", err
	}
	if err := expectStatus("ikinci onay", twice, http.StatusConflict, "INVALID_STATE_TRANSITION"); err != nil {
		return "", err
	}
	// Reddetme: para hareket etmez.
	big2, err := send("big2", 10_000_01)
	if err != nil {
		return "", err
	}
	if err := big2.JSON(&tr); err != nil {
		return "", err
	}
	rej, err := decide(comp, "reject", "rej", map[string]string{"reason": "Şüpheli işlem (doğrulama)"})
	if err != nil {
		return "", err
	}
	if err := expectStatus("reddetme", rej, http.StatusOK, ""); err != nil {
		return "", err
	}
	if bal4, _ := env.balances(ctx, ops.AccessToken, from.ID); bal4.Balance != bal1.Balance-10_000_01 {
		return "", fmt.Errorf("reddedilen transfer parayı hareket ettirdi: %d", bal4.Balance)
	}
	if err := env.requireInvariants(ctx, ops.AccessToken); err != nil {
		return "", err
	}

	// --- 3. Felaket kurtarma: scripts/dr/pitr-demo.sh sonucu (make verify önce çalıştırır).
	path := os.Getenv("VERIFY_PITR_RESULT")
	if path == "" {
		return "", fmt.Errorf("VERIFY_PITR_RESULT tanımlı değil: PITR sonucu okunamıyor")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("PITR sonucu yok (%w): önce `make pitr-demo` ya da `make verify` çalıştırılmalı", err)
	}
	pitr := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(pitr, "PASS") {
		return "", fmt.Errorf("PITR gösterimi başarısız: %s", pitr)
	}
	return "geriye tarihli düzeltme ters kayıtla (as_of aynı, known_at'e göre bakiye farklı; özgün satır duruyor); 10.000,00 TL onaysız, 10.000,01 TL dört göz (isteyen onaylayamaz 403, onay yürütür, çift onay 409, ret para oynatmaz); " + pitr, nil
}
