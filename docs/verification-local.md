# Verifikasi task mise

Tanggal: 21 Oktober 2026. mise 2026.4.19 macos-arm64, Go 1.26.2, dbmate 2.28.0, PostgreSQL test 17.11 Linux/arm64. Ini perubahan tooling lokal; kode bisnis tahap 4, schema, kontrak finalitas, dan dependency Go tidak diubah.

## File review

- mise.toml: pin tool, pemuatan dotenv, setup/migrasi, tiga proses foreground, demo dan task test.
- .gitignore: override mise lokal tidak masuk Git.
- README.md: alur utama memakai mise, perintah manual tetap tersedia.
- docs/local.md: command, environment, lifecycle, batas test dan troubleshooting.
- docs/recovery.md serta docs/demo-provider.md: tautan ke alur mise.
- Dokumen ini: hasil aktual dan batas lingkungan agent.

## Pemeriksaan aktual

| Pemeriksaan | Hasil |
| --- | --- |
| mise tasks validate | Exit 0; 23 task valid, tanpa error referensi/dependency. |
| mise fmt --check | Exit 0. |
| mise run --skip-tools build | Exit 0; empat binary native tersedia di bin/local. |
| mise run test | Exit 0; seluruh package bertest lulus dengan race detector. |
| mise run --skip-tools --dry-run dev:504 | Exit 0; urutan env/database, migrasi kedua fitur, build dan task group terbaca. |
| Fixture mise native terisolasi | Exit 0; env:init membuat file yang hilang dan mempertahankan existing, precedence dotenv benar, demo meneruskan reference literal/default, task bank:504 mengalahkan scenario dotenv, Ctrl+C menghentikan ketiga proses fixture. |
| Build/copy artefak test | Exit 0; uname container aarch64 dipetakan ke arm64, binary dan migrasi disalin ke target. |
| Perintah isi test:e2e melalui Docker langsung | Exit 0; TestPostgresAPIStartupAndRoutes lulus, recovered=SUCCESS initial=PROCESSING. |
| Perintah isi test:integration melalui Docker langsung | Exit 0; lima suite, 34 top-level TestPostgres lulus. |
| Perintah isi test:race melalui Docker langsung | Exit 0; seluruh package bertest lulus dengan PostgreSQL dan race detector. |
| Compose config | Exit 0. |
| Cleanup fixture | transfer_test/current_user transfer_test, schema fixture tersisa 0. |

Agent menjalankan mise dengan MISE_TRUSTED_CONFIG_PATHS menunjuk proyek dan MISE_CACHE_DIR/MISE_STATE_DIR pada /tmp, tanpa mengubah trust/config global. GOCACHE=/tmp/transfer-101-go-build dan GOPROXY=off dipakai untuk Go host. Auto install task dimatikan selama verifikasi karena Go/dbmate sudah tersedia pada mesin. mise install belum dijalankan; unduhan/instalasi tool melalui mise belum diverifikasi dalam sandbox ini.

Fixture lifecycle hanya memakai direktori sementara dan executable uji yang menunggu sinyal. Dependency setup diganti di fixture agar tidak mengakses database demo; ini menguji pemuatan environment, argument forwarding, override 504, task group dan penghentian, bukan startup server Go aktual. Percobaan worker fixture exit 7 menunjukkan mise terpasang tidak menghentikan sibling otomatis; perilaku ini dicatat pada troubleshooting. Alur dev lengkap terhadap demo pengguna tidak dijalankan.

## Batas akses Docker dari mise

Percobaan mise run test:e2e berhenti pada dependency test:db dengan exit 1: sandbox menolak connect ke socket Docker ketika Docker diluncurkan oleh mise. Test E2E belum berjalan pada percobaan itu. Ini tidak dilaporkan sebagai task yang lulus.

Akses Docker langsung tersedia, sehingga command Docker yang sama pada isi task dijalankan melalui tool terminal. PostgreSQL test dinyalakan, build/copy diselesaikan, lalu perintah berikut benar-benar dijalankan:

```sh
docker compose --profile test up -d --wait postgres-test
docker compose exec -T postgres-test uname -m
sh scripts/build-container-tests.sh arm64
docker compose cp bin/integration/. postgres-test:/tmp/transfer-101-tests
docker compose exec -T -e 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:5432/transfer_test?sslmode=disable&connect_timeout=3' -e DBMATE_BIN=/tmp/transfer-101-tests/dbmate -e DBMATE_MIGRATIONS_DIR=/tmp/transfer-101-tests/migrations -e MOCK_BANK_MIGRATIONS_DIR=/tmp/transfer-101-tests/mockbank-migrations -e PROVIDER_DEMO_BIN=/tmp/transfer-101-tests/demo-provider postgres-test /tmp/transfer-101-tests/api.test -test.run '^TestPostgresAPIStartupAndRoutes$' -test.v -test.timeout=120s
docker compose exec -T -e 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:5432/transfer_test?sslmode=disable&connect_timeout=3' postgres-test sh /tmp/transfer-101-tests/run-container-tests.sh /tmp/transfer-101-tests
docker compose -f compose.yaml -f compose.race.yaml --profile test run --rm --no-deps recovery-race go test -race -tags=integration -count=1 -timeout=180s ./...
```

Task test:recovery dan test:recovery:race divalidasi konfigurasinya pada sesi ini; pengulangan fokusnya tidak dijalankan ulang karena perubahan hanya tooling. Bukti pengulangan tahap 4 tetap berada pada [verifikasi recovery](verification-recovery.md). Smoke, suite lengkap dan race di atas benar-benar dijalankan ulang.

Output lokal tersimpan pada bin/integration/mise-unit.log, mise-lifecycle.log, mise-e2e.log, mise-integration.log, mise-race.log, dan mise-sandbox-failure.log. Folder bin diabaikan Git.

Fingerprint pg_dump transfer_demo sebelum/sesudah tetap b7eac75341753de8f84e3d8d310f371fdff798a71a21eb1dc275163c1fb1cb7f. Tidak ada reset database, migrasi demo, restart server pengguna, atau perubahan file env pengguna. Hanya service database test yang dinyalakan/dipakai untuk verifikasi ini. Belum ada metadata .git pada folder kerja; tidak ada commit/push.

## Checklist review

- [ ] Cukup mise trust/install, dev:504 dan demo untuk alur lokal utama.
- [ ] Versi tool dan urutan dotenv sesuai kebutuhan lokal.
- [ ] Setup tidak menimpa env existing atau mereset database.
- [ ] Test memakai transfer_test dan schema fixture; tidak memerlukan server manual.
- [ ] Batas harness E2E, sandbox, dan lifecycle mise diketahui.
- [ ] Tidak ada perubahan dana atau rekonsiliasi tahap 5.
