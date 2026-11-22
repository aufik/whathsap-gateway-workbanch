# RENCANA: Live Chat Dinamis — Form Fleksibel, Respons Bervariasi, Anti-Spam Terpusat

Dokumen ini adalah **rencana**, belum ada implementasi.
Target kerja: `/var/www/kominfo/whatshap-gateway`

Status: **BELUM DIKERJAKAN** — menunggu persetujuan (dimulai dari DDL).

---

## 0. Ringkasan Masalah & Perbaikan

| # | Masalah yang dilaporkan user | Perbaikan |
|---|---|---|
| 1 | Form "Panel Kontrol Sesi Baru" selalu menolak submit karena field wajib | Hanya **Device WA** yang wajib. Semua field lain kosong = **diisi acak otomatis oleh server**. |
| 2 | Respons ke bot selalu sama | **3 lapis**: varian frasa (A) + konteks balasan bot (B) + AI bila LLM aktif (C), dengan seed tersimpan. |
| 3 | Anti-spam WA belum lengkap di Live Chat | Samakan dengan Bank Pertanyaan: presence, unread delay, `MarkRead`, read duration, pre-typing, multi-burst composing. |
| 4 | Nomor bot target tidak jelas | `bot_phone` = **nomor tujuan (bot)**, bukan nomor kita. Nilai menang: **pengaturan per user** → fallback `.env`. |
| 5 | Tidak ada templat laporan | **Tidak ada**. `report_text` boleh **NULL**; pada mode `benar` jawaban diambil dari konteks bot (opsi A). |

### 0.1 Keputusan yang sudah dikunci user

| No | Keputusan |
|---|---|
| 1 | Wajib: **hanya Device WA** (select box). Bot phone otomatis dari pengaturan user. |
| 2 | `report_text` **boleh NULL**. |
| 3 | Mode `benar` + laporan kosong → **opsi A**: jawab dari konteks balasan bot. |
| 4 | `bot_phone` yang menang = **bot target milik user kita** (`user_settings.bot_phone`), fallback `.env BOT_PHONE`. |
| 5 | Lapis C (AI) **aktif hanya bila LLM di `.env` terisi & valid**; jika tidak, Lapis A+B. |
| 6 | **Seed acak disimpan ke DB** agar variasi dapat direproduksi (audit). |
| 7 | **Anti-spam tetap memakai tab "Pengaturan Perilaku Manusia & SOP Anti-Ban"** — tidak ada angka yang di-hardcode. |

---

## 1. Anti-Spam Terpusat di Tab Pengaturan (non-negotiable)

Seluruh parameter sudah ada di `model.HumanSettings` (`user_settings`) dan
form UI tab *Pengaturan Perilaku Manusia & SOP Anti-Ban*. **Plan ini tidak
menambah parameter baru dan tidak meng-hardcode angka.**

Event `internal/wa` → Live Chat:

| Tahap | Parameter (kolom `HumanSettings`) | Sumber |
|---|---|---|
| 1. Presence online | — (selalu `available` selama sesi) | tetap |
| 2. Pre-think sebelum mengetik | `PreThinkMinMs`..`PreThinkMaxMs` | tab |
| 3. Durasi mengetik | `TypingMsPerChar`, `TypingMinMs`, `TypingMaxMs` | tab |
| 4. Multi-burst composing | turunan `estimateBurstCount` | tab (ms) |
| 5. Jeda centang abu-abu | `UnreadDelayMinMs`..`UnreadDelayMaxMs` | tab |
| 6. **Centang biru (`MarkRead`)** | — (dikirim setelah jeda unread) | tetap |
| 7. Jeda membaca | `ReadDelayMinMs`..`ReadDelayMaxMs` | tab |
| 8. Jeda antar giliran | `DelayMinMs`..`DelayMaxMs` | tab |
| 9. Distraction spike | `DistractionSpikePct`, `DistractionMinMs`..`MaxMs` | tab |
| 10. Micro-break | `CooldownEveryMin`..`Max`, `CooldownMinMs`..`MaxMs` | tab |
| 11. Long rest | `LongRestEveryMin`..`Max`, `LongRestMinMs`..`MaxMs` | tab |
| 12. Quiet window & timeout | `QuietWindowMs`, `ReplyTimeoutMs` | tab |

> Nilai dibaca **saat sesi dimulai** dari `userSettings(ctx, userID)`
> (`Manager.SettingsProvider`, sudah ada di `bootstrap.go`). Perubahan tab
> berlaku untuk **sesi berikutnya** (tidak mengubah sesi yang sedang jalan)
> — konsisten dengan perilaku runner.

---

## 2. Perubahan Database (DDL) — perlu persetujuan

```sql
BEGIN;

-- 2.1 Seed acak per sesi (untuk reproduksi varian & audit)
ALTER TABLE livechat.sessions
    ADD COLUMN IF NOT EXISTS rng_seed BIGINT NOT NULL DEFAULT 0;

-- 2.2 Laporan boleh kosong
ALTER TABLE livechat.sessions
    ALTER COLUMN report_text DROP NOT NULL;

COMMIT;
```

- Aditif & idempoten; tidak menyentuh `public.*` maupun `workbench.*`.
- Sesi lama otomatis `rng_seed = 0` (artinya "tidak diketahui", tetap valid).
- Rollback: `ALTER TABLE ... DROP COLUMN rng_seed;` dan `SET NOT NULL`
  (hanya bila tidak ada baris NULL).

---

## 3. Form Fleksibel (Server-side, bukan hanya UI)

### 3.1 Urutan penentuan `bot_phone`
```
1. req.BotPhone (bila penguji mengetik manual)
2. user_settings.bot_phone          <- PEMENANG default (bot target user)
3. .env BOT_PHONE                   <- fallback global
4. (kosong) -> 400 "nomor bot target belum diatur"
```
Implementasi di `handleStartSession`, sebelum memanggil `Manager.StartSession`.

### 3.2 Auto-random saat kosong
Satu RNG per sesi: `seed := time.Now().UnixNano()`, `rng := rand.New(rand.NewSource(seed))`.
Preview dari fungsi yang sudah ada: `livechat.GenerateRandomPreview(rng, now)`.

| Field | Bila kosong |
|---|---|
| `device_uuid` | **tetap wajib** → 400 |
| `bot_phone` | urutan §3.1 |
| `title` | `"Live Chat " + now.Format("02 Jan 15:04")` |
| `report_text` | **NULL** (bukan templat) |
| `nama_pengadu` | `preview.NamaPengadu` |
| `tanggal_kejadian` | `preview.TanggalKejadian` (1–14 hari lalu) |
| `alamat_kejadian` | `preview.AlamatKejadian` |
| `titik_lokasi` | `preview.TitikLokasi` + source `random` |
| `max_turns` | `40` (dibatasi 1..200) |
| `category_mode` | `benar` |

`rng_seed` dikirim ke `SessionInput` dan dipakai engine.

### 3.3 UI (`public/livechat.js`, `public/index.html`)
- Hapus `required` pada title/report/nama/tanggal/alamat.
- Hapus 5 guard `toastLC("... wajib diisi")`; sisakan guard device.
- Prefill `lcBotPhone` dari `/api/state` → `settings.botPhone`.
- Label diberi hint: *"opsional — kosongkan untuk acak otomatis"*.
- Field `lcBotPhone` diberi label tegas **"Nomor Bot Target (tujuan)"**.

---

## 4. Respons Tidak Selalu Sama

### 4.1 Lapis A — Varian frasa (selalu aktif)
Setiap `QuestionKind` mendapat 4–6 formulasi, dipilih `in.Random`.

| Kind | Contoh varian (nilai faktual tetap utuh) |
|---|---|
| Konfirmasi | "iya pak, sudah benar" / "betul min, silakan" / "ya sudah sesuai" / "benar pak, diproses ya" |
| Lainnya | "lanjut pak" / "siap, silakan" / "oke min, terus?" / "mohon dilanjutkan" |
| Nama | `<nama>` / "nama saya `<nama>` pak" / "`<nama>` min" |
| Tanggal | `<YYYY-MM-DD>` / "kejadiannya `<YYYY-MM-DD>` pak" |
| Alamat | `<alamat>` / "lokasinya di `<alamat>`" |
| Titik lokasi | (tetap `LocationMessage` bila parse; teks bila gagal) |
| Berkas | (caption varian: "ini fotonya pak" / "lampirannya min" / "bukti fotonya") |

**Aturan keras:** angka, tanggal, alamat, dan isi laporan **tidak boleh**
diubah oleh varian. `typo`/`ngasal`/`celah` tetap sesuai §4.3.

### 4.2 Lapis B — Konteks balasan bot
- `AnswerInput` mendapat `BotContext string` (gabungan bubble satu batch).
- Engine meneruskan seluruh teks batch, bukan hanya bubble terakhir.
- Responder memakai `BotContext` untuk:
  1. **Deteksi pengulangan**: bila bot mengulang pertanyaan yang sama
     (kind sama 2× berturut), pakai varian penegasan berbeda
     (mis. "nama saya sudah saya infokan tadi pak: `<nama>`").
  2. **Pemilihan varian relevan**: bila bot menyebut kata kunci
     (mis. "foto", "alamat (depan pasar)"), pilih varian yang menyebut hal itu.

### 4.3 Pengaruh `category_mode` ke `isi_laporan` (keputusan user)
| Mode | Perilaku `isi_laporan` |
|---|---|
| `benar` + laporan **terisi** | kirim `report_text` apa adanya (boleh dibungkus varian) |
| `benar` + laporan **NULL** | **opsi A**: jawab dari `BotContext` ("mengenai hal itu, mohon ditindaklanjuti pak") |
| `typo` | varian typo/singkatan dari teks tersedia |
| `ngasal` | jawaban di luar konteks (tidak mengarang fakta) |
| `celah` | uji batas (laporan kosong/terlalu pendek) |

### 4.4 Lapis C — AI (opsional)
| Aspek | Rencana |
|---|---|
| Aktivasi | `cfg.AIStatus().Enabled` (sudah ada; cek `LLM_BASE_URL` + `LLM_MODEL`) |
| API | Tambah **1 method publik aditif** di `internal/ai`: `ChatReply(ctx, system, user string, temp float64) (string, error)` — wrapper tipis `chatCompletion` |
| Cakupan | **hanya mode `benar`**, hanya jawaban teks (bukan media/lokasi) |
| Prompt | "Kamu warga Kota Medan. Jawab 1 kalimat singkat gaya chat. Fakta: `<fakta>`. Pertanyaan bot: `<bot>`." |
| Fallback | error/timeout/kosong → otomatis Lapis A+B; sesi **tidak pernah** gagal karena AI |
| Batas | `sanitizeAIText` (1 baris, maks 900 char) |

### 4.5 Seed tersimpan
- `sessions.rng_seed` dipakai `rand.NewSource(seed)` **satu kali per sesi**
  di `Engine`, bukan dibuat ulang tiap giliran.
- Reproduksi: seed sama + data sesi sama → urutan varian sama.

---

## 5. Anti-Spam WA (menyamakan Bank Pertanyaan)

### 5.1 Penambahan ke `internal/wa` (aditif, +unit test)
| Fungsi baru | Isi |
|---|---|
| `SendAvailablePresence(ctx, available bool)` | membungkus `SendPresence` (sudah ada sebagai `SetPresenceAvailable`) |
| `SendChatPresenceTyping(ctx, phone, state string)` | `SendChatPresence(composing/paused)` ke JID bot |
| `MarkReadBatch(ctx, phone string, ids []string) error` | membungkus `cli.MarkRead` |

Semua **aditif**; `AskBotHumanized` tidak diubah.

### 5.2 Alur engine per giliran
```
bot bubble masuk
  -> unreadDelay(settings.UnreadDelay*)    [tab]
  -> MarkRead ke bot                       [baru]
  -> readDuration(settings.ReadDelay*)     [tab]
  -> preThink(settings.PreThink*)          [tab]
  -> multi-burst composing(settings.Typing*) [tab + baru]
  -> kirim pesan
  -> paused
  -> interTurnDelay / cooldown / long rest [tab]
```

> Bila `MarkReadBatch`/`Composing` gagal (mis. WP mengembalikan error),
> engine **lanjut** dan mencatat ke `turns.note` — tidak menggagalkan sesi.

### 5.3 Yang tidak diubah
- `AskBotHumanized` dan seluruh runner Bank/OOB/Stress.
- Tabel `user_settings` dan form tab (tidak ada field baru).
- Nilai default: tetap dari tab, tidak ada hardcode baru.

---

## 6. File yang Disentuh

### Baru
| File | Isi |
|---|---|
| `internal/livechat/migrations/002_session_rng_report_null.sql` | DDL §2 |
| `internal/livechat/responder_variants.go` | tabel varian Lapis A |
| `internal/livechat/responder_test_variants_test.go` | test ≥4 varian per kind |
| `internal/ai/chat_reply.go` | `ChatReply` (aditif) |

### Diubah
| File | Perubahan | Risiko |
|---|---|---|
| `internal/livechat/model.go` | `Session.RNGSeed`, `ReportText` string+nilai kosong sah | rendah |
| `internal/livechat/store.go` | insert/scan `rng_seed`, longgarkan validasi (`only device+bot`) | rendah |
| `internal/livechat/responder.go` | varian + `BotContext` + mode `isi_laporan` | sedang |
| `internal/livechat/engine.go` | seed per sesi, unread/markRead/readDuration/composing | sedang |
| `internal/livechat/internals.go` | `unreadDelay`, `readDuration`, `composingSend` | sedang |
| `internal/livechat/wa_adapter.go` | expose `MarkRead` / `ChatPresence` | rendah |
| `internal/wa/client.go` | **+3 method aditif** (§5.1) | sedang (inti) |
| `internal/web/livechat.go` | auto-random, bot phone resolver, kirim seed | rendah |
| `internal/web/livechat_adapter.go` | oper `aiEngine` + `aiEnabled` | rendah |
| `internal/livechat/bootstrap.go` | pasang `Responder.AI` bila aktif | rendah |
| `public/livechat.js` | hapus required/guard, prefill bot phone, hint | rendah |

### **Tidak** disentuh
`internal/runner/*` · `internal/excel/*` · `internal/tenants/*` · `internal/ai` (selain +1 method) ·
schema `public.*` · schema `workbench.*` · tabel `livechat.sessions` selain 2 DDL §2 ·
`data/users/*/workbook.xlsx`

---

## 7. Tahapan Implementasi

| # | Tahap | DoD |
|---|---|---|
| 1 | **DDL** §2 — tampilkan SQL, tunggu "jalankan" | kolom `rng_seed` ada; `report_text` nullable; sesi lama valid |
| 2 | **Model + Store** (`rng_seed`, report NULL, validasi longgar) | unit test insert/scan; sesi lama tetap terbaca |
| 3 | **Lapis A varian + seed engine** | test ≥4 varian/kind; nilai faktual identik; seed sama → hasil sama |
| 4 | **Lapis B `BotContext`** | test: bot mengulang → kalimat berbeda & menegaskan |
| 5 | **Anti-spam** (3 method `wa` + alur engine) | test fake WA: urutan unread→markRead→read→preThink→composing→send; gagal markRead tidak mematikan sesi |
| 6 | **Auto-random + UI** | submit hanya device → 201; semua field terisi; UI tanpa `required` |
| 7 | **Lapis C AI** | LLM mati → fallback; LLM ada → dipakai; tidak pernah menggagalkan sesi |
| 8 | **Verifikasi akhir** | `go build`, `go vet`, `go test -count=1 ./...`, `node --check`, data user 2 utuh |

Setiap tahap berhenti untuk lapor. Verifikasi per tahap:
```
go build ./... && go vet ./... && go test -count=1 ./...
node --check public/livechat.js
psql: \dt livechat.* (4) | \dt public.* (5) | \dt workbench.* (4)
sha256sum -c data/backups_secure/workbench_pre_20261121_115748/sha256.original.live
```

---

## 8. Risiko & Mitigasi

| Risiko | Mitigasi |
|---|---|
| `MarkRead`/`Composing` memicu lebih banyak event WA → risiko ban naik? | Semua event **mengikuti parameter tab** (jeda manusiawi), bukan dikirim beruntun. Justru menyerupai manusia. |
| Bot target user belum diatur | Fallback `.env BOT_PHONE`; bila kosong → 400 dengan pesan jelas di UI. |
| AI timeout memperlambat sesi | Timeout pendek + fallback lokal; sesi tidak menunggu AI lebih dari ~3 dtk. |
| `report_text` NULL merusak kode lama | Semua pembacaan lewat `coalesce(report_text,'')`; Excel menampilkan kosong. |
| Seed tak konsisten antar giliran | Seed disimpan di `Engine` sekali; tidak di-reseed. |
| Varian mengubah fakta | Unit test membandingkan nilai faktual per kind dengan varian mana pun. |

---

## 9. Keputusan Terbuka (kecil)

| # | Pertanyaan | Usulan |
|---|---|---|
| 1 | Format `title` fallback | `"Live Chat 21 Nov 18:45"` |
| 2 | Timeout AI untuk Live Chat | **3000 ms** (terpisah dari `LLM_TIMEOUT_MS` runner agar tidak memperlambat) |
| 3 | Apakah `MarkRead` wajib atau opsional per sesi? | **Selalu aktif** (bagian anti-spam) |

---

**Status: PLAN ONLY — belum ada kode/DDL yang dijalankan.**
Menunggu persetujuan untuk mulai **Tahap 1 (DDL)**.
