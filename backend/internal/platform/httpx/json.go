// Package httpx, handler'ların ortak HTTP yardımcılarıdır: JSON okuma/yazma, girdi doğrulama,
// tutarlı hata biçimi ve sayfalama parametreleri.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"

	"parakopru/internal/platform/apperr"
)

// MaxBodyBytes JSON istek gövdesinin üst sınırıdır. Büyük dosyalar (ör. mutabakat ekstresi)
// bu yoldan değil, kendi akış tabanlı uçlarından yüklenir.
const MaxBodyBytes = 1 << 20 // 1 MiB

// validate, struct etiketlerindeki kuralları uygular. Kural bilgisini önbelleğe aldığı için
// tek örnek kullanılması önerilir; durum tutmaz.
var validate = newValidator()

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	// Hata mesajlarında Go alan adı yerine istemcinin gördüğü JSON adı geçsin.
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			return f.Name
		}
		return name
	})
	return v
}

// WriteJSON v'yi verilen durum koduyla JSON olarak yazar.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	// Başlık yazıldıktan sonra kodlama hatası istemciye iletilemez; yalnız bağlantı kopmuş olabilir.
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON gövdeyi dst'ye çözer ve struct etiketlerine göre doğrular.
// Bilinmeyen alan, birden fazla JSON değeri ve 1 MiB üstü gövde reddedilir.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return apperr.ErrValidation.WithMessage("İstek gövdesi tek bir JSON nesnesi olmalı.")
	}
	return Validate(dst)
}

func decodeError(err error) error {
	var (
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
		maxErr    *http.MaxBytesError
	)
	switch {
	case errors.Is(err, io.EOF):
		return apperr.ErrValidation.WithMessage("İstek gövdesi boş.")
	case errors.As(err, &maxErr):
		return apperr.ErrTooLarge
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		return apperr.ErrValidation.WithMessage("İstek gövdesi geçerli JSON değil.")
	case errors.As(err, &typeErr):
		return apperr.ErrValidation.Messagef("%s alanının tipi hatalı.", typeErr.Field)
	// encoding/json bilinmeyen alan için ayrı bir hata tipi sunmuyor; belgelenmiş mesaj biçimine bakılır.
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.TrimPrefix(err.Error(), "json: unknown field ")
		return apperr.ErrValidation.Messagef("Bilinmeyen alan: %s.", field)
	default:
		return apperr.ErrValidation.WithMessage("İstek gövdesi okunamadı.")
	}
}

// Validate v'yi struct etiketlerine göre doğrular; ilk hatalı alanı Türkçe mesajla döndürür.
func Validate(v any) error {
	err := validate.Struct(v)
	if err == nil {
		return nil
	}
	var fieldErrs validator.ValidationErrors
	if !errors.As(err, &fieldErrs) || len(fieldErrs) == 0 {
		return fmt.Errorf("validate: %w", err)
	}
	msgs := make([]string, 0, len(fieldErrs))
	for _, fe := range fieldErrs {
		msgs = append(msgs, fieldMessage(fe))
	}
	return apperr.ErrValidation.WithMessage(strings.Join(msgs, " "))
}

func fieldMessage(fe validator.FieldError) string {
	f, p := fe.Field(), fe.Param()
	unit := ""
	if fe.Kind() == reflect.String {
		unit = " karakter"
	}
	switch fe.Tag() {
	case "required":
		return f + " zorunludur."
	case "gt":
		return fmt.Sprintf("%s %s değerinden büyük olmalı.", f, p)
	case "gte":
		return fmt.Sprintf("%s en az %s olmalı.", f, p)
	case "lt":
		return fmt.Sprintf("%s %s değerinden küçük olmalı.", f, p)
	case "lte":
		return fmt.Sprintf("%s en fazla %s olmalı.", f, p)
	case "min":
		return fmt.Sprintf("%s en az %s%s olmalı.", f, p, unit)
	case "max":
		return fmt.Sprintf("%s en fazla %s%s olmalı.", f, p, unit)
	case "len":
		return fmt.Sprintf("%s tam %s%s olmalı.", f, p, unit)
	case "oneof":
		return fmt.Sprintf("%s şunlardan biri olmalı: %s.", f, p)
	case "email":
		return f + " geçerli bir e-posta adresi olmalı."
	case "uuid", "uuid4", "uuid7":
		return f + " geçerli bir UUID olmalı."
	default:
		return f + " geçersiz."
	}
}
