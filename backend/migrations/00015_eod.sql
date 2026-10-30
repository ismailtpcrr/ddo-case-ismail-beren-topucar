-- +goose Up

-- Günlük faiz tahakkukları (proje.md Görev 4; PLAN.md §5.6). UNIQUE (account_id, accrual_date): bir hesap bir gün için
-- yalnız bir kez tahakkuk eder; gün sonu işi çöküp yeniden çalışsa bile çift tahakkuk olamaz. Satır ve defter kaydı aynı
-- transaction'da yazılır. cumulative_exact / cumulative_posted kuruş dağıtımının durumudur (bir sonraki günün girdisi).
CREATE TABLE interest_accruals (
    id                uuid          PRIMARY KEY,
    account_id        uuid          NOT NULL REFERENCES accounts (id),
    accrual_date      date          NOT NULL,
    principal_minor   bigint        NOT NULL,
    method            text          NOT NULL,
    day_basis         text          NOT NULL,
    annual_rate       numeric(10,6) NOT NULL,
    tax_rate          numeric(6,4)  NOT NULL,
    gross_minor       bigint        NOT NULL CHECK (gross_minor >= 0),
    tax_minor         bigint        NOT NULL CHECK (tax_minor >= 0),
    net_minor         bigint        NOT NULL CHECK (net_minor >= 0),
    cumulative_exact  numeric(38,16) NOT NULL,
    cumulative_posted bigint        NOT NULL,
    journal_entry_id  uuid          REFERENCES journal_entries (id),
    created_at        timestamptz   NOT NULL DEFAULT now(),

    CONSTRAINT interest_accruals_once UNIQUE (account_id, accrual_date),
    CONSTRAINT interest_accruals_gross_split CHECK (gross_minor = net_minor + tax_minor)
);
CREATE INDEX interest_accruals_date_idx ON interest_accruals (accrual_date);

-- Gün sonu çalıştırmaları: hangi adımda ve hangi hesaba kadar gelindiği. Her parti kendi transaction'ında last_account_id'yi
-- ilerletir; iş yarıda kalırsa bir sonraki çalıştırma buradan devam eder.
CREATE TABLE eod_runs (
    business_date     date        PRIMARY KEY,
    step              text        NOT NULL DEFAULT 'INTEREST' CHECK (step IN ('INTEREST', 'HOLD_EXPIRY', 'DONE')),
    status            text        NOT NULL DEFAULT 'RUNNING' CHECK (status IN ('RUNNING', 'DONE')),
    last_account_id   uuid,
    batches           int         NOT NULL DEFAULT 0,
    accounts_accrued  int         NOT NULL DEFAULT 0,
    attempts          int         NOT NULL DEFAULT 0,
    requested_by      uuid        REFERENCES users (id),
    started_at        timestamptz NOT NULL DEFAULT now(),
    finished_at       timestamptz,
    CONSTRAINT eod_runs_done CHECK ((status = 'DONE') = (step = 'DONE' AND finished_at IS NOT NULL))
);

-- +goose Down
DROP TABLE eod_runs;
DROP TABLE interest_accruals;
