# Load test lokal

`cmd/loadtest` memakai `net/http`, `database/sql`, dan driver PostgreSQL yang sudah ada. Runner membatasi target HTTP/PostgreSQL ke loopback, jumlah request maksimum 100.000, concurrency maksimum 256, timeout request, durasi traffic, dan waktu tunggu finality. Prefix setiap run unik dan dibatasi; row tidak dihapus setelah run.

## Jalankan

Siapkan demo dan saldo dahulu:

```sh
mise run setup
mise run funds:init merchant_a 10000000
mise run dev
```

Di terminal lain, jalankan workload standar:

```sh
mise run loadtest -- --requests 200 --concurrency 16 --duration 20s --http-timeout 3s --completion-timeout 30s
```

Task mem-build executable lewat `mise run setup` atau `mise run build` dan mewarisi `.env` serta `.env.mockbank` melalui mise. `DEMO_MERCHANT_TOKEN` dapat mengganti token; default memakai fixture merchant A. Gunakan reference prefix baru untuk setiap run.

Runner membagi traffic menjadi key unik dan duplikat identik. Opsi tambahan membuat sebagian key konflik (runner membuat transaksi seed dulu, lalu mengubah amount) dan sebagian request dengan amount di atas available awal:

```sh
mise run loadtest -- --requests 200 --concurrency 16 --duplicate-percent 25 --conflict-percent 10 --insufficient-percent 10 --amount 1000
```

Persentase skenario tidak boleh melebihi 90% agar workload tetap memiliki request baru. Insufficient amount dihitung dari saldo available saat awal run. Konflik memakai key yang sudah di-seed sebelum traffic diukur. Semua request diberi deadline dan redirect tidak diikuti.

## Output dan pemeriksaan

Runner melaporkan versi Go/OS/PostgreSQL, prefix, konfigurasi, jumlah dan status request, throughput request API, p50/p95/p99 latency request, latency request yang diterima, waktu dari akhir traffic sampai status transfer terakhir teramati final, serta error/outstanding.

Setelah completion deadline, tool memeriksa row count dan uniqueness merchant/reference/provider_reference, operasi bank untuk transfer final, satu RESERVE, paling banyak satu CAPTURE/RELEASE sesuai status final, state dana, saldo nonnegatif, dan konservasi `available + held + total CAPTURE = opening_amount`. Rumus ini mengasumsikan tidak ada top-up/fee selama pengujian. Jika skenario bank tetap ACCEPTED atau recovery mencapai manual review, runner melaporkan outstanding dan exit nonzero; ambiguous tidak dihitung sebagai kegagalan final.

Load test memakai database demo lokal tetapi tidak menjalankan migration, reset, atau cleanup. Run rows sengaja dipertahankan untuk pemeriksaan dan saldo berkurang saat SUCCESS. Mulai hanya dengan saldo yang memang dialokasikan untuk simulasi dan gunakan prefix baru. Jika ingin mengisolasi di PostgreSQL `transfer_test`, sediakan schema `loadtest_*` terpisah untuk aplikasi dan bank, URL memakai `search_path`, dan tambahkan `--allow-test-database`; tool menolak database test tanpa dua scope schema khusus.

Hasil latency adalah pengukuran satu lingkungan lokal (API, worker, mock, PostgreSQL pada mesin yang sama), bukan benchmark kapasitas atau jaminan produksi. API acceptance diukur pada POST `/transfers`; waktu penyelesaian diukur terpisah sampai status final terlihat dari PostgreSQL setelah trafik berhenti.
