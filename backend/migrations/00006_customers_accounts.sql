-- +goose Up

-- Müşteriler. Müşteri rolündeki kullanıcı (users) bir müşteri kaydına bağlıdır; simülatörün ürettiği müşterilerin
-- giriş hesabı olmayabilir (user_id boş).
CREATE TABLE customers (
    id          uuid        PRIMARY KEY,
    user_id     uuid        UNIQUE REFERENCES users (id),
    full_name   text        NOT NULL CHECK (length(full_name) BETWEEN 2 AND 200),
    national_id text        NOT NULL CHECK (national_id ~ '^[0-9]{11}$'), -- kurgusal T.C. kimlik no
    email       text,
    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT customers_national_id_key UNIQUE (national_id)
);

-- Hesap numaraları (IBAN'ın son 16 hanesi) bu diziden gelir.
CREATE SEQUENCE account_number_seq;

-- Hesaplar: müşteri hesapları (vadesiz, vadeli) ve bankanın iç hesapları (her para birimi için bir tane).
--
-- normal_side bakiyenin doğal işaretini belirler: defter satırlarında + borç, − alacaktır. Müşteri hesabı banka
-- için borçtur (CREDIT): müşteriye para yatınca hesap alacaklanır ve bakiye artar. NOSTRO (muhabir bankadaki
-- paramız) varlıktır (DEBIT): borçlanınca artar.
CREATE TABLE accounts (
    id               uuid        PRIMARY KEY,
    customer_id      uuid        REFERENCES customers (id),
    kind             text        NOT NULL,
    currency         char(3)     NOT NULL CHECK (currency IN ('TRY', 'USD', 'EUR')),
    normal_side      text        NOT NULL CHECK (normal_side IN ('DEBIT', 'CREDIT')),
    allow_negative   boolean     NOT NULL DEFAULT false,
    name             text        NOT NULL,
    iban             text        UNIQUE CHECK (iban ~ '^TR[0-9]{24}$'),
    interest_product text,
    maturity_date    date,
    status           text        NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'CLOSED')),
    opened_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT accounts_kind_check CHECK (kind IN (
        'CHECKING', 'TIME_DEPOSIT',                                           -- müşteri
        'NOSTRO', 'EXTERNAL_CLEARING', 'CARD_SETTLEMENT', 'INTEREST_EXPENSE', -- banka iç hesapları
        'WITHHOLDING_TAX', 'FX_POSITION', 'SUSPENSE', 'OPENING_EQUITY')),
    -- Müşteri hesabının sahibi, IBAN'ı ve faiz ürünü vardır, eksiye düşemez; iç hesabın sahibi ve IBAN'ı yoktur.
    CONSTRAINT accounts_customer_fields CHECK (
        (kind IN ('CHECKING', 'TIME_DEPOSIT')
            AND customer_id IS NOT NULL AND iban IS NOT NULL AND interest_product IS NOT NULL AND NOT allow_negative)
        OR
        (kind NOT IN ('CHECKING', 'TIME_DEPOSIT') AND customer_id IS NULL AND iban IS NULL)),
    -- Vadeli hesabın vade tarihi vardır; vadesizin yoktur.
    CONSTRAINT accounts_maturity CHECK ((kind = 'TIME_DEPOSIT') = (maturity_date IS NOT NULL))
);

-- Her iç hesap türünden her para biriminde tam bir tane.
CREATE UNIQUE INDEX accounts_internal_kind_currency_key ON accounts (kind, currency) WHERE customer_id IS NULL;
-- Bir müşterinin hesapları.
CREATE INDEX accounts_customer_idx ON accounts (customer_id, id) WHERE customer_id IS NOT NULL;

-- Bakiye özeti: defterden (Σ postings) türetilebilir; hızlı sorgu için defter kaydıyla aynı transaction'da güncellenir.
-- balance_minor doğal işaretlidir (normal_side'a göre). held_minor aktif kart provizyonlarının toplamıdır.
-- Kullanılabilir bakiye = balance_minor − held_minor; müşteri hesabında eksiye düşemez (veritabanı kısıtı).
CREATE TABLE account_balances (
    account_id     uuid        PRIMARY KEY REFERENCES accounts (id),
    balance_minor  bigint      NOT NULL DEFAULT 0,
    held_minor     bigint      NOT NULL DEFAULT 0 CHECK (held_minor >= 0),
    allow_negative boolean     NOT NULL, -- accounts'tan kopya; CHECK başka tabloya bakamaz
    updated_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT account_balances_available_non_negative CHECK (allow_negative OR balance_minor - held_minor >= 0)
);

-- +goose Down
DROP TABLE account_balances;
DROP TABLE accounts;
DROP SEQUENCE account_number_seq;
DROP TABLE customers;
