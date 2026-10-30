# ParaKöprü — Proje Planı ve Tasarım Kararları

Bu dosya projenin yol haritasıdır. Kodlamaya başlarken önce `proje.md` (şartname), sonra bu dosya okunur.
İş, **12. bölümdeki adımlarla sırayla** ilerler; biten adımın kutusu `[x]` yapılır.
Buradaki her karar gerekçesiyle birlikte `DECISIONS.md` ve README'ye taşınacaktır.

**Temel kural:** şartname (`proje.md`) tek doğruluk kaynağıdır. Şartnamenin istemediği özellik eklenmez.
Bu plandaki her kalem ya şartnamede doğrudan istenen bir şeydir ya da onu gerçekleştirmenin / kanıtlamanın aracıdır.
Bonus maddeleri yalnız zorunlu her şey bittikten sonra yapılır.

---

## 1. Şartname ne istiyor? (kontrol listesi)

**Çalıştırma**
- `cp .env.example .env && docker compose up --build` → frontend `:3000`, API `:8080`, PostgreSQL dışarı kapalı.
- Healthcheck + `depends_on: condition: service_healthy`; boş Docker önbelleğinde ≤ 5 dk'da hazır.
- Migrasyon ve seed otomatik; her rol için bir demo hesap README'de.
- `GET /health` veritabanı bağlantısını da kontrol eder.
- Root olmayan kullanıcı; multi-stage, küçük imajlar.
- Veri üretici (simülatör) ayrı compose servisi.

**İşlev** — F-1 hesaplar (vadesiz/vadeli, TRY/USD/EUR) · F-2 çift taraflı defter ve bakiye · F-3 iç/dış transfer (havale/EFT/FAST) · F-4 kart provizyon/çekim/iade · F-5 kur ve çok para birimi · F-6 faiz ve stopaj · F-7 risk kuralları ve inceleme kuyruğu · F-8 gün sonu, dış ekstre mutabakatı, müşteri ekstresi.
Frontend gerçek veriyle çalışır; her ekranda yükleniyor/boş/hata durumu; mobil genişlikte kullanılabilir.

**Zorlu görevler** — T-1 … T-7, her biri için çalıştığını **kanıtlayan** test/ölçüm/betik (bkz. bölüm 9).

**Ortak teknik** — argon2id; JWT access + refresh; RBAC ve **her** uç noktada yetki kontrolü (401/403) · migrasyon dosyaları; FK/UNIQUE/CHECK; para tam sayı; kritik sorgular için dizin + README'de `EXPLAIN (ANALYZE)` · transaction + kilit + yarış testi · kritik POST'larda `Idempotency-Key` · append-only denetim tablosu · hata biçimi `{"error":{"code","message"}}`, sunucu tarafı doğrulama, keyset sayfalama · `context` zaman aşımı, graceful shutdown, JSON log (parola/token asla), tüm ayarlar ortam değişkeninden.

**Simülatör** — 20.000 müşteri, günde ~200.000 işlem, dış banka sahtesi (gecikme, hata, çift cevap), gün sonu ekstresi (bilerek bozuk satırlarla), aynı tohum = aynı veri, arayüzde sistemin canlı olduğu görülür.

**Kalite** — ≥ 60 otomatik test · domain paketlerinde kapsam ≥ %75 · ≥ 2 fuzz/property test · GitHub Actions: build + test + lint + `govulncheck` + imaj derleme · OpenAPI · `/metrics` · en az bir akışta OpenTelemetry trace · k6 + SLO tablosu (dürüst) · `DECISIONS.md` ≥ 7 kayıt · ≥ 2 kaos betiği + sonuçları · `RUNBOOK.md` (yedek/geri yükleme, expand → migrate → contract).

**Hızlı doğrulama** — `make verify`: ayaktaki sisteme karşı F-1…F-8, T-1…T-7, `/health`, yanlış parola, yetkisiz rol, yetki ihlali; sonunda PASS/FAIL tablosu, hata varsa sıfırdan farklı çıkış kodu, ≤ 15 dk · `make load` ayrı · `TESTING.md` (demo hesaplar, kimlik başına "nasıl denenir / beklenen sonuç", arayüz senaryoları, kanıt komutları) · `requests.http` · `make reset-demo`.

**README** — başta ad, kurulum, mimari özet, karar gerekçeleri · frontend seçimi ve gerekçesi · demo hesaplar · felaket kurtarma bölümü · `EXPLAIN (ANALYZE)` çıktıları · `## Doğrulama Soruları` (5 soru).

**Teslim** — public depo, anlamlı commit geçmişi (tek commit kabul edilmez), teslimden sonra force push/silme yok.

**Puanlama** — 150 puanın 40'ı zorlu görevler, 50'si yöneticinin genel izlenimi (kullanılabilirlik, okunabilirlik, karar mantığı, README açıklığı). Bu yüzden: **derinlik ve okunabilirlik önce**; yapılamayan kısım README'de dürüstçe "yapılmadı, nedeni şu" diye yazılır.

---

## 2. Mimari

Şartname kodun iç yapısını dayatmıyor; seçim bize ait.

**Seçim: modüler monolit, her modülün içi katmanlı.**

- **Modüler:** kod iş alanlarına göre bölünür — müşteri/hesap, defter, transfer, kart, kur, faiz, mutabakat, risk.
- **Monolit:** hepsi tek Go programı, tek veritabanı. Bir transfer = defter kaydı + bakiye özeti + denetim kaydı
  + iş kuyruğu kaydı; bunların **tek bir transaction** içinde yazılması para korunumunun (T-1) temelidir.
  Mikroserviste bu, servisler arası dağıtık transaction demek olurdu.
- **Katmanlı:** her modülün içi `handler` (HTTP) → `service` (iş kuralı) → `repo` (SQL).

```
                 hesap  defter  transfer  kart  kur  faiz  mutabakat  risk
handler  (HTTP)    │      │        │       │     │     │       │        │
service  (kural)   │      │        │       │     │     │       │        │
repo     (SQL)     │      │        │       │     │     │       │        │
                   └──────┴────────┴─── tek PostgreSQL ────────┴────────┘
```

**İki çalışma biçimi, aynı imaj:** `api` (HTTP) ve `worker` (zamanlanmış işler: dış transfer gönderimi ve
sorgulaması, süresi dolan provizyonlar, gün sonu). Şartnamedeki "Sistem (zamanlanmış işler)" rolü worker'dır.
Ayrı konteyner olması, gün sonu işinin "çökünce kaldığı yerden devam ettiğini" worker'ı öldürerek göstermeyi sağlar.

### Compose servisleri

Şartname kuralı: her servis `healthcheck` içerir ve **tüm** `depends_on`'lar `condition: service_healthy` kullanır.

```
postgres ─healthy─► api :8080   (açılışta migrasyon + temel seed; bitince healthy)
                      │ healthy
                      ├──► worker
                      ├──► frontend :3000
                      └──► simulator   (dış banka + 20.000 müşteri seed'i + işlem trafiği + gün sonu ekstre dosyası)
                                │ healthy (seed bitti)
                                ▼
                            verify [profil: verify]      k6 [profil: load]
jaeger (OpenTelemetry trace arayüzü :16686)
```

- Migrasyonlar `api` açılışında PostgreSQL advisory lock ile tek sefer çalışır. Tek seferlik ayrı bir servis yoktur;
  böylece her bağımlılık `service_healthy` olur.
- Simülatör, şartnamedeki gibi **tek, ayrı** bir servistir: dış banka sahtesi, 20.000 müşterilik tohumlu seed,
  günde ~200.000 işlemlik trafik (kart ağı dahil) ve gün sonu ekstre dosyası.
  `api` simülatörü beklemeden başlar (bağımlılık döngüsü olmasın diye); banka erişilemezse transferler kuyrukta bekleyip yeniden denenir.
- `postgres` dahil tüm konteynerler root olmayan kullanıcıyla çalışır; `postgres` WAL arşivi açık çalışır (T-7).

---

## 3. Kod tasarım ilkeleri

### 3.1 Katman kuralları

| Katman | Yapar | Yapmaz |
|---|---|---|
| `handler` | JSON çözme, girdi doğrulama, yetki kontrolü (`Require(izin)`), servisi çağırma, yanıt/hata yazma | SQL çalıştırmaz, iş kuralı içermez |
| `service` | İş kuralları, transaction sınırları, durum geçişleri, diğer modüllerle konuşma (arayüz üzerinden) | HTTP bilmez (`http.Request` görmez) |
| `repo` | SQL sorguları; `db.DBTX` alır (havuz ya da transaction) | Transaction başlatmaz, iş kuralı içermez |

- Bağımlılık yönü tek: `handler → service → repo`. Modüller birbirinin `repo`'suna erişmez, yalnızca servis arayüzü üzerinden konuşur.
- `platform/` (ortak altyapı) hiçbir iş modülüne bağımlı değildir.

### 3.2 Bağımlılık enjeksiyonu (DI)

**Evet, kullanıyoruz — framework'süz, constructor injection ile.**

- Her bileşen ihtiyaçlarını `New…(…)` parametresi olarak alır. Global değişken, singleton, `init()` içinde bağlantı kurma yok.
- Bütün nesneler tek bir yerde — `cmd/api/main.go` ve `cmd/worker/main.go` ("composition root") — oluşturulur ve birbirine bağlanır.
- Neden framework yok (wire, fx): Go'da yaygın yol elle bağlamaktır; kod "sihirsiz" ve okunur kalır; testte sahte bağımlılık vermek kolaydır.

```go
// internal/transfer/service.go
type LedgerPoster interface {
    Post(ctx context.Context, tx db.DBTX, e ledger.Entry) (ledger.EntryID, error)
}
type BankClient interface {
    Send(ctx context.Context, r bank.SendRequest) (bank.SendResult, error)
    Inquire(ctx context.Context, ref string) (bank.Status, error)
}

type Service struct {
    tx     *db.TxManager
    repo   *Repo
    ledger LedgerPoster
    bank   BankClient
    risk   RiskEvaluator
    clock  clock.Clock
}

func NewService(tx *db.TxManager, repo *Repo, l LedgerPoster, b BankClient, r RiskEvaluator, c clock.Clock) *Service {
    return &Service{tx: tx, repo: repo, ledger: l, bank: b, risk: r, clock: c}
}
```

### 3.3 SOLID — bu projede somut karşılığı

| İlke | Projedeki karşılığı |
|---|---|
| **S** Tek sorumluluk | `handler` yalnız HTTP, `service` yalnız kural, `repo` yalnız SQL. Faiz hesabı (saf fonksiyon) ile faizi deftere yazan iş ayrı. |
| **O** Açık/kapalı | Yeni risk kural tipi = `RuleEvaluator` arayüzünü uygulayan yeni tip + kayıt defterine (registry) ekleme; motor değişmez. Transfer durum geçişleri kodda veri (tablo) olarak tanımlı. |
| **L** Yerine geçebilirlik | `BankClient`'ın gerçek HTTP uygulaması ile testteki sahtesi; `Clock`'un gerçek ve sabit saat uygulamaları birbirinin yerine sorunsuz geçer. |
| **I** Arayüz ayrımı | Arayüzler küçük ve **kullanan** paket tarafından tanımlanır ("arayüz al, somut tip döndür"). Transfer servisi defterin tamamını değil, yalnız `Post` metodunu ister. |
| **D** Bağımlılığın tersine çevrilmesi | Servisler somut pgx/HTTP istemcilerine değil arayüzlere bağımlı; somut tipler composition root'ta verilir. |

### 3.4 Ortak kurallar

- **Para:** `money.Amount{Minor int64, Currency}`; para birimleri farklıysa toplama hata döner. Saklanan her tutar `int64` kuruş/sent.
  Faiz ve kur gibi ara hesaplar `decimal` ile yapılır, sonuç **banker's rounding** (yarımda çifte yuvarlama) ile kuruşa çevrilir.
  Para ile ilgili paketlerde `float32/64` yasak (lint kuralı).
- **Transaction:** `db.TxManager.WithTx(ctx, fn)`; serileştirme hatası (40001) ve deadlock'ta (40P01) en fazla 3 kez, rastgele beklemeyle yeniden dener.
  Transaction sınırına servis karar verir.
- **Hata:** `apperr.Error{Code, Status, Message}`; önceden tanımlı hatalar (`ErrInsufficientFunds`, `ErrInvalidTransition`, …).
  Handler tek bir `httpx.WriteError` ile `{"error":{"code","message"}}` yazar. Bilinmeyen hata → 500 `INTERNAL`, ayrıntısı yalnız loga.
- **Context:** her I/O fonksiyonunun ilk parametresi `ctx`; istek başına zaman aşımı; dış banka çağrısına ayrı, kısa zaman aşımı.
- **Log:** `log/slog` JSON; her istekte `request_id`, `user_id`, yol, durum, süre. `Authorization`, parola, token ve istek gövdeleri **asla** loglanmaz.
- **Yapılandırma:** tüm ayarlar ortam değişkeninden bir `Config` struct'ına; eksik zorunlu ayarda program başlamaz.
- **Saat ve kimlik üretimi enjekte edilir** (`clock.Clock`, `ids.Generator`) → faiz, provizyon süresi ve zaman yolculuğu testleri deterministik.
- **Adlandırma:** kod adları İngilizce; yorumlar ve dokümanlar Türkçe.

---

## 4. Teknolojiler ve kütüphaneler

| Amaç | Seçim |
|---|---|
| Dil | Go 1.27 (şart: 1.22+) |
| HTTP router | `go-chi/chi/v5` |
| PostgreSQL sürücüsü | `jackc/pgx/v5` (`pgxpool`), elle yazılmış SQL |
| Migrasyon | `pressly/goose/v3`, SQL dosyaları binary'ye gömülü |
| JWT / parola | `golang-jwt/jwt/v5` · `golang.org/x/crypto/argon2` |
| UUID | `google/uuid` (UUIDv7 — zaman sıralı, dizin dostu, keyset sayfalamaya uygun) |
| Ondalık ara hesap | `shopspring/decimal` (yalnız faiz/kur hesabında; saklanan tutar `int64`) |
| Girdi doğrulama | `go-playground/validator/v10` |
| Yapılandırma | `caarlos0/env/v11` |
| Metrik / trace | `prometheus/client_golang` · OpenTelemetry (`otelhttp`, OTLP) → Jaeger |
| Test | `testing` + `testify/require` · `testcontainers-go` (gerçek PostgreSQL 16) · `pgregory.net/rapid` (property) · Go fuzz |
| Lint / güvenlik | `golangci-lint` · `govulncheck` |
| Yük testi | k6 (Docker imajıyla, yerel kurulum gerekmez) |
| Frontend | React + Vite + TypeScript · React Router · TanStack Query · Mantine (hazır, mobil uyumlu bileşenler) |
| Konteyner | Go: `golang:1.27-alpine` → `distroless/static:nonroot` · Frontend: `node` → `nginx-unprivileged` · `postgres:16-alpine` |

---

## 5. Alan (domain) tasarımı

### 5.1 Hesap planı ve defter — T-1, F-1, F-2

**Hesaplar**
- Müşteri hesapları: `CHECKING` (vadesiz), `TIME_DEPOSIT` (vadeli: vade tarihi, faiz ürünü). Her hesap tek para birimi (TRY/USD/EUR).
- Banka iç hesapları (her para birimi için): `NOSTRO` (muhabir bankadaki paramız), `EXTERNAL_CLEARING` (giden transfer takası),
  `CARD_SETTLEMENT`, `INTEREST_EXPENSE`, `WITHHOLDING_TAX` (stopaj borcu), `FX_POSITION`, `SUSPENSE` (askı), `OPENING_EQUITY` (seed açılış bakiyeleri).

**Kayıt yapısı**
- `journal_entries` (bir iş olayı) + `postings` (satırlar). `amount_minor` işaretli: `+` borç, `−` alacak.
- **Borç = alacak:** her kayıtta, her para birimi için Σ `amount_minor` = 0. Go'da kayıt oluşturulurken ve DB'de
  `DEFERRABLE INITIALLY DEFERRED` constraint trigger ile commit anında kontrol edilir.
- **Bakiye türetilebilir:** Σ postings. Hızlı sorgu için `account_balances(balance_minor, held_minor)` özeti **aynı transaction'da** güncellenir.
  Hesabın `normal_side` alanı (borç/alacak) bakiyenin doğal işaretini belirler (müşteri hesabında pozitif = müşterinin parası).
- **DB güvencesi:** `CHECK (allow_negative OR balance_minor - held_minor >= 0)` — müşteri hesabı eksiye düşemez.
- **Kilit sırası:** işleme giren tüm hesapların özet satırları tek sorguda ID sırasıyla kilitlenir:
  `SELECT … FROM account_balances WHERE account_id = ANY($1) ORDER BY account_id FOR UPDATE` → döngüsel bekleme olmaz, deadlock olmaz.
- **Değişmezlik:** `journal_entries`, `postings`, `audit_log` üzerinde UPDATE/DELETE/TRUNCATE'i reddeden trigger
  + uygulama DB rolünde bu tablolarda yalnız `INSERT/SELECT` yetkisi. Düzeltme = ters kayıt (`reversal_of`, UNIQUE → aynı kayıt iki kez ters çevrilemez).
- **Değişmezler (invariant) ucu:** `GET /api/v1/admin/invariants` → Σ postings = 0 (para birimi başına), özet = türetilmiş bakiye,
  `held_minor` = Σ aktif provizyon. `make verify` ve kaos betikleri bunu kullanır.

### 5.2 Transfer durum makinesi ve saga — T-2, F-3

Türler: `INTERNAL` (banka içi havale), `EFT` ve `FAST` (dış banka). Şartname bunlar arasında kural farkı tanımlamadığı için
ikisi de aynı dış banka akışından geçer; tür, kayıtta ve ekstrede görünür.

```
CREATED ─► UNDER_REVIEW ─► PENDING_APPROVAL ─► (yürütme)
   │            │                 │
   └────────────┴─────────────────┴─────────► REJECTED

yürütme, iç:  ─► COMPLETED                          (tek transaction)
yürütme, dış: ─► RESERVED ─► SENT ─┬─► COMPLETED
                                    ├─► FAILED ─► REVERSED (ters kayıt)
                                    └─► UNKNOWN ─(sorgulama)─► COMPLETED | FAILED
```

- `UNDER_REVIEW`: risk kararı "inceleme"; `PENDING_APPROVAL`: tutar **10.000 TL'nin üzerinde** (TL karşılığı `> 10.000,00`), dört göz onayı bekler.
  Yetersiz bakiye yürütme anında kontrol edilir → `REJECTED` (`INSUFFICIENT_FUNDS`).
- Geçiş tablosu kodda tek yerde (`map[State][]State`). DB'de `UPDATE … WHERE id = $1 AND state = $beklenen` → yarışta tek kazanan.
- Aynı duruma tekrar gelen cevap (çift cevap) **sessizce yok sayılır**; geçersiz geçiş `409 INVALID_STATE_TRANSITION` ve loglanır.
- Her geçiş `transfer_events` tablosuna yazılır (geçmiş + denetim).
- **Defter kayıtları:** RESERVED: borç müşteri / alacak `EXTERNAL_CLEARING` · COMPLETED: borç `EXTERNAL_CLEARING` / alacak `NOSTRO` ·
  REVERSED: borç `EXTERNAL_CLEARING` / alacak müşteri · iç transfer: borç gönderen / alacak alıcı · gelen dış transfer: borç `NOSTRO` / alacak müşteri.
- **Outbox:** transfer RESERVED olurken aynı transaction'da `jobs` tablosuna `transfer.send` işi eklenir; worker `SKIP LOCKED` ile alır.
- Bankaya referans olarak **bizim transfer ID'miz** gider → yeniden gönderim güvenlidir (banka tarafında idempotent).
- `UNKNOWN`: artan aralıklarla sorgulama. Banka `COMPLETED` → tamam; `REJECTED`/`NOT_FOUND` → `FAILED` → ters kayıt.
  Sorgulama da başarısızsa yeniden denenir; limit aşılırsa transfer `UNKNOWN` kalır ve operasyon ekranında listelenir (para asla tahmine göre hareket etmez).

**Idempotency-Key** (`idempotency_keys`: anahtar + kullanıcı, istek özeti, yanıt durumu ve gövdesi)
- Anahtar satırı iş işlemiyle **aynı transaction'da** eklenir. Aynı anahtarla eşzamanlı ikinci istek, birincinin commit'ini bekler, sonra kayıtlı yanıtı döndürür.
- Aynı anahtar + farklı gövde → `422 IDEMPOTENCY_KEY_REUSED`.
- İş kuralı reddi de sonuçtur (ör. yetersiz bakiye → `REJECTED` transfer kaydı); tekrar istek aynı reddi döndürür.

### 5.3 Dış banka simülatörü protokolü

Şartname: dış servis **zaman aşımı, tekrar eden cevap ve belirsiz sonuç** üretir; **%20 hata, %10 çift cevap**.

- `POST /v1/transfers {reference, amount_minor, currency, beneficiary_iban}` — her çağrıda gecikme
  - **%20 hata:** zaman aşımı, işlenmeden dönen hata ve **işlendiği halde** dönen hata/zaman aşımı
    (belirsiz sonuç: "işlendi mi bilmiyoruz") arasında bölünür
  - **%10 çift cevap:** sonuç bildirimi iki kez gönderilir
  - Kalanı başarılı. Aynı referansla tekrar gönderim aynı sonucu verir.
- `GET /v1/transfers/{reference}` → `COMPLETED | REJECTED | NOT_FOUND` (gerçek durum; sorgulama akışı bunu kullanır)
- Sonuç bildirimleri API'ye `POST /api/v1/webhooks/bank/…` ile gelir. Simülatör **sistem** rolündeki hesapla giriş yapar;
  "her uç noktada RBAC" kuralı burada da geçerlidir.
- Gün sonu ekstre **dosyası** (CSV ve JSON) paylaşılan bir klasöre yazılır; bilerek bozuk ve tutarsız satırlar içerir.
- Oranlar ve gecikme ortam değişkeniyle ayarlanır; varsayılanlar şartnamedeki değerler. Aynı tohum = aynı davranış.

### 5.4 Kart provizyonu — T-3, F-4

- Kart ağını `simulator` servisi taklit eder (sistem rolü).
- **Provizyon:** risk → kullanılabilir bakiye kontrolü → `holds` (ACTIVE, tutar, kalan, `expires_at`) ve `held_minor += tutar`. Defter kaydı yok (bloke ≠ harcama).
- **Çekim (capture):** `tutar ≤ kalan`; defter: borç müşteri / alacak `CARD_SETTLEMENT`; `final=true` ise kalan blok serbest kalır.
- **İade:** `tutar ≤ çekilen − iade edilen`; defter: borç `CARD_SETTLEMENT` / alacak müşteri.
- **Süre aşımı:** worker, süresi dolan ACTIVE blokları `SKIP LOCKED` ile `EXPIRED` yapar ve `held_minor`'u düşer.
- **Kullanılabilir bakiye = `balance_minor − held_minor`**, her zaman O(1). Kilit sırası her yerde: önce hesap özeti, sonra provizyon satırı.

### 5.5 Kur — F-5

- `fx_rates(base, quote, rate numeric(18,8), valid_from)` — zaman geçerlilikli; operasyon uzmanı yönetir (kur yönetimi).
- Farklı para birimli transfer **tek** defter kaydıdır: borç gönderen (TRY) / alacak `FX_POSITION_TRY` + borç `FX_POSITION_USD` / alacak alıcı (USD).
  Her para biriminde Σ = 0. Kullanılan kur kayda yazılır; dönüşüm banker's rounding ile.
- 10.000 TL dört göz eşiği, diğer para birimlerinde güncel kurla TL karşılığına göre uygulanır.

### 5.6 Faiz ve gün sonu — T-4, F-6

- **Oranlar:** `interest_rates(ürün, para birimi, yöntem SIMPLE|COMPOUND, gün tabanı ACT_360|ACT_365|ACT_ACT, yıllık oran, valid_from)`.
  Çakışan geçerlilik aralıkları `EXCLUDE` kısıtıyla engellenir. Bir gün **her zaman o gün geçerli oranla** hesaplanır → oran değişince geçmiş etkilenmez.
- **Günlük brüt (hassas)** = esas × yıllık oran / gün tabanı. Basitte esas = anapara; bileşikte esas = anapara + birikmiş faiz.
  ACT_ACT'de artık yılda taban 366 (29 Şubat testleri).
- **Kuruş dağıtımı:** hassas kümülatif faiz saklanır; her gün yazılan = `round_half_even(kümülatif) − şimdiye kadar yazılan`
  → günlük tutarların toplamı her zaman yuvarlanmış toplam faize **tam** eşit.
- **Stopaj:** zaman geçerlilikli oran; `vergi = round_half_even(brüt × oran)`, `net = brüt − vergi` → brüt = net + vergi her zaman.
- **Kayıt:** borç `INTEREST_EXPENSE` (brüt) / alacak müşteri (net) / alacak `WITHHOLDING_TAX` (vergi).
- **Gün sonu işi (kaldığı yerden devam):** `eod_runs(business_date, adım, durum, last_account_id)`; hesaplar ID sırasıyla 1.000'lik
  partilerle, her parti tek transaction. `interest_accruals UNIQUE(account_id, accrual_date)` + defter kaydı aynı transaction'da →
  çökme sonrası yeniden çalıştırma çift tahakkuk yapamaz. Çökme testi: `EOD_CRASH_AFTER_BATCHES` ile yarıda öldür, yeniden başlat, toplamları karşılaştır.
- Gün sonu adımları: faiz tahakkuku → süresi dolan provizyonlar → ekstre çekme ve mutabakat → müşteri ekstreleri.

### 5.7 Mutabakat — T-5, F-8

- **Sınıflar:** `MATCHED`, `AMOUNT_MISMATCH`, `MISSING_IN_LEDGER` (ekstrede var, defterde yok), `MISSING_IN_STATEMENT` (defterde var, ekstrede yok),
  `DUPLICATE_IN_STATEMENT`, `INVALID_LINE` (bozuk satır).
- **Akış:** dosya yükle → akış halinde oku ve doğrula → `COPY` ile `recon_lines`'a → tek SQL'de `FULL OUTER JOIN` ile defterle eşleştir
  → `INSERT … SELECT` ile sonuçlar ve break'ler. Hedef: 100.000 satır < 10 sn.
- **Deterministik:** sınıflandırma yalnız veriye bağlı, tüm sıralamalar açık (`ORDER BY reference, line_no`); sonuç özeti SHA-256 ile raporlanır → aynı dosya aynı özet.
  Aynı dosya (checksum) aynı gün için tekrar yüklenirse mevcut sonuç döner.
- **Break akışı:** `OPEN → PROPOSED` (operasyon: açıklama + gerekirse `SUSPENSE`'e düzeltme kaydı önerisi) → `RESOLVED` (uyum onayı, **farklı kişi**) ya da tekrar `OPEN`.
- **Müşteri ekstresi:** dönem açılış/kapanış bakiyesi + hareketler (keyset sayfalı). PDF bonus.

### 5.8 Risk — T-6, F-7

- **Kural seti sürümlü:** `risk_rule_sets(version, rules jsonb, status DRAFT|ACTIVE|RETIRED, created_by, activated_by)`; tek ACTIVE (kısmi unique index).

```json
[
  {"id": "velocity_5m",      "type": "velocity",          "params": {"window_seconds": 300, "max_count": 10}, "action": "REVIEW"},
  {"id": "new_device_big",   "type": "new_device_amount", "params": {"min_amount_minor": 500000},           "action": "REVIEW"},
  {"id": "impossible_travel","type": "geo_velocity",      "params": {"max_kmh": 900},                       "action": "DECLINE"}
]
```

- **Motor:** tip → değerlendirici kayıt defteri (yeni tip = yeni kod, motor değişmez). Karar = en sert eylem (`DECLINE > REVIEW > APPROVE`).
  Aktif set bellekte; birkaç saniyede bir sürüm kontrolüyle yenilenir.
- **Girdiler:** son 5 dk işlem sayısı (dizinli sorgu), cihaz daha önce görüldü mü (`customer_devices`), son konum ve zaman.
- **Her değerlendirme kaydedilir:** `risk_evaluations` (girdi özellikleri, karar, sürüm, süre). Hız kuralı da bu tablodan sayar.
- **What-if:** kayıtlı girdiler aday sürümden yeniden geçirilir → eski/yeni karar sayıları ve değişen işlemler raporu.
- **p95 < 30 ms:** Prometheus histogramı + Go benchmark + k6 ile ölçülür, README'de dürüstçe raporlanır.
- **İnceleme kuyruğu:** transferde `REVIEW` → `UNDER_REVIEW`, risk analisti onaylar/reddeder. Kart provizyonunda `REVIEW` → onaylanır ama sonradan incelemeye düşer; `DECLINE` → red.

### 5.9 Zaman yolculuğu, dört göz, felaket kurtarma — T-7

- **İki zaman:** `effective_at` (geçerlilik/valör zamanı) ve `recorded_at` (kayıt zamanı, DB `now()`; istemci belirleyemez).
  Geriye tarihli düzeltme = yeni kayıt, `effective_at` geçmişte, `recorded_at` şimdi.
- **Sorgular:** "V anındaki bakiye, bugün bildiğimizle" → `effective_at ≤ V`; "V anındaki bakiye, T anında bildiğimizle" → `effective_at ≤ V AND recorded_at ≤ T`.
  `GET /api/v1/accounts/{id}/balance?as_of=V&known_at=T`, defter listesi de aynı parametrelerle.
- **Dört göz (10.000 TL üzeri transferler):** `approvals(requested_by, approved_by, CHECK (approved_by <> requested_by))`; onaylayan operasyon veya uyum rolünde olmalı.
- **Felaket kurtarma:** `archive_mode=on` + WAL arşivi; `scripts/dr/pitr-demo.sh`: temel yedek (`pg_basebackup`) + mantıksal yedek (`pg_dump`)
  → işaret kaydı A, zaman T → "hatalı" kayıt B → yedekten ayrı bir konteynere `recovery_target_time = T` ile geri dön → A var, B yok → PASS.
  README'de `pg_dump` (mantıksal, anlık görüntü) ile WAL tabanlı PITR (belirli bir ana dönüş) farkı anlatılır.

### 5.10 Roller ve yetki matrisi

Yetkiler izin bazlıdır (ör. `transfers:approve`); rol → izin eşlemesi kodda tek tablodadır. Uç nokta seviyesinde `Require(izin)`,
nesne seviyesinde (müşteri yalnız kendi hesabını görür) servis içinde kontrol edilir.

| İşlem | Müşteri | Operasyon | Risk | Uyum | Denetçi | Sistem |
|---|:-:|:-:|:-:|:-:|:-:|:-:|
| Kendi hesap, işlem, kart, ekstre | ✓ | | | | | |
| Müşteri ve hesap açma | | ✓ | | | | |
| Tüm hesap ve defteri okuma, zaman yolculuğu | | ✓ | ✓ | ✓ | ✓ | |
| Transfer başlatma | kendi | ✓ | | | | |
| Dört göz onayı (10.000 TL üzeri transfer) | | ✓ | | ✓ | | |
| Kur yönetimi | | ✓ | | | | |
| Risk kuralları, what-if | | | ✓ | | | |
| İnceleme kuyruğu kararı | | | ✓ | | | |
| Mutabakat yükleme, break çözüm önerisi | | ✓ | | | | |
| Break onayı | | | | ✓ | | |
| Denetim kaydı | | | | ✓ | ✓ | |
| Gün sonu tetikleme | | ✓ | | | | ✓ |
| Kart ağı çağrıları, banka bildirimleri | | | | | | ✓ |

Denetçi her yerde **salt okur**; hiçbir yazma ucuna erişemez.

---

## 6. Veritabanı

- **Kurallar:** `snake_case`, çoğul tablo adları · PK `uuid` (UUIDv7, uygulamada üretilir) · zamanlar `timestamptz` ·
  tutarlar `bigint` ve `_minor` soneki · para birimi `char(3)` + `CHECK` · durumlar `text` + `CHECK` (enum yerine, migrasyonu kolay).
- **İki DB rolü:** sahip rol (migrasyon) ve uygulama rolü (api/worker; defter ve denetim tablolarında yalnız `INSERT/SELECT`).
- **Tablolar:**
  - Kimlik: `users`, `refresh_tokens`
  - Müşteri: `customers`, `accounts`, `account_balances`, `cards`, `customer_devices`
  - Defter: `journal_entries`, `postings`
  - Transfer: `transfers`, `transfer_events`, `approvals`
  - Kart: `holds`, `captures`, `refunds`
  - Kur: `fx_rates`
  - Faiz: `interest_rates`, `tax_rates`, `interest_accruals`, `eod_runs`
  - Mutabakat: `recon_imports`, `recon_lines`, `recon_results`, `recon_breaks`
  - Risk: `risk_rule_sets`, `risk_evaluations`, `risk_reviews`
  - Altyapı: `idempotency_keys`, `audit_log`, `jobs`
- **Kritik dizinler** (README'de dizinli/dizinsiz `EXPLAIN (ANALYZE)`):
  `postings(account_id, effective_at)` — bakiye, ekstre, zaman yolculuğu ·
  `risk_evaluations(customer_id, evaluated_at)` — hız kuralı ·
  `jobs(run_at) WHERE status = 'queued'` — iş kuyruğu ·
  `recon_lines(import_id, reference)` — mutabakat eşleştirmesi.
- **Denetim kaydı:** `audit_log(actor_id, actor_role, at, action, entity, entity_id, old_value, new_value, request_id)` —
  şartnamedeki "kim, ne zaman, neyi, eski/yeni değer"; kritik işlemle aynı transaction'da yazılır.
- **Seed (iki parça):**
  - `api` açılışında temel seed: her rol için demo kullanıcı, banka iç hesapları, başlangıç kur/faiz/stopaj oranları, varsayılan risk kural seti.
  - `simulator` açılışında: 20.000 müşteri ve hesapları; tohumla deterministik, `COPY` ile toplu (aynı tohumla yapılmışsa atlanır).
  - Açılış bakiyeleri de defterden geçer (borç `OPENING_EQUITY` / alacak müşteri), yani seed sonrası da Σ = 0.
- **Demo hesaplar:** `musteri@parakopru.test`, `operasyon@parakopru.test`, `risk@parakopru.test`, `uyum@parakopru.test`,
  `denetci@parakopru.test`, `sistem@parakopru.test` — parola `.env.example`'da ve README'de.

---

## 7. API

**Kurallar**
- Taban yol `/api/v1`; JSON alanları `snake_case`; tutarlar her zaman `amount_minor` (tam sayı) + `currency`, asla ondalıklı sayı.
- Hata: `{"error":{"code":"INSUFFICIENT_FUNDS","message":"…"}}`. Durum kodları: 200/201/202 · 400 doğrulama · 401 · 403 · 404 ·
  409 çakışma/geçersiz geçiş · 422 iş kuralı · 500 · 503 (sağlıksız).
- Liste uçları keyset sayfalı: `?limit=50&cursor=…` → `{"items":[…],"next_cursor":"…"}`.
- Para hareketi yapan her `POST` `Idempotency-Key` ister (yoksa 400).
- Her yanıtta `X-Request-ID`. Belgeler: `GET /openapi.yaml` ve `GET /docs`.

**Uç noktalar (taslak)**

```
GET  /health · /metrics · /openapi.yaml · /docs
POST /api/v1/auth/login · /auth/refresh · /auth/logout         GET /api/v1/me

GET  POST /api/v1/customers
GET  POST /api/v1/accounts            GET /api/v1/accounts/{id}
GET  /api/v1/accounts/{id}/balance?as_of=&known_at=
GET  /api/v1/accounts/{id}/entries?from=&to=&known_at=&cursor=
GET  /api/v1/accounts/{id}/statement?from=&to=
GET  /api/v1/accounts/{id}/holds

POST /api/v1/transfers                               [Idempotency-Key]
GET  /api/v1/transfers · /transfers/{id}
POST /api/v1/transfers/{id}/approve · /reject        [Idempotency-Key]
POST /api/v1/webhooks/bank/transfer-status · /webhooks/bank/incoming   (sistem rolü)

POST /api/v1/cards/{id}/authorizations               [Idempotency-Key]
POST /api/v1/holds/{id}/captures                     [Idempotency-Key]
POST /api/v1/captures/{id}/refunds                   [Idempotency-Key]

GET  POST /api/v1/fx/rates
POST /api/v1/ledger/entries/{id}/reverse             [Idempotency-Key]
POST /api/v1/eod/runs                 GET /api/v1/eod/runs/{date}

POST /api/v1/recon/imports            GET /api/v1/recon/imports/{id}
GET  /api/v1/recon/breaks             POST /recon/breaks/{id}/propose · /approve · /reject

GET  POST /api/v1/risk/rule-sets      POST /risk/rule-sets/{v}/activate · /what-if
GET  /api/v1/risk/reviews             POST /risk/reviews/{id}/decide

GET  /api/v1/audit-log · /admin/invariants · /ops/stats
```

`/admin/invariants`, T-1 kanıtını API üzerinden vermek için vardır (verify ve kaos betikleri kullanır).
`/ops/stats` ise şartnamedeki "arayüzde sistemin canlı olduğu görülebilmeli" maddesi için.

---

## 8. Frontend

- React + Vite + TypeScript; derlenip `nginx-unprivileged` ile `:3000`'de sunulur; `/api` istekleri nginx üzerinden `api:8080`'e gider (aynı köken, CORS yok).
- TanStack Query (sunucu verisi, önbellek, yeniden deneme), React Router, Mantine (`AppShell` ile mobil menü, tablo, form, bildirim).
- **Oturum:** access token bellekte; refresh token `HttpOnly` + `SameSite=Strict` çerezde (JavaScript okuyamaz → XSS'e karşı daha güvenli).
  401'de bir kez sessizce yenileme, olmazsa giriş ekranı.
- Ortak `Loading` / `Empty` / `ErrorState` bileşenleri her ekranda kullanılır.
- Menü role göre süzülür (asıl koruma sunucuda).
- **Ekranlar:**
  - Müşteri: hesaplarım (bakiye + kullanılabilir), hesap hareketleri/ekstre, transfer (her gönderimde yeni Idempotency-Key), kartlar ve provizyonlar
  - Operasyon: müşteriler/hesaplar, transferler + onay kuyruğu, UNKNOWN transferler, mutabakat (yükle, break'ler), gün sonu, kurlar, **canlı işlem akışı** (birkaç saniyede bir yenilenir)
  - Risk: kural setleri (JSON düzenleyici, sürümler, aktifleştir), what-if raporu, inceleme kuyruğu
  - Uyum: onaylar (transfer, break), denetim kaydı
  - Denetçi: zaman yolculuğu (hesap + V + T seç → bakiye ve defter), denetim kaydı

---

## 9. Test ve kanıt stratejisi

**Test türleri**
- **Birim:** saf fonksiyonlar (para, faiz, durum makinesi, risk değerlendiricileri, ekstre ayrıştırıcı) — tablo tabanlı testler.
- **Entegrasyon:** `testcontainers-go` ile gerçek PostgreSQL 16; migrasyonlar uygulanmış, paket başına bir konteyner.
- **Property:** `rapid` — kuruş dağıtımı ve brüt = net + vergi; rastgele işlem dizilerinde defter dengesi.
- **Fuzz:** `FuzzParseStatementCSV`, `FuzzParseAmount` (çökme yok, geçersiz girdi hata döner).
- **Uçtan uca:** `cmd/verify` — ayaktaki sisteme API üzerinden.

| Kimlik | Kanıt |
|---|---|
| T-1 | 10.000 eşzamanlı transfer (rastgele hesap çiftleri, ters yönler dahil) → Σ değişmez, özet = türetilmiş, deadlock yok · property test |
| T-2 | Aynı anahtar → aynı yanıt (eşzamanlı dahil) · farklı gövde → 422 · geçersiz geçiş reddi · hata üreten banka simülatörüyle her transfer `COMPLETED` ya da `REVERSED`, `EXTERNAL_CLEARING` uçuştakiler dışında 0 |
| T-3 | Eşzamanlı provizyon/çekim/iade yarışı → `held_minor` = Σ aktif blok, kullanılabilir ≥ 0, süre aşımı serbest bırakır |
| T-4 | 360/365/artık yıl tabloları · oran değişimi geçmişi bozmaz · property: Σ günlük = yuvarlanmış toplam · yarıda öldür → yeniden başlat → çift tahakkuk yok |
| T-5 | 100.000 satır < 10 sn ölçümü · aynı dosya iki kez → aynı SHA-256 · her sınıf için örnek satır |
| T-6 | Benchmark + histogram ile p95 · sürüm aktifleştirme · what-if raporu farkları |
| T-7 | `as_of`/`known_at` testleri · UPDATE/DELETE reddi · onaylayan = isteyen reddi · `make pitr-demo` |

- Hedefler: ≥ 60 test, domain paketlerinde kapsam ≥ %75 (`make cover` raporlar).
- `make verify`: önce `verify` konteyneri her kimliği API üzerinden dener ve frontend'in yanıt verdiğini kontrol eder
  (şartname "API ve mümkünse arayüz" diyor); sonra T-7'nin felaket kurtarma kısmı için PITR betiği çalışır.
  T-7 satırı ikisi de geçerse PASS. Toplam süre ≤ 15 dk; uzun yük testi ayrı (`make load`).

---

## 10. Docker, Compose, Makefile, CI

- **Go imajı:** tek multi-stage `backend/Dockerfile`; bütün binary'ler (`api`, `worker`, `simulator`, `verify`, `healthcheck`)
  `CGO_ENABLED=0` ile derlenir; son aşama `distroless/static:nonroot`. Compose servisleri aynı imajı farklı komutla çalıştırır.
- **Healthcheck:** distroless'ta curl yok → küçük `healthcheck` binary'si (`/healthcheck http://localhost:8080/health`).
  Worker ve simulator de kendi `/health` uçlarını açar; simulator seed bitince healthy olur.
- **Frontend imajı:** `node` ile derle → `nginx-unprivileged` ile sun (container içi 8080, host 3000).
- Tüm `depends_on`'lar `service_healthy`. `postgres` da root olmadan (`user: postgres`) çalışır.
- **Makefile:** `up`, `down`, `logs`, `verify`, `load`, `reset-demo` (`down -v` + `up --wait`), `test`, `test-race`, `cover`, `lint`,
  `chaos-db-restart`, `chaos-bank-latency`, `pitr-demo`.
- **Kaos:** (1) trafik akarken `postgres` yeniden başlatılır → API kısa süre 503, sonra toparlanır, değişmezler bozulmaz;
  (2) dış banka simülatörü dondurulur (`docker compose pause`) ya da yüksek gecikmeyle çalıştırılır → transferler `UNKNOWN` olur, sorgulamayla çözülür, para kaybolmaz.
- **CI (GitHub Actions):** Go build · `go test -race` (testcontainers) · `golangci-lint` · `govulncheck` · frontend lint + build · Docker imajlarının derlenmesi.

---

## 11. Klasör yapısı

```
backend/
  go.mod
  cmd/
    api/  worker/  simulator/  verify/  healthcheck/
  internal/
    platform/      config · db (TxManager, DBTX, migrasyon) · httpx (hata, JSON, sayfalama) · auth (jwt, argon2, rbac)
                   audit · idempotency · jobs · clock · ids · observability (log, metrik, trace)
    money/         Amount, Currency, yuvarlama
    customer/  account/  ledger/  transfer/  card/  fx/  interest/  recon/  risk/
                   her modülde: model.go · service.go · repo.go · handler.go · *_test.go
    sim/           tohumlu veri üretici · dış banka sahtesi · ekstre üretici
  migrations/      goose SQL dosyaları
  api/openapi.yaml
  Dockerfile
frontend/          src/{api,auth,components,pages}  Dockerfile  nginx.conf
deploy/postgres/   WAL arşivi ayarları, rol oluşturma betiği
scripts/           chaos/  dr/
loadtest/          k6 senaryoları
.github/workflows/ci.yml
docker-compose.yml  Makefile  .env.example
README.md  DECISIONS.md  RUNBOOK.md  TESTING.md  requests.http
```

---

## 12. Adımlar (sırayla)

### A — İskelet ve altyapı
- [x] A1 `chore`: depo iskeleti, `.gitignore`, `.env.example`, `Makefile` iskeleti
- [x] A2 `feat(platform)`: Go modülü, config, slog JSON log, `apperr` + `httpx` (hata biçimi, JSON, sayfalama), clock, ids
- [x] A3 `feat(api)`: chi router, istek middleware'leri (request id, log, recover, zaman aşımı), `GET /health` (DB dahil), graceful shutdown
- [x] A4 `feat(db)`: `TxManager` (yeniden denemeli), goose ile ilk migrasyonlar, iki DB rolü, `api` açılışında advisory lock ile migrasyon
- [x] A5 `build`: Go ve frontend Dockerfile'ları, `healthcheck` binary'si, compose (her serviste healthcheck, tüm bağımlılıklar `service_healthy`, root olmayan kullanıcılar), boş frontend sayfası
- [x] A6 `feat(auth)`: `users`, 6 rol, argon2id, JWT access + refresh çerezi, izin tablosu ve `Require` middleware, demo hesap seed'i
- [x] A7 `feat(platform)`: append-only `audit_log`, `idempotency_keys`, `jobs` kuyruğu + `worker` iskeleti
- [x] A8 `test(verify)`: `cmd/verify` iskeleti + PASS/FAIL tablosu (health, yanlış parola, yetkisiz rol, yetki ihlali), `make verify`

### B — Para ve defter (T-1, F-1, F-2)
- [x] B1 `feat(money)`: `Amount`, para birimi, banker's rounding + birim/property testleri
- [x] B2 `feat(account)`: müşteriler, hesaplar (vadesiz/vadeli, TRY/USD/EUR), iç hesap planı, `account_balances`
- [x] B3 `feat(ledger)`: yevmiye + satırlar, borç = alacak (Go + DB), değişmezlik trigger'ları, özet güncelleme, sıralı kilit, ters kayıt
- [x] B4 `feat(transfer)`: iç transfer + Idempotency-Key, `/admin/invariants`
- [x] B5 `test(ledger)`: 10.000 eşzamanlı transfer para korunumu testi + property test
- [x] B6 `feat(sim)`: `simulator` servisi + tohumlu seed (20.000 müşteri, açılış bakiyeleri defterden) + verify'a F-1, F-2, T-1 kontrolleri

### C — Dış transfer ve saga (T-2, F-3)
- [x] C1 `feat(sim)`: simülatöre dış banka (gecikme, zaman aşımı, %20 hata, %10 çift cevap, belirsiz sonuç, sorgulama)
- [x] C2 `feat(transfer)`: durum makinesi, outbox, worker'da gönderim/sorgulama, banka bildirimleri (sistem rolü), ters kayıt
- [x] C3 `test(transfer)`: idempotency (eşzamanlı dahil), geçersiz geçiş, belirsiz sonuç ve telafi testleri + verify'a F-3, T-2

### D — İlk arayüz
- [x] D1 `feat(frontend)`: giriş, rol bazlı menü, hesaplarım, hareketler, transfer; ortak yükleniyor/boş/hata bileşenleri, mobil uyum

### E — Kart provizyonu (T-3, F-4)
- [x] E1 `feat(card)`: kartlar, provizyon, kısmi/final çekim, iade, süre aşımı işi
- [ ] E2 `test(card)`: eşzamanlı yarış testleri + verify'a F-4, T-3

### F — Zaman yolculuğu, dört göz, felaket kurtarma (T-7)
- [ ] F1 `feat(ledger)`: `effective_at` / `recorded_at`, `as_of` + `known_at` bakiye ve defter sorguları
- [ ] F2 `feat(transfer)`: 10.000 TL üzeri transferlerde dört göz onayı
- [ ] F3 `feat(dr)`: WAL arşivi, `pg_dump`, PITR betiği (`make pitr-demo`; `make verify` de çalıştırır) + verify'a T-7

### G — Kur (F-5)
- [ ] G1 `feat(fx)`: zaman geçerlilikli kurlar ve kur yönetimi (operasyon), çok para birimli transfer, TL eşdeğeri eşik + verify'a F-5

### H — Faiz ve gün sonu (T-4, F-6)
- [ ] H1 `feat(interest)`: oran/stopaj tabloları, gün tabanları, basit/bileşik, kümülatif yuvarlama (saf fonksiyonlar + testler)
- [ ] H2 `feat(interest)`: kaldığı yerden devam eden gün sonu işi, çökme testi + verify'a F-6, T-4

### I — Mutabakat (T-5, F-8)
- [ ] I1 `feat(sim)`: tohumlu gün sonu ekstre dosyası (CSV/JSON, bilerek bozuk ve tutarsız satırlar)
- [ ] I2 `feat(recon)`: yükleme, `COPY`, eşleştirme, break'ler, öneri/onay akışı, müşteri ekstresi
- [ ] I3 `perf(recon)`: 100.000 satır ölçümü, determinizm testi, fuzz + verify'a F-8, T-5

### J — Risk (T-6, F-7)
- [ ] J1 `feat(risk)`: sürümlü kural setleri, değerlendirici kayıt defteri, karar, transfer ve karta bağlama, inceleme kuyruğu
- [ ] J2 `feat(risk)`: what-if raporu, p95 ölçümü (histogram + benchmark) + verify'a F-7, T-6

### K — Simülatör trafiği ve arayüzün tamamı
- [ ] K1 `feat(sim)`: işlem trafiği — günde ~200.000 işlem hızında transferler, kart provizyon/çekim/iade, gelen transferler
- [ ] K2 `feat(frontend)`: operasyon, risk, uyum, denetçi ekranları; canlı işlem akışı

### L — Kalite
- [x] L1 `ci`: GitHub Actions (build, test, lint, govulncheck, imajlar)
- [ ] L2 `feat(obs)`: OpenAPI + `/docs`, `/metrics`, transfer akışında api → worker → simulator OpenTelemetry trace (Jaeger)
- [ ] L3 `test(load)`: k6 senaryoları, `make load`, SLO tablosu
- [ ] L4 `test(chaos)`: iki kaos betiği + sonuçları
- [ ] L5 `test`: eksik testler — ≥ 60 test, kapsam ≥ %75
- [ ] L6 `test`: soğuk başlangıç ölçümü — Docker önbelleği boşaltılıp (`docker builder prune -af`) `docker compose up --build --wait` süresi ölçülür; hedef ≤ 5 dk, sonuç README'ye

### M — Doküman
- [ ] M1 `docs`: README (ad, kurulum, mimari, kararlar, frontend gerekçesi, demo hesaplar, DR, `EXPLAIN (ANALYZE)`, Doğrulama Soruları)
- [ ] M2 `docs`: DECISIONS.md, RUNBOOK.md (yedek/geri yükleme, expand → migrate → contract örneği)
- [ ] M3 `docs`: TESTING.md (demo hesaplar, F/T tablosu, kanıt komutları, arayüz senaryoları), `requests.http`, `make reset-demo` kontrolü

### N — Bonus (şartnamede "zorunlu değil"; yalnız zorunlu her şey bittiyse)
- [ ] N1 müşteri ekstresi PDF'i
- [ ] N2 operasyon panelinde canlı işlem hacmi ve hata oranı
- [ ] N3 kur değişiminde değerleme (revaluation) kayıtları

### Teslim
- [ ] Depo public; README'nin başında ad; anlamlı commit geçmişi; teslimden sonra force push / silme / private yok
- [ ] Depo bağlantısı `proje.md`'deki ankete yüklenir

Yetişmeyen adım README'de dürüstçe "yapılmadı, nedeni şu" diye yazılır.

### README Doğrulama Soruları için geliştirme boyunca tutulan notlar

Bu soruların cevabı sona bırakılırsa unutulur; ilgili şey yaşandığı anda buraya eklenir.

1. **En zorlanılan görev ve elenen alternatifler:** (geliştirme sırasında doldurulacak)
2. **Bilinen sınırlamalar:** (geliştirme sırasında doldurulacak)
   - Dört göz onayı ve risk incelemesi beklerken para bloke edilmez; yetersiz bakiye yürütme anında kontrol edilir.
3. **Dizin ölçümleri (dizinsiz / dizinli):** (ilgili adımda `EXPLAIN (ANALYZE)` ile ölçülüp yazılacak)
4. **Reddedilen ya da düzeltilen çıktılar (önce → sonra):**
   - Dört göz eşiği ilk taslakta `≥ 10.000 TL` yazılmıştı; şartname "10.000 TL **üzeri**" diyor → `> 10.000 TL`.
   - İlk taslakta migrasyon, `service_completed_successfully` ile beklenen tek seferlik bir servisti; şartname tüm `depends_on`'larda
     `service_healthy` istiyor → migrasyon `api` açılışına (advisory lock ile) taşındı.
   - İlk taslakta dış banka simülatörüne şartnamede olmayan ek bir "%10 kesin red" oranı konmuştu; şartname "%20 hata, %10 çift cevap" diyor → kaldırıldı.
5. **T-1 kanıtı:** B5'teki testin komutu ve çıktısı buraya kopyalanacak.

---

## 13. Commit kuralı ve "bitti" tanımı

Biçim: `tür(kapsam): ne yapıldığı` — açıklama Türkçe, emir kipinde.

```
feat(ledger): çift taraflı defter tablolarını ve borç=alacak kontrolünü ekle
test(ledger): 10.000 eşzamanlı transfer için para korunumu testi ekle
fix(transfer): kilitleri hesap ID sırasıyla alarak deadlock'u gider
docs(readme): kurulum adımlarını ve mimari özeti yaz
```

Türler: `feat` `fix` `test` `docs` `refactor` `perf` `build` `ci` `chore`. Bir commit = bir mantıksal değişiklik.

**Bir adım "bitti" sayılır, eğer:**
1. Derleniyor, `gofmt`/lint temiz, o ana kadarki tüm testler geçiyor.
2. İlgili F/T kimliği varsa `cmd/verify`'a kontrolü eklendi.
3. Alınan yeni karar varsa `DECISIONS.md`'ye, deneme yolu varsa `TESTING.md`'ye eklendi.
4. Yeni uç nokta varsa `api/openapi.yaml`'a ve `requests.http`'ye eklendi (şartname "tüm uç noktalar" istiyor; sona bırakılmaz).
5. Doğrulama Soruları için not çıktıysa 12. bölümdeki not listesine eklendi.
6. Bu dosyadaki kutusu işaretlendi.

---

## 14. DECISIONS.md'ye girecek kararlar

Biçim (her kayıt): **Bağlam · Karar · Değerlendirilen alternatifler · Sonuçlar/ödünleşimler**.

1. Modüler monolit, modül içi katmanlı (mikroservis yerine)
2. `api` ve `worker` aynı imajdan ayrı süreçler; migrasyon `api` açılışında advisory lock ile (tüm bağımlılıklar `service_healthy` kalsın diye)
3. chi + `net/http` (gin/echo yerine)
4. pgx + elle SQL (ORM ve sqlc yerine)
5. Framework'süz constructor injection (wire/fx yerine)
6. Tutarlar `bigint` kuruş; ara hesap `decimal`, banker's rounding
7. İşaretli satırlı çift taraflı defter + aynı transaction'da bakiye özeti
8. Deadlock önleme: hesap ID sırasıyla toplu `FOR UPDATE` (SERIALIZABLE yerine)
9. Idempotency anahtarının iş işlemiyle aynı transaction'da saklanması
10. PostgreSQL tabanlı iş kuyruğu + outbox (Redis/Kafka yerine)
11. Transfer sagası: açık durum makinesi, bizim ID'mizle idempotent dış çağrı, belirsizlikte sorgula-sonra-telafi et
12. İki zamanlı defter (`effective_at` / `recorded_at`) ve trigger + DB yetkisiyle değişmezlik
13. Faizde kümülatif yuvarlama ile kuruş dağıtımı; çift tahakkuku UNIQUE kısıtla engelleme
14. Mutabakatta `COPY` + set tabanlı SQL eşleştirme (uygulamada döngü yerine)
15. Risk: sürümlü JSON kurallar, bellekte değerlendirme, girdilerin kaydedilip what-if için yeniden oynatılması
16. JWT access (bellekte) + refresh (`HttpOnly` çerez)
17. Frontend: React + Vite + Mantine, nginx proxy (Next.js yerine)
