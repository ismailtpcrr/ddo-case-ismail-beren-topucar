// Package config, tüm ayarları ortam değişkenlerinden okur.
//
// Her süreç (api, worker, simulator…) kendi ayar struct'ını kullanır. Zorunlu bir ayar eksik ya da
// hatalıysa Load hata döner ve süreç başlamaz; yanlış ayarla yarım çalışan bir servis olmaz.
package config

import (
	"errors"
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

	// Uygulama rolüyle bağlantı adresi (postgres://kullanıcı:parola@host:5432/db).
	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`
	DBMaxConns  int32  `env:"DB_MAX_CONNS" envDefault:"20"`
}

// Validate etiketlerle ifade edilemeyen kuralları kontrol eder.
func (c API) Validate() error {
	switch {
	case c.RequestTimeout <= 0:
		return errors.New("REQUEST_TIMEOUT must be positive")
	case c.ShutdownTimeout <= 0:
		return errors.New("SHUTDOWN_TIMEOUT must be positive")
	case c.DBMaxConns < 1:
		return errors.New("DB_MAX_CONNS must be at least 1")
	}
	return nil
}

// validator, Load'un okuduktan sonra çağırdığı isteğe bağlı kontrol arayüzüdür.
type validator interface{ Validate() error }

// Load ayarları süreç ortamından okur.
func Load[T any]() (T, error) {
	return parse[T](env.Options{})
}

// LoadFrom ayarları verilen değişken kümesinden okur (testler için).
func LoadFrom[T any](vars map[string]string) (T, error) {
	return parse[T](env.Options{Environment: vars})
}

func parse[T any](opts env.Options) (T, error) {
	cfg, err := env.ParseAsWithOptions[T](opts)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	if v, ok := any(cfg).(validator); ok {
		if err := v.Validate(); err != nil {
			return cfg, fmt.Errorf("config: %w", err)
		}
	}
	return cfg, nil
}
