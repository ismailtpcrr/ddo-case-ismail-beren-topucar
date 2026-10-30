package verify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// demoAccounts her rol için demo hesaptır (auth.DemoUsers ile aynı; verify sistemi dışarıdan, API üzerinden dener).
var demoAccounts = []struct{ Email, Role string }{
	{"musteri@parakopru.test", "CUSTOMER"},
	{"operasyon@parakopru.test", "OPERATIONS"},
	{"risk@parakopru.test", "RISK_ANALYST"},
	{"uyum@parakopru.test", "COMPLIANCE"},
	{"denetci@parakopru.test", "AUDITOR"},
	{"sistem@parakopru.test", "SYSTEM"},
}

// Checks, `make verify`'ın çalıştırdığı bütün kimliklerdir; tablo bu sırayla basılır.
// Run'ı boş olan kimlikler henüz uygulanmamıştır ve FAIL sayılır.
func Checks() []Check {
	return []Check{
		{ID: "HEALTH", Title: "GET /health (veritabanı dahil)", Run: checkHealth},
		{ID: "AUTH-1", Title: "Her rolün demo hesabıyla giriş", Run: checkLoginAllRoles},
		{ID: "AUTH-2", Title: "Yanlış parola reddedilir", Run: checkWrongPassword},
		{ID: "AUTH-3", Title: "Yetkisiz rol reddedilir (403)", Run: checkRolePermissions},
		{ID: "AUTH-4", Title: "Yetki ihlali denemeleri reddedilir (401)", Run: checkViolations},
		{ID: "UI", Title: "Arayüz ve arayüz üzerinden API", Run: checkFrontend},

		{ID: "F-1", Title: "Müşteri ve hesap yönetimi", Run: checkF1, Timeout: 3 * time.Minute},
		{ID: "F-2", Title: "Çift taraflı defter ve bakiye sorguları", Run: checkF2},
		{ID: "F-3", Title: "Havale/EFT/FAST transferleri", Run: checkF3, Timeout: 3 * time.Minute},
		{ID: "F-4", Title: "Kart provizyon, çekim, iade", Run: checkF4, Timeout: 3 * time.Minute},
		{ID: "F-5", Title: "Kur yönetimi ve çok para birimi", Run: checkF5},
		{ID: "F-6", Title: "Faiz tahakkuku ve vergi kesintisi", Run: checkF6, Timeout: 4 * time.Minute},
		{ID: "F-7", Title: "Risk kuralları ve inceleme kuyruğu", PlannedIn: "J2"},
		{ID: "F-8", Title: "Gün sonu, mutabakat, müşteri ekstresi", PlannedIn: "I3"},

		{ID: "T-1", Title: "Çift taraflı defter ve para korunumu", Run: checkT1, Timeout: 10 * time.Minute},
		{ID: "T-2", Title: "İdempotent transfer ve saga", Run: checkT2, Timeout: 8 * time.Minute},
		{ID: "T-3", Title: "Provizyon ve kullanılabilir bakiye", Run: checkT3, Timeout: 5 * time.Minute},
		{ID: "T-4", Title: "Faiz tahakkuku", Run: checkT4, Timeout: 4 * time.Minute},
		{ID: "T-5", Title: "Mutabakat motoru", PlannedIn: "I3"},
		{ID: "T-6", Title: "Gerçek zamanlı risk kuralları", PlannedIn: "J2"},
		{ID: "T-7", Title: "Zaman yolculuğu, bütünlük, felaket kurtarma", Run: checkT7, Timeout: 3 * time.Minute},
	}
}

func checkHealth(ctx context.Context, env *Env) (string, error) {
	resp, err := env.API(ctx, http.MethodGet, "/health", "", nil)
	if err != nil {
		return "", err
	}
	if err := expectStatus("GET /health", resp, http.StatusOK, ""); err != nil {
		return "", err
	}
	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := resp.JSON(&body); err != nil {
		return "", err
	}
	if body.Status != "ok" || body.Checks["database"] != "ok" {
		return "", fmt.Errorf("beklenen status=ok ve database=ok, gelen %s", resp.Body)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		return "", errors.New("yanıtta X-Request-ID başlığı yok")
	}
	return "200; status=ok, database=ok; X-Request-ID var", nil
}

func checkLoginAllRoles(ctx context.Context, env *Env) (string, error) {
	for _, a := range demoAccounts {
		s, err := env.Login(ctx, a.Email)
		if err != nil {
			return "", err
		}
		if s.Role != a.Role {
			return "", fmt.Errorf("%s: beklenen rol %s, gelen %s", a.Email, a.Role, s.Role)
		}
		me, err := env.API(ctx, http.MethodGet, "/api/v1/me", s.AccessToken, nil)
		if err != nil {
			return "", err
		}
		if err := expectStatus(a.Email+" GET /me", me, http.StatusOK, ""); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%d rolün hepsi giriş yaptı; /me doğru rolü döndürdü", len(demoAccounts)), nil
}

func checkWrongPassword(ctx context.Context, env *Env) (string, error) {
	wrong, err := env.API(ctx, http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"email": "operasyon@parakopru.test", "password": "kesinlikle-yanlis"})
	if err != nil {
		return "", err
	}
	if err := expectStatus("yanlış parola", wrong, http.StatusUnauthorized, "INVALID_CREDENTIALS"); err != nil {
		return "", err
	}
	if wrong.Header.Get("Set-Cookie") != "" {
		return "", errors.New("yanlış parolada oturum çerezi verilmemeli")
	}
	unknown, err := env.API(ctx, http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"email": "boyle-biri-yok@parakopru.test", "password": "kesinlikle-yanlis"})
	if err != nil {
		return "", err
	}
	if err := expectStatus("kayıtlı olmayan e-posta", unknown, http.StatusUnauthorized, "INVALID_CREDENTIALS"); err != nil {
		return "", err
	}
	if string(wrong.Body) != string(unknown.Body) {
		return "", errors.New("yanlış parola ile kayıtlı olmayan e-postanın yanıtları farklı; e-postanın varlığı anlaşılabilir")
	}
	return "401 INVALID_CREDENTIALS; kayıtlı olmayan e-postayla aynı yanıt", nil
}

// Denetim kaydını yalnız uyum uzmanı ve denetçi görebilir (PLAN.md §5.10); diğer roller 403 almalı.
func checkRolePermissions(ctx context.Context, env *Env) (string, error) {
	allowed := map[string]bool{"COMPLIANCE": true, "AUDITOR": true}
	var denied []string
	for _, a := range demoAccounts {
		s, err := env.Login(ctx, a.Email)
		if err != nil {
			return "", err
		}
		resp, err := env.API(ctx, http.MethodGet, "/api/v1/audit-log?limit=1", s.AccessToken, nil)
		if err != nil {
			return "", err
		}
		what := a.Role + " GET /audit-log"
		if allowed[a.Role] {
			err = expectStatus(what, resp, http.StatusOK, "")
		} else {
			err = expectStatus(what, resp, http.StatusForbidden, "FORBIDDEN")
			denied = append(denied, a.Role)
		}
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("GET /audit-log: COMPLIANCE ve AUDITOR 200; %s 403", strings.Join(denied, ", ")), nil
}

// Yetki ihlali: token'sız erişim, uydurma token, rolü değiştirilmiş (imzası bozulmuş) token, refresh token'ı
// access yerine kullanma ve çıkıştan sonra oturumu yenileme.
func checkViolations(ctx context.Context, env *Env) (string, error) {
	customer, err := env.Login(ctx, "musteri@parakopru.test")
	if err != nil {
		return "", err
	}
	escalated, err := tamperRole(customer.AccessToken, "AUDITOR")
	if err != nil {
		return "", err
	}
	refreshToken := cookieValue(customer, env.APIURL+"/api/v1/auth/refresh", "pk_refresh")
	if refreshToken == "" {
		return "", errors.New("girişte refresh çerezi (pk_refresh) verilmedi")
	}

	for _, tc := range []struct {
		what, token string
	}{
		{"token olmadan", ""},
		{"uydurma token", "uydurma.token.degeri"},
		{"rolü AUDITOR yapılmış müşteri token'ı", escalated},
		{"refresh token access yerine", refreshToken},
	} {
		resp, err := env.API(ctx, http.MethodGet, "/api/v1/audit-log", tc.token, nil)
		if err != nil {
			return "", err
		}
		if err := expectStatus(tc.what, resp, http.StatusUnauthorized, "UNAUTHORIZED"); err != nil {
			return "", err
		}
	}

	// Nesne düzeyi ihlal: müşteri, başka bir müşterinin hesabını ve hareketlerini okuyamaz (403).
	if err := checkObjectLevel(ctx, env, customer); err != nil {
		return "", err
	}

	// Çıkıştan sonra aynı oturum yenilenemez.
	if _, err := env.Do(ctx, Request{Method: http.MethodPost, URL: env.APIURL + "/api/v1/auth/logout", Session: customer}); err != nil {
		return "", err
	}
	// Çıkış yanıtı çerezi tarayıcıdan siler; saldırganın elinde kalmış eski token'ı elle gönderiyoruz.
	resp, err := env.Do(ctx, Request{
		Method: http.MethodPost, URL: env.APIURL + "/api/v1/auth/refresh",
		Header: http.Header{"Cookie": {"pk_refresh=" + refreshToken}},
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus("çıkıştan sonra yenileme", resp, http.StatusUnauthorized, "SESSION_EXPIRED"); err != nil {
		return "", err
	}
	return "token'sız, uydurma, rolü değiştirilmiş ve refresh token'la erişim 401; müşteri başkasının hesabını okuyamaz 403; çıkış sonrası yenileme 401", nil
}

// Arayüz yanıt veriyor ve arayüzün kökeninden (nginx) API'ye ulaşılabiliyor mu?
func checkFrontend(ctx context.Context, env *Env) (string, error) {
	page, err := env.Do(ctx, Request{Method: http.MethodGet, URL: env.FrontendURL + "/"})
	if err != nil {
		return "", err
	}
	if err := expectStatus("arayüz ana sayfası", page, http.StatusOK, ""); err != nil {
		return "", err
	}
	if !strings.Contains(string(page.Body), "<title>ParaKöprü</title>") {
		return "", errors.New("arayüz sayfası ParaKöprü başlığını içermiyor")
	}

	s, err := env.Login(ctx, "denetci@parakopru.test")
	if err != nil {
		return "", err
	}
	viaUI, err := env.Do(ctx, Request{Method: http.MethodGet, URL: env.FrontendURL + "/api/v1/me", Token: s.AccessToken})
	if err != nil {
		return "", err
	}
	if err := expectStatus("arayüz üzerinden GET /api/v1/me", viaUI, http.StatusOK, ""); err != nil {
		return "", err
	}
	return "sayfa 200; arayüz kökeninden /api/v1/me 200 (nginx → api)", nil
}

// tamperRole, token'ın gövdesindeki rolü değiştirir ama imzayı olduğu gibi bırakır (ayrıcalık yükseltme denemesi).
func tamperRole(token, role string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("access token JWT biçiminde değil")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	claims["role"] = role
	b, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(b)
	return strings.Join(parts, "."), nil
}

func cookieValue(s *Session, rawURL, name string) string {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil) //nolint:noctx // yalnız çerez kavanozunu sorgulamak için
	if err != nil {
		return ""
	}
	for _, c := range s.jar.Cookies(req.URL) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// checkObjectLevel müşteri token'ıyla başka bir müşterinin hesabını ve hareketlerini ister; ikisi de 403 olmalı.
func checkObjectLevel(ctx context.Context, env *Env, customer *Session) error {
	ops, err := env.Login(ctx, "operasyon@parakopru.test")
	if err != nil {
		return err
	}
	own := map[string]bool{}
	if err := env.accountsPage(ctx, "/api/v1/accounts", customer.AccessToken, func(a acct) bool { own[a.ID] = true; return false }); err != nil {
		return err
	}
	var other string
	if err := env.accountsPage(ctx, "/api/v1/accounts", ops.AccessToken, func(a acct) bool {
		if a.CustomerID != nil && !own[a.ID] {
			other = a.ID
		}
		return other != ""
	}); err != nil {
		return err
	}
	if other == "" {
		return errors.New("başka bir müşterinin hesabı bulunamadı (simülatör verisi yok mu?)")
	}
	for _, path := range []string{"/api/v1/accounts/" + other, "/api/v1/accounts/" + other + "/entries"} {
		resp, err := env.API(ctx, http.MethodGet, path, customer.AccessToken, nil)
		if err != nil {
			return err
		}
		if err := expectStatus("müşteri başkasının hesabı "+path, resp, http.StatusForbidden, "FORBIDDEN"); err != nil {
			return err
		}
	}
	return nil
}
