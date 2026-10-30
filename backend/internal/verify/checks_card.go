package verify

// F-4 ve T-3 kontrolleri: kart provizyonu, çekim, iade ve yarışlar. Kart ağı çağrıları "sistem" rolüyle yapılır.

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

type cardResp struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
}

type holdResp struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	AmountMinor    int64  `json:"amount_minor"`
	CapturedMinor  int64  `json:"captured_minor"`
	ReleasedMinor  int64  `json:"released_minor"`
	RemainingMinor int64  `json:"remaining_minor"`
}

type captureResp struct {
	Capture struct {
		ID            string `json:"id"`
		RefundedMinor int64  `json:"refunded_minor"`
	} `json:"capture"`
	Hold holdResp `json:"hold"`
}

type balances struct {
	Balance, Held, Available int64
}

func (e *Env) balances(ctx context.Context, token, accountID string) (balances, error) {
	var a struct {
		Balance   int64 `json:"balance_minor"`
		Held      int64 `json:"held_minor"`
		Available int64 `json:"available_minor"`
	}
	err := e.getJSON(ctx, "GET /accounts/"+accountID, http.MethodGet, "/api/v1/accounts/"+accountID, token, nil, http.StatusOK, &a)
	return balances{a.Balance, a.Held, a.Available}, err
}

func (e *Env) authorize(ctx context.Context, sys, key, cardID string, minor int64, ttl int) (Response, error) {
	body := map[string]any{"amount_minor": minor, "currency": "TRY", "merchant": "Doğrulama"}
	if ttl > 0 {
		body["ttl_seconds"] = ttl
	}
	return e.postKeyed(ctx, "/api/v1/cards/"+cardID+"/authorizations", sys, key, body)
}

func (e *Env) mustAuthorize(ctx context.Context, sys, key, cardID string, minor int64, ttl int) (holdResp, error) {
	var h holdResp
	resp, err := e.authorize(ctx, sys, key, cardID, minor, ttl)
	if err != nil {
		return h, err
	}
	if err := expectStatus("provizyon", resp, http.StatusCreated, ""); err != nil {
		return h, err
	}
	return h, resp.JSON(&h)
}

func (e *Env) capture(ctx context.Context, sys, key, holdID string, minor int64, final bool) (Response, error) {
	return e.postKeyed(ctx, "/api/v1/holds/"+holdID+"/captures", sys, key, map[string]any{"amount_minor": minor, "final": final})
}

func checkF4(ctx context.Context, env *Env) (string, error) {
	demo, err := env.Login(ctx, "musteri@parakopru.test")
	if err != nil {
		return "", err
	}
	sys, err := env.Login(ctx, "sistem@parakopru.test")
	if err != nil {
		return "", err
	}
	var cards struct {
		Items []cardResp `json:"items"`
	}
	if err := env.getJSON(ctx, "GET /cards", http.MethodGet, "/api/v1/cards", demo.AccessToken, nil, http.StatusOK, &cards); err != nil || len(cards.Items) == 0 {
		return "", fmt.Errorf("demo müşterinin kartı yok: %w", err)
	}
	c := cards.Items[0]
	run := time.Now().UnixNano()
	key := func(n string) string { return fmt.Sprintf("f4-%d-%s", run, n) }
	b0, err := env.balances(ctx, demo.AccessToken, c.AccountID)
	if err != nil {
		return "", err
	}
	want := func(what string, b balances, bal, held int64) error {
		if b.Balance != bal || b.Held != held || b.Available != bal-held {
			return fmt.Errorf("%s: bakiye/bloke/kullanılabilir %d/%d/%d, beklenen %d/%d/%d", what, b.Balance, b.Held, b.Available, bal, held, bal-held)
		}
		return nil
	}

	// Provizyon 100 TL: bakiye aynı, bloke +100, kullanılabilir −100.
	h, err := env.mustAuthorize(ctx, sys.AccessToken, key("auth"), c.ID, 100_00, 0)
	if err != nil {
		return "", err
	}
	b, _ := env.balances(ctx, demo.AccessToken, c.AccountID)
	if err := want("provizyon", b, b0.Balance, b0.Held+100_00); err != nil {
		return "", err
	}
	// Aynı anahtar: aynı provizyon, ikinci blok yok.
	again, err := env.authorize(ctx, sys.AccessToken, key("auth"), c.ID, 100_00, 0)
	if err != nil {
		return "", err
	}
	var h2 holdResp
	if err := again.JSON(&h2); err != nil || h2.ID != h.ID || again.Header.Get("Idempotent-Replayed") != "true" {
		return "", fmt.Errorf("aynı anahtar aynı provizyonu döndürmeli (replayed=%q)", again.Header.Get("Idempotent-Replayed"))
	}

	// Kısmi çekim 40 TL: bakiye −40, bloke kalan 60.
	resp, err := env.capture(ctx, sys.AccessToken, key("cap1"), h.ID, 40_00, false)
	if err != nil {
		return "", err
	}
	if err := expectStatus("kısmi çekim", resp, http.StatusCreated, ""); err != nil {
		return "", err
	}
	var cap1 captureResp
	if err := resp.JSON(&cap1); err != nil {
		return "", err
	}
	b, _ = env.balances(ctx, demo.AccessToken, c.AccountID)
	if err := want("kısmi çekim", b, b0.Balance-40_00, b0.Held+60_00); err != nil || cap1.Hold.State != "ACTIVE" {
		return "", fmt.Errorf("kısmi çekim sonrası hold %s: %w", cap1.Hold.State, err)
	}
	// Aşırı çekim 422; final çekim 30 TL: kalan 30 TL serbest.
	over, err := env.capture(ctx, sys.AccessToken, key("cap-over"), h.ID, 60_01, false)
	if err != nil {
		return "", err
	}
	if err := expectStatus("aşırı çekim", over, http.StatusUnprocessableEntity, "CAPTURE_EXCEEDS_HOLD"); err != nil {
		return "", err
	}
	resp, err = env.capture(ctx, sys.AccessToken, key("cap2"), h.ID, 30_00, true)
	if err != nil {
		return "", err
	}
	var cap2 captureResp
	if err := expectStatus("final çekim", resp, http.StatusCreated, ""); err != nil {
		return "", err
	}
	if err := resp.JSON(&cap2); err != nil {
		return "", err
	}
	if cap2.Hold.State != "CAPTURED" || cap2.Hold.ReleasedMinor != 30_00 || cap2.Hold.CapturedMinor != 70_00 {
		return "", fmt.Errorf("final çekim sonrası hold: %+v", cap2.Hold)
	}
	b, _ = env.balances(ctx, demo.AccessToken, c.AccountID)
	if err := want("final çekim", b, b0.Balance-70_00, b0.Held); err != nil {
		return "", err
	}
	late, err := env.capture(ctx, sys.AccessToken, key("cap-late"), h.ID, 1, false)
	if err != nil {
		return "", err
	}
	if err := expectStatus("çekilmiş provizyondan çekim", late, http.StatusConflict, "HOLD_NOT_ACTIVE"); err != nil {
		return "", err
	}

	// İade: 10 TL; çekilenden (40 TL) fazlası 422.
	for _, tc := range []struct {
		n      string
		minor  int64
		status int
		code   string
	}{{"ref-over", 40_01, http.StatusUnprocessableEntity, "REFUND_EXCEEDS_CAPTURE"}, {"ref", 10_00, http.StatusCreated, ""}} {
		r, err := env.postKeyed(ctx, "/api/v1/captures/"+cap1.Capture.ID+"/refunds", sys.AccessToken, key(tc.n), map[string]any{"amount_minor": tc.minor})
		if err != nil {
			return "", err
		}
		if err := expectStatus("iade "+tc.n, r, tc.status, tc.code); err != nil {
			return "", err
		}
	}
	b, _ = env.balances(ctx, demo.AccessToken, c.AccountID)
	if err := want("iade", b, b0.Balance-60_00, b0.Held); err != nil {
		return "", err
	}

	// Süre aşımı: 3 sn ömürlü provizyon worker tarafından serbest bırakılır (EXPIRED).
	short, err := env.mustAuthorize(ctx, sys.AccessToken, key("short"), c.ID, 25_00, 3)
	if err != nil {
		return "", err
	}
	if b, _ = env.balances(ctx, demo.AccessToken, c.AccountID); b.Held != b0.Held+25_00 {
		return "", fmt.Errorf("kısa ömürlü provizyon bloke etmedi: %+v", b)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		var hs struct {
			Items []holdResp `json:"items"`
		}
		if err := env.getJSON(ctx, "GET /holds", http.MethodGet, "/api/v1/accounts/"+c.AccountID+"/holds?state=EXPIRED&limit=50", demo.AccessToken, nil, http.StatusOK, &hs); err != nil {
			return "", err
		}
		expired := false
		for _, x := range hs.Items {
			expired = expired || x.ID == short.ID
		}
		if expired {
			break
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("süresi dolan provizyon 60 sn içinde serbest bırakılmadı")
		}
		time.Sleep(time.Second)
	}
	if b, _ = env.balances(ctx, demo.AccessToken, c.AccountID); b.Held != b0.Held {
		return "", fmt.Errorf("süre aşımından sonra bloke serbest kalmadı: %+v", b)
	}

	// Yetki ve doğrulama: müşteri provizyon açamaz; yetersiz bakiye 422.
	forbidden, err := env.authorize(ctx, demo.AccessToken, key("forbidden"), c.ID, 1, 0)
	if err != nil {
		return "", err
	}
	if err := expectStatus("müşteri provizyon açıyor", forbidden, http.StatusForbidden, "FORBIDDEN"); err != nil {
		return "", err
	}
	poor, err := env.authorize(ctx, sys.AccessToken, key("poor"), c.ID, b0.Available+1_000_000_00, 0)
	if err != nil {
		return "", err
	}
	if err := expectStatus("yetersiz bakiye provizyonu", poor, http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS"); err != nil {
		return "", err
	}
	ops, err := env.Login(ctx, "operasyon@parakopru.test")
	if err != nil {
		return "", err
	}
	if err := env.requireInvariants(ctx, ops.AccessToken); err != nil {
		return "", err
	}
	return "provizyon → kısmi çekim → final çekim (kalan serbest) → iade; bakiye/bloke/kullanılabilir her adımda doğru; süre aşımı worker'la serbest bıraktı; aşırı çekim/iade 422; müşteri provizyon açamaz 403; held = Σ aktif provizyon", nil
}

func checkT3(ctx context.Context, env *Env) (string, error) {
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
	// Demo kartı hariç, simülatör müşterilerinden yeterli bakiyeli bir kart (ilk 50 uygun karttan sonrası: diğer kontrollerle çakışmasın).
	var demoCards struct {
		Items []cardResp `json:"items"`
	}
	if err := env.getJSON(ctx, "GET /cards", http.MethodGet, "/api/v1/cards", demo.AccessToken, nil, http.StatusOK, &demoCards); err != nil {
		return "", err
	}
	var card cardResp
	var start balances
	skipped := 0
	if err := env.pageAll(ctx, "/api/v1/cards", ops.AccessToken, func(raw []byte) (bool, error) {
		var p struct {
			Items []cardResp `json:"items"`
		}
		if err := (Response{Body: raw}).JSON(&p); err != nil {
			return false, err
		}
		for _, c := range p.Items {
			if len(demoCards.Items) > 0 && c.ID == demoCards.Items[0].ID {
				continue
			}
			b, err := env.balances(ctx, ops.AccessToken, c.AccountID)
			if err != nil {
				return false, err
			}
			if b.Available >= 10_000_00 && b.Held == 0 {
				if skipped++; skipped > 50 {
					card, start = c, b
					return true, nil
				}
			}
		}
		return false, nil
	}); err != nil {
		return "", err
	}
	if card.ID == "" {
		return "", fmt.Errorf("simülatör verisinde uygun kart bulunamadı")
	}
	run := time.Now().UnixNano()

	// 1. Eşzamanlı provizyonlar kullanılabilir bakiyeyi aşamaz: 60 istek, her biri bakiyenin ~1/20'si.
	x := start.Available / 20
	expected := start.Available / x
	var holds []holdResp
	var mu = make(chan holdResp, 100)
	var ok, poor atomic.Int32
	g, gctx := errgroup.WithContext(ctx)
	for i := range 60 {
		g.Go(func() error {
			resp, err := env.authorize(gctx, sys.AccessToken, fmt.Sprintf("t3-a-%d-%d", run, i), card.ID, x, 60)
			if err != nil {
				return err
			}
			switch {
			case resp.Status == http.StatusCreated:
				var h holdResp
				if err := resp.JSON(&h); err != nil {
					return err
				}
				mu <- h
				ok.Add(1)
			case resp.Status == http.StatusUnprocessableEntity && resp.ErrorCode() == "INSUFFICIENT_FUNDS":
				poor.Add(1)
			default:
				return fmt.Errorf("provizyon: beklenmeyen yanıt %d %s", resp.Status, short(resp.Body))
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return "", err
	}
	close(mu)
	for h := range mu {
		holds = append(holds, h)
	}
	if int64(ok.Load()) != expected {
		return "", fmt.Errorf("%d provizyon başarılı oldu, kullanılabilir bakiye tam %d tanesine yeter", ok.Load(), expected)
	}
	b, err := env.balances(ctx, ops.AccessToken, card.AccountID)
	if err != nil {
		return "", err
	}
	if b.Held != expected*x || b.Available != start.Available-expected*x || b.Available < 0 {
		return "", fmt.Errorf("eşzamanlı provizyonlardan sonra bloke %d (beklenen %d), kullanılabilir %d", b.Held, expected*x, b.Available)
	}

	// 2. Aynı provizyondan eşzamanlı çekimler: her provizyonda 3 çekim yarışır (her biri tutarın yarısından fazla): yalnız biri geçer.
	cap := x/2 + 1
	var captures []captureResp
	var capCh = make(chan captureResp, 3*len(holds))
	var capOK atomic.Int32
	g, gctx = errgroup.WithContext(ctx)
	for _, h := range holds {
		for j := range 3 {
			g.Go(func() error {
				resp, err := env.capture(gctx, sys.AccessToken, fmt.Sprintf("t3-c-%d-%s-%d", run, h.ID, j), h.ID, cap, false)
				if err != nil {
					return err
				}
				switch {
				case resp.Status == http.StatusCreated:
					var r captureResp
					if err := resp.JSON(&r); err != nil {
						return err
					}
					capCh <- r
					capOK.Add(1)
				case resp.Status == http.StatusUnprocessableEntity && resp.ErrorCode() == "CAPTURE_EXCEEDS_HOLD":
				default:
					return fmt.Errorf("çekim: beklenmeyen yanıt %d %s", resp.Status, short(resp.Body))
				}
				return nil
			})
		}
	}
	if err := g.Wait(); err != nil {
		return "", err
	}
	close(capCh)
	for r := range capCh {
		captures = append(captures, r)
	}
	if int(capOK.Load()) != len(holds) {
		return "", fmt.Errorf("%d provizyon için %d çekim geçti (her provizyonda tam bir çekim geçmeli)", len(holds), capOK.Load())
	}
	b, err = env.balances(ctx, ops.AccessToken, card.AccountID)
	if err != nil {
		return "", err
	}
	n := int64(len(holds))
	if b.Balance != start.Balance-n*cap || b.Held != n*(x-cap) || b.Available < 0 {
		return "", fmt.Errorf("çekimlerden sonra bakiye %d (beklenen %d), bloke %d (beklenen %d)", b.Balance, start.Balance-n*cap, b.Held, n*(x-cap))
	}

	// 3. Aynı çekimden eşzamanlı iadeler: her çekimde 3 iade (her biri çekilenin yarısından fazla) yarışır: yalnız biri geçer.
	var refOK atomic.Int32
	ra := cap/2 + 1
	g, gctx = errgroup.WithContext(ctx)
	for _, c := range captures {
		for j := range 3 {
			g.Go(func() error {
				resp, err := env.postKeyed(gctx, "/api/v1/captures/"+c.Capture.ID+"/refunds", sys.AccessToken,
					fmt.Sprintf("t3-r-%d-%s-%d", run, c.Capture.ID, j), map[string]any{"amount_minor": ra})
				if err != nil {
					return err
				}
				switch {
				case resp.Status == http.StatusCreated:
					refOK.Add(1)
				case resp.Status == http.StatusUnprocessableEntity && resp.ErrorCode() == "REFUND_EXCEEDS_CAPTURE":
				default:
					return fmt.Errorf("iade: beklenmeyen yanıt %d %s", resp.Status, short(resp.Body))
				}
				return nil
			})
		}
	}
	if err := g.Wait(); err != nil {
		return "", err
	}
	if int(refOK.Load()) != len(captures) {
		return "", fmt.Errorf("%d çekim için %d iade geçti (her çekimde tam bir iade geçmeli)", len(captures), refOK.Load())
	}
	end, err := env.balances(ctx, ops.AccessToken, card.AccountID)
	if err != nil {
		return "", err
	}
	if want := start.Balance - n*cap + n*ra; end.Balance != want || end.Held != n*(x-cap) || end.Available != end.Balance-end.Held || end.Available < 0 {
		return "", fmt.Errorf("iadelerden sonra bakiye %d (beklenen %d), bloke %d, kullanılabilir %d", end.Balance, start.Balance-n*cap+n*ra, end.Held, end.Available)
	}
	if err := env.requireInvariants(ctx, ops.AccessToken); err != nil {
		return "", err
	}
	return fmt.Sprintf("tek hesapta eşzamanlı yarışlar: %d provizyon isteğinden tam %d'i geçti (bakiye %d'e yeter), her provizyonda 3 yarışan çekimden 1'i ve her çekimde 3 yarışan iadeden 1'i geçti; bakiye = açılış − çekim + iade, bloke = Σ aktif kalan, kullanılabilir ≥ 0; /admin/invariants ok", 60, expected, start.Available), nil
}
