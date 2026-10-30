package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/clock"
	"parakopru/internal/platform/db"
	"parakopru/internal/platform/db/dbtest"
	"parakopru/internal/platform/ids"
	"parakopru/internal/platform/jobs"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type setup struct {
	env    *dbtest.Env
	clock  *clock.Manual
	queue  *jobs.Queue
	runner *jobs.Runner
	kind   string // her test kendi iş türünü kullanır; testler birbirinin işini almaz
}

func newSetup(t *testing.T) setup {
	t.Helper()
	env := dbtest.Get(t)
	// Saat, testteki diğer işlerden etkilenmemek için uzak gelecekte değil "şimdi"de başlar;
	// iş türü ayrı olduğu için başka testlerin işleri handler'sız kalıp "failed" olur, sorun değil.
	clk := clock.NewManual(time.Now().UTC().Truncate(time.Microsecond)) // PostgreSQL mikrosaniye saklar
	r := jobs.NewRunner(env.App, clk, slog.New(slog.NewJSONHandler(io.Discard, nil)), jobs.RunnerConfig{
		Concurrency: 1, PollInterval: 10 * time.Millisecond, Lease: time.Minute,
	})
	return setup{env: env, clock: clk, queue: jobs.NewQueue(ids.V7{}, clk), runner: r, kind: "test." + uuid.NewString()[:8]}
}

type jobRow struct {
	Status    string
	Attempts  int
	RunAt     time.Time
	LastError *string
}

func (s setup) row(t *testing.T, id uuid.UUID) jobRow {
	t.Helper()
	var r jobRow
	require.NoError(t, s.env.App.QueryRow(t.Context(),
		`SELECT status, attempts, run_at, last_error FROM jobs WHERE id = $1`, id).
		Scan(&r.Status, &r.Attempts, &r.RunAt, &r.LastError))
	return r
}

// sameInstant iki zamanın aynı an olduğunu kontrol eder (veritabanından yerel saat diliminde döner).
func sameInstant(t *testing.T, want, got time.Time, msg string) {
	t.Helper()
	require.True(t, want.Equal(got), "%s: beklenen %s, gelen %s", msg, want.UTC(), got.UTC())
}

// runAll kuyruktaki bu testin işlerini bitene kadar çalıştırır.
func (s setup) runAll(t *testing.T) {
	t.Helper()
	for range 100 {
		worked, err := s.runner.RunOnce(t.Context())
		require.NoError(t, err)
		if !worked {
			return
		}
	}
}

// Outbox: iş, iş kaydıyla aynı transaction'dadır. Transaction geri alınırsa iş de yoktur.
func TestEnqueueFollowsTransaction(t *testing.T) {
	s := newSetup(t)
	tm := db.NewTxManager(s.env.App)

	var rolledBack, committed uuid.UUID
	_ = tm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
		rolledBack, _ = s.queue.Enqueue(ctx, tx, s.kind, nil)
		return errors.New("iş kaydı başarısız")
	})
	require.NoError(t, tm.WithTx(t.Context(), func(ctx context.Context, tx db.DBTX) error {
		var err error
		committed, err = s.queue.Enqueue(ctx, tx, s.kind, map[string]string{"transfer_id": "t-1"})
		return err
	}))

	var n int
	require.NoError(t, s.env.App.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE id = $1`, rolledBack).Scan(&n))
	require.Zero(t, n, "geri alınan transaction'ın işi olmamalı")
	require.Equal(t, "queued", s.row(t, committed).Status)
}

func TestRunnerRunsHandlerWithPayload(t *testing.T) {
	s := newSetup(t)
	var got map[string]string
	s.runner.Handle(s.kind, func(_ context.Context, j jobs.Job) error {
		return json.Unmarshal(j.Payload, &got)
	})
	id, err := s.queue.Enqueue(t.Context(), s.env.App, s.kind, map[string]string{"transfer_id": "t-42"})
	require.NoError(t, err)

	s.runAll(t)
	require.Equal(t, map[string]string{"transfer_id": "t-42"}, got)
	require.Equal(t, "done", s.row(t, id).Status)
}

func TestFailedJobIsRetriedWithBackoffThenFails(t *testing.T) {
	s := newSetup(t)
	var calls int
	s.runner.Handle(s.kind, func(context.Context, jobs.Job) error {
		calls++
		return errors.New("dış servis yanıt vermedi")
	})
	id, err := s.queue.Enqueue(t.Context(), s.env.App, s.kind, nil)
	require.NoError(t, err)
	_, err = s.env.App.Exec(t.Context(), `UPDATE jobs SET max_attempts = 3 WHERE id = $1`, id)
	require.NoError(t, err)

	s.runAll(t)
	r := s.row(t, id)
	require.Equal(t, "queued", r.Status, "ilk hatadan sonra tekrar kuyruğa")
	require.Equal(t, 1, r.Attempts)
	sameInstant(t, s.clock.Now().Add(time.Second), r.RunAt, "1. denemeden sonra 1 sn bekleme")
	require.Equal(t, "dış servis yanıt vermedi", *r.LastError)

	s.runAll(t) // zamanı gelmedi: çalışmamalı
	require.Equal(t, 1, calls)

	s.clock.Advance(time.Second)
	s.runAll(t)
	sameInstant(t, s.clock.Now().Add(2*time.Second), s.row(t, id).RunAt, "2. denemeden sonra 2 sn")

	s.clock.Advance(2 * time.Second)
	s.runAll(t)
	r = s.row(t, id)
	require.Equal(t, "failed", r.Status, "deneme hakkı bitince failed")
	require.Equal(t, 3, calls)
}

func TestPermanentErrorAndUnknownKindFailImmediately(t *testing.T) {
	s := newSetup(t)
	s.runner.Handle(s.kind, func(context.Context, jobs.Job) error {
		return jobs.Permanent(errors.New("bozuk payload"))
	})
	permanent, err := s.queue.Enqueue(t.Context(), s.env.App, s.kind, nil)
	require.NoError(t, err)
	unknown, err := s.queue.Enqueue(t.Context(), s.env.App, s.kind+".bilinmeyen", nil)
	require.NoError(t, err)

	s.runAll(t)
	require.Equal(t, "failed", s.row(t, permanent).Status)
	r := s.row(t, unknown)
	require.Equal(t, "failed", r.Status)
	require.Contains(t, *r.LastError, "no handler")
}

func TestRetryAfterAndPanicRecovery(t *testing.T) {
	s := newSetup(t)
	s.runner.Handle(s.kind, func(context.Context, jobs.Job) error {
		return jobs.RetryAfter(30*time.Second, errors.New("sonuç henüz belli değil"))
	})
	s.runner.Handle(s.kind+".panik", func(context.Context, jobs.Job) error { panic("beklenmedik") })
	retry, err := s.queue.Enqueue(t.Context(), s.env.App, s.kind, nil)
	require.NoError(t, err)
	panicky, err := s.queue.Enqueue(t.Context(), s.env.App, s.kind+".panik", nil)
	require.NoError(t, err)

	s.runAll(t)
	sameInstant(t, s.clock.Now().Add(30*time.Second), s.row(t, retry).RunAt, "RetryAfter süresi")
	r := s.row(t, panicky)
	require.Equal(t, "queued", r.Status, "panic worker'ı düşürmemeli; iş tekrar denenmeli")
	require.Contains(t, *r.LastError, "panic")
}

// Çöken worker: iş "running" kalır. Kirası dolunca başka bir worker işi yeniden alır.
func TestCrashedWorkersJobIsReclaimedAfterLease(t *testing.T) {
	s := newSetup(t)
	id, err := s.queue.Enqueue(t.Context(), s.env.App, s.kind, nil)
	require.NoError(t, err)
	// Çökmeyi taklit et: iş alınmış, kira 1 dk, ama sonucu hiç yazılmamış.
	_, err = s.env.App.Exec(t.Context(),
		`UPDATE jobs SET status = 'running', attempts = 1, locked_until = $2 WHERE id = $1`, id, s.clock.Now().Add(time.Minute))
	require.NoError(t, err)

	var runs int
	s.runner.Handle(s.kind, func(context.Context, jobs.Job) error { runs++; return nil })
	s.runAll(t)
	require.Zero(t, runs, "kira sürerken başka worker almamalı")

	s.clock.Advance(time.Minute + time.Second)
	s.runAll(t)
	require.Equal(t, 1, runs)
	r := s.row(t, id)
	require.Equal(t, "done", r.Status)
	require.Equal(t, 2, r.Attempts)
}

// 200 iş, 8 eşzamanlı worker: SKIP LOCKED sayesinde her iş tam bir kez çalışır.
func TestConcurrentWorkersRunEachJobExactlyOnce(t *testing.T) {
	s := newSetup(t)
	const n = 200
	var mu sync.Mutex
	seen := map[uuid.UUID]int{}
	var total atomic.Int32

	r := jobs.NewRunner(s.env.App, s.clock, slog.New(slog.NewJSONHandler(io.Discard, nil)), jobs.RunnerConfig{
		Concurrency: 8, PollInterval: 5 * time.Millisecond, Lease: time.Minute,
	})
	r.Handle(s.kind, func(_ context.Context, j jobs.Job) error {
		mu.Lock()
		seen[j.ID]++
		mu.Unlock()
		total.Add(1)
		return nil
	})
	for range n {
		_, err := s.queue.Enqueue(t.Context(), s.env.App, s.kind, nil)
		require.NoError(t, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return total.Load() >= n }, 20*time.Second, 10*time.Millisecond)
	cancel()
	<-done // Run, ctx iptalinden sonra elindeki işleri bitirip döner

	require.Len(t, seen, n)
	for id, c := range seen {
		require.Equal(t, 1, c, "iş %s birden fazla çalıştı", id)
	}
	var queued int
	require.NoError(t, s.env.App.QueryRow(t.Context(),
		`SELECT count(*) FROM jobs WHERE kind = $1 AND status <> 'done'`, s.kind).Scan(&queued))
	require.Zero(t, queued)
}
