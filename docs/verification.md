# Verifikasi tahap 6

Tanggal eksekusi: 2026-10-23. Hasil ini mencatat yang benar-benar dijalankan di workspace lokal; GitHub Actions belum dijalankan pada remote.

## Matriks requirement dan bukti

Semua test `TestPostgres...` di bawah adalah integration tests bertag `integration` dan memakai PostgreSQL asli melalui `TEST_DATABASE_URL`; test tidak mengganti PostgreSQL dengan mock.

| Requirement | Test |
|---|---|
| Transfer sukses / penolakan final | `TestPostgresDispatchOutcomes`; `TestPostgresMockBankScenariosAndInquiry` |
| Duplicate identik / payload conflict | `TestPostgresCreateAndDuplicate`; `TestPostgresConflictAndDuplicateInEveryStatus`; `TestPostgresConcurrentIdenticalRequests`; `TestPostgresConcurrentPayloadConflict` |
| Isolasi merchant | `TestPostgresMerchantIsolation` |
| Bank sukses lalu HTTP 504 | `TestPostgresDispatchTimeoutAndCancellationAfterBankCommit`; `TestPostgresRecovery504AndPersistentSchedule`; `TestPostgresFundsTimeoutHeldInquiryCapturesAndRollbackFinal` |
| Crash/restart dan recovery | `TestPostgresRecoverySIGKILLAndRestartAtCommitBoundaries`; `TestPostgresRecovery504AndPersistentSchedule` |
| Concurrent requests/workers dan stale lease | `TestPostgresConcurrentIdenticalRequests`; `TestPostgresRecoveryTwoWorkersAndSkipLocked`; `TestPostgresRecoveryLeaseExpiryFencesEveryWrite`; `TestPostgresRecoveryOverlappingSubmitAfterLeaseExpiry` |
| Reservasi, capture, dan release sekali | `TestPostgresFundsInitializationInsufficientAndRollback`; `TestPostgresFundsConcurrentTransfersCannotOverspend`; `TestPostgresFundsConcurrentFinalizationExactlyOneEffect`; `TestPostgresFundsTimeoutHeldInquiryCapturesAndRollbackFinal` |
| Rekonsiliasi ulang dan mismatch | `TestPostgresReconciliationFinalizesOnceAcrossRepeatedConcurrentReports`; `TestPostgresReconciliationMismatchDuplicateInvalidMissingAndPartial`; `TestPostgresReconciliationSourceConflictFinalConflictAndAudit`; `TestPostgresReconciliationImportRollbackAndRetry`; `TestPostgresReconciliationConcurrentInquiryAndReport` |
| Provider idempotency dan redaksi log | `TestPostgresProviderConcurrentIdempotencyAndScope`; `TestPostgresStructuredTransferLogsAreUsefulAndRedacted` |

## Pemeriksaan kode

| Perintah | Hasil |
|---|---|
| `gofmt -l $(rg --files -g '*.go')` | Lulus; tidak ada file yang tercetak. |
| `go vet ./...` | Lulus. |
| `go test ./...` | Lulus untuk semua package pada host Go `go1.26.2 darwin/arm64`. |
| `go test -race ./...` | Lulus untuk semua package pada host Go `go1.26.2 darwin/arm64`. |
| `docker compose config --quiet` | Lulus. |
| `go mod verify` | Lulus. |
| `sh scripts/build-container-tests.sh arm64` | Lulus; membangun binary test dan executable CLI/API untuk container. |
| `sh /tmp/transfer-101-tests/run-container-tests.sh /tmp/transfer-101-tests` | Lulus sebelumnya pada PostgreSQL `17.11`; kelima suite executable ditemukan dan suite integration/process berakhir exit 0. Test PostgreSQL berjalan, bukan dilewati. |
| Focused `-count=20` | Lulus pada PostgreSQL `17.11` setelah test container dinaikkan menjadi tmpfs 1 GiB. Filter: `TestPostgres(FundsConcurrent|Reconciliation(FinalizesOnceAcrossRepeatedConcurrentReports|ConcurrentInquiryAndReport)|Recovery|ConcurrentIdenticalRequests|ConcurrentPayloadConflict|DispatchClaimCommittedAndUnlocked|StructuredTransferLogs)`. |

Satu pengulangan focused awal gagal saat WAL memenuhi tmpfs 256 MiB (`No space left on device`). Harness test diperbaiki dengan tmpfs 1 GiB dan batas/checkpoint WAL untuk service test; container test dibuat ulang secara khusus. Pengulangan kedua lulus. Database demo tidak dihentikan, di-reset, atau dihapus.

Setelah container test selesai sebelumnya, izin akses Docker daemon dari sesi ini berubah menjadi `permission denied` pada socket `/Users/glenn/.docker/run/docker.sock`. Karena itu integration test/race detector integration suite dan focused `-count=20` tidak dapat dijalankan ulang pada final pass ini. Hasil lulus di atas berasal dari actual runs sebelumnya pada kode tahap ini. Race detector yang berjalan ulang pada host hanya mencakup suite tanpa tag `integration`.

Toolchain proyek dipin ke Go `1.26.10` sebagai patch keamanan terkini saat perubahan ini dibuat; Go locally installed adalah `1.26.2`. Workflow CI menggunakan `1.26.10`, PostgreSQL `17.11`, menjalankan migration melalui test setup, semua integration/process tests, focused `-count=20`, serta race suite biasa dan integration. Workflow telah ditambahkan tetapi belum dieksekusi di GitHub.

## Structured logging

Log JSON API untuk pembuatan dan duplicate berisi `transaction_id`, `provider_reference`, `operation`, `attempt`, `duration_ms`, dan `outcome`. Dispatch dan inquiry/resubmit log menambahkan hasil provider, durasi, alasan/error type, serta apakah hasil tersimpan. Instrumentation diuji oleh `TestPostgresStructuredTransferLogsAreUsefulAndRedacted`; nilai account number penuh dan token tidak tercantum pada record yang diuji. Log bukan audit trail finansial; audit rekonsiliasi/fund movement tetap berada di PostgreSQL.

## Load test: hasil aktual dan batasnya

Runner yang digunakan adalah `cmd/loadtest`, standard library HTTP dan database/sql, dengan driver PostgreSQL proyek. Lingkungan: container Linux `arm64`, Go `go1.26.2`, PostgreSQL `17.11`; API, recovery worker, mock bank, serta database test berada pada Docker test container yang sama. Konfigurasi: 100 request, concurrency 12, batas traffic 10 detik, HTTP timeout 3 detik, completion timeout 60 detik, amount Rp1.000, duplicate 25%, conflict 10%, insufficient 10%, prefix `loadverify2310b`.

Run tersebut mengirim 100 request dalam 41 ms (throughput terukur 2.412,57 request/detik), status `200:19`, `201:61`, `409:10`, `422:10`, error HTTP 0; latency API p50/p95/p99 3/11/14 ms; latency POST yang diterima p50/p95/p99 3/10/18 ms; 71 transfer teramati final dalam 806 ms setelah traffic selesai. Angka ini hanya catatan satu run lokal, bukan benchmark kapasitas.

Pemeriksaan correctness run pertama menemukan bug nyata di runner: query konservasi saldo mengelompokkan hanya dengan `merchant_id`, tetapi mengambil `opening_amount`, `available`, dan `held` secara langsung. PostgreSQL menolak query tersebut. Query diubah menjadi subquery agregat untuk capture dan pemeriksaan unit runner lulus. Run lanjutan yang bisa menyatakan semua invariant lulus belum dapat dilakukan karena akses Docker daemon sesi ini kemudian ditolak. Oleh sebab itu hasil traffic di atas **bukan** run load test yang tervalidasi correctness end-to-end; jangan gunakan sebagai benchmark. Data fixture ber-prefix itu tidak dihapus oleh runner.

Perbaikan query lokal telah dijalankan melalui `go test ./cmd/loadtest`; lulus. Verifikasi ulang load test di container adalah item tertunda.

## CI dan batasan

`.github/workflows/verification.yml` menyediakan PostgreSQL test service terpisah (tidak memakai DB demo), memasang dbmate versi tetap, menjalankan vet/build/unit/integration/focused/race, dan menggunakan migration direktori aplikasi serta mock bank. `compose.yaml` juga memisahkan `postgres-test` dan data test sementara. Run CI remote belum tersedia.

Belum ada klaim benchmark atau kesiapan produksi. Load test terbatas pada HTTP POST dan observasi status lokal, tanpa TLS/proxy/host jarak jauh, tanpa biaya/top-up, dengan asumsi konservasi saldo `available + held + captured = opening_amount`. Durasi pendek dan sampel 100 tidak cukup untuk inferensi kapasitas atau tail latency produksi. Runner tidak membersihkan data setelah run.
