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

## K-10 `make verify`: dışarıdan, gerçek isteklerle doğrulama

- **Bağlam:** proje.md §10: `make verify` ayaktaki ve seed edilmiş sistemde her özelliği ve zorlu görevi API (ve mümkünse
  arayüz) üzerinden denemeli; kimlik başına PASS/FAIL tablosu basmalı; hata varsa sıfırdan farklı çıkış kodu dönmeli; ≤ 15 dk.
- **Karar:**
  - Doğrulama ayrı bir Go programıdır (`cmd/verify`). Compose'da `verify` profiliyle tek seferlik çalışır ve sisteme
    compose ağı içinden, bir kullanıcının yapacağı gibi HTTP ile bağlanır. Veritabanına doğrudan bakmaz.
  - Şartnamenin ek istediği senaryolar ayrı kimliklerle tabloda yer alır: `HEALTH`, `AUTH-1…4`, `UI`.
  - Arayüz kontrolü, sayfanın açılmasının yanında nginx üzerinden API'ye ulaşılabildiğini de doğrular.
  - **Henüz uygulanmamış bir kimlik FAIL sayılır** ve hangi plan adımında geleceği yazılır.
- **Değerlendirilen alternatifler:**
  - Bash + curl betiği: tablo, JSON ayrıştırma ve eşzamanlılık (T-1'deki 10.000 transfer) için zayıf kalır.
  - Go entegrasyon testlerini (`go test`) verify olarak kullanmak: onlar sistemi içeriden test eder; şartname ise
    "ayağa kalkmış sistem üzerinde" uçtan uca deneme istiyor.
  - Uygulanmamış kimlikleri SKIP göstermek: tablo yeşil görünür ama sistemin eksik olduğunu gizler.
- **Sonuçlar / ödünleşimler:** Bütün kimlikler uygulanana kadar `make verify` sıfırdan farklı kodla biter. Bu bilinçli bir
  tercih: tablo her an sistemin gerçek durumunu gösterir.

## K-11 Para tutarları: int64 minor birim, ara hesapta decimal, banker's rounding

*(PLAN.md §14 madde 6; B1 adımında uygulandı.)*

- **Bağlam:** proje.md §6-§7: tutarlar tam sayı (kuruş) ya da `numeric` olmalı, kayan nokta yasak. T-4 banker's rounding ve
  kuruş dağıtımı istiyor; kur ve faiz hesabı kesirli sonuç üretir.
- **Karar:**
  - Saklanan ve taşınan her tutar `money.Amount{Minor int64, Currency}`; veritabanında `bigint` (`*_minor`), API'de
    `amount_minor` tam sayı + `currency`.
  - Farklı para birimleri toplanamaz; int64 taşması hata döner.
  - Faiz ve kur gibi ara hesaplar `shopspring/decimal` ile yapılır; sonuç `FromDecimal` ile banker's rounding
    (yarımda çifte) kullanılarak kuruşa çevrilir.
  - Metinden tutar okuma (ekstre satırları) katıdır: en fazla 2 ondalık basamak; üs, binlik ayırıcı ya da boşluk kabul
    edilmez. Fazla basamak sessizce yuvarlanmaz, satır bozuk sayılır.
  - Kayan nokta yasağı lint kuralıdır (`forbidigo`): para ile ilgili paketlerde (money, account, ledger, transfer, card,
    fx, interest, recon) `float32/64` ve float ↔ decimal dönüşümleri derleme öncesi yakalanır.
- **Değerlendirilen alternatifler:**
  - Veritabanında `numeric(20,2)`, Go'da decimal: her toplamada decimal nesnesi gerekir ve yavaştır. Kuruş zaten tam sayı
    olduğu için int64 yeterli ve hızlıdır.
  - Yarımı yukarı yuvarlama (half-up): çok sayıda işlemde sapma hep aynı yönde birikir; banker's rounding bunu dengeler.
  - Float yasağını bütün projeye uygulamak: risk modülündeki mesafe/hız hesabı (imkânsız seyahat) kayan nokta gerektirir.
- **Sonuçlar / ödünleşimler:** int64 kuruş ±92 katrilyon TL'ye kadar yeter. Desteklenen üç para birimi de 2 basamaklıdır;
  farklı basamaklı bir para birimi (ör. JPY) eklenirse basamak sayısı para birimine bağlanmalıdır (tek bir sabit değişir).

## K-12 Hesap modeli: doğal taraf, iç hesap planı, IBAN ve nesne düzeyinde yetki

- **Bağlam:** PLAN.md §5.1: müşteri hesapları (vadesiz/vadeli, TRY/USD/EUR), her para birimi için 8 iç hesap,
  `account_balances` özeti ve `CHECK (allow_negative OR balance_minor - held_minor >= 0)`. §5.10: müşteri yalnız kendi hesabını görür.
- **Karar:**
  - Her hesabın `normal_side`'ı vardır. Müşteri hesapları banka için borçtur (CREDIT), NOSTRO ve faiz gideri DEBIT'tir.
    `balance_minor` bu tarafa göre doğal işaretli tutulur; müşteri hesabında pozitif bakiye müşterinin parasıdır.
  - İç hesaplar eksiye düşebilir (`allow_negative`). Doğrulukları bakiye kısıtıyla değil, defter değişmezleriyle korunur
    (B3/B4: her kayıtta borç = alacak, özet = türetilmiş bakiye).
  - Müşteri hesaplarına kontrol haneli (mod 97) TR IBAN üretilir: kurgusal banka kodu + veritabanı dizisinden 16 haneli
    hesap no. Dış transferde alıcı IBAN'ı doğrulanabilir.
  - Müşteri hesabı türüne göre bir faiz ürünü alır (`CHECKING_STANDARD`, `TIME_DEPOSIT_STANDARD`); faiz oranları (H1)
    ürün + para birimine bağlanır.
  - Başka bir müşterinin hesabına erişim **403** döner (proje.md §7: "yetkisiz erişim 401/403 ile reddedilmeli").
  - `GET /accounts` tek uçtur: müşteri kendi hesaplarını, `ledger:read` sahibi roller hepsini görür (`RequireAny` + serviste kapsam).
- **Değerlendirilen alternatifler:**
  - Başkasının hesabına 404 döndürmek (hesabın varlığını gizlemek): şartname 401/403 istediği için 403 seçildi. Hesap
    kimlikleri tahmin edilemez UUID'ler olduğundan varlığı sızdırmanın pratik riski düşüktür.
  - Bakiye işaretini her yerde "+ borç" olarak tutmak: müşteri ekranında ve kısıtta işaret çevirmek gerekir; hata kaynağı olur.
- **Sonuçlar / ödünleşimler:** Demo müşteri ve hesapları api açılışında oluşur; bakiyeleri açılış kaydıyla (defterden) B6'da gelir.

## K-13 Defterin ayrıntıları: işaretli satır, üç katlı borç = alacak, iki zaman, ters kayıt kuralları

*(PLAN.md §14 madde 7, 8 ve 12'nin uygulanışı; B3 adımı.)*

- **Bağlam:** proje.md Görev 1: borç = alacak, kayan nokta yok, türetilebilir bakiye + tutarlı özet, kilit sıralamasıyla
  deadlock'suz. Görev 7: satırlar silinmez/güncellenmez, düzeltme ters kayıtla; işlem zamanı ile geçerlilik zamanı ayrı.
  PLAN.md §5.1 yapıyı veriyor, ama şunları açık bırakıyor: §7'deki ters kayıt ucunun yetkisi (§5.10 matrisinde satırı yok),
  hangi kayıtların elle ters çevrilebileceği, `effective_at`/`recorded_at` kolonlarının hangi adımda geleceği.
- **Karar:**
  - **İşaretli satır:** `postings.amount_minor` + borç, − alacak. API'de yön (`side`) + pozitif tutar; hesap hareketlerinde
    ayrıca bakiyeye etkisi (`balance_effect_minor`, müşteri hesabında giriş +, çıkış −).
  - **Borç = alacak üç yerde:** (1) Go'da `Entry.Validate`: en az iki satır, pozitif tutar, her para biriminde Σ = 0;
    (2) veritabanında `DEFERRABLE INITIALLY DEFERRED` constraint trigger: commit anında dengesiz, satırsız ya da tek satırlı
    kayıt reddedilir; (3) satırın para birimi hesabınkiyle aynı (`(account_id, currency)` yabancı anahtarı).
  - **Özet:** `Post`, hesap başına toplam değişimi hesabın `normal_side`'ına göre doğal işarete çevirip aynı transaction'da
    yazar. Eksiye düşemeyen hesapta kullanılabilir bakiye Go'da kontrol edilir (`422 INSUFFICIENT_FUNDS`); `CHECK` son güvencedir.
  - **Kilit:** kayda giren hesapların özet satırları tek sorguda `ORDER BY account_id FOR UPDATE` ile alınır. Test, yeniden
    deneme yapmayan transaction'larla ters yönlü eşzamanlı kayıtlarda hiç deadlock olmadığını gösterir.
  - **İki zaman bu adımda:** `effective_at` (verilmezse veritabanı saati) ve `recorded_at` (trigger her eklemede `now()`
    yazar; ne istemci ne uygulama belirleyebilir). `effective_at ≤ recorded_at`: geriye tarihli kayıt olur, ileri tarihli
    olmaz. Zamanların satırlarda kopyası var (bakiye/hareket sorguları tek dizinden okur); kopyaların kayıttakilerle aynı
    olmasını yabancı anahtar sağlar, bu da commit edilmiş bir kayda sonradan satır eklenmesini engeller.
    `as_of`/`known_at` sorguları F1'de.
  - **Değişmezlik:** `forbid_mutation()` trigger'ı (UPDATE/DELETE/TRUNCATE, süper kullanıcıya da) + uygulama rolünden
    `UPDATE, DELETE, TRUNCATE` yetkisinin geri alınması.
  - **Ters kayıt:** `Reverse` genel bir servis işlevidir (transfer sagasının telafisi gibi modüller kendi transaction'larında
    kullanır). Bir kayıt bir kez ters çevrilir (`UNIQUE (reversal_of)`; eşzamanlı denemede biri kazanır), ters kayıt yeniden
    ters çevrilemez, geçerlilik zamanı özgün kayıttan önce olamaz.
  - **Elle ters kayıt ucu** (`POST /ledger/entries/{id}/reverse`): yalnız bir sürece ait olmayan kayıt türleri
    (şimdilik `OPENING_BALANCE`). Yeni izin `ledger:reverse`, yalnız operasyon uzmanında. `Idempotency-Key` zorunlu;
    denetim kaydına `ledger.reverse` (eski: özgün kayıt, yeni: ters kayıt) yazılır.
  - **Ek okuma ucu** `GET /ledger/entries/{id}` (`ledger:read`): kaydın iki tarafını gösterir (F-2, "her kuruş açıklanabilir").
  - **Defter kayıtları ayrıca `audit_log`'a yazılmaz:** defter zaten değiştirilemez bir kayıttır. Denetim kaydına kaydı
    doğuran iş işlemi (transfer, ters kayıt …) yazılır.
  - **Demo müşterinin açılış bakiyeleri bu adımda** defterden yazılır (borç `OPENING_EQUITY` / alacak müşteri): defter
    artık var ve B4'teki transfer bakiyesiz denenemez. Simülatör müşterilerininki B6'da.
- **Değerlendirilen alternatifler:**
  - Ayrı borç/alacak kolonları: denge kontrolü ve bakiye toplamı tek işaretli kolonla daha basit.
  - Özeti veritabanı trigger'ıyla güncellemek: kilit sırası ve yetersiz bakiye kararı uygulamanın elinden çıkar; hata
    anlamlı bir `422` yerine kısıt ihlali olarak gelir.
  - `SERIALIZABLE` + yeniden deneme: sıcak hesaplarda (ör. NOSTRO) sürekli çakışma ve yeniden deneme; sıralı kilit bekletir.
  - Her kaydı elle ters çevirmeye izin vermek: transfer COMPLETED görünürken para geri dönebilir; süreç ile defter ayrışır.
  - `recorded_at`'i uygulamanın saatiyle yazmak: "o gün ne biliyorduk" sorusunun cevabı uygulamaya güvenmek zorunda kalır.
  - İki zaman kolonunu F1'e bırakmak: değişmez tabloya sonradan dolu kolon eklemek bir UPDATE gerektirir; trigger buna izin vermez.
- **Sonuçlar / ödünleşimler:**
  - Borç = alacak trigger'ı satır başına çalışır (iki satırlı kayıtta commit'te üç küçük, dizinli sorgu).
  - Bankanın iç hesapları (NOSTRO, takas hesapları) aynı para birimindeki her kayıtta kilitlenir; bu hesaplara dokunan
    işlemler o satırda sıraya girer. Şartnamedeki hacim (günde ~200.000 işlem ≈ 2,3/sn) için yeterli; çok daha yüksek
    hacimde iç hesap özeti parçalara bölünmelidir (bilinen sınırlama).
  - `ledger:reverse` PLAN.md §5.10 matrisinde yok; `TestPermissionMatrixMatchesPlan` bu satırla birlikte doğruluyor.
  - `journal_entries`'te zaman kopyalarının yabancı anahtarı için ek bir benzersiz dizin var.

## K-14 İç transfer: ret de bir sonuçtur, durum makinesi baştan tam, değişmezler REPEATABLE READ'de

*(PLAN.md §14 madde 9 ve 11'in ilk kısmı; B4 adımı.)*

- **Bağlam:** proje.md Görev 2: aynı `Idempotency-Key` aynı sonucu döndürmeli, durum makinesi geçersiz geçişi reddetmeli.
  Görev 1: "tüm hesapların toplamı değişmemeli" kanıtı API'den de alınabilmeli (`/admin/invariants`).
  PLAN.md §5.2: yetersiz bakiye `REJECTED` transfer kaydı olur ve tekrar istek aynı reddi döndürür; §7 ise iş kuralı
  reddini 422 sayıyor.
- **Karar:**
  - **Ret = kayıtlı sonuç:** yetersiz bakiyede `REJECTED` transfer ve olay satırı yazılır; servis bunu *başarılı sonuç* olarak
    döndürür (idempotency anahtarıyla saklanır), handler 422 `INSUFFICIENT_FUNDS` yazar ve transfer kimliğini
    `X-Transfer-ID` başlığında verir. Böylece hem ret kaydı kalıcıdır hem istemci hata biçimini alır. (Idempotency
    katmanının kendi 4xx saklaması, kaydı savepoint'e geri aldığı için bu amaca yetmez.)
  - **Durum makinesi baştan tam:** `transitions` tablosu PLAN.md §5.2'nin tamamını içerir (dış banka durumları dahil);
    iç transfer yalnız `CREATED → COMPLETED/REJECTED` kullanır. Her geçiş koşullu `UPDATE … WHERE state = $beklenen` +
    olay satırı; tabloda olmayan geçiş `409 INVALID_STATE_TRANSITION` olur ve loglanır. `transfer_events` eklemeli-yalnızdır.
  - **Alıcı IBAN ile verilir.** Bilinmeyen IBAN dış banka transferidir; C2'ye kadar `422 EXTERNAL_TRANSFER_UNAVAILABLE`.
    Farklı para birimi G1'e kadar `422 CURRENCY_MISMATCH`. (Sessizce yanlış işlem yapmaktansa açıkça reddetmek.)
  - **Yetki:** başlatma `transfers:create` (müşteri yalnız kendi hesabından; operasyon uzmanı herhangi bir müşteri
    hesabından). Okuma: gönderen ya da alıcı müşteri, `ledger:read` sahibi roller.
  - **`/admin/invariants`:** `ledger:read` izniyle (yeni izin yok). Ölçümler tek bir `REPEATABLE READ` salt okunur
    transaction'da yapılır; eşzamanlı yazma sürerken bile sayılar aynı anın görüntüsüdür. Provizyon kontrolü
    (`held_minor` = Σ aktif blok) kart tablosu gelince (E1) eklenir; o zamana kadar alan `null`.
- **Değerlendirilen alternatifler:**
  - Reddi yalnız 422 olarak saklamak (transfer kaydı yazmadan): "reddedilen işlemin izi" kaybolur, operasyon ekranında görünmez.
  - Reddi 201 ile `state=REJECTED` döndürmek: istemci her başarılı görünen yanıtta durumu ayrıca kontrol etmek zorunda kalır.
  - Durum makinesini adım adım büyütmek: her adımda tabloyu değiştirmek geçmişi ve testi dağıtır.
- **Sonuçlar / ödünleşimler:** Değişmezler ucu bütün `postings` tablosunu tarar (20.000 müşteri ölçeğinde saniyenin altı,
  günlük 200.000 işlemde tablo büyüdükçe artar); bu bir yönetim/doğrulama ucudur, sıcak yol değildir.

## K-15 Transaction içinde havuzdan ikinci bağlantı alınmaz

*(B5 adımında 10.000 eşzamanlı transfer testi bir hatayı ortaya çıkardı.)*

- **Bağlam:** Transfer servisi, transaction açıkken gönderen hesabı `accounts.Get` ile okuyordu; o da havuzdan *ayrı* bir
  bağlantı alıyordu. 64 eşzamanlı istek (havuz: 50 bağlantı) hepsi bir bağlantıyı transaction'da tutarken ikinci
  bağlantıyı bekleyince hiçbiri ilerlemedi: veritabanında değil, uygulama havuzunda kilitlenme (test 10 dk'da bitmedi).
  Birim testler ve 20 istekli testler bunu göstermemişti.
- **Karar:** Transaction içinde çalışan her okuma o transaction'ın bağlantısını kullanır (`account.Service.GetTx(q, …)`).
  Havuzdan okuyan `Get` yalnız transaction dışındaki çağrılar içindir.
- **Değerlendirilen alternatifler:** Havuzu büyütmek (sorunu ertelemek, eşzamanlılık havuzdan büyükse yine kilitlenir);
  transaction'dan önce hesabı okumak (okuma ile yazma arasında hesap değişebilir ve iki kontrol ayrışır).
- **Sonuçlar:** Kural: transaction başlatan servis, çağırdığı her şeye `tx`'i geçirir. Yük testi (L3) bu sınıf hataya karşı da koruma sağlar.
