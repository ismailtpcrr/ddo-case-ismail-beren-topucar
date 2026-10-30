package fx

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"parakopru/internal/money"
	"parakopru/internal/platform/audit"
	"parakopru/internal/platform/auth"
	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/ids"
)

// Service kur iş kuralları.
type Service struct {
	tx    *db.TxManager
	ids   ids.Generator
	clock clock.Clock
	audit *audit.Recorder
}

// NewService yeni bir Service oluşturur.
func NewService(tx *db.TxManager, idGen ids.Generator, c clock.Clock, rec *audit.Recorder) *Service {
	return &Service{tx: tx, ids: idGen, clock: c, audit: rec}
}

const maxScale = 8

// parseRate kuru metinden okur: pozitif, en fazla 8 ondalık basamak, üs gösterimi yok.
func parseRate(s string) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "eE+-_ ,") {
		return decimal.Decimal{}, ErrInvalidRate
	}
	d, err := decimal.NewFromString(s)
	if err != nil || !d.IsPositive() || d.Exponent() < -maxScale || d.GreaterThan(decimal.NewFromInt(1_000_000)) {
		return decimal.Decimal{}, ErrInvalidRate
	}
	return d, nil
}

// Create yeni bir kur ekler (operasyon uzmanı). Geçerlilik zamanı verilmezse şimdi. Eski kurlar değişmez.
func (s *Service) Create(ctx context.Context, p auth.Principal, in CreateInput) (Rate, error) {
	d, err := parseRate(in.Rate)
	if err != nil {
		return Rate{}, err
	}
	at := s.clock.Now()
	if in.ValidFrom != nil {
		at = in.ValidFrom.UTC().Truncate(time.Microsecond)
	}
	r := Rate{ID: s.ids.NewID(), Base: in.Base, Quote: money.TRY, Rate: d, ValidFrom: at, CreatedBy: &p.UserID}
	ctx = auth.WithPrincipal(ctx, p)
	err = s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		if err := insert(ctx, tx, r); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{Action: "fx.rate_create", Entity: "fx_rate", EntityID: r.ID.String(), New: r})
	})
	if err != nil {
		return Rate{}, err
	}
	return scanBack(ctx, s.tx.DB(), r.ID)
}

func scanBack(ctx context.Context, q db.DBTX, id uuid.UUID) (Rate, error) {
	return scanRate(q.QueryRow(ctx, `SELECT `+cols+` FROM fx_rates WHERE id = $1`, id))
}

// List kurları listeler (en yeni geçerlilik önce). base ve asOf süzer: asOf verilirse o anda geçerli olanlar ve öncekiler.
func (s *Service) List(ctx context.Context, base *money.Currency, asOf *time.Time, limit int) ([]Rate, error) {
	return history(ctx, s.tx.DB(), base, asOf, limit)
}

// EnsureDefaults başlangıç kurlarını ekler (yoksa); api her açılışta çağırır (temel seed). validFrom geçmişte bir tarihtir,
// böylece geçmiş tarihli sorgularda da bir kur bulunur.
func (s *Service) EnsureDefaults(ctx context.Context, validFrom time.Time, defaults map[money.Currency]string) error {
	ctx = audit.WithSystemActor(ctx)
	return s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		for _, c := range []money.Currency{money.USD, money.EUR} {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM fx_rates WHERE base = $1`, c).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			d, err := parseRate(defaults[c])
			if err != nil {
				return err
			}
			r := Rate{ID: s.ids.NewID(), Base: c, Quote: money.TRY, Rate: d, ValidFrom: validFrom}
			if err := insert(ctx, tx, r); err != nil {
				return err
			}
		}
		return nil
	})
}

// rateToTRY c para biriminin at anındaki TL kuru (TRY için 1).
func (s *Service) rateToTRY(ctx context.Context, q db.DBTX, c money.Currency, at time.Time) (decimal.Decimal, error) {
	if c == money.TRY {
		return decimal.NewFromInt(1), nil
	}
	r, err := current(ctx, q, c, at)
	return r.Rate, err
}

// crossPrecision çapraz kurun ondalık basamağıdır (saklanan numeric(28,12) ile aynı).
const crossPrecision = 12

// Convert amount'u to para birimine çevirir (şimdiki kurla): 1 from = Rate to. Aynı para biriminde dönüşüm yoktur
// (Rate = 1). Sonuç banker's rounding ile kuruşa yuvarlanır.
func (s *Service) Convert(ctx context.Context, q db.DBTX, amount money.Amount, to money.Currency) (Conversion, error) {
	if q == nil {
		q = s.tx.DB()
	}
	if amount.Currency == to {
		return Conversion{From: amount.Currency, To: to, Rate: decimal.NewFromInt(1), Amount: amount}, nil
	}
	at := s.clock.Now()
	from, err := s.rateToTRY(ctx, q, amount.Currency, at)
	if err != nil {
		return Conversion{}, err
	}
	target, err := s.rateToTRY(ctx, q, to, at)
	if err != nil {
		return Conversion{}, err
	}
	cross := from.DivRound(target, crossPrecision) // 1 amount.Currency = cross to
	out, err := money.FromDecimal(amount.Decimal().Mul(cross), to)
	if err != nil {
		return Conversion{}, err
	}
	return Conversion{From: amount.Currency, To: to, Rate: cross, Amount: out}, nil
}

// TRYEquivalentMinor tutarın şimdiki kurla TL karşılığıdır (kuruş). Dört göz eşiği için kullanılır.
func (s *Service) TRYEquivalentMinor(ctx context.Context, q db.DBTX, amount money.Amount) (int64, error) {
	c, err := s.Convert(ctx, q, amount, money.TRY)
	return c.Amount.Minor, err
}
