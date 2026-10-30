// Package bank dış bankaya (simülatörün banka sahtesi) giden HTTP istemcisidir (PLAN.md §5.3).
//
// Her çağrının kısa bir zaman aşımı vardır. Ağ hatası, zaman aşımı ve 2xx dışı her yanıt "belirsiz sonuç"tur:
// banka işlemi yapmış olabilir de olmayabilir de. Çağıran (transfer sagası) bu durumda sorgular, tahmin etmez.
package bank

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Status bankanın bir transfer için bildirdiği durumdur.
type Status string

const (
	Completed Status = "COMPLETED"
	Rejected  Status = "REJECTED"
	NotFound  Status = "NOT_FOUND" // banka bu referansı hiç işlemedi
)

// ErrUncertain çağrının sonucunun bilinmediğini söyler (zaman aşımı, bağlantı kopması, banka hatası).
var ErrUncertain = errors.New("bank: outcome uncertain")

// SendRequest bankaya gönderim isteğidir. Reference bizim transfer kimliğimizdir: aynı referansla yeniden gönderim güvenlidir.
type SendRequest struct {
	Reference       string `json:"reference"`
	AmountMinor     int64  `json:"amount_minor"`
	Currency        string `json:"currency"`
	BeneficiaryIBAN string `json:"beneficiary_iban"`
}

// Client bankanın HTTP istemcisidir.
type Client struct {
	baseURL string
	http    *http.Client
}

// New yeni bir Client oluşturur. timeout her çağrının üst sınırıdır.
func New(baseURL string, timeout time.Duration) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: timeout}}
}

// Send transferi bankaya gönderir. Başarılı yanıtta bankanın kararını (COMPLETED/REJECTED) döndürür;
// diğer her durumda ErrUncertain.
func (c *Client) Send(ctx context.Context, r SendRequest) (Status, error) {
	body, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/transfers", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

// Inquire bankanın bu referans için gerçek durumunu sorar.
func (c *Client) Inquire(ctx context.Context, reference string) (Status, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/transfers/"+url.PathEscape(reference), nil)
	if err != nil {
		return "", err
	}
	return c.do(req)
}

func (c *Client) do(req *http.Request) (Status, error) {
	resp, err := c.http.Do(req) //nolint:gosec // adres yapılandırmadan gelir
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUncertain, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: status %d", ErrUncertain, resp.StatusCode)
	}
	var out struct {
		Status Status `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("%w: bad response: %w", ErrUncertain, err)
	}
	switch out.Status {
	case Completed, Rejected, NotFound:
		return out.Status, nil
	}
	return "", fmt.Errorf("%w: unknown status %q", ErrUncertain, out.Status)
}
