# Verifikasi tahap 4: recovery

Tanggal: 21 Oktober 2026. Native Go 1.26.2 darwin/arm64; PostgreSQL asli 17.11 (Debian 17.11-1.pgdg12+2); dbmate 2.28.0. Race integration dijalankan pada image golang:1.26.2-bookworm, Linux/arm64, CGO_ENABLED=1. Tahap 3 sudah diterima sebelum pekerjaan ini.

## Temuan dan keputusan sebelum implementasi

1. Dispatch tahap 3 mempunyai claim pendek tetapi belum memakai fencing. Tahap 4 memakai claim/token/expiry yang sama untuk dispatch dan worker, agar respons dari pemilik lease lama tidak menulis state baru.
2. NOT_FOUND dan deduplikasi sampai reset belum cukup untuk memutuskan resubmit setelah histori bank hilang. Mock kini mempunyai epoch dataset durable, scope header, dan guard deadline; aplikasi menyimpan epoch sebelum POST serta window 15 menit. Legacy PROCESSING tanpa bukti epoch awal tidak diberi izin resubmit otomatis.
3. Validasi scope hanya pada body final dapat terlambat untuk credential salah. Inquiry mengenali akun terautentikasi sebelum POST; scope mismatch masuk review tanpa operasi bank.
4. Pemeriksaan clock bank dilakukan sesudah lock retensi diperoleh, untuk menghindari timestamp dari sebelum lock wait. Fence aplikasi memeriksa expiry pada UPDATE terakhir dalam transaksi hasil.

## File yang perlu direview

| File/kelompok | Perubahan |
| --- | --- |
| internal/transfer/recovery.go | Scheduler row transfer, claim SKIP LOCKED, token/expiry, authorization resubmit, backoff/jitter, finish atomik, dan loop worker/shutdown. |
| internal/transfer/dispatch.go | Dispatch manual memakai claim, perform dan finish yang sama dengan worker. |
| internal/transfer/provider.go | Inquiry GET, parser NOT_FOUND, scope/epoch header, submit guard, dan klasifikasi manual review. |
| internal/transfer/transfer.go, store.go, schema.go | GET needs_review/review_reason dan startup check recovery; algoritma idempotency merchant dipertahankan. |
| internal/transfer/migrations/20261021000100_recovery.sql | Metadata durable, partial due index, attempt kind/token, backfill legacy, up/down. |
| internal/mockbank/migrations/20261021000200_retention_epoch.sql, mockbank.go | Epoch durable, scope header, guard POST dalam transaksi bank, dan inquiry not-found fixture. |
| internal/transfer/recovery_test.go | Jitter/cap serta inquiry timeout/not-found/closure unit tests. |
| internal/transfer/recovery_postgres_integration_test.go | Recovery, jadwal persisted, dua worker, stale lease, overlap submit, budget, retensi/window, credential scope, dan NOT_FOUND sementara tanpa izin resubmit lalu inquiry final. |
| internal/transfer/recovery_process_integration_test.go | Worker loop di OS process, SIGKILL pada lima batas dan restart proses baru. |
| internal/transfer/dispatch_postgres_integration_test.go, postgres_integration_test.go | Test tahap sebelumnya dipertahankan; barrier kini mengamati POST setelah preflight inquiry; tiga versi migrasi dan rollback diuji. |
| internal/mockbank/postgres_integration_test.go | Guard epoch/window salah ditolak tanpa operasi tambahan. |
| cmd/worker/main.go | Proses worker standalone, env/config, schema check dan signal shutdown. |
| cmd/api/main.go, postgres_integration_test.go | Budget write timeout dua HTTP/tiga DB; smoke API, worker live dan demo CLI recovery. |
| cmd/demo-provider/main.go | Mode wait status dengan deadline, early exit manual review, identitas existing tetap diperiksa. |
| cmd/mockbank/main.go, .env.example, .env.mockbank.example | Konfigurasi worker dan fixture inquiry not-found. File env pengguna tidak diubah. |
| compose.race.yaml | Runner Go/CGO khusus test dengan source read-only dan cache test tersendiri; bukan runtime aplikasi. |
| scripts/build-container-tests.sh, run-container-tests.sh | Artefak recovery-worker dan preflight migrasi tahap 4; tetap lima suite test. |
| README.md, docs/api-contract.md, provider-contract.md, design.md, recovery.md, demo-provider.md | Kontrak aktual, upgrade dari writer lama, demo, trade-off dan batas tahap. |

Go minimum tetap 1.26.0. Module aplikasi hanya memakai github.com/lib/pq v1.12.3 di luar standard library. Tidak ada Redis, broker, ORM, HTTP framework, generic repository, DI container, saldo, reservasi, atau laporan rekonsiliasi.

## Command reproduksi

Semua dari root repository. Service test, database dan role terpisah dari demo; entrypoint Compose menyiapkannya, lalu fixture menjalankan dbmate asli pada schema acak sendiri.

```sh
docker compose --profile test up -d --wait postgres-test
sh scripts/build-container-tests.sh arm64
docker compose exec -T postgres-test mkdir -p /tmp/transfer-101-tests
docker compose cp bin/integration/. postgres-test:/tmp/transfer-101-tests
docker compose exec -T -e 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:5432/transfer_test?sslmode=disable&connect_timeout=3' postgres-test sh /tmp/transfer-101-tests/run-container-tests.sh /tmp/transfer-101-tests
```

Host script memakai arm64 untuk target aarch64. amd64 didukung script tetapi belum diverifikasi. CGO_ENABLED=0 pada artefak biasa berarti suite ini tanpa race detector. dbmate Linux dan lima binary test serta CLI demo diverifikasi executable sebelum suite. Binary worker juga dibangun; proses crash test menjalankan loop RecoveryWorker.Run dari test executable di proses terpisah.

Focused PostgreSQL diulang 10 kali:

```sh
docker compose exec -T -e 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:5432/transfer_test?sslmode=disable&connect_timeout=3' -e DBMATE_BIN=/tmp/transfer-101-tests/dbmate -e DBMATE_MIGRATIONS_DIR=/tmp/transfer-101-tests/migrations -e MOCK_BANK_MIGRATIONS_DIR=/tmp/transfer-101-tests/mockbank-migrations postgres-test /tmp/transfer-101-tests/transfer.test -test.run '^TestPostgresRecovery' -test.count=10 -test.timeout=180s
```

Focused PostgreSQL + race detector, lima kali:

```sh
docker compose -f compose.yaml -f compose.race.yaml --profile test run --rm --no-deps recovery-race
```

Seluruh package + PostgreSQL + race detector:

```sh
docker compose -f compose.yaml -f compose.race.yaml --profile test run --rm --no-deps recovery-race go test -race -tags=integration -count=1 -timeout=180s ./...
```

Runner race memakai Go SDK dengan CGO dan compiler C, mount source read-only, cache Go/module khusus test, serta PostgreSQL postgres-test di network Compose. Image/module dapat diunduh pada run awal; tidak menambah dependency module aplikasi atau mengubah container demo. Build artefak diperlukan sebelum race karena CLI smoke test memakai demo-provider dan dbmate Linux.

Untuk native unit/race dan pemeriksaan statik:

```sh
gofmt -l cmd internal
go mod verify
go build ./...
go vet -tags=integration ./...
go test -race -count=1 -timeout=60s ./...
sh -n scripts/build-container-tests.sh scripts/run-container-tests.sh scripts/demo-provider.sh
docker compose -f compose.yaml -f compose.race.yaml config --quiet
```

Pada sesi agent cache default dibatasi; Go host memakai env GOCACHE=/tmp/transfer-101-go-build GOPROXY=off. Tidak ada konfigurasi global yang diubah.

## Hasil yang benar-benar dijalankan

| Pemeriksaan | Hasil aktual |
| --- | --- |
| Native unit/race | Exit 0; package bertest lulus. |
| Build dan vet bertag integration | Exit 0. |
| gofmt, module verify, syntax script, Compose config | Exit 0. |
| Build artefak Linux/arm64 | Exit 0; dbmate, SQL kedua fitur, lima test binary, demo CLI dan worker tersedia. |
| Full runner PostgreSQL biasa | Exit 0, lima suite, 34 top-level test PostgreSQL termasuk subtest crash/retensi/budget. |
| Focused recovery -count=10 | Exit 0, PASS. |
| Focused recovery + race -count=5 di Go Docker | Exit 0, PASS. |
| Seluruh package + PostgreSQL + race di Go Docker | Exit 0, seluruh package bertest lulus. |
| API + worker + CLI demo recovery | Lulus; output recovered=SUCCESS dengan reference existing. |
| Crash proses | SIGKILL diverifikasi melalui exit signal; restart proses baru memulihkan state durable. |
| Sisa schema fixture | transfer_test/current_user transfer_test; jumlah schema fixture tersisa 0 setelah seluruh suite. |

Delapan artefak (lima test binary, demo-provider, recovery-worker, dbmate) mempunyai SHA-256 identik antara host dan container tujuan. Runner menjalankan binary tersebut di Linux/arm64. Fingerprint pg_dump database aplikasi demo sebelum/sesudah sesi tetap b7eac75341753de8f84e3d8d310f371fdff798a71a21eb1dc275163c1fb1cb7f. Database demo tidak di-reset dan migrasi tahap 4 belum diterapkan ke demo. Folder kerja belum mempunyai metadata .git, sehingga daftar perubahan disajikan per file dan belum ada commit.

Percobaan native focused PostgreSQL pertama tidak menyertakan TEST_DATABASE_URL dan ditolak guard (exit 1). Setelah URL dikutip/diset, host tetap menolak koneksi loopback dengan operation not permitted (exit 1). Keduanya bukan test yang lulus. Runner Go Docker kemudian benar-benar menjalankan focused PostgreSQL berulang dengan race serta seluruh package dengan race; batas host tidak dipakai sebagai alasan untuk mengklaim integrasi sudah lulus.

Output mentah tersedia lokal pada bin/integration/recovery.log, recovery-focused.log, recovery-race.log, dan recovery-all-race.log, diabaikan Git. Reproduksi menggunakan PostgreSQL asli, bukan mock SQL. Guard memverifikasi transfer_test/current_user sebelum DDL; dbmate hanya menjalankan migrasi pada search_path fixture. Tidak ada CREATE DATABASE/SQL setup manual yang tidak didokumentasikan.

## Validasi ulang setelah permintaan tahap 4

Implementasi recovery sudah tersedia saat pemeriksaan ulang. Tidak dibuat ulang. Ditambahkan TestPostgresRecoveryTemporaryNotFoundThenVisibleWithoutResubmit untuk menutup gap bukti: bank telah SUCCEEDED, inquiry sementara menyembunyikan operasi dengan NOT_FOUND/scope/epoch valid, dan izin resubmit lokal NULL. Worker tetap PROCESSING tanpa POST tambahan; inquiry berikutnya menemukan bukti SUCCEEDED, lalu aplikasi SUCCESS. Reference tetap sama, counter durable menjadi tiga, dan bank mempunyai satu operasi. Test memakai atomic flag dan due time fixture, tanpa time.Sleep.

Komentar bank diperbaiki agar menyebut commit transaksi eksplisit; kalimat duplikat pada kontrak inquiry dihapus. Tidak ada perubahan perilaku worker, schema, atau dependency pada pemeriksaan ulang ini. Go host tetap 1.26.2 dan module minimum 1.26.0.

Semua command pemeriksaan di atas dijalankan ulang: native unit/race, build/vet, format/module/script/Compose, lima suite PostgreSQL (34 test utama), focused recovery sepuluh kali, focused PostgreSQL dengan race lima kali, dan seluruh package dengan PostgreSQL/race. Semuanya exit 0. Focused race menghasilkan ok internal/transfer 34.133s; ini durasi suite, bukan benchmark. API/worker/demo smoke kembali menghasilkan recovered=SUCCESS. Delapan artefak host/container cocok, schema fixture tersisa nol, dan fingerprint demo tetap sama.

Log terbaru: bin/integration/recovery-recheck.log, recovery-recheck-focused.log, recovery-recheck-race.log, recovery-recheck-all-race.log. Log tanpa recheck adalah hasil sebelum penambahan test, sehingga jumlahnya 33. Dua percobaan Docker dengan redirect stdout ke log ditolak sandbox pada akses socket daemon (exit 1), sebelum test berjalan. Command Docker langsung tanpa redirect kemudian dijalankan dan benar-benar lulus; outputnya disimpan setelah command selesai. Kegagalan lingkungan awal tersimpan pada recovery-recheck-environment-failures.log.

## Bukti tiap kasus

- Bank commit SUCCEEDED lalu submit 504: aplikasi PROCESSING, due time/counter tersimpan; instance worker baru menghormati backoff, lalu inquiry menjadikannya SUCCESS. Satu operasi bank, reference tidak berubah.
- Dua worker bersamaan: satu pemenang untuk satu job. Row yang dikunci tidak menghalangi job lain berkat SKIP LOCKED.
- Inquiry/POST ditahan barrier: koneksi lain berhasil FOR UPDATE NOWAIT. Lock aplikasi sudah dilepas selama HTTP.
- Lease expired tanpa reclaim: save owner lama ditolak. Setelah token baru terbit, owner lama tidak dapat menulis, reschedule, atau melepas lease baru. Hasil lama juga tidak menurunkan final/menandai review palsu.
- POST lama masih aktif setelah expiry: worker baru melakukan inquiry lalu guarded submit dengan reference sama; dua HTTP POST menghasilkan satu bank_operations dan final yang sama. Save owner lama ditolak.
- NOT_FOUND dengan dataset sama/window valid: guarded resubmit existing aman dan satu operasi efektif. Dataset berubah atau window habis: tidak ada POST tambahan, status tetap PROCESSING/review.
- NOT_FOUND sementara tanpa izin resubmit: PROCESSING dipertahankan, kemudian inquiry final menyelesaikan SUCCESS tanpa POST kedua; counter tiga dan satu operasi efektif bank.
- Credential akun salah: mismatch diketahui dari inquiry sebelum POST; bank operation count nol.
- Mock menolak epoch/deadline salah dengan 412 dalam transaksi bank; operasi tidak bertambah.
- Timeout, NOT_FOUND tanpa otorisasi resubmit, dan ACCEPTED berulang mencapai budget: PROCESSING + needs_review, tanpa FAILED palsu atau retry tak terbatas.
- Backoff/counter/lease tidak direset oleh instance/process baru. Future due time tidak dipercepat saat restart; clock yang dipakai lease/schedule adalah PostgreSQL.
- SIGKILL pada lima batas: setelah create sebelum claim; setelah claim sebelum HTTP; setelah bank commit sebelum response; setelah response final sebelum update lokal; setelah final/job commit. Proses baru memakai akun/reference/payload existing; final/job commit tidak diulang, satu operasi efektif bank. Lease dibuat expired secara eksplisit pada schema fixture, tanpa mengubah clock global atau time.Sleep.
- Unit deadline HTTP memakai testing/synctest; channel/hook mengurutkan request tertunda dan crash. Deadline real-time hanya pengaman test macet.
- Smoke API menjalankan worker live dan CLI black box melalui HTTP: local PROCESSING pulih ke SUCCESS, duplicate merchant/provider tetap existing.

## Trade-off, demo dan titik berhenti

[Recovery dan demo](recovery.md) menjelaskan upgrade kedua proses, konfigurasi, epoch/window, query manual review dan keterbatasan. Semua writer lama harus dihentikan sebelum migrasi; proses tahap 3 yang sudah berjalan tidak di-restart otomatis pada sesi implementasi ini. Migrasi/demo volume tidak diterapkan atau di-reset oleh agent. Pengujian hanya memakai service/database test dan schema fixture sendiri. Tidak ada benchmark atau klaim produksi.

Budget dihitung per claim, bukan per HTTP call. Lease tanpa heartbeat dapat menyebabkan overlap; kontrak bank menjaga satu operasi efektif. Global epoch konservatif terhadap reset sebagian dataset. Administrator yang menghapus histori tanpa mengganti epoch melanggar kontrak retensi. ACCEPTED tidak otomatis berubah final pada mock. Perlu manual review berarti pekerjaan tetap terlihat, bukan hasil gagal yang diasumsikan.

Checklist review:

- [ ] Fencing token + expiry berlaku pada dispatch dan worker, tanpa lock selama HTTP.
- [ ] Jadwal/count serta status/evidence/job berikutnya atomik dan durable.
- [ ] NOT_FOUND, scope akun, epoch, window dan legacy policy tidak memicu resubmit yang tidak terjamin.
- [ ] Budget habis/konflik tetap terlihat melalui needs_review dan query operator.
- [ ] Bukti pengulangan, race, crash SIGKILL dan satu operasi efektif diterima sebelum tahap 5.

Berhenti setelah tahap 4. Dana dan rekonsiliasi laporan belum diimplementasikan.
