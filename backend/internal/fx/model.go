// Package fx kur yönetimi ve para birimi dönüşümüdür (proje.md F-5; PLAN.md §5.5).
//
// Kur, yabancı para biriminin TL karşılığıdır (1 USD = rate TRY), zaman geçerlilikli ve eklemeli-yalnızdır: yeni kur
// eskisini değiştirmez; "o anda hangi kur geçerliydi" sorulabilir. İki yabancı para birimi arasındaki çapraz kur TL
// üzerinden hesaplanır. Dönüşüm decimal ile yapılır, sonuç banker's rounding ile kuruşa çevrilir; kayan nokta kullanılmaz.
package fx

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"parakopru/internal/money"
	"parakopru/internal/platform/apperr"
)

// Rate bir kur kaydıdır. Rate, metin olarak taşınır (ondalık sayı; kayan nokta değil).
type Rate struct {
	ID        uuid.UUID       `json:"id"`
	Base      money.Currency  `json:"base"`
	Quote     money.Currency  `json:"quote"`
	Rate      decimal.Decimal `json:"rate"`
	ValidFrom time.Time       `json:"valid_from"`
	CreatedBy *uuid.UUID      `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
}

// CreateInput yeni kur girişidir. Rate JSON'da metin olmalıdır ("41.2500"); ValidFrom boşsa şimdi.
type CreateInput struct {
	Base      money.Currency `json:"base"       validate:"required,oneof=USD EUR"`
	Quote     money.Currency `json:"quote"      validate:"omitempty,eq=TRY"`
	Rate      string         `json:"rate"       validate:"required,max=30"`
	ValidFrom *time.Time     `json:"valid_from"`
}

// Conversion bir dönüşümün sonucudur: alıcıya geçen tutar ve kullanılan (çapraz) kur: 1 From = Rate To.
type Conversion struct {
	From, To money.Currency
	Rate     decimal.Decimal
	Amount   money.Amount // To para biriminde
}

var (
	ErrRateUnavailable = apperr.New(http.StatusUnprocessableEntity, "FX_RATE_UNAVAILABLE", "Bu para birimi için geçerli bir kur tanımlı değil.")
	ErrInvalidRate     = apperr.New(http.StatusBadRequest, "VALIDATION_ERROR", "rate pozitif bir ondalık sayı olmalı (en fazla 8 ondalık basamak, ör. \"41.2500\").")
	ErrRateExists      = apperr.New(http.StatusConflict, "RATE_EXISTS", "Bu para birimi için aynı geçerlilik zamanında bir kur zaten var.")
)
