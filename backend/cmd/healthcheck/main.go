// Command healthcheck, Docker healthcheck'in çağırdığı küçük HTTP istemcisidir.
//
// Go servisleri distroless imajda çalışır; imajda kabuk, curl ya da wget yoktur (saldırı yüzeyi küçük kalsın diye).
// Bu yüzden sağlık kontrolü için bu binary kullanılır:
//
//	healthcheck http://localhost:8080/health
//
// Yanıt 2xx ise çıkış kodu 0 (sağlıklı), aksi halde 1 (sağlıksız) olur.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const timeout = 3 * time.Second

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: healthcheck <url>")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := check(ctx, os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		os.Exit(1)
	}
}

func check(ctx context.Context, url string) error {
	// Adres kullanıcıdan değil, compose'daki healthcheck tanımından gelir; SSRF uyarısı burada geçersiz.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // bkz. üstteki not
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // bkz. üstteki not
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
