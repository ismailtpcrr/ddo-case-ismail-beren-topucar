-- +goose Up

-- Idempotency-Key kayıtları (proje.md §7, PLAN.md §5.2).
--
-- Satır, iş işlemiyle aynı transaction'da eklenir ve sonucu commit'ten önce yazılır. Aynı anahtarla gelen
-- eşzamanlı ikinci istek, birincisinin satırı üzerindeki benzersizlik kilidinde bekler; birincisi commit
-- edince kayıtlı sonucu okur. Dışarıdan bakıldığında satır ya hiç yoktur ya da sonucu yazılmıştır.
CREATE TABLE idempotency_keys (
    user_id       uuid        NOT NULL REFERENCES users (id),
    key           text        NOT NULL,
    operation     text        NOT NULL,  -- ör. transfer.create; aynı anahtar başka bir işlemde kullanılamaz
    request_hash  bytea       NOT NULL,  -- işlem adı + istek gövdesinin SHA-256 özeti
    result        jsonb,                 -- başarılı sonuç
    error_status  int,                   -- ya da iş kuralı reddi
    error_code    text,
    error_message text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    completed_at  timestamptz,

    PRIMARY KEY (user_id, key),
    CONSTRAINT idempotency_keys_key_length CHECK (length(key) BETWEEN 1 AND 255),
    -- Tamamlanmış bir kayıtta sonuç ile hata alanlarından tam olarak biri doludur.
    CONSTRAINT idempotency_keys_one_outcome CHECK (
        completed_at IS NULL
        OR (result IS NOT NULL AND error_code IS NULL)
        OR (result IS NULL AND error_code IS NOT NULL AND error_status IS NOT NULL)
    )
);

-- +goose Down
DROP TABLE idempotency_keys;
