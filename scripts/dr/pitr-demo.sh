#!/usr/bin/env bash
# Felaket kurtarma gösterimi (proje.md Görev 7): belirli bir ana geri dönüş (PITR).
#
# Akış:
#   1. temel yedek (pg_basebackup) + mantıksal yedek (pg_dump) alınır
#   2. işaret kaydı A yazılır, zaman T not edilir, ardından "hatalı" kayıt B yazılır
#   3. WAL parçası arşive atılana kadar beklenir
#   4. AYRI bir konteynerde temel yedek açılır, WAL arşivinden recovery_target_time = T'ye kadar ileri sarılır
#   5. doğrulama: A var, B yok; uygulama verisi (müşteri sayısı, defter dengesi) yerinde
#   6. karşılaştırma: pg_dump yedeği T'ye geri dönemez (anlık görüntüdür, dump anındaki hali verir)
#
# Çalışan sisteme (docker compose up) karşı çalışır; üretim verisine yalnız geçici bir dr_demo tablosu ekler ve siler.
# Sonuç .verify/pitr-result.txt dosyasına yazılır (make verify T-7 için okur). Çıkış kodu: 0 = PASS.
set -euo pipefail
cd "$(dirname "$0")/../.."
set -a; . ./.env; set +a

RESULT="${PITR_RESULT_FILE:-.verify/pitr-result.txt}"
mkdir -p "$(dirname "$RESULT")"
RUN="pitr$(date +%s)"
ARCHIVE_DIR="/wal_archive/pitr/$RUN"
VOLUME="${RUN}_data"
IMAGE="parakopru-postgres:local"
RESTORE="parakopru-pitr"
ARCHIVE_VOLUME="parakopru_walarchive"

primary() { docker compose exec -T postgres "$@"; }
sql()     { primary psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -At -c "$1"; }
restored() { docker exec "$RESTORE" psql -U "$POSTGRES_USER" -d "${2:-$POSTGRES_DB}" -v ON_ERROR_STOP=1 -At -c "$1"; }

cleanup() {
  docker rm -f "$RESTORE" >/dev/null 2>&1 || true
  docker volume rm -f "$VOLUME" >/dev/null 2>&1 || true
  sql "DROP TABLE IF EXISTS dr_demo" >/dev/null 2>&1 || true
  primary rm -rf "$ARCHIVE_DIR" >/dev/null 2>&1 || true
}
fail() { echo "FAIL $*" > "$RESULT"; echo "✗ PITR başarısız: $*"; cleanup; exit 1; }
trap 'fail "beklenmeyen hata (satır $LINENO)"' ERR

step() { echo "→ $*"; }

step "1/6 Temel yedek (pg_basebackup) ve mantıksal yedek (pg_dump)"
primary mkdir -p "$ARCHIVE_DIR"
primary pg_basebackup -U "$POSTGRES_USER" -D "$ARCHIVE_DIR/base" -Fp -X none -c fast
primary pg_dump -U "$POSTGRES_USER" -Fc -f "$ARCHIVE_DIR/logical.dump" "$POSTGRES_DB"
sql "SELECT pg_switch_wal()" >/dev/null

step "2/6 İşaret kaydı A yazılıyor, zaman T not ediliyor, sonra 'hatalı' kayıt B yazılıyor"
sql "CREATE TABLE dr_demo (label text PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT clock_timestamp())"
sql "INSERT INTO dr_demo (label) VALUES ('A: T öncesi (korunmalı)')"
sleep 2
T="$(sql "SELECT to_char(clock_timestamp(), 'YYYY-MM-DD HH24:MI:SS.US TZH')")"
CUSTOMERS="$(sql "SELECT count(*) FROM customers")"
sleep 2
sql "INSERT INTO dr_demo (label) VALUES ('B: T sonrası hatalı kayıt (geri alınmalı)')"
echo "   T = $T ; müşteri sayısı = $CUSTOMERS"

step "3/6 WAL parçası arşivleniyor"
SEGMENT="$(sql "SELECT pg_walfile_name(pg_current_wal_lsn())")"
sql "SELECT pg_switch_wal()" >/dev/null
for _ in $(seq 1 60); do
  primary test -f "/wal_archive/$SEGMENT" && break
  sleep 1
done
primary test -f "/wal_archive/$SEGMENT" || fail "WAL parçası $SEGMENT 60 sn içinde arşivlenmedi (archive_mode açık mı?)"
echo "   arşivlendi: $SEGMENT"

step "4/6 Ayrı konteynerde geri yükleme: temel yedek + WAL arşivi, recovery_target_time = T"
docker volume create "$VOLUME" >/dev/null
docker run --rm --user root --entrypoint sh -v "$ARCHIVE_VOLUME:/wal_archive" -v "$VOLUME:/data" "$IMAGE" -c "
  cp -a $ARCHIVE_DIR/base/. /data/ &&
  rm -f /data/postmaster.pid &&
  touch /data/recovery.signal &&
  printf \"restore_command = 'cp /wal_archive/%%f %%p'\nrecovery_target_time = '$T'\nrecovery_target_action = 'promote'\n\" >> /data/postgresql.auto.conf &&
  chown -R postgres:postgres /data && chmod 700 /data"
docker run -d --name "$RESTORE" -v "$VOLUME:/var/lib/postgresql/data" -v "$ARCHIVE_VOLUME:/wal_archive:ro" "$IMAGE" \
  postgres -c archive_mode=off >/dev/null
READY=""
for _ in $(seq 1 90); do
  if [ "$(restored 'SELECT pg_is_in_recovery()' 2>/dev/null || true)" = "f" ]; then READY=1; break; fi
  sleep 1
done
[ -n "$READY" ] || { docker logs "$RESTORE" 2>&1 | tail -15; fail "geri yükleme 90 sn içinde tamamlanmadı"; }

step "5/6 Doğrulama: T anındaki durum"
LABELS="$(restored "SELECT string_agg(left(label, 1), ',' ORDER BY label) FROM dr_demo")"
[ "$LABELS" = "A" ] || fail "geri yüklenen veritabanında yalnız A olmalıydı, bulundu: '$LABELS'"
echo "   ✓ A var, B yok (geri yüklenen kayıtlar: $LABELS)"
RESTORED_CUSTOMERS="$(restored "SELECT count(*) FROM customers")"
[ "$RESTORED_CUSTOMERS" = "$CUSTOMERS" ] || fail "müşteri sayısı $RESTORED_CUSTOMERS, beklenen $CUSTOMERS"
UNBALANCED="$(restored "SELECT count(*) FROM (SELECT currency FROM postings GROUP BY currency HAVING sum(amount_minor) <> 0) x")"
[ "$UNBALANCED" = "0" ] || fail "geri yüklenen defter dengesiz ($UNBALANCED para birimi)"
echo "   ✓ uygulama verisi yerinde: $RESTORED_CUSTOMERS müşteri; defterde her para biriminde borç = alacak"

step "6/6 Karşılaştırma: pg_dump (mantıksal) yedeği T'ye geri dönemez"
restored "CREATE DATABASE dumpdb" >/dev/null
docker exec "$RESTORE" pg_restore -U "$POSTGRES_USER" -d dumpdb --no-owner --no-privileges "$ARCHIVE_DIR/logical.dump" >/dev/null 2>&1 || true
DUMP_HAS="$(restored "SELECT to_regclass('public.dr_demo') IS NOT NULL" dumpdb)"
[ "$DUMP_HAS" = "f" ] || fail "pg_dump yedeği dr_demo içermemeliydi (dump T'den önce alındı)"
echo "   ✓ pg_dump yedeği dump anındaki hali verir (A'dan önce): dr_demo yok; yalnız WAL ile T anına dönülebilir"

echo "PASS PITR: T=$T; A var, B yok; $RESTORED_CUSTOMERS müşteri ve dengeli defter geri geldi; pg_dump yalnız dump anına döner" | tee "$RESULT"
trap - ERR
cleanup
