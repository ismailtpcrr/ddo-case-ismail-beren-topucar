-- +goose Up

-- Temel yetki düzeni.
--
-- Uygulama (api, worker), "app_rw" grup rolünün üyesi olan bir kullanıcıyla bağlanır
-- (bkz. deploy/postgres/initdb/01-app-role.sh). Tabloları sahip rol (bu migrasyonu çalıştıran) oluşturur;
-- uygulama yalnız kendisine verilen yetkilerle veri okur ve yazar. Tablo oluşturamaz, değiştiremez, silemez.

-- Grup rolü başlangıç betiğiyle zaten oluşmuş olur; yoksa (ör. boş bir veritabanında) burada oluşturulur.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_rw') THEN
        CREATE ROLE app_rw NOLOGIN;
    END IF;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO app_rw;

-- Sahip rolün bundan sonra oluşturacağı her tabloda uygulama varsayılan olarak okuyup yazabilir.
-- Eklemeli-yalnız tablolarda (defter, denetim kaydı) UPDATE ve DELETE, o tabloların kendi migrasyonunda geri alınır.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_rw;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO app_rw;

-- +goose Down
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE USAGE, SELECT ON SEQUENCES FROM app_rw;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM app_rw;
REVOKE USAGE ON SCHEMA public FROM app_rw;
