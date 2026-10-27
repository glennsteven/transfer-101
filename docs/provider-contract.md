# Kontrak mock bank

**Tahap 3–5: submit, inquiry, kontrak resubmit aman, dan laporan CSV telah diimplementasikan.** Ini kontrak mock lokal dan tidak menyatakan perilaku bank nyata. Proses `cmd/mockbank` memakai database `mock_bank` pada service/volume tersendiri; aplikasi mengamati bank lewat HTTP.

## Akun dan key idempotency

Credential integrasi demo dipetakan server mock ke `provider_account_id`; body tidak memilih scope akun. Submit/inquiry/reports hanya melihat operasi akun tersebut.

Key bank adalah `(provider_account_id, provider_reference)`. Aplikasi menyimpan akun dan reference secara durable sebelum submit. Reference dipakai ulang pada semua retry/inquiry, termasuk sesudah timeout atau restart. Reference yang sama di akun berbeda boleh mewakili operasi berbeda.

Mock menyimpan key, payload, dan operasi/hasil dalam PostgreSQL milik mock, terpisah dari tabel aplikasi. Insert/deduplication harus atomik di bank: concurrent submit identik menghasilkan satu operasi logical; payload berbeda ditolak tanpa operasi baru. Jumlah HTTP attempt berbeda dari jumlah operasi logical dan jumlah pencairan sukses.

Retensi demo: key/hasil bertahan selama data mock dipertahankan, termasuk restart proses/container; tidak ada expiry otomatis. Reset volume/data mock mengakhiri jaminan tersebut. Aplikasi tidak boleh melanjutkan retry otomatis atas histori yang hilang akibat reset bank tanpa review. Unique constraint aplikasi tidak menggantikan deduplikasi bank.

## Submit: POST `/v1/transfers`

```json
{
  "provider_reference": "prv_example",
  "amount": 150000,
  "currency": "IDR",
  "bank_code": "001",
  "account_number": "00123456789"
}
```

Validasi amount/bank/rekening mengikuti kontrak API; currency wajib IDR. Reference server nonempty dengan panjang maksimum 128 karakter ASCII `[A-Za-z0-9._-]`. Parameter immutable tersebut dibandingkan pada duplicate.

Respons HTTP `200` membawa `operation_id` opaque, `provider_account_id`, reference, amount/currency, bank/rekening, dan `status` bisnis berikut. REJECTED wajib memiliki `reason_code` (simulasi saat ini DEMO_REJECTED):

| Status bank | Bukti | Status aplikasi |
| --- | --- | --- |
| `ACCEPTED` | Tersimpan, belum final. | `PROCESSING` |
| `SUCCEEDED` | Operasi sukses final dan hasil tersimpan durable. | `SUCCESS` jika identitas/payload cocok. |
| `REJECTED` | Penolakan final, reason code disimpan; operasi ini tidak akan kemudian dijalankan. | `FAILED` jika identitas/payload cocok. |

Contoh sukses final:

```json
{
  "operation_id": "bank_example",
  "provider_account_id": "bank_account_demo_a",
  "provider_reference": "prv_example",
  "amount": 150000,
  "currency": "IDR",
  "bank_code": "001",
  "account_number": "00123456789",
  "status": "SUCCEEDED"
}
```

Duplicate identik mengembalikan operasi existing beserta status terkininya; bukan operasi baru. Duplicate berbeda mengembalikan `409` dengan code `PAYLOAD_CONFLICT`. Conflict ini bukan bukti transfer aplikasi gagal; ini dapat menandakan bug integrasi atau key yang sudah dipakai operasi lain dan perlu review.

Body rusak/status tidak dikenal, timeout, putus koneksi, HTTP 500/504, dan error protokol tidak menjadi bukti final. Simulasi dapat commit sukses terlebih dahulu lalu merespons 504. Jangan menurunkan status final berdasarkan respons terlambat/nonfinal. Bukti final bertentangan harus dicatat untuk review.

## Inquiry: GET `/v1/transfers/{provider_reference}`

Gunakan akun integrasi existing dan reference yang sama. `200` mengembalikan resource/status dari state durable mock. `404` dengan code `NOT_FOUND` berarti operasi belum terlihat pada inquiry tersebut; bukan bukti operasi tidak akan muncul/diproses.

MOCK_BANK_INQUIRY_NOT_FOUND=true menyimulasikan NOT_FOUND meskipun operasi sudah diterima. Submit tetap melakukan deduplication pada state durable yang sebenarnya.

Setelah hasil ambigu, aplikasi mengutamakan inquiry. Submit ulang hanya aman bila jaminan deduplikasi/retensi dalam scope masih berlaku atau ada bukti definitif operasi lama tidak akan dieksekusi. Pada mock yang datanya utuh, resubmit reference existing tetap satu operasi karena kontrak deduplikasi. Pada provider tanpa jaminan itu, not found saja tidak cukup; gunakan inquiry terbatas/perlu review.

Budget retry habis tidak membuat transfer `FAILED`; pertahankan nonfinal dan tandai perlu review. Setiap attempt memiliki timeout; backoff/jadwal harus durable pada tahap 4.

## Epoch dan masa berlaku resubmit: tahap 4

Respons terautentikasi membawa X-Mock-Provider-Account dan X-Mock-Retention-Epoch, termasuk NOT_FOUND. Scope header harus cocok dengan provider_account_id existing sebelum aplikasi boleh memakai jaminan retensi. Epoch berada di bank_retention pada database bank; tidak berubah saat restart proses/container dengan data utuh.

Aplikasi menyimpan epoch sebelum POST pertama dan batas resubmit_before 15 menit sejak create. POST recovery membawa X-Mock-Retention-Epoch dan X-Mock-Resubmit-Before (RFC3339Nano). Bank memeriksa epoch dan clock DB setelah mengunci row retensi, dalam transaksi pendek yang sama dengan insert/deduplikasi. Epoch salah atau window habis menghasilkan 412 tanpa operasi baru. Endpoint manual/CLI lama tanpa kedua header tetap didukung untuk fixture; recovery aplikasi selalu memakai guard.

NOT_FOUND yang valid + akun cocok + epoch sama + window belum habis memperbolehkan submit dengan akun/reference/payload existing. Tanpa bukti itu aplikasi hanya melanjutkan inquiry terbatas atau manual review. Membuat reference baru bukan cara menangani timeout.

Setiap reset/restore yang menghilangkan key/hasil harus mengganti epoch secara atomik bersama perubahan dataset. Mempertahankan epoch setelah menghapus histori melanggar kontrak mock; guard tidak melindungi dari administrator yang melanggar invariant ini. Backup/restore harus memperlakukan operasi dan epoch sebagai satu dataset. Tidak ada endpoint reset otomatis. Retensi tanpa reset tetap tidak memiliki expiry; 15 menit adalah batas resubmit otomatis aplikasi, bukan TTL penghapusan operasi bank.

## Skenario yang tersedia pada tahap 3

`MOCK_BANK_SCENARIO` ditentukan saat proses mock start; tidak ada field/header merchant untuk mengubah skenario. Hanya operasi baru mengikuti skenario proses. Duplicate identik selalu HTTP 200 dengan operasi existing, termasuk setelah restart/skenario berubah.

| Nilai | State durable operasi baru | Respons submit pertama |
| --- | --- | --- |
| success | SUCCEEDED | 200 dengan resource final. |
| reject | REJECTED, reason DEMO_REJECTED | 200 dengan penolakan bisnis final. |
| accepted | ACCEPTED | 200 dengan status nonfinal. |
| success_504 | SUCCEEDED | 504 setelah INSERT committed. |
| accepted_500 | ACCEPTED | 500 setelah INSERT committed. |
| success_timeout | SUCCEEDED | Ditunda MOCK_BANK_RESPONSE_DELAY setelah commit; request cancellation menghentikan penantian. |

Body submit dibatasi 4 KiB dan credential bank wajib. Currency hanya IDR, amount int64 positif, format bank/rekening/reference mengikuti kontrak. Unknown field termasuk provider_account_id ditolak. Scope akun berasal dari bearer token server. Format error: `{"error":{"code":"PAYLOAD_CONFLICT"}}`; endpoint inquiry menghasilkan NOT_FOUND untuk key milik akun lain maupun key tidak ada.

Delay default 5s, maksimum 1m; gunakan delay > PROVIDER_TIMEOUT untuk demo timeout. Kontrol ini hanya simulasi lokal. Tidak ada chaos acak, expiry key, penghapusan operasi, atau transisi otomatis ACCEPTED menjadi final. [Demo lengkap](demo-provider.md).

## Laporan: GET `/v1/reports/{report_id}`

Tahap 5 memakai CSV standard library. Mock menyediakan ID laporan per akun; snapshot immutable disimpan di database bank dan berasal dari `bank_operations`, bukan tabel transfer aplikasi. Baris pertama memuat versi, `report_id`, `provider_account_id`, start, cutoff/end, generated_at, `complete`, dan retention epoch. Waktu RFC 3339 UTC. Snapshot saat ini full-snapshot dari epoch awal sampai cutoff dan dibatasi 1.000 baris/8 MiB; ini bukan statement harian.

Header CSV adalah `provider_reference,operation_id,amount,currency,bank_code,account_number,status,reason_code`. Nominal IDR berupa integer; status `SUCCEEDED` dan `REJECTED` final, `ACCEPTED` nonfinal. Snapshot yang sama dikembalikan setelah restart. ID laporan yang telah diterbitkan tidak boleh berubah.

Inquiry memeriksa satu operasi; laporan membandingkan snapshot bank dengan pencatatan lokal. Cocokkan akun/reference serta payload. Catat bank-only, local-only/missing, amount/currency mismatch, tujuan berbeda, duplicate/conflicting row, invalid row, dan status conflict. Laporan tak lengkap atau entry hilang tidak membuktikan transfer gagal.

Entry SUCCEEDED/REJECTED yang valid dan cocok dapat menjadi bukti final untuk transaksi nonfinal melalui fungsi settlement atomik yang sama dengan inquiry. Import harus mengambil ulang CSV dari provider dan membandingkan byte/hash serta scope; file lokal saja bukan bukti. Snapshot harus lengkap, payload cocok, dan retention epoch sama. Jangan menimpa status final bertentangan atau mengubah dana berdasarkan mismatch. Reimport/restart import tidak menggandakan mutasi dana; report ID dengan isi berbeda ditandai conflict.

## Batas skenario

- Transisi otomatis ACCEPTED menjadi final; saat ini tetap nonfinal sampai review atau fixture mengubah state bank.
- Laporan valid, missing/mismatch, row duplikat/konflik, dan reimport sudah ditangani tahap 5 serta diuji melalui fixture PostgreSQL. Tidak ada endpoint merchant untuk menyuntikkan mismatch ke state bank demo; edit CSV lokal dicatat sebagai sumber tidak cocok, bukan bukti final.

Kontrol skenario hanya untuk fixture/test lokal. Test asynchronous memakai barrier/channel atau testing/synctest, tanpa time.Sleep untuk menunggu hasil. Deadline real-time menjadi pengaman test macet.
