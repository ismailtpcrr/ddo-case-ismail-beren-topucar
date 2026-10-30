-- +goose Up

-- Kurlar (proje.md F-5; PLAN.md §5.5). Zaman geçerlilikli ve eklemeli-yalnız: yeni kur eskisini değiştirmez, geçerlilik
-- zamanına göre "o anda hangi kur geçerliydi" sorulabilir. Kur, yabancı para biriminin TL karşılığıdır (1 USD = rate TRY);
-- iki yabancı para birimi arasındaki çapraz kur TL üzerinden hesaplanır.
CREATE TABLE fx_rates (
    id         uuid          PRIMARY KEY,
    base       char(3)       NOT NULL CHECK (base IN ('USD', 'EUR')),
    quote      char(3)       NOT NULL DEFAULT 'TRY' CHECK (quote = 'TRY'),
    rate       numeric(18,8) NOT NULL CHECK (rate > 0),
    valid_from timestamptz   NOT NULL,
    created_by uuid          REFERENCES users (id),
    created_at timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT fx_rates_valid_from_key UNIQUE (base, quote, valid_from)
);

CREATE TRIGGER fx_rates_append_only
    BEFORE UPDATE OR DELETE ON fx_rates
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER fx_rates_no_truncate
    BEFORE TRUNCATE ON fx_rates
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();
REVOKE UPDATE, DELETE, TRUNCATE ON fx_rates FROM app_rw;

-- Çok para birimli transferde alıcının aldığı tutar ve kayda yazılan kur (geçmişteki transferler kur değişince etkilenmez).
ALTER TABLE transfers
    ADD COLUMN to_amount_minor bigint,
    ADD COLUMN to_currency     char(3) CHECK (to_currency IN ('TRY', 'USD', 'EUR')),
    ADD COLUMN fx_rate         numeric(28,12) CHECK (fx_rate > 0),
    ADD CONSTRAINT transfers_fx_columns CHECK ((to_amount_minor IS NULL) = (fx_rate IS NULL) AND (to_currency IS NULL) = (fx_rate IS NULL));

-- +goose Down
ALTER TABLE transfers DROP CONSTRAINT transfers_fx_columns, DROP COLUMN fx_rate, DROP COLUMN to_currency, DROP COLUMN to_amount_minor;
DROP TABLE fx_rates;
