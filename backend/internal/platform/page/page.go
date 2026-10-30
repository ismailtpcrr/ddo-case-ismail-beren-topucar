// Package page, büyük listeler için keyset (imleç) sayfalamayı tanımlar.
//
// OFFSET sayfalamada veritabanı atlanan satırları da okur; sayfa ilerledikçe sorgu yavaşlar ve
// araya yeni kayıt girerse satırlar kayar ya da tekrar eder. Keyset sayfalamada istemciye son görülen
// satırın sıralama anahtarı (imleç) verilir ve sonraki sayfa "WHERE (anahtar) > (imleç)" ile, dizinden
// doğrudan okunur. Sayfa numarası ne olursa olsun maliyet aynıdır.
//
// Bu paket HTTP bilmez; servis ve repo katmanları da kullanır.
package page

import (
	"encoding/base64"
	"encoding/json"

	"parakopru/internal/platform/apperr"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Request bir sayfa isteğidir. Cursor boşsa ilk sayfa istenir.
type Request struct {
	Limit  int
	Cursor string
}

// Page bir sayfa sonucudur. NextCursor nil ise son sayfadır.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// Encode imleç değerini (ör. son satırın sıralama anahtarı) istemciye verilecek opak metne çevirir.
func Encode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Decode istemciden gelen imleci v'ye çözer. Bozuk imleç doğrulama hatasıdır (400).
func Decode(cursor string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return apperr.ErrValidation.WithMessage("cursor geçersiz.")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return apperr.ErrValidation.WithMessage("cursor geçersiz.")
	}
	return nil
}

// New, repodan gelen satırlardan sayfayı oluşturur. Repo Limit+1 satır okur: fazladan satır
// geldiyse sonraki sayfa vardır; o satır atılır ve imleç sayfanın son satırından üretilir.
func New[T any](rows []T, limit int, cursorOf func(T) any) (Page[T], error) {
	p := Page[T]{Items: rows}
	if p.Items == nil {
		p.Items = []T{} // JSON'da null yerine [] dönsün
	}
	if len(rows) <= limit {
		return p, nil
	}
	p.Items = rows[:limit]
	next, err := Encode(cursorOf(p.Items[limit-1]))
	if err != nil {
		return Page[T]{}, err
	}
	p.NextCursor = &next
	return p, nil
}
