package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// APINotifier banka bildirimlerini API'nin webhook ucuna gönderir. "Sistem" rolündeki demo hesapla giriş yapar:
// "her uç noktada RBAC" kuralı simülatör için de geçerlidir.
type APINotifier struct {
	BaseURL, Email, Password string
	client                   http.Client

	mu    sync.Mutex
	token string
}

// NewAPINotifier yeni bir APINotifier oluşturur.
func NewAPINotifier(baseURL, email, password string) *APINotifier {
	return &APINotifier{BaseURL: baseURL, Email: email, Password: password, client: http.Client{Timeout: 10 * time.Second}}
}

// Notify bildirimi gönderir; 401 gelirse yeniden giriş yapıp bir kez tekrar dener.
func (a *APINotifier) Notify(ctx context.Context, n Notification) error {
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := a.login(ctx, attempt > 0)
		if err != nil {
			return err
		}
		status, err := a.post(ctx, "/api/v1/webhooks/bank/transfer-status", tok, n)
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusUnauthorized:
			continue
		case status >= 300:
			return fmt.Errorf("webhook: status %d", status)
		}
		return nil
	}
	return fmt.Errorf("webhook: unauthorized")
}

func (a *APINotifier) login(ctx context.Context, force bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && !force {
		return a.token, nil
	}
	body, _ := json.Marshal(map[string]string{"email": a.Email, "password": a.Password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req) //nolint:gosec // adres yapılandırmadan gelir
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var s struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&s) != nil || s.AccessToken == "" {
		return "", fmt.Errorf("login: status %d", resp.StatusCode)
	}
	a.token = s.AccessToken
	return a.token, nil
}

func (a *APINotifier) post(ctx context.Context, path, token string, v any) (int, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.client.Do(req) //nolint:gosec // adres yapılandırmadan gelir
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}
