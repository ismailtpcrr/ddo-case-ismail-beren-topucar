-- +goose Up

-- Eklemeli-yalnız tablolar için ortak koruma: UPDATE, DELETE ve TRUNCATE'i reddeder.
-- Denetim kaydında ve defter tablolarında (journal_entries, postings) kullanılır.
-- Trigger süper kullanıcıya da uygulanır; uygulama rolünden yetkinin geri alınmasıyla birlikte iki ayrı güvencedir.
-- +goose StatementBegin
CREATE FUNCTION forbid_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% tablosu eklemeli-yalnızdır; % yapılamaz', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation',
              HINT = 'Düzeltme yeni bir kayıtla yapılır.';
END;
$$;
-- +goose StatementEnd

-- Denetim kaydı (proje.md §7): kim, ne zaman, neyi, eski ve yeni değer.
CREATE TABLE audit_log (
    id         uuid        PRIMARY KEY,           -- UUIDv7: zamana göre sıralı, sayfalama anahtarı
    at         timestamptz NOT NULL DEFAULT now(), -- kayıt zamanı veritabanından alınır
    actor_id   uuid,                               -- işlemi yapan kullanıcı; sistem süreçlerinde boş
    actor_role text        NOT NULL,
    action     text        NOT NULL,               -- ör. transfer.approve
    entity     text        NOT NULL,               -- ör. transfer
    entity_id  text,
    old_value  jsonb,
    new_value  jsonb,
    request_id text
);

-- Belirli bir kaydın geçmişini görmek için (ör. bir transferin bütün işlemleri).
CREATE INDEX audit_log_entity_idx ON audit_log (entity, entity_id, id);

CREATE TRIGGER audit_log_append_only
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER audit_log_no_truncate
    BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();

-- Uygulama rolü yalnız ekleyip okuyabilir.
REVOKE UPDATE, DELETE, TRUNCATE ON audit_log FROM app_rw;

-- +goose Down
DROP TABLE audit_log;
DROP FUNCTION forbid_mutation();
