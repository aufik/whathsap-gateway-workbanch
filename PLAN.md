# Rencana Arsitektur & Implementasi (Golang): WhatsApp Bot Test Runner & Web Dashboard

Lokasi Kerja: `/var/www/kominfo/whatshap-gateway`  
Sumber Data Utama: `Uji-Bank-Pertanyaan-Programmer-4.xlsx`  
Target Runtime: **Go `1.23.6` (`linux/amd64`) — Single Binary Architecture**

---

## 1. Konteks & Karakteristik Data Uji

Berdasarkan bedah struktur `Uji-Bank-Pertanyaan-Programmer-4.xlsx`:
- **Volume & Kategori**: `210 baris` uji (baris Excel `3..212`) tersebar pada berbagai OPD dan 7 kategori (*Medan Terkini*, *Layanan Masyarakat*, *Aplikasi Simpang*, *Struktur Organisasi*, *Kependudukan*, *Kesehatan*, *Info Tentang BOT*).
- **Status Bank**:
  - `207 approved`: Aktif pada bot, harus diuji kesesuaian maknanya.
  - `3 waiting`: Belum memiliki jawaban di bank; sesuai petunjuk otomatis bernilai `Tidak`.
- **Karakteristik Balasan Bot WhatsApp**:
  - **Conversational / Free-Text AI**: Menerima kalimat tanya bebas tanpa menu angka kaku.
  - **Multi-Bubble & Lampiran Media**: `136 baris` menuntut `[Berkas]` (gambar/dokumen) dan `2 baris` menuntut `[Koordinat]` (`locationMessage` lat/lng). Balasan sering terbagi menjadi beberapa gelembung berurutan (`[1] ...`, `[2] ...`).
  - **Data Harga Harian Dinamis**: `7 baris` kategori *Aplikasi Simpang* menampilkan tanggal & harga komoditas terbaru dari `simpang.medan.go.id` yang pasti bergeser mengikuti hari pengujian.
  - **Anomali Encoding Sumber**: Beberapa sel pada kolom referensi mengandung karakter rusak (`` pengganti tanda hubung `–`) yang harus dinormalisasi saat evaluasi.
- **Integritas Rumus Excel**: Sheet `Ringkasan` menggunakan formula `COUNTIF` dan `COUNTIFS('Bank Pertanyaan'!J3:J400, "Cocok"/"Tidak")`. Pembaruan file wajib bersifat *in-place* tanpa merusak sheet `Petunjuk` maupun formula `Ringkasan`.

---

## 2. Pemilihan Teknologi (Golang Tech Stack)
  
Dirancang dengan prinsip *lean production engineering*: stabil, hemat memori (~25 MB RAM), bebas *race condition*, dan terkompilasi menjadi **1 file binary mandiri**.

| Lapisan | Teknologi / Paket | Peran & Alasan Arsitektural |
| :--- | :--- | :--- |
| **Bahasa & Runtime** | **Go `1.23.6`** | *Strongly-typed*, dukungan *routing* modern di `net/http` (`METHOD /path/{param}`), manajemen pembatalan proses presisi via `context.Context`, dan *single binary deployment*. |
| **Protokol WhatsApp** | **`go.mau.fi/whatsmeow`** | Standar industri klien WhatsApp Web Multi-Device di Go berbasis Protobuf. Jauh lebih stabil dari sisi manajemen sesi kriptografi dibanding pustaka JS, mendukung QR Code & Pairing Code 8-digit. |
| **Penyimpanan Sesi WA** | **`github.com/mattn/go-sqlite3`** | Menyimpan kredensial sesi `whatsmeow` ke dalam satu file lokal `wa_session.db` dengan mode `WAL` (`gcc` sudah tersedia di server). |
| **Engine Excel** | **`github.com/xuri/excelize/v2`** | Pustaka `.xlsx` kelas produksi untuk membaca dan memodifikasi sel secara *in-place* sambil menjaga struktur XML, styling, serta rumus `COUNTIFS` pada sheet `Ringkasan`. |
| **QR Renderer** | **`github.com/skip2/go-qrcode`** | Mengubah string QR login `whatsmeow` menjadi PNG Base64 secara langsung di memori untuk ditampilkan di Web Dashboard. |
| **AI / LLM Client** | **Go `net/http` (OpenAI-Compatible)** | Tanpa SDK berat; memanggil endpoint `/v1/chat/completions` (`9router`, OpenAI, Gemini, Ollama) untuk generator kalimat uji & *semantic judge*, lengkap dengan *deterministic fallback*. |
| **Web Server & Live Sync** | **`net/http` + SSE + `embed`** | Port `3020`. Menggunakan *Server-Sent Events* (SSE) untuk pembaruan status *real-time* ke browser dan `//go:embed public/*` agar aset web menyatu di dalam binary. |
| **Frontend Dashboard** | **HTML5 + CSS3 + Vanilla ES Modules** | Tanpa tahapan *build* frontend (tanpa Node/Vite/React), cepat dimuat, fokus pada kepadatan informasi tabel kerja & kontrol QA. |
| **Automated Verification** | **Go `testing`** | Unit & integration test bawaan (`go test ./...`) untuk memverifikasi penulisan atomik Excel, keutuhan rumus `Ringkasan`, serta akurasi evaluator. |

### Rincian Biaya & Lisensi (100% Gratis / Zero-Cost)
- **Bahasa, Server, & Seluruh Pustaka (`Go`, `whatsmeow`, `excelize`, `go-sqlite3`, `go-qrcode`)**: **100% Gratis & Open-Source** (tanpa sewa layanan WA Gateway berbayar seperti Fonnte/Wablas/Twilio).
- **Koneksi WhatsApp**: **Gratis**, cukup menggunakan aplikasi WhatsApp biasa pada nomor penguji/sekunder.
- **Engine Variasi Kalimat & Evaluasi AI**: **100% Gratis** melalui dua opsi:
  1. **Tanpa API Eksternal Sama Sekali (Built-in Smart Local Engine)**: Tetap berjalan gratis secara lokal di Go untuk menghasilkan variasi bahasa warga, singkatan percakapan, *typo* realistis QWERTY, dan pencocokan jawaban.
  2. **API AI Gratis**: Mendukung koneksi ke **`9router` internal**, **Google Gemini API (Free Tier)**, **Groq (Free Tier)**, atau **Ollama lokal** tanpa biaya langganan.

---

## 3. Struktur Direktori & Paket

```text
/var/www/kominfo/whatshap-gateway/
├── PLAN.md                                         # Dokumen rencana arsitektur ini
├── Uji-Bank-Pertanyaan-Programmer-4.xlsx           # File Excel kerja aktif (sinkron otomatis)
├── Uji-Bank-Pertanyaan-Programmer-4.backup.xlsx    # Backup murni otomatis saat start pertama
├── .env.example                                    # Template konfigurasi
├── .env                                            # Konfigurasi aktif lokal (diabaikan git)
├── go.mod                                          # Definisi modul & 4 dependensi inti
├── go.sum                                          # Checksum kriptografis dependensi
├── main.go                                         # Entry point, embed FS, wiring, graceful shutdown
├── wa_session.db                                   # Database sesi login WhatsApp (otomatis dibuat)
├── internal/
│   ├── config/
│   │   └── config.go                               # Pembaca .env ringan, validasi nilai default & durasi
│   ├── model/
│   │   └── types.go                                # Kontrak struct domain (TestRow, OPDSummary, RunnerStatus, Event)
│   ├── excel/
│   │   ├── store.go                                # In-memory cache + mutex + atomic temp-file Excelize writer
│   │   └── store_test.go                           # Pengujian baca/tulis in-place & keutuhan formula Ringkasan
│   ├── ai/
│   │   ├── engine.go                               # Generator kalimat (exact/natural/chaos) & Semantic Evaluator
│   │   └── engine_test.go                          # Pengujian normalisasi encoding, waiting, [Berkas], Simpang, dll.
│   ├── wa/
│   │   └── client.go                               # Wrapper whatsmeow, JID/LID filter, typing, quiet-window collector
│   ├── runner/
│   │   └── runner.go                               # Worker sekuensial tunggal (Start, Pause, Resume, Stop, SingleRow)
│   └── web/
│       ├── server.go                               # Routing REST API, upload/download .xlsx, static embed handler
│       └── sse.go                                  # Broker Server-Sent Events (fan-out ke tab browser aktif)
└── public/
    ├── index.html                                  # Antarmuka Dashboard Ringkasan, Panel WA, & Tabel Uji
    ├── styles.css                                  # Tata letak tabel padat, badge status, modal QR, responsif
    └── app.js                                      # State manajemen frontend, filter multi-kriteria, SSE listener
```

---

## 4. Desain Teknis Mendalam per Komponen

### 4.1. Penyimpanan Excel Anti-Korup (`internal/excel/store.go`)
- **Initial Safety Backup**: Saat aplikasi dijalankan pertama kali, jika `*.backup.xlsx` belum ada, salin byte-per-byte dari file asli sebagai cadangan permanen.
- **Dual-Layer Access**:
  - **Read Path (`sync.RWMutex`)**: Seluruh 210 baris (`No`, `OPD`, `Kategori`, `StatusBank`, `QuestionRef`, `QuestionInput`, `ExpectedReply`, `ActualReply`, `Notes`, `ResultStatus`) dan kalkulasi statistik per OPD disajikan langsung dari memori (<1 ms).
  - **Write Path (Atomic Save)**: Setiap kali satu baris selesai diuji atau dikoreksi manual lewat Web:
    1. Buka workbook dengan `excelize.OpenFile`, perbarui hanya sel `F{r}`, `H{r}`, `I{r}`, `J{r}` pada sheet `Bank Pertanyaan`.
    2. Tulis ke file sementara `Uji-Bank-Pertanyaan-Programmer-4.xlsx.tmp` pada direktori yang sama, pastikan tersinkron ke disk, lalu lakukan `os.Rename` secara atomik ke file utama. Cara ini mencegah file `.xlsx` rusak walau proses berhenti mendadak di tengah penulisan.
    3. Sheet `Petunjuk` dan rumus `COUNTIF`/`COUNTIFS` di sheet `Ringkasan` tidak disentuh sehingga Excel tetap menghitung otomatis saat dibuka.

### 4.2. Koneksi WhatsApp, Protokol Anti-Deteksi Bot, & Kolektor Balasan (`internal/wa/client.go`)
- **Manajemen Sesi & Identitas Perangkat Wajar**:
  - Menggunakan `sqlstore` SQLite (`file:wa_session.db?_foreign_keys=on&_journal_mode=WAL`) dengan dukungan QR Code maupun **Pairing Code 8-digit**.
  - Mengatur `DeviceProps` pendamping menyerupai browser desktop standar (`Chrome (Linux)`), bukan identitas skrip mencurigakan.
- **Filter Pesan Akurat (JID & LID Safe)**:
  - WhatsApp modern menggunakan `@s.whatsapp.net` maupun *Linked Identity* (`@lid`) pada akun bisnis/bot. Klien mencocokkan baik `Info.Chat` maupun `Info.Sender` / `SenderAlt` terhadap target `BOT_PHONE`.
  - Mengabaikan pesan `IsFromMe == true`, mengabaikan event sinkronisasi riwayat lama (`*events.HistorySync`), dan memvalidasi stempel waktu agar hanya pesan yang tiba setelah pertanyaan dikirim yang ditangkap.
- **6 Lapis Simulasi Perilaku Manusia (*Anti-Ban & Anti-Bot Cadence*)**:
  *(Catatan: Kasus pengujian ini jauh lebih aman dibanding WA Blast karena hanya mengobrol 2-arah dengan 1 nomor Bot yang selalu membalas 100%, bukan spam ke banyak nomor asing. Namun, irama robotik tetap wajib dihilangkan melalui 6 mekanisme berikut)*:
  1. **Siklus Kehadiran (*Presence Lifecycle*)**: Mengirim `PresenceAvailable` saat sesi aktif, `ChatPresenceComposing` saat mengetik, `ChatPresencePaused` bila kalimat panjang mengalami jeda pikir singkat, dan `PresenceUnavailable` saat sesi beristirahat/selesai.
  2. **Centang Biru & Waktu Baca (*MarkRead + Reading Delay*)**: Saat balasan Bot masuk, sistem tidak langsung mengetik soal baru, melainkan mengirim tanda baca (`MarkRead`) dan menunggu **waktu baca proporsional (`2.5 – 6 detik`)** sesuai panjang balasan Bot.
  3. **Kecepatan Ketik Proporsional (*Dynamic WPM Typing*)**: Durasi `ChatPresenceComposing` dihitung dari panjang karakter kalimat (`~45 – 80 ms/karakter` + variasi acak, dibatasi `2 – 9 detik`), bukan angka konstan yang mudah terdeteksi sebagai bot.
  4. **Jeda Antar Soal Non-Seragam (*Randomized Jitter*)**: Jeda acak tidak berpola tetap (`8 – 16 detik` antar giliran) setelah proses baca selesai.
  5. **Istirahat Berkala (*Periodic Human Micro-Break / Cooldown*)**: Setiap `15 – 25 pertanyaan` (dipilih acak), runner otomatis beristirahat **`45 – 90 detik`** (mensimulasikan penguji sedang membaca tabel Excel) agar grafik trafik per jam terlihat alami seperti manusia.
  6. **Circuit Breaker Otomatis (*Zero Blind-Retry*)**: Apabila server WhatsApp mengembalikan error pengiriman, indikasi *rate-limit*, atau terputus tidak wajar, runner **langsung otomatis masuk status `Paused`** dan menampilkan peringatan di Web Dashboard (tidak pernah mengulang kiriman secara agresif yang dapat memicu pemblokiran akun).
- **Quiet-Window Multi-Bubble Collector**:
  - Setelah pesan terkirim, kolektor membuka jendela pantau terhadap pesan masuk dari Bot.
  - Setiap gelembung pesan baru yang tiba akan me-reset *quiet timer* (`5 detik` hening), dibatasi *hard timeout* total (`30 detik`).
  - Ekstraksi payload komprehensif:
    - `Conversation` / `ExtendedTextMessage.Text`
    - `ImageMessage` / `DocumentMessage` / `VideoMessage`: menangkap `Caption` sekaligus mencatat indikator `[Berkas] <mimetype / filename>` (tanpa perlu mengunduh file gambar besar ke disk agar hemat storage & bandwidth).
    - `LocationMessage` / `LiveLocationMessage`: mencatat `[Koordinat] <DegreesLatitude>, <DegreesLongitude>`.
    - Pesan interaktif (`ButtonsMessage` / `ListMessage` / `TemplateMessage`) jika bot membalas dengan komponen tombol.
  - Jika terkumpul >1 gelembung pesan, dirangkai otomatis dengan penomoran `[1] ...\n\n[2] ...` sesuai konvensi pada Excel sumber.

### 4.3. Engine AI & Smart Local Heuristic (`internal/ai/engine.go`)
- **Mode Generator Pertanyaan (`Pertanyaan Diinput`) — Mendukung AI Gratis & Mode Lokal 0 Biaya**:
  1. `exact` (*Sesuai Data*): Mengirim teks asli kolom E tanpa ubahan (uji regresi dasar).
  2. `natural` (*Bahasa Warga Wajar*): Mengubah kalimat/judul dataset menjadi pertanyaan percakapan wajar sehari-hari sambil menjaga utuh entitas kunci (angka tahun, nama kecamatan/kelurahan, singkatan teknis seperti `PKL`, `KK`, `SPBE`, `BKAD`, nama bahan pokok).
  3. `chaos` (*User Error / Kalimat Aneh tapi Logis*): Dirancang khusus untuk menguji ketangguhan logika bot terhadap perilaku pengguna nyata—mencakup *typo* ketikan jari HP (huruf QWERTY berdekatan pada kata umum), singkatan percakapan lokal (`yg`, `thn`, `dmn`, `brp`, `utk`, `gimana`), struktur kalimat terbalik atau agak berbelit, namun maksud intinya tetap logis dan tidak merusak angka tahun/nama wilayah.
  4. `manual` (*Input Kustom Penguji*): Memakai teks yang diketik/diedit langsung oleh penguji di Web UI untuk skenario *edge-case* spesifik di masa depan.
  - *Catatan Mode Tanpa API Key*: Jika `LLM_API_KEY` dikosongkan, generator lokal cerdas di Go tetap mampu memproduksi variasi `natural` (pembungkus kalimat tanya warga natural berdasarkan tipe kalimat/judul dataset) maupun `chaos` (injeksi singkatan percakapan & *typo* terukur yang mengunci entitas angka/nama tempat) secara gratis 100% offline.
- **Aturan Evaluasi Berlapis (`Status Uji` & `Keterangan`)**:
  1. **Aturan `waiting`**: Jika `Status Bank == "waiting"`, otomatis menetapkan `Status Uji = "Tidak"` dan `Keterangan = "Status bank waiting (belum ada jawaban di bank)"`.
  2. **Validasi Kontrak Media & Lokasi**:
     - Jika referensi memuat `[Berkas]`, periksa apakah balasan sistem menyertakan media `[Berkas]`.
     - Jika referensi memuat `[Koordinat]`, periksa apakah balasan sistem mengirim titik lokasi `[Koordinat]` dengan koordinat yang berdekatan.
     - Jika teks cocok tetapi media/lokasi absen, catat secara eksplisit di `Keterangan` (misal: *"Teks sesuai, namun bot tidak mengirim lampiran [Berkas]"*).
  3. **Penanganan Data Dinamis (`Aplikasi Simpang`)**: Memvalidasi nama komoditas, struktur harga (*Terendah / Tertinggi / Rata-rata*), dan rujukan `simpang.medan.go.id` tanpa menyalahkan perbedaan tanggal hari uji maupun perubahan nominal harga harian.
  4. **Semantic Judge (LLM + Fallback Deterministik)**:
     - Menormalisasi karakter rusak (``) dan format tebal WhatsApp (`*...*`).
     - Meminta penilaian terstruktur JSON (`{"match": true|false, "reason": "..."}`) ke endpoint OpenAI-compatible untuk membandingkan kesamaan makna jawaban.
     - Jika konfigurasi API AI kosong atau sedang gangguan jaringan, otomatis beralih ke *fallback* lokal berbasis pencocokan URL dataset (`satudata.medan.go.id/...`), angka/fakta kunci, dan kemiripan token (*Jaccard/dice similarity*) agar pengujian tidak pernah terhenti.

### 4.4. Orkestrator Antrean (`internal/runner/runner.go`)
- **Strict Single-Concurrency State Machine**: Status eksekusi (`idle`, `running`, `paused`, `stopping`) dijaga ketat oleh satu worker goroutine. Pengiriman paralel dilarang demi mencegah tertukarnya balasan chat WhatsApp dan menghindari pemblokiran nomor oleh Meta.
- **Fitur Eksekusi**:
  - **Auto-Resume Batch**: Menjalankan secara berurutan hanya baris yang status ujinya masih kosong (atau menjalankan rentang nomor tertentu).
  - **Pause / Resume / Stop Instan**: Menggunakan `context.Context` sehingga saat tombol *Pause* atau *Stop* ditekan saat masa jeda tunggu, runner berhenti seketika tanpa merusak data baris aktif.
  - **Single-Row Test & Re-test**: Menguji 1 baris spesifik dari tabel Web kapan saja saat batch sedang `idle`/`paused`.

### 4.5. REST API, SSE, & Web Dashboard (`internal/web/` & `public/`)
- **Daftar Endpoint Server (`http://127.0.0.1:3020`)**:
  - `GET /api/state` — Snapshot lengkap konfigurasi aman, status WA, status runner, statistik ringkasan OPD, dan 210 baris data.
  - `GET /api/events` — Stream SSE *real-time* (`wa_status`, `qr_code`, `runner_progress`, `human_behavior`, `row_updated`, `summary_updated`).
  - `POST /api/settings` — Update runtime untuk `BOT_PHONE`, mode kalimat (`exact`/`natural`/`chaos`), serta seluruh parameter perilaku manusia (waktu baca, kecepatan ketik, jeda antar soal, dan *micro-break*).
  - `POST /api/wa/connect`, `POST /api/wa/pair`, `POST /api/wa/logout` — Manajemen sesi WhatsApp penguji.
  - `POST /api/runner/start`, `POST /api/runner/pause`, `POST /api/runner/resume`, `POST /api/runner/stop` — Kontrol batch.
  - `POST /api/rows/{no}/run` — Jalankan pengujian ke WA khusus baris `{no}`.
  - `POST /api/rows/{no}/generate` — Buat/preview kalimat variasi AI (`natural` atau `chaos`) pada baris `{no}` sebelum dikirim.
  - `PATCH /api/rows/{no}` — Edit manual `Pertanyaan Diinput`, override manual `Cocok`/`Tidak`, atau ubah `Keterangan` (langsung tersimpan ke Excel).
  - `GET /api/excel/download` — Unduh file `.xlsx` terbaru yang siap dikumpulkan.
  - `POST /api/excel/upload` — Unggah file `.xlsx` lain (misal berkas Programmer 1–8 lainnya) dengan validasi struktur kolom & auto-backup.
- **Fitur Antarmuka Web (`public/`)**:
  - **Top Control Bar**: Indikator koneksi WA penguji (tombol Scan QR / Pairing), input Nomor WA Bot, pemilih Mode Kalimat, tombol Batch (*Start / Pause / Stop*), progres bar aktif, serta tombol **Download Excel**.
  - **Live Monitor & Panel Perilaku Manusia (*Anti-Ban Telemetry & Config*)**:
    - **Status Aktivitas Real-Time + Countdown**: Menampilkan fase perilaku manusia yang sedang berlangsung detik demi detik (misal: *Mengetik 74 huruf (4.1 dtk)...*, *Menunggu gelembung balasan Bot (Quiet-Window 5 dtk)...*, *Centang Biru terkirim & Membaca balasan (3.9 dtk)...*, *Jeda santai antar soal (11.2 dtk)...*, hingga *Istirahat Micro-Break (58 dtk tersisa)*).
    - **Metrik Keamanan Ritme**: Penghitung jarak menuju istirahat berikutnya (misal `14 / 19 soal sebelum micro-break`), status kehadiran WA (`Available` / `Composing` / `Paused` / `Unavailable`), status *Circuit Breaker*, dan riwayat log jejak perilaku (*activity feed*).
    - **Pengaturan Perilaku Manusia Langsung dari Web**: Pilihan preset cepat (*Aman Standar*, *Ekstra Hati-hati*, atau *Kustom*) untuk mengubah durasi baca (`MarkRead`), kecepatan ketik (`ms/karakter`), jeda antar soal, serta interval & durasi *micro-break* tanpa perlu restart server.
  - **Tab 1 — Dashboard & Ringkasan OPD**: Kartu KPI utama (*Total Baris*, *Cocok*, *Tidak*, *Belum Diuji*, *Tingkat Kecocokan %*) dan tabel rekapitulasi seluruh OPD sesuai sheet `Ringkasan` (klik nama OPD langsung memfilter tabel pertanyaan).
  - **Tab 2 — Bank Pertanyaan & Hasil Uji (Lengkap dengan Kolom Ritme Manusia per Baris)**:
    - Filter kombinasi: Pencarian kata kunci, dropdown OPD, Kategori, Status Bank (`approved`/`waiting`), dan Status Uji (`Semua`/`Belum Diuji`/`Cocok`/`Tidak`).
    - Kolom komparasi berdampingan (*Pertanyaan Data vs Diinput* serta *Balasan Data vs Balasan Sistem*), ditambah **Kolom Rekam Ritme Manusia Aktual** (`Pre-Think`, `Lama Typing + Gelombang`, `Waktu Tunggu Bot`, `Jeda Sebelum Read`, `Lama Baca + Centang Biru`, `Jeda Antrean`).
    - **Modal Peluncuran Batch + Kalkulator Estimasi Waktu Real-Time**: Saat tombol *Mulai / Lanjut Uji* diklik, penguji memasukkan jumlah soal (dengan rekomendasi aman 30 soal/gelombang atau seluruh soal Excel) dan melihat estimasi **Waktu Paling Cepat** serta **Waktu Paling Lama** selesai secara *live*.
  - **Tab 3 — Sheet Terpisah `Uji Diluar Bank` (Tahun 2026 / Pertanyaan Aneh & Anti-Halusinasi)**:
    - Tersimpan pada worksheet tersendiri bernama **`Uji Diluar Bank`** di dalam file `.xlsx` yang sama tanpa mencampuri sheet `Bank Pertanyaan`, `Petunjuk`, maupun `Ringkasan`.
    - Fitur generator otomatis dari soal bank (misal memutasi *Tahun 2022* menjadi *Tahun 2026/2027* atau objek di luar dataset) + tambah soal manual + evaluator khusus anti-halusinasi.

---

## 5. Spesifikasi Konfigurasi `.env`

```env
HOST=127.0.0.1
PORT=3020
EXCEL_FILE=Uji-Bank-Pertanyaan-Programmer-4.xlsx
WA_SESSION_DB=wa_session.db

# Nomor WhatsApp Bot Target (contoh: 6281234567890)
BOT_PHONE=

# Parameter Simulasi Manusia (Anti-Ban) & Kolektor Balasan (milidetik)
READ_DELAY_MIN_MS=2500
READ_DELAY_MAX_MS=6000
TYPING_MS_PER_CHAR=55
TYPING_MIN_MS=2000
TYPING_MAX_MS=9000
DELAY_MIN_MS=8000
DELAY_MAX_MS=16000
COOLDOWN_EVERY_MIN=15
COOLDOWN_EVERY_MAX=25
COOLDOWN_MIN_MS=45000
COOLDOWN_MAX_MS=90000
QUIET_WINDOW_MS=5000
REPLY_TIMEOUT_MS=30000

# Konfigurasi AI OpenAI-Compatible (Opsional & Gratis: 9router / Gemini Free / Groq Free / Ollama)
# Jika dikosongkan, sistem otomatis memakai Smart Local Engine bawaan Go (100% gratis & offline)
LLM_BASE_URL=
LLM_API_KEY=
LLM_MODEL=
LLM_TIMEOUT_MS=20000
```

---

## 6. Mitigasi Risiko & SOP Anti-Ban Nomor Penguji

1. **SOP Pemanasan Awal (*Warm-up*) Nomor Penguji**:
   - Gunakan nomor WhatsApp sekunder/khusus QA (hindari nomor pribadi utama maupun nomor yang baru didaftarkan kurang dari 24 jam).
   - Simpan nomor Bot target ke daftar kontak HP penguji dan kirimkan 1 pesan sapaan manual dari HP terlebih dahulu sebelum menjalankan otomatisasi 210 baris, sehingga WhatsApp menandai relasi keduanya sebagai percakapan dua arah yang sah.
2. **Keamanan Irama Chat**: Kombinasi `MarkRead` (centang biru), waktu baca, kecepatan ketik proporsional panjang teks, jeda acak antar soal, *micro-break* tiap 15–25 soal, serta *Circuit Breaker* otomatis meminimalkan pola mekanis yang biasa memicu deteksi spam Meta.
3. **Keamanan Jaringan Lokal**: Secara default server mengikat `HOST=127.0.0.1` pada port `3020` agar sesi WhatsApp dan dokumen uji tidak terbuka ke jaringan publik tanpa pelindung.
4. **Perlindungan Data Uji**: Penulisan atomik via `.tmp` + `os.Rename` dan pembuatan `.backup.xlsx` memastikan tidak ada risiko kehilangan hasil pengujian maupun rusaknya rumus Excel asli.
5. **Penutupan Bersih (*Graceful Shutdown*)**: Menangkap sinyal `SIGINT`/`SIGTERM` untuk menghentikan runner dengan aman, memutus soket `whatsmeow`, dan menutup koneksi SQLite sebelum proses keluar.

---

## 7. Urutan Tahap Implementasi (Saat Dimulai)

1. Inisialisasi `go.mod`, `.env.example`, `.gitignore` lokal, dan penarikan dependensi (`whatsmeow`, `go-sqlite3`, `excelize/v2`, `go-qrcode`).
2. Pembangunan `internal/config`, `internal/model`, dan `internal/excel` lengkap dengan pengujian `go test ./internal/excel/...` terhadap salinan uji untuk membuktikan formula `Ringkasan` tetap utuh.
3. Pembangunan `internal/ai` (generator 4 mode termasuk *User Error / Kalimat Aneh Logis* + semantic evaluator + fallback) beserta unit test `go test ./internal/ai/...`.
4. Pembangunan `internal/wa` (`whatsmeow` + *quiet-window collector*) dan `internal/runner` (state machine antrean sekuensial).
5. Pembangunan `internal/web` (REST API + SSE), antarmuka `public/` (Dashboard + Tabel Interaktif), dan `main.go` (`//go:embed`).
6. Kompilasi binary (`go build -o wa-bot-tester .`), eksekusi seluruh `go test ./...`, dan verifikasi *smoke test* endpoint HTTP di port `3020`.
