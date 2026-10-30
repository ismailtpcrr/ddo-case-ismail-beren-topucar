// Package customer müşteri kayıtlarını yönetir (proje.md F-1).
package customer

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"parakopru/internal/platform/apperr"
)

// Customer bir müşteridir.
type Customer struct {
	ID         uuid.UUID  `json:"id"`
	UserID     *uuid.UUID `json:"user_id"`
	FullName   string     `json:"full_name"`
	NationalID string     `json:"national_id"`
	Email      *string    `json:"email"`
	CreatedAt  time.Time  `json:"created_at"`
}

// CreateInput yeni müşteri bilgisidir.
type CreateInput struct {
	FullName   string `json:"full_name"   validate:"required,min=2,max=200"`
	NationalID string `json:"national_id" validate:"required,len=11,numeric"`
	Email      string `json:"email"       validate:"omitempty,email,max=254"`
}

var (
	ErrNotFound = apperr.ErrNotFound.WithMessage("Müşteri bulunamadı.")
	ErrExists   = apperr.New(http.StatusConflict, "CUSTOMER_EXISTS", "Bu kimlik numarasıyla kayıtlı bir müşteri zaten var.")
	// ErrNoCustomerProfile müşteri rolündeki kullanıcının bağlı bir müşteri kaydı yok.
	ErrNoCustomerProfile = apperr.ErrForbidden.WithMessage("Kullanıcınıza bağlı bir müşteri kaydı yok.")
)
