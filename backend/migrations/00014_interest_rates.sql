-- +goose Up

-- Faiz ve stopaj oranları (proje.md Görev 4; PLAN.md §5.6). Zaman geçerlilikli ve eklemeli-yalnız: bir oran, kendi
-- valid_from'undan aynı ürün/para birimindeki bir sonraki oranın valid_from'una kadar geçerlidir. Aralıklar valid_from'dan
-- türetildiği için çakışamaz; aynı başlangıçta iki oran UNIQUE ile engellenir. Bir gün her zaman o gün geçerli oranla
-- hesaplanır: oran değişince geçmiş günler etkilenmez.
CREATE TABLE interest_rates (
    id          uuid          PRIMARY KEY,
    product     text          NOT NULL CHECK (product IN ('CHECKING_STANDARD', 'TIME_DEPOSIT_STANDARD')),
    currency    char(3)       NOT NULL CHECK (currency IN ('TRY', 'USD', 'EUR')),
    method      text          NOT NULL CHECK (method IN ('SIMPLE', 'COMPOUND')),
    day_basis   text          NOT NULL CHECK (day_basis IN ('ACT_360', 'ACT_365', 'ACT_ACT')),
    annual_rate numeric(10,6) NOT NULL CHECK (annual_rate >= 0 AND annual_rate < 10), -- 0.45 = %45
    valid_from  date          NOT NULL,
    created_at  timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT interest_rates_valid_from_key UNIQUE (product, currency, valid_from)
);

-- Stopaj (faizden kesilen vergi) oranı; zaman geçerlilikli.
CREATE TABLE tax_rates (
    id         uuid         PRIMARY KEY,
    rate       numeric(6,4) NOT NULL CHECK (rate >= 0 AND rate < 1), -- 0.15 = %15
    valid_from date         NOT NULL UNIQUE,
    created_at timestamptz  NOT NULL DEFAULT now()
);

CREATE TRIGGER interest_rates_append_only BEFORE UPDATE OR DELETE ON interest_rates FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER tax_rates_append_only BEFORE UPDATE OR DELETE ON tax_rates FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
REVOKE UPDATE, DELETE, TRUNCATE ON interest_rates, tax_rates FROM app_rw;

-- Başlangıç oranları (2026-01-01'den geçerli). Vadesiz hesaba faiz yok; vadeli hesaba bileşik faiz.
INSERT INTO interest_rates (id, product, currency, method, day_basis, annual_rate, valid_from) VALUES
    ('01900000-0000-7000-8000-000000000001', 'CHECKING_STANDARD',     'TRY', 'SIMPLE',   'ACT_365', 0,    '2026-01-01'),
    ('01900000-0000-7000-8000-000000000002', 'CHECKING_STANDARD',     'USD', 'SIMPLE',   'ACT_360', 0,    '2026-01-01'),
    ('01900000-0000-7000-8000-000000000003', 'CHECKING_STANDARD',     'EUR', 'SIMPLE',   'ACT_360', 0,    '2026-01-01'),
    ('01900000-0000-7000-8000-000000000004', 'TIME_DEPOSIT_STANDARD', 'TRY', 'COMPOUND', 'ACT_365', 0.42, '2026-01-01'),
    ('01900000-0000-7000-8000-000000000005', 'TIME_DEPOSIT_STANDARD', 'USD', 'SIMPLE',   'ACT_360', 0.03, '2026-01-01'),
    ('01900000-0000-7000-8000-000000000006', 'TIME_DEPOSIT_STANDARD', 'EUR', 'SIMPLE',   'ACT_ACT', 0.02, '2026-01-01');
INSERT INTO tax_rates (id, rate, valid_from) VALUES ('01900000-0000-7000-8000-000000000101', 0.15, '2026-01-01');

-- +goose Down
DROP TABLE tax_rates;
DROP TABLE interest_rates;
