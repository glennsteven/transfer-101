# Pemeriksaan tahap 1

Tanggal: 20 Oktober 2026. Lingkungan: macOS darwin/arm64, Go 1.26.2, Docker CLI 28.1.1, Docker Compose v2.35.1. Pengujian ini hanya untuk foundation, bukan bukti implementasi transfer atau kapasitas produksi.

## Hasil akhir

| Pemeriksaan | Hasil aktual |
| --- | --- |
| `gofmt -l cmd/api internal/health` | PASS, tidak ada output file yang belum terformat. |
| `go mod tidy` dengan cache yang dapat ditulis | PASS; satu dependency `github.com/lib/pq v1.12.3`. |
| `go mod verify` | PASS, `all modules verified`. |
| `go build ./...` | PASS, exit 0. |
| `go build -o /tmp/transfer-101-api ./cmd/api` | PASS, exit 0. |
| `go vet ./...` | PASS, exit 0. |
| `go test -race -count=1 -timeout=30s -json ./...` | PASS, exit 0, kedua package `cmd/api` dan `internal/health` lulus. |
| `docker compose config --quiet` | PASS, exit 0; ini validasi konfigurasi, bukan startup PostgreSQL. |
| Proses binary tanpa DATABASE_URL | PASS untuk skenario negatif: exit 1, log `invalid configuration`. |
| Proses binary dengan URL database malformed berisi credential demo | PASS untuk skenario negatif: exit 1; credential tidak muncul di stdout/stderr. |
| Test integration tanpa TEST_DATABASE_URL | Gagal sesuai guard, exit 1 dengan instruksi bahwa variabel wajib; tidak silent skip. |
| Test integration dengan URL PostgreSQL lokal | BLOCKED oleh sandbox: `dial tcp 127.0.0.1:54329: connect: operation not permitted`, exit 1. Koneksi nyata belum terbukti. |
| Smoke proses HTTP dan PostgreSQL hidup/mati/pulih | Belum terverifikasi; sandbox menolak socket localhost dan database belum berhasil dijalankan. |

Go command akhir dijalankan memakai environment berikut agar tidak menulis build cache di luar direktori yang diizinkan dan dapat memakai module yang sudah ada:

```sh
export GOCACHE=/tmp/transfer-101-go-build
export GOPROXY=off
go mod tidy
go mod verify
go build ./...
go vet ./...
go test -race -count=1 -timeout=30s -json ./...
```

`GOPROXY=off` hanya cocok pada sesi ini karena module sudah ada di cache dan checksum diverifikasi. Quickstart pengguna memakai pengaturan Go normal agar dependency dapat diunduh jika belum tersedia; tidak ada konfigurasi Go global yang diubah.

## Cakupan yang benar-benar dijalankan

- Config defaults, nilai invalid/kosong, validasi URL/port/durasi, error tidak membocorkan credential, dan connect timeout driver tidak bisa dinonaktifkan lewat URL.
- Liveness tanpa akses DB; readiness tersedia/gagal, cancellation request, deadline dengan waktu virtual, response JSON/no-store, method/path, dan route transfer yang belum tersedia.
- HTTP lifecycle melalui `net.Pipe`: request aktif selesai saat graceful shutdown, error listener diteruskan, serta deadline shutdown memaksa koneksi ditutup dan request dibatalkan. Timer memakai `testing/synctest`; tidak ada `time.Sleep` untuk menunggu hasil.
- Build, vet, checksum dependency, validasi Compose, serta dua skenario startup negatif pada executable.

## Hambatan dan percobaan yang tidak dihitung sebagai PASS

Pada percobaan awal, build cache Go default tidak dapat ditulis (`operation not permitted`). Percobaan memakai module cache baru juga gagal resolusi DNS `proxy.golang.org`. Memakai build cache sementara dan module yang sudah tersedia menyelesaikan pemeriksaan Go; tidak ada versi/dependency yang diturunkan.

Test lifecycle awal yang memakai listener TCP sungguhan gagal saat `bind` localhost: `operation not permitted`. Test tersebut diubah menjadi HTTP melalui `net.Pipe` dengan clock virtual, lalu seluruh test/race dijalankan ulang dan lulus. Ini membuktikan logika HTTP/lifecycle, bukan kemampuan membuka port OS dalam sesi ini. Test integrasi database nyata tetap terpisah dan gagal terbuka ketika prerequisite tidak tersedia.

`docker version` awal tidak dapat menghubungi daemon; `docker desktop status` juga gagal menulis log di direktori di luar workspace. Selanjutnya `docker compose up -d --wait postgres` dapat mencapai tahap pull image, tetapi download belum selesai. Pull dihentikan dengan SIGINT (exit 130); belum ada container/database yang berjalan dari percobaan itu. Startup/readiness PostgreSQL belum boleh disebut PASS berdasarkan validasi Compose saja.

Upaya menyiapkan binary PostgreSQL sementara untuk pengujian juga tidak selesai: download timeout setelah 60 detik. Binary tersebut tidak dijalankan dan tidak menjadi dependency repository. Instalasi PostgreSQL permanen atau perubahan konfigurasi Docker global tidak dilakukan.

Catatan ini adalah hasil historis foundation. Untuk mengulang integrasi dengan guard/setup terbaru, gunakan database test terpisah; jangan menyalin DATABASE_URL demo:

```sh
docker compose --profile test up -d --wait postgres-test
env 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:54330/transfer_test?sslmode=disable&connect_timeout=3' go test -race -tags=integration -count=1 -timeout=120s -v ./...
```

Pada foundation, integration test hanya memeriksa health tanpa migrasi. Pada setup tahap 2 saat ini, fixture transfer/API menjalankan migrasi di schema miliknya pada database transfer_test. Lihat [hasil dan resep terbaru](verification-idempotency.md). Hasil historis di atas tidak menyatakan command baru ini sudah dijalankan pada host.

## Berkas dan batas pekerjaan

Ditambahkan: `go.mod`, `go.sum`, `.gitignore`, `.env.example`, `compose.yaml`, `README.md`; empat file di `cmd/api`; tiga file di `internal/health`; serta empat dokumen di `docs`. Tujuh file skill yang ditemukan di `skills/` tidak diubah.

Tidak ada schema transfer, create/get, autentikasi merchant, dispatch/provider, worker, reservasi dana, atau CI pada tahap ini. Kontrak/desain fitur tersebut ditulis untuk review sebelum tahap pemiliknya. Folder belum diinisialisasi sebagai Git repository; tidak ada commit, push, atau publish.

## Checklist review sebelum tahap 2

- [ ] Review validasi config, timeout pool/HTTP, dan cleanup pada semua jalur startup/shutdown.
- [ ] Review perbedaan liveness/readiness dan batas bukti test in-memory.
- [ ] Verifikasi quickstart, koneksi PostgreSQL nyata, readiness hidup/mati/pulih, serta SIGTERM di lingkungan yang mengizinkan socket.
- [ ] Setujui kontrak API: authenticated merchant, IDR integer, validasi string, 201/200/409, dan arti empat status.
- [ ] Setujui kontrak mock bank: deduplikasi/retensi, scope reference, bukti final, dan not found ambigu.
- [ ] Review LoB, SQL conflict/select terpisah, batas commit/HTTP, serta roadmap tanpa abstraction tambahan.
- [ ] Berikan review tahap pertama sebelum menjalankan `transfer-02-idempotency`.
