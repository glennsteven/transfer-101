# Pemeriksaan tahap 2: idempotency dan dbmate

Tanggal: 20 Oktober 2026. Host Go 1.26.2, darwin/arm64. Migrasi memakai dbmate 2.28.0. PostgreSQL integrasi: `17.11 (Debian 17.11-1.pgdg12+2)` di container `transfer-101-postgres-test-1`, database/role `transfer_test`.

## File untuk review

- `internal/transfer/migrations/001_transfers.sql` diganti menjadi `20261020000100_create_transfers.sql`: dbmate up/down, tanpa COLLATE, CREATE/DROP defensif.
- `internal/transfer/migrate.go` dihapus; `internal/transfer/schema.go` memeriksa versi dbmate dan kolom saat startup.
- `cmd/migrate/main.go` dan `main_test.go` dihapus bersama runner khusus.
- `internal/testdb/migrate.go` ditambahkan untuk menjalankan dbmate asli pada fixture terisolasi.
- `internal/transfer/postgres_integration_test.go` dan `cmd/api/postgres_integration_test.go` memakai dbmate; test migrasi diperbarui.
- `scripts/build-container-tests.sh` dan `scripts/run-container-tests.sh` menyiapkan serta memeriksa executable dbmate, SQL, dan binary test.
- `README.md`, `docs/design.md`, dan dokumen ini memperbarui command, keputusan metadata, hasil verifikasi, serta keterbatasan.
- `bin/integration/` berisi artefak build/log lokal yang diabaikan Git. `.env`, database demo, dan dependency Go aplikasi tidak diubah.

## Setup yang dapat diulang

| Penggunaan | Service | Database/role | Port host | Storage |
| --- | --- | --- | --- | --- |
| Demo | `postgres` | `transfer_demo` | 54329 | Named volume demo yang sudah ada. |
| Test | `postgres-test` | `transfer_test` | 54330 | tmpfs khusus test. |

Compose menyiapkan database dan role test melalui entrypoint resmi PostgreSQL. Tidak ada langkah CREATE DATABASE atau SQL manual. Semua fixture memakai `internal/testdb.Open`: URL dibatasi ke database/role `transfer_test`, connection override ditolak, lalu database/role aktual diperiksa sebelum DDL.

Fixture transfer/API membuat schema acak sendiri dan menjalankan **CLI dbmate asli**, dengan search_path hanya schema tersebut. Helper membatasi target URL dan nama schema fixture, menonaktifkan pembacaan `.env` serta dump schema, dan memberi timeout proses 20 detik. SQL diambil dari `internal/transfer/migrations`, tanpa runner/SQL parser Go buatan sendiri. Cleanup hanya menghapus schema fixture. Health test hanya memerlukan koneksi database.

Semua command memakai tabel metadata `dbmate_schema_migrations`. Tabel `schema_migrations` lama memiliki checksum wajib dan format versi berbeda; memisahkan metadata memungkinkan adopsi dbmate tanpa reset, penghapusan histori, atau perubahan metadata lama. API memeriksa versi dbmate dan kolom transfer yang dibutuhkan. dbmate tidak melakukan pemeriksaan checksum file.

## Native dengan Go dan dbmate

Jalankan dari root repository. Gunakan dbmate 2.28.0 di PATH, atau set `DBMATE_BIN` ke path absolut. Jika binary test dijalankan di lokasi lain, set `DBMATE_MIGRATIONS_DIR` ke direktori SQL absolut.

```sh
dbmate --version
docker compose --profile test up -d --wait postgres-test
env 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:54330/transfer_test?sslmode=disable&connect_timeout=3' go test -race -tags=integration -count=1 -timeout=120s -v ./...
```

Kutip seluruh argumen `NAME=value` untuk `env` dan `docker compose exec -e`. Dengan single quote, `?` dan `&` tidak ditafsirkan sebagai glob/operator zsh. Jangan menyalin DATABASE_URL demo ke TEST_DATABASE_URL. Native combined race+PostgreSQL belum dijalankan di sesi agent yang membatasi socket lokal; hasil container di bawah terpisah dari unit race native.

## Container tujuan tanpa Go terpasang

Host membutuhkan Go, curl, dan shasum. Tentukan arsitektur container:

```sh
docker compose --profile test up -d --wait postgres-test
docker compose exec -T postgres-test uname -m
```

Hasil sesi ini `aarch64`: gunakan `arm64`. Untuk `x86_64`, gunakan `amd64`.

```sh
sh scripts/build-container-tests.sh arm64
docker compose exec -T postgres-test mkdir -p /tmp/transfer-101-tests
docker compose cp bin/integration/. postgres-test:/tmp/transfer-101-tests
docker compose exec -T -e 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:5432/transfer_test?sslmode=disable&connect_timeout=3' postgres-test sh /tmp/transfer-101-tests/run-container-tests.sh /tmp/transfer-101-tests
```

Build script menyiapkan dbmate Linux 2.28.0 dari [rilis resmi](https://github.com/amacneil/dbmate/releases/tag/v2.28.0), memverifikasi SHA-256 yang dipin untuk arm64/amd64, menyalin SQL, dan membangun transfer.test, health.test, api.test. Artefak tersedia pada `bin/integration` dan diabaikan Git. Runner memeriksa dbmate dapat dieksekusi, SQL tersedia, serta setiap binary executable dan memuat test PostgreSQL sebelum menjalankan suite. PostgreSQL image tidak memerlukan Go, curl, atau dbmate yang dipasang terpisah.

Pada sesi ini release arm64 diunduh dengan curl, lalu build script memverifikasi cache binary tersebut. Cabang download script belum dijalankan langsung. Build memakai `GOCACHE=/tmp/transfer-101-go-build GOPROXY=off` karena cache default dibatasi dan module sudah tersedia; konfigurasi Go global tidak diubah. Binary Linux memakai CGO_ENABLED=0, tanpa race detector.

## Hasil aktual sesudah perubahan dbmate

| Pemeriksaan | Hasil |
| --- | --- |
| `gofmt -l cmd internal` | Exit 0, tidak ada file yang perlu diformat. |
| `go mod verify` | Exit 0, seluruh module terverifikasi. |
| `go build ./...` | Exit 0 dengan GOCACHE sementara. |
| `go vet -tags=integration ./...` | Exit 0 dengan GOCACHE sementara. |
| `go test -race -count=1 -timeout=60s ./...` | Exit 0, empat package lulus. |
| `docker compose config --quiet` | Exit 0. |
| `sh -n` kedua script | Exit 0. |
| Build test Linux/arm64 | Exit 0, tiga binary dan dbmate tersedia. |
| Preflight di container | dbmate 2.28.0 berjalan; tiga binary dapat dieksekusi dan memuat test PostgreSQL. |
| Runner integrasi final | Exit 0: suite transfer, health, dan API lulus. |
| PostgreSQL transfer | 13 top-level test lulus, termasuk constraint/concurrency dan migrasi. |
| PostgreSQL API | Startup, HTTP nyata, health, 201/200/409/401/404, auth/isolasi merchant, shutdown lulus. |
| Hash artefak host/container | Ketiga binary test dan dbmate identik. |
| Sisa schema fixture di database test | 0. |
| Database demo | Dump schema/data sebelum dan sesudah identik; tidak dimigrasi/reset/rollback. |

Run awal build integration sempat gagal karena import transfer terhapus meskipun masih dipakai oleh decode response test API; import dipulihkan dan build ulang lulus. Test tambahan adopsi metadata sempat gagal karena assertion membandingkan object HTTP yang menyembunyikan routing dengan row DB lengkap. Assertion diperbaiki untuk membandingkan snapshot row DB sebelum/sesudah; run final dijalankan ulang, bukan menganggap run gagal sebagai lulus.

Output mentah run final disimpan di `bin/integration/postgres.log`. Binary dan SQL yang dijalankan tersedia di `/tmp/transfer-101-tests` pada container test.

### Bukti migrasi dan enam kasus tahap 2

- dbmate `up`, `status`, rerun tanpa duplikasi/data hilang, `rollback`, lalu `up` kembali.
- `CREATE TABLE IF NOT EXISTS` tidak menimpa row pada tabel existing; rollback juga berhasil bila tabel sudah tidak ada.
- SQL yang sengaja gagal dengan division by zero tidak meninggalkan tabel parsial atau versi applied. Tabel bookkeeping kosong boleh tetap ada karena dibuat dbmate sebelum transaksi migrasi.
- Metadata legacy dan seluruh field transfer existing tetap sama setelah adopsi dbmate.
- Startup menolak schema yang belum dimigrasi, telah di-rollback, atau tabel existing yang kehilangan kolom.
- CHECK pada tabel baru tidak memiliki COLLATE eksplisit; unique constraint merchant/provider, amount/status/currency dan format tetap diuji pada PostgreSQL nyata.
- Create: 201, PENDING/IDR, ID/provider reference durable, MaxInt64 dan nol awal rekening terjaga.
- Duplicate identik: 200 dengan ID/reference existing dan satu row.
- Perubahan amount/bank_code/account_number: masing-masing 409 pada keempat status, tanpa overwrite.
- 24 request bersamaan: satu 201, 23 duplicate 200, satu row, ID/reference sama.
- Reference sama lintas merchant: dua row/ID/provider reference berbeda.
- Merchant lain membaca transaksi: 404, termasuk dengan header identitas palsu.

Test lain yang lulus meliputi payload berbeda bersamaan, SELECT snapshot baru setelah insert pemenang commit, routing existing setelah config berubah, dan storage error menjadi 503. Status final dibuat melalui fixture SQL, bukan endpoint finalisasi.

## Perlindungan demo dan batas bukti

Pemeriksaan read-only menunjukkan demo sudah memiliki transfers dan schema_migrations dari runner lama, dengan nol row transfer pada saat diperiksa. Tidak ada mutasi yang dijalankan terhadap demo. Hash dump schema/data sebelum dan sesudah perubahan: `4cdb0bbf119b319f14d63a3f7e5db6f3c195648f4385fe0a3c398c099d59394c`. Dump memakai `pg_dump --no-owner --no-privileges --restrict-key=transfer101verification` dan hasilnya hanya di-hash di container.

Mengubah file create tidak mengubah constraint pada tabel demo existing. `IF NOT EXISTS` juga tidak mengaudit struktur lengkap; menghapus collation pada CHECK yang sudah terpasang memerlukan migrasi ALTER terpisah. Rollback yang disediakan menghapus tabel transfers dan datanya, sehingga hanya diuji pada schema fixture test. Jalankan satu proses migrasi pada satu waktu; jaminan concurrent migration/checksum dari runner Go lama tidak dipertahankan.

Arsitektur amd64 dan native combined race+PostgreSQL belum diverifikasi. Tidak ada benchmark, klaim produksi, atau fitur tahap 3. Go minimum dan dependency module aplikasi tetap sama.

Setelah selesai, hentikan hanya service test bila diperlukan:

```sh
docker compose --profile test stop postgres-test
```

Database test memakai tmpfs dan akan disiapkan kembali oleh entrypoint pada start berikutnya. Jangan memakai cleanup project/global volume untuk test. Kedua service dibiarkan healthy pada akhir sesi review.
