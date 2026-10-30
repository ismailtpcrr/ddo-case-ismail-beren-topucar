# ParaKöprü — Test ve Doğrulama Rehberi

Bu dosya proje.md §10'un istediği bölümleri içerir:

- (a) her rol için demo hesap
- (b) her doğrulama kimliği için "nasıl denenir / beklenen sonuç" tablosu
- (c) arayüzde elle denenecek senaryolar
- her zorlu görev için kanıt komutu

Bölümler, ilgili özellik geliştirildikçe doldurulur.

## Demo hesaplar

Her rol için bir hesap, api ilk açıldığında otomatik oluşturulur. Hepsinin parolası `.env`'deki
`DEMO_PASSWORD`'dır (varsayılan **`ParaKopru.2026`**).

| Rol | E-posta |
|---|---|
| Müşteri | `musteri@parakopru.test` |
| Operasyon uzmanı | `operasyon@parakopru.test` |
| Risk analisti | `risk@parakopru.test` |
| Uyum (compliance) uzmanı | `uyum@parakopru.test` |
| Denetçi | `denetci@parakopru.test` |
| Sistem (zamanlanmış işler) | `sistem@parakopru.test` |

## Kimlik doğrulama ve yetki

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| Giriş | `curl -i -H 'Content-Type: application/json' -d '{"email":"operasyon@parakopru.test","password":"ParaKopru.2026"}' http://localhost:8080/api/v1/auth/login` | `200`; gövdede `access_token` ve `user.permissions`; `Set-Cookie: pk_refresh=…; HttpOnly; SameSite=Strict` |
| Yanlış parola | Aynı istek, yanlış parolayla | `401 INVALID_CREDENTIALS` "E-posta ya da parola hatalı." |
| Kayıtlı olmayan e-posta | Aynı istek, `yok@parakopru.test` ile | Yanlış parolayla **birebir aynı** yanıt (hangi bilginin yanlış olduğu belli olmaz) |
| Token olmadan korunan uç | `curl -i http://localhost:8080/api/v1/me` | `401 UNAUTHORIZED` |
| Token ile | `curl -H "Authorization: Bearer <access_token>" http://localhost:8080/api/v1/me` | `200`; rol ve izin listesi |
| Oturum yenileme ve çıkış | [requests.http](requests.http) içindeki "Oturumu yenile", "Çıkış", "Çıkıştan sonra yenileme" | `200`, `204`, `401 SESSION_EXPIRED` |
| Rol izinleri | `make test` içindeki `TestPermissionMatrixMatchesPlan` | Yetki matrisi (PLAN.md §5.10) koddaki tabloyla birebir aynı; denetçinin yazma izni yok |
| Çalınan refresh token | `make test` içindeki `TestRefreshRotatesAndDetectsReuse` | İptal edilmiş token tekrar gelince 401 ve kullanıcının bütün oturumları kapanır |

Rolün izni olmayan bir uca erişim (`403 FORBIDDEN`) ilk rol kısıtlı uçla (denetim kaydı, A7) birlikte bu tabloya eklenecek.

## Sistemi başlatma

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| Tek komutla açılış | `cp .env.example .env && docker compose up --build` (ya da `make up`) | Tüm servisler `healthy`; `docker compose ps` ile görülür |
| `.env` unutulursa | `.env` olmadan `docker compose up` | Compose "`.env eksik - once 'cp .env.example .env' calistirin`" hatasıyla durur |
| Frontend | Tarayıcıda `http://localhost:3000` | ParaKöprü sayfası açılır |
| Frontend üzerinden API | `curl -i http://localhost:3000/api/v1/olmayan-uc` | API'nin JSON yanıtı (`404 NOT_FOUND`) nginx üzerinden gelir |
| Veritabanı dışarı kapalı | `docker compose ps postgres` | Port sütununda yalnız `5432/tcp`; host'a eşleme yok |
| Root olmayan kullanıcılar | `docker top parakopru-api-1` (postgres ve frontend için de) | UID: api 65532, postgres 70, frontend 101 |
| Verileri sıfırlama | `make reset-demo` | Veriler silinir, sistem ilk haliyle yeniden açılır |

## Sistem uçları

Sistem `docker compose up --build` ile ayaktayken:

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| Sağlık kontrolü | `curl -i http://localhost:8080/health` | `200` · `{"status":"ok","checks":{"database":"ok"}}` |
| Veritabanı erişilemezken sağlık | `docker compose pause postgres`, sonra aynı `curl`, ardından `docker compose unpause postgres` | Duraklatılmışken `503` · `{"status":"unavailable","checks":{"database":"unavailable"}}`; açılınca yeniden `200` |
| Tutarlı hata biçimi | `curl -i http://localhost:8080/api/v1/olmayan-uc` | `404` · `{"error":{"code":"NOT_FOUND","message":"Böyle bir uç nokta yok."}}` |
| Desteklenmeyen yöntem | `curl -i -X DELETE http://localhost:8080/health` | `405` · `{"error":{"code":"METHOD_NOT_ALLOWED",...}}` |
| İstek kimliği | `curl -i -H 'X-Request-ID: deneme-1' http://localhost:8080/health` | Yanıt başlığında `X-Request-ID: deneme-1`; API logunda aynı `request_id` |

Aynı istekler [requests.http](requests.http) dosyasında da var.

## Otomatik testler

| Komut | Ne yapar |
|---|---|
| `make test` | Tüm Go testleri |
| `make test-race` | Aynı testler, yarış dedektörüyle |
| `make cover` | Test kapsamı özeti |
| `make lint` | golangci-lint |

Veritabanı testleri gerçek bir PostgreSQL 16 konteyneri açar (testcontainers); Docker çalışıyor olmalıdır.
Her test paketi kendi konteynerini açar, migrasyonları uygular ve iş bitince kapatır.
`cd backend && go test -short ./...` veritabanı testlerini atlayıp yalnız birim testlerini çalıştırır.

## Doğrulama kimlikleri (F-1 … F-8, T-1 … T-7)

Her kimlik, ilgili özellik geliştirildiğinde bu tabloya ve `make verify`'a eklenecek.

## Arayüz senaryoları

İlk arayüz adımında (D1) eklenecek.
