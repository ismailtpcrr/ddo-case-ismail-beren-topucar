// Package apperr, uygulama genelinde kullanılan hata tipini tanımlar.
//
// Servis katmanı iş kuralı hatalarını bu tiple döndürür; HTTP katmanı (httpx.WriteError)
// bunları şartnamedeki {"error":{"code":"…","message":"…"}} biçimine çevirir.
// Code makine tarafından okunur (frontend ve testler buna bakar), Message kullanıcıya gösterilir.
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

// Error, HTTP durum kodu ve makine okunur kodu taşıyan uygulama hatasıdır.
type Error struct {
	Code    string // ör. INSUFFICIENT_FUNDS
	Status  int    // HTTP durum kodu
	Message string // kullanıcıya gösterilecek açıklama
	cause   error  // iç neden; yalnız loglanır, istemciye gönderilmez
}

// New yeni bir hata tanımı oluşturur.
func New(status int, code, message string) *Error {
	return &Error{Code: code, Status: status, Message: message}
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return e.Code + ": " + e.Message
}

// Unwrap iç nedeni döndürür (errors.Is / errors.As zinciri için).
func (e *Error) Unwrap() error { return e.cause }

// Is, iki apperr.Error'ı koduna göre eşleştirir. Böylece mesajı değiştirilmiş bir kopya da
// errors.Is(err, apperr.ErrNotFound) ile tanınır.
func (e *Error) Is(target error) bool {
	var t *Error
	if !errors.As(target, &t) {
		return false
	}
	return t.Code == e.Code
}

// WithMessage aynı kod ve durumla, farklı mesajlı bir kopya döndürür. Önceden tanımlı hata değişmez.
func (e *Error) WithMessage(msg string) *Error {
	c := *e
	c.Message = msg
	return &c
}

// Messagef, WithMessage'ın biçimli halidir.
func (e *Error) Messagef(format string, args ...any) *Error {
	return e.WithMessage(fmt.Sprintf(format, args...))
}

// Wrap iç nedeni ekleyerek bir kopya döndürür.
func (e *Error) Wrap(cause error) *Error {
	c := *e
	c.cause = cause
	return &c
}

// As, err zincirinde bir *Error varsa onu döndürür.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Modüllerden bağımsız, her yerde kullanılan hatalar.
// İş alanına özgü hatalar (ör. yetersiz bakiye) kendi modüllerinde tanımlanır.
var (
	ErrValidation   = New(http.StatusBadRequest, "VALIDATION_ERROR", "İstek geçersiz.")
	ErrUnauthorized = New(http.StatusUnauthorized, "UNAUTHORIZED", "Kimlik doğrulaması gerekli.")
	ErrForbidden    = New(http.StatusForbidden, "FORBIDDEN", "Bu işlem için yetkiniz yok.")
	ErrNotFound     = New(http.StatusNotFound, "NOT_FOUND", "Kayıt bulunamadı.")
	ErrConflict     = New(http.StatusConflict, "CONFLICT", "İstek, kaydın mevcut durumuyla çakışıyor.")
	ErrTooLarge     = New(http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "İstek gövdesi çok büyük.")
	ErrInternal     = New(http.StatusInternalServerError, "INTERNAL", "Beklenmeyen bir hata oluştu.")
	ErrUnavailable  = New(http.StatusServiceUnavailable, "UNAVAILABLE", "Servis geçici olarak kullanılamıyor.")
	ErrTimeout      = New(http.StatusGatewayTimeout, "TIMEOUT", "İstek zaman aşımına uğradı.")
)
