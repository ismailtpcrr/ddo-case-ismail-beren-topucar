# ParaKöprü — Test ve Doğrulama Rehberi

Bu dosya proje.md §10'un istediği bölümleri içerir:

- (a) her rol için demo hesap
- (b) her doğrulama kimliği için "nasıl denenir / beklenen sonuç" tablosu
- (c) arayüzde elle denenecek senaryolar
- her zorlu görev için kanıt komutu

Bölümler, ilgili özellik geliştirildikçe doldurulur.

## Hızlı doğrulama: `make verify`

```bash
make verify          # = docker compose --profile verify run --rm --build verify
```

Sistem kapalıysa önce onu başlatır, sonra compose ağı içinden gerçek HTTP istekleriyle her doğrulama kimliğini dener ve
bir tablo basar. Bir kimlik bile FAIL ise çıkış kodu sıfırdan farklıdır. Şu anki süre yaklaşık 5-10 sn (sınır: 15 dk).

| Kimlik | Ne denenir | PASS koşulu |
|---|---|---|
| `HEALTH` | `GET /health` | `200`, `status=ok`, `database=ok`, yanıtta `X-Request-ID` |
| `AUTH-1` | Altı demo hesabın her biriyle giriş ve `/me` | Hepsi `200`; `/me` doğru rolü döndürür |
| `AUTH-2` | Yanlış parola; kayıtlı olmayan e-posta | İkisi de `401 INVALID_CREDENTIALS`, yanıtlar birebir aynı, oturum çerezi yok |
| `AUTH-3` | Her rolle `GET /api/v1/audit-log` (yetkisiz rol) | Uyum ve denetçi `200`; müşteri, operasyon, risk, sistem `403 FORBIDDEN` |
| `AUTH-4` | Yetki ihlali: token'sız istek; uydurma token; rolü `AUDITOR` yapılmış müşteri token'ı (imza bozuk); refresh token'ı access yerine kullanmak; çıkıştan sonra eski token'la oturum yenileme | Hepsi `401` |
| `UI` | Arayüz sayfası; arayüz kökeninden (nginx) `GET /api/v1/me` | Sayfa `200` ve ParaKöprü başlığı; nginx üzerinden API `200` |
| `AUTH-4` (ek) | Müşteri token'ıyla başka bir müşterinin hesabı ve hareketleri | `403 FORBIDDEN` (nesne düzeyi yetki) |
| `F-1` | Müşteri ve 3 hesap aç (vadesiz TRY/USD, vadeli EUR); kurallar; tekrar kimlik; müşteri hesap açmayı dener; simülatör verisi | `201`, IBAN'lı hesaplar; vade/para birimi kuralı ihlali `400`; aynı kimlik `409`; müşteri `403`; keyset sayfalamayla ≥ 20.000 müşteri |
| `F-2` | Demo hesaptan yeni hesaba 123,45 TL havale; bakiye, hareket ve kayıt sorguları | İki bakiye tam tutar kadar değişir; hareketlerin toplamı bakiyeye eşit; kayıtta borç = alacak; yetersiz bakiye `422`; transfer kaydı elle ters çevrilemez `422`; `/admin/invariants` ok |
| `T-1` | Simülatör müşterilerinden 40 hesap arasında 10.000 eşzamanlı transfer (48 işçi, HTTP) | Her transfer 201 ya da 422 `INSUFFICIENT_FUNDS`; her hesapta bakiye = başlangıç − giden + gelen; 40 hesabın toplamı değişmez; `/admin/invariants` önce ve sonra ok. Tipik süre ≈ 4-5 sn |
| `F-3` | EFT + FAST dış transfer (gerçek banka sahtesi); bozuk IBAN; farklı para birimi; yetersiz bakiye; gelen dış transfer (çift bildirim); operasyonun banka bildirimi göndermeyi denemesi | Transferler `COMPLETED` ya da `REVERSED` olur, gönderen bakiyesi tamamlananlar kadar düşer; `400 INVALID_IBAN`, `422 CURRENCY_MISMATCH`, `422 INSUFFICIENT_FUNDS`; gelen transfer iki bildirimde bir kez yazılır; bildirim yalnız sistem rolünde `200`, operasyonda `403` |
| `T-2` | Aynı Idempotency-Key ile 20 eşzamanlı istek; farklı gövde; anahtarsız; 100 eşzamanlı dış transfer (%20 hata, %10 çift cevap); çift ve çelişkili banka bildirimi | Tek transfer, bakiye bir kez düşer, diğerleri `Idempotent-Replayed`; `422 IDEMPOTENCY_KEY_REUSED`, `400 IDEMPOTENCY_KEY_REQUIRED`; 100 transferin hepsi `COMPLETED` ya da `REVERSED` (tipik 88-92 tamamlanır, 8-12 telafi edilir); `EXTERNAL_CLEARING` değişmez, `NOSTRO` tamamlananlar kadar azalır; çelişkili bildirim transferi ve parayı değiştirmez, tutar uyuşmazlığı `422`. Tipik süre 14-17 sn |
| `F-4` … `F-8`, `T-3` … `T-7` | Her özellik ve zorlu görev | İlgili özellik yazıldıkça eklenir. Henüz yazılmamış kimlik **FAIL ("henüz uygulanmadı")** görünür; tablo sistemin gerçek durumunu gösterir |

Yerelde (compose dışında) çalıştırmak için:

```bash
cd backend
VERIFY_API_URL=http://localhost:8080 VERIFY_FRONTEND_URL=http://localhost:3000 DEMO_PASSWORD=ParaKopru.2026 go run ./cmd/verify
```

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

| Yetki ihlali (rolün izni yok) | Müşteri token'ıyla `curl -i -H "Authorization: Bearer <token>" http://localhost:8080/api/v1/audit-log` | `403 FORBIDDEN`; uyum ve denetçi token'ıyla `200` |

## Denetim kaydı, idempotency ve iş kuyruğu

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| Denetim kaydı okunur | Uyum ya da denetçi token'ıyla `GET /api/v1/audit-log?limit=20` | `200`; `{"items":[…],"next_cursor":…}`, en yeni önce |
| Denetim kaydı değiştirilemez | `make test` içindeki `TestAuditLogIsAppendOnly` | Uygulama rolünün UPDATE/DELETE/TRUNCATE denemesi `42501` (yetki yok); süper kullanıcının denemesi bile trigger'a takılır (`23001`) |
| Denetim satırı işlemle birlikte geri alınır | `TestRecordIsRolledBackWithTheOperation` | İşlem başarısız olunca denetim satırı da kalmaz |
| Aynı Idempotency-Key aynı sonucu döndürür | `TestSameKeyReturnsSameResultWithoutRepeating`, `TestConcurrentRequestsWithSameKey` | İşlem tek kez yapılır; eşzamanlı 20 istek de aynı sonucu alır |
| Aynı anahtar, farklı istek | `TestSameKeyDifferentRequestIsRejected` | `422 IDEMPOTENCY_KEY_REUSED` |
| İş kuralı reddi de saklanır | `TestBusinessRejectionIsStoredAndPartialWritesUndone` | Tekrar istek aynı reddi alır; reddedilen işlemin yarım yazmaları geri alınır |
| Outbox | `TestEnqueueFollowsTransaction` | Geri alınan transaction'ın işi kuyrukta yoktur |
| Her iş bir kez çalışır | `TestConcurrentWorkersRunEachJobExactlyOnce` | 8 eşzamanlı worker, 200 iş: her iş tam bir kez (SKIP LOCKED) |
| Çöken worker'ın işi kaybolmaz | `TestCrashedWorkersJobIsReclaimedAfterLease` | Kira dolunca iş başka worker'a geçer ve tamamlanır |
| Worker çalışıyor | `docker compose ps worker` ve `docker compose logs worker` | `healthy`; `worker started` satırı |
| Worker düzgün kapanır | `docker compose stop worker`, sonra `docker compose logs worker` | `shutdown signal received` → `all running jobs finished` → `worker stopped` |

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

## Sürekli entegrasyon (GitHub Actions)

`.github/workflows/ci.yml`, her `push` (main) ve pull request'te çalışır; yerelde aynı adımlar şu komutlarla denenir:

| İş | Ne yapar | Yerelde |
|---|---|---|
| `backend` | `go build`, `go test -race` (testcontainers; runner'da Docker hazır), golangci-lint v2.15.0, govulncheck | `cd backend && go build ./... && go test -race ./...`; `make lint`; `cd backend && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` (beklenen: "No vulnerabilities found") |
| `frontend` | `npm ci`, oxlint, `tsc` + Vite derlemesi | `cd frontend && npm ci && npm run lint && npm run build` |
| `images` | Backend ve frontend Docker imajlarının derlenmesi (push yok) | `docker compose build` |

Durum: GitHub'da **Actions** sekmesi ya da `gh run list`.

## Müşteri ve hesaplar

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| Demo müşterinin hesapları | Müşteri token'ıyla `GET /api/v1/accounts` | Vadesiz TRY (50.000,00), USD (2.000,00), EUR (2.000,00) ve vadeli TRY (100.000,00); her birinde IBAN, bakiye, bloke, kullanılabilir bakiye. Bakiyeler açılış kaydıyla defterden gelir |
| Müşteri ve hesap açma | Operasyon token'ıyla [requests.http](requests.http) "Müşteri aç" → "Vadeli EUR hesap aç" | `201`; IBAN üretilir; denetim kaydına `customer.create` ve `account.open` düşer |
| Vade kuralları | Vadeli hesapta `maturity_date` olmadan / geçmiş tarihle; vadesizde tarihle | `400 VALIDATION_ERROR` |
| Aynı kimlik numarası | Aynı `national_id` ile ikinci müşteri | `409 CUSTOMER_EXISTS` |
| Müşteri başkasının hesabını göremez | Müşteri token'ıyla başka bir müşterinin `GET /api/v1/accounts/{id}` | `403 FORBIDDEN` |
| Müşteri hesap açamaz | Müşteri token'ıyla `POST /api/v1/accounts` | `403 FORBIDDEN` |
| Müşteri hesabı eksiye düşemez (veritabanı) | `go test -run TestCustomerBalanceCannotGoNegative ./internal/account/` | Bakiyeyi eksiye ya da blokeyi bakiyenin üstüne çeken UPDATE `CHECK` kısıtına takılır |
| İç hesap planı | Operasyon token'ıyla `GET /api/v1/accounts` | Her para birimi için 8 iç hesap (NOSTRO, EXTERNAL_CLEARING, …, OPENING_EQUITY) |

## Para tipi ve yuvarlama

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| Banker's rounding | `cd backend && go test -run 'TestRoundHalfEven' -v ./internal/money/` | `0.125 → 0.12`, `0.135 → 0.14`, `2.5 → 2`, `3.5 → 4`; negatiflerde de simetrik |
| Farklı para birimleri karışmaz | `go test -run TestAddSubRequireSameCurrency ./internal/money/` | TRY + USD hata döner (`ErrCurrencyMismatch`) |
| Taşma sessizce yanlış sonuç üretmez | `go test -run 'TestOverflow|TestPropertyAddNeverWrapsSilently' ./internal/money/` | int64 sınırında hata döner; rastgele her çiftte ya doğru sonuç ya hata |
| Property testleri (rapid) | `go test -run TestProperty -v ./internal/money/` | Toplama değişmeli ve birleşmeli, `(a+b)-b = a`; metin ↔ tutar ve decimal ↔ tutar dönüşümleri kayıpsız; yuvarlama en fazla yarım kuruş sapar |
| Fuzz (ekstre tutarı ayrıştırma) | `go test -run '^$' -fuzz FuzzParseAmount -fuzztime 30s ./internal/money/` | Panic yok; kabul edilen her metin aynı tutara geri döner |
| Kayan nokta yasağı | `make lint` | Para paketlerinde `float32/64`, `decimal.NewFromFloat`, `.Float64()` kullanımı lint hatasıdır |

## Çift taraflı defter

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| Hesap hareketleri | Müşteri token'ıyla `GET /api/v1/accounts/{id}/entries` (kendi hesabı) | `200`; en yeni önce, sayfalı; her harekette `side`, `amount_minor`, `balance_effect_minor` (giriş +, çıkış −). Toplamları bakiyeye eşit |
| Başkasının hareketleri | Müşteri token'ıyla başka bir müşterinin hesabının `/entries` ucu | `403 FORBIDDEN` |
| Kaydın iki tarafı | Operasyon/risk/uyum/denetçi token'ıyla `GET /api/v1/ledger/entries/{entry_id}` | `200`; açılış kaydında borç `OPENING_EQUITY`, alacak müşteri hesabı; tutarlar eşit. Müşteri token'ıyla `403` |
| Borç = alacak (Go) | `cd backend && go test -run 'TestValidate' -v ./internal/ledger/` | Tek satırlı, sıfır/negatif tutarlı, dengesiz ya da "100 TRY borç / 100 USD alacak" gibi para birimi karışık kayıtlar reddedilir |
| Borç = alacak (veritabanı) | `go test -run TestDatabaseRejectsUnbalancedEntry -v ./internal/ledger/` | Doğrudan SQL ile yazılan dengesiz ya da satırsız kayıt commit anında reddedilir (`23514`); commit edilmiş kayda sonradan satır eklenemez; satırın para birimi hesabınkiyle aynı olmalı |
| Özet doğal tarafa göre güncellenir | `go test -run TestPostUpdatesSummaryByNormalSide -v ./internal/ledger/` | Müşteri hesabı alacaklanınca, NOSTRO borçlanınca artar; her hesapta özet = defterden türetilen bakiye |
| Yetersiz bakiye | `go test -run TestInsufficientFundsWritesNothing ./internal/ledger/` | `422 INSUFFICIENT_FUNDS`; hiçbir satır ve bakiye değişikliği kalmaz |
| Kilit sırası, deadlock yok | `go test -run TestOppositeConcurrentPostingsDoNotDeadlock -v ./internal/ledger/` | 16 eşzamanlı işçi, rastgele ters yönlü 400 kayıt, yeniden denemesiz transaction'larla: tek bir deadlock bile yok; toplam para değişmez |
| Defter değiştirilemez | `go test -run TestLedgerIsAppendOnly ./internal/ledger/` ya da `docker compose exec postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "UPDATE postings SET amount_minor = 1"'` | Uygulama rolü `42501` (yetki yok); sahip rol bile trigger'a takılır: "postings tablosu eklemeli-yalnızdır" |
| Kayıt zamanı veritabanından | `go test -run TestRecordedAtComesFromDatabase ./internal/ledger/` | Elle verilen `recorded_at` yok sayılır; geriye tarihli `effective_at` olur, ileri tarihli olmaz |
| Ters kayıt | [requests.http](requests.http) "Ters kayıt (operasyon)" | `201`; yeni kayıt `kind=REVERSAL`, satırlar özgünün tersi, müşteri bakiyesi geri alınır; özgün kayıt yerinde (`reversed_by` dolu); denetim kaydına `ledger.reverse`. Demo bakiyesini geri getirmek için `make reset-demo` |
| Ters kayıt idempotent | Aynı istek aynı `Idempotency-Key` ile | Aynı yanıt, `Idempotent-Replayed: true`; ikinci bir ters kayıt yazılmaz |
| Bir kez ters çevrilir | Aynı kayıt yeni anahtarla; ters kaydın kendisi | `409 ALREADY_REVERSED`; `422 ENTRY_NOT_REVERSIBLE`. Eşzamanlı 10 deneme: `go test -run TestConcurrentReversalsOnlyOneWins ./internal/ledger/` → tam bir başarı |
| Sürece ait kayıt elle ters çevrilemez | `go test -run TestReverseByRequest ./internal/ledger/` | Transfer kaydı gibi bir sürecin kaydı `422 ENTRY_NOT_REVERSIBLE` |
| Ters kayıt yetkisi | Müşteri, risk, uyum ya da denetçi token'ıyla ters kayıt | `403 FORBIDDEN` (yalnız operasyon) |
| Defterin tamamı dengede | `docker compose exec postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT currency, sum(amount_minor) FROM postings GROUP BY 1"'` | Her para biriminde `0` |

## Transfer (banka içi) ve değişmezler

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| Havale | [requests.http](requests.http) "Banka içi havale" (müşteri token'ı) | `201`, `state=COMPLETED`; gönderenden düşer, alıcıya geçer; `GET /transfers/{id}` olaylarda `CREATED → COMPLETED`; defter kaydı transfer ID'sine bağlı; denetim kaydına `transfer.create` |
| Idempotency-Key | Aynı isteği aynı anahtarla tekrarla | Aynı yanıt, `Idempotent-Replayed: true`; para bir kez hareket eder. Eşzamanlı 20 istek: `go test -run TestConcurrentRequestsWithSameKey -v ./internal/transfer/` |
| Anahtar yok / farklı gövde | Anahtarsız; aynı anahtar farklı tutar | `400 IDEMPOTENCY_KEY_REQUIRED`; `422 IDEMPOTENCY_KEY_REUSED` |
| Yetersiz bakiye | Bakiyeden büyük tutar | `422 INSUFFICIENT_FUNDS`, `X-Transfer-ID` başlığı; `REJECTED` kaydı yazılır, bakiye değişmez; tekrar istek aynı reddi döndürür (`TestInsufficientFundsIsRecordedAsRejected`) |
| İş kuralları | `go test -run TestBusinessRules -v ./internal/transfer/` | Başkasının hesabı 403; iç hesap, para birimi uyumsuzluğu, kendine gönderme, bozuk IBAN, bilinmeyen (dış) IBAN reddedilir |
| Durum makinesi | `go test -run TestStateMachine ./internal/transfer/` | Geçerli geçişler kabul, geçersizler (ör. `COMPLETED → CREATED`) reddedilir |
| Nesne düzeyi okuma | `go test -run TestObjectLevelReadAccess ./internal/transfer/` | Gönderen ve alıcı transferi görür, üçüncü kişi `403` |
| Değişmezler ucu | Operasyon/denetçi token'ıyla `GET /api/v1/admin/invariants` | `200`, `ok=true`, her para biriminde `sum_minor=0`, `summary_mismatches=0`. Müşteri token'ıyla `403` |
| Ölçüm bozulmayı yakalar | `go test -run TestInvariantsDetectCorruption ./internal/transfer/` | Bakiye özeti elle bozulunca `ok=false`, `summary_mismatches=1` |

## T-1 kanıtı: para korunumu (10.000 eşzamanlı transfer)

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| 10.000 eşzamanlı transfer | `cd backend && go test -race -count=1 -v -run TestConservationUnder10000ConcurrentTransfers ./internal/transfer/` | PASS. 40 hesap, 64 eşzamanlı işçi, rastgele (ters yönlü dahil) çiftler, her biri yeni Idempotency-Key ile tam servis yolundan. Doğrulananlar: sunucunun deadlock sayacı (`pg_stat_database.deadlocks`) artmaz; her transfer COMPLETED ya da REJECTED; her hesapta bakiye = açılış − giden + gelen; hiçbir hesap eksiye düşmez; toplam para değişmez; `CheckInvariants` `ok` |
| Property testi | `go test -race -count=1 -v -run TestPropertyRandomTransferSequencesConserveMoney ./internal/transfer/` | 100 rastgele işlem dizisi (rapid): model bakiyesi = özet = defterden türetilen bakiye; transfer ancak bakiye yetiyorsa tamamlanır; toplam değişmez |

Örnek çıktı (macOS, Docker'da PostgreSQL 16, `-race` açık):

```
conservation_test.go:127: 10000 transfer, 64 eşzamanlı işçi: 9115 tamamlandı, 885 bakiye yetersiz → REJECTED; 9.494s (1053 transfer/sn); deadlock: 0; toplam 20000000 → 20000000
--- PASS: TestConservationUnder10000ConcurrentTransfers (9.80s)
conservation_test.go:143: [rapid] OK, passed 100 tests (4.521866208s)
--- PASS: TestPropertyRandomTransferSequencesConserveMoney (4.54s)
```

## Simülatör ve seed

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| Servis hazır | `docker compose ps simulator`; `docker compose logs simulator` | `healthy` (seed bitince); `simulator ready … "seed_skipped":false` (≈ 3-4 sn, 20.000 müşteri) |
| Aynı tohumla tekrar atlanır | `docker compose restart simulator`, sonra logu izle | `"seed_skipped":true`, veri değişmez (`seed_runs` tablosunda kayıt var) |
| Seed sonrası defter dengede | Operasyon token'ıyla `GET /api/v1/admin/invariants` | `ok:true`; açılış bakiyeleri defterden geçtiği için her para biriminde Σ = 0 |
| Aynı tohum = aynı veri | `cd backend && go test -run 'TestGenerateIsDeterministic|TestGenerateIdentifiersAreUniqueAndAvoidDemo' -v ./internal/sim/` | Aynı tohumla birebir aynı müşteri/hesap/UUID; kimlik numaraları benzersiz, demo müşterinin numarasıyla çakışmaz |
| Toplu yazma doğru | `go test -run TestSeedWritesBalancedLedgerAndIsIdempotent -v ./internal/sim/` | Kayıtlar yazılır, değişmezler ok, OPENING_EQUITY eksi toplam, ikinci çalıştırma atlanır |
| Tohumu değiştirme | `.env` içinde `SIM_SEED`/`SIM_CUSTOMERS` değiştirip `make reset-demo` | Farklı tohum farklı veri; boyut `SIM_CUSTOMERS` ile ayarlanır |

## Kart provizyonu, çekim, iade

Kart ağı çağrıları yalnız `sistem@parakopru.test` hesabıyla yapılır (RBAC); demo müşterinin vadesiz TRY hesabına bağlı bir kartı vardır
(`GET /api/v1/cards`), simülatör müşterilerinin de birer kartı vardır.

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| Provizyon | [requests.http](requests.http) "Kart provizyonu" (sistem token'ı) | `201`; hesapta `balance_minor` aynı, `held_minor` +tutar, `available_minor` −tutar; defter kaydı yok |
| Kullanılabilir bakiyeyi aşma | Bakiyeden büyük tutarla provizyon | `422 INSUFFICIENT_FUNDS`; blok değişmez |
| Kısmi ve final çekim | "Kısmi çekim", "Final çekim" | Kısmi: bakiye çekilen kadar düşer, blok kalanı korur, hold `ACTIVE`. Final: kalan blok serbest, hold `CAPTURED` (`released_minor` dolu) |
| Aşırı çekim / aktif olmayan | Kalandan fazla; çekilmiş holda tekrar | `422 CAPTURE_EXCEEDS_HOLD`; `409 HOLD_NOT_ACTIVE` |
| İade | "İade" | Bakiye iade kadar artar; toplam iade çekilenden fazlaysa `422 REFUND_EXCEEDS_CAPTURE` |
| Süre aşımı | `ttl_seconds: 5` ile provizyon aç, ~10 sn bekle, `GET /accounts/{id}/holds?state=EXPIRED` | Worker blokeyi serbest bırakır (`HOLD_EXPIRY_INTERVAL`, varsayılan 5 sn); hold `EXPIRED`; sonrasında çekim `409` |
| Idempotency | Aynı anahtarla tekrar | Aynı yanıt, `Idempotent-Replayed: true`; ikinci blok/çekim/iade yok |
| Değişmezler | `GET /api/v1/admin/invariants` | `hold_mismatches: 0` (held_minor = Σ aktif provizyon) |
| Yetki | Müşteri token'ıyla `POST /cards/{id}/authorizations`; başka müşterinin hesabı `/holds` | `403` |
| Go testleri | `cd backend && go test -race -v ./internal/card/` | Provizyon, kısmi/final çekim, iade, süre aşımı, idempotency, nesne düzeyi yetki |

## Dış banka transferi (saga)

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| Dış transfer | [requests.http](requests.http) "Dış banka transferi" (müşteri token'ı; IBAN bankanın dışında) | `202`, `state=RESERVED`; para hemen müşteriden düşer (takas hesabında bekler); birkaç saniye içinde `GET /transfers/{id}` → `COMPLETED` (olaylar `CREATED → RESERVED → SENT → COMPLETED`) |
| Toplu deneme (gerçek banka sahtesi) | 40 dış transfer gönder (`curl` döngüsü; `TESTING.md` altındaki komut ya da arayüz), ~30 sn bekle, `GET /transfers?limit=100` | Hepsi `COMPLETED` ya da `REVERSED`; hiçbiri `RESERVED/SENT` takılı kalmaz. Örnek çalıştırma: 40 transferin 37'si COMPLETED, 3'ü REVERSED; müşteri bakiyesi tam 37 × tutar düştü; `EXTERNAL_CLEARING` = 0; `/admin/invariants` ok; worker logunda `bank send uncertain` (4), API logunda `duplicate bank result ignored` (39) |
| Belirsiz sonuç → sorgu → tamamla | `go test -run TestUncertainOutcomeIsQueriedThenCompleted -v ./internal/transfer/` | Banka zaman aşımı verince `UNKNOWN`; para ayrılmış kalır; sorgulama da başarısızsa `UNKNOWN` kalıp yeniden denenir; sorgu `COMPLETED` deyince tamamlanır, yeniden gönderim olmaz |
| Belirsiz sonuç → telafi (ters kayıt) | `go test -run 'TestUncertainOutcomeNotFoundIsCompensated|TestExternalTransferRejectedIsReversed' -v ./internal/transfer/` | Banka reddederse ya da hiç işlememişse (`NOT_FOUND`): `FAILED → REVERSED`, ters kayıt yazılır, para müşteriye döner, takas 0, nostro değişmez |
| Çift cevap | `go test -run TestWebhookDuplicatesAndMismatch -v ./internal/transfer/` | Aynı bildirim iki kez ve geç gelen çelişkili bildirim para hareketi yapmaz; tutar uyuşmazlığı `422 BANK_RESULT_MISMATCH`; bilinmeyen referans `404` |
| Worker ve bildirim yarışı | `go test -run TestConcurrentSettlementHappensOnce -v ./internal/transfer/` | Aynı anda 6 worker çalıştırması + 6 bildirim: tek tahsilat (nostro bir kez azalır) |
| Gönderim işi outbox'tan | `go test -run TestExternalTransferCompletes -v ./internal/transfer/` | Ayırma kaydıyla aynı transaction'da `transfer.send` işi kuyruğa girer; iş tekrar çalışsa banka bir kez çağrılır |
| Gelen dış transfer | [requests.http](requests.http) "Bankadan gelen transfer" (sistem token'ı); `go test -run TestIncomingTransferIsIdempotentByBankReference ./internal/transfer/` | Alıcı alacaklanır, `NOSTRO` borçlanır; aynı banka referansı ikinci kez `duplicate:true`, tekrar yazılmaz |
| Bildirim yetkisi | Operasyon/müşteri token'ıyla `POST /webhooks/bank/incoming` | `403` (yalnız sistem rolü) |

### T-2 kanıt komutları

| Ne | Komut |
|---|---|
| API üzerinden uçtan uca (idempotency + saga) | `make verify` → `F-3`, `T-2` satırları |
| Geçersiz geçişin reddi (`409 INVALID_STATE_TRANSITION`) | `cd backend && go test -run 'TestInvalidTransitionIsRejected|TestStateMachine' -v ./internal/transfer/` |
| Eşzamanlı aynı anahtar (banka içi ve dış) | `go test -run 'TestConcurrentRequestsWithSameKey|TestExternalConcurrentSameKeyReservesOnce' -v ./internal/transfer/` |
| Idempotency katmanı (yarım yazma geri alınır, ret saklanır) | `go test -v ./internal/platform/idempotency/` |

## Dış banka sahtesi (simülatör)

Banka, `simulator` servisinde, compose ağında `http://simulator:8080` adresindedir (host'a açık değil).
Ayarlar `.env`: `BANK_FAILURE_PERCENT=20`, `BANK_DUPLICATE_PERCENT=10`, `SIM_SEED`.

| Ne denenir | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| Gönderim | `docker run --rm --network parakopru_default curlimages/curl -s -m 5 -X POST -H 'Content-Type: application/json' -d '{"reference":"deneme-1","amount_minor":1000,"currency":"TRY","beneficiary_iban":"TR450099900000000000000001"}' http://simulator:8080/v1/transfers` | Çoğunlukla `200 {"status":"COMPLETED",…}`; yaklaşık beşte birinde `503`, bağlantı kesilmesi ya da zaman aşımı. Aynı referansla tekrar: aynı sonuç, yeni işlem yok |
| Sorgulama | Aynı ağda `curl http://simulator:8080/v1/transfers/deneme-1` | `COMPLETED`, `REJECTED` (geçersiz alıcı IBAN) ya da `NOT_FOUND` (hiç işlenmedi). Sorgulamada hata üretilmez |
| %20 hata ve belirsiz sonuç | `cd backend && go test -run TestBankFailureRateAndUncertainOutcomes -v ./internal/sim/` | Hata oranı ≈ %20 (±3); hatalı çağrıların bir kısmı işlenmeden (`NOT_FOUND`), bir kısmı **işlendiği halde** (`COMPLETED`) hata dönmüştür |
| Banka tarafı idempotans | `go test -run TestBankIsIdempotentPerReference ./internal/sim/` | Aynı referansla yeniden denemeler tek işlem üretir |
| %10 çift cevap | `go test -run TestBankSendsDuplicateNotifications ./internal/sim/` | Sonuç bildirimlerinin ≈ %10'u iki kez gelir |
| Aynı tohum = aynı davranış | `go test -run TestBankIsDeterministicPerSeed ./internal/sim/` | İki örnek aynı isteklere aynı kaderi yaşatır; farklı tohum farklı |

## Doğrulama kimlikleri (F-1 … F-8, T-1 … T-7)

Her kimlik, ilgili özellik geliştirildiğinde bu tabloya ve `make verify`'a eklenecek.

## Arayüz senaryoları

Sistem ayaktayken `http://localhost:3000`. Her ekranda yükleniyor / boş / hata durumu vardır; mobil genişlikte
(≈ 390 px) menü hamburger düğmesine sığar, tablolar yatay kayar.

| # | Senaryo | Beklenen sonuç |
|---|---|---|
| 1 | Giriş ekranında yanlış parola | "E-posta ya da parola hatalı." mesajı, oturum açılmaz |
| 2 | `musteri@parakopru.test` / `ParaKopru.2026` ile giriş | Hesaplar ekranı: Vadesiz TRY 50.000,00 ₺, USD 2.000,00 $, EUR 2.000,00 €, Vadeli TRY 100.000,00 ₺ (IBAN, bakiye, kullanılabilir) |
| 3 | Bir hesap kartına tıkla | Hesap ayrıntısı ve hareketler: "Açılış bakiyesi +50.000,00 ₺"; 25'ten fazla hareket varsa "Daha eski hareketler" |
| 4 | Sayfayı yenile (F5) | Oturum çerezden (HttpOnly refresh token) geri gelir, giriş ekranına dönülmez |
| 5 | Transferler → gönderen: Vadesiz TRY, alıcı IBAN: vadeli TRY hesabının IBAN'ı (alan kendi hesaplarını önerir), tutar `12,50` | "Transfer tamamlandı" bildirimi; Geçmiş'te "Tamamlandı" satırı; hesap bakiyeleri güncellenir |
| 6 | Aynı formda bakiyeden büyük tutar (`999.999,00`) | Kırmızı uyarı "Hesapta yeterli kullanılabilir bakiye yok."; geçmişte "Reddedildi" satırı |
| 7 | Gönderen TRY hesap, alıcı USD hesabın IBAN'ı | "Transfer para birimi, gönderen hesabın para birimiyle aynı olmalı." (çok para birimi G1'de) |
| 8 | `operasyon@parakopru.test` ile giriş | Menü Hesaplar ve Transferler; tüm hesaplar (iç hesaplar dahil) "Daha fazla göster" ile sayfalanır |
| 9 | `denetci@parakopru.test` ile Transferler | Transfer formu yok (denetçi salt okur), yalnız geçmiş |
| 10 | Tarayıcıda ağ bağlantısını kes, bir ekranı yenile | Kırmızı hata kutusu ve "Tekrar dene" düğmesi |
| 11 | Çıkış | Giriş ekranına dönülür; geri tuşuyla korunan ekran açılmaz |

Otomatik arayüz kanıtı: `make verify` içindeki `UI` kimliği (sayfa ve nginx üzerinden API).
