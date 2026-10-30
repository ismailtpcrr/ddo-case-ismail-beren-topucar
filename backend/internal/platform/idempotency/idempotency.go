// Package idempotency, durum değiştiren kritik POST isteklerinin Idempotency-Key ile korunmasını sağlar
// (proje.md §7, PLAN.md §5.2): aynı anahtarla gelen tekrar istek, işlemi ikinci kez yapmaz, ilk sonucu döndürür.
//
// Nasıl çalışır (Run):
//  1. İş transaction'ı açılır ve anahtar satırı eklenmeye çalışılır.
//     - Satır yoksa eklenir; bu istek işlemi yapacak olandır.
//     - Aynı anahtarla eşzamanlı başka bir istek satırı eklemiş ama henüz commit etmemişse, PostgreSQL
//     benzersizlik kilidi bu isteği bekletir. O commit edince satır görünür hale gelir.
//     - Satır zaten varsa kayıtlı sonuç döndürülür; işlem tekrar yapılmaz.
//  2. İşlem aynı transaction içinde çalışır; sonucu (ya da iş kuralı reddi) satıra yazılır ve hepsi birlikte commit edilir.
//
// Anahtar başka bir istek gövdesiyle tekrar kullanılırsa 422 IDEMPOTENCY_KEY_REUSED döner.
// Beklenmeyen hatalarda (5xx) hiçbir şey kaydedilmez; istemci aynı anahtarla güvenle yeniden deneyebilir.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/db"
)

// HeaderName istemcinin anahtarı gönderdiği HTTP başlığıdır.
const HeaderName = "Idempotency-Key"

// ReplayedHeader, yanıtın daha önce kaydedilmiş bir sonuç olduğunu bildiren başlıktır.
const ReplayedHeader = "Idempotent-Replayed"

// MarkReplayed, sonuç kayıttan geldiyse yanıta Idempotent-Replayed: true başlığını ekler.
// Handler yanıtı yazmadan önce çağırır.
func MarkReplayed(w http.ResponseWriter, replayed bool) {
	if replayed {
		w.Header().Set(ReplayedHeader, "true")
	}
}

const maxKeyLen = 255

var (
	ErrKeyRequired = apperr.New(http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED",
		"Bu işlem Idempotency-Key başlığı gerektirir.")
	ErrKeyInvalid = apperr.New(http.StatusBadRequest, "IDEMPOTENCY_KEY_INVALID",
		"Idempotency-Key en fazla 255 karakterlik, boşluk içermeyen görünür ASCII olmalı.")
	ErrKeyReused = apperr.New(http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED",
		"Bu Idempotency-Key daha önce farklı bir istekle kullanıldı.")
)

// FromRequest Idempotency-Key başlığını okur ve doğrular. Yoksa 400 döner (PLAN.md §7).
func FromRequest(r *http.Request) (string, error) {
	v := r.Header.Get(HeaderName)
	if v == "" {
		return "", ErrKeyRequired
	}
	if len(v) > maxKeyLen {
		return "", ErrKeyInvalid
	}
	for i := range len(v) {
		if v[i] <= ' ' || v[i] > '~' {
			return "", ErrKeyInvalid
		}
	}
	return v, nil
}

// Key bir isteğin idempotency kimliğidir. Anahtar kullanıcıya özeldir: iki farklı kullanıcının
// aynı anahtar metnini kullanması çakışma yaratmaz.
type Key struct {
	UserID    uuid.UUID
	Value     string
	Operation string // ör. transfer.create
	hash      []byte
}

// NewKey anahtarı oluşturur. request, işlemin girdisidir; özeti "aynı anahtar, farklı istek" kontrolünde kullanılır.
func NewKey(userID uuid.UUID, value, operation string, request any) (Key, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return Key{}, fmt.Errorf("idempotency: marshal request: %w", err)
	}
	h := sha256.New()
	h.Write([]byte(operation))
	h.Write([]byte{0})
	h.Write(body)
	return Key{UserID: userID, Value: value, Operation: operation, hash: h.Sum(nil)}, nil
}

// Run fn'i anahtarla korunan tek bir transaction içinde çalıştırır.
//
// Dönen replayed değeri, sonucun daha önce kaydedilmiş bir sonuç olup olmadığını söyler (handler bunu
// Idempotent-Replayed başlığıyla bildirir). fn içindeki yazmalar, sonuçla birlikte commit edilir.
func Run[T any](ctx context.Context, tm *db.TxManager, key Key, fn func(ctx context.Context, tx db.DBTX) (T, error)) (result T, replayed bool, err error) {
	var bizErr error
	txErr := tm.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		var zero T
		result, replayed, bizErr = zero, false, nil // yeniden denemede temiz başla

		claimed, err := claim(ctx, tx, key)
		if err != nil {
			return err
		}
		if !claimed {
			replayed = true
			result, bizErr, err = load[T](ctx, tx, key)
			return err
		}

		// İş kuralı reddinde fn'in yarım yazmaları geri alınabilsin diye savepoint.
		if _, err := tx.Exec(ctx, "SAVEPOINT idempotent_operation"); err != nil {
			return fmt.Errorf("idempotency: savepoint: %w", err)
		}
		result, bizErr = fn(ctx, tx)
		if bizErr == nil {
			return complete(ctx, tx, key, result, nil)
		}
		ae, ok := apperr.As(bizErr)
		if !ok || ae.Status >= http.StatusInternalServerError {
			return bizErr // beklenmeyen hata: her şey geri alınır, anahtar kaydedilmez
		}
		// İş kuralı reddi de bir sonuçtur: yarım yazmalar geri alınır, ret kaydedilir ve commit edilir.
		if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT idempotent_operation"); err != nil {
			return fmt.Errorf("idempotency: rollback to savepoint: %w", err)
		}
		return complete(ctx, tx, key, result, ae)
	})
	if txErr != nil {
		var zero T
		return zero, false, txErr
	}
	return result, replayed, bizErr
}

// claim anahtar satırını eklemeye çalışır; eklendiyse true döner.
func claim(ctx context.Context, tx db.DBTX, k Key) (bool, error) {
	tag, err := tx.Exec(ctx,
		`INSERT INTO idempotency_keys (user_id, key, operation, request_hash)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (user_id, key) DO NOTHING`,
		k.UserID, k.Value, k.Operation, k.hash)
	if err != nil {
		return false, fmt.Errorf("idempotency: claim: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func complete(ctx context.Context, tx db.DBTX, k Key, result any, bizErr *apperr.Error) error {
	var (
		resultJSON          []byte
		errStatus           *int
		errCode, errMessage *string
		err                 error
	)
	if bizErr != nil {
		errStatus, errCode, errMessage = &bizErr.Status, &bizErr.Code, &bizErr.Message
	} else if resultJSON, err = json.Marshal(result); err != nil {
		return fmt.Errorf("idempotency: marshal result: %w", err)
	}
	_, err = tx.Exec(ctx,
		`UPDATE idempotency_keys
		    SET result = $3, error_status = $4, error_code = $5, error_message = $6, completed_at = now()
		  WHERE user_id = $1 AND key = $2`,
		k.UserID, k.Value, resultJSON, errStatus, errCode, errMessage)
	if err != nil {
		return fmt.Errorf("idempotency: complete: %w", err)
	}
	return nil
}

// load kayıtlı sonucu okur. İstek özeti farklıysa ErrKeyReused döner.
func load[T any](ctx context.Context, tx db.DBTX, k Key) (T, error, error) {
	var (
		zero                T
		operation           string
		hash, resultJSON    []byte
		errStatus           *int
		errCode, errMessage *string
	)
	err := tx.QueryRow(ctx,
		`SELECT operation, request_hash, result, error_status, error_code, error_message
		   FROM idempotency_keys WHERE user_id = $1 AND key = $2 AND completed_at IS NOT NULL`,
		k.UserID, k.Value).Scan(&operation, &hash, &resultJSON, &errStatus, &errCode, &errMessage)
	if errors.Is(err, pgx.ErrNoRows) {
		// Satır var ama tamamlanmamış görünüyor: normalde mümkün değil (sonuç commit'ten önce yazılır).
		return zero, nil, apperr.ErrConflict.WithMessage("Aynı Idempotency-Key ile bir işlem hâlâ sürüyor.")
	}
	if err != nil {
		return zero, nil, fmt.Errorf("idempotency: load: %w", err)
	}
	if operation != k.Operation || !bytes.Equal(hash, k.hash) {
		return zero, nil, ErrKeyReused
	}
	if errCode != nil {
		return zero, apperr.New(*errStatus, *errCode, *errMessage), nil
	}
	var result T
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		return zero, nil, fmt.Errorf("idempotency: unmarshal result: %w", err)
	}
	return result, nil, nil
}
