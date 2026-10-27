# Validasi dokumentasi portfolio

Tanggal: 23 Oktober 2026. Scope tahap 7 adalah dokumentasi berdasarkan implementasi dan bukti tahap 1–6. Kode bisnis, migration, credential pengguna, dan database demo tidak diubah pada tahap ini.

## Artefak untuk review

| File | Isi/perubahan |
| --- | --- |
| [README](../README.md) | Masalah, fitur aktual, LoB, struktur, setup/migration/config, credential publik, API/status, test/demo, bukti dan batasan. |
| [Diagram](diagrams.md) | Tiga diagram Mermaid terpisah: normal, 504/inquiry, serta reservasi/capture/release. |
| [Demo](demo.md) | Command/expected result tujuh skenario, query observasi tanpa cleanup demo, serta test crash/concurrency/mismatch terisolasi. |
| [Artikel](article.md) | Draft Indonesia dengan potongan kode aktual dan tautan relatif ke source/test. |
| [Presentasi](presentation.md) | Deskripsi repository dan alokasi presentasi 3–5 menit. |
| [Desain](design.md) | Memperbaiki uraian autocommit lama menjadi transaksi READ COMMITTED create/reserve; claim/inquiry sesuai kode saat ini. |
| [Kontrak API](api-contract.md) | Menyelaraskan scope dan menambahkan funds_state pada contoh response. |
| [Kontrak provider](provider-contract.md) | Menghapus klaim lama bahwa laporan/mismatch/reimport belum tersedia. |
| [Panduan lokal](local.md) | Daftar binary build mencakup loadtest. |

README berfungsi sebagai pintu masuk; detail langkah per skenario berada di demo agar pembaca dapat mengamati PROCESSING/held sebelum recovery. Artikel menjelaskan alasan desain dan menghubungkannya dengan bukti, tanpa mengubah hasil expected menjadi klaim run aktual.

## Lingkungan dan isolasi

Host: Go `go1.26.2 darwin/arm64`, mise `2026.4.19 macos-arm64`, dbmate `2.28.0`. Pin proyek tetap Go `1.26.10`; unduhan/eksekusi versi pin tersebut belum divalidasi pada sesi ini.

Source, config contoh, script, dan file tool/Compose disalin ke direktori sementara baru `transfer101-publish-*`. Salinan awal tidak membawa `.env`/`.env.mockbank` pengguna, binary, atau data database. Build menggunakan cache module yang sudah tersedia dan `GOPROXY=off`, sehingga ini bukan bukti bahwa unduhan dependency dari mesin kosong telah berhasil.

Mise menggunakan `MISE_TRUSTED_CONFIG_PATHS` pada salinan sementara, cache/state di `/tmp`, serta `--skip-tools` untuk memakai Go host. Trust/config global tidak diubah. `GOCACHE` diarahkan ke `/tmp/transfer101-publish-gocache`; auto-install task dinonaktifkan. Mise tetap memperingatkan kegagalan lookup versi remote karena `github.com` tidak dapat di-resolve, tetapi command build/validasi/test di bawah berhasil menggunakan tool host.

## Pemeriksaan aktual

| Pemeriksaan | Hasil |
| --- | --- |
| `mise tasks validate` pada salinan terisolasi | Exit 0; 27 task valid. Ada peringatan lookup versi remote/DNS seperti di atas. |
| `mise run --skip-tools env:init` | Exit 0; membuat env dari contoh di salinan sementara. Tidak menyentuh env workspace pengguna. |
| `mise run --skip-tools build` | Exit 0; tujuh executable native dibangun dari source salinan. |
| `mise run --skip-tools test` | Exit 0; menjalankan `go test -race -count=1 -timeout=60s ./...`, seluruh package bertest lulus. Tanpa tag integration. |
| `./bin/local/demo-provider --help` dan `./bin/local/loadtest -h` | Exit 0; bentuk argumen/flag sesuai command dokumentasi. Tidak melakukan HTTP/DB. |
| `docker compose config --quiet` | Exit 0; validasi konfigurasi lokal, tidak mengakses database. |
| Shell command blocks | Diperiksa dengan `sh -n` dan `zsh -n`; sintaks valid. Ini tidak mengeksekusi curl/SQL/migration. |
| Payload curl | Tiga payload diperiksa setelah ekspansi shell dengan curl stub; JSON, field, integer amount, dan nol awal rekening sesuai contoh. Tidak mengirim request. |
| Tautan relatif/anchor | Diperiksa terhadap file dan heading aktual. Tidak memverifikasi URL eksternal. |
| Potongan artikel dan nama test | Semua potongan Go/SQL ditemukan di source aktual setelah normalisasi whitespace; nama test yang disebut ditemukan di file integration test. |

Test unit/race di atas dijalankan ulang untuk memeriksa command yang didokumentasikan. Bukti PostgreSQL dan focused `-count=20` dari tahap 6 tetap merujuk [verification.md](verification.md); tidak diklaim sebagai run baru tahap 7.

## Belum dapat diverifikasi

- `docker compose ps` gagal mengakses socket Docker dengan `connect: operation not permitted`. Karena itu setup/migration pada database bersih, tujuh skenario HTTP/DB, query SQL observasi, crash integration, dan reimport tidak dieksekusi ulang pada tahap ini. Expected result telah dicocokkan dengan kode/test; bukan hasil runtime baru.
- `mise install` dan pengunduhan Go 1.26.10 belum dijalankan. Lookup GitHub pada mise gagal karena DNS dibatasi. Build/test aktual memakai Go 1.26.2 yang tersedia, bukan versi pin baru.
- Renderer Mermaid CLI tidak tersedia; blok diagram ditinjau sebagai source, tetapi hasil render visual belum diverifikasi.
- CI GitHub remote belum dijalankan. Folder lokal tidak memiliki `.git`, sehingga remote/URL repository tidak tersedia. Tidak ada URL repository atau badge sukses yang dibuat.
- Bukti tahap 6 masih mencatat load correctness yang belum berhasil diulang sesudah perbaikan query, serta integration race final yang belum terverifikasi. Artikel/README mempertahankan batas bukti itu dan tidak mengklaim benchmark.

## Checklist review

- [ ] README menjelaskan scope simulasi dan hanya fitur yang tersedia.
- [ ] Command/default credential sesuai konfigurasi lokal yang akan dipresentasikan.
- [ ] Demo 504 memperlihatkan PROCESSING/held sebelum worker, lalu SUCCESS/capture dengan reference sama.
- [ ] Perbedaan restart manual dan crash SIGKILL pada barrier test jelas.
- [ ] Semua klaim artikel dapat ditelusuri ke source/test dan hasil aktual, dengan batas kontrak provider dinyatakan.
- [ ] Preview Mermaid serta jalankan demo database terisolasi di lingkungan dengan akses Docker sebelum publikasi.
- [ ] Isi repository About, naskah, dan bahan presentasi disetujui; URL baru diisi jika remote aktual tersedia.

Seluruh artefak diserahkan lokal. Tidak ada push, deploy, pembuatan remote, atau publikasi artikel pada tahap ini.
