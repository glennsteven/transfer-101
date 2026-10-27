# Pemeriksaan tahap 3: provider

Tanggal: 20 Oktober 2026. Host Go 1.26.2 darwin/arm64. PostgreSQL nyata 17.11 (Debian 17.11-1.pgdg12+2), dbmate 2.28.0. Tahap idempotency sudah diterima sebelum perubahan ini.

## Scope dan file review

| File/kelompok | Perubahan |
| --- | --- |
| internal/transfer/provider.go, provider_test.go | Client HTTP mock, credential per akun existing, validasi bukti, timeout/cancellation, limit dan closure respons. |
| internal/transfer/dispatch.go, dispatch_postgres_integration_test.go | Endpoint dispatch merchant; claim + attempt atomik, HTTP tanpa lock, status/evidence bersyarat, konflik final untuk review. |
| internal/transfer/migrations/20261020000300_dispatch_attempts.sql, schema.go | Migrasi additive up/down transfer_attempts dan startup check versi/kolom. Migrasi transfers awal tidak diubah. |
| internal/transfer/postgres_integration_test.go | Test rollback/rerun disesuaikan dengan dua migrasi aplikasi; pengujian idempotency sebelumnya dipertahankan. |
| internal/mockbank/ | HTTP submit/inquiry, auth akun, penyimpanan operasi durable, SQL up/down dan test PostgreSQL/unit. |
| cmd/mockbank/ | Proses lokal terpisah, konfigurasi, health, startup/shutdown; test restart dua OS process. |
| cmd/api/config.go, main.go, config_test.go, postgres_integration_test.go | Wiring client/dispatch, timeout server, konfigurasi baru; smoke test API nyata dan CLI demo. |
| cmd/demo-provider/main.go, scripts/demo-provider.sh | Demo stdlib Go: create/duplicate, dispatch, inquiry mock, duplicate provider, assertion status/identitas. |
| internal/testdb/migrate.go, mockbank.go | Guard test tetap ke transfer_test; runner dbmate kedua fitur dan schema bank fixture terpisah. |
| scripts/build-container-tests.sh, run-container-tests.sh | Lima binary test Linux, CLI demo, dbmate dan SQL kedua fitur; preflight executable/daftar test. |
| compose.yaml | Service/database/volume mock bank tersendiri, port 54331. Service demo/test existing dipertahankan. |
| .env.example, .env.mockbank.example, .gitignore, config/provider-accounts.example.json | Contoh konfigurasi publik, default lokal, file env/token lokal diabaikan Git. File .env pengguna tidak diubah. |
| README.md, docs/api-contract.md, provider-contract.md, design.md, demo-provider.md | Kontrak aktual, keputusan/batas tahap, quickstart dan demo. |

Tidak ada dependency Go baru: module tetap go 1.26.0 dan github.com/lib/pq v1.12.3. Tidak ada worker recovery, retry otomatis, lease, saldo, atau laporan rekonsiliasi.

## Reproduksi native

Unit/race tidak memerlukan PostgreSQL atau socket OS:

```sh
gofmt -l cmd internal
go mod verify
go build ./...
go vet -tags=integration ./...
go test -race -count=1 -timeout=60s ./...
sh -n scripts/build-container-tests.sh scripts/run-container-tests.sh scripts/demo-provider.sh
docker compose config --quiet
```

Integrasi native memerlukan Go dan dbmate di PATH, serta service test terpisah:

```sh
docker compose --profile test up -d --wait postgres-test
env 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:54330/transfer_test?sslmode=disable&connect_timeout=3' go test -race -tags=integration -count=1 -timeout=120s -v ./...
```

Command native combined race+PostgreSQL di atas belum dijalankan di sandbox agent. Hasil aktual PostgreSQL menggunakan binary Linux tanpa race detector, terpisah dari native unit/race. Quote penuh NAME=value menjaga ? dan & literal pada zsh.

## Reproduksi container yang benar-benar dijalankan

Image PostgreSQL tidak memerlukan Go, curl, atau Python. Host membangun binary, SQL dan dbmate disalin bersama runner:

```sh
docker compose --profile test up -d --wait postgres-test
sh scripts/build-container-tests.sh arm64
docker compose exec -T postgres-test mkdir -p /tmp/transfer-101-tests
docker compose cp bin/integration/. postgres-test:/tmp/transfer-101-tests
docker compose exec -T -e 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:5432/transfer_test?sslmode=disable&connect_timeout=3' postgres-test sh /tmp/transfer-101-tests/run-container-tests.sh /tmp/transfer-101-tests
```

Arsitektur container aarch64, sehingga arm64 dipakai. Untuk x86_64, build script mendukung amd64 tetapi arsitektur tersebut belum diverifikasi. dbmate 2.28.0 Linux memakai digest upstream yang dipin. SQL tetap di fitur pemilik; artefak migrasi bank berada di mockbank-migrations. Runner mengekspor DBMATE_BIN, DBMATE_MIGRATIONS_DIR, MOCK_BANK_MIGRATIONS_DIR, PROVIDER_DEMO_BIN ke path absolut di artefak.

Fixture memverifikasi current_database/current_user adalah transfer_test sebelum DDL, lalu membuat schema aplikasi dan bank terpisah yang acak. CLI dbmate asli menjalankan up; tidak ada SQL bootstrap manual. Cleanup hanya menghapus schema fixture. Test migrasi khusus membuat kondisi error di schema miliknya sendiri. Tidak ada reset/truncate/drop database demo.

## Hasil aktual

| Pemeriksaan | Hasil |
| --- | --- |
| gofmt | Exit 0, tidak ada file perlu format. |
| go mod verify | Exit 0, seluruh module terverifikasi. |
| Build seluruh package | Exit 0. |
| Vet dengan tag integration | Exit 0. |
| Native unit dengan race detector | Exit 0, enam package dengan test lulus; cmd/demo-provider diuji lewat CLI di integrasi. |
| Shell syntax tiga script | Exit 0. |
| Compose config | Exit 0. |
| Build container Linux/arm64 | Exit 0, lima binary test dan CLI demo tersedia. |
| Preflight container | dbmate 2.28.0, CLI demo, dan lima binary test benar-benar dapat dieksekusi; daftar PostgreSQL test tidak kosong. |
| Suite transfer | Exit 0, termasuk 18 top-level test PostgreSQL. |
| Suite mockbank | Exit 0, tiga top-level test PostgreSQL. |
| Suite health | Exit 0, termasuk readiness PostgreSQL. |
| Suite API | Exit 0, HTTP nyata untuk CRUD/auth/dispatch dan CLI demo. |
| Suite mockbank-process | Exit 0, operasi/result bertahan setelah restart dua OS process. |
| Total PostgreSQL | 24 top-level test lulus, termasuk subtest semua skenario. |
| Hash host vs container | Lima binary test, CLI demo, dan dbmate SHA-256 identik. |
| Sisa schema fixture | 0 pada transfer_test, termasuk fixture mockbank. |
| Database demo | Hash dump sebelum/sesudah identik, tidak dimutasi. |

Run lengkap dilakukan beberapa kali tanpa reset database test atau langkah SQL manual. Output mentah final ada pada bin/integration/provider.log (diabaikan Git). Binary/SQL yang dijalankan tersedia di bin/integration dan /tmp/transfer-101-tests pada container test.

Command awal go test dengan cache default sempat exit 1 karena sandbox menolak akses ~/Library/Caches/go-build. Setelah memakai env GOCACHE=/tmp/transfer-101-go-build GOPROXY=off, unit/race, build, vet, dan build test container exit 0. GOPROXY=off hanya digunakan pada sesi ini karena module sudah tersedia; tidak ada pengaturan global Go yang diubah. Run yang gagal tidak dianggap sebagai hasil lulus.

## Bukti perilaku

- Happy path: bank SUCCEEDED/HTTP 200 → aplikasi SUCCESS, evidence atomik tersimpan.
- Definitive failure: bank REJECTED/HTTP 200/reason DEMO_REJECTED → FAILED.
- Pending: HTTP 200 ACCEPTED → PROCESSING, bukan SUCCESS.
- Bank commit SUCCEEDED lalu HTTP 504 → aplikasi PROCESSING; inquiry mock SUCCEEDED; satu operasi bank, satu attempt, reference existing tetap.
- Bank commit ACCEPTED lalu HTTP 500 → PROCESSING.
- 24 submit provider serentak identik → satu operation ID/operasi efektif. Amount/bank/rekening berbeda → 409 tanpa overwrite; reference sama di akun berbeda diperbolehkan dan inquiry akun lain 404.
- Claim/attempt committed sebelum HTTP; koneksi DB lain berhasil FOR UPDATE NOWAIT selama bank ditahan barrier. 24 dispatch pesaing tidak menambah HTTP call/attempt.
- Kegagalan INSERT attempt mengembalikan transaksi ke PENDING tanpa bank call. Kegagalan pencatatan evidence me-rollback status final lokal, meskipun bank sudah SUCCEEDED.
- Bukti final berlawanan yang terlambat tidak menimpa final dan tercatat FINAL_CONFLICT; respons nonfinal tidak menurunkan final.
- Deadline HTTP diuji dengan testing/synctest dan transport yang menunggu context.Done. Cancellation in-flight memakai channel/barrier. Body ditutup pada sukses, penolakan, non-200, malformed, mismatch, oversize, dan redirect; batas baca 8 KiB + satu byte untuk deteksi oversize.
- Fault timeout/cancellation setelah commit bank diinjeksikan deterministik pada transport sesudah respons HTTP nyata selesai dibaca. Ini pengujian celah commit, bukan klaim mengukur latensi timeout jaringan. State bank tetap SUCCEEDED dan local PROCESSING; observasi canceled caller tetap dicatat dengan context DB terpisah.
- Skenario success_timeout mock diuji dengan barrier tepat setelah commit lalu cancellation HTTP nyata: penantian delay berhenti dan hasil SUCCEEDED tetap durable. Tidak ada time.Sleep untuk menunggu hasil.
- Restart memakai dua OS process terpisah yang menjalankan service cmd/mockbank dari test executable. Setelah process pertama commit SUCCEEDED lalu 504, process kedua memakai koneksi DB baru dan scenario reject. Inquiry/duplicate tetap operation ID/status lama dan satu row; database tidak dihapus/reseed.
- CLI demo dieksekusi sebagai binary terpisah terhadap API run() dan mock HTTP nyata: app=PROCESSING, bank=SUCCEEDED; duplicate merchant/provider mempertahankan identitas.

## Batas dan keputusan review

Dispatch manual/sinkron dipilih agar pekerjaan tetap tercatat pada transfers/transfer_attempts tanpa worker prematur. Tidak ada pekerjaan hanya di goroutine lepas. Crash setelah claim dapat meninggalkan PROCESSING/STARTED; tahap 4 diperlukan untuk menemukan dan memulihkan keadaan itu. Inquiry mock belum memperbarui status aplikasi. ACCEPTED belum memiliki transisi otomatis. Token contoh bersifat publik, HTTP hanya loopback.

Skenario berlaku hanya pada operasi baru. Tidak ada expiry otomatis key bank. Jaminan idempotency berakhir bila bank di-reset atau rollback. Demo memakai database/server bank tersendiri; test memakai namespace bank tersendiri di database test terverifikasi. Standard library digunakan untuk HTTP/JSON/context dan test; hanya driver PostgreSQL eksternal.

Demo database dump SHA-256 sebelum/sesudah: 4cdb0bbf119b319f14d63a3f7e5db6f3c195648f4385fe0a3c398c099d59394c. Perbandingan memakai pg_dump --no-owner --no-privileges --restrict-key=transfer101verification, hanya hash yang dicetak. Migrasi tahap 3 tidak diterapkan ke database demo pada sesi ini; jalankan quickstart setelah review. Service postgres dan postgres-test tetap healthy. Service postgres-bank belum dijalankan oleh agent; restart PostgreSQL/container bank dan resep terminal demo penuh belum dijalankan pada volume demo. Bukti restart di atas adalah restart proses mock dengan PostgreSQL tetap hidup. Tidak ada benchmark atau klaim produksi.

Checklist reviewer:

- [ ] Batas commit claim, HTTP, dan final status/evidence jelas; tidak ada lock selama HTTP.
- [ ] Finalitas bergantung pada bukti kontrak, termasuk HTTP 200 pending dan 504 sesudah commit.
- [ ] Scope merchant/provider, reference existing, dan unique constraints terjaga.
- [ ] Retensi key mock serta dampak reset/rollback dipahami.
- [ ] Demo dan test dapat diulang; batas tanpa recovery/dana diterima sebelum tahap 4.
