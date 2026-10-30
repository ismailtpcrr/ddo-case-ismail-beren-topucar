-- +goose Up

-- Çift taraflı defter (proje.md Görev 1 ve Görev 7; PLAN.md §5.1, §5.9).
--
-- Her iş olayı bir yevmiye kaydıdır (journal_entries); kaydın satırları (postings) hesaplara borç ya da alacak yazar.
-- Satır tutarı işaretlidir: + borç, − alacak. Güvenceler:
--   - Borç = alacak: her kayıtta, her para birimi için satırların toplamı 0'dır (commit anında kontrol edilir).
--   - Satırın para birimi hesabın para birimiyle aynıdır (yabancı anahtar).
--   - Kayıtlar değiştirilemez ve silinemez (trigger + uygulama rolünden yetkinin geri alınması). Düzeltme ters kayıtla
--     yapılır; bir kayıt yalnız bir kez ters çevrilebilir.
--   - İki zaman: effective_at (geçerlilik/valör zamanı) ve recorded_at (kayıt zamanı; her zaman veritabanı saati).

-- Satırın para birimi hesabınkiyle aynı olmalı: postings (account_id, currency) çiftiyle bu anahtara bağlanır.
ALTER TABLE accounts ADD CONSTRAINT accounts_id_currency_key UNIQUE (id, currency);

CREATE TABLE journal_entries (
    id           uuid        PRIMARY KEY,
    kind         text        NOT NULL CHECK (kind ~ '^[A-Z][A-Z_]{1,49}$'), -- ör. OPENING_BALANCE, REVERSAL
    description  text        NOT NULL CHECK (length(description) BETWEEN 1 AND 500),
    reference    text        CHECK (length(reference) BETWEEN 1 AND 200),  -- iş kaydının kimliği (ör. transfer ID)
    effective_at timestamptz NOT NULL,                                     -- geçerlilik (valör) zamanı
    recorded_at  timestamptz NOT NULL DEFAULT now(),                       -- kayıt zamanı; trigger her zaman now() yazar
    reversal_of  uuid        REFERENCES journal_entries (id),              -- ters kayıtsa özgün kayıt

    -- Bir kayıt yalnız bir kez ters çevrilebilir (eşzamanlı iki ters çevirmede biri bu kısıta takılır).
    CONSTRAINT journal_entries_reversal_once UNIQUE (reversal_of),
    CONSTRAINT journal_entries_reversal_kind CHECK ((kind = 'REVERSAL') = (reversal_of IS NOT NULL)),
    CONSTRAINT journal_entries_reversal_not_self CHECK (reversal_of <> id),
    -- İleri tarihli kayıt yok: geçerlilik zamanı, kaydın yazıldığı andan sonra olamaz. Geriye tarihli düzeltme olabilir.
    CONSTRAINT journal_entries_effective_not_future CHECK (effective_at <= recorded_at),
    -- Satırlardaki zaman kopyalarının kayıttakilerle aynı olmasını yabancı anahtar sağlar (postings_entry_fkey).
    CONSTRAINT journal_entries_times_key UNIQUE (id, effective_at, recorded_at)
);

CREATE TABLE postings (
    id           uuid        PRIMARY KEY,
    entry_id     uuid        NOT NULL,
    account_id   uuid        NOT NULL,
    currency     char(3)     NOT NULL,
    amount_minor bigint      NOT NULL CHECK (amount_minor <> 0), -- + borç, − alacak
    -- Kaydın zamanlarının kopyası: bakiye ve hareket sorguları birleştirme yapmadan tek dizinden okunur.
    effective_at timestamptz NOT NULL,
    recorded_at  timestamptz NOT NULL DEFAULT now(),

    -- Zamanlar kayıttakilerle aynı olmak zorunda. recorded_at her satırda now() olduğu için, commit edilmiş eski bir
    -- kayda sonradan satır eklemek de bu anahtara takılır.
    CONSTRAINT postings_entry_fkey FOREIGN KEY (entry_id, effective_at, recorded_at)
        REFERENCES journal_entries (id, effective_at, recorded_at),
    CONSTRAINT postings_account_currency_fkey FOREIGN KEY (account_id, currency)
        REFERENCES accounts (id, currency)
);

-- Bir kaydın satırları (kayıt ayrıntısı, ters kayıt, borç = alacak kontrolü).
CREATE INDEX postings_entry_idx ON postings (entry_id);
-- Hesap hareketleri (en yeni önce, keyset), geçmiş tarihli bakiye ve ekstre. INCLUDE ile toplam, tabloya gitmeden
-- yalnız dizinden hesaplanabilir (index-only scan).
CREATE INDEX postings_account_effective_idx ON postings (account_id, effective_at, id) INCLUDE (amount_minor, recorded_at);

-- recorded_at'i istemci ya da uygulama belirleyemez: her eklemede veritabanının saati yazılır (transaction başlangıcı;
-- aynı transaction'daki kayıt ve satırları aynı değeri alır).
-- +goose StatementBegin
CREATE FUNCTION set_recorded_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.recorded_at := now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER journal_entries_recorded_at
    BEFORE INSERT ON journal_entries
    FOR EACH ROW EXECUTE FUNCTION set_recorded_at();
CREATE TRIGGER postings_recorded_at
    BEFORE INSERT ON postings
    FOR EACH ROW EXECUTE FUNCTION set_recorded_at();

-- Borç = alacak, veritabanı güvencesi. Uygulama kaydı yazmadan önce aynı kontrolü yapar; bu trigger uygulamadaki
-- bir hataya ya da doğrudan SQL'e karşı ikinci güvencedir. DEFERRABLE INITIALLY DEFERRED: kayıt ve satırları
-- ayrı INSERT'lerle yazıldığı için kontrol commit anında, hepsi yazıldıktan sonra yapılır.
-- +goose StatementBegin
CREATE FUNCTION assert_entry_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_entry    uuid;
    v_lines    int;
    v_currency char(3);
    v_sum      numeric;
BEGIN
    IF TG_TABLE_NAME = 'journal_entries' THEN
        v_entry := NEW.id;
    ELSE
        v_entry := NEW.entry_id;
    END IF;

    SELECT count(*) INTO v_lines FROM postings WHERE entry_id = v_entry;
    IF v_lines < 2 THEN
        RAISE EXCEPTION 'yevmiye kaydı % en az iki satır içermeli (% satır var)', v_entry, v_lines
            USING ERRCODE = 'check_violation', CONSTRAINT = 'journal_entries_min_two_postings';
    END IF;

    SELECT currency, sum(amount_minor) INTO v_currency, v_sum
      FROM postings
     WHERE entry_id = v_entry
     GROUP BY currency
    HAVING sum(amount_minor) <> 0
     LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'yevmiye kaydı % dengesiz: % için borç − alacak = %', v_entry, v_currency, v_sum
            USING ERRCODE = 'check_violation', CONSTRAINT = 'journal_entries_balanced';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER journal_entries_balanced
    AFTER INSERT ON journal_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION assert_entry_balanced();
CREATE CONSTRAINT TRIGGER postings_balanced
    AFTER INSERT ON postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION assert_entry_balanced();

-- Değişmezlik: UPDATE, DELETE ve TRUNCATE reddedilir (forbid_mutation, migrasyon 00003). Trigger süper kullanıcıya da
-- uygulanır; uygulama rolünde ayrıca yetki de yoktur.
CREATE TRIGGER journal_entries_append_only
    BEFORE UPDATE OR DELETE ON journal_entries
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER journal_entries_no_truncate
    BEFORE TRUNCATE ON journal_entries
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER postings_append_only
    BEFORE UPDATE OR DELETE ON postings
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER postings_no_truncate
    BEFORE TRUNCATE ON postings
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();

REVOKE UPDATE, DELETE, TRUNCATE ON journal_entries, postings FROM app_rw;

-- +goose Down
DROP TABLE postings;
DROP TABLE journal_entries;
DROP FUNCTION assert_entry_balanced();
DROP FUNCTION set_recorded_at();
ALTER TABLE accounts DROP CONSTRAINT accounts_id_currency_key;
