-- +goose Up

-- Transferler (proje.md Görev 2, F-3; PLAN.md §5.2). Durum geçişleri uygulamada tek tabloda tanımlıdır
-- (transfer.transitions); veritabanı yalnız geçerli durum adlarını ve tutarlılığı güvenceye alır.
CREATE TABLE transfers (
    id              uuid        PRIMARY KEY,
    type            text        NOT NULL CHECK (type IN ('INTERNAL', 'EFT', 'FAST')),
    state           text        NOT NULL CHECK (state IN ('CREATED', 'UNDER_REVIEW', 'PENDING_APPROVAL', 'RESERVED',
                                                          'SENT', 'UNKNOWN', 'COMPLETED', 'FAILED', 'REVERSED', 'REJECTED')),
    from_account_id uuid        NOT NULL REFERENCES accounts (id),
    to_account_id   uuid        REFERENCES accounts (id),   -- dış transferde boş
    to_iban         text        NOT NULL CHECK (to_iban ~ '^TR[0-9]{24}$'),
    amount_minor    bigint      NOT NULL CHECK (amount_minor > 0),
    currency        char(3)     NOT NULL CHECK (currency IN ('TRY', 'USD', 'EUR')),
    description     text        NOT NULL DEFAULT '' CHECK (length(description) <= 200),
    requested_by    uuid        NOT NULL REFERENCES users (id),
    journal_entry_id uuid       REFERENCES journal_entries (id),
    failure_code    text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT transfers_internal_has_target CHECK (type <> 'INTERNAL' OR to_account_id IS NOT NULL),
    CONSTRAINT transfers_not_to_self CHECK (to_account_id IS NULL OR to_account_id <> from_account_id),
    CONSTRAINT transfers_rejected_has_code CHECK (state <> 'REJECTED' OR failure_code IS NOT NULL)
);
CREATE INDEX transfers_from_idx ON transfers (from_account_id, id DESC);
CREATE INDEX transfers_to_idx ON transfers (to_account_id, id DESC) WHERE to_account_id IS NOT NULL;

-- Her durum geçişi bir olay satırıdır (geçmiş + denetim). Eklemeli-yalnız.
CREATE TABLE transfer_events (
    id          uuid        PRIMARY KEY,
    transfer_id uuid        NOT NULL REFERENCES transfers (id),
    from_state  text,
    to_state    text        NOT NULL,
    note        text,
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX transfer_events_transfer_idx ON transfer_events (transfer_id, id);

CREATE TRIGGER transfer_events_append_only
    BEFORE UPDATE OR DELETE ON transfer_events
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER transfer_events_no_truncate
    BEFORE TRUNCATE ON transfer_events
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();
REVOKE UPDATE, DELETE, TRUNCATE ON transfer_events FROM app_rw;

-- +goose Down
DROP TABLE transfer_events;
DROP TABLE transfers;
