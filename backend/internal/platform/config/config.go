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

// Verify, `make verify` doğrulamasının ayarlarıdır.
type Verify struct {
	APIURL       string        `env:"VERIFY_API_URL"      envDefault:"http://api:8080"`
	FrontendURL  string        `env:"VERIFY_FRONTEND_URL" envDefault:"http://frontend:8080"`
	DemoPassword string        `env:"DEMO_PASSWORD,required,notEmpty"`
	Timeout      time.Duration `env:"VERIFY_TIMEOUT"      envDefault:"14m"` // şartname: en fazla 15 dk
	// Simülatörün seed ettiği en az müşteri sayısı (F-1 kontrolü) ve T-1'de gönderilecek eşzamanlı transfer sayısı.
	MinCustomers int `env:"VERIFY_MIN_CUSTOMERS" envDefault:"20000"`
	T1Transfers  int `env:"VERIFY_T1_TRANSFERS"  envDefault:"10000"`
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

// Simulator, veri üretici (simülatör) sürecinin ayarlarıdır.
type Simulator struct {
	HTTPAddr string     `env:"HTTP_ADDR" envDefault:":8080"` // yalnız sağlık ucu; compose'da dışarı açılmaz
	LogLevel slog.Level `env:"LOG_LEVEL" envDefault:"info"`

	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`
	DBMaxConns  int32  `env:"DB_MAX_CONNS" envDefault:"4"`

	// Aynı tohum aynı veriyi üretir (proje.md §8).
	Seed      int64 `env:"SIM_SEED"      envDefault:"20261026"`
	Customers int   `env:"SIM_CUSTOMERS" envDefault:"20000"`

	// Dış banka sahtesi (proje.md Görev 2: %20 hata, %10 çift cevap).
	BankFailurePercent   int           `env:"BANK_FAILURE_PERCENT"   envDefault:"20"`
	BankDuplicatePercent int           `env:"BANK_DUPLICATE_PERCENT" envDefault:"10"`
	BankMinLatency       time.Duration `env:"BANK_MIN_LATENCY"       envDefault:"20ms"`
	BankMaxLatency       time.Duration `env:"BANK_MAX_LATENCY"       envDefault:"250ms"`
	BankHang             time.Duration `env:"BANK_HANG"              envDefault:"15s"` // zaman aşımı hatasında bağlantının asılı kalma süresi

	// Banka bildirimlerinin gönderileceği API (boşsa bildirim yok) ve "sistem" rolündeki hesap.
	APIURL         string `env:"SIM_API_URL"`
	SystemEmail    string `env:"SIM_SYSTEM_EMAIL"    envDefault:"sistem@parakopru.test"`
	SystemPassword string `env:"SIM_SYSTEM_PASSWORD"`
}

// Validate etiketlerle ifade edilemeyen kuralları kontrol eder.
func (c Simulator) Validate() error {
	switch {
	case c.DBMaxConns < 1:
		return errors.New("DB_MAX_CONNS must be at least 1")
	case c.Customers < 0 || c.Customers > 1_000_000:
		return errors.New("SIM_CUSTOMERS must be between 0 and 1000000")
	case c.BankFailurePercent < 0 || c.BankFailurePercent > 100 || c.BankDuplicatePercent < 0 || c.BankDuplicatePercent > 100:
		return errors.New("BANK_*_PERCENT must be between 0 and 100")
	case c.BankMinLatency < 0 || c.BankMaxLatency < c.BankMinLatency:
		return errors.New("BANK_MAX_LATENCY must be at least BANK_MIN_LATENCY")
	}
	return nil
}
