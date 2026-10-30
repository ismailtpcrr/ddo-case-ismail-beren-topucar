-- +goose Up

-- Dört göz onayı (proje.md Görev 7; PLAN.md §5.9): 10.000 TL üzeri transferi isteyen kişi onaylayamaz.
-- Kural uygulamada kontrol edilir ve ikinci güvence olarak veritabanı kısıtıdır.
CREATE TABLE approvals (
    id           uuid        PRIMARY KEY,
    transfer_id  uuid        NOT NULL REFERENCES transfers (id),
    requested_by uuid        NOT NULL REFERENCES users (id),
    status       text        NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED')),
    decided_by   uuid        REFERENCES users (id),
    reason       text        CHECK (length(reason) <= 400),
    created_at   timestamptz NOT NULL DEFAULT now(),
    decided_at   timestamptz,

    CONSTRAINT approvals_transfer_key UNIQUE (transfer_id),
    -- Onaylayan, isteyen kişiden farklı olmalı.
    CONSTRAINT approvals_four_eyes CHECK (status <> 'APPROVED' OR decided_by <> requested_by),
    CONSTRAINT approvals_decided_consistent CHECK ((status = 'PENDING') = (decided_by IS NULL AND decided_at IS NULL))
);
CREATE INDEX approvals_pending_idx ON approvals (created_at) WHERE status = 'PENDING';

-- +goose Down
DROP TABLE approvals;
