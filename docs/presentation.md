# Deskripsi repository dan presentasi

## Deskripsi singkat repository

Simulasi transfer Go/PostgreSQL: idempotency, recovery timeout/504, worker lease, reservasi dana atomik, dan rekonsiliasi CSV dengan mock bank durable.

Deskripsi ini disiapkan untuk field About repository; belum dikirim ke GitHub. Folder lokal belum memiliki metadata Git/remote sehingga URL repository belum tersedia.

## Alur presentasi 3–5 menit

Durasi berikut adalah alokasi presentasi, bukan waktu demo yang terukur.

1. **0:00–0:40, masalah.** Tunjukkan perbedaan status HTTP dan hasil bisnis: bank bisa commit SUCCEEDED lalu merespons 504. Aplikasi tidak boleh menganggap FAILED atau membuat reference baru. Perkenalkan scope mock bank dan saldo simulasi.
2. **0:40–1:30, penyimpanan dan idempotency.** Gunakan [diagram normal](diagrams.md#transfer-normal). Satu transaksi lokal per merchant/reference, reference provider tersimpan sebelum HTTP, duplicate tidak reserve lagi. Bank juga memiliki deduplikasi durable per akun/reference.
3. **1:30–2:30, recovery.** Gunakan [diagram 504](diagrams.md#bank-sukses-tetapi-respons-504). Tunjukkan PROCESSING/HELD sebelum worker, lalu SUCCESS/CAPTURED setelah inquiry. Claim commit sebelum HTTP; token/expiry menolak stale worker. Retry habis meminta review.
4. **2:30–3:20, dana dan laporan.** Jelaskan available/held, satu settlement untuk dispatch/inquiry/report, dan unique movement. Inquiry memeriksa satu reference; CSV membandingkan snapshot. Missing bukan gagal; mismatch menjadi finding; reimport tidak menambah debit.
5. **3:20–4:20, bukti.** Tunjukkan matrix [verifikasi](verification.md), test PostgreSQL concurrent, dan [SIGKILL barriers](../internal/transfer/recovery_process_integration_test.go). Pisahkan unit race yang lulus dari integration race/CI remote yang belum terverifikasi pada tahap 6. Jangan mengubah traffic load yang belum lolos correctness menjadi klaim kapasitas.
6. **4:20–5:00, keputusan dan batasan.** LoB/SQL langsung, PostgreSQL sebagai state/job durable, lease tanpa heartbeat, retensi/epoch provider, IDR satu sumber dana, auth lokal. Tidak ada klaim exactly-once lintas sistem atau kesiapan produksi.

Untuk format tiga menit, ringkas poin dana/laporan dan bukti masing-masing menjadi satu contoh. Siapkan proses demo sebelum presentasi; jangan memasukkan unduhan tool/image ke jatah demo. [Langkah demo](demo.md) berisi command dan expected result, sedangkan [verification-publish.md](verification-publish.md) menunjukkan bagian yang benar-benar divalidasi.

## Source yang dibuka saat review

- [store.go](../internal/transfer/store.go): conflict target, SELECT terpisah, commit create/reserve.
- [recovery.go](../internal/transfer/recovery.go): claim SKIP LOCKED, inquiry-first, fencing, backoff/budget.
- [funds.go](../internal/transfer/funds.go): conditional reserve dan settlement bersama.
- [reconciliation.go](../internal/transfer/reconciliation.go): verifikasi sumber, reuse import, finding/decision.
- [migration dana](../internal/transfer/migrations/20261021000300_funds_reconciliation.sql): constraint, index settlement, kebijakan legacy.

## Pertanyaan desain yang perlu bisa dijelaskan

- Mengapa unique constraint lokal belum cukup untuk deduplikasi efek bank?
- Mengapa HTTP call berada di luar transaksi DB dan bagaimana celah crash dipulihkan?
- Apa yang terjadi jika lease habis tetapi request worker lama masih berjalan?
- Mengapa NOT_FOUND/missing laporan/retry habis tidak menjadi FAILED?
- Bukti apa yang diperlukan agar dana held boleh di-capture atau di-release?
- Apa konsekuensi reset histori provider dan batas waktu resubmit?

Ini catatan persiapan presenter, bukan tugas untuk reviewer menulis implementasi. Seluruh artefak tetap lokal untuk review.
