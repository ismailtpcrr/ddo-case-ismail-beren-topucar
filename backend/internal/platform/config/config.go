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
	// Sahip rolle bağlantı adresi; yalnız açılıştaki migrasyon için kullanılır.
	MigrationDatabaseURL string `env:"MIGRATION_DATABASE_URL,required,notEmpty"`

	// JWT imza anahtarı; en az 32 karakter.
	JWTSecret       string        `env:"JWT_SECRET,required,notEmpty"`
	AccessTokenTTL  time.Duration `env:"ACCESS_TOKEN_TTL"  envDefault:"15m"`
	RefreshTokenTTL time.Duration `env:"REFRESH_TOKEN_TTL" envDefault:"168h"`
	// Refresh çerezi yalnız HTTPS'te gönderilsin mi? Yerel demo http://localhost olduğu için varsayılan kapalı.
	CookieSecure bool `env:"COOKIE_SECURE" envDefault:"false"`
	// Açılışta oluşturulan altı demo hesabın parolası.
	DemoPassword string `env:"DEMO_PASSWORD,required,notEmpty"`
}

const minJWTSecretLen = 32

// Validate etiketlerle ifade edilemeyen kuralları kontrol eder.
func (c API) Validate() error {
	switch {
	case c.RequestTimeout <= 0:
		return errors.New("REQUEST_TIMEOUT must be positive")
	case c.ShutdownTimeout <= 0:
		return errors.New("SHUTDOWN_TIMEOUT must be positive")
	case c.DBMaxConns < 1:
		return errors.New("DB_MAX_CONNS must be at least 1")
	case len(c.JWTSecret) < minJWTSecretLen:
		return fmt.Errorf("JWT_SECRET must be at least %d characters", minJWTSecretLen)
	case c.AccessTokenTTL <= 0 || c.RefreshTokenTTL <= c.AccessTokenTTL:
		return errors.New("token TTLs must be positive and REFRESH_TOKEN_TTL longer than ACCESS_TOKEN_TTL")
	}
	return nil
}

// Worker, worker sürecinin (zamanlanmış işler ve iş kuyruğu) ayarlarıdır.
type Worker struct {
	// Yalnız sağlık ucu (/health) için; compose'da dışarı açılmaz.
	HTTPAddr        string        `env:"HTTP_ADDR"        envDefault:":8080"`
	LogLevel        slog.Level    `env:"LOG_LEVEL"        envDefault:"info"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"30s"`

	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`
	DBMaxConns  int32  `env:"DB_MAX_CONNS" envDefault:"10"`

	Concurrency  int           `env:"WORKER_CONCURRENCY"   envDefault:"4"`
	PollInterval time.Duration `env:"WORKER_POLL_INTERVAL" envDefault:"1s"`
	JobLease     time.Duration `env:"WORKER_JOB_LEASE"     envDefault:"2m"`
}

// Validate etiketlerle ifade edilemeyen kuralları kontrol eder.
func (c Worker) Validate() error {
	switch {
	case c.ShutdownTimeout <= 0:
		return errors.New("SHUTDOWN_TIMEOUT must be positive")
	case c.DBMaxConns < 1:
		return errors.New("DB_MAX_CONNS must be at least 1")
	case c.Concurrency < 1:
		return errors.New("WORKER_CONCURRENCY must be at least 1")
	case int64(c.Concurrency) >= int64(c.DBMaxConns):
		return errors.New("DB_MAX_CONNS must be greater than WORKER_CONCURRENCY")
	case c.PollInterval <= 0 || c.JobLease <= 0:
		return errors.New("WORKER_POLL_INTERVAL and WORKER_JOB_LEASE must be positive")
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
