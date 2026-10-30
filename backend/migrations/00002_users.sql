-- +goose Up

-- Kullanıcılar: her kullanıcının tek bir rolü vardır (proje.md §4).
CREATE TABLE users (
    id            uuid        PRIMARY KEY,
    email         text        NOT NULL,
    password_hash text        NOT NULL,            -- argon2id, PHC biçimi; parola asla düz saklanmaz
    role          text        NOT NULL,
    full_name     text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT users_email_key       UNIQUE (email),
    CONSTRAINT users_email_lowercase CHECK (email = lower(email)),
    CONSTRAINT users_role_check      CHECK (role IN ('CUSTOMER', 'OPERATIONS', 'RISK_ANALYST',
                                                     'COMPLIANCE', 'AUDITOR', 'SYSTEM'))
);

-- Refresh token kayıtları. Token'ın kendisi (JWT) saklanmaz, yalnız kimliği (jti) saklanır.
-- Her yenilemede eski token iptal edilir ve yerine geçen yazılır (replaced_by). İptal edilmiş bir token
-- tekrar gelirse çalınmış sayılır ve kullanıcının açık tüm oturumları kapatılır.
CREATE TABLE refresh_tokens (
    id          uuid        PRIMARY KEY,           -- JWT "jti"
    user_id     uuid        NOT NULL REFERENCES users (id),
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz,
    replaced_by uuid        REFERENCES refresh_tokens (id),

    CONSTRAINT refresh_tokens_replaced_requires_revoked CHECK (replaced_by IS NULL OR revoked_at IS NOT NULL)
);

-- Bir kullanıcının açık oturumlarını kapatırken kullanılır.
CREATE INDEX refresh_tokens_user_active_idx ON refresh_tokens (user_id) WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE refresh_tokens;
DROP TABLE users;
