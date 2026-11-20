# RENCANA: Live Chat Emulator — Modul Pengaduan Otomatis (WhatsApp)

Dokumen ini adalah **rencana**, belum ada implementasi.  
Target kerja: `/var/www/kominfo/whatshap-gateway`

---

## 1. Ringkasan & Tujuan

Membangun **modul terpisah** bernama **Live Chat Emulator** untuk menguji alur
percakapan Bot WhatsApp Pemko Medan pada skenario **pengaduan/laporan warga**
secara otomatis dan multi-giliran.

Modul ini:
- Menampilkan emulator chat bergaya WhatsApp (satu kartu per nomor WA penguji
  yang terhubung, maksimal 3).
- Menjalankan **loop percakapan otomatis** sampai Bot menyatakan pengaduan
  selesai/diterima — meniru warga yang sedang mengisi form pengaduan.
- Menyimpan seluruh riwayat chat ke **database** (header sesi + detail pesan +
  log per giliran).
- Mengekspor **ringkasan per sesi** ke file Excel terpisah; tombol Download
  hanya membaca file yang sudah ada (tanpa generate ulang).

Prinsip utama: **modul terpisah, tidak menyentuh** sheet dan alur uji yang
sudah ada (`Bank Pertanyaan`, `Petunjuk`, `Ringkasan`, `Uji Diluar Bank`,
`Uji Stress AI`).

---

## 2. Batasan (Non-Goals)

- Tidak membaca/menampilkan inbox WhatsApp pribadi penguji.
- Tidak menambah/mengubah kolom atau sheet pada workbook uji yang sudah ada.
- Tidak mengubah `internal/runner` (mesin uji Bank/OOB/Stress tetap apa adanya).
- Tidak memakai tabel `public.*` yang sudah berjalan.
- Tidak menjalankan migration/seed ke database tanpa persetujuan eksplisit.

---

## 3. Keputusan Arsitektur

| Aspek | Keputusan | Alasan |
|---|---|---|
| Database | **PostgreSQL yang sama** dengan aplikasi, tetapi **schema baru `livechat`** | Sesuai permintaan; menghindari instance DB baru, dan `search_path` default `public` tetap utuh sehingga tabel lama tidak terpengaruh |
| Kode | Paket baru `internal/livechat` + `internal/livechat/store` | Isolasi logika, zero perubahan pada paket lain |
| HTTP | Subrouter `/api/livechat/*` pada server yang sama | Satu proses, tanpa port tambahan |
| Excel | **File terpisah**: `data/users/{id}/livechat.xlsx` | File uji lama tidak tersentuh |
| Web | **Tab baru** `Live Chat Emulator` | Navigasi konsisten dengan UI yang sudah ada |
| Runner | Engine baru (loop percakapan), bukan `runner.StartBatch` | Siklus percakapan berbeda total dari uji satu-tanya-satu-jawab |
| WA Client | Reuse `internal/wa` + tambah **registry kolektor** + `SendMedia`/`SendLocation` | Butuh kolektor aktif bersamaan dengan collector sistem uji tanpa saling menimpa, dan perlu kirim berkas/lokasi |
| Berkas | **Aset milik sendiri** di `data/users/{id}/livechat_assets/`, content-addressed SHA-256 | Dikirim berulang tanpa upload ulang; tidak menyentuh berkas uji lama; tidak mengambil dari WhatsApp pribadi |

### 3.1 Isolasi schema

```sql
CREATE SCHEMA IF NOT EXISTS livechat;
SET search_path = public;            -- default tetap
-- semua tabel livechat memakai prefix schema eksplisit
```

Akses kode livechat memakai koneksi `?search_path=livechat,public` **khusus
untuk modul ini**, atau seluruh query memakai nama schema eksplisit
(`livechat.live_sessions`). Pendekatan yang dipilih: **nama schema eksplisit**
agar tidak bergantung pada parameter koneksi dan tidak ada peluang salah
sasaran ke `public`.

---

## 4. Skema Database (`livechat`)

### 4.1 `livechat.sessions` — header per pengaduan

| Kolom | Tipe | Keterangan |
|---|---|---|
| `id` | BIGSERIAL PK | ID sesi |
| `user_id` | BIGINT NOT NULL | Pemilik sesi (FK logis ke `public.users.id`, **tanpa** FK fisik lintas schema) |
| `device_uuid` | VARCHAR(64) NOT NULL | Nomor WA penguji yang dipakai |
| `wa_phone` | VARCHAR(30) NOT NULL | Denormalisasi nomor HP penguji saat sesi dibuat |
| `bot_phone` | VARCHAR(30) NOT NULL | Nomor Bot target |
| `title` | VARCHAR(200) NOT NULL | Judul pengaduan |
| `report_text` | TEXT NOT NULL | Isi laporan/pengaduan dari penguji |
| `nama_pengadu` | VARCHAR(120) NOT NULL | Diisi acak saat form dibuka, dapat diganti pengguna |
| `tanggal_kejadian` | DATE NOT NULL | Acak (1–14 hari terakhir), dapat diganti |
| `alamat_kejadian` | TEXT NOT NULL | Acak dari daftar jalan/kecamatan Medan, dapat diganti |
| `titik_lokasi` | VARCHAR(200) | Opsional, mis. `https://maps.google.com/?q=3.59,98.67` |
| `asset_ids` | BIGINT[] | Daftar `livechat.assets.id` yang dipakai sesi ini |
| `max_turns` | INT NOT NULL DEFAULT 40 | Batas pengaman giliran (dapat diubah per sesi/user) |
| `category_mode` | VARCHAR(20) NOT NULL | `benar` \| `typo` \| `ngasal` \| `celah` |
| `status` | VARCHAR(20) NOT NULL | `running` \| `finished` \| `failed` \| `stopped` \| `timeout` |
| `end_reason` | TEXT | Alasan berhenti (pesan bot / manual / batas giliran) |
| `last_chat_excerpt` | TEXT | Cuplikan 3 gelembung terakhir saat sesi ditutup |
| `ticket_no` | VARCHAR(40) | Nomor tiket dari bot, mis. `385692` |
| `total_turns` | INT NOT NULL DEFAULT 0 | Jumlah giliran |
| `created_at` | TIMESTAMPTZ NOT NULL DEFAULT now() | |
| `started_at` | TIMESTAMPTZ | Awal pengiriman pesan pembuka |
| `ended_at` | TIMESTAMPTZ | |

Indeks: `(user_id, created_at DESC)`, `(status)`.

### 4.2 `livechat.messages` — riwayat gelembung chat

| Kolom | Tipe | Keterangan |
|---|---|---|
| `id` | BIGSERIAL PK | |
| `session_id` | BIGINT NOT NULL REFERENCES livechat.sessions(id) ON DELETE CASCADE | |
| `turn_no` | INT NOT NULL | Giliran ke-N (0 = pembuka) |
| `direction` | VARCHAR(10) NOT NULL | `sent` \| `received` |
| `body` | TEXT | Teks gelembung (caption bila ada media) |
| `media_json` | JSONB | `[{kind: image\|document\|location, file, mimetype, size, caption}]` |
| `msg_type` | VARCHAR(12) NOT NULL DEFAULT 'text' | `text` \| `image` \| `document` \| `location` |
| `sent_at` | TIMESTAMPTZ NOT NULL DEFAULT now() | |
| `latency_ms` | INT | Selisih kirim → balas (untuk `received`) |

Indeks: `(session_id, id)`.

### 4.3 `livechat.turns` — log detail per giliran (audit QC)

| Kolom | Tipe | Keterangan |
|---|---|---|
| `id` | BIGSERIAL PK | |
| `session_id` | BIGINT NOT NULL REFERENCES livechat.sessions(id) ON DELETE CASCADE | |
| `turn_no` | INT NOT NULL | |
| `bot_question` | TEXT | Pertanyaan bot yang terdeteksi |
| `question_kind` | VARCHAR(30) | `nama` \| `isi_laporan` \| `tanggal` \| `alamat` \| `titik_lokasi` \| `berkas` \| `konfirmasi` \| `lainnya` |
| `our_answer` | TEXT | Jawaban yang kita kirim (teks + penanda `[Berkas]`/`[Koordinat]`) |
| `sent_media_json` | JSONB | Berkas/lokasi yang benar-benar dikirim pada giliran ini |
| `category_mode` | VARCHAR(20) | Mode yang dipakai giliran ini |
| `match_status` | VARCHAR(20) | `ok` \| `typo` \| `ngasal` \| `celah` |
| `note` | TEXT | Catatan evaluasi |
| `created_at` | TIMESTAMPTZ NOT NULL DEFAULT now() | |

Indeks unik: `(session_id, turn_no)`.

### 4.4 `livechat.assets` — berkas milik pengguna untuk dikirim berulang

| Kolom | Tipe | Keterangan |
|---|---|---|
| `id` | BIGSERIAL PK | |
| `user_id` | BIGINT NOT NULL | Tanpa FK lintas schema |
| `kind` | VARCHAR(10) NOT NULL | `image` \| `document` |
| `label` | VARCHAR(120) NOT NULL | Nama tampil, mis. "Foto Jalan Rusak" |
| `file_name` | VARCHAR(200) NOT NULL | Nama file asli saat diunggah |
| `stored_path` | TEXT NOT NULL | Relatif terhadap `data/users/{id}/livechat_assets/` |
| `mimetype` | VARCHAR(100) NOT NULL | |
| `size_bytes` | BIGINT NOT NULL | |
| `sha256` | VARCHAR(64) NOT NULL | Deteksi duplikat & integritas |
| `created_at` | TIMESTAMPTZ NOT NULL DEFAULT now() | |

Indeks: `(user_id, created_at DESC)`, unik `(user_id, sha256)`.

**Aturan penyimpanan berkas (di luar database):**

```text
data/users/{user_id}/livechat_assets/{sha256[:2]}/{sha256}{ext}
```

- Potongan direktori `sha256[:2]` mencegah direktori terlalu besar.
- Nama file = SHA-256 (content-addressed) sehingga **berkas yang sama tidak
  disimpan dua kali** meski diunggah berkali-kali.
- Batas ukuran unggah: 16 MB per berkas, total aset per user 500 MB
  (`LIVECHAT_MAX_ASSET_BYTES`), mimetype allowlist: `image/png`, `image/jpeg`,
  `application/pdf`, `application/vnd.openxmlformats-officedocument.*`.
- Hapus aset hanya jika tidak dipakai sesi mana pun (cek `sent_media_json`).


---

## 5. Struktur Kode (baru, tanpa menyentuh yang ada)

```text
internal/livechat/
├── model.go          # struct Session, Message, Turn, Asset, SessionSummary
├── store.go          # CRUD ke schema livechat (prefix schema eksplisit)
├── assets.go         # simpan/baca berkas aset (content-addressed, sha256)
├── excel.go          # tulis ringkasan per sesi ke livechat.xlsx (atomic)
├── engine.go         # loop percakapan otomatis (state machine)
├── responder.go      # pembentuk jawaban: AI (jika ada) -> fallback lokal
├── detector.go       # deteksi jenis pertanyaan bot + deteksi "selesai"
├── media.go          # upload ke WhatsApp (image/document/location) + mapping
└── manager.go        # orchestrator per user: sesi aktif per device_uuid

internal/web/livechat.go   # handler HTTP baru (/api/livechat/*)
public/livechat.js         # UI emulator (modul JS terpisah)
```

Perubahan minimal pada file existing (hanya *wiring*, tanpa mengubah logika):

| File | Perubahan |
|---|---|
| `internal/database/store.go` | Tambah `MigrateLiveChat()` (DDL schema `livechat` saja) |
| `internal/wa/client.go` | Tambah **registry kolektor** (`collectors sync.Map`) + `SendMedia`/`SendLocation` — kolektor & method lama tetap dipakai runner |
| `internal/web/server.go` | Daftarkan route `/api/livechat/*` + endpoint media livechat |
| `public/index.html` | Tambah 1 tombol tab `Live Chat Emulator` + section kosong |
| `public/styles.css` | Style emulator (grid, bubble, dsb.) |
| `main.go` | Init `livechat.Manager` dan inject ke server |

> Semua penambahan **aditif**. Tidak ada penghapusan/penulisan ulang pada
> `internal/runner`, `internal/excel`, atau tabel `public.*`.

---

## 6. Mesin Percakapan (State Machine)

### 6.1 Status sesi

```
created -> running -> (finished | failed | stopped | timeout)
```

### 6.2 Alur

1. Pengguna memilih device WA aktif, mengisi **judul**, **teks pengaduan**,
   **kategori mode**, dan (opsional) memilih **aset berkas** yang akan dikirim.
2. **Field pengaduan punya awalan acak namun bisa diganti** (keputusan no. 3):
   - Saat form dibuka, sistem mengisi otomatis:
     - `nama_pengadu` → nama acak Indonesia (mis. "Budi Santoso")
     - `tanggal_kejadian` → tanggal acak 1–14 hari terakhir (`YYYY-MM-DD`)
     - `alamat_kejadian` → alamat acak dari daftar kecamatan/jalan Kota Medan
   - Semua field dapat **diketik ulang/diganti** oleh pengguna sebelum sesi dimulai.
   - Nilai final disimpan di `sessions` (kolom baru, lihat §4.1) dan dipakai
     persis saat bot menanyakan hal tersebut — jadi isian deterministik, tidak
     berubah-ubah di tengah sesi.
3. Sesi dibuat (`status=running`), emulator langsung tampil.
4. Kirim pesan pembuka (mis. `"saya mau buat pengaduan"`) dengan ritme manusia.
5. **Loop**:
   1. Tunggu balasan bot (quiet-window 5,5 detik; timeout 35 detik).
   2. Simpan gelembung ke `messages` (`direction=received`, `msg_type` sesuai).
   3. Deteksi `question_kind` menggunakan `detector.go`.
   4. Susun jawaban via `responder.go` sesuai kategori.
   5. Kirim jawaban (ritme manusia: pre-think → typing bertahap → kirim).
      - Jika giliran membutuhkan **berkas**: kirim media lewat `media.go`
        (lihat §6.5) lalu catat `sent_media_json`.
   6. Simpan ke `messages` (`sent`) + `turns`.
   7. Cek kondisi selesai.
6. Tutup sesi → tulis baris ringkasan ke `livechat.xlsx`.

### 6.3 Kondisi selesai (`end_reason`)

| Kondisi | Status |
|---|---|
| Bot menyebut `selesai`, `terkirim`, `diterima`, `berhasil dikirim`, atau memuat tiket `#\d{5,6}` | `finished` |
| Batas pengaman giliran tercapai (**default 40**, `LIVECHAT_MAX_TURNS`, dapat diubah per user) | `timeout` |
| Koneksi WA terputus | `failed` |
| Pengguna menekan Stop | `stopped` |

**Semua kondisi menutup sesi dan tetap menulis baris Excel** — termasuk
`failed`, `timeout`, dan `stopped` (keputusan no. 4). Baris tersebut juga
menyertakan **cuplikan chat terakhir** (lihat §9).

### 6.4 Deteksi jenis pertanyaan

Pencocokan berbasis pola pada teks balasan bot (case-insensitive):

| `question_kind` | Pola |
|---|---|
| `nama` | `isi nama`, `nama lengkap`, `nama pengadu` |
| `isi_laporan` | `isi laporan`, `pengaduan`, `tuliskan aduan` |
| `tanggal` | `tanggal kejadian`, `format: yyyy-mm-dd` |
| `alamat` | `alamat kejadian`, `alamat lengkap` |
| `titik_lokasi` | `titik lokasi`, `koordinat`, `tautan peta` |
| `berkas` | `unggah`, `lampirkan`, `foto`, `dokumen`, `bukti` |
| `konfirmasi` | `apakah anda ingin`, `ya/tidak` |
| `lainnya` | tidak ada pola cocok |

### 6.5 Pengiriman Berkas & Lokasi (baru)

Bot terkadang meminta bukti/foto/dokumen. Modul mendukung ini secara eksplisit.

**Sumber berkas:** folder milik kita di server, **bukan** dari WhatsApp pribadi.

```text
data/users/{user_id}/livechat_assets/{sha256[:2]}/{sha256}{ext}
```

- Pengguna mengunggah aset lewat UI (satu kali), lalu aset itu **dikirim
  berulang** ke bot pada sesi mana pun tanpa perlu upload ulang.
- Nama file = SHA-256 → duplikat otomatis dihindari.

**Cara kirim (memakai API `whatsmeow` yang sudah tersedia):**

| Jenis | Langkah |
|---|---|
| `image` | `cli.Upload(ctx, data, whatsmeow.MediaImage)` → `waE2E.ImageMessage{Caption, Mimetype, URL, DirectPath, MediaKey, FileEncSHA256, FileSHA256, FileLength}` → `SendMessage` |
| `document` | `cli.Upload(ctx, data, whatsmeow.MediaDocument)` → `waE2E.DocumentMessage{FileName, Mimetype, URL, DirectPath, ...}` → `SendMessage` |
| `location` | `waE2E.LocationMessage{DegreesLatitude, DegreesLongitude, Name, Address}` → `SendMessage` (tanpa upload) |

**Aturan pemilihan berkas per giliran (`question_kind = berkas`):**

1. Jika sesi punya aset terpilih, kirim **aset pertama** + caption singkat
   (mis. "ini fotonya pak").
2. Jika tidak ada aset, jawab teks `lewat` (bot biasanya melanjutkan karena
   lampiran umumnya opsional).
3. Mode `celah` boleh mengirim dokumen **tipe salah** (mis. PDF saat diminta
   foto) untuk menguji validasi bot — tetap dari aset milik penguji sendiri.

**Batas & keamanan:**

- Ukuran unggah ≤ 16 MB/berkas; mimetype allowlist (png, jpeg, pdf, docx, xlsx).
- Berkas divalidasi ulang sebelum dikirim (sha256 + mimetypenya harus sama
  dengan saat unggah).
- Media yang **diterima** dari bot disimpan di `data/users/{id}/livechat_media/`
  dan direferensikan lewat `media_json` (bukan disalin ke `data/downloads/` milik
  sistem uji lama).
- Thumbnail gambar yang diterima diunduh sekadar untuk pratinjau UI; ukuran
  dibatasi (`LIVECHAT_MEDIA_MAX_BYTES`, default 8 MB) agar tidak membebani disk.

---

## 7. Pembentuk Jawaban (Responder)

**Rekomendasi: AI bila tersedia, fallback lokal wajib jalan.** (Keputusan no. 1)

1. **AI (jika `LLM_BASE_URL` + `LLM_MODEL` terisi)** — pakai `internal/ai` yang
   sudah ada. Prompt singkat per giliran, suhu rendah, keluaran dibatasi 1–2
   kalimat agar mirip chat warga.
2. **Fallback lokal (selalu tersedia)** — template deterministik per
   `question_kind`, memakai **nilai final dari sesi** (bukan nilai acak baru):
   - `nama` → `sessions.nama_pengadu`
   - `isi_laporan` → `sessions.report_text`
   - `tanggal` → `sessions.tanggal_kejadian` (`YYYY-MM-DD`)
   - `alamat` → `sessions.alamat_kejadian`
   - `titik_lokasi` → `sessions.titik_lokasi` bila ada, selain itu `lewat`
   - `berkas` → aset terpilih (`asset_ids`), atau `lewat` bila kosong
   - `konfirmasi` → `iya`
   - `lainnya` → jawaban netral yang mendorong bot lanjut
3. **Transformasi mode** (diterapkan setelah jawaban dasar terbentuk):

| Mode | Transformasi |
|---|---|
| `benar` | apa adanya |
| `typo` | typo jari wajar + singkatan chat (`yg`, `brp`, `dmn`) pada kata umum; angka/tanggal/alamat tetap valid |
| `ngasal` | jawaban di luar konteks namun tidak vulgar (uji ketahanan bot) |
| `celah` | jawaban yang menguji batas logika bot (mis. tanggal mustahil, alamat kosong, minta lewati semua field, kirim dokumen jenis salah) |

> Untuk mode `ngasal` dan `celah`, tanggal/alamat **tidak** boleh memalsukan
> data agar bot tetap dapat menyelesaikan pengaduan sesuai skenario uji.
> Nilai acak dihitung **sekali** saat sesi dibuat; setelah itu konsisten.

---

## 8. Ritme Manusia (Reuse)

Modul memakai parameter `model.HumanSettings` yang sudah ada per user
(`user_settings`), sehingga tetap menerapkan:
- Pre-think (`2–5 dtk`)
- Typing bertahap (multi-burst)
- Jeda sebelum `MarkRead` + waktu baca
- Jeda antar giliran (`8–16 dtk`)
- Micro-break setelah 12–18 giliran

Tidak ada perubahan pada nilai default; livechat hanya *membaca* setting user.

---

## 9. Excel Ringkas (per sesi)

- File: `data/users/{user_id}/livechat.xlsx`
- Dibuat otomatis saat sesi selesai; **atomic write** (`.tmp` → `rename`).
- Kolom:

| No | Kolom |
|---|---|
| A | No |
| B | Tanggal Sesi |
| C | Nomor WA Penguji |
| D | Nomor WA Bot |
| E | Kategori Mode |
| F | Judul Pengaduan |
| G | Jumlah Giliran |
| H | Status Akhir |
| I | Alasan Selesai |
| J | Nomor Tiket |
| K | Keterangan (gagal/error/tidak dijelaskan bot) |
| L | **Chat Terakhir** (cuplikan 3 gelembung terakhir) |
| M | Berkas Terkirim (nama aset) |

- **Semua status ditulis**, termasuk `failed`, `timeout`, dan `stopped`
  (keputusan no. 4). Kolom `L` diisi dari `sessions.last_chat_excerpt` yang
  disimpan saat sesi ditutup — **bukan** digenerate ulang dari AI.
- **Tombol Download** hanya membaca file yang ada (`GET /api/livechat/excel/download`).
  Tidak ada proses generate kalimat ulang.

---

## 10. API Endpoint (baru)

| Method | Path | Fungsi |
|---|---|---|
| GET | `/api/livechat/sessions` | Daftar sesi (header) milik user |
| GET | `/api/livechat/sessions/{id}` | Header + pesan + turns |
| POST | `/api/livechat/sessions` | Buat & mulai sesi (input form) |
| POST | `/api/livechat/sessions/{id}/stop` | Stop manual |
| DELETE | `/api/livechat/sessions/{id}` | Hapus sesi (opsional, butuh konfirmasi UI) |
| GET | `/api/livechat/events` | SSE khusus livechat (per user) |
| GET | `/api/livechat/excel/download` | Unduh `livechat.xlsx` |
| GET | `/api/livechat/media/{userID}/{file}` | Lampiran sesi (media diterima dari bot) |
| GET | `/api/livechat/assets` | Daftar aset milik user |
| POST | `/api/livechat/assets` | Unggah aset (image/document, ≤16 MB) |
| DELETE | `/api/livechat/assets/{id}` | Hapus aset (ditolak bila masih dipakai sesi) |
| GET | `/api/livechat/assets/{id}/preview` | Pratinjau aset |

Semua endpoint memakai middleware sesi yang sudah ada; **hanya data
`livechat.*`** yang dibaca.

---

## 11. UI/UX Emulator

Tab baru **`Live Chat Emulator`** (aksen warna hijau WA, berbeda dari tab lain).

### 11.1 Panel kontrol
- Pilih device WA (hanya yang `connected`).
- Input **Judul Pengaduan**.
- Textarea **Isi Laporan/Pengaduan**.
- **Field pengaduan dengan awalan acak** (dapat diedit sebelum sesi dimulai):
  - `Nama Pengadu` — terisi nama acak, mis. "Budi Santoso"
  - `Tanggal Kejadian` — terisi `YYYY-MM-DD` acak 1–14 hari terakhir
  - `Alamat Kejadian` — terisi jalan/kecamatan Medan acak
  - Tombol kecil **"Acak ulang"** di tiap field.
  - `Titik Lokasi` — opsional (link maps/koordinat), default kosong.
- **Pemilih Berkas**: dropdown aset yang sudah diunggah; tombol
  **Unggah Berkas Baru** (image/pdf/docx ≤16 MB). Aset terpilih akan dipakai
  ketika bot meminta bukti/foto.
- Dropdown **Kategori Mode**: `Benar`, `Typo`, `Ngasal`, `Cari Celah`.
- Input **Batas Giliran** (default **40**, dapat diubah).
- Tombol **Mulai Sesi**.

### 11.2 Grid emulator
- Jumlah kartu = jumlah WA terhubung (maks 3), satu kartu per sesi aktif.
- Tampilan: header nomor (mis. `6281…`), bubble kiri/kanan, jam, indikator
  *typing* tiga titik, badge status sesi (running/finished/failed).
- **Bubble media**: gambar tampil sebagai thumbnail, dokumen sebagai kartu nama
  file, lokasi sebagai kartu peta mini.
- Tombol per kartu: **Stop**, **Lihat Log**.

### 11.3 Riwayat sesi
- Tabel ringkas di bawah grid (judul, tanggal, mode, giliran, status, tiket).
- Klik baris → panel detail (header + seluruh chat + log turnover).
- Sesi `failed`/`timeout` tetap tampil dengan badge merah + cuplikan chat terakhir.

### 11.4 Aksesibilitas & konsistensi
- Gunakan token warna/spacing yang sudah ada (`styles.css`).
- Kontras teks minimal 4.5:1; status tidak hanya dibedakan warna tetapi juga
  label teks.
- Responsif: grid 3 → 2 → 1 kolom.

---

## 12. Risiko & Mitigasi

| Risiko | Dampak | Mitigasi |
|---|---|---|
| Kolektor ganda di `wa.Client` | Balasan bot tertukar antara uji bank dan livechat | Registry `sync.Map` per sesi; runner tetap pakai `collector` lama; unit test khusus |
| Deteksi "selesai" meleset | Loop berlebihan | Regex multi-pola + batas `max_turns` (default 40, bisa diubah) |
| Bot menolak sesi panjang | Sesi gagal | Jeda 8–16 dtk + Stop manual + status `failed` tercatat |
| Schema tertukar | Menulis ke `public.*` | Semua query pakai prefix `livechat.` eksplisit; tidak memakai `search_path` |
| FK lintas schema | Migration gagal / coupling | `user_id` **tanpa FK fisik**; validasi di aplikasi |
| Excel korup saat crash | Data sesi hilang | Atomic write `.tmp` → `rename`; data sumber tetap di DB |
| Kebocoran chat pribadi | Pelanggaran privasi | Query selalu `WHERE session_id IN (milik user)` |
| Mode `ngasal` memancing bot error permanen | Bot target menolak | Tidak mengubah data tanggal/alamat; tetap dalam satu nomor penguji |
| Berkas aset besar/berbahaya | Disk penuh / kirim file berbahaya | Batas 16 MB, total 500 MB/user, allowlist mimetype, validasi sha256 sebelum kirim |
| Aset terkirim ke bot salah | Salah uji | Aset hanya dari milik user sendiri + ditautkan eksplisit ke sesi (`asset_ids`) |
| Berkas terhapus padahal dipakai | Sesi gagal kirim | Hapus aset ditolak bila `asset_ids` masih mereferensikannya |

---

## 13. Tahapan Implementasi (saat disetujui)

1. **Backup & baseline**
   - Backup `data/users/{id}/workbook.xlsx`, `workbook.metrics.json`, `.env`.
   - Pastikan `go test ./...` hijau sebelum mulai.
2. **Migration schema `livechat`** (DDL saja, dieksekusi **setelah persetujuan**)
3. **`internal/livechat/model.go` + `store.go`** — CRUD ke schema
4. **`assets.go`** — simpan aset content-addressed + validasi
5. **`internal/wa`** — registry kolektor + `SendMedia`/`SendLocation` (aditif, unit test)
6. **`detector.go` + `responder.go`** — unit test untuk semua `question_kind`
   dan semua `category_mode`
7. **`engine.go` + `manager.go`** — state machine + ritme manusia
8. **`internal/web/livechat.go`** — REST + SSE + media + assets
9. **`public/`** — tab, emulator, riwayat sesi, pemilih berkas, styling
10. **`excel.go`** — ringkasan per sesi + endpoint download
11. **Verifikasi**
    - `go build ./...`, `go vet ./...`, `go test -count=1 ./...`
    - `node --check public/livechat.js`
    - Uji manual: 1 sesi mode `benar` end-to-end dengan 1 WA nyata
    - Uji manual: 1 sesi dengan kirim berkas (image) ketika bot meminta bukti
    - Pastikan sheet/tabel lama **tidak berubah** (bandingkan checksum workbook)

---

## 14. Keputusan yang Sudah Dikunci

| No | Keputusan | Status |
|---|---|---|
| 1 | Nama schema: **`livechat`** | ✅ Dikunci |
| 2 | Batas pengaman giliran: **default 40**, dapat diubah per sesi/user (`max_turns`) | ✅ Dikunci |
| 3 | Field `nama_pengadu`, `tanggal_kejadian`, `alamat_kejadian`: **terisi acak saat form dibuka**, dapat diketik ulang/diganti; nilai final dipakai konsisten selama sesi | ✅ Dikunci |
| 4 | Pengiriman **image/document** dari aset milik sendiri (`data/users/{id}/livechat_assets/`), dikirim berulang tanpa upload ulang; juga mendukung `location` | ✅ Dikunci |
| 5 | Sesi `failed`/`timeout`/`stopped` **tetap ditulis ke Excel**, termasuk cuplikan chat terakhir (kolom L) | ✅ Dikunci |
| 6 | Laporan/pengaduan diinput **manual per sesi** | ✅ Dikunci |
| 7 | Emulator **murni otomatis** (tanpa intervensi manual di tengah sesi, hanya Stop) | ✅ Dikunci |
| 8 | Database: PostgreSQL **sama**, schema **terpisah** `livechat` | ✅ Dikunci |

### 14.1 Pertanyaan yang Masih Terbuka

1. **Retensi data**: sesi disimpan selamanya, atau ada batas (mis. 90 hari)?
2. **Batas total aset per user**: 500 MB (usulan) — atau angka lain?
3. **`titik_lokasi` default**: dikosongkan (usulan, karena opsional) atau ikut diacak?
