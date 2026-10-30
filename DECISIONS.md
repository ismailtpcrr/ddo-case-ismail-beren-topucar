# Mimari Karar Kayıtları

Her kayıt şu başlıklardan oluşur: **Bağlam · Karar · Değerlendirilen alternatifler · Sonuçlar / ödünleşimler**.
Kayıtlar alındıkları sırayla eklenir. Projenin ana mimari kararları (modüler monolit, defter tasarımı, kilit sırası vb.)
PLAN.md §14'teki listeden buraya taşınacaktır.

---

## K-01 Hata mesajlarının ve logların dili

- **Bağlam:** Arayüz Türkçe. API hataları hem kullanıcıya gösteriliyor hem de frontend ve testler tarafından
  koda göre ayırt ediliyor. Loglar ise araçlarla aranıyor.
- **Karar:** Hata yanıtındaki `message` Türkçe; `code` (ör. `INSUFFICIENT_FUNDS`) ve log mesajları İngilizce.
- **Değerlendirilen alternatifler:**
  - Her şey Türkçe: log aramaları ve kod eşleştirme Türkçe karakterlerle zorlaşır.
  - Her şey İngilizce: frontend'in her hata için ayrı bir çeviri tablosu tutması gerekir.
- **Sonuçlar:** Frontend `message`'ı doğrudan gösterebilir; program mantığı yalnız `code`'a bakar, mesaj metni değişse de kırılmaz.

## K-02 Ek HTTP durum kodları: 405, 413, 504

- **Bağlam:** proje.md §7 "anlamlı HTTP durum kodları" istiyor. PLAN.md §7'deki listede (200/201/202, 400, 401, 403, 404, 409, 422, 500, 503)
  yanlış HTTP yöntemi, aşırı büyük istek gövdesi ve istek zaman aşımı için bir kod yok.
- **Karar:** `405 METHOD_NOT_ALLOWED`, `413 REQUEST_TOO_LARGE` (JSON gövdesi 1 MiB üstü), `504 TIMEOUT` (istek süre sınırını aştı).
- **Değerlendirilen alternatifler:** Bunları 400 ve 503'e katlamak. Ancak 503 `/health`'te "sistem sağlıksız" anlamında kullanılıyor;
  zaman aşımını da 503 yapmak iki farklı durumu karıştırır ve istemcinin yeniden deneme kararını zorlaştırır.
- **Sonuçlar:** İstemci hatanın türünü durum kodundan anlar. Hepsi aynı `{"error":{"code","message"}}` biçimindedir.

## K-03 Kimlik doğrulaması istemeyen uçlar

- **Bağlam:** proje.md §7 "her uç noktada yetki kontrolü" istiyor. Öte yandan §3, Docker healthcheck'in kullanacağı bir `GET /health`
  istiyor. Docker healthcheck token taşıyamaz; giriş yapmamış bir kullanıcı da henüz token'a sahip değildir.
- **Karar:** Yalnız şu uçlar girişsiz çalışır:
  - `GET /health`: Docker healthcheck; yanıtta yalnız `ok` / `unavailable` bilgisi var
  - `GET /metrics`: Prometheus toplayıcısı
  - `GET /openapi.yaml`, `GET /docs`: API belgesi
  - `POST /api/v1/auth/login`, `POST /api/v1/auth/refresh`: giriş; refresh, `HttpOnly` çerezdeki token ile doğrulanır

  Bunların dışındaki **her** uç `Require(izin)` ile korunur; yetkisiz istek 401/403 alır.
- **Değerlendirilen alternatifler:**
  - `/health` için sabit bir token: healthcheck komutuna bir sır gömmek gerekir ve yanıt zaten iç bilgi içermez, güvenlik kazancı yok.
  - `/metrics`'i JWT ile korumak: Prometheus'a token dağıtmayı gerektirir. Gerçek bir ortamda bu uç ağ seviyesinde (yalnız iç ağ) kapatılır.
- **Sonuçlar:** `/health` hata ayrıntısını (ör. veritabanı adresi) yanıtta değil yalnız logda verir. `make verify`, korunan uçların
  token olmadan 401 döndüğünü ayrıca test eder.

## K-04 PLAN.md §11'de olmayan iki platform paketi: `page` ve `health`

- **Bağlam:** PLAN.md §11 sayfalamayı `httpx` içinde gösteriyor. Ancak sayfa tipleri ve imleçleri repo ve servis katmanı da kullanacak;
  PLAN.md §3.1'e göre servis katmanı HTTP bilmemeli.
- **Karar:**
  - `internal/platform/page`: sayfa tipleri ve imleç kodlama; HTTP bilmez.
  - `httpx.ParsePage`: yalnız URL'deki `?limit=&cursor=` parametrelerini okur.
  - `internal/platform/health`: `/health` uçu kendi küçük paketinde; kontrol listesini (veritabanı vb.) dışarıdan alır.
- **Değerlendirilen alternatifler:** Hepsini `httpx`'te tutmak. Bu durumda servis paketleri `httpx`'i import etmek zorunda kalır ve
  katman kuralı bozulur.
- **Sonuçlar:** Bağımlılık yönü `handler → service → repo` olarak kalır; `page` paketi üç katmanda da kullanılabilir.

## K-05 "Global değişken yok" kuralının iki bilinçli istisnası

- **Bağlam:** PLAN.md §3.2: bağımlılıklar constructor ile verilir; global değişken, singleton ya da `init()` içinde bağlantı kurmak yok.
- **Karar:** İki paket düzeyi değer bilinçli olarak tutuluyor:
  1. `httpx` içindeki JSON doğrulayıcı (`validator.Validate`). Kütüphane, kural bilgisini önbelleğe aldığı için tek bir örnek
     kullanılmasını öneriyor. Bağlantı açmaz, I/O yapmaz, değişen bir durum tutmaz.
  2. `main` içinde `slog.SetDefault`. Context'inde logger olmayan nadir bir yol bile log yazarsa JSON biçiminde yazsın
     (şartname: yapılandırılmış JSON log).
- **Değerlendirilen alternatifler:** Doğrulayıcıyı her handler'a constructor ile vermek. Kod artar ama test edilebilirlik artmaz,
  çünkü doğrulayıcı zaten saf bir fonksiyon gibi davranıyor.
- **Sonuçlar:** Veritabanı havuzu, saat, kimlik üreteci, dış servis istemcileri gibi durum ya da I/O taşıyan bağımlılıkların
  hepsi constructor ile verilir; kural onlar için eksiksiz geçerlidir.

## K-06 Veritabanı rolleri: yetkiler bir grup rolüne verilir

- **Bağlam:** PLAN.md §6 iki veritabanı rolü istiyor: migrasyonu çalıştıran sahip rol ve api/worker'ın kullandığı uygulama rolü
  (defter ve denetim tablolarında yalnız `INSERT/SELECT`). Uygulama kullanıcısının adı ve parolası `.env`'den gelir;
  migrasyon SQL dosyaları ise sabittir.
- **Karar:**
  - Yetkiler, giriş yapamayan `app_rw` grup rolüne migrasyonlarda verilir. Uygulama kullanıcısı (`APP_DB_USER`) bu grubun
    üyesidir; PostgreSQL ilk açılışında `deploy/postgres/initdb/01-app-role.sh` tarafından oluşturulur.
  - `ALTER DEFAULT PRIVILEGES`: sahip rolün oluşturduğu her yeni tabloda `app_rw` okuma/yazma yetkisi alır. Eklemeli-yalnız
    tablolarda `UPDATE/DELETE` o tablonun kendi migrasyonunda geri alınır.
  - Sahip rol, postgres imajının `POSTGRES_USER`'ıdır (konteyner içinde süper kullanıcı).
- **Değerlendirilen alternatifler:**
  - Yetkileri doğrudan kullanıcı adına vermek: migrasyonun kullanıcı adını ortam değişkeninden okuması gerekir
    (goose ENVSUB); SQL'e dışarıdan metin girer.
  - Tek rol: defter satırlarını değiştirme yasağı yalnız trigger'a kalır; iki ayrı güvence yerine tek güvence olur.
- **Sonuçlar / ödünleşimler:** Uygulama rolü tablo oluşturamaz ve silemez; testle kanıtlı (`42501 insufficient_privilege`).
  Sahip rolün süper kullanıcı olması demo ortamı içindir; WAL arşivi ve `pg_basebackup` (T-7) için gereken yetkileri de sağlar.
  Gerçek bir ortamda migrasyon için süper kullanıcı olmayan ayrı bir sahip rol açılır.

## K-07 Kimlik doğrulamanın ayrıntıları

- **Bağlam:** proje.md §7: parola özeti argon2id ya da bcrypt; JWT (access + refresh); her uçta yetki kontrolü; 401/403.
  PLAN.md: access token bellekte, refresh token `HttpOnly` + `SameSite=Strict` çerezde; `refresh_tokens` tablosu.
- **Karar:**
  - **Parola:** argon2id, OWASP önerisi `m=19 MiB, t=2, p=1`. Parametreler özetin içinde (PHC biçimi) saklanır.
    Özetten okunan parametreler sınırlandırılır; bozuk bir özet sunucuyu bellek tüketerek düşüremez (fuzz testiyle denendi).
  - **Access token:** HS256 JWT, 15 dk; algoritma sabit (`alg: none` reddedilir); `typ=access`.
  - **Refresh token:** o da HS256 JWT (`typ=refresh`, 7 gün), ama kimliği (`jti`) `refresh_tokens` tablosunda tutulur.
    Her yenilemede eski token iptal edilir, yenisi verilir (rotasyon). İptal edilmiş bir token tekrar gelirse çalınmış
    sayılır ve kullanıcının açık bütün oturumları kapatılır.
  - **Yetki:** her korunan uç `Authenticate` + `Require(izin)` arkasındadır; `/me` için de her rolde bulunan
    `profile:read` izni istenir. Rol → izin tablosu kodda tek yerdedir (`auth/rbac.go`) ve PLAN.md §5.10 ile birebir aynı
    olduğu testle korunur.
  - **Giriş hataları:** yanlış parola ile kayıtlı olmayan e-posta aynı yanıtı verir; kayıtlı olmayan e-postada da bir
    argon2id hesabı yapılır, yanıt süresinden e-postanın varlığı anlaşılamaz.
  - **Çerezin `Secure` bayrağı** `COOKIE_SECURE` ile açılır; yerel demo `http://localhost` üzerinde çalıştığı için varsayılan kapalıdır.
- **Değerlendirilen alternatifler:**
  - Refresh token'ı opak rastgele değer olarak saklamak: şartname "JWT (access + refresh)" dediği için o da JWT yapıldı;
    iptal edilebilirlik `jti` kaydıyla sağlandı.
  - RFC 9106'daki 64 MiB argon2id: tek girişte daha güçlü, ama yük testinde yüzlerce eşzamanlı girişte bellek tükenir.
  - Sunucu taraflı oturum: her istekte veritabanı okuması gerekir; access token'ın imzayla doğrulanması daha hızlıdır.
- **Sonuçlar / ödünleşimler:**
  - Access token iptal edilemez; en fazla 15 dk geçerli kalır. Çıkışta yalnız refresh token iptal edilir.
  - Aynı refresh token'la eşzamanlı iki yenileme isteğinde (ör. iki sekme) biri kazanır, diğeri iptal edilmiş token
    gördüğü için bütün oturumlar kapanır. Arayüz aynı anda tek yenileme isteği gönderecek şekilde yazılır (D1).

## K-08 Idempotency-Key'in uygulanışı

- **Bağlam:** proje.md §7 kritik POST'larda `Idempotency-Key` ile "aynı istek, aynı sonuç" istiyor. PLAN.md §5.2: anahtar iş
  işlemiyle aynı transaction'da yazılır; eşzamanlı ikinci istek birincinin commit'ini bekler; farklı gövde → 422; iş kuralı
  reddi de sonuçtur.
- **Karar:**
  - Servis, işlemi `idempotency.Run` içinde yapar. Anahtar satırı `INSERT … ON CONFLICT DO NOTHING` ile eklenir;
    eşzamanlı ikinci istek PostgreSQL'in benzersizlik kilidinde bekler, ayrı bir kilit mekanizması gerekmez.
  - Anahtar kullanıcıya özeldir (`PRIMARY KEY (user_id, key)`). İstek özeti = SHA-256(işlem adı + gövde).
  - İş kuralı reddinde (4xx) işlemin yarım yazmaları bir savepoint'e geri alınır, ret kaydedilir ve commit edilir.
  - Beklenmeyen hatada (5xx) hiçbir şey kaydedilmez; istemci aynı anahtarla güvenle yeniden dener.
  - Kayıtlı sonuç dönerken yanıta `Idempotent-Replayed: true` başlığı eklenir.
- **Değerlendirilen alternatifler:**
  - HTTP middleware'inde yanıtı kaydetmek: anahtar iş transaction'ının dışında kalır. İşlem commit edilip yanıt kaydedilmeden
    süreç çökerse tekrar istek işlemi ikinci kez yapar.
  - Redis'te kilit + sonuç: ikinci bir sistem ve iki sistem arasında tutarlılık sorunu.
- **Sonuçlar / ödünleşimler:** Anahtarlar süresiz saklanır (şartname bir saklama süresi istemiyor); büyük ölçekte eski
  anahtarları silen bir iş gerekir.

## K-09 İş kuyruğunda kira (lease) ve "en az bir kez" çalıştırma

- **Bağlam:** PLAN.md §5.2: outbox ile `jobs` tablosu; worker işleri `SKIP LOCKED` ile alır. Worker herhangi bir anda çökebilir.
- **Karar:** İş, tek bir `UPDATE … WHERE id = (SELECT … FOR UPDATE SKIP LOCKED LIMIT 1)` ile "running" yapılır ve bir kira
  (`locked_until`, varsayılan 2 dk) alır; handler transaction dışında çalışır. Kirası dolmuş "running" işler yeniden alınabilir.
  Hata → artan bekleme (1, 2, 4 … sn, en fazla 5 dk); `Permanent` hata ya da deneme hakkı bitince "failed".
  Sonuç yalnız iş hâlâ aynı denemeye aitse yazılır; geç kalan bir worker yenisinin sonucunu ezmez.
- **Değerlendirilen alternatifler:** İşi baştan sona tek bir transaction içinde, satır kilidini tutarak çalıştırmak. Daha basit
  ama dış servis çağrısı sürerken bir veritabanı bağlantısı ve kilit açık kalır; handler kendi transaction'larını açamaz.
- **Sonuçlar / ödünleşimler:** İşler "en az bir kez" çalışır: worker işi bitirip sonucu yazamadan çökerse iş tekrar çalışır.
  Bu yüzden handler'lar tekrar çalıştırılmaya dayanıklı yazılır (ör. dış bankaya hep aynı referansla gidilir).
