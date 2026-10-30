// Package migrations, veritabanı şemasının goose SQL migrasyonlarını içerir.
//
// Dosyalar binary'ye gömülür; api açılışında db.Migrate ile uygulanır. Ayrı bir migrasyon aracı
// ya da elle adım gerekmez (proje.md §3: migrasyon compose ile otomatik çalışmalı).
//
// Kurallar: dosya adı NNNNN_açıklama.sql; uygulanmış bir dosya değiştirilmez, düzeltme yeni dosyayla yapılır.
package migrations

import "embed"

// FS gömülü migrasyon dosyalarıdır.
//
//go:embed *.sql
var FS embed.FS
