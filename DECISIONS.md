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
