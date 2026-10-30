# ParaKöprü — Test ve Doğrulama Rehberi

Bu dosya proje.md §10'un istediği bölümleri içerir:

- (a) her rol için demo hesap
- (b) her doğrulama kimliği için "nasıl denenir / beklenen sonuç" tablosu
- (c) arayüzde elle denenecek senaryolar
- her zorlu görev için kanıt komutu

Bölümler, ilgili özellik geliştirildikçe doldurulur.

## Demo hesaplar

Kimlik doğrulama adımında (A6) eklenecek.

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

## Doğrulama kimlikleri (F-1 … F-8, T-1 … T-7)

Her kimlik, ilgili özellik geliştirildiğinde bu tabloya ve `make verify`'a eklenecek.

## Arayüz senaryoları

İlk arayüz adımında (D1) eklenecek.
