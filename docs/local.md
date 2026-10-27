# Menjalankan lokal dengan mise

## Mulai dari sini

Pastikan Docker aktif dan mise tersedia. Dari root proyek:

```sh
mise trust
mise install
mise run setup
mise run funds:init merchant_a 10000000
mise run dev:504
```

Di terminal kedua:

```sh
mise run demo
```

Hasil yang diharapkan: `app=SUCCESS bank=SUCCEEDED`, ID duplicate merchant/provider tetap sama, dan `recovered=SUCCESS`. Saldo dibuka secara eksplisit sebelum demo; `funds:init` hanya idempoten untuk jumlah pembuka yang sama. Untuk saldo existing gunakan `mise run funds:show merchant_a`. Mock menyimpan SUCCEEDED sebelum respons submit 504; worker menyelesaikan aplikasi lewat inquiry. Initial status dapat PROCESSING atau sudah SUCCESS, tergantung kapan worker berjalan. Command menunggu status final dengan deadline.

`mise run demo order-001` memakai reference pilihan sendiri. Mengulang reference dengan payload demo sama membaca transaksi existing. Gunakan reference baru untuk skenario lain. CLI ini mengharapkan hasil bank SUCCEEDED/aplikasi SUCCESS; skenario reject/accepted memakai [demo provider manual](demo-provider.md).

`mise run dev` mengikuti MOCK_BANK_SCENARIO pada file env, default success. `mise run dev:504` memaksakan success_504 hanya pada task mock tersebut; file env tidak diubah. Untuk mengganti skenario atau kode, Ctrl+C dahulu, lalu jalankan ulang dev. Tidak ada hot reload.

## Setup dan proses

1. env:init menyalin contoh hanya bila `.env` atau `.env.mockbank` belum tersedia.
2. db:up menjalankan PostgreSQL aplikasi/mock dan menunggu healthy.
3. migrate menjalankan dbmate up kedua fitur, berurutan, tanpa reset.
4. build membuat API, mock bank, worker, demo-provider, funds, reconcile, dan loadtest pada bin/local.

`mise run setup` menjalankan langkah tersebut tanpa server. dev kemudian menjalankan api/bank/worker paralel dalam foreground dengan prefix log. Executable hasil build dipakai langsung, sehingga tidak melewati wrapper go run. Ctrl+C menghentikan task group; database Docker tetap berjalan.

Hentikan semua writer tahap lama sebelum setup/migrasi agar fencing konsisten. Tidak ada task reset/drop/rollback otomatis. Jalankan migrasi hanya satu sesi pada satu waktu. `mise run db:status` melihat versi migrasi kedua database. Jika ingin terminal proses terpisah: jalankan setup sekali, kemudian api, bank dan worker di terminal masing-masing.

## Environment dan tool

mise.toml memuat `.env.example`, `.env.mockbank.example`, `.env`, lalu `.env.mockbank`. Nilai lokal menggantikan contoh. Tidak perlu source file env manual; dotenv dibaca mise sehingga `?` dan `&` pada URL tidak ditafsirkan sebagai glob shell. Task test menetapkan TEST_DATABASE_URL secara eksplisit, terpisah dari DATABASE_URL demo.

Edit file env lokal untuk mengganti port/credential/skenario, lalu restart dev. Jika mengganti port mock, sesuaikan MOCK_BANK_HTTP_ADDR dan MOCK_BANK_URL. CLI demo mengikuti HTTP_ADDR/MOCK_BANK_URL; DEMO_API_URL/DEMO_BANK_URL dan token demo dapat diatur untuk konfigurasi lain. Origin CLI harus loopback.

Go 1.26.10 dan dbmate 2.28.0 dipin pada tools; Docker/Compose disediakan mesin pengguna. Run pertama dapat mengunduh tool, image, module Go, dan dbmate Linux. Script build memverifikasi checksum dbmate Linux. Tidak ada dependency Go baru.

Konfigurasi mengikuti [mise.toml](https://mise.jdx.dev/configuration.html), [task TOML](https://mise.jdx.dev/tasks/toml-tasks.html), dan [dotenv environment](https://mise.jdx.dev/environments/). mise.local.toml dan mise.*.local.toml diabaikan Git. Jangan commit credential lokal; output `mise env` dapat memuat credential/URL database.

## Test otomatis

Tidak perlu dev atau server manual:

```sh
mise run test:e2e
mise run test:integration
mise run test:recovery
mise run test:race
mise run test:recovery:race
```

Load test lokal: `mise run loadtest -- --requests 200 --concurrency 16`; detail batas, skenario request, metrik, dan invariant ada di [panduan load test](load-test.md). Hasil berada di prefix khusus dan tidak dihapus otomatis.

Untuk CSV: `mise run reconcile export bank_account_demo_a REPORT_ID ./report.csv`, lalu `mise run reconcile import bank_account_demo_a REPORT_ID ./report.csv` dan `mise run reconcile show bank_account_demo_a IMPORT_ID`. `mise run funds:init MERCHANT RUPIAH` membuat saldo pembuka eksplisit; migrasi tidak membuat saldo historis untuk transfer legacy. Panduan detail ada di [dana dan rekonsiliasi](funds-reconciliation.md).

- test:e2e menjalankan TestPostgresAPIStartupAndRoutes: listener API, mock HTTP dan worker live memakai PostgreSQL asli, lalu CLI eksternal memeriksa duplicate dan recovery. Server/worker berada di test harness; ini bukan pengujian tiga executable server standalone. Crash worker dan restart mock diuji pada test:integration.
- test:integration menjalankan lima suite lengkap, termasuk constraint, merchant isolation, concurrency, SIGKILL/restart, lease, timeout dan manual review.
- test:recovery mengulang focused test 10 kali; test:recovery:race mengulang 5 kali dengan race.
- test:race memakai Go SDK Linux/CGO agar race detector aktif. Suite container biasa memakai binary CGO_ENABLED=0.
- Task menyiapkan postgres-test dan artefak otomatis. Arsitektur ditentukan dari uname container tujuan: arm64 atau amd64.

Database demo transfer_demo/mock_bank memakai named volume terpisah. Database transfer_test memakai tmpfs dan role sendiri. Fixture memverifikasi database/role sebelum membuat schema acak, menerapkan migrasi dbmate dan membersihkan schema miliknya. URL shell dikutip penuh pada task. Dev yang sudah aktif dapat tetap berjalan selama test.

## Berhenti dan troubleshooting

- Ctrl+C pada terminal dev menghentikan proses Go. Tunggu task selesai sebelum menjalankan dev lagi.
- `mise run db:stop` hanya menghentikan kedua database demo tanpa menghapus volume. Jalankan setelah proses Go berhenti.
- `mise run test:stop` hanya menghentikan postgres-test; data tmpfs test sementara dapat hilang.
- Port 8080/8081 sudah dipakai: hentikan proses lama di terminal pemiliknya atau atur port env. Task tidak membunuh proses lain otomatis.
- Jika satu proses server gagal, periksa error lalu Ctrl+C untuk menghentikan proses lainnya sebelum mengulang dev. Pada mise 2026.4.19 yang diverifikasi, kegagalan satu task paralel tidak otomatis menghentikan sibling yang masih berjalan.
- Docker tidak tersedia: buka Docker Desktop/Engine lalu ulangi command. Kegagalan dependency menghentikan task berikutnya.
- Binary tidak tersedia ketika memakai api/bank/worker terpisah: jalankan setup dahulu. dev sudah melakukan setup/build.
- Skema belum dimigrasi: hentikan writer lalu jalankan migrate. Jangan reset volume demo untuk memperbaiki versi.
- Demo mencapai deadline/manual review: periksa log worker dan GET transaksi, serta [observasi manual review](recovery.md#observasi-manual-review). Jangan mengganti reference untuk menyembunyikan hasil ambigu.

Sesi implementasi mise tidak memigrasi atau me-restart demo pengguna. [Laporan verifikasi mise](verification-local.md) mencatat command aktual dan batas lingkungan agent.
