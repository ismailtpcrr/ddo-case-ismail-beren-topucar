-- +goose Up

-- İş kuyruğu (outbox). Dış dünyaya etki eden işler (ör. dış bankaya transfer gönderimi) iş kaydıyla aynı
-- transaction'da buraya eklenir; worker sonra alıp çalıştırır. Böylece "veritabanına yazıldı ama mesaj
-- gönderilmedi" ya da tersi bir durum oluşmaz.
CREATE TABLE jobs (
    id           uuid        PRIMARY KEY,
    kind         text        NOT NULL,                -- ör. transfer.send
    payload      jsonb       NOT NULL DEFAULT '{}',
    status       text        NOT NULL DEFAULT 'queued',
    run_at       timestamptz NOT NULL,                -- en erken çalışma zamanı (yeniden denemede ileri alınır)
    attempts     int         NOT NULL DEFAULT 0,
    max_attempts int         NOT NULL DEFAULT 10,
    locked_until timestamptz,                         -- çalışan işin kirası; süresi geçerse iş başka worker'a verilir
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT jobs_status_check   CHECK (status IN ('queued', 'running', 'done', 'failed')),
    CONSTRAINT jobs_attempts_check CHECK (attempts >= 0 AND max_attempts > 0),
    CONSTRAINT jobs_running_locked CHECK (status <> 'running' OR locked_until IS NOT NULL)
);

-- Sıradaki işi bulma sorgusu yalnız bekleyen işlere bakar; tamamlanan işler büyüse de dizin küçük kalır.
CREATE INDEX jobs_queued_run_at_idx ON jobs (run_at) WHERE status = 'queued';
-- Çöken bir worker'ın yarım bıraktığı işleri (kirası dolmuş) bulmak için.
CREATE INDEX jobs_running_locked_until_idx ON jobs (locked_until) WHERE status = 'running';

-- +goose Down
DROP TABLE jobs;
