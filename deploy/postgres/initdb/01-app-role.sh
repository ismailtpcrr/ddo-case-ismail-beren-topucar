#!/bin/sh
# Uygulama veritabanı rolünü oluşturur.
#
# postgres imajı /docker-entrypoint-initdb.d altındaki betikleri yalnız veri klasörü boşken
# (ilk açılışta) çalıştırır. İki rol vardır:
#   - POSTGRES_USER (sahip): tabloları oluşturur ve migrasyonları çalıştırır.
#   - APP_DB_USER (uygulama): api ve worker bununla bağlanır.
#
# Uygulama kullanıcısının yetkileri doğrudan kendisine değil, migrasyonlarda "app_rw" grup rolüne
# verilir; kullanıcı bu grubun üyesidir. Böylece kullanıcı adı .env'den değiştirilebilir,
# migrasyon dosyaları değişmez.
set -eu

: "${APP_DB_USER:?APP_DB_USER tanımlı değil}"
: "${APP_DB_PASSWORD:?APP_DB_PASSWORD tanımlı değil}"

psql --set=ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  --set=app_user="$APP_DB_USER" --set=app_password="$APP_DB_PASSWORD" <<'SQL'
CREATE ROLE app_rw NOLOGIN;
CREATE ROLE :"app_user" LOGIN PASSWORD :'app_password' IN ROLE app_rw;
SQL
