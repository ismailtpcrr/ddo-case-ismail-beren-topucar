# ParaKöprü — Dijital Banka Çekirdeği ve Mutabakat Sistemi

**Geliştiren:** İsmail Beren Topuçar

DDO Yazılım Ekibi alım projesi: müşteri hesapları, çift taraflı defter, transferler, kart provizyonları, faiz tahakkuku,
kur ve zaman yolculuğu. Go + PostgreSQL 16 + React; Docker Compose ile tek komutla çalışır.

> **Durum (dürüst özet):** 7 zorlu görevden 5'i (T-1, T-2, T-3, T-4, T-7) ve 8 kapsam maddesinden 6'sı (F-1 … F-6) çalışıyor ve
> `make verify` ile kanıtlı. **Mutabakat (T-5/F-8) ve risk kuralları (T-6/F-7) yapılmadı**; ayrıntı ve nedenleri
> [Yapılanlar ve yapılmayanlar](#yapılanlar-ve-yapılmayanlar) tablosunda.

## Kurulum

Gereken tek şey Docker (Compose v2 ile).

```bash
git clone https://github.com/ismailtpcrr/ddo-case-ismail-beren-topucar.git && cd ddo-case-ismail-beren-topucar
cp .env.example .env
docker compose up --build
```

Tüm servisler sağlıklı olduğunda:

| Servis | Adres |
|---|---|
| Frontend | http://localhost:3000 |
| API | http://localhost:8080 (sağlık: `/health`) |
| PostgreSQL | Host'a açık değil; yalnız compose ağından erişilir |

Migrasyon ve seed otomatiktir (api açılışında migrasyon + temel seed; `simulator` 20.000 müşteriyi yazar). Her servisin
`healthcheck`'i vardır ve tüm `depends_on`'lar `service_healthy` kullanır. Sık kullanılan komutlar: `make help`.

| Komut | Ne yapar |
|---|---|
| `make verify` | Ayaktaki sistemde her özelliği uçtan uca dener, PASS/FAIL tablosu basar (≈ 45 sn) |
| `make pitr-demo` | Felaket kurtarma gösterimi (WAL arşivi + temel yedek ile belirli bir ana dönüş) |
| `make reset-demo` | Verileri siler, sistemi ilk seed haliyle yeniden başlatır |
| `make test` / `make test-race` / `make cover` / `make lint` | Go testleri, yarış dedektörlü testler, kapsam, golangci-lint |

## Demo hesaplar

Her rol için bir hesap, sistem ilk açıldığında otomatik oluşturulur. Hepsinin parolası `.env.example`'daki
`DEMO_PASSWORD` değeridir: **`ParaKopru.2026`**.

| Rol | E-posta |
|---|---|
| Müşteri | `musteri@parakopru.test` |
| Operasyon uzmanı | `operasyon@parakopru.test` |
| Risk analisti | `risk@parakopru.test` |
| Uyum (compliance) uzmanı | `uyum@parakopru.test` |
| Denetçi | `denetci@parakopru.test` |
| Sistem (zamanlanmış işler) | `sistem@parakopru.test` |

Demo müşterinin dört hesabı (vadesiz TRY 50.000 / USD 2.000 / EUR 2.000, vadeli TRY 100.000) ve bir kartı vardır. Elle deneme:
[requests.http](requests.http) (VS Code REST Client), her uç için örnek ve beklenen sonuç: [TESTING.md](TESTING.md).
API belgesi: `GET http://localhost:8080/docs` (Swagger UI; betik CDN'den yüklenir, çevrimdışıysa `/openapi.yaml` doğrudan okunur), dosya olarak [backend/api/openapi.yaml](backend/api/openapi.yaml).
Prometheus metrikleri: `GET http://localhost:8080/metrics` (istek sayısı ve süre dağılımı, yol deseniyle etiketli).

## Mimari özet

**Modüler monolit, modül içi katmanlı** (handler → service → repo; kararlar: [DECISIONS.md](DECISIONS.md)):

```
frontend (React, nginx :3000) ──/api──► api (Go, chi :8080) ──┐
                                        worker (Go) ──────────┼─► PostgreSQL 16 (WAL arşivli)
simulator (seed + dış banka sahtesi) ─┘                       │
verify (make verify, tek seferlik) ───────────────────────────┘
```

- **api / worker / simulator / verify** aynı Go imajından (multi-stage, `distroless/static:nonroot`) farklı komutlarla çalışır;
  frontend `nginx-unprivileged`; tüm konteynerler root olmayan kullanıcıyla.
- **Para:** `int64` kuruş/sent, kayan nokta yok (lint kuralı); ara hesaplar `decimal`, yuvarlama banker's rounding (K-11).
- **Defter:** işaretli satırlar, her kayıtta her para biriminde borç = alacak (Go + commit anında DB trigger'ı), UPDATE/DELETE/TRUNCATE
  yasak (trigger + yetki), düzeltme ters kayıtla, hesap özetleri aynı transaction'da, hesap kimliği sırasıyla kilit (K-13).
- **Eşzamanlılık:** sıralı `FOR UPDATE`, koşullu `UPDATE`, `SKIP LOCKED`; yarışlar testle kanıtlı (T-1, T-3).
- **Idempotency-Key:** anahtar iş transaction'ıyla aynı transaction'da saklanır (K-8). **Outbox:** dış transfer gönderimi `jobs` tablosundan (K-9, K-20).
- **Denetim kaydı** eklemeli-yalnızdır; **RBAC** her uçta izin tablosuyla (6 rol).

### Frontend gerekçesi

React + Vite + TypeScript, Mantine, TanStack Query, React Router, nginx ile statik sunum ve `/api` proxy'si (aynı köken, CORS yok).
Mantine hazır, erişilebilir, mobil uyumlu bileşenler (`AppShell`, tablo, form, bildirim) verir: az kodla tutarlı arayüz. Next.js/SSR
kullanılmadı çünkü oturum zaten API'de ve sayfaların sunucuda üretilmesine gerek yok. Access token bellekte, refresh token `HttpOnly`
çerezde; aynı anda tek yenileme isteği (K-7, K-17). Her ekranda yükleniyor / boş / hata durumu var, 390 px genişlikte denendi.
Ekranlar: giriş, hesaplar, hesap hareketleri, transfer (banka içi, dış banka, onay bekleyen durumları).

## Karar gerekçeleri

27 karar kaydı [DECISIONS.md](DECISIONS.md)'de (Bağlam · Karar · Alternatifler · Sonuçlar). Başlıcaları:

| Konu | Karar | Kayıt |
|---|---|---|
| Defter | İşaretli satır, 3 katlı borç = alacak, sıralı kilit, iki zaman | K-13 |
| Idempotency | Anahtar iş transaction'ında; ret de sonuçtur | K-8, K-14 |
| Dış transfer | Ayır → gönder → (sorgula) → tamamla ya da ters kayıtla telafi | K-19, K-20 |
| Kart | Koşullu UPDATE ile bloke, tek kilitli güncellemeyle çekim | K-21 |
| Zaman yolculuğu | `as_of` (geçerlilik) × `known_at` (kayıt), defterden türetme | K-22 |
| Dört göz | Eşik `>` 10.000 TL, bekleyende bloke yok, DB kısıtı | K-23 |
| Felaket kurtarma | WAL arşivi + temel yedek (PITR); `pg_dump` ayrı | K-24 |
| Kur | TL'ye göre kotasyon, ondalık metin, kayda yazılan kur | K-25 |
| Faiz | Saf fonksiyon, kümülatif yuvarlama, çökmede devam | K-26, K-27 |

## Felaket kurtarma

`make pitr-demo` (≈ 11 sn) → `scripts/dr/pitr-demo.sh`: `pg_basebackup` + `pg_dump`, işaret kaydı **A**, zaman **T**, "hatalı" kayıt **B**; ayrı
konteynerde temel yedek açılıp WAL arşivinden `recovery_target_time = T`'ye ileri sarılır. Doğrulama: A var, B yok, müşteri sayısı yerinde,
defter dengeli. Son satır `PASS PITR: …`. `make verify` bu betiği önce çalıştırır; T-7 hem API kontrolleri hem PITR geçerse PASS olur.
`pg_dump` anlık görüntüdür (dump anındaki hali verir); belirli bir ana dönüş yalnız WAL ile olur: betik bunu da gösterir.
Adımlar ve yedekleme/geri yükleme: [RUNBOOK.md](RUNBOOK.md).

## EXPLAIN (ANALYZE)

Aşağıdaki ölçümler seed'li yerel veritabanında (20.000 müşteri, ~32.000 hesap, ~98.000 defter satırı) alındı; dizin `BEGIN; DROP INDEX …; ROLLBACK`
ile geçici kaldırıldı. **Veri küçük olduğu için farklar milisaniye mertebesinde; ölçeklenme iddiası değildir.**

| Dizin | Sorgu | Dizinli | Dizinsiz |
|---|---|---|---|
| `accounts_customer_idx` | bir müşterinin hesapları (`WHERE customer_id = …`) | `Index Only Scan`, 0,70 ms | `Seq Scan`, 1,48 ms |
| `postings_account_effective_idx` | hesabın `effective_at ≤ now()` toplamı | `Index Only Scan`, 2,96 ms | `Seq Scan`, 5,09 ms |

Aynı `postings` sorgusunu başka bir hesapla tekrarladığımda (4,45 ms dizinli / 3,43 ms dizinsiz) fark kayboldu: ~98.000 satırlık tabloda
tam tarama zaten çok hızlı, ölçüm gürültüsü farkın içinde. Dizinin değeri tablo milyonlara büyüdüğünde ortaya çıkar; bunu ölçmedim.
Planın öngördüğü diğer iki dizin (`jobs`, `risk_evaluations`/`recon_lines`) için ölçüm **yapılmadı**.

## Doğrulama Soruları

**1. En zorlandığın görev hangisiydi? Hangi alternatifleri düşündün, neden eledin?**
Para korunumu ve eşzamanlılık (T-1/T-3). Alternatifler: `SERIALIZABLE` + yeniden deneme (sıcak hesaplarda sürekli çakışır → elendi),
özeti trigger'la güncellemek (kilit sırası ve "yetersiz bakiye" kararı uygulamadan çıkar → elendi), kilit sırası yerine yeniden denenen
deadlock (gizli yavaşlama → elendi). Seçilen: hesap kimliği sırasıyla toplu `FOR UPDATE` (K-13). Bu seçim 10.000 eşzamanlı transferde deadlock'suz çalıştı.

**2. Çözümün kırıldığı ya da yetersiz kaldığı senaryo (bilinen sınırlama):**
- Bankanın iç hesapları (NOSTRO, takas) aynı para biriminde her kayıtta kilitlenir; çok yüksek hacimde bu satır sıra oluşturur (K-13).
- Banka `NOT_FOUND` dedikten *sonra* gecikmiş isteği işlerse para iki kez çıkabilir ("geç işleme"); gerçek çözüm bankanın iptal API'sidir (K-20).
- Dört göz onayı beklerken para bloke edilmez; iki büyük transfer aynı bakiyeye karşı bekleyebilir, ikincisi onayda reddedilir (K-23).
- Faiz anaparası gün sonu anındaki bakiyeden alınır; geç çalıştırılan gün sonu o günün bakiyesini değil çalıştırma anınınkini kullanır (K-27).

**3. İki dizin: hangi sorgu, dizinsiz/dizinli fark?** Yukarıdaki EXPLAIN tablosu: `accounts_customer_idx` 1,48 → 0,70 ms; `postings_account_effective_idx`
5,09 → 2,96 ms (ama ikinci ölçümde fark yok). Bu veri boyutunda fark küçüktür; dürüst sonuç budur.

**4. Yapay zekâ kullandıysan reddettiğin/düzelttiğin çıktı (önce → sonra):**
- Dört göz eşiği `≥ 10.000 TL` yazılmıştı → şartname "üzeri" diyor → `> 10.000 TL` (K-23).
- Migrasyon ayrı, tek seferlik bir servisti (`service_completed_successfully`) → şartname tüm `depends_on`'larda `service_healthy` istiyor → migrasyon `api` açılışına advisory lock ile taşındı.
- Dış banka simülatörüne şartnamede olmayan "%10 kesin red" konmuştu → şartname "%20 hata, %10 çift cevap" → kaldırıldı.
- 10.000 eşzamanlı transfer testi ilk çalıştırmada kilitlendi: transfer servisi transaction açıkken havuzdan **ikinci** bağlantı alıyordu; 64 işçi 50'lik havuzu doldurunca hepsi birbirini bekledi → okuma aynı transaction'a taşındı (K-15).
- CI'da `govulncheck` önce eski sürümle çöktü, sonra Go 1.27.0 standart kütüphane zafiyetlerini buldu → araç sürümü yükseltildi, CI en son 1.27 yamasını kurar (K-18). İlk üç commit'in kırmızı çarpısı bundandır; geçmiş yeniden yazılmadı.
- `TestConcurrentReversalsOnlyOneWins` yük altında ara sıra yanlış hata veriyordu (`INSUFFICIENT_FUNDS` yerine `ALREADY_REVERSED`) → "zaten ters çevrilmiş mi" bakışı hesap kilidinden sonraya alındı.
- Kendi test beklentimi yanlış hesaplamıştım (bileşik faiz 52.124 yazmıştım; doğrusu 52.159,41) → kod doğruydu, test düzeltildi.

**5. T-1 (para korunumu) nasıl kanıtlandı?**
```bash
cd backend && go test -race -count=1 -v -run TestConservationUnder10000ConcurrentTransfers ./internal/transfer/
```
```
conservation_test.go:127: 10000 transfer, 64 eşzamanlı işçi: 8976 tamamlandı, 1024 bakiye yetersiz → REJECTED; 9.539s (1048 transfer/sn); deadlock: 0; toplam 20000000 → 20000000
--- PASS: TestConservationUnder10000ConcurrentTransfers (9.85s)
```
40 hesap, rastgele ve ters yönlü çiftler, yarış dedektörü açık; deadlock sayısı PostgreSQL'in kendi sayacından okunur; her hesapta bakiye =
açılış − giden + gelen; toplam değişmedi; `/admin/invariants` ok. Ek olarak property testi (rapid, 100 rastgele dizi) ve `make verify` T-1
(HTTP üzerinden 10.000 eşzamanlı transfer, ≈ 4,5 sn). Komutlar: [TESTING.md](TESTING.md).

## Yapılanlar ve yapılmayanlar

| Kimlik | Durum | Not |
|---|---|---|
| F-1 Müşteri ve hesap | ✅ | vadesiz/vadeli, TRY/USD/EUR, IBAN, nesne düzeyinde yetki |
| F-2 Defter ve bakiye | ✅ | çift taraflı, ters kayıt, `/admin/invariants` |
| F-3 Transferler | ✅ | banka içi, EFT/FAST dış banka, gelen transfer |
| F-4 Kart | ✅ | provizyon, kısmi/final çekim, iade, süre aşımı |
| F-5 Kur | ✅ | zaman geçerlilikli kur, çok para birimli transfer |
| F-6 Faiz ve stopaj | ✅ | gün sonu işi, çökmede devam; ekstre ve mutabakat adımları **yok** |
| F-7 Risk kuralları, inceleme kuyruğu | ❌ yapılmadı | zaman yetmedi (planda J1/J2); transferde risk durumu (`UNDER_REVIEW`) tanımlı ama kullanılmıyor |
| F-8 Gün sonu, mutabakat, müşteri ekstresi | ❌ yapılmadı | zaman yetmedi (I1–I3); yalnız faiz ve provizyon süre aşımı adımları var, ekstre dosyası/mutabakat/ekstre ucu yok |
| T-1 Para korunumu | ✅ | 10.000 eşzamanlı transfer testi + verify |
| T-2 İdempotent transfer, saga | ✅ | belirsiz sonuçta sorgula-sonra-telafi |
| T-3 Provizyon, kullanılabilir bakiye | ✅ | yarış testleri + verify |
| T-4 Faiz | ✅ | gün tabanları, artık yıl, kuruş dağıtımı, çökme testi |
| T-5 Mutabakat motoru | ❌ yapılmadı | I2/I3 (100.000 satır ölçümü dahil) yazılmadı |
| T-6 Risk kuralları (p95 < 30 ms) | ❌ yapılmadı | J1/J2 yazılmadı; p95 ölçümü yok |
| T-7 Zaman yolculuğu, dört göz, DR | ✅ | `as_of`/`known_at`, dört göz, PITR betiği |
| Simülatör: 20.000 müşteri, dış banka | ✅ | tohumlu seed; **günde ~200.000 işlem trafiği ve gün sonu ekstre dosyası yok** (K1, I1) |
| Frontend | ◐ | müşteri ekranları var; operasyon/risk/uyum/denetçi ekranları ve canlı akış yok (K2); `make verify` UI kimliği yalnız sayfa + proxy |
| CI | ✅ | build, `go test -race`, lint, govulncheck, imaj derleme (GitHub Actions) |
| OpenAPI, `/docs`, `/metrics` | ✅ | `GET /openapi.yaml`, `GET /docs`, `GET /metrics` (girişsiz, K-03) |
| OpenTelemetry trace (api → worker → simulator) | ❌ yapılmadı | zaman yetmedi (L2'nin trace kısmı); Jaeger servisi de yok |
| k6 yük testi ve SLO tablosu | ❌ yapılmadı | L3 |
| Kaos senaryoları (≥ 2 betik) | ❌ yapılmadı | L4; PITR betiği felaket kurtarma içindir, kaos senaryosu değildir |
| Kapsam ≥ %75 (domain paketleri) | ✅ | ölçüldü (`go test -cover`): money %92, fx %88, customer %87, account %82, interest %82, transfer %79, card %78, ledger %77. Simülatör paketi (`sim`, veri üretici) %72: domain değil, bu eşiğin altında |
| Temiz klondan kurulum | ✅ | repo GitHub'dan yeni klonlandı, `cp .env.example .env && docker compose up --build`: 5 servis healthy (26 sn, Docker önbelleği sıcakken), klonda `make verify` 17 PASS / 4 FAIL (yalnız yapılmayan kimlikler) |
| Soğuk başlangıç ≤ 5 dk ölçümü | ❌ yapılmadı | `docker builder prune` ile boş önbellekten kurulum süresi ölçülmedi |
| Bonus (PDF ekstre, canlı hacim, revaluation) | ❌ | N1–N3 zorunlu değil, yapılmadı |

Test sayısı 190'ın üzerinde (şartname ≥ 60); property testleri: para, faiz, defter, kur, transfer dizileri; fuzz: `FuzzParseAmount`, parola özeti ayrıştırma.

## Son `make verify` sonucu

23:17'de, taze seed'li sistemde çalıştırıldı: **21 kontrolden 17 PASS, 4 FAIL**, süre 44 sn. FAIL olanlar yazılmamış kimlikler: F-7, F-8, T-5, T-6
(bilinçli olarak FAIL: uygulanmamış kimlik yeşil gösterilmez, K-10).

```
HEALTH AUTH-1 AUTH-2 AUTH-3 AUTH-4 UI                 PASS
F-1 F-2 F-3 F-4 F-5 F-6                               PASS
F-7 F-8                                               FAIL (henüz uygulanmadı)
T-1 T-2 T-3 T-4 T-7                                   PASS
T-5 T-6                                               FAIL (henüz uygulanmadı)
```

## Geçmiş notu

CI'nın ilk üç çalışması `govulncheck` adımında kırmızı çıktı (araç Go 1.27.0 ile çöktü, sonra standart kütüphane zafiyetlerini buldu); düzeltildi ve
son commit'ler yeşildir. Teslimden sonra geçmiş yeniden yazılmamıştır.
