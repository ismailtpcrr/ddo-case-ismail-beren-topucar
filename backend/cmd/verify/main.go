// Command verify, `make verify` ile çalışan uçtan uca doğrulamadır (proje.md §10).
//
// Ayakta ve seed edilmiş sisteme dışarıdan, gerçek HTTP istekleriyle bağlanır; her doğrulama kimliği için
// PASS/FAIL tablosu basar. Bir kimlik bile FAIL ise çıkış kodu 1'dir. Toplam süre VERIFY_TIMEOUT ile sınırlıdır
// (varsayılan 14 dk; şartname sınırı 15 dk).
//
// Compose içinde: make verify (docker compose --profile verify run --rm verify)
// Yerelde:        VERIFY_API_URL=http://localhost:8080 VERIFY_FRONTEND_URL=http://localhost:3000 \
//
//	DEMO_PASSWORD=ParaKopru.2026 go run ./cmd/verify
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"parakopru/internal/platform/config"
	"parakopru/internal/verify"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load[config.Verify]()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	fmt.Printf("ParaKöprü doğrulaması — API: %s, arayüz: %s\n\n", cfg.APIURL, cfg.FrontendURL)
	start := time.Now()
	env := verify.NewEnv(cfg.APIURL, cfg.FrontendURL, cfg.DemoPassword)
	env.MinCustomers, env.T1Transfers = cfg.MinCustomers, cfg.T1Transfers
	results := verify.RunAll(ctx, env, verify.Checks(), os.Stdout)

	fmt.Println()
	ok := verify.PrintTable(os.Stdout, results)
	fmt.Printf("Süre: %s\n", time.Since(start).Round(time.Second))
	if !ok {
		return 1
	}
	return 0
}
