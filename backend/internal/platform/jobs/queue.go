// Package jobs, PostgreSQL tabanlı iş kuyruğudur (outbox).
//
// Dış dünyaya etki eden bir iş (ör. dış bankaya transfer gönderimi) iş kaydıyla aynı transaction içinde
// Enqueue ile kuyruğa eklenir. Transaction geri alınırsa iş de hiç oluşmamış olur; commit edilirse iş
// kesin olarak oradadır ve worker onu alıp çalıştırır. Ayrı bir mesaj kuyruğu (Redis, Kafka) gerekmez.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/ids"
)

// Job kuyruktaki bir iştir.
type Job struct {
	ID          uuid.UUID
	Kind        string
	Payload     json.RawMessage
	Attempts    int // bu çalıştırma dahil kaç kez denendi
	MaxAttempts int
}

// Queue işleri kuyruğa ekler.
type Queue struct {
	ids   ids.Generator
	clock clock.Clock
}

// NewQueue yeni bir Queue oluşturur.
func NewQueue(idGen ids.Generator, c clock.Clock) *Queue { return &Queue{ids: idGen, clock: c} }

// Enqueue işi hemen çalışacak şekilde, verilen transaction içinde kuyruğa ekler.
func (q *Queue) Enqueue(ctx context.Context, tx db.DBTX, kind string, payload any) (uuid.UUID, error) {
	return q.EnqueueAt(ctx, tx, kind, payload, q.clock.Now())
}

// EnqueueAt işi en erken runAt zamanında çalışacak şekilde kuyruğa ekler.
func (q *Queue) EnqueueAt(ctx context.Context, tx db.DBTX, kind string, payload any, runAt time.Time) (uuid.UUID, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, fmt.Errorf("jobs: marshal payload: %w", err)
	}
	id := q.ids.NewID()
	_, err = tx.Exec(ctx,
		`INSERT INTO jobs (id, kind, payload, run_at) VALUES ($1, $2, $3, $4)`, id, kind, body, runAt)
	if err != nil {
		return uuid.Nil, fmt.Errorf("jobs: enqueue: %w", err)
	}
	return id, nil
}

// permanentError yeniden denenmeyecek hatadır.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent, yeniden denemenin işe yaramayacağı hatayı işaretler (ör. bozuk payload); iş hemen "failed" olur.
func Permanent(err error) error { return permanentError{err: err} }

// retryAfterError belirli bir süre sonra yeniden denenecek hatadır.
type retryAfterError struct {
	after time.Duration
	err   error
}

func (e retryAfterError) Error() string { return e.err.Error() }
func (e retryAfterError) Unwrap() error { return e.err }

// RetryAfter işin en erken d kadar sonra yeniden denenmesini ister (ör. dış servisin sonucu henüz belli değil).
func RetryAfter(d time.Duration, err error) error { return retryAfterError{after: d, err: err} }

func isPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

func retryDelay(err error, attempts int) time.Duration {
	var r retryAfterError
	if errors.As(err, &r) {
		return r.after
	}
	return backoff(attempts)
}

// backoff, n. başarısız denemeden sonraki beklemedir: 1, 2, 4, 8 … saniye, en fazla 5 dakika.
func backoff(attempts int) time.Duration {
	const maxDelay = 5 * time.Minute
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 9 { // 2^9 sn > 5 dk
		return maxDelay
	}
	return min(time.Second<<(attempts-1), maxDelay)
}
