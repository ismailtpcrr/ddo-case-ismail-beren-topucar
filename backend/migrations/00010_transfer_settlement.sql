-- +goose Up

-- Dış transferin defter kayıtları: ayırma (journal_entry_id), banka onayından sonra takasın kapanışı ve başarısızlıkta ters kayıt.
ALTER TABLE transfers
    ADD COLUMN settlement_entry_id uuid REFERENCES journal_entries (id),
    ADD COLUMN reversal_entry_id   uuid REFERENCES journal_entries (id);

-- Operasyon ekranı "belirsiz (UNKNOWN) transferler" listesi için.
CREATE INDEX transfers_open_state_idx ON transfers (state, id DESC) WHERE state IN ('RESERVED', 'SENT', 'UNKNOWN');

-- Gelen dış transferler (PLAN.md §7 /webhooks/bank/incoming): banka aynı bildirimi iki kez gönderebilir; banka referansı
-- defter kaydında tekil olduğu için aynı gelen transfer iki kez alacaklandırılamaz.
CREATE UNIQUE INDEX journal_entries_incoming_reference_key ON journal_entries (reference) WHERE kind = 'INCOMING_TRANSFER';

-- +goose Down
DROP INDEX journal_entries_incoming_reference_key;
DROP INDEX transfers_open_state_idx;
ALTER TABLE transfers DROP COLUMN reversal_entry_id, DROP COLUMN settlement_entry_id;
