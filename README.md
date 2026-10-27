# transfer-101

Simulasi backend transfer Go dan PostgreSQL untuk menangani keadaan ketika **bank sudah memproses transfer, tetapi aplikasi menerima HTTP 504**. Identitas transfer disimpan sebelum provider dihubungi; recovery mencari bukti final dengan reference yang sama, sementara dana tetap ditahan.

Seluruh bank, rekening, credential, dan saldo adalah fixture lokal. Saldo bukan uang nyata. Panduan: [demo](docs/demo.md), [diagram](docs/diagrams.md), [desain](docs/design.md), [bukti verifikasi](docs/verification.md), [draft artikel](docs/article.md), [bahan presentasi](docs/presentation.md).

Review kode pada 2026-10-27 menemukan gap pencatatan bukti final terlambat, kelengkapan snapshot laporan, dan batas dependency bisnis. Temuan tersebut masih terbuka; lihat [temuan review](#temuan-review-yang-masih-terbuka) dan [hasil verifikasi](#test-dan-verifikasi) sebelum menilai jaminan implementasi.

## Fitur yang tersedia

- Create/get transfer dengan autentikasi bearer demo dan isolasi merchant.
- Idempotency merchant: key/payload sama mengembalikan ID existing; payload berbeda menghasilkan 409 pada seluruh status.
- Mock bank durable dengan idempotency provider, inquiry, dan snapshot laporan CSV.
- Simulasi sukses, penolakan final, ACCEPTED, sukses sebelum 504/timeout, serta ACCEPTED sebelum 500.
- Dispatch manual atau worker polling; claim singkat, lease/token, retry durable, bounded exponential backoff dengan jitter, dan manual review.
- Reservasi dana atomik saat create; capture/release sekali berdasarkan bukti final, dengan catatan movement durable.
- Rekonsiliasi verified dengan temuan mismatch/missing/duplikat; reimport tidak menggandakan saldo.
- Unit/integration/process tests, workflow CI PostgreSQL, structured logging, dan load runner lokal terbatas.

## Stack dan Locality of Behavior

Minimum module **Go 1.26.0**; mise/CI memin **Go 1.26.10**, dbmate **2.28.0**, dan Compose memakai **PostgreSQL 17.11**. Satu dependency module aplikasi adalah `github.com/lib/pq v1.12.3`, driver `database/sql`. HTTP, JSON, CSV, context, logging, random ID, dan test memakai standard library. dbmate adalah CLI pengembangan terpisah.

```text
cmd/
  api/                  komposisi HTTP API dan lifecycle
  mockbank/             proses bank dengan database sendiri
  worker/               proses dispatch/inquiry recovery
  demo-provider/        CLI demo HTTP dan pengecekan duplicate
  funds/                inisialisasi/baca saldo simulasi
  reconcile/            export/import/show laporan dan audit
  loadtest/             traffic HTTP terbatas dan audit invariant
internal/
  transfer/             HTTP, auth, SQL, provider, recovery, dana, rekonsiliasi
    migrations/         SQL aplikasi; test berada dekat fitur
  mockbank/             submit/inquiry/laporan, SQL, test bank
    migrations/         SQL bank
  health/               liveness/readiness dan test
  testdb/               guard database test dan migration fixture
config/                 credential contoh merchant/provider
scripts/                build/runner test container dan wrapper demo
docs/                   kontrak, desain, demo, verifikasi, artikel
.agents/                tujuh skill pengerjaan bertahap
.github/workflows/      workflow verifikasi PostgreSQL
AGENTS.md               instruksi agent untuk membaca aturan arsitektur
ARCHITECTURE_RULES.md   aturan LoB, batas dependency, dan workflow review
```

Locality of Behavior menempatkan SQL, handler, recovery, dan aturan saldo di fitur `transfer` agar perubahan status dapat direview bersama efek dananya. Mock bank memiliki fitur/penyimpanan sendiri. `cmd` merangkai komponen konkret. Tidak ada ORM, HTTP framework, generic repository, DI container, atau layer penerus panggilan. PostgreSQL menyimpan state sekaligus pekerjaan durable; tidak memerlukan Redis, broker, atau Kubernetes.

[AGENTS.md](AGENTS.md) mengarahkan implementasi dan review ke [ARCHITECTURE_RULES.md](ARCHITECTURE_RULES.md). Aturan tersebut menetapkan alur handler/worker ke service, lalu ke kontrak storage/provider. Implementasi saat ini masih menggabungkan keputusan bisnis dengan SQL dan memakai provider konkret pada recovery; pengelompokan satu package belum memenuhi batas dependency tersebut. Perbaikan dilakukan bertahap pada workflow terkait dengan mempertahankan kontrak API, schema, serta invariant transaksi.

## Menjalankan lokal

Prasyarat: mise, Docker Engine/Desktop aktif dengan Compose v2 yang mendukung `--wait`, serta akses unduhan tool/module/image untuk run pertama. Repository: [glennsteven/transfer-101](https://github.com/glennsteven/transfer-101). Clone melalui SSH, lalu jalankan setup dari root proyek:

```sh
git clone git@github.com:glennsteven/transfer-101.git
cd transfer-101
```

```sh
mise trust
mise install
mise run setup
mise run funds:init merchant_a 10000000
mise run funds:show merchant_a
mise run dev:504
```

`setup` membuat `.env` dan `.env.mockbank` hanya jika belum ada, menjalankan dua PostgreSQL, menerapkan migration dbmate kedua fitur, lalu build. `funds:init` mencatat pembuka Rp10.000.000 secara eksplisit; pengulangan pada akun existing hanya menerima jumlah pembuka yang sama. Untuk akun dengan pembuka lain, periksa `funds:show` dan gunakan saldo yang telah dialokasikan. Command ini bukan top-up.

`dev:504` menjalankan API, bank `success_504`, dan worker. Di terminal kedua:

```sh
mise run demo
mise run funds:show merchant_a
```

Expected result: `app=SUCCESS bank=SUCCEEDED`, identitas duplicate tetap sama, dan `recovered=SUCCESS`. Initial status dapat sudah final karena worker berjalan bersamaan. [Demo terpisah](docs/demo.md#bank-sukses-respons-504-lalu-inquiry) memperlihatkan PROCESSING/held sebelum worker dijalankan.

`mise run dev` memakai skenario `.env.mockbank` (default success). Ctrl+C menghentikan proses foreground. `mise run db:stop` menghentikan dua database tanpa menghapus volume. Hentikan writer lama sebelum migration; jangan memakai penghapusan volume sebagai cara restart/cleanup test.

### Konfigurasi dan credential publik

mise memuat `.env.example`, `.env.mockbank.example`, `.env`, kemudian `.env.mockbank`; file lokal menggantikan contoh. Restart proses setelah mengubah konfigurasi.

| Komponen | Default lokal |
| --- | --- |
| API | `http://127.0.0.1:8080` (`HTTP_ADDR`) |
| Bank | `http://127.0.0.1:8081` (`MOCK_BANK_HTTP_ADDR` / `MOCK_BANK_URL`) |
| PostgreSQL aplikasi | `127.0.0.1:54329`, database/user/password `transfer_demo` |
| PostgreSQL bank | `127.0.0.1:54331`, database/user/password `mock_bank` |
| PostgreSQL test | `127.0.0.1:54330`, database/user/password `transfer_test`, tmpfs |
| Merchant A | token `demo-merchant-a-token`, merchant `merchant_a`, dana `funds_a` |
| Merchant B | token `demo-merchant-b-token`, merchant `merchant_b`, dana `funds_b` |
| Akun integrasi kedua merchant | `bank_account_demo_a`, token bank `demo-bank-account-a-token` |

Credential sengaja publik dan khusus demo loopback. Merchant B perlu saldo pembuka sebelum create. Identitas merchant berasal dari authenticated context; body tidak boleh memilih merchant/sumber dana/akun provider. Pemetaan: [merchant](config/merchants.example.json), [provider](config/provider-accounts.example.json).

| Opsi | Default/fungsi |
| --- | --- |
| `DATABASE_URL`, `MOCK_BANK_DATABASE_URL` | URL TCP PostgreSQL dari contoh env; quote seluruh assignment shell karena `?`/`&`. |
| `MERCHANT_CREDENTIALS_FILE`, `PROVIDER_CREDENTIALS_FILE` | File credential contoh dalam config, dibaca saat startup. |
| `DB_TIMEOUT`, `PROVIDER_TIMEOUT`, `SHUTDOWN_TIMEOUT` | `2s`, `2s`, `5s`. |
| `RECOVERY_LEASE`, `RECOVERY_POLL` | `30s`, `500ms`. |
| `RECOVERY_BASE_BACKOFF`, `RECOVERY_MAX_BACKOFF` | `1s`, `1m`; due time disimpan di DB. |
| `MOCK_BANK_SCENARIO` | `success`; hanya memengaruhi operasi baru. |
| `MOCK_BANK_RESPONSE_DELAY` | `5s` untuk skenario timeout. |
| `MOCK_BANK_INQUIRY_NOT_FOUND` | `false`; simulasi visibility delay tanpa menghapus operasi. |

### Migration dan data existing

```sh
mise run migrate
mise run db:status
```

SQL berada di direktori fitur dengan dbmate up/down, `CREATE TABLE IF NOT EXISTS`, tanpa `COLLATE` eksplisit. Metadata memakai `dbmate_schema_migrations`; metadata runner lama dipertahankan. `IF NOT EXISTS` tidak memperbaiki schema drift atau mengganti constraint existing. Jalankan migration dari satu proses; API tidak menjalankan DDL otomatis.

Migration dana mempertahankan transfer historis sebagai `funds_state=LEGACY`. PENDING/PROCESSING lama diberi `LEGACY_FUNDS_UNACCOUNTED` dan jadwal dihentikan. Saldo atau movement historis tidak ditebak; saldo pembuka baru tidak mengadopsinya. Resolusi memerlukan keputusan manual berbasis bukti. [Kebijakan legacy](docs/funds-reconciliation.md). Rollback dapat menghapus tabel/data tahap yang dilepas dan bukan bagian restart.

## API dan status

Dengan API aktif dan saldo cukup:

```sh
curl --max-time 10 -i http://127.0.0.1:8080/transfers \
  -H 'Authorization: Bearer demo-merchant-a-token' \
  -H 'Content-Type: application/json' \
  -d '{"reference_id":"readme-order-001","amount":150000,"bank_code":"001","account_number":"00123456789"}'
```

Request baru menghasilkan `201 Created`, `Location: /transfers/{id}`. Bentuk body ketika worker belum berjalan (ID/timestamp ilustratif):

```json
{
  "id": "trf_example",
  "reference_id": "readme-order-001",
  "provider_reference": "prv_example",
  "amount": 150000,
  "currency": "IDR",
  "bank_code": "001",
  "account_number": "00123456789",
  "status": "PENDING",
  "created_at": "2026-10-23T00:00:00Z",
  "updated_at": "2026-10-23T00:00:00Z",
  "funds_state": "HELD"
}
```

Worker aktif dapat memajukan status sebelum response dibaca. `201` berarti resource tersimpan, bukan bank sukses. Nominal selalu IDR integer rupiah `int64`/`BIGINT`, tanpa float. Bank/rekening berupa string dengan nol awal dipertahankan; `001` adalah kode fixture.

| Status | Makna | Dana transaksi baru |
| --- | --- | --- |
| PENDING | Tersimpan, menunggu dispatch. | Sudah direservasi ke held. |
| PROCESSING | Dispatch dimulai, termasuk hasil eksternal belum pasti. | Tetap held. |
| SUCCESS | Bukti final SUCCEEDED dengan identitas/payload cocok. | Held dikonsumsi sekali, CAPTURED. |
| FAILED | Penolakan final REJECTED dengan reason valid. | Held dikembalikan sekali, RELEASED. |

`needs_review`/`review_reason` adalah metadata, bukan status kelima. Timeout/504/500, HTTP 200 ACCEPTED, not-found, atau retry habis tidak membuktikan kegagalan final. Budget habis mempertahankan PROCESSING dan held.

| Endpoint/perilaku | Hasil |
| --- | --- |
| POST /transfers baru | 201; create/reference/job/reservasi atomik tanpa HTTP bank. |
| Key + payload sama | 200, ID/reference existing, tanpa reservasi lagi. |
| Key sama; amount/bank_code/account_number berbeda | 409 PAYLOAD_CONFLICT pada semua status. |
| Saldo kurang/belum diinisialisasi | 422 INSUFFICIENT_FUNDS; rollback tanpa perubahan parsial. |
| GET /transfers/{id} | 200 bagi pemilik; 404 untuk tidak ada/milik merchant lain. |
| POST /transfers/{id}/dispatch | Body kosong, dispatch sinkron PENDING dengan claim/fencing worker. |
| GET /livez, GET /readyz | Liveness 200; readiness DB 200 atau 503. |

Gunakan ID aktual dari create pada GET/dispatch. Contoh error: `{"error":{"code":"PAYLOAD_CONFLICT","message":"reference_id already used with a different payload"}}`. [Kontrak API](docs/api-contract.md) merinci batas body 4 KiB, validasi, serta respons lain.

## Idempotency, recovery, dan dana

Create memakai `INSERT ... ON CONFLICT (merchant_id, reference_id) DO NOTHING RETURNING id`, kemudian SELECT statement terpisah bila insert kalah. Keduanya berada dalam transaksi READ COMMITTED; SELECT mendapat snapshot baru. Hanya insert baru menjalankan reservasi conditional `available >= amount` dan movement RESERVE. Commit menyimpan reference/job/dana sebelum provider dihubungi.

Worker mengklaim pekerjaan jatuh tempo dengan `FOR UPDATE SKIP LOCKED`, menyimpan token/expiry/attempt, lalu commit. HTTP berjalan setelah lock dilepas. Penyimpanan hasil memeriksa token dan lease aktif; stale worker tidak boleh menimpa claim/status final. Status final, CAPTURE/RELEASE, dan evidence/audit commit atomik melalui settlement bersama dispatch, inquiry, dan rekonsiliasi.

Setiap attempt dimulai dengan inquiry, termasuk sebelum submit pertama. NOT_FOUND baru boleh dilanjutkan submit dengan reference/payload existing jika akun, epoch dataset, serta window resubmit dijamin kontrak mock. Window default 15 menit sejak create; budget default 8 claim termasuk claim yang crash. Backoff eksponensial dibatasi dengan jitter pada rentang separuh cap sampai cap. Jadwal/attempt tetap ada setelah restart. [Diagram](docs/diagrams.md), [recovery](docs/recovery.md), [kontrak provider](docs/provider-contract.md).

Inquiry membaca satu operasi. Rekonsiliasi membandingkan snapshot CSV bank dengan pencatatan lokal. Hanya laporan verified/lengkap dengan scope/epoch/payload cocok yang dapat memfinalisasi transaksi nonfinal. Missing/mismatch bukan bukti gagal; konflik dengan status final menjadi finding review. Import identik memakai audit existing tanpa efek saldo kedua. [Demo rekonsiliasi](docs/demo.md#rekonsiliasi-laporan).

## Temuan review yang masih terbuka

Review sumber kode pada 2026-10-27 menemukan hal berikut. Belum ada perubahan implementasi atau regression test PostgreSQL yang membuktikan perbaikannya.

- Finalisasi laporan menghapus lease dan menutup attempt aktif. Jika respons worker yang bertentangan datang sesudahnya, `finish` mengembalikan `errLeaseLost` sebelum membandingkan bukti final. Status dan dana tidak ditimpa, tetapi konflik tersebut tidak disimpan sebagai finding review durable. Jalur ini perlu pencatatan bukti terlambat tanpa memberi worker lama hak mengubah status/dana. Lihat [recovery](internal/transfer/recovery.go) dan [finalisasi laporan](internal/transfer/reconciliation.go).
- Mock bank menetapkan snapshot `REPEATABLE READ` sebelum mengambil timestamp cutoff, lalu memberi metadata `complete=true`. Operasi yang commit di antara kedua titik itu dapat tidak terlihat walaupun waktunya berada sebelum cutoff. Laporan immutable tersebut dapat menghasilkan finding missing yang menyesatkan. Semantik cutoff/kelengkapan perlu diselaraskan dengan snapshot dan diuji dengan commit bersamaan. Lihat [pembuatan laporan](internal/mockbank/reports.go).
- Workflow create/recovery masih bergantung langsung pada `*sql.DB` dan recovery memakai `*ProviderClient` konkret. Pemisahan service, kontrak storage/provider, dan adapter yang diwajibkan aturan arsitektur belum diterapkan. Perbaikannya perlu menjaga transaksi atomik dan menambah pengujian workflow melalui kontrak yang kecil. Lihat [create](internal/transfer/store.go), [recovery](internal/transfer/recovery.go), dan [aturan dependency](ARCHITECTURE_RULES.md#3-dependency-boundaries).

## Test dan verifikasi

```sh
mise run test
mise run test:e2e
mise run test:integration
mise run test:race
```

`mise run test` menjalankan unit test dengan race detector tanpa PostgreSQL. Task `test:e2e`, `test:integration`, dan `test:race` menyiapkan PostgreSQL test terpisah, binary sesuai arsitektur container, dan migration dbmate pada schema fixture acak. Guard memeriksa database/role `transfer_test` sebelum DDL; cleanup hanya schema fixture. Tanpa tag integration, `go test ./...` tidak menjalankan constraint/concurrency PostgreSQL.

Alternatif native (dbmate di PATH):

```sh
docker compose --profile test up -d --wait postgres-test
env 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:54330/transfer_test?sslmode=disable&connect_timeout=3' \
  go test -race -tags=integration -count=1 -timeout=10m ./...
```

[CI](.github/workflows/verification.yml) menjalankan format/vet/build/unit/integration/focused `-count=20`/race dengan PostgreSQL service sendiri. Test crash memakai child process SIGKILL dan barrier pada batas commit; test asynchronous memakai sinkronisasi terkontrol tanpa `time.Sleep` untuk menunggu hasil.

Pemeriksaan terbaru, 2026-10-27, memakai Go `1.26.10 darwin/arm64`:

| Pemeriksaan | Hasil |
| --- | --- |
| `go build ./...` | Lulus. |
| `go vet ./...` | Lulus. |
| `gofmt -l cmd internal` | Lulus; tidak ada file yang tercetak. |
| `go test -count=1 -timeout=60s ./...` | Lulus untuk suite tanpa tag integration. |
| `go test -race -count=1 -timeout=60s ./...` | Lulus untuk suite tanpa tag integration. |
| `go test -tags=integration -run '^$' ./...` | Kompilasi suite integration lulus; tidak ada test yang dijalankan. |
| `TestPostgresAPIStartupAndRoutes` dengan database test pada `127.0.0.1:54330` | Gagal sebelum fixture/test flow berjalan: `connect: operation not permitted`. |
| Akses Docker melalui `docker compose --profile test ps` | Ditolak oleh izin socket Docker workspace. |

Constraint/concurrency PostgreSQL, crash recovery, integration race, dan load test belum diverifikasi ulang pada sesi terbaru. Temuan review di atas berasal dari pembacaan kode; suite unit yang lulus tidak membuktikan jalur tersebut benar. Hasil CI remote belum teramati.

Catatan historis tahap 6 pada [docs/verification.md](docs/verification.md), bertanggal 2026-10-23, melaporkan format/vet/unit/race unit lulus pada Go 1.26.2 darwin/arm64 serta lima suite integration/process dan focused `-count=20` lulus di PostgreSQL 17.11 Linux arm64. WAL sempat memenuhi tmpfs test 256 MiB; setelah harness diperbesar 1 GiB pengulangan lulus. Hasil historis itu bukan verifikasi ulang kode terbaru atau bukti bahwa temuan review sudah ditangani.

Load runner memerlukan proses demo aktif, binary hasil setup/build, dan saldo cukup:

```sh
mise run loadtest -- --requests 200 --concurrency 16 --duration 20s --http-timeout 3s --completion-timeout 30s
```

Runner membedakan latency API dari observasi finality, lalu memeriksa uniqueness lokal/provider dan dana. Run tahap 6 belum lolos correctness penuh: query audit saldo gagal dan telah diperbaiki, tetapi run ulang terhalang izin Docker. Tidak ada benchmark tervalidasi yang diklaim. Data load dipertahankan dan saldo simulasi terpakai. [Panduan load](docs/load-test.md), [hasil aktual lengkap](docs/verification.md), [validasi dokumentasi tahap 7](docs/verification-publish.md).

Log JSON berisi transaction_id, provider_reference, operation, attempt, durasi, dan outcome tanpa token/rekening lengkap. Audit saldo/rekonsiliasi durable berada di PostgreSQL. [Demo](docs/demo.md) menyediakan query baca transaksi, transfer efektif bank, serta saldo tanpa cleanup demo.

## Trade-off dan keterbatasan

- Safety retry bergantung pada deduplikasi/retensi provider dalam scope akun/reference. Constraint lokal tidak menggantikan itu; tidak ada klaim exactly-once lintas sistem.
- Polling PostgreSQL dan worker sequential per proses menjaga model sederhana. Lease tanpa heartbeat memerlukan timeout/durasi lease yang konsisten; belum ada studi kapasitas.
- Dana held mengurangi available selama hasil belum pasti. Manual review belum memiliki UI atau command resolusi umum.
- Satu sumber dana terkonfigurasi per merchant, IDR, tanpa biaya/top-up/FX. Catatan movement sederhana bukan general ledger.
- Mock ACCEPTED tidak otomatis final. Laporan full snapshot dibatasi 1.000 baris/8 MiB, bukan statement harian bank nyata. Reset histori tanpa rotasi epoch melanggar kontrak.
- Bearer statis dan HTTP loopback, tanpa TLS/rotasi online/HA/bank nyata. Test lokal tidak menyatakan kesiapan produksi.

Artikel dan bahan presentasi disiapkan lokal untuk review. Push, deploy, dan publikasi tidak dijalankan otomatis.
