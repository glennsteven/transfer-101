# Demo provider tahap 3 (mode manual)

Untuk tiga proses lokal dan recovery tahap 4 dengan command singkat: `mise run dev:504`, lalu `mise run demo` di terminal kedua. Lihat [panduan mise](local.md).

Untuk mengamati PROCESSING tanpa recovery, hentikan worker saat memakai resep ini. Tahap 4 menyediakan [demo recovery dengan worker](recovery.md); semua proses tetap memakai kode/migrasi terbaru.

## Setup

Ikuti quickstart [README](../README.md#menjalankan-lokal): start service PostgreSQL aplikasi dan bank, salin file env bila belum ada, jalankan migrasi **kedua** fitur dengan dbmate, lalu start mock bank dan API pada terminal terpisah. Go >=1.26 dan dbmate 2.28.0 diperlukan. Tidak membutuhkan jq, Python, Redis, atau broker.

Database aplikasi: transfer_demo pada service postgres, port 54329, volume postgres_data. Database bank: mock_bank pada service postgres-bank, port 54331, volume mock_bank_data. Mock HTTP default 127.0.0.1:8081; API 127.0.0.1:8080. Kedua proses memakai credential contoh publik di config/. Database demo yang sudah ada tidak di-reset. Perubahan schema diterapkan dengan migrasi additive, bukan membuat ulang transfers.

Untuk restart proses mock dengan jelas, build dan jalankan executable foreground:

```sh
go build -o bin/mockbank ./cmd/mockbank
set -a
. ./.env.mockbank
set +a
./bin/mockbank
```

Di terminal API:

```sh
set -a
. ./.env
set +a
go run ./cmd/api
```

## Happy path

Default MOCK_BANK_SCENARIO adalah success. Di terminal ketiga:

```sh
go run ./cmd/demo-provider demo-success-001 SUCCESS SUCCEEDED
```

CLI membuat transfer dan duplicate merchant, dispatch transaksi existing, inquiry HTTP ke mock, lalu submit duplicate langsung ke mock untuk membuktikan operation ID existing. Output hanya ID/reference/status, tanpa token atau nomor rekening. Bank dipanggil oleh aplikasi saat dispatch; create/duplicate tidak memanggil bank. Exit nonzero bila status/identitas tidak sesuai.

Wrapper ekuivalen: `sh scripts/demo-provider.sh demo-success-001 SUCCESS SUCCEEDED`. Jika alamat/token demo berbeda, set DEMO_API_URL, DEMO_BANK_URL, DEMO_MERCHANT_TOKEN, DEMO_BANK_TOKEN. Semua URL demo dibatasi HTTP origin loopback. Aplikasi tetap memakai credential berdasarkan provider_account_id yang disimpan di transaksi.

## Bank sukses tetapi submit 504

Hentikan **proses mock** dengan Ctrl+C; biarkan kedua database dan API tetap berjalan. Pada terminal mock yang env-nya sudah di-export:

```sh
env MOCK_BANK_SCENARIO=success_504 ./bin/mockbank
```

Kemudian:

```sh
go run ./cmd/demo-provider demo-504-001 PROCESSING SUCCEEDED
```

Hasil yang diharapkan: app=PROCESSING, bank=SUCCEEDED. HTTP 504 dikirim setelah row bank committed. Transaksi/reference aplikasi tidak diganti. Inquiry CLI mengobservasi hasil bank tetapi belum memfinalisasi aplikasi. Submit duplicate langsung ke bank mengembalikan operasi yang sama; aplikasi sendiri belum melakukan retry otomatis.

Ulangi command yang sama: ID/reference/operation tetap existing dan dispatch PROCESSING tidak memanggil bank lagi. Untuk menguji persistensi, stop/start proses mock tanpa menyentuh database/volume, lalu ulangi command; hasil bank tetap ada. Test otomatis memverifikasi hal ini di dua OS process berbeda.

## Skenario lain

Stop/start proses mock memakai nilai env berikut. Gunakan reference berbeda untuk setiap skenario; key lama selalu mengembalikan hasil existing, meskipun konfigurasi berubah.

| MOCK_BANK_SCENARIO | Command CLI setelah mock start |
| --- | --- |
| reject | `go run ./cmd/demo-provider demo-reject-001 FAILED REJECTED` |
| accepted | `go run ./cmd/demo-provider demo-accepted-001 PROCESSING ACCEPTED` |
| accepted_500 | `go run ./cmd/demo-provider demo-500-001 PROCESSING ACCEPTED` |
| success_timeout | `go run ./cmd/demo-provider demo-timeout-001 PROCESSING SUCCEEDED` |

Untuk timeout:

```sh
env MOCK_BANK_SCENARIO=success_timeout MOCK_BANK_RESPONSE_DELAY=5s ./bin/mockbank
```

PROVIDER_TIMEOUT aplikasi default 2s. Mock menyimpan SUCCEEDED sebelum delay. Delay ini hanya simulasi respons lokal; test async tidak memakai time.Sleep.

## Observasi manual

POST /transfers/{id}/dispatch membutuhkan bearer merchant pemilik dan body kosong. GET /transfers/{id} membaca status lokal. Inquiry mock:

```sh
curl -sS 'http://127.0.0.1:8081/v1/transfers/PROVIDER_REFERENCE_DARI_RESPONSE' \
  -H 'Authorization: Bearer demo-bank-account-a-token'
```

Attempt bisa direview pada tabel aplikasi melalui command terdokumentasi berikut; tidak membaca payload/evidence rekening:

```sh
docker compose exec -T postgres psql -U transfer_demo -d transfer_demo -c 'SELECT id, transfer_id, outcome, http_status, bank_status, reason_code, started_at, finished_at FROM transfer_attempts ORDER BY id;'
```

FINAL_CONFLICT atau PROVIDER_PAYLOAD_CONFLICT perlu review. STARTED tanpa finished_at dapat berarti proses mati sebelum hasil dicatat. PROCESSING tidak dapat dipaksa dispatch ulang melalui endpoint tahap ini. Jangan membuat reference baru untuk mengatasi timeout.

## Batas dan cleanup

Panduan ini merekam alur manual tahap 3. Pada alur tahap 5, saldo eksplisit wajib diinisialisasi dan recovery worker dapat memfinalisasi inquiry sambil settlement held. Lihat [alur saldo/laporan](funds-reconciliation.md) dan [demo recovery](recovery.md). Jaminan idempotency mock berlaku selama data bank dipertahankan, bukan setelah reset/rollback bank. Rollback bank menghapus bank_operations; jangan gunakan untuk restart demo.

Hentikan proses dengan Ctrl+C dan service dengan `docker compose stop postgres postgres-bank` bila selesai. Command stop mempertahankan volume. Pengujian menggunakan postgres-test dan schema fixture sendiri; jangan menghapus volume demo untuk cleanup test.
