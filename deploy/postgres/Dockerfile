# PostgreSQL 16 + WAL arşiv klasörü (felaket kurtarma, proje.md Görev 7). Klasör imajda postgres kullanıcısına aittir;
# üzerine bağlanan adlandırılmış volume bu sahipliği devralır, böylece postgres root olmadan arşive yazabilir.
FROM postgres:16-alpine
RUN mkdir -p /wal_archive && chown postgres:postgres /wal_archive
