// Package config, tüm ayarları ortam değişkenlerinden okur.
//
// Her süreç (api, worker, simulator…) kendi ayar struct'ını kullanır. Zorunlu bir ayar eksik ya da
// hatalıysa Load hata döner ve süreç başlamaz; yanlış ayarla yarım çalışan bir servis olmaz.
package config

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/caarlos0/env/v11"
)

// API, api sürecinin ayarlarıdır.
type API struct {
	HTTPAddr        string        `env:"HTTP_ADDR"        envDefault:":8080"`
	LogLevel        slog.Level    `env:"LOG_LEVEL"        envDefault:"info"`
	RequestTimeout  time.Duration `env:"REQUEST_TIMEOUT"  envDefault:"10s"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`
}

// Load ayarları süreç ortamından okur.
func Load[T any]() (T, error) {
	cfg, err := env.ParseAs[T]()
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// LoadFrom ayarları verilen değişken kümesinden okur (testler için).
func LoadFrom[T any](vars map[string]string) (T, error) {
	cfg, err := env.ParseAsWithOptions[T](env.Options{Environment: vars})
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}
