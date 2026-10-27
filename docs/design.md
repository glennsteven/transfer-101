# Keputusan desain transfer-101

## Scope dan Locality of Behavior

Foundation yang sudah direview menyediakan konfigurasi, koneksi, health/readiness, dan lifecycle. Tahap 2 menyediakan idempotency di `internal/transfer`; tahap 3 menambahkan dispatch dan HTTP client provider di fitur yang sama. `internal/mockbank` memiliki auth akun, submit/inquiry, SQL, migrasi, dan test sendiri. `cmd/mockbank` menjalankan proses bank lokal; `cmd/demo-provider` menjalankan demo melalui HTTP tanpa membaca database. `cmd/api` melakukan composition; CLI dbmate menjalankan migrasi secara eksplisit sebelum API. Tidak ada repository generik, service interface, atau DI container.

Tahap 4 menambahkan scheduler/lease pada row transfer dan proses cmd/worker. SQL aplikasi berada di `internal/transfer/migrations` (transfers, metadata recovery, dan transfer_attempts); SQL mock berada di `internal/mockbank/migrations` (bank_operations), semuanya memiliki directive dbmate up/down. Aplikasi dan bank memakai database/service/volume tersendiri pada demo; integration memakai schema fixture yang berbeda dalam transfer_test. Runner Go, embed SQL, checksum, dan advisory lock khusus telah dihapus. dbmate 2.28.0 adalah CLI terpisah; tidak menambah dependency Go aplikasi. Semua command menggunakan `--migrations-dir internal/transfer/migrations --migrations-table dbmate_schema_migrations --no-dump-schema`. Tabel bookkeeping terpisah dipilih karena database demo sudah memiliki `schema_migrations(version, checksum, applied_at)` dari runner lama; dbmate hanya menyimpan version. Metadata lama dipertahankan dan tidak dipakai lagi oleh API.

Apply/rollback memakai transaksi default dbmate; migrasi gagal tidak meninggalkan DDL parsial atau version yang ditandai applied. `CREATE TABLE IF NOT EXISTS` mempertahankan tabel existing, dan `DROP TABLE IF EXISTS` menangani rollback. Rollback menghapus seluruh data transfer pada schema target. Menjalankan ulang migrasi yang sudah dicatat tidak menghapus data. dbmate tidak memeriksa checksum perubahan file dan tidak menggantikan audit schema drift; jalankan migrasi dari satu proses. Perubahan berikutnya harus memakai file versi baru. Worker recovery dan mutasi dana tahap 5 tersedia; dokumen tiap tahap mencatat policy dan bukti test.

Tanpa `COLLATE` eksplisit, regex CHECK memakai collation database. Validasi API tetap membatasi karakter sesuai kontrak. Mengubah SQL create tidak mengubah CHECK pada tabel yang sudah ada; `IF NOT EXISTS` juga tidak memperbaiki struktur tabel yang berbeda. Database demo tidak diubah oleh pekerjaan ini; perubahan constraint existing memerlukan migrasi ALTER terpisah.

Module sementara `transfer-101` tidak mengasumsikan pemilik/URL GitHub. Minimum module `go 1.26.0`; pemeriksaan lokal memakai Go 1.26.2. Satu dependency adalah `github.com/lib/pq v1.12.3`, driver murni Go untuk `database/sql`, tanpa dependency module tambahan. HTTP, JSON, logging, context, signal, config, dan test memakai standard library.

## Startup, readiness, dan shutdown

Config dan credential merchant divalidasi sebelum koneksi/listen. URL PostgreSQL berupa TCP URL dengan user/host/database; URL lengkap tidak dilog. Startup memeriksa ping, versi dbmate transfer, dan keberadaan kolom yang dipakai CRUD; gagal menghasilkan exit nonzero sebelum membuka port. Setelah start, setiap `/readyz` melakukan ping dengan context request dan DB_TIMEOUT; `/livez` tidak menyentuh database. Readiness tidak melakukan audit schema drift berulang. Startup tidak mengaudit seluruh tipe, index, atau constraint; metadata versi dan keberadaan kolom bukan bukti kecocokan seluruh schema.

`database/sql` memiliki pool maksimum 10 open/5 idle. Timeout driver untuk connect dipaksakan dari DB_TIMEOUT dalam detik agar koneksi pool juga dibatasi. CRUD memakai timeout context DB_TIMEOUT. Server memiliki read-header/read/write/idle timeout. Port bind hanya loopback pada default; autentikasi bearer pada tahap 2 hanya untuk demo lokal, tanpa server TLS atau pengelolaan token produksi.

Signal SIGINT/SIGTERM menghentikan listener dan menunggu request aktif sampai SHUTDOWN_TIMEOUT. Context shutdown baru terpisah dari context signal yang sudah canceled. Jika deadline habis, koneksi HTTP ditutup dan error dilaporkan. Pool database ditutup setelah lifecycle server selesai. Create yang sudah committed tetap dapat ditemukan saat request diulang, walaupun respons awal hilang.

Fungsi ping pada handler health menyediakan seam kecil untuk menguji tersedia/gagal/cancel tanpa database palsu. Lifecycle test menjalankan `http.Server` melalui `net.Pipe`; timer memakai `testing/synctest`. Ini bukan bukti bahwa socket OS atau PostgreSQL nyata berhasil berjalan.

Integration test memakai service/database/role `transfer_test` tersendiri, bukan database demo. `internal/testdb` memusatkan guard URL dan verifikasi database/role aktual sebelum DDL bagi tiga suite. Compose menyiapkan database test; fixture fitur menjalankan CLI dbmate pada search_path schema acaknya dan menghapus hanya schema itu. Script build menyiapkan dbmate Linux dengan versi/digest yang dipin, SQL, dan binary test Linux. Runner memeriksa dbmate, SQL, executable test, serta daftar test sebelum menjalankan suite. Seluruh argumen URL shell dikutip penuh. Tidak ada langkah SQL manual atau cleanup database demo pada workflow ini.

## Model status transfer

| Status | Makna | Bukti/transisi |
| --- | --- | --- |
| PENDING | Row durable, menunggu dispatch. | Create lokal berhasil. |
| PROCESSING | Dispatch dimulai, termasuk hasil eksternal belum pasti. | Claim/attempt tersimpan sebelum call. |
| SUCCESS | Hasil eksternal sukses final. | SUCCEEDED yang cocok dari submit/inquiry/laporan sesuai kontrak. |
| FAILED | Kegagalan bisnis final. | REJECTED final yang cocok; bukan timeout/error transport. |

Alur umum `PENDING -> PROCESSING -> SUCCESS/FAILED`. Lease, retry, dan needs-review adalah metadata, bukan status baru. Tidak ada transisi otomatis dari retry habis ke FAILED. Status final tidak mundur; bukti final bertentangan menjadi temuan review. HTTP 200 memerlukan body bisnis; timeout/504/500 tidak membuktikan kegagalan transfer.

## Idempotency dan batas commit

Merchant berasal dari authenticated context, dengan satu sumber dana terkonfigurasi. IDR memakai integer rupiah positif `int64`, tanpa floating point. Perbandingan idempotency memakai amount, bank_code, account_number; kode/rekening berupa string dengan nol awal utuh dan aturan validasi pada kontrak API.

`UNIQUE(merchant_id, reference_id)` memilih satu row. Implementasi tahap 2 menggunakan:

```sql
INSERT INTO transfers (...)
VALUES (...)
ON CONFLICT (merchant_id, reference_id) DO NOTHING
RETURNING id;
```

Jika tidak ada row, `SELECT` existing dilakukan sebagai statement terpisah dengan isolation READ COMMITTED agar memakai snapshot baru setelah konflik selesai. CTE insert/select satu statement tidak memberikan snapshot baru. Payload sama mengembalikan existing; payload berbeda selalu 409 pada semua status. Error constraint provider yang lain tidak boleh ditelan sebagai duplicate merchant.

Pada implementasi saat ini INSERT dan SELECT adalah statement terpisah di dalam satu transaksi READ COMMITTED. Insert baru diikuti reservasi conditional dan movement RESERVE sebelum commit. Jalur duplicate membaca dan membandingkan existing tanpa reservasi tambahan. Koneksi terputus setelah commit dapat menghasilkan 503; caller mengulang key/payload sama. Tidak ada loop insert ulang jika existing lookup hilang. Kandidat ID/reference dibuat sebelum setiap upaya INSERT; kandidat yang kalah tidak tersimpan atau dipakai untuk dispatch. Resource duplicate selalu memakai reference lama. Batas transaksi ini menggantikan autocommit tahap 2 sejak penambahan dana tahap 5.

Alasan snapshot mengikuti [semantik READ COMMITTED PostgreSQL](https://www.postgresql.org/docs/17/transaction-iso.html#XACT-READ-COMMITTED). Test menahan insert pemenang tanpa commit, menunggu PostgreSQL melaporkan koneksi pesaing berada pada transaction lock, lalu commit. Hanya setelah itu request pesaing boleh mengembalikan resource existing/409. Test tidak menggunakan delay untuk mengasumsikan urutan.

Simpan ID transfer, provider_account_id, dan provider_reference sebelum HTTP. `UNIQUE(provider_account_id, provider_reference)` hanya constraint lokal; bank perlu deduplikasi durable sendiri. Semua retry/inquiry menggunakan akun/reference existing, termasuk setelah konfigurasi merchant berubah.

| Batas | Celah crash | Pemulihan yang tersedia |
| --- | --- | --- |
| Commit create/reference/job, sebelum claim | Row ada, belum dikirim. | Worker menemukan pekerjaan durable. |
| Commit claim PROCESSING, sebelum HTTP | Bank mungkin belum menerima. | Lease kedaluwarsa, tindak lanjut dengan reference sama. |
| Bank commit, sebelum respons diterima | Aplikasi belum mengetahui hasil. | Inquiry; resubmit hanya sesuai jaminan provider. |
| Respons final, sebelum commit hasil lokal | Bank final, lokal belum final. | Inquiry/laporan menyediakan bukti ulang. |
| Commit final/dana/job | Proses dapat mati sebelum respons lokal. | Duplicate/finalisasi ulang mengembalikan hasil existing tanpa efek ganda. |

Claim dan pencatatan hasil masing-masing transaksi DB singkat yang terpisah dari HTTP call. Jangan menahan lock selama menunggu bank. Lease token/generation mencegah stale worker menimpa state, tetapi tidak membatalkan request yang sudah diterima bank. Deduplikasi provider tetap diperlukan untuk attempt yang overlap. Tidak ada jaminan exactly-once delivery dari constraint/lease lokal.

## Dispatch dan bukti provider: tahap 3, dilengkapi tahap 4

Mekanisme lokal berupa endpoint merchant POST /transfers/{id}/dispatch yang sinkron dan worker polling terpisah. Create/duplicate tetap hanya mengakses state lokal. Claim memakai SELECT FOR UPDATE SKIP LOCKED, kemudian UPDATE PROCESSING/token/expiry/counter dan INSERT transfer_attempts dalam transaksi pendek READ COMMITTED. Endpoint manual hanya memilih PENDING milik merchant; worker memilih PENDING/PROCESSING jatuh tempo. Jika INSERT attempt/commit gagal, bank tidak dipanggil. Pesaing claim yang kalah mengembalikan existing tanpa submit. Pekerjaan tetap berada di row transfer, tanpa antrean di goroutine. Setiap attempt melakukan inquiry dahulu; submit bersyarat hanya setelah NOT_FOUND dan guard scope/epoch/window tervalidasi.

Setelah commit claim, pool/lock DB dilepas. HTTP client mengirim reference/payload/akun existing, dengan deadline PROVIDER_TIMEOUT, cancellation caller, body limit 8 KiB, Close pada semua respons, serta redirect/proxy dimatikan. Hanya body HTTP 200 yang cocok seluruh payload, provider account/reference, operation ID, dan status bisnis diketahui yang dapat menjadi bukti final. REJECTED wajib reason code. HTTP 200 ACCEPTED tetap PROCESSING. 504/500/409, malformed/trailing/oversize body, mismatch, atau transport error tidak menjadi FAILED.

Setelah HTTP, transaksi DB terpisah mengunci row sebentar untuk menerapkan hasil. Status hanya berubah dari PROCESSING ke final; evidence dan outcome attempt commit bersama. Final bertentangan tercatat FINAL_CONFLICT/CONTRADICTING_FINAL_EVIDENCE tanpa overwrite. Hasil nonfinal terlambat juga tidak menurunkan final. Pencatatan hasil memakai context baru dengan DB_TIMEOUT meskipun caller canceled. Crash atau kegagalan pencatatan meninggalkan PROCESSING/STARTED dengan next_attempt_at pada expiry lease; worker tahap 4 mengambilnya kembali dengan token baru. Dispatch manual memakai fencing yang sama dengan worker.

Mock memakai INSERT ON CONFLICT (provider_account_id,provider_reference) DO NOTHING RETURNING, kemudian SELECT terpisah. Bearer menentukan scope akun. Seluruh payload immutable dibandingkan; conflict tidak menimpa operasi. Satu row operasi durable mewakili satu transfer efektif; banyak HTTP request bukan banyak operasi. ID/status tetap ada setelah proses restart dan tidak memiliki expiry. Menghapus volume/rollback bank mengakhiri retensi tersebut.

Skenario process config hanya berlaku untuk operasi baru. success_504 melakukan commit SUCCEEDED sebelum HTTP 504; success_timeout melakukan commit sebelum menunggu timer/cancellation. Duplicate selalu mengembalikan operasi existing melalui HTTP 200, meskipun scenario proses sudah berubah. ACCEPTED tidak berubah otomatis menjadi final pada tahap 3. Aplikasi tahap 4 memakai inquiry untuk recovery, dengan epoch/window sebagai syarat tambahan sebelum resubmit. Detail [recovery](recovery.md).

## Saldo simulasi dan rekonsiliasi (tahap 5)

Merchant memiliki satu saldo untuk source account terkonfigurasi: `available` dan `held` BIGINT IDR. Operator membuat saldo pembuka eksplisit melalui CLI; bukan endpoint deposit. Create menyimpan transfer/provider reference, reservasi conditional `available >= amount`, gerakan RESERVE, dan job durable dalam satu transaksi. Duplicate existing diverifikasi sebelum reservasi. Jika reservasi/jurnal gagal, transaksi rollback tanpa row atau saldo parsial. Legacy rows ditandai `LEGACY`; PENDING/PROCESSING legacy diberi review dan scheduler dihentikan. Tidak ada saldo atau mutasi historis yang ditebak, dan saldo pembuka baru tidak mengadopsi transaksi tersebut.

Settlement memakai `settleFunds` untuk dispatch/inquiry dan laporan. Caller mengunci transfer lalu saldo; urutan konsisten. SUCCESS mengonsumsi held, FAILED final mengembalikannya ke available. `fund_movements` mencatat OPEN/RESERVE/CAPTURE/RELEASE dengan constraint satu pembukaan, reservasi, dan settlement per transfer. Status, dana, evidence/attempt atau audit, dan jadwal commit bersama. Hasil ambigu, ACCEPTED, missing, mismatch, atau retry habis tetap held.

Mock bank menghasilkan snapshot CSV immutable per akun/report ID, dari operasi bank sampai cutoff. Import memakai encoding/csv, batas ukuran, fetch ulang sumber, verifikasi byte/hash/scope/epoch, serta menyimpan CSV mentah dan findings/keputusan. Reimport identik memakai audit yang sama. Hanya snapshot lengkap dengan payload cocok dan epoch sama dapat memfinalisasi SUCCEEDED/REJECTED pada transfer nonfinal. Missing row bukan bukti gagal. Konflik final, mismatch, duplicate, invalid, bank-only, dan laporan incomplete menjadi review; tidak mengoreksi status/dana.

Inquiry not found tetap tidak otomatis FAILED atau resubmit kecuali kontrak provider mengizinkan. Batas model saldo, format CSV, migrasi legacy, dan demo ada di [dokumen dana dan rekonsiliasi](funds-reconciliation.md).

## Batas tahap dan review

| Tahap | Scope |
| --- | --- |
| 1, diterima | Setup, health/readiness, config/lifecycle, kontrak dan desain. |
| 2, diterima | Schema, create/get, merchant authentication demo, idempotency/conflict. |
| 3, diterima | Mock bank durable, dispatch eksplisit, simulasi 504/timeout, bukti attempt. |
| 4, diterima | Inquiry, durable backoff/jitter, lease/token, crash recovery, dan manual review. |
| 5, diterima | Reservasi/finalisasi dana dan laporan rekonsiliasi. |
| 6, selesai menurut review pengguna | Verifikasi lintas fitur, concurrency/CI/observability/load; batas bukti ada di verification.md. |
| 7, disiapkan untuk review | README, demo, diagram, dan draft artikel lokal; tidak ada publikasi otomatis. |

Setiap tahap memiliki test sesuai scope dan berhenti untuk review. Test async memakai sinkronisasi/clock terkontrol; constraint dan concurrency database wajib PostgreSQL asli. Tidak ada Redis, broker, Kubernetes, ORM, HTTP framework, generic repository, atau DI container. Publikasi/push tidak otomatis.
