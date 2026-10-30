-- +goose Up

-- Kartlar, provizyonlar (holds), çekimler ve iadeler (proje.md Görev 3, F-4; PLAN.md §5.4).
CREATE TABLE cards (
    id          uuid        PRIMARY KEY,
    account_id  uuid        NOT NULL REFERENCES accounts (id),
    customer_id uuid        NOT NULL REFERENCES customers (id),
    pan_masked  text        NOT NULL CHECK (pan_masked ~ '^[0-9]{4} \*{4} \*{4} [0-9]{4}$'), -- kart numarası asla tam saklanmaz
    status      text        NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'BLOCKED')),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cards_customer_idx ON cards (customer_id, id);
CREATE INDEX cards_account_idx ON cards (account_id);

-- Provizyon: bakiyeyi bloke eder, defter kaydı yoktur (bloke ≠ harcama). amount = çekilen + serbest bırakılan + kalan.
CREATE TABLE holds (
    id              uuid        PRIMARY KEY,
    account_id      uuid        NOT NULL REFERENCES accounts (id),
    card_id         uuid        NOT NULL REFERENCES cards (id),
    merchant        text        NOT NULL DEFAULT '' CHECK (length(merchant) <= 100),
    currency        char(3)     NOT NULL CHECK (currency IN ('TRY', 'USD', 'EUR')),
    amount_minor    bigint      NOT NULL CHECK (amount_minor > 0),
    captured_minor  bigint      NOT NULL DEFAULT 0 CHECK (captured_minor >= 0),
    released_minor  bigint      NOT NULL DEFAULT 0 CHECK (released_minor >= 0), -- final çekimde ya da süre aşımında bırakılan
    remaining_minor bigint      NOT NULL CHECK (remaining_minor >= 0),          -- hâlâ bloke olan
    state           text        NOT NULL DEFAULT 'ACTIVE' CHECK (state IN ('ACTIVE', 'CAPTURED', 'EXPIRED')),
    expires_at      timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT holds_amounts_add_up CHECK (captured_minor + released_minor + remaining_minor = amount_minor),
    -- Yalnız ACTIVE provizyon bakiyeyi bloke eder.
    CONSTRAINT holds_inactive_releases CHECK (state = 'ACTIVE' OR remaining_minor = 0)
);
-- Süresi dolan aktif provizyonları bulma (worker) ve hesabın aktif provizyonları.
CREATE INDEX holds_expiry_idx ON holds (expires_at) WHERE state = 'ACTIVE';
CREATE INDEX holds_account_idx ON holds (account_id, id DESC);

CREATE TABLE captures (
    id               uuid        PRIMARY KEY,
    hold_id          uuid        NOT NULL REFERENCES holds (id),
    amount_minor     bigint      NOT NULL CHECK (amount_minor > 0),
    refunded_minor   bigint      NOT NULL DEFAULT 0 CHECK (refunded_minor >= 0),
    final            boolean     NOT NULL,
    journal_entry_id uuid        NOT NULL REFERENCES journal_entries (id),
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT captures_refund_within_amount CHECK (refunded_minor <= amount_minor)
);
CREATE INDEX captures_hold_idx ON captures (hold_id);

CREATE TABLE refunds (
    id               uuid        PRIMARY KEY,
    capture_id       uuid        NOT NULL REFERENCES captures (id),
    amount_minor     bigint      NOT NULL CHECK (amount_minor > 0),
    journal_entry_id uuid        NOT NULL REFERENCES journal_entries (id),
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refunds_capture_idx ON refunds (capture_id);

-- +goose Down
DROP TABLE refunds;
DROP TABLE captures;
DROP TABLE holds;
DROP TABLE cards;
