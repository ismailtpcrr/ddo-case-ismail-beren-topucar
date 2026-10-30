package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"
)

// Env kontrollerin ortak ortamıdır.
type Env struct {
	APIURL       string // ör. http://api:8080
	FrontendURL  string // ör. http://frontend:8080
	DemoPassword string
	MinCustomers int // F-1: simülatörün seed ettiği en az müşteri sayısı
	T1Transfers  int // T-1: eşzamanlı gönderilecek transfer sayısı
	http         *http.Client
}

// NewEnv yeni bir ortam oluşturur.
func NewEnv(apiURL, frontendURL, demoPassword string) *Env {
	return &Env{
		APIURL: apiURL, FrontendURL: frontendURL, DemoPassword: demoPassword, MinCustomers: 20000, T1Transfers: 10000,
		// T-1 yüzlerce eşzamanlı istek gönderir: bağlantılar yeniden kullanılsın.
		http: &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 256, IdleConnTimeout: 30 * time.Second}},
	}
}

// Response bir HTTP yanıtıdır.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// ErrorCode yanıttaki {"error":{"code"}} değerini döndürür.
func (r Response) ErrorCode() string {
	var b struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &b)
	return b.Error.Code
}

// JSON gövdeyi v'ye çözer.
func (r Response) JSON(v any) error {
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("yanıt JSON değil (%d): %s", r.Status, short(r.Body))
	}
	return nil
}

// Request tek bir HTTP isteğidir.
type Request struct {
	Method  string
	URL     string
	Token   string // varsa Authorization: Bearer
	Body    any    // varsa JSON olarak gönderilir
	Header  http.Header
	Session *Session // varsa çerezleri (refresh token) taşır
}

// Do isteği gönderir.
func (e *Env) Do(ctx context.Context, r Request) (Response, error) {
	var body io.Reader
	if r.Body != nil {
		b, err := json.Marshal(r.Body)
		if err != nil {
			return Response{}, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
	if err != nil {
		return Response{}, err
	}
	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	for k, v := range r.Header {
		req.Header[k] = v
	}
	client := e.http
	if r.Session != nil {
		client = &http.Client{Timeout: e.http.Timeout, Jar: r.Session.jar}
	}
	resp, err := client.Do(req) //nolint:gosec // adresler verify yapılandırmasından gelir
	if err != nil {
		return Response{}, fmt.Errorf("%s %s: %w", r.Method, r.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return Response{}, err
	}
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: b}, nil
}

// API, API adresine göre istek gönderir.
func (e *Env) API(ctx context.Context, method, path, token string, body any) (Response, error) {
	return e.Do(ctx, Request{Method: method, URL: e.APIURL + path, Token: token, Body: body})
}

// Session bir kullanıcının giriş oturumudur.
type Session struct {
	Email       string
	Role        string
	AccessToken string
	jar         http.CookieJar
}

// Login demo parolasıyla giriş yapar.
func (e *Env) Login(ctx context.Context, email string) (*Session, error) {
	return e.LoginWith(ctx, email, e.DemoPassword)
}

// LoginWith verilen parolayla giriş yapar; 200 dışındaki yanıt hatadır.
func (e *Env) LoginWith(ctx context.Context, email, password string) (*Session, error) {
	jar, _ := cookiejar.New(nil)
	s := &Session{Email: email, jar: jar}
	resp, err := e.Do(ctx, Request{
		Method: http.MethodPost, URL: e.APIURL + "/api/v1/auth/login",
		Body: map[string]string{"email": email, "password": password}, Session: s,
	})
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("%s girişi: beklenen 200, gelen %d %s", email, resp.Status, short(resp.Body))
	}
	var body struct {
		AccessToken string `json:"access_token"`
		User        struct {
			Role string `json:"role"`
		} `json:"user"`
	}
	if err := resp.JSON(&body); err != nil {
		return nil, err
	}
	s.AccessToken, s.Role = body.AccessToken, body.User.Role
	return s, nil
}

// expectStatus yanıt durumu beklenen değilse açıklayıcı bir hata döndürür.
func expectStatus(what string, resp Response, want int, wantCode string) error {
	if resp.Status != want {
		return fmt.Errorf("%s: beklenen %d, gelen %d %s", what, want, resp.Status, short(resp.Body))
	}
	if wantCode != "" && resp.ErrorCode() != wantCode {
		return fmt.Errorf("%s: beklenen hata kodu %s, gelen %q", what, wantCode, resp.ErrorCode())
	}
	return nil
}

func short(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "…"
	}
	return string(b)
}
