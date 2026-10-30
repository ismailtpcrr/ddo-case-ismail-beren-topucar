# ParaKöprü — İşletim Rehberi (RUNBOOK)

Bu belge iki işi anlatır: **yedekleme / geri yükleme** ve **sıfır kesintili migrasyon** (expand → migrate → contract).
Komutlar compose servis adlarını kullanır (`postgres`, `api`, `worker`); kök dizinde ve `.env` yüklüyken çalıştırılır.
Bölüm 1'deki PITR betiği bu depoda çalıştırılıp doğrulanmıştır; bölüm 2'deki migrasyon örneği **anlatım amaçlıdır, bu sistemde çalıştırılmamıştır**.

## 1. Yedekleme ve geri yükleme

İki ayrı araç var, ikisi farklı sorulara cevap verir:

| | `pg_dump` (mantıksal yedek) | WAL arşivi + temel yedek (PITR) |
|---|---|---|
| Ne alır | Dump anındaki veritabanının anlık görüntüsü | Temel yedek + sürekli arşivlenen WAL parçaları |
| Geri dönülebilen an | Yalnız dump anı | Temel yedekten sonraki **herhangi bir an** (`recovery_target_time`) |
| Ne için | Sürüm/şema taşıma, tek tablo ya da seçici geri yükleme | "Dün 14:03'te hatalı işlemden hemen önceye dön" |
| Bu depoda | `pg_dump -Fc` (betikte alınır) | `archive_mode=on`, `/wal_archive` volume'u, `scripts/dr/pitr-demo.sh` |

### 1.1 Mantıksal yedek (`pg_dump`)

```bash
# Yedek al (özel biçim, sıkıştırılmış)
docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -Fc "$POSTGRES_DB"' > parakopru.dump

# Ayrı bir veritabanına geri yükle (canlı veriye dokunmaz)
docker compose exec -T postgres sh -c 'createdb -U "$POSTGRES_USER" geri_yuklenen'
docker compose exec -T postgres sh -c 'pg_restore -U "$POSTGRES_USER" -d geri_yuklenen --no-owner --no-privileges' < parakopru.dump
```

Canlı veritabanının üzerine geri yüklemek önce `api` ve `worker`'ı durdurmayı gerektirir (`docker compose stop api worker`); aksi halde yazan
süreçler geri yüklemeyle çakışır. Not: `pg_dump` WAL'ı içermez, dump'tan sonraki işlemler kaybolur.

### 1.2 Belirli bir ana dönüş (PITR)

PostgreSQL her tamamlanan WAL parçasını `/wal_archive` volume'una kopyalar (`archive_command`; trafik azken de en geç 15 sn'de bir parça: `archive_timeout`).
Bir temel yedek + o yedekten sonraki WAL'lar, hedef zamana kadar yeniden oynatılarak o ana dönüş sağlar.

Gösterim (yaklaşık 11 sn, canlı sisteme yalnız geçici bir `dr_demo` tablosu ekler ve siler):

```bash
make pitr-demo        # = ./scripts/dr/pitr-demo.sh ; çıkış kodu 0 = PASS
```

Betiğin adımları:

1. `pg_basebackup -X none` (temel yedek) ve `pg_dump -Fc` (mantıksal yedek) alınır.
2. İşaret kaydı **A** yazılır, zaman **T** not edilir, sonra "hatalı" kayıt **B** yazılır.
3. WAL parçasının arşivlendiği beklenir (`pg_switch_wal`).
4. **Ayrı bir konteynerde** temel yedek açılır; `recovery.signal`, `restore_command = 'cp /wal_archive/%f %p'` ve
   `recovery_target_time = T`, `recovery_target_action = 'promote'` ile ileri sarılır.
5. Doğrulama: A var, B yok; müşteri sayısı yerinde; her para biriminde defter dengeli.
6. Karşılaştırma: `pg_dump` yedeği T'ye dönemez (dump anındaki hali verir).

Gerçek bir felakette aynı yöntem canlı veri dizininin yerine uygulanır: servisleri durdur → bozuk veri dizinini (volume) yedekle/ayır → temel
yedeği açıp `recovery_target_time`'ı hatalı işlemden hemen öncesine ayarla → PostgreSQL'i başlat → `pg_is_in_recovery() = false` olunca
uygulamayı aç → `GET /api/v1/admin/invariants` ile `ok: true` doğrula. Bu son canlı-dizin adımları betikte otomatik değildir; betik geri yüklemeyi ayrı
konteynerde yapar ve yöntemi kanıtlar.

Arşivin durumu: `docker compose exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB -c "SELECT * FROM pg_stat_archiver"`.

### 1.3 Bilinen sınırlamalar

- Arşiv **sınırsız büyür**; saklama/temizlik politikası yok. Gerçek ortamda temel yedekler zamanlanır, en eski temel yedekten önceki WAL'lar silinir.
- Tek düğüm ve tek volume: arşiv ve veri aynı Docker sunucusundadır. Gerçek ortamda arşiv ayrı bir depolamaya (nesne depolama) gider.
- `make reset-demo` volume'ları siler; arşiv de gider.
- Yedekten geri yükleme süresi (RTO) ve kayıp penceresi (RPO) ölçülmedi. RPO'nun üst sınırı `archive_timeout` (15 sn) + son tamamlanmamış WAL parçasıdır, ama bu bir hesap, ölçüm değildir.

## 2. Sıfır kesintili migrasyon: expand → migrate → contract

> **Anlatım örneği.** Aşağıdaki adımlar bu depoda uygulanıp ölçülmedi; yöntemi ve neden güvenli olduğunu gösterir. Şema değişiklikleri
> `backend/migrations/` altında goose dosyalarıdır; `api` açılışında advisory lock ile otomatik uygulanır (PLAN.md §2; uygulama rolü şemayı değiştiremez, DECISIONS.md K-6).

**Sorun:** bir sütunu doğrudan yeniden adlandırmak (`ALTER TABLE … RENAME COLUMN`) eski sürümde çalışan `api`/`worker` kopyalarını anında kırar:
dağıtım sırasında iki sürüm aynı anda çalışır. Çözüm değişikliği üç uyumlu aşamaya bölmektir; her aşamada hem eski hem yeni kod çalışır.

Örnek: `transfers.description` sütununu `memo` olarak yeniden adlandırmak.

### Aşama 1 — Expand (genişlet): yeni sütunu ekle, eskisi dursun

Migrasyon `00016_transfers_memo_expand.sql`:

```sql
-- +goose Up
ALTER TABLE transfers ADD COLUMN memo text;                          -- boş geçebilir: eski kod bilmiyor, kırılmaz
-- Yeni satırlar için iki sütunu senkron tutan trigger (eski kod description yazar, yeni kod memo yazar)
CREATE FUNCTION transfers_sync_memo() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.memo IS NULL THEN NEW.memo := NEW.description; END IF;
  IF NEW.description IS NULL OR NEW.description = '' THEN NEW.description := coalesce(NEW.memo, ''); END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER transfers_sync_memo BEFORE INSERT OR UPDATE ON transfers FOR EACH ROW EXECUTE FUNCTION transfers_sync_memo();
```

Bu aşamada ekleme kilidi kısadır (nullable sütun, varsayılansız). Eski sürüm çalışmaya devam eder.

### Aşama 2 — Migrate (geçir): veriyi taşı, kodu yeni sütuna çevir

1. Mevcut satırlar **partiler halinde** doldurulur (uzun kilit ve WAL patlaması olmasın):
   ```sql
   UPDATE transfers SET memo = description WHERE id IN (SELECT id FROM transfers WHERE memo IS NULL LIMIT 5000);
   -- memo IS NULL satırı kalmayana kadar tekrarlanır
   ```
   (`transfers` eklemeli-yalnız değildir; eklemeli-yalnız tablolarda — defter, denetim kaydı — bu yöntem uygulanamaz, onlar için yeni tablo + çift yazma gerekir.)
2. **Yeni kod sürümü** `memo`'yu okur ve yazar; `description`'a yalnız trigger yoluyla dokunur. Dağıtım **kayan güncelleme** ile yapılır: yeni kopya açılıp sağlıklı
   olunca eskisi kapatılır (bu depodaki compose tek `api` kopyası ve sabit port kullanır; gerçek ortamda yük dengeleyici arkasında birden çok kopya gerekir, burada gösterilmedi).
   İki sürüm de şemayla uyumlu olduğu için geçiş sırasında hata olmaz.
3. Doğrula: `SELECT count(*) FROM transfers WHERE memo IS DISTINCT FROM description` sıfır; eski sürüm kopyası kalmadı.

### Aşama 3 — Contract (daralt): eskiyi kaldır

Yeni kod bir sürüm boyunca sorunsuz çalıştıktan ve geri dönüş (rollback) ihtimali kalmadıktan sonra, ayrı bir migrasyonla:

```sql
-- +goose Up
DROP TRIGGER transfers_sync_memo ON transfers;
DROP FUNCTION transfers_sync_memo();
ALTER TABLE transfers DROP COLUMN description;
ALTER TABLE transfers ALTER COLUMN memo SET DEFAULT '';
ALTER TABLE transfers ALTER COLUMN memo SET NOT NULL;                -- artık güvenli: tüm satırlar dolu
```

### Kural özeti

| Aşama | Şema | Kod | Geri alınabilir mi |
|---|---|---|---|
| Expand | yeni + eski var | eski kod çalışır | evet (yeni sütunu sil) |
| Migrate | yeni + eski var, veri taşındı | önce yeni kod yayılır | evet (eski koda dön) |
| Contract | yalnız yeni | yalnız yeni kod | **hayır** (eski sütun silindi): yalnız yedekten |

Her aşama ayrı dağıtımdır. Bir aşamayı bir sonrakiyle aynı dağıtıma sıkıştırmak sıfır kesinti garantisini kaldırır.
