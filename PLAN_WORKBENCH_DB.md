# RENCANA: Migrasi Database-First — `workbench` + Rework Live Chat Excel

Dokumen ini adalah **rencana**, belum ada implementasi.
Target kerja: `/var/www/kominfo/whatshap-gateway`

Status: **BELUM DIKERJAKAN** — menunggu persetujuan atas §12.

Revisi: **v2** — `internal/runner` disetujui diubah (mengikuti rekomendasi tim);
template workbook mengikuti rekomendasi arsitek; seluruh klaim teknis diaudit
ulang terhadap kode aktual.

---

## 1. Ringkasan & Tujuan

Memindahkan **seluruh data pengujian** (Bank Pertanyaan, Uji Diluar Bank, Uji
Stress AI) dari kolom biner `public.user_workbooks.file_data BYTEA` ke **tabel
relasional di schema baru `workbench`**, sehingga:

1. Database menjadi **single source of truth** — tidak ada lagi file Excel 19 MB
   ditulis/dibaca ulang setiap 20 detik atau setiap shutdown.
2. Excel hanya menjadi **format laporan** yang di-generate **di memori** saat
   tombol Download ditekan, lalu di-stream ke browser. File `.xlsx` **tidak lagi
   disimpan di database maupun di disk**.
3. **Kesiapan analitik (bukan fitur yang dikerjakan di plan ini)**: karena data
   sudah relasional, rekap lintas user (akurasi per OPD, progres per tester,
   evaluasi Live Chat) *nanti* dapat di-query dengan `SELECT … GROUP BY`. Fitur
   rekap/dashboard **ditunda** — lihat §1.1 dan §19.

### 1.1 Non-Goals (tegas di luar lingkup)

- Tidak mengubah isi `data/users/*/workbook.xlsx` (file dibekukan sebagai arsip).
- Tidak mengubah `internal/wa`, atau `internal/livechat` engine/detector/responder.
- Tidak menambah dependency Go baru.
- Tidak mengubah perilaku fungsional runner: hanya **tipe** yang berubah, bukan
  alur uji.
- **Tidak mengerjakan fitur rekap/dashboard** (per keputusan user): tidak ada
  endpoint `/api/dashboard/rekap`, tidak ada `/api/admin/dashboard-global`,
  tidak ada perubahan UI toggle dashboard, tidak ada tabel snapshot. Detail
  ditunda ke §19.

### 1.2 Fakta terverifikasi (dasar perencanaan)

| Item | Nilai terukur |
|---|---|
| `data/users/2/workbook.xlsx` | 19.099.736 byte, sha256 `d921ae42…a7769` |
| `public.user_workbooks.file_data` | 19.099.736 byte, md5 `97cffe4dc8aa4c921db4a4462f8424fe` |
| Baris Bank Pertanyaan diparse `excel.Store` | **210** (max `No` = 210) |
| Baris Uji Diluar Bank | **52** (max `No` = 52) |
| Baris Uji Stress AI | **15** (max `No` = 15) |
| Lampiran terdaftar | `bankMedia` 104 + `outOfBankMedia` 14 = **118 path** |
| File lampiran ditemukan di disk | **118** dari 118 |
| Schema saat ini | `public` (5 tabel) + `livechat` (4 tabel) |

> Koreksi terhadap `PLAN_LIVECHAT.md` yang menyebut "36 baris + 114 gambar":
> angka aktual dari parser adalah **52 OOB / 15 Stress / 118 lampiran**. Plan ini
> memakai angka aktual; tahap 5 memverifikasi ulang baris-per-baris.

### 1.3 Temuan audit kode (dasar keputusan desain)

Hasil pembacaan ulang `internal/runner`, `internal/web`, `internal/excel`,
`internal/tenants`, dan `main.go`:

| # | Temuan | Dampak pada plan |
|---|---|---|
| A | `runner.Runner.store` bertipe konkret `*excel.Store`; **tidak ada** akses field internal (`bankMetrics`, `rows`, dsb.) dari runner — hanya 17 pemanggilan **method** publik | Interface cukup berisi method publik → aman |
| B | `runner` **tidak** melakukan `nil`-check pada `store` (`SetStore` menerima pointer apa adanya). `web.tenantStore` **tidak pernah** mengembalikan nil (fallback `NewEmptyStore()`), jadi risiko saat ini rendah — guard tetap ditambahkan sebagai hardening saat tipe berubah menjadi interface | Tambah guard, bukan perubahan perilaku |
| C | **Tidak ada** pemakaian `Store.FilePath()` dan `Store.MediaDir()` di luar paket `excel` | Keduanya **boleh keluar** dari interface |
| D | `Store.ExportBytes()` dipakai `handleExcelDownloadForUser` dengan signature `(data, fileName, err)` | Interface harus memuat `ExportBytes(ctx) ([]byte, string, error)` |
| E | `Store.ReplaceFromBytes(raw)` mengembalikan `(OverallSummary, error)` dan **menulis file** | Di `DBStore`, implementasi menulis DB, bukan file |
| F | `computeSummaryLocked` memakai `s.opdOrder` yang berasal dari urutan sheet `Ringkasan` | `DBStore` harus mempertahankan urutan OPD; disimpan di kolom `opd_order` |
| G | `runner/manager_test.go` dan `web/server_test.go` meng-`construct` `excel.NewStore(...)` | Test lama **tetap hijau** karena `*excel.Store` memenuhi interface |
| H | `excel.NewStore(path)` memanggil `ensureBackup` + baca disk | ETL boleh memakainya sebagai parser; `DBStore` **tidak** |
| I | `main.go` menjalankan tiker `FlushDirty` 20 dtk + `PersistAll` saat shutdown | Keduanya dihentikan saat flag aktif |

---

## 2. Keputusan Arsitektur

| Aspek | Keputusan | Alasan |
|---|---|---|
| Schema | **`workbench`** (baru), berdampingan dengan `public` dan `livechat` | Isolasi; pola prefix eksplisit sudah dipakai `livechat.` |
| Baris soal | 1 baris = 1 soal; PK `id`, unik `(user_id, no)` | Sederhana, mudah di-`GROUP BY` |
| Metrik ritme manusia | `metrics JSONB` | 26 field `RowHumanMetrics`; tidak pernah di-query agregat |
| Lampiran | `media_files TEXT[]` berisi **path relatif** | Sesuai keputusan user: file tetap di disk |
| Urutan OPD | `workbench.user_workbooks.opd_order TEXT[]` | Menjaga urutan `Ringkasan` seperti lama (temuan F) |
| Excel | Generate **in-memory** via `excelize` dari SQL | Tidak ada file yang ditulis |
| Template | **`data/templates/workbook_template.xlsx`** (runtime, bukan embed) | Lihat §4.5 — keputusan arsitek |
| Cut-over | **Big-bang dengan feature flag** `WORKBENCH_DB=1` | Rollback tanpa revert kode |
| Tipe di runner/web | **Interface** `workbench.Store` | Memutus ketergantungan tipe konkret |
| `*excel.Store` | **Tetap ada**, dipertahankan sebagai implementasi lama + parser ETL | Rollback instan; test lama hijau |

### 2.1 Mengapa interface, dan mengapa `*excel.Store` tetap hidup

- `runner`/`web` hanya memakai **25 method publik** dari 28 yang ada (temuan A,
  hasil audit), sehingga interface sempit dan stabil bisa dibuat.
- `*excel.Store` **sudah memenuhi** interface tersebut (signature sama), jadi
  tidak perlu "alias method" tambahan → lebih sedikit kode.
- `WORKBENCH_DB=0` → `excel.Store` (perilaku hari ini, 100%).
  `WORKBENCH_DB=1` → `workbench.DBStore`.
- Test regresi lama (`excel`, `runner`, `web`) tetap menguji jalur file → bukti
  rollback tidak rusak.

---

## 3. Skema Database Baru (`workbench`)

### 3.1 DDL

```sql
BEGIN;

CREATE SCHEMA IF NOT EXISTS workbench;

-- 3.1.1 Metadata per user (+ urutan OPD untuk Ringkasan)
CREATE TABLE IF NOT EXISTS workbench.user_workbooks (
    user_id     BIGINT       PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    file_name   VARCHAR(200) NOT NULL DEFAULT 'workbook.xlsx',
    opd_order   TEXT[]       NOT NULL DEFAULT '{}',
    imported_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- 3.1.2 Bank Pertanyaan (210 baris terverifikasi)
CREATE TABLE IF NOT EXISTS workbench.bank_rows (
    id                 BIGSERIAL    PRIMARY KEY,
    user_id            BIGINT       NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    no                 INT          NOT NULL,
    excel_row          INT          NOT NULL DEFAULT 0,
    opd                VARCHAR(200) NOT NULL DEFAULT '',
    kategori           VARCHAR(200) NOT NULL DEFAULT '',
    status_bank        VARCHAR(50)  NOT NULL DEFAULT '',
    question_ref       TEXT         NOT NULL DEFAULT '',
    question_input     TEXT         NOT NULL DEFAULT '',
    expected_reply     TEXT         NOT NULL DEFAULT '',
    actual_reply       TEXT         NOT NULL DEFAULT '',
    notes              TEXT         NOT NULL DEFAULT '',
    result_status      VARCHAR(20)  NOT NULL DEFAULT ''
        CHECK (result_status IN ('','Cocok','Tidak')),
    has_expected_file  BOOLEAN      NOT NULL DEFAULT false,
    has_expected_coord BOOLEAN      NOT NULL DEFAULT false,
    has_actual_file    BOOLEAN      NOT NULL DEFAULT false,
    has_actual_coord   BOOLEAN      NOT NULL DEFAULT false,
    media_files        TEXT[]       NOT NULL DEFAULT '{}',
    metrics            JSONB        NOT NULL DEFAULT '{}'::jsonb,
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uq_bank_user_no UNIQUE (user_id, no)
);

-- 3.1.3 Uji Diluar Bank (52 baris terverifikasi)
CREATE TABLE IF NOT EXISTS workbench.out_of_bank_rows (
    id                BIGSERIAL    PRIMARY KEY,
    user_id           BIGINT       NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    no                INT          NOT NULL,
    excel_row         INT          NOT NULL DEFAULT 0,
    source_ref        VARCHAR(200) NOT NULL DEFAULT '',
    scenario_type     VARCHAR(200) NOT NULL DEFAULT '',
    base_question     TEXT         NOT NULL DEFAULT '',
    probe_question    TEXT         NOT NULL DEFAULT '',
    expected_behavior TEXT         NOT NULL DEFAULT '',
    actual_reply      TEXT         NOT NULL DEFAULT '',
    human_summary     TEXT         NOT NULL DEFAULT '',
    notes             TEXT         NOT NULL DEFAULT '',
    result_status     VARCHAR(20)  NOT NULL DEFAULT ''
        CHECK (result_status IN ('','Cocok','Tidak')),
    forbidden_year    VARCHAR(20)  NOT NULL DEFAULT '',
    reference_year    VARCHAR(20)  NOT NULL DEFAULT '',
    media_files       TEXT[]       NOT NULL DEFAULT '{}',
    metrics           JSONB        NOT NULL DEFAULT '{}'::jsonb,
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uq_oob_user_no UNIQUE (user_id, no)
);

-- 3.1.4 Uji Stress AI (15 baris terverifikasi)
CREATE TABLE IF NOT EXISTS workbench.stress_test_rows (
    id                BIGSERIAL    PRIMARY KEY,
    user_id           BIGINT       NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    no                INT          NOT NULL,
    excel_row         INT          NOT NULL DEFAULT 0,
    stress_category   VARCHAR(200) NOT NULL DEFAULT '',
    user_prompt_q     TEXT         NOT NULL DEFAULT '',
    intent_purpose    TEXT         NOT NULL DEFAULT '',
    expected_behavior TEXT         NOT NULL DEFAULT '',
    actual_reply      TEXT         NOT NULL DEFAULT '',
    human_summary     TEXT         NOT NULL DEFAULT '',
    notes             TEXT         NOT NULL DEFAULT '',
    result_status     VARCHAR(20)  NOT NULL DEFAULT ''
        CHECK (result_status IN ('','Cocok','Tidak')),
    politeness_score  INT          NOT NULL DEFAULT 0,
    hallucination_lvl VARCHAR(50)  NOT NULL DEFAULT '',
    media_files       TEXT[]       NOT NULL DEFAULT '{}',
    metrics           JSONB        NOT NULL DEFAULT '{}'::jsonb,
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uq_stress_user_no UNIQUE (user_id, no)
);

CREATE INDEX IF NOT EXISTS idx_wb_bank_user_status
    ON workbench.bank_rows (user_id, result_status);
CREATE INDEX IF NOT EXISTS idx_wb_bank_opd
    ON workbench.bank_rows (user_id, opd, result_status);
CREATE INDEX IF NOT EXISTS idx_wb_oob_user_status
    ON workbench.out_of_bank_rows (user_id, result_status);
CREATE INDEX IF NOT EXISTS idx_wb_stress_user_status
    ON workbench.stress_test_rows (user_id, result_status);

COMMIT;
```

### 3.2 Pemetaan kolom Excel → tabel

Disalin dari `internal/excel/store.go` (baris 292–475), **bukan** ditafsir ulang.

**Bank Pertanyaan** (mulai baris 3):
`A=no, B=opd, C=kategori, D=status_bank, E=question_ref, F=question_input,`
`G=expected_reply, H=actual_reply, I=notes, J=result_status`

**Uji Diluar Bank** (mulai baris 3):
`A=no, B=source_ref, C=scenario_type, D=base_question, E=probe_question,`
`F=expected_behavior, G=actual_reply, H=human_summary, I=notes,`
`J=result_status, K=forbidden_year, L=reference_year`

**Uji Stress AI** (mulai baris 3):
`A=no, B=stress_category, C=user_prompt_q, D=intent_purpose, E=expected_behavior,`
`F=actual_reply, G=human_summary, H=notes, I=result_status, J=politeness_score,`
`K=hallucination_lvl`

**Metrics & media** dari `workbook.metrics.json`:
`bank{no}→metrics`, `outOfBank{no}→metrics`, `stress{no}→metrics`,
`bankMedia{no}→media_files`, `outOfBankMedia{no}→media_files`.

**Urutan OPD** dari sheet `Ringkasan` (kolom A baris 3+), disimpan ke
`workbench.user_workbooks.opd_order` (temuan F).

---

## 4. Rencana Perubahan Kode

### 4.1 Interface `internal/workbench/store.go`

Diturunkan **persis** dari 25 method publik `*excel.Store` yang dipakai
`runner` + `web` (temuan A, hasil audit aktual). Dari 28 method publik yang ada,
`MediaDir`, `FilePath`, dan `ReplaceWorkbook` **dikeluarkan** (temuan C) karena
tidak dipakai di luar paket `excel`.

```go
package workbench

import "whatshap-gateway/internal/model"

// Store adalah kontrak tunggal untuk data pengujian per user.
// Dipenuhi oleh *excel.Store (jalur lama, flag WORKBENCH_DB=0) dan
// *workbench.DBStore (jalur baru, flag WORKBENCH_DB=1).
//
// Signature di bawah DISALIN PERSIS dari method publik *excel.Store yang
// dipakai runner + web (diverifikasi Tahap 4 lewat assert kompilasi).
type Store interface {
	// Snapshot & ringkasan (dipakai dashboard personal + SSE)
	Snapshot() ([]model.TestRow, model.OverallSummary)
	OutOfBankSnapshot() ([]model.OutOfBankRow, model.OutOfBankSummary)
	StressTestSnapshot() ([]model.StressTestRow, model.StressTestSummary)

	// Baca satu baris
	GetRow(no int) (model.TestRow, bool)
	GetOutOfBankRow(no int) (model.OutOfBankRow, bool)
	GetStressTestRow(no int) (model.StressTestRow, bool)

	// Antrean pending (dipakai runner)
	PendingRowNumbers(forceAll bool, limit int) []int
	PendingRowRange(fromNo, toNo int, forceAll bool) []int
	PendingOutOfBankNumbers(forceAll bool, limit int) []int
	PendingOutOfBankRange(fromNo, toNo int, forceAll bool) []int
	PendingStressNumbers(forceAll bool, limit int) []int
	PendingStressRange(fromNo, toNo int, forceAll bool) []int

	// Mutasi satu baris
	UpdateRow(no int, patch model.RowPatch) (model.TestRow, model.OverallSummary, error)
	UpdateOutOfBankRow(no int, patch model.OutOfBankPatch) (model.OutOfBankRow, model.OutOfBankSummary, error)
	UpdateStressTestRow(no int, patch model.StressTestPatch) (model.StressTestRow, model.StressTestSummary, error)

	// Tambah/hapus Uji Diluar Bank
	AppendOutOfBankRows(items []model.OutOfBankRow) ([]model.OutOfBankRow, model.OutOfBankSummary, int, error)
	DeleteOutOfBankRow(no int) ([]model.OutOfBankRow, model.OutOfBankSummary, error)
	DeleteOutOfBankBatch(numbers []int) ([]model.OutOfBankRow, model.OutOfBankSummary, error)
	ClearOutOfBank() ([]model.OutOfBankRow, model.OutOfBankSummary, error)

	// Tambah/hapus Uji Stress AI
	AppendStressTestRows(items []model.StressTestRow) ([]model.StressTestRow, model.StressTestSummary, int, error)
	DeleteStressTestRow(no int) ([]model.StressTestRow, model.StressTestSummary, error)
	DeleteStressTestBatch(numbers []int) ([]model.StressTestRow, model.StressTestSummary, error)
	ClearStressTest() ([]model.StressTestRow, model.StressTestSummary, error)

	// Ganti seluruh workbook (jalur upload Excel baru)
	ReplaceFromBytes(uploaded []byte) (model.OverallSummary, error)

	// Unduh Excel (generate in-memory untuk DBStore, baca file untuk excel.Store)
	ExportBytes() ([]byte, string, error)
}
```

**Assert kompilasi** (tahap 4, file `internal/workbench/assert.go`):

```go
package workbench

import "whatshap-gateway/internal/excel"

var _ Store = (*excel.Store)(nil) // jalur lama WAJIB memenuhi interface
var _ Store = (*DBStore)(nil)     // jalur baru WAJIB memenuhi interface
```

Bila salah satu gagal compile, definisi interface salah — bukan wiring-nya.

### 4.2 Implementasi `workbench.DBStore`

```go
type DBStore struct {
    db     *sql.DB
    userID int64
    mu     sync.RWMutex // serialisasi tulis per user
}
```

Perilaku per method (padanan dengan `excel.Store`):

| Method interface | Query SQL |
|---|---|
| `Snapshot` | `SELECT … ORDER BY no` + agregasi summary via `FILTER (WHERE …)` |
| `OutOfBankSnapshot` / `StressTestSnapshot` | idem |
| `GetRow` / `GetOutOfBankRow` / `GetStressTestRow` | `SELECT … WHERE user_id=$1 AND no=$2` |
| `PendingRowNumbers` | `WHERE result_status=''` (atau tanpa filter bila `forceAll`) `ORDER BY no LIMIT $n` |
| `Pending*Range` | idem + `AND no BETWEEN $a AND $b` |
| `UpdateRow` | `UPDATE … SET … WHERE user_id=$1 AND no=$2 RETURNING *` lalu bangun summary |
| `AppendOutOfBankRows` | dedupe `lower(probe_question)` via `ON CONFLICT DO NOTHING` (setara logika lama) |
| `Delete*Batch` / `Clear*` | `DELETE … WHERE user_id=$1 AND no = ANY($2)` |
| `ReplaceFromBytes` | parse via `excel.ParseBytes` (lihat §4.3) → `BEGIN; DELETE; INSERT; COMMIT` |
| `ExportBytes` | panggil generator §6 |

Catatan penting:
- Summary **harus identik** dengan `excel.computeSummaryLocked`, termasuk
  `Approved`/`Waiting` dari `status_bank == "waiting"` dan urutan OPD dari
  `opd_order`.
- Semua query memakai prefix eksplisit `workbench.`; tidak ada `search_path`.
- Semua mutasi menyertakan `WHERE user_id = $1`.

### 4.3 Parser bersama untuk upload

`*excel.Store.ReplaceFromBytes` saat ini **menulis file**. Untuk `DBStore`,
tambahkan satu fungsi murni di `internal/excel`:

```go
// ParseRows mem-parse bytes workbook menjadi baris-baris siap-insert.
// Tidak menulis disk, tidak memakai sidecar metrics.
func ParseRows(raw []byte) (ParseResult, error)

type ParseResult struct {
    Bank      []model.TestRow
    OOB       []model.OutOfBankRow
    Stress    []model.StressTestRow
    OPDOrder  []string
}
```

Ini **refactor internal** `excel.Store` (memecah fungsi parse dari IO), bukan
perubahan perilaku. `ReplaceFromBytes` lama tetap bekerja dan bisa
diimplementasikan di atas `ParseRows` + writer lama.

### 4.4 Perubahan file yang sudah ada

| File | Perubahan | Sifat |
|---|---|---|
| `internal/runner/runner.go` | `store *excel.Store` → `store workbench.Store`; import `workbench`; tambah guard `if r.store == nil` di 6 titik masuk (temuan B). **Logika uji tidak berubah.** | ⚠️ inti — disetujui |
| `internal/runner/manager.go` | `ForUser(..., store workbench.Store, ...)`; import | ⚠️ inti — disetujui |
| `internal/web/server.go` | `tenantStore(ctx, userID) workbench.Store`; nil guard tetap; import `workbench` (bukan `excel`) | mekanis |
| `internal/web/tenant_state.go` | pass-through tipe | mekanis |
| `internal/excel/store.go` | Ekstrak `ParseRows` (§4.3); perilaku lain tetap | aditif + refactor internal |
| `internal/tenants/manager.go` | Tidak berubah — tetap dipakai saat `WORKBENCH_DB=0` | tidak berubah |
| `main.go` | Pilih implementasi dari flag; bila `WORKBENCH_DB=1`: hentikan tiker `FlushDirty`/`PersistAll` (temuan I) | mekanis |

**Bukti diff minimal:** pada `internal/runner/runner.go`, satu-satunya perubahan
adalah tipe parameter, import, dan guard; **tidak ada** perubahan alur/fungsi uji.

### 4.5 Template workbook — keputusan rekomendasi arsitek

**Keputusan: `data/templates/workbook_template.xlsx` (runtime, bukan `//go:embed`).**

Alasan teknis (bukan preferensi):

| Kriteria | `//go:embed` (19 MB) | **`data/templates/` (runtime)** |
|---|---|---|
| Ukuran binary | +19 MB setiap build/commit | 0 |
| Waktu build | Lebih lambat | Tetap |
| Ganti template tanpa rebuild | Tidak bisa | Bisa (mis. revisi kolom) |
| Risiko git error (binary besar) | Tinggi | Nol |
| Ketersediaan file | Selalu | Perlu seeding sekali |
| Keamanan | Terkunci di binary | Bisa diganti admin |

Seeder: `data/templates/.gitkeep` di-commit; file template **disalin** dari
`Uji-Bank-Pertanyaan-Programmer-4.xlsx` oleh:

```
go run ./cmd/workbench-seed-template -src Uji-Bank-Pertanyaan-Programmer-4.xlsx
```

yang:
1. Membuang semua gambar & sheet data (`Bank Pertanyaan`, `Uji Diluar Bank`,
   `Uji Stress AI`) → hanya menyisakan **header + styling + formula
   `Ringkasan` + `Petunjuk`**.
2. Menyimpan ke `data/templates/workbook_template.xlsx` (jauh lebih kecil, ~200 KB).
3. Mencetak sha256 template untuk dicatat di manifest.

Fallback bila template tidak ada: generator membuat workbook minimal
(5 sheet, header saja) dan mencatat peringatan — **download tetap berhasil**,
hanya kehilangan styling.

### 4.6 Kontrak endpoint yang berubah

| Endpoint | Sebelum | Sesudah | Kontrak HTTP |
|---|---|---|---|
| `GET /api/excel/download` | baca file disk | generate in-memory | **sama** (`content-type`, `content-disposition`, `content-length`) |
| `POST /api/excel/upload` | tulis file + bytea | parse → insert transaksional ke `workbench.*` | **sama** (response payload identik) |
| `GET /api/livechat/excel/download` | `ServeFile` | generate in-memory | **sama** |

---

## 5. ETL: Migrator Satu Kali Jalan

`cmd/workbench-migrate/main.go`

```
go run ./cmd/workbench-migrate -user 2 \
       -xlsx data/users/2/workbook.xlsx \
       -metrics data/users/2/workbook.metrics.json \
       -dry-run                       # default
go run ./cmd/workbench-migrate -user 2 ... -apply
```

Alur:
1. Parse via `excel.ParseRows` (parser yang sudah teruji).
2. Baca `metrics.json` untuk `metrics` + `media_files`.
3. `BEGIN; DELETE FROM workbench.<t> WHERE user_id=$1; INSERT …; COMMIT;`
4. `-dry-run` mencetak: 210 / 52 / 15 / 118 / jumlah OPD / jumlah path media hilang.

---

## 6. Generator Excel In-Memory

`internal/workbench/export.go`

```
ExportBytes() ([]byte, string, error):
  1. load template (data/templates/workbook_template.xlsx) atau minimal fallback
  2. isi sheet Bank/OOB/Stress dari SQL (baris data saja)
  3. untuk baris dengan media_files != {}, resolve path disk
     (data/users/{id}/downloads/… lalu data/downloads/…) →
     f.AddPictureFromBytes (logika disalin dari excel.embedRowPictures)
  4. tulis ke bytes.Buffer, kembalikan; TIDAK menyentuh disk/database
```

Batasan:
- `sync.Mutex` per `(userID)` agar tidak ada dua generate bersamaan (memori ~20 MB).
- Path gambar hilang → log warning, baris tetap tertulis tanpa gambar.
- Penamaan file hasil: `workbook.xlsx` (sama seperti sekarang).

---

## 7. Rework Live Chat Excel (bisa jalan lebih dulu)

| Item | Perubahan |
|---|---|
| `internal/livechat/excel.go` | Tambah `GenerateExcel(ctx, store, userID) ([]byte, error)` membaca `livechat.sessions`; `Exporter` lama dipertahankan sementara |
| `internal/web/livechat.go` `handleExcelDownload` | Generate → `w.Write`; tidak lagi `ServeFile` |
| File `data/users/*/livechat.xlsx` | Tidak lagi dibuat (sudah di `.gitignore`) |

Tidak bergantung pada `workbench` → bisa dikerjakan paralel.

---

## 8. Rekap / Dashboard — DITUNDA

**Keputusan user: tidak dikerjakan dulu.** Bagian ini hanya mencatat apa yang
sengaja **tidak** dibuat, agar tidak ada yang mengira fitur ini ikut di rilis.

**Tidak ada** dalam lingkup plan ini:

- endpoint `GET /api/dashboard/rekap`
- endpoint `GET /api/admin/dashboard-global`
- toggle `[ Data Saya ] [ Rekap Semua Tester ]` di tab Dashboard
- tabel agregasi/snapshot (`workbench.daily_stats` atau sejenisnya)
- perubahan `public/index.html`, `public/app.js`, `public/styles.css` untuk rekap

**Yang tetap berjalan tanpa perubahan:** tab *Dashboard & Ringkasan OPD* yang
sudah ada. Ia membaca `model.OverallSummary` dari `Snapshot()`/`OutOfBankSnapshot()`
/`StressTestSnapshot()` — method yang **sudah ada di interface** (§4.1) dan tetap
dipenuhi baik oleh `*excel.Store` maupun `DBStore`. Jadi dashboard personal
tester tidak ikut rusak.

**Prasyarat bila nanti rekap dilanjutkan:** cukup data sudah ada di
`workbench.*` (hasil Tahap 1–14). Query `GROUP BY` bisa ditambahkan kapan saja
tanpa DDL baru, tanpa mengubah skema, dan tanpa migrasi ulang. Detail rancangan
awal disimpan di §19 sebagai backlog, bukan instruksi kerja.

---

## 9. Tahapan Implementasi

> Setiap tahap **berhenti untuk lapor**. Build/vet/test wajib hijau.
> DDL tidak dieksekusi tanpa persetujuan eksplisit ("jalankan").

| # | Tahap | DoD |
|---|---|---|
| 1 | **Backup & baseline** ke `data/backups_secure/workbench_pre_<ts>/` | manifest sha256; `\dt workbench.*` kosong; `go test ./...` hijau |
| 2 | **DDL schema `workbench`** (§3.1) — tampilkan dulu | `\dt workbench.*`=4; `\dt public.*`=5; `\dt livechat.*`=4 |
| 3 | **Refactor `excel.ParseRows`** (§4.3), tanpa perubahan perilaku | `excel` test lama hijau; `ParseRows` diuji terhadap `workbook.xlsx` |
| 4 | **Interface + assert kompilasi** (§4.1) | `var _ Store = (*excel.Store)(nil)` compile |
| 5 | **ETL `-dry-run`** | cetak 210/52/15/118/OPD |
| 6 | **ETL `-apply` + parity audit** | 0 beda nilai semua kolom; 118 path resolve |
| 7 | **`workbench.DBStore`** + unit test | hasil method == hasil `excel.Store` pada dataset sama |
| 8 | **Generator Excel in-memory** + parity | xlsx hasil generator == sumber (baris, kolom, nilai, gambar) |
| 9 | **Seeder template** (§4.5) | template <1 MB, sha256 tercatat |
| 10 | **Wiring runner/web/main di balik flag** | flag=0 → `go test` lama hijau; flag=1 → semua endpoint | 
| 11 | **Rework Live Chat Excel** (§7) | download dari SQL; tidak ada file baru di disk |
| 12 | **Cut-over default** (`WORKBENCH_DB=1`) | bytea tidak berubah 24 jam; tiker flush mati |
| 13 | **Verifikasi akhir** | §10 lengkap; dashboard personal tetap berfungsi |

---

## 10. Strategi Transaksi & Konkurensi

Ini bagian yang sering dilupakan dan menjadi sumber bug paling mahal. Aturan
yang **wajib** dipatuhi `DBStore`:

| Aturan | Alasan |
|---|---|
| Setiap mutasi = **satu** statement atau satu transaksi utuh | Tidak ada state setengah jadi |
| `ReplaceFromBytes` = `BEGIN` → `DELETE` → `INSERT` → `COMMIT` | Upload baru tidak boleh meninggalkan data parsial |
| Semua `DELETE`/`UPDATE` **wajib** `WHERE user_id = $1` | Isolasi tenant; tidak ada operasi lintas user |
| `SELECT … FOR UPDATE` **tidak** dipakai | Runner sudah menjamin 1 baris dikerjakan 1 device (`TryLockRow`) |
| Tulis per user diserialisasi oleh `DBStore.mu` | Cegah dua UPDATE bersamaan pada baris yang sama saat upload |
| `RETURNING *` pada setiap `UPDATE` | Hindari *read-after-write* yang bisa melihat data orang lain |
| `context.Context` diteruskan dari handler | Request dibatalkan → query ikut dibatalkan |
| Statement **prepare** di-cache (`db.PrepareContext` sekali di `NewDBStore`) | 25 method, query stabil; hindari parse ulang |

**Skenario yang diuji wajib (Tahap 7):**

1. Dua `UpdateRow` paralel pada `no` berbeda → keduanya berhasil, summary benar.
2. `UpdateRow` dengan `user_id` milik user lain → 0 baris terpengaruh, error jelas.
3. `ReplaceFromBytes` yang gagal di tengah → data lama **utuh** (rollback).
4. `AppendOutOfBankRows` dengan `probe_question` duplikat → tidak menambah baris.
5. `context` dibatalkan saat transaksi → tidak ada baris setengah tersimpan.

---

## 11. Peta Dampak File (Impact Map)

Daftar lengkap file yang **disentuh** dan **tidak disentuh**, agar review mudah
dan tidak ada kejutan saat diff.

### Dibuat baru

| File | Isi |
|---|---|
| `internal/workbench/store.go` | Interface `Store` |
| `internal/workbench/dbstore.go` | Implementasi `DBStore` (25 method) |
| `internal/workbench/summary.go` | Agregasi summary identik dengan `computeSummaryLocked` |
| `internal/workbench/export.go` | Generator Excel in-memory |
| `internal/workbench/assert.go` | Assert kompilasi interface |
| `internal/workbench/*_test.go` | Unit + parity test |
| `cmd/workbench-migrate/main.go` | ETL `-dry-run` / `-apply` |
| `cmd/workbench-audit/main.go` | Parity audit baris-per-baris |
| `cmd/workbench-seed-template/main.go` | Seeder template runtime |
| `internal/workbench/template/.gitkeep` | Penanda direktori (file template tidak di-commit) |

### Diubah (minimal, mekanis)

| File | Perubahan | Risiko |
|---|---|---|
| `internal/runner/runner.go` | tipe param → interface; 1 import; nil-guard | sedang |
| `internal/runner/manager.go` | tipe param → interface; import | rendah |
| `internal/web/server.go` | `tenantStore` return interface; import | rendah |
| `internal/web/tenant_state.go` | pass-through tipe | rendah |
| `internal/excel/store.go` | ekstrak `ParseRows` (refactor internal) | **sedang** |
| `main.go` | pilih implementasi via flag; matikan tiker saat flag=1 | rendah |

### **Tidak** disentuh (jaminan)

`internal/wa/*` · `internal/livechat/{engine,detector,responder,manager,assets,excel}.go`
(kecuali Tahap 11 rework `excel.go` Live Chat) · `internal/ai/*` ·
`internal/auth/*` · `internal/config/*` · `public/app.js` · `public/index.html` ·
`public/styles.css` · `public/livechat.js` · skema `public.*` · skema `livechat.*` ·
`data/users/*/workbook.xlsx`

---

## 12. Kriteria Go / No-Go sebelum Cut-over

Tahap 12 (`WORKBENCH_DB=1` default) **hanya boleh dijalankan** jika **semua**
kriteria berikut terbukti:

| # | Kriteria | Cara membuktikan |
|---|---|---|
| G1 | Parity 100% dataset user 2 | `cmd/workbench-audit` → 0 beda pada 210/52/15 baris |
| G2 | Seluruh 118 media path resolve | audit mencetak `missing=0` |
| G3 | Summary identik | test membandingkan `DBStore.Snapshot()` vs `excel.Store.Snapshot()` |
| G4 | Generator Excel identik isinya | buka ulang hasil generate, bandingkan per cell |
| G5 | Test lama hijau di kedua mode | `go test` × `WORKBENCH_DB=0` dan `=1` |
| G6 | Race clean | `go test -race` pada `workbench`, `runner`, `web` |
| G7 | Tidak ada tulis ke `public.user_workbooks` | `updated_at` tidak berubah ≥24 jam |
| G8 | Rollback terbukti | set flag `0` → perilaku lama kembali, data intact |

Bila **satu** kriteria gagal → **No-Go**: tahap 12 dibatalkan, flag tetap `0`,
laporan dibuat.

---

## 13. Verifikasi Wajib per Tahap

```
go build ./...                                   → 0
go vet ./...                                     → 0
go test -count=1 ./...                           → semua paket hijau
go test -race ./internal/workbench/ ./internal/runner/   → 0
node --check public/app.js && node --check public/livechat.js
sha256sum -c data/backups_secure/workbench_pre_<ts>/sha256.original.live
psql: \dt workbench.*  (4) | \dt public.* (5) | \dt livechat.* (4)
```

Tambahan pada tahap 6, 8, 13:
- parity audit baris-per-baris (`cmd/workbench-audit`).
- `md5(public.user_workbooks.file_data)` tidak berubah selama 24 jam setelah cut-over.

---

## 14. Risiko & Mitigasi

| Risiko | Dampak | Mitigasi |
|---|---|---|
| `internal/runner` diubah | Uji Bank/OOB/Stress rusak | Perubahan hanya tipe + guard; `runner/manager_test.go` tetap hijau di **kedua** mode; rollback = `WORKBENCH_DB=0` |
| Interface tidak lengkap | Compile error saat wiring | Assert `var _ Store = (*excel.Store)(nil)` di tahap 4 |
| Summary DB beda dari summary lama | Tab Dashboard & Ringkasan OPD (personal) salah angka | Parity test tahap 7 pada dataset identik |
| Urutan OPD hilang | Tabel Ringkasan berubah urutan | `opd_order TEXT[]` disimpan saat ETL |
| Template hilang | Download tanpa styling | Fallback minimal + warning; seeder menyertakan |
| Generator memakan RAM | OOM saat banyak download | Mutex per user; 1 generate aktif per user |
| Upload workbook baru | Data tak konsisten | `ReplaceFromBytes` transaksional (DELETE+INSERT dalam satu `BEGIN`) |
| Dua sumber data saat transisi | Kebingungan | Flag biner: satu jalur aktif per proses |

---

## 15. Keputusan yang Sudah Dikunci

| # | Keputusan | Status |
|---|---|---|
| 1 | `internal/runner` **boleh diubah** (mengikuti rekomendasi tim): tipe `*excel.Store` → interface `workbench.Store`, logika uji tidak berubah | ✅ Dikunci |
| 2 | Template workbook **runtime** di `data/templates/workbook_template.xlsx`, disebar via `cmd/workbench-seed-template` | ✅ Dikunci |
| 3 | Schema **`workbench`** terpisah dari `public` dan `livechat` | ✅ Dikunci |
| 4 | Gambar **hanya path** di `media_files TEXT[]`; file di disk | ✅ Dikunci |
| 5 | Excel **in-memory**, tidak disimpan di DB/disk | ✅ Dikunci |
| 6 | Cut-over via flag `WORKBENCH_DB` default `0` | ✅ Dikunci |
| 7 | **Fitur rekap/dashboard tidak dikerjakan** pada plan ini (ditunda ke §19) | ✅ Dikunci |
| 8 | **`internal/excel` dipertahankan sebagai parser** (`ParseRows`/`ReplaceFromBytes`), bukan dipensiunkan | ✅ Dikunci |
| 9 | `public.user_workbooks` **diarsipkan** (tidak dibaca/ditulis lagi, tidak di-drop) | ✅ Dikunci |

### 15.1 Masih Terbuka

| # | Pertanyaan | Usulan |
|---|---|---|
| 1 | Retensi data Live Chat (warisan `PLAN_LIVECHAT.md` §14.1) | Tidak diubah plan ini |
| 2 | Kapan `public.user_workbooks` boleh di-drop | Evaluasi setelah 30 hari stabil; bukan bagian plan ini |

---

## 16. Riwayat Revisi

| Revisi | Perubahan |
|---|---|
| v1 | Rancangan awal |
| v2 | `internal/runner` disetujui diubah; template runtime (bukan embed) dipilih; interface diturunkan dari audit 25 method yang dipakai (bukan perkiraan); `opd_order` ditambahkan (temuan F); `ParseRows` diekstrak (temuan E); nil-guard runner (temuan B); `MediaDir`/`FilePath` dikeluarkan (temuan C); assert kompilasi interface; angka 52/15/118 dikoreksi |
| v3 | **Fitur rekap/dashboard ditunda** (keputusan user). §8 diubah dari spesifikasi menjadi "DITUNDA"; §1 tujuan #3 diubah menjadi kesiapan analitik; non-goal rekap ditambahkan; Tahap 11 (endpoint rekap) dihapus → tahapan menjadi 13; keputusan terbuka "rekap on-demand vs snapshot" dihapus; rancangan awal dipindah ke §19 (backlog) |
| v4 | **Finalisasi**: signature interface disalin persis dari `*excel.Store` (nama parameter `numbers`, `fromNo/toNo`, `patch`) + assert kompilasi; `internal/excel` dikunci sebagai **parser**; `public.user_workbooks` dikunci **arsip**; ditambah §10 Strategi Transaksi & Konkurensi, §11 Peta Dampak File, §12 Kriteria Go/No-Go; penomoran bab disusun ulang (§1–§19) |

---

## 17. Checklist Audit Internal Plan

Setiap klaim teknis plan ini telah diverifikasi terhadap kode aktual:

| Klaim | Metode verifikasi | Hasil |
|---|---|---|
| Jumlah baris Bank / OOB / Stress | parse `workbook.xlsx` via XML | **210 / 52 / 15** ✅ |
| Jumlah lampiran | `workbook.metrics.json` + cek disk | **118 path, 118 file ada** ✅ |
| Method publik `*excel.Store` | `grep -cP '^func \(s \*Store\) [A-Z]'` | **28** ✅ |
| Method yang dipakai `runner`+`web` | grep pemanggilan + dedupe | **25** ✅ |
| Interface plan mencakup semua 25 method | diff set | **lengkap** ✅ |
| `runner` tidak nil-check `store` | `grep 'r.store == nil'` | **0 hasil** ✅ |
| `web.tenantStore` tidak pernah nil | baca fungsi | **fallback `NewEmptyStore()`** ✅ |
| `MediaDir`/`FilePath` tidak dipakai di luar paket | grep seluruh repo | **0 hasil** ✅ |
| `ExportBytes` signature | baca definisi | `([]byte, string, error)` ✅ |
| `ReplaceFromBytes` menulis file | baca body | `os.WriteFile` + `os.Rename` ✅ |
| Test lama construct `excel.NewStore` | grep `_test.go` | **4 titik** ✅ |
| Signature 25 method interface | grep definisi aktual | **cocok persis** (termasuk nama param) ✅ |
| Nama parameter interface | bandingkan satu per satu | `numbers`, `fromNo/toNo`, `patch`, `items`, `uploaded` ✅ |
| Fence markdown plan | hitung ` ``` ` | **seimbang (genap)** ✅ |
| Rujukan silang bab | grep `§N` | **konsisten setelah renumber** ✅ |

**Koreksi v2 terhadap v1:** jumlah method (18 → 25), deskripsi temuan B
(`tenantStore` diklaim bisa nil — faktanya tidak), dan daftar method yang
dikeluarkan (`ReplaceWorkbook` ikut dikeluarkan).

---

## 18. Standar Verifikasi Tim (Definition of Quality)

Sebuah tahap dinyatakan **SELESAI** hanya jika seluruh poin berikut terbukti:

1. **Kompilasi & statik**: `go build ./...` dan `go vet ./...` exit 0.
2. **Test regresi**: `go test -count=1 ./...` hijau, termasuk paket yang
   **tidak** disentuh (bukti tidak ada efek samping).
3. **Race**: `go test -race` pada paket yang diubah.
4. **Frontend**: `node --check` pada setiap file JS yang diubah.
5. **Data integrity**: `sha256sum -c` manifest baseline — workbook & env
   identik dengan sebelum pekerjaan dimulai.
6. **Schema guard**: `\dt` pada ketiga schema sesuai ekspektasi plan.
7. **Isolasi tenant**: setiap query baru memiliki `WHERE user_id = $1`
   (dapat diverifikasi dengan grep review).
8. **Rollback**: dengan `WORKBENCH_DB=0`, seluruh test lama tetap hijau.
9. **Laporan**: hasil build/vet/test ditempel **apa adanya**, termasuk yang gagal.
10. **Tidak ada test yang diperbaiki diam-diam**: kegagalan di luar scope
    dilaporkan, bukan di-workaround.

---

## 19. Backlog: Rekap / Dashboard (DITUNDA, bukan instruksi kerja)

> **Jangan dikerjakan** sampai ada instruksi eksplisit. Isi di bawah disimpan
> hanya agar rancangan tidak hilang dan agar syarat datanya terdokumentasi.

### 19.1 Syarat yang sudah terpenuhi oleh plan ini

Semua rekap di bawah hanya butuh data yang **sudah** akan ada setelah Tahap 1–13:

| Data | Tabel | Status |
|---|---|---|
| Baris Bank + status uji + OPD | `workbench.bank_rows` | dibuat Tahap 2, diisi Tahap 6 |
| Baris OOB + status uji | `workbench.out_of_bank_rows` | idem |
| Baris Stress + status uji | `workbench.stress_test_rows` | idem |
| Sesi Live Chat + tiket + status | `livechat.sessions` | sudah ada hari ini |
| Nama/username tester | `public.users` | sudah ada hari ini |

Tidak ada DDL tambahan yang dibutuhkan untuk rekap sederhana (`GROUP BY`).

### 19.2 Daftar pekerjaan bila dilanjutkan

| # | Item | Perkiraan |
|---|---|---|
| 1 | `GET /api/dashboard/rekap` (data sendiri; admin boleh lihat semua) | endpoint + query agregat + test |
| 2 | `GET /api/admin/dashboard-global` (role `admin` saja) | endpoint + query lintas user + test isolasi role |
| 3 | Toggle UI di tab Dashboard & Ringkasan OPD | `public/app.js` + `public/index.html` + test `node --check` |
| 4 | (Opsional) Snapshot harian `workbench.daily_stats` | **hanya jika** query on-demand terbukti lambat |

### 19.3 Contoh query (referensi, bukan target rilis)

```sql
-- Akurasi per OPD seluruh tester
SELECT opd,
       COUNT(*)                                        AS total,
       COUNT(*) FILTER (WHERE result_status = 'Cocok') AS cocok,
       COUNT(*) FILTER (WHERE result_status = 'Tidak') AS tidak,
       COUNT(*) FILTER (WHERE result_status = '')      AS belum,
       ROUND(100.0 * COUNT(*) FILTER (WHERE result_status = 'Cocok')
             / NULLIF(COUNT(*) FILTER (WHERE result_status IN ('Cocok','Tidak')), 0), 1)
         AS akurasi_pct
FROM workbench.bank_rows
GROUP BY opd
ORDER BY akurasi_pct ASC NULLS LAST;

-- Progres per tester (termasuk Live Chat)
SELECT u.id, u.username, u.full_name,
       COUNT(DISTINCT b.id)                                      AS bank_rows,
       COUNT(DISTINCT b.id) FILTER (WHERE b.result_status <> '')  AS bank_tested,
       COUNT(DISTINCT o.id)                                      AS oob_rows,
       COUNT(DISTINCT s.id)                                      AS stress_rows,
       COUNT(DISTINCT l.id)                                      AS livechat_sessions
FROM public.users u
LEFT JOIN workbench.bank_rows        b ON b.user_id = u.id
LEFT JOIN workbench.out_of_bank_rows o ON o.user_id = u.id
LEFT JOIN workbench.stress_test_rows s ON s.user_id = u.id
LEFT JOIN livechat.sessions          l ON l.user_id = u.id
GROUP BY u.id, u.username, u.full_name
ORDER BY bank_tested DESC;
```

### 19.4 Catatan desain (dipertahankan dari v2)

- Rekap personal **tidak boleh** menggantikan tab Dashboard yang ada; ia hanya
  menambah sumber data baru.
- Endpoint global wajib `role = 'admin'`; menolak `tester` dengan **403**.
- Tidak ada angka yang dihitung ulang di Go bila SQL bisa menghitungnya —
  hindari selisih antara tampilan dan database.

---

**Status: SELESAI DIEKSEKUSI (v4) — Tahap 1 sampai 13.**

Ringkasan eksekusi:

| Tahap | Hasil |
|---|---|
| 1 | Backup & baseline di `data/backups_secure/workbench_pre_20261121_115748/` |
| 2 | Schema `workbench` (4 tabel) terpasang; public 5, livechat 4 tidak berubah |
| 3 | Parser murni `excel.ParseBytes/ParseFile`; sidik jari parse identik |
| 4 | Interface `workbench.Store` (25 method) + assert kompilasi |
| 5 | ETL dry-run: 210 / 52 / 15 / 118 media |
| 6 | ETL apply: PARITY OK, 0 perbedaan baris-per-baris |
| 7 | `DBStore` + parity API pada data produksi (baris & summary identik) |
| 8 | Generator Excel in-memory: 0 baris berbeda, 118 gambar ter-embed |
| 9 | Template runtime 19 MB -> 59 KB; formula Ringkasan terjaga |
| 10 | Wiring runner/web/main di balik flag `WORKBENCH_DB` |
| 11 | Ringkasan Live Chat dari SQL, tanpa file |
| 12 | Cut-over: default database, `livechat.xlsx` berhenti ditulis |
| 13 | Verifikasi akhir G1–G8 di `stage13_final_verification.txt` |

Status default: `WORKBENCH_DB=1` (data di PostgreSQL). Rollback: set `0`.

Bukti verifikasi akhir tersimpan di
`data/backups_secure/workbench_pre_20261121_115748/stage13_final_verification.txt`.
