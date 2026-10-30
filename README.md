# ParaKöprü — Dijital Banka Çekirdeği ve Mutabakat Sistemi

**Geliştiren:** İsmail Beren Topuçar

DDO Yazılım Ekibi alım projesi: müşteri hesapları, çift taraflı defter, transferler, kart provizyonları,
faiz tahakkuku, risk kuralları ve gün sonu mutabakatı. Go + PostgreSQL 16 + React; Docker Compose ile tek komutla çalışır.

> Proje geliştirme aşamasında. Mimari, tasarım kararları ve iş adımları [PLAN.md](PLAN.md) dosyasında.

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

Her özelliği çalışan sistem üzerinde uçtan uca denemek için `make verify` (ayrıntılar: [TESTING.md](TESTING.md)).
Sık kullanılan komutlar için `make help`.

## Demo hesaplar

Her rol için bir hesap, sistem ilk açıldığında otomatik oluşturulur. Hepsinin parolası `.env`'deki
`DEMO_PASSWORD`'dır (varsayılan **`ParaKopru.2026`**).

| Rol | E-posta |
|---|---|
| Müşteri | `musteri@parakopru.test` |
| Operasyon uzmanı | `operasyon@parakopru.test` |
| Risk analisti | `risk@parakopru.test` |
| Uyum (compliance) uzmanı | `uyum@parakopru.test` |
| Denetçi | `denetci@parakopru.test` |
| Sistem (zamanlanmış işler) | `sistem@parakopru.test` |
