# HTTP 504 Bukan Berarti Transfer Gagal: Membangun Transfer Recovery dengan Go dan PostgreSQL

> Draft artikel berbahasa Indonesia untuk review. Seluruh bank dan dana dalam tulisan ini adalah simulasi lokal pada proyek transfer-101. Tautan source/test relatif ke repository; publikasi belum dilakukan.

Sebuah request transfer dapat berakhir dengan HTTP 504 walaupun bank sudah menyimpan hasil sukses. Aplikasi hanya mengetahui bahwa respons yang ditunggunya tidak memberi kepastian. Jika langsung menandai gagal dan mengirim transfer baru dengan reference baru, satu intent merchant berisiko berubah menjadi dua operasi bank.

transfer-101 membuat celah tersebut terlihat. Mock bank menyimpan `SUCCEEDED` di PostgreSQL miliknya, commit, lalu mengembalikan 504. Aplikasi mempertahankan `PROCESSING`, menahan dana, dan mencari hasil lewat inquiry menggunakan reference yang sama. Ketika bukti sukses ditemukan, status dan dana difinalisasi dalam satu transaksi lokal.

Tujuan simulasi ini adalah memperlihatkan batas commit, sumber bukti, dan konsekuensi crash. Stack-nya Go minimal 1.26, PostgreSQL, SQL langsung, dan standard library dengan satu driver PostgreSQL `lib/pq`. Tidak ada broker atau transaksi terdistribusi.

## Timeout menyisakan pertanyaan

Ada setidaknya tiga kemungkinan ketika respons tidak diterima: request belum sampai ke bank, bank masih memprosesnya, atau bank sudah selesai tetapi respons hilang. Respons transport saja tidak membedakan ketiganya. Karena itu `FAILED` memerlukan bukti penolakan bisnis final, bukan sekadar error jaringan.

HTTP 200 juga perlu dibaca menurut kontrak body. Mock membedakan `ACCEPTED`, `SUCCEEDED`, dan `REJECTED`. ACCEPTED belum final; REJECTED wajib memiliki reason code. Client aplikasi memeriksa akun, reference, operation ID, nominal, mata uang, dan tujuan sebelum menerima hasil final. Timeout, 504/500, body rusak, atau payload mismatch tetap ambigu. Implementasinya berada di [provider.go](../internal/transfer/provider.go), dengan [test parser/transport](../internal/transfer/provider_test.go) dan [test dispatch PostgreSQL](../internal/transfer/dispatch_postgres_integration_test.go).

Empat status lokal cukup untuk model ini:

| Status | Makna |
| --- | --- |
| PENDING | Identitas, payload, pekerjaan, dan reservasi sudah tersimpan. |
| PROCESSING | Dispatch dimulai; hasil eksternal bisa belum diketahui. |
| SUCCESS | Ada bukti SUCCEEDED final yang cocok. |
| FAILED | Ada bukti REJECTED final yang cocok. |

`needs_review` adalah metadata tambahan. Menghabiskan budget retry tidak menghasilkan pengetahuan baru tentang hasil bank, sehingga transfer tetap PROCESSING.

## Dua batas idempotency

Merchant memakai `(merchant_id, reference_id)`. Merchant ID berasal dari context autentikasi server. Payload pembanding adalah amount, bank_code, dan account_number. Key/payload sama mengembalikan transaksi existing; perubahan salah satu field menghasilkan 409, termasuk ketika transfer sudah SUCCESS atau FAILED.

Potongan berikut diambil dari [store.go](../internal/transfer/store.go):

```go
err = tx.QueryRowContext(ctx, `
    INSERT INTO transfers (id, merchant_id, source_account_id, provider_account_id,
        reference_id, provider_reference, amount, bank_code, account_number)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
    ON CONFLICT (merchant_id, reference_id) DO NOTHING
    RETURNING id`, id, merchant.ID, merchant.SourceAccountID, merchant.ProviderAccountID,
    request.ReferenceID, providerReference, request.Amount, request.BankCode, request.AccountNumber).Scan(&id)
```

Jika insert tidak mengembalikan row, kode melakukan SELECT dalam statement terpisah. Transaksi menggunakan READ COMMITTED, sehingga statement kedua dapat membaca snapshot setelah insert pesaing selesai. Payload existing dibandingkan sebelum reservasi. Reservasi hanya terjadi pada insert baru, dan commit menyimpan transfer/reference/job/dana bersama.

Di bank, key-nya berbeda: `(provider_account_id, provider_reference)`. Reference itu dibuat aplikasi dan tersimpan sebelum HTTP pertama. Mock menyimpan payload dan hasil secara durable, lalu mengembalikan operation ID existing pada submit identik, termasuk concurrent submit dan setelah restart proses. Reference sama dengan payload berbeda menjadi conflict. [Implementasi mock](../internal/mockbank/mockbank.go) dan `TestPostgresProviderConcurrentIdempotencyAndScope` dalam [test provider PostgreSQL](../internal/mockbank/postgres_integration_test.go) memeriksa batas ini.

Constraint merchant memilih satu row lokal. Ia tidak dapat menentukan berapa kali sistem lain memproses HTTP. Karena itu deduplikasi provider tetap diperlukan, dan retry tidak boleh mengganti reference demi mendapat respons baru.

## Dana mengikuti bukti

Saldo simulasi memiliki `available` dan `held`, semuanya BIGINT rupiah IDR. Create baru memindahkan amount dari available ke held. Query aktual di [funds.go](../internal/transfer/funds.go) memakai conditional update:

```sql
UPDATE merchant_funds SET available=available-$3,held=held+$3
WHERE merchant_id=$1 AND source_account_id=$2 AND available>=$3
```

Jika tidak ada row yang berubah, create gagal dengan 422 dan seluruh transaksi di-rollback. Insert transfer tidak tertinggal tanpa dana. Request duplicate tidak menjalankan reservasi lagi.

SUCCESS mengonsumsi held. FAILED final mengembalikan held ke available. PROCESSING atau manual review mempertahankan held agar dana yang hasil transfernya belum pasti tidak dipakai lagi. Status, state dana, movement, evidence/audit, dan jadwal diperbarui atomik di PostgreSQL aplikasi.

Dispatch, inquiry, dan rekonsiliasi memakai fungsi `settleFunds` yang sama. Selain lock dan pemeriksaan state, [migration dana](../internal/transfer/migrations/20261021000300_funds_reconciliation.sql) menambahkan batas berikut:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS fund_settle_once ON fund_movements(transfer_id) WHERE kind IN ('CAPTURE','RELEASE');
```

Dengan demikian satu transfer tidak dapat memiliki CAPTURE dan RELEASE sekaligus. Ini catatan mutasi minimum untuk simulasi saldo, bukan framework general ledger. Data sebelum fitur saldo ditandai LEGACY, tidak diberi saldo historis hasil tebakan, dan transaksi lama yang nonfinal dihentikan untuk review.

## Worker harus bisa kehilangan prosesnya

Pekerjaan disimpan pada row transfer: jenis job, next_attempt_at, counter, budget, lease token/expiry, dan alasan review. Worker mengambil row due memakai `FOR UPDATE SKIP LOCKED` dalam transaksi singkat, mengubahnya menjadi PROCESSING, mencatat attempt, dan commit sebelum HTTP.

Implementasi saat ini mengawali setiap attempt dengan inquiry, termasuk pengiriman pertama. Jika NOT_FOUND memenuhi kontrak resubmit, barulah provider dihubungi dengan POST reference/payload yang sama. Jadwal setelah claim diarahkan ke lease expiry, sehingga crash sebelum hasil disimpan tetap meninggalkan pekerjaan durable.

Ketika proses baru mengambil pekerjaan yang lease-nya kedaluwarsa, ia mendapat token baru. Worker lama mungkin masih memiliki HTTP request yang aktif; ia tidak boleh menimpa claim atau hasil worker baru. Di [recovery.go](../internal/transfer/recovery.go), penyimpanan status ditutup dengan predicate berikut sebagai bagian UPDATE:

```sql
WHERE id=$1 AND lease_token=$2 AND lease_until>clock_timestamp()
```

Bila fence tidak menghasilkan row, transaksi hasil di-rollback, termasuk perubahan saldo. Lease melindungi penulisan lokal; request HTTP lama tidak otomatis hilang dari bank. Deduplikasi provider melindungi operasi bank ketika attempt overlap.

Retry memakai exponential backoff dengan cap dan jitter antara separuh cap hingga cap. Delay dipilih sekali, lalu due time disimpan di DB. Default budget delapan claim mencakup pengiriman awal, inquiry, dan claim yang crash; satu claim dapat melakukan inquiry lalu submit bersyarat. Jika budget habis, jadwal otomatis berhenti dan transfer tetap PROCESSING/held dengan `RETRY_BUDGET_EXHAUSTED`.

## Not found belum memberi izin transfer baru

Mock memiliki epoch dataset yang bertahan bersama histori operasi. Aplikasi menyimpan epoch sebelum POST pertama dan window resubmit 15 menit sejak create. Resubmit harus memakai akun/reference/payload existing, epoch yang sama, serta window dan lease yang masih valid. Mock memeriksa guard itu dalam transaksi yang sama dengan deduplikasi.

Jika epoch berganti, window habis, credential scope salah, atau jaminan retensi tidak tersedia, NOT_FOUND tidak cukup untuk mengirim ulang. Aplikasi melanjutkan inquiry terbatas atau meminta review sesuai kondisi. [Kontrak provider](provider-contract.md) menjelaskan guard dan batasnya. Administrator yang menghapus histori tanpa merotasi epoch melanggar asumsi kontrak; kode ini tidak mengatasi pelanggaran tersebut.

## Inquiry dan rekonsiliasi menjawab pertanyaan berbeda

Inquiry menanyakan keadaan satu provider reference. Rekonsiliasi membandingkan snapshot laporan bank dengan pencatatan lokal untuk menemukan transaksi bank-only, missing, nominal/tujuan berbeda, baris invalid/duplikat, dan konflik status final.

Mock mengekspor CSV immutable per akun/report ID. Import menggunakan `encoding/csv`, mengambil ulang sumber dari bank, dan membandingkan byte/hash/scope. File lokal yang diedit bukan bukti final. Hanya snapshot lengkap dan verified dengan epoch/payload cocok yang boleh menyelesaikan transfer nonfinal melalui settlement bersama. Missing tidak otomatis FAILED; bukti berlawanan dengan status final menjadi finding review tanpa overwrite.

[reconciliation.go](../internal/transfer/reconciliation.go) menyimpan input CSV, hash, findings, dan keputusan. Import identik mengembalikan audit existing, sehingga reimport tidak mengulang capture/release. Test [rekonsiliasi PostgreSQL](../internal/transfer/reconciliation_postgres_integration_test.go) juga menjalankan inquiry dan import bersamaan untuk memeriksa bahwa satu hasil final menghasilkan satu efek dana.

## Bagaimana perilakunya dibuktikan

Test database memakai PostgreSQL asli, database/role `transfer_test`, schema fixture acak, dan migration dbmate. Unit test tanpa tag integration tidak membuktikan constraint atau transaksi PostgreSQL. Batas race/CI/load yang telah dan belum dieksekusi tercatat di [verification.md](verification.md), bukan disimpulkan hanya dari keberadaan file test.

| Skenario | Bukti di repository |
| --- | --- |
| Sukses dan penolakan final | `TestPostgresDispatchOutcomes` di [dispatch tests](../internal/transfer/dispatch_postgres_integration_test.go). |
| Bank sukses lalu 504, akhirnya SUCCESS | `TestPostgresRecovery504AndPersistentSchedule` di [recovery tests](../internal/transfer/recovery_postgres_integration_test.go). |
| Duplicate/conflict dan isolasi merchant | [Idempotency tests](../internal/transfer/postgres_integration_test.go), termasuk concurrent insert. |
| Crash di batas commit | `TestPostgresRecoverySIGKILLAndRestartAtCommitBoundaries` di [process tests](../internal/transfer/recovery_process_integration_test.go). |
| Dua worker, lease expired, request overlap | `TestPostgresRecoveryTwoWorkersAndSkipLocked`, `TestPostgresRecoveryLeaseExpiryFencesEveryWrite`, dan `TestPostgresRecoveryOverlappingSubmitAfterLeaseExpiry` di recovery tests. |
| Tidak overspend dan tidak settle dua kali | `TestPostgresFundsConcurrentTransfersCannotOverspend`, `TestPostgresFundsConcurrentFinalizationExactlyOneEffect` di [funds tests](../internal/transfer/funds_postgres_integration_test.go). |
| Reimport dan mismatch | [Reconciliation tests](../internal/transfer/reconciliation_postgres_integration_test.go). |

Test crash menjalankan child process, menunggu channel/barrier pada fase sebelum claim, sesudah claim, bank commit sebelum response, sesudah HTTP, dan sesudah final commit. Proses dikirim SIGKILL, kemudian worker baru memulihkan pekerjaan. Test mengatur due time pada fixture untuk memeriksa expiry tanpa tidur menunggu lease wall-clock. Assertion memeriksa reference yang sama, satu operasi bank efektif, serta pekerjaan final yang tidak aktif lagi.

Bukti tahap 6 mencatat unit/race unit lulus pada Go 1.26.2 darwin/arm64, lima suite integration/process dan focused tests 20 kali lulus pada PostgreSQL 17.11 Linux arm64. Integration race final dan CI remote belum terverifikasi. Load run menemukan bug query audit saldo yang sudah diperbaiki, tetapi run correctness lengkap sesudah perbaikan belum tersedia karena izin Docker. Karena itu tulisan ini tidak menarik kesimpulan kapasitas atau benchmark dari angka traffic tersebut.

## Mencoba dan menilai batasnya

[Panduan demo](demo.md) menyediakan setup, sukses, penolakan final, duplicate/conflict, 504/recovery, crash/restart, serta rekonsiliasi. Tiga [diagram](diagrams.md) menunjukkan batas commit dan perubahan dana. Proses dapat dihentikan/dijalankan ulang tanpa menghapus database demo.

Model ini menukar sejumlah kenyamanan dengan state yang dapat diperiksa: polling membebani DB, lease tanpa heartbeat perlu konfigurasi timeout yang konsisten, dan dana yang belum pasti tetap held sampai ada bukti. Mock tidak otomatis mengubah ACCEPTED menjadi final. Saldo hanya IDR dengan satu sumber dana, tanpa biaya/top-up/FX; laporan maksimum 1.000 baris/8 MiB. Auth bearer statis dan HTTP loopback belum menyediakan kebutuhan operasional bank nyata.

Tidak ada klaim exactly-once lintas sistem. Jaminan yang dicari lebih terbatas dan dapat diuji: satu intent lokal per merchant/reference, deduplikasi operasi bank selama kontrak provider berlaku, serta efek dana lokal yang atomik dan tidak digandakan saat hasil final dicatat ulang. Ketika bukti belum cukup, aplikasi mempertahankan ketidakpastian itu dalam status dan dana yang bisa direview.
