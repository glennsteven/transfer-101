# Dana dan rekonsiliasi

Tahap 5 menambahkan satu saldo simulasi per pasangan merchant/sumber dana, tanpa mengubah empat status transfer. Semua nominal IDR berupa `BIGINT` rupiah integer.

## Inisialisasi saldo lokal

Jalankan setup/migrasi, lalu buat saldo pembuka yang disengaja:

```sh
mise run setup
mise run funds:init merchant_a 10000000
mise run funds:show merchant_a
```

`funds:init` mengambil source account dari konfigurasi merchant server. Nilainya adalah saldo pembuka fixture; command ini bukan top-up. Pengulangan hanya berhasil jika jumlah pembuka sama. Transaksi yang dibuat sebelumnya oleh tahap 2–4 tidak memiliki bukti asal dana. Migrasi mempertahankan row itu sebagai `funds_state=LEGACY`, menandai PENDING/PROCESSING untuk review dan menghentikan jadwal otomatisnya. Migrasi tidak membuat saldo atau mutasi historis. Inisialisasi saldo baru tidak mengadopsi transaksi legacy. Selesaikan transaksi lama secara manual setelah bukti/sumber dana diputuskan; jangan membuat saldo pembuka berdasarkan jumlah transaksi yang ditebak.

Untuk demo baru, inisialisasi sebelum mengirim transfer. `setup` dan migrasi tidak menghapus volume/database demo. `mise run dev` menjalankan API, mock bank dan recovery worker; `mise run demo REFERENCE` menggunakan transfer baru. Contoh skenario timeout:

```sh
mise run dev:504
# terminal lain
mise run demo stage5-504-001
mise run funds:show merchant_a
```

Transfer baru harus terlebih dahulu mereservasi saldo tersedia. Timeout/504 tetap PROCESSING dan saldo berpindah dari available ke held. Inquiry yang mengonfirmasi SUCCESS mengonsumsi held. Penolakan final yang pasti mengembalikannya ke available.

## Invarian saldo

Create menjalankan insert idempotent, reservasi conditional update, catatan RESERVE, dan pembuatan pekerjaan durable pada satu transaksi PostgreSQL. Duplicate payload yang sama dikembalikan sebelum reservasi. Payload berbeda menghasilkan 409. `available >= amount` diperiksa dan dikunci secara atomik oleh PostgreSQL; saldo tidak dapat menjadi negatif pada create bersamaan.

Finalizer dispatch/inquiry dan rekonsiliasi mengunci transfer, lalu saldo, dengan urutan yang sama. `settleFunds` adalah satu jalur untuk kedua hasil final: SUCCESS mengurangi held; FAILED mengurangi held dan menambah available. Status, state dana, mutasi, evidence/attempt atau finding, serta jadwal recovery diperbarui dalam transaksi yang sama. Index unik membatasi satu OPEN, RESERVE, dan settlement CAPTURE/RELEASE per identitas transfer. Status nonfinal dan manual review mempertahankan held.

Tabel `fund_movements` merupakan jurnal mutasi minimum untuk saldo simulasi, bukan general ledger atau sumber dana nyata. OPEN merekam inisialisasi eksplisit; RESERVE, CAPTURE, RELEASE mencatat setiap efek. Saldo snapshot berada di `merchant_funds` dan tidak mendukung top-up, biaya, FX, atau banyak sumber dana aktif per merchant.

## Laporan CSV mock bank

Mock bank membuat snapshot CSV immutable per `(provider_account_id, report_id)`, disimpan di database bank terpisah. Snapshot berisi metadata versi, ID laporan, akun, periode/cutoff, waktu pembuatan, kelengkapan, epoch retensi dan baris operasi dengan provider reference, operation ID, amount, currency, bank/rekening, status, serta reason code. Snapshot saat ini meliputi seluruh operasi akun yang committed sampai cutoff (periode mulai tetap dari epoch awal); maksimum 1.000 baris dan 8 MiB.

```sh
mise run reconcile export bank_account_demo_a report-demo-001 ./report.csv
mise run reconcile import bank_account_demo_a report-demo-001 ./report.csv
mise run reconcile show bank_account_demo_a 1
```

Nama file export harus belum ada atau berisi byte identik. Import memuat ulang snapshot dari mock bank dan hanya menandai verified saat byte/hash dan scope cocok. File lokal saja tidak cukup sebagai bukti. ID laporan sama dengan isi bank berubah dicatat sebagai konflik. Import yang identik mengembalikan audit yang telah ada dan tidak menjalankan settlement kedua.

Import menyimpan CSV mentah, hash sumber/input, status verified, serta temuan/keputusan untuk audit. Temuan meliputi baris invalid, bank-only, missing dari snapshot, duplikat identik/konflik, mismatch nominal/mata uang/tujuan, status nonfinal, konflik bukti final, laporan parsial, dan finalisasi. Pencocokan memakai akun provider dan reference; payload tujuan serta nominal harus sama.

Hanya snapshot lengkap, verified, cocok pada akun/reference/amount/currency/tujuan, dengan epoch provider yang masih cocok yang dapat menyelesaikan transaksi nonfinal menjadi SUCCESS/FAILED berdasarkan status SUCCEEDED/REJECTED. ACCEPTED tetap nonfinal. Baris yang tidak ada bukan bukti FAILED. Mismatch dan laporan parsial tidak mengubah saldo. Bukti final yang bertentangan dengan status final lokal menjadi temuan review dan tidak menimpa status atau mutasi dana.

Contoh pembacaan audit:

```sh
mise run reconcile show bank_account_demo_a 1
```

`import_id` adalah angka yang dikembalikan oleh perintah import; gunakan angka tersebut pada `show`. Jangan memakai ID contoh 1 jika hasil import berbeda.

## Batas simulasi

Saldo tidak mewakili rekening bank atau settlement sungguhan. Inisialisasi adalah fixture operator lokal, tanpa endpoint deposit/top-up. Snapshot mock adalah simulasi laporan bank; rentang saat ini full-snapshot sampai cutoff, bukan scheduler statement harian atau protokol bank eksternal. Audit belum menyediakan UI/endpoint merchant dan bukan pengganti proses operasional manual. Hasil test tidak mengukur kapasitas produksi.
