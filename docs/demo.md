# Demo transfer-101

Semua command dari root proyek. Expected result di bawah berasal dari kontrak/kode/test; status eksekusi ulang dokumentasi ada di [verification-publish.md](verification-publish.md). Pakai fixture publik default. Jika `.env` existing berbeda, sesuaikan alamat/credential/akun; command SQL berikut secara eksplisit membaca service demo `postgres` dan `postgres-bank` default.

## Persiapan

Hentikan API/mock/worker lama pada terminal pemiliknya sebelum migration. Pertahankan database/volume.

```sh
mise trust
mise install
mise run setup
mise run funds:show merchant_a
```

Pada checkout/database baru, `funds:show` belum menemukan akun. Inisialisasi secara eksplisit:

```sh
mise run funds:init merchant_a 10000000
```

Jika saldo sudah ada, jangan mengganti pembuka atau menebak histori. `funds:init` hanya menerima pembuka yang sama. Periksa available cukup untuk tiap transfer Rp150.000; setiap SUCCESS mengurangi dana simulasi. `LEGACY` nonfinal memerlukan review, bukan demo dengan reservasi baru. [Kebijakan dana](funds-reconciliation.md).

Terminal A, API:

```sh
mise run api
```

Terminal B, bank normal:

```sh
mise exec -- env MOCK_BANK_SCENARIO=success MOCK_BANK_INQUIRY_NOT_FOUND=false ./bin/local/mockbank
```

**Biarkan worker berhenti** untuk demo sukses, reject, duplicate, conflict, dan observasi awal 504. CLI demo melakukan dispatch eksplisit. Di terminal C, siapkan namespace run dan tempat laporan/output; `mktemp` membuat direktori baru tanpa menghapus data existing:

```sh
RUN="portfolio-$(date -u +%Y%m%dT%H%M%S)-$$"
DEMO_DIR=$(mktemp -d "${TMPDIR:-/tmp}/transfer101-demo.XXXXXX")
curl --max-time 5 -fsS http://127.0.0.1:8080/readyz
mise run funds:show merchant_a
```

Expected readiness `{"status":"ready"}`. Gunakan terminal C yang sama agar variabel tetap tersedia. Saat mengganti skenario, Ctrl+C **hanya proses bank** di terminal B, lalu start command skenario baru. Skenario hanya memengaruhi reference baru; data bank dan epoch tetap utuh.

## Transfer sukses

Bank terminal B tetap `success`. Terminal C:

```sh
REF="$RUN-ok"
mise exec -- ./bin/local/demo-provider "$REF" SUCCESS SUCCEEDED
mise run funds:show merchant_a
```

Expected `app=SUCCESS bank=SUCCEEDED`, output ID transfer/reference/operation, serta konfirmasi duplicate mempertahankan ID. CLI membuat create/duplicate, dispatch, inquiry, lalu duplicate submit bank; banyak HTTP request tetap satu operasi bank efektif. Dibanding saldo sebelum demo, available berkurang Rp150.000 dan held kembali ke nilai sebelum demo. Perubahan saldo ini diasumsikan tanpa writer lain; gunakan movement per transfer untuk bukti spesifik.

## Memeriksa transaksi, bank, dan saldo

Setelah suatu skenario, jalankan blok ini dengan `REF` skenario tersebut. Identitas diambil otomatis dari row lokal, bukan ID ilustratif:

```sh
identity=$(docker compose exec -T postgres psql -U transfer_demo -d transfer_demo -At -F '|' -v ref="$REF" <<'SQL'
SELECT id,provider_reference FROM transfers
WHERE merchant_id='merchant_a' AND reference_id=:'ref';
SQL
)
: "${identity:?Reference tidak ditemukan; periksa REF dan database demo}"
TRANSFER_ID=${identity%%|*}
PROVIDER_REFERENCE=${identity#*|}
curl --max-time 10 -fsS "http://127.0.0.1:8080/transfers/$TRANSFER_ID" \
  -H 'Authorization: Bearer demo-merchant-a-token'
curl --max-time 10 -fsS "http://127.0.0.1:8081/v1/transfers/$PROVIDER_REFERENCE" \
  -H 'Authorization: Bearer demo-bank-account-a-token'
```

GET lokal menunjukkan status/funds_state dan metadata review bila ada. Inquiry bank harus mempertahankan operation ID setelah restart. Berikut query baca tanpa token/rekening lengkap:

```sh
docker compose exec -T postgres psql -U transfer_demo -d transfer_demo -v ref="$REF" <<'SQL'
SELECT id,reference_id,provider_reference,status,funds_state,needs_review,review_reason,
       recovery_attempts,next_attempt_at,lease_until
FROM transfers WHERE merchant_id='merchant_a' AND reference_id=:'ref';
SELECT m.kind,m.amount,m.evidence_source
FROM fund_movements m JOIN transfers t ON t.id=m.transfer_id
WHERE t.merchant_id='merchant_a' AND t.reference_id=:'ref' ORDER BY m.id;
SELECT a.kind,a.outcome,a.http_status,a.bank_status,a.reason_code
FROM transfer_attempts a JOIN transfers t ON t.id=a.transfer_id
WHERE t.merchant_id='merchant_a' AND t.reference_id=:'ref' ORDER BY a.id;
SQL
docker compose exec -T postgres-bank psql -U mock_bank -d mock_bank -v ref="$PROVIDER_REFERENCE" <<'SQL'
SELECT operation_id,provider_reference,amount,status FROM bank_operations
WHERE provider_account_id='bank_account_demo_a' AND provider_reference=:'ref';
SELECT count(*) AS effective_operations FROM bank_operations
WHERE provider_account_id='bank_account_demo_a' AND provider_reference=:'ref';
SQL
mise run funds:show merchant_a
```

Expected satu row lokal, satu operasi bank untuk reference yang sudah disubmit, satu RESERVE, dan paling banyak satu CAPTURE/RELEASE. REJECTED juga memiliki row operasi, tetapi bukan pencairan sukses.

## Duplicate request

Pada skenario `success`, ulangi CLI dengan **REF yang sama dari demo sukses**:

```sh
REF="$RUN-ok"
mise exec -- ./bin/local/demo-provider "$REF" SUCCESS SUCCEEDED
curl --max-time 10 -i http://127.0.0.1:8080/transfers \
  -H 'Authorization: Bearer demo-merchant-a-token' \
  -H 'Content-Type: application/json' \
  -d "{\"reference_id\":\"$REF\",\"amount\":150000,\"bank_code\":\"001\",\"account_number\":\"00123456789\"}"
```

Expected POST `200` dengan ID/provider_reference existing, bukan 201. Pemeriksaan movement tetap satu RESERVE dan satu CAPTURE; available tidak berkurang lagi.

## Payload conflict

Ubah amount pada key yang sudah dipakai:

```sh
curl --max-time 10 -i http://127.0.0.1:8080/transfers \
  -H 'Authorization: Bearer demo-merchant-a-token' \
  -H 'Content-Type: application/json' \
  -d "{\"reference_id\":\"$REF\",\"amount\":150001,\"bank_code\":\"001\",\"account_number\":\"00123456789\"}"
```

Expected `409 PAYLOAD_CONFLICT` walau transaksi sudah final. Tidak ada row, reference, atau reserve baru. Perubahan bank_code/account_number mengikuti aturan yang sama. `curl -i` menampilkan status; command curl tanpa `--fail` memang tidak menjadikan 409 sebagai shell error.

## Penolakan final

Hentikan bank B, jalankan:

```sh
mise exec -- env MOCK_BANK_SCENARIO=reject MOCK_BANK_INQUIRY_NOT_FOUND=false ./bin/local/mockbank
```

Terminal C:

```sh
REF="$RUN-reject"
mise exec -- ./bin/local/demo-provider "$REF" FAILED REJECTED
mise run funds:show merchant_a
```

Expected aplikasi FAILED/RELEASED, bank REJECTED dengan `DEMO_REJECTED`, satu RESERVE dan satu RELEASE. Available kembali ke nilai sebelum transfer ini. Ini penolakan bisnis pasti dari body provider, meskipun HTTP submit bank 200.

## Bank sukses respons 504 lalu inquiry

Worker tetap berhenti. Ganti bank B:

```sh
mise exec -- env MOCK_BANK_SCENARIO=success_504 MOCK_BANK_INQUIRY_NOT_FOUND=false ./bin/local/mockbank
```

Terminal C:

```sh
REF="$RUN-504"
mise exec -- ./bin/local/demo-provider "$REF" PROCESSING SUCCEEDED
mise run funds:show merchant_a
```

Expected aplikasi PROCESSING/HELD, bank SUCCEEDED. Available turun Rp150.000 dan held naik Rp150.000 relatif sebelum transfer. Jalankan blok pemeriksaan di atas: journal mencatat submit HTTP 504 dan belum ada CAPTURE. CLI juga melakukan duplicate bank untuk membuktikan operation ID existing; aplikasi sendiri belum menerima bukti final itu.

Terminal D, jalankan worker:

```sh
mise run worker
```

Terminal C, ulangi key yang sama dengan penantian bounded pada CLI:

```sh
mise exec -- ./bin/local/demo-provider "$REF" ANY SUCCEEDED SUCCESS
mise run funds:show merchant_a
```

Expected `recovered=SUCCESS`, aplikasi SUCCESS/CAPTURED, reference/operation ID sama, held kembali seperti sebelum transfer dan satu CAPTURE. CLI polling dengan deadline 45 detik, bukan tidur tetap untuk mengasumsikan hasil. Jika mencapai deadline/review, lihat GET dan journal; jangan membuat reference pengganti. Ctrl+C worker D sebelum skenario berikutnya.

## Crash dan restart

Untuk observasi restart proses tanpa kehilangan data, buat reference baru pada bank `success_504` **tanpa worker**:

```sh
REF="$RUN-restart"
mise exec -- ./bin/local/demo-provider "$REF" PROCESSING SUCCEEDED
```

Setelah aplikasi PROCESSING dan bank SUCCEEDED, hentikan API A dan bank B dengan Ctrl+C. Jalankan kembali `mise run api` pada A dan command bank `success_504` yang sama pada B, tanpa migration/reset. Jalankan `mise run worker` pada D, lalu di terminal C:

```sh
mise exec -- ./bin/local/demo-provider "$REF" ANY SUCCEEDED SUCCESS
```

Expected status final pulih dengan ID/reference/operation yang sama, satu CAPTURE. Ctrl+C worker D setelah observasi selesai.

Langkah manual itu memperlihatkan state durable melewati restart normal. Crash tepat sebelum pengiriman atau setelah bank commit diuji secara deterministik oleh child process SIGKILL, bukan dengan berharap sinyal jatuh pada waktu tertentu. Untuk menjalankan test crash terarah pada DB test terisolasi:

```sh
mise run test:db
mise exec -- env 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:54330/transfer_test?sslmode=disable&connect_timeout=3' \
  go test -tags=integration -count=1 -timeout=3m -v \
  -run '^TestPostgresRecoverySIGKILLAndRestartAtCommitBoundaries$' ./internal/transfer
```

Expected PASS untuk `before_claim`, `claim`, `bank_before_response`, `http`, dan `finish`: original reference, satu operasi bank, dan job final tidak aktif. Test memakai barrier/DB due time fixture; tidak mengubah jadwal demo. [Source test](../internal/transfer/recovery_process_integration_test.go). Pada host yang membatasi socket native, `mise run test:integration` menjalankan suite ini di container lewat binary Linux.

## Rekonsiliasi laporan

Hentikan worker D. Bank tetap `success_504`, API hidup. Buat transfer nonfinal baru:

```sh
REF="$RUN-report"
mise exec -- ./bin/local/demo-provider "$REF" PROCESSING SUCCEEDED
REPORT_ID="$RUN-snapshot"
mise run reconcile export bank_account_demo_a "$REPORT_ID" "$DEMO_DIR/report.csv"
mise run reconcile import bank_account_demo_a "$REPORT_ID" "$DEMO_DIR/report.csv"
```

Expected `verified:true`, `FINALIZED` / `FINALIZED_SUCCESS` untuk REF ini, dan angka `import_id` aktual. Periksa saldo dan movement: satu CAPTURE, aplikasi SUCCESS. Laporan snapshot seluruh akun sampai cutoff, sehingga findings dapat mencakup histori demo lain; missing/legacy/mismatch dapat ditandai review tanpa mengubah status final. Baca temuan tersebut, jangan menghapus histori untuk membuat output tampak bersih. Batas laporan 1.000 baris/8 MiB.

Import ulang:

```sh
mise run reconcile import bank_account_demo_a "$REPORT_ID" "$DEMO_DIR/report.csv"
```

Expected `reused:true`, import_id sama, tidak ada CAPTURE/RELEASE kedua. Ambil import ID terbaru untuk report ini dan baca audit (tidak mengasumsikan ID selalu 1):

```sh
IMPORT_ID=$(docker compose exec -T postgres psql -U transfer_demo -d transfer_demo -At -v report="$REPORT_ID" <<'SQL'
SELECT id FROM reconciliation_imports
WHERE provider_account_id='bank_account_demo_a' AND report_id=:'report'
ORDER BY id DESC LIMIT 1;
SQL
)
mise run reconcile show bank_account_demo_a "$IMPORT_ID"
```

Untuk menunjukkan file lokal bukan bukti final, buat salinan yang berbeda byte tanpa menimpa export asli:

```sh
cp "$DEMO_DIR/report.csv" "$DEMO_DIR/modified.csv"
printf '\n' >> "$DEMO_DIR/modified.csv"
mise run reconcile import bank_account_demo_a "$REPORT_ID" "$DEMO_DIR/modified.csv"
```

Expected `verified:false` dan finding `SOURCE_MISMATCH`/`NO_EFFECT`, bukan settlement baru. Audit impor berbeda ini memang dipertahankan. Amount/destination mismatch, duplicate rows, missing, partial snapshot, dan konflik final dari **sumber provider** diuji di fixture terisolasi; mengedit file lokal tidak dapat menyamar sebagai laporan bank valid.

## Concurrent requests dan saldo terbatas

Pada bank `success` dan reference baru, dua CLI dapat berjalan bersamaan tanpa asumsi urutan:

```sh
REF="$RUN-concurrent"
mise exec -- ./bin/local/demo-provider "$REF" ANY SUCCEEDED SUCCESS > "$DEMO_DIR/concurrent-1.txt" &
p1=$!
mise exec -- ./bin/local/demo-provider "$REF" ANY SUCCEEDED SUCCESS > "$DEMO_DIR/concurrent-2.txt" &
p2=$!
wait "$p1"
wait "$p2"
cat "$DEMO_DIR/concurrent-1.txt" "$DEMO_DIR/concurrent-2.txt"
```

Expected kedua proses exit 0 dengan ID/reference sama. Jalankan blok observasi untuk membuktikan satu row/operasi/RESERVE/CAPTURE, bukan hanya membandingkan log. `ANY` menerima PROCESSING sementara akibat claim pesaing; CLI menunggu final secara bounded.

Jangan menghabiskan atau mengubah saldo existing untuk demo overspend. Test terarah menyediakan saldo terbatas sendiri dan mencakup budget exhaustion/not-found serta mismatch provider:

```sh
mise run test:db
mise exec -- env 'TEST_DATABASE_URL=postgres://transfer_test:transfer_test@127.0.0.1:54330/transfer_test?sslmode=disable&connect_timeout=3' \
  go test -tags=integration -count=1 -timeout=3m -v \
  -run 'TestPostgres(FundsConcurrentTransfersCannotOverspend|RecoveryBudgetTimeoutNotFoundAndAccepted|ReconciliationMismatchDuplicateInvalidMissingAndPartial)' \
  ./internal/transfer
```

Expected PASS; saldo tidak negatif, attempt exhaustion tetap PROCESSING/review/held, dan mismatch menjadi temuan tanpa overwrite dana. Fixture menjalankan migration dbmate dan cleanup hanya schema test miliknya.

## Pemeriksaan dana dan penghentian

Tanpa biaya/top-up/writer lain, model baru harus memenuhi `available + held + total CAPTURE = opening_amount`. Query baca seluruh akun berikut juga mencakup histori baru dari run sebelumnya:

```sh
docker compose exec -T postgres psql -U transfer_demo -d transfer_demo <<'SQL'
SELECT f.merchant_id,f.source_account_id,f.opening_amount,f.available,f.held,
       COALESCE(c.captured,0) AS captured,
       f.available::numeric+f.held+COALESCE(c.captured,0)=f.opening_amount AS conserved
FROM merchant_funds f LEFT JOIN (
  SELECT merchant_id,source_account_id,sum(amount) AS captured
  FROM fund_movements WHERE kind='CAPTURE' GROUP BY merchant_id,source_account_id
) c USING (merchant_id,source_account_id);
SELECT transfer_id,count(*) FROM fund_movements
WHERE kind IN ('CAPTURE','RELEASE') GROUP BY transfer_id HAVING count(*)>1;
SELECT merchant_id,reference_id,count(*) FROM transfers
GROUP BY merchant_id,reference_id HAVING count(*)>1;
SQL
```

Expected conserved true, available/held nonnegatif, dan dua query duplikat kosong. Bandingkan row bank spesifik provider_reference dengan query observasi sebelumnya; jangan menganggap seluruh histori akun hanya milik run ini.

Ctrl+C proses API/bank/worker pada terminal masing-masing. Opsional hentikan service tanpa menghapus volume:

```sh
mise run db:stop
mise run test:stop
```

Laporan/output di DEMO_DIR dipertahankan untuk review. Tidak ada reset/drop/truncate atau cleanup database demo pada panduan ini.
