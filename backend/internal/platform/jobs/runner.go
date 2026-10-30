package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/observability"
)

// HandlerFunc bir iş türünü çalıştırır. Hata dönerse iş yeniden denenir (Permanent ve RetryAfter hariç).
type HandlerFunc func(ctx context.Context, job Job) error

// RunnerConfig worker davranışıdır.
type RunnerConfig struct {
	Concurrency  int           // aynı anda kaç iş çalışır
	PollInterval time.Duration // kuyruk boşken ne sıklıkla bakılır
	Lease        time.Duration // bir işin en uzun çalışma süresi; aşılırsa iş başka worker'a verilir
}

// Runner kuyruktan iş alıp çalıştırır.
//
// İş alma, tek bir UPDATE … WHERE id = (SELECT … FOR UPDATE SKIP LOCKED) sorgusudur: birden fazla
// worker (ya da goroutine) aynı anda sorgulasa da her iş yalnız birine düşer; diğerleri kilitli satırı
// beklemeden atlayıp sıradakine geçer. Alınan işe bir kira (Lease) verilir; worker çökerse kira dolunca
// iş yeniden alınabilir hale gelir. Bu yüzden işler "en az bir kez" çalışır; handler'lar tekrar
// çalıştırılmaya dayanıklı (idempotent) yazılır.
type Runner struct {
	db       db.DBTX
	clock    clock.Clock
	log      *slog.Logger
	cfg      RunnerConfig
	handlers map[string]HandlerFunc
}

// NewRunner yeni bir Runner oluşturur.
func NewRunner(q db.DBTX, c clock.Clock, log *slog.Logger, cfg RunnerConfig) *Runner {
	return &Runner{db: q, clock: c, log: log, cfg: cfg, handlers: map[string]HandlerFunc{}}
}

// Handle bir iş türü için handler kaydeder.
func (r *Runner) Handle(kind string, h HandlerFunc) { r.handlers[kind] = h }

// Run ctx iptal edilene kadar iş alıp çalıştırır. ctx iptal edilince yeni iş almaz; elindeki işleri
// bitirip döner (düzgün kapanış). Çalışan işler ctx iptalinden etkilenmez, yalnız kira süresiyle sınırlıdır.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range max(r.cfg.Concurrency, 1) {
		wg.Go(func() { r.loop(ctx) })
	}
	wg.Wait()
}

func (r *Runner) loop(ctx context.Context) {
	for ctx.Err() == nil {
		worked, err := r.RunOnce(ctx)
		if err != nil {
			r.log.Error("job loop error", slog.Any("error", err))
		}
		if worked && err == nil {
			continue // kuyrukta başka iş olabilir; beklemeden bak
		}
		select {
		case <-ctx.Done():
		case <-time.After(r.cfg.PollInterval):
		}
	}
}

// RunOnce sıradaki işi alıp çalıştırır. İş yoksa false döner.
func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	job, ok, err := r.claim(ctx)
	if err != nil || !ok {
		return false, err
	}

	// Kapanış sinyali gelse de iş yarıda kesilmez; süre sınırı kiradır.
	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.Lease)
	defer cancel()
	log := r.log.With(slog.String("job_id", job.ID.String()), slog.String("kind", job.Kind), slog.Int("attempt", job.Attempts))
	jobCtx = observability.WithLogger(jobCtx, log)

	start := time.Now()
	runErr := r.execute(jobCtx, job)
	if err := r.finish(jobCtx, job, runErr); err != nil {
		return true, err
	}
	if runErr != nil {
		log.Warn("job failed", slog.Any("error", runErr), slog.Int64("duration_ms", time.Since(start).Milliseconds()))
	} else {
		log.Info("job done", slog.Int64("duration_ms", time.Since(start).Milliseconds()))
	}
	return true, nil
}

func (r *Runner) execute(ctx context.Context, job Job) (err error) {
	h, ok := r.handlers[job.Kind]
	if !ok {
		return Permanent(fmt.Errorf("no handler for job kind %q", job.Kind))
	}
	defer func() { // handler'daki panic worker'ı düşürmesin; iş hata almış sayılır
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return h(ctx, job)
}

const claimColumns = `id, kind, payload, attempts, max_attempts`

// claim sıradaki işi alır: önce zamanı gelmiş bekleyen işler, sonra kirası dolmuş (yarım kalmış) işler.
func (r *Runner) claim(ctx context.Context) (Job, bool, error) {
	now := r.clock.Now()
	leaseUntil := now.Add(r.cfg.Lease)

	for _, pick := range []string{
		`SELECT id FROM jobs WHERE status = 'queued' AND run_at <= $1
		  ORDER BY run_at, id FOR UPDATE SKIP LOCKED LIMIT 1`,
		`SELECT id FROM jobs WHERE status = 'running' AND locked_until < $1
		  ORDER BY locked_until, id FOR UPDATE SKIP LOCKED LIMIT 1`,
	} {
		var j Job
		err := r.db.QueryRow(ctx,
			`UPDATE jobs SET status = 'running', attempts = attempts + 1, locked_until = $2, updated_at = $1
			  WHERE id = (`+pick+`)
			  RETURNING `+claimColumns,
			now, leaseUntil).Scan(&j.ID, &j.Kind, &j.Payload, &j.Attempts, &j.MaxAttempts)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return Job{}, false, fmt.Errorf("jobs: claim: %w", err)
		}
		return j, true, nil
	}
	return Job{}, false, nil
}

// owned, sonucun yalnız işi hâlâ bu çalıştırma tutuyorsa yazılmasını sağlar. Kira dolup iş başka bir
// worker'a geçtiyse (attempts artmıştır) geç kalan sonuç yenisinin kaydını ezmez.
const owned = ` WHERE id = $1 AND status = 'running' AND attempts = $3`

// finish işin sonucunu yazar: başarılıysa done; hata kalıcıysa ya da deneme hakkı bittiyse failed;
// değilse beklemeyle tekrar kuyruğa.
func (r *Runner) finish(ctx context.Context, job Job, runErr error) error {
	now := r.clock.Now()
	var err error
	switch {
	case runErr == nil:
		_, err = r.db.Exec(ctx,
			`UPDATE jobs SET status = 'done', locked_until = NULL, last_error = NULL, updated_at = $2`+owned,
			job.ID, now, job.Attempts)
	case isPermanent(runErr) || job.Attempts >= job.MaxAttempts:
		_, err = r.db.Exec(ctx,
			`UPDATE jobs SET status = 'failed', locked_until = NULL, last_error = $4, updated_at = $2`+owned,
			job.ID, now, job.Attempts, runErr.Error())
	default:
		_, err = r.db.Exec(ctx,
			`UPDATE jobs SET status = 'queued', locked_until = NULL, last_error = $4, run_at = $5, updated_at = $2`+owned,
			job.ID, now, job.Attempts, runErr.Error(), now.Add(retryDelay(runErr, job.Attempts)))
	}
	if err != nil {
		return fmt.Errorf("jobs: finish: %w", err)
	}
	return nil
}
