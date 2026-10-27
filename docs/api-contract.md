# Kontrak API

Endpoint health, create/get, dan dispatch pada dokumen ini tersedia pada implementasi saat ini, termasuk aturan dana tahap 5. Semua contoh identitas/data adalah fixture demo. Kontrak merchant memakai route tanpa prefix versi sesuai review tahap 2: `/transfers`; route `/v1/transfers` tidak didaftarkan. Route mock bank pada dokumen provider merupakan kontrak terpisah.

## Endpoint yang tersedia pada tahap 1

| Method | Path | Hasil |
| --- | --- | --- |
| GET/HEAD | `/livez` | `200`, JSON `{"status":"alive"}`. Tidak mengakses database. |
| GET/HEAD | `/readyz` | `200`/`{"status":"ready"}` bila ping PostgreSQL berhasil; `503`/`{"status":"not_ready"}` bila gagal/deadline/cancel. |

HEAD memiliki status/header yang sama tanpa response body pada server HTTP. Probe memakai `Content-Type: application/json` dan `Cache-Control: no-store`. Method lain pada path probe menghasilkan `405`; path tidak dikenal menghasilkan `404`, memakai respons default `net/http`. Format error domain di bawah belum berlaku pada error routing foundation.

## Transfer: tahap 2

Create/get, autentikasi demo, validasi payload, dan penyimpanan sudah diimplementasikan. Create/get tidak mempunyai side effect bank. Create baru mereservasi saldo atomik; dispatch eksplisit dijelaskan pada tahap provider di bawah.

### Identitas dan batas data

API menerima `Authorization: Bearer <credential-demo>` yang dipetakan server ke merchant context terautentikasi. `MERCHANT_CREDENTIALS_FILE` memilih file JSON; default `config/merchants.example.json`. Credential `demo-merchant-a-token` dipetakan ke `merchant_a`, sumber dana `funds_a`, dan akun bank integrasi `bank_account_demo_a`. Token merchant B adalah `demo-merchant-b-token`, sumber dana `funds_b`, dan akun integrasinya sama. Semua token contoh bersifat publik dan hanya untuk demo lokal.

Loader mewajibkan satu konfigurasi per merchant dan token unik; ID merchant/akun memakai 1–64 karakter ASCII `[A-Za-z0-9._-]`. Token berisi 16–256 karakter ASCII terlihat tanpa spasi. Config dibaca saat startup; perubahan membutuhkan restart. Token di-hash SHA-256 sebagai key lookup; context hanya membawa identitas merchant/akun, bukan token. Credential tidak ditulis ke tabel transfer atau log.

Client tidak memilih `merchant_id`, sumber dana, akun bank integrasi, status, atau provider reference. Field tersebut berasal dari context/config/server dan input yang mencoba memasukkannya ditolak sebagai unknown field. Merchant tidak dapat membaca resource milik merchant lain; get mengembalikan `404` baik untuk ID tidak ada maupun milik merchant lain.

### POST `/transfers`

Content type `application/json`, maksimum body 4 KiB, satu JSON object tanpa trailing object/data dan tanpa unknown field.

```json
{
  "reference_id": "order-001",
  "amount": 150000,
  "bank_code": "001",
  "account_number": "00123456789"
}
```

`001` di sini kode mock, bukan pernyataan dukungan bank nyata.

| Field | Validasi dan perbandingan |
| --- | --- |
| `reference_id` | 1–64 karakter ASCII `[A-Za-z0-9._-]`, case-sensitive, bagian key merchant. |
| `amount` | Literal integer JSON positif, 1 sampai 9223372036854775807, rupiah IDR; pecahan/exponent ditolak, tanpa float. |
| `bank_code` | Tepat 3 digit ASCII, string. |
| `account_number` | 1–34 digit ASCII, string; nol awal dipertahankan. |

Tidak ada trimming, perubahan case, atau konversi rekening menjadi integer. Input di luar aturan ditolak, sehingga nilai tersimpan dan nilai pembanding identik. Currency selalu IDR, tidak dapat dipilih client. Keberadaan rekening nyata tidak divalidasi pada MVP.

Request baru: `201 Created`, `Location: /transfers/{id}`. INSERT baru berstatus PENDING dan jadwal awal tersimpan atomik pada row yang sama. Worker aktif dapat memajukan status sebelum response dibaca client; 201 berarti resource tersimpan, bukan bank sukses. Contoh bentuk response:

```json
{
  "id": "trf_example",
  "reference_id": "order-001",
  "provider_reference": "prv_example",
  "amount": 150000,
  "currency": "IDR",
  "bank_code": "001",
  "account_number": "00123456789",
  "status": "PENDING",
  "created_at": "2026-10-20T06:00:00Z",
  "updated_at": "2026-10-20T06:00:00Z",
  "funds_state": "HELD"
}
```

ID/reference bersifat opaque dan dibuat server; contoh bukan nilai yang harus di-hardcode. Timestamp RFC 3339 UTC. Response rekening hanya untuk merchant pemilik; log harus meredaksinya.

Idempotency memakai `UNIQUE(merchant_id, reference_id)`:

- Key sama + `amount`, `bank_code`, `account_number` sama: `200 OK` dengan transaksi existing dan status terkininya, pada seluruh status. Jangan mengubah payload, mengalokasikan reference baru, atau mereservasi dana lagi.
- Key sama + salah satu field pembanding berbeda: `409 Conflict`, apa pun status sebelumnya, termasuk SUCCESS/FAILED. Tidak mengganti data existing.
- Reference merchant sama pada merchant berbeda membentuk transaksi berbeda. Source/account konfigurasi baru tidak mengubah transaksi existing.

Validasi sintaks/tipe/payload tetap diperlukan. Aturan conflict di atas berlaku untuk payload transfer yang valid; malformed JSON/amount invalid menghasilkan `400`.

### GET `/transfers/{id}`

`200 OK` dengan bentuk resource sama, atau `404` untuk tidak ditemukan/tidak dimiliki merchant. Status HTTP `200` berarti pembacaan berhasil; nilai `status` menentukan apakah transfer final.

### POST `/transfers/{id}/dispatch`: tahap 3

Bearer merchant wajib dan body harus kosong. Merchant lain/ID tidak ada menerima 404. Endpoint sinkron ini hanya mengklaim PENDING. UPDATE status PROCESSING dan INSERT attempt dilakukan dalam satu transaksi singkat, lalu commit sebelum HTTP submit. Claim pesaing atau dispatch ulang pada PROCESSING/SUCCESS/FAILED hanya mengembalikan existing tanpa memulai pekerjaan tambahan. Worker terpisah dapat memproses jadwal durable yang sudah ada.

HTTP API 200 berarti observasi dispatch/lookup tersimpan, bukan transfer pasti sukses. Periksa status resource:

- SUCCEEDED dengan identitas, scope akun, payload, dan operation ID valid → SUCCESS.
- REJECTED dengan bukti cocok dan reason code valid → FAILED.
- ACCEPTED, HTTP bank non-200 termasuk 504/500/409, body invalid/terlalu besar, timeout, atau cancellation → PROCESSING.

Setiap HTTP dibatasi PROVIDER_TIMEOUT. Sebelum submit, inquiry mengenali epoch dataset dan scope akun; epoch/window resubmit disimpan dengan fencing sebelum bank boleh dipanggil. Respons bank maksimum 8 KiB, body selalu ditutup; redirect tidak diikuti dan proxy environment tidak dipakai. Akun/token bank dipilih dari provider_account_id existing, bukan konfigurasi merchant terbaru atau request body. Provider reference existing dipertahankan.

Status final dan evidence attempt disimpan atomik. Respons terlambat tidak menurunkan status final; bukti final berlawanan tercatat sebagai FINAL_CONFLICT untuk review. Jika caller canceled, pencatatan hasil tetap dicoba dengan context DB baru yang dibatasi DB_TIMEOUT. Bila penyimpanan hasil gagal, API dapat mengembalikan 503 sementara bank sudah sukses; gunakan GET untuk membaca status lokal, jangan membuat transaksi/reference baru.

Tahap 4 menambahkan proses cmd/worker: PENDING diklaim untuk pengiriman awal, PROCESSING yang jatuh tempo di-inquiry. Lease/token dan expiry diperiksa pada update hasil, schedule, dan pelepasan lease. Crash dipulihkan setelah lease habis, dengan reference/payload existing. NOT_FOUND tidak berarti FAILED dan tidak menjadi izin resubmit tanpa epoch/window yang cocok. Endpoint dispatch tetap tidak mengirim ulang PROCESSING secara langsung. Jadwal dan counter bukan antrean di memory.

### Error domain

```json
{"error":{"code":"PAYLOAD_CONFLICT","message":"reference_id already used with a different payload"}}
```

| HTTP | Code | Makna |
| --- | --- | --- |
| 400 | `INVALID_REQUEST` | JSON/field/amount tidak valid. |
| 401 | `UNAUTHENTICATED` | Credential tidak ada/tidak dikenal. |
| 404 | `NOT_FOUND` | Resource tidak ada/tidak dimiliki merchant. |
| 409 | `PAYLOAD_CONFLICT` | Key existing dengan payload valid berbeda. |
| 413 | `REQUEST_TOO_LARGE` | Body melewati batas. |
| 415 | `UNSUPPORTED_MEDIA_TYPE` | Content type salah. |
| 503 | `UNAVAILABLE` | Penyimpanan sementara tidak tersedia. |

Transaksi baru tanpa saldo cukup mengembalikan `422` dengan kode `INSUFFICIENT_FUNDS`; tidak ada transfer/reservasi parsial. Saldo yang berubah tidak mengubah respons duplicate/conflict transaksi existing. Inisialisasi saldo operator dan migrasi transaksi legacy dijelaskan di [dana dan rekonsiliasi](funds-reconciliation.md).

Jika respons create hilang atau menerima timeout/5xx, caller mengulang key dan payload yang sama. Error di batas commit dapat terjadi meskipun data sudah committed; jangan mengasumsikan row tidak ada dan membuat key baru. Kegagalan HTTP API tidak otomatis menjadi status transfer `FAILED`.

Response transfer/error memakai JSON dan `Cache-Control: no-store`. Method/path tidak terdaftar mengikuti respons routing default `net/http` (405/404). GET juga mendukung HEAD melalui `net/http`. Detail SQL dan nilai yang ditolak constraint tidak dikirim ke client atau log; error penyimpanan menjadi 503. Jika lookup duplicate tidak menemukan row setelah konflik, aplikasi mengembalikan 503 tanpa insert ulang otomatis; API tahap ini tidak mempunyai operasi delete.

### Metadata recovery: tahap 4

GET/create/dispatch dapat membawa needs_review=true dan review_reason. Field ini read-only; client tidak dapat mengaturnya. Contoh: status PROCESSING, needs_review true, review_reason RETRY_BUDGET_EXHAUSTED. Merchant isolation tetap berlaku. Field false/kosong dihilangkan dari JSON agar response lama tetap ringkas.

Worker menyimpan jumlah claim, batas default 8, due time, last error, token/expiry dan epoch/window pada PostgreSQL. Budget mencakup dispatch awal, inquiry, serta claim yang ditinggalkan proses crash. Satu claim dapat melakukan inquiry lalu satu submit bersyarat. Budget habis mempertahankan PROCESSING dan menonaktifkan retry otomatis. PROVIDER_ACCOUNT_MISMATCH, PROVIDER_RETENTION_CHANGED, RESUBMIT_WINDOW_EXPIRED, serta konflik payload/final memerlukan review. Legacy PROCESSING tanpa otorisasi resubmit tetap melakukan inquiry terbatas, lalu review jika tidak ada hasil pasti.

Tidak ada endpoint reset status, penggantian reference, atau pemaksaan resubmit. Inquiry berbeda dari laporan rekonsiliasi tahap 5. Lihat [recovery](recovery.md), [dana dan rekonsiliasi](funds-reconciliation.md), dan [verifikasi recovery](verification-recovery.md) untuk policy/operasi dan bukti.
