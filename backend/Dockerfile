# syntax=docker/dockerfile:1

# Tüm Go servisleri (api, worker, simulator, verify) ve healthcheck aynı imajdan çalışır;
# compose her servis için farklı komut verir.

# --- 1. aşama: derleme ---
FROM golang:1.27-alpine AS build
WORKDIR /src

# Ayrı bir `go mod download` adımı yok: o adım yalnız testlerin kullandığı modülleri (testcontainers vb.)
# de indirir. `go build` yalnız binary'lerin ihtiyaç duyduğunu indirir; soğuk başlangıç kısalır (hedef ≤ 5 dk).
COPY . .
# CGO kapalı: binary'ler statik olur, C kütüphanesi olmayan distroless imajda çalışır.
# -trimpath ve -s -w: yerel dosya yolları ve hata ayıklama bilgisi binary'ye girmez, boyut küçülür.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

# --- 2. aşama: çalışma imajı ---
# distroless/static: yalnız CA sertifikaları ve saat dilimi verisi; kabuk ve paket yöneticisi yok.
# :nonroot etiketi süreci 65532 numaralı root olmayan kullanıcıyla çalıştırır.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/ /app/
USER nonroot:nonroot
EXPOSE 8080
CMD ["/app/api"]
