package workbench

import (
	"bytes"
	"context"
	"fmt"
	// excelize.AddPictureFromBytes memakai image.DecodeConfig, yang hanya
	// mengenal format yang decoder-nya terdaftar. Tanpa import blank ini,
	// PNG/JPEG gagal dengan "image: unknown format" (ditemukan di Tahap 8).
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xuri/excelize/v2"

	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
)

// ExportConfig mengatur sumber template dan lokasi pencarian lampiran.
type ExportConfig struct {
	// TemplatePath adalah workbook dasar (styling + sheet Petunjuk/Ringkasan).
	// Kosong → pakai TemplatePathDefault.
	TemplatePath string
	// MediaRoots adalah direktori pencarian file lampiran berdasarkan nama file.
	// Path metrics berbentuk "/media/x.png" atau "/media/{user}/x.png"; keduanya
	// dicocokkan lewat filepath.Base.
	MediaRoots []string
}

// TemplatePathDefault adalah lokasi template runtime (hasil seeder Tahap 9).
const TemplatePathDefault = "data/templates/workbook_template.xlsx"

// exportMu membatasi jumlah generate bersamaan per user agar RAM tidak
// meledak saat beberapa tombol download ditekan serentak.
var exportMu sync.Mutex

// SetExportConfig mengatur konfigurasi generator. Aman dipanggil setelah
// NewDBStore; nilai default dipakai bila tidak dipanggil.
func (s *DBStore) SetExportConfig(cfg ExportConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exportCfg = cfg
}

// ExportBytes menghasilkan workbook .xlsx di memori dari isi PostgreSQL.
// File tidak pernah ditulis ke disk atau database.
func (s *DBStore) exportWorkbook() ([]byte, string, error) {
	ctx := context.Background()

	exportMu.Lock()
	defer exportMu.Unlock()

	s.mu.Lock()
	cfg := s.exportCfg
	s.mu.Unlock()

	tplPath := cfg.TemplatePath
	if tplPath == "" {
		tplPath = TemplatePathDefault
	}
	roots := cfg.MediaRoots
	if len(roots) == 0 {
		roots = []string{
			filepath.Join("data", "users", fmt.Sprintf("%d", s.userID), "downloads"),
			filepath.Join("data", "downloads"),
		}
	}

	f, err := openTemplate(tplPath)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	bank, err := s.loadBank(ctx)
	if err != nil {
		return nil, "", err
	}
	oob, err := s.loadOOB(ctx)
	if err != nil {
		return nil, "", err
	}
	stress, err := s.loadStress(ctx)
	if err != nil {
		return nil, "", err
	}
	_, fileName := s.meta(ctx)

	index := buildMediaIndex(roots)
	var missing []string
	missingSeen := map[string]bool{}
	note := func(p string) {
		if !missingSeen[p] {
			missingSeen[p] = true
			missing = append(missing, p)
		}
	}

	if err := fillBankSheet(f, bank, index, note); err != nil {
		return nil, "", err
	}
	if err := fillOOBSheet(f, oob, index, note); err != nil {
		return nil, "", err
	}
	if err := fillStressSheet(f, stress, index, note); err != nil {
		return nil, "", err
	}
	f.SetActiveSheet(0)

	if len(missing) > 0 {
		// Path hilang tidak menggagalkan download (sesuai plan §6), tetapi
		// harus terlihat: tulis ke stderr agar operator tahu.
		fmt.Fprintf(os.Stderr, "workbench export: %d lampiran tidak ditemukan: %s\n",
			len(missing), strings.Join(missing, ", "))
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, "", fmt.Errorf("tulis workbook: %w", err)
	}
	return buf.Bytes(), fileName, nil
}

// openTemplate membuka template; bila tidak ada, bangun workbook minimal
// sehingga download tetap berhasil (tanpa styling), dengan header lengkap.
func openTemplate(path string) (*excelize.File, error) {
	if strings.TrimSpace(path) != "" {
		if _, err := os.Stat(path); err == nil {
			f, err := excelize.OpenFile(path)
			if err == nil {
				return f, nil
			}
			return nil, fmt.Errorf("template %s tidak dapat dibuka: %w", path, err)
		}
	}
	return buildMinimalWorkbook(), nil
}

// buildMinimalWorkbook membuat workbook 5 sheet dasar tanpa styling.
func buildMinimalWorkbook() *excelize.File {
	f := excelize.NewFile()
	_ = f.SetSheetName("Sheet1", excel.SheetBank)
	for _, name := range []string{excel.SheetRingkasan, excel.SheetOutOfBank, excel.SheetStress} {
		_, _ = f.NewSheet(name)
	}
	return f
}

// buildMediaIndex memetakan nama file → path disk (root pertama menang).
func buildMediaIndex(roots []string) map[string]string {
	index := map[string]string{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if _, dup := index[e.Name()]; !dup {
				index[e.Name()] = filepath.Join(root, e.Name())
			}
		}
	}
	return index
}

// sheetWriter adalah variasi nilai + sel gambar per sheet.
type sheetWriter struct {
	sheet    string
	headers  []string
	widths   map[string]float64
	mediaCol string // kolom tempat gambar ditempel, mis. "H"/"G"/"F"
}

var (
	bankSheetWriter = sheetWriter{
		sheet: excel.SheetBank,
		headers: []string{
			"No", "OPD", "Kategori", "Status Bank", "Pertanyaan Acuan",
			"Pertanyaan Diinput ke Bot", "Balasan Acuan", "Balasan Dikirim Sistem",
			"Keterangan Evaluasi", "Status Uji (Cocok/Tidak)",
		},
		widths: map[string]float64{
			"A": 6, "B": 28, "C": 22, "D": 14, "E": 36,
			"F": 40, "G": 42, "H": 42, "I": 32, "J": 16,
		},
		mediaCol: "H",
	}
	oobSheetWriter = sheetWriter{
		sheet: excel.SheetOutOfBank,
		headers: []string{
			"No", "Sumber / OPD Acuan", "Jenis Skenario Uji",
			"Pertanyaan Acuan di Bank (Data Asli)",
			"Pertanyaan Diinput ke Bot (Diluar Bank)",
			"Perilaku Bot yang Diharapkan", "Balasan Dikirim Sistem",
			"Detail Ritme Manusia (Anti-Spam)", "Keterangan Evaluasi",
			"Status Uji (Cocok/Tidak)", "Tahun Uji Diluar Data", "Tahun Acuan Bank",
		},
		widths: map[string]float64{
			"A": 6, "B": 28, "C": 24, "D": 36, "E": 38, "F": 38,
			"G": 42, "H": 34, "I": 30, "J": 16, "K": 14, "L": 14,
		},
		mediaCol: "G",
	}
	stressSheetWriter = sheetWriter{
		sheet: excel.SheetStress,
		headers: []string{
			"No", "Kategori Stress Test", "Pertanyaan Chat Warga (Diinput ke Bot)",
			"Maksud / Inti Jebakan Penguji",
			"Perilaku Bot yang Diharapkan (Standar Ketahanan AI)",
			"Balasan Dikirim Sistem (WA)", "Detail Ritme Manusia (Anti-Spam)",
			"Keterangan Evaluasi Ketahanan AI", "Status Uji (Cocok/Tidak)",
			"Skor Kesantunan & Logika (1-5)", "Tingkat Halusinasi",
		},
		widths: map[string]float64{
			"A": 6, "B": 24, "C": 40, "D": 32, "E": 38, "F": 42,
			"G": 34, "H": 32, "I": 16, "J": 14, "K": 16,
		},
		mediaCol: "F",
	}
)

// ensureSheet membuat sheet bila template tidak memilikinya, lalu menulis
// header di baris 2 (menyamai layout file asli) dan lebar kolom.
func ensureSheet(f *excelize.File, w sheetWriter) error {
	if idx, err := f.GetSheetIndex(w.sheet); err != nil || idx < 0 {
		if _, err := f.NewSheet(w.sheet); err != nil {
			return fmt.Errorf("buat sheet %s: %w", w.sheet, err)
		}
	}
	for i, h := range w.headers {
		name, _ := excelize.ColumnNumberToName(i + 1)
		if err := f.SetCellStr(w.sheet, fmt.Sprintf("%s2", name), h); err != nil {
			return err
		}
	}
	for col, width := range w.widths {
		_ = f.SetColWidth(w.sheet, col, col, width)
	}
	return nil
}

// clearDataRows menghapus baris data (mulai baris 3) agar generate ulang dari
// template lama tidak menyisakan baris basi.
func clearDataRows(f *excelize.File, sheet string) error {
	rows, err := f.GetRows(sheet)
	if err != nil {
		return nil // sheet kosong
	}
	if len(rows) < 3 {
		return nil
	}
	// excelize.RemoveRow menghapus satu baris per panggilan; hapus dari
	// belakang agar indeks tidak bergeser.
	for i := len(rows); i >= 3; i-- {
		if err := f.RemoveRow(sheet, i); err != nil {
			return fmt.Errorf("bersihkan sheet %s baris %d: %w", sheet, i, err)
		}
	}
	return nil
}

func fillBankSheet(f *excelize.File, rows []model.TestRow, media map[string]string, note func(string)) error {
	w := bankSheetWriter
	if err := ensureSheet(f, w); err != nil {
		return err
	}
	if err := clearDataRows(f, w.sheet); err != nil {
		return err
	}
	for i, r := range rows {
		rowNum := i + 3
		vals := []any{
			r.No, r.OPD, r.Kategori, r.StatusBank, r.QuestionRef,
			r.QuestionInput, r.ExpectedReply, r.ActualReply, r.Notes, r.ResultStatus,
		}
		if err := setRowValues(f, w.sheet, rowNum, vals); err != nil {
			return err
		}
		embedMedia(f, w.sheet, fmt.Sprintf("%s%d", w.mediaCol, rowNum), rowNum, r.MediaFiles, media, note)
	}
	return nil
}

func fillOOBSheet(f *excelize.File, rows []model.OutOfBankRow, media map[string]string, note func(string)) error {
	w := oobSheetWriter
	if err := ensureSheet(f, w); err != nil {
		return err
	}
	if err := clearDataRows(f, w.sheet); err != nil {
		return err
	}
	for i, r := range rows {
		rowNum := i + 3
		vals := []any{
			r.No, r.SourceRef, r.ScenarioType, r.BaseQuestion, r.ProbeQuestion,
			r.ExpectedBehavior, r.ActualReply, r.HumanSummary, r.Notes,
			r.ResultStatus, r.ForbiddenYear, r.ReferenceYear,
		}
		if err := setRowValues(f, w.sheet, rowNum, vals); err != nil {
			return err
		}
		embedMedia(f, w.sheet, fmt.Sprintf("%s%d", w.mediaCol, rowNum), rowNum, r.MediaFiles, media, note)
	}
	return nil
}

func fillStressSheet(f *excelize.File, rows []model.StressTestRow, media map[string]string, note func(string)) error {
	w := stressSheetWriter
	if err := ensureSheet(f, w); err != nil {
		return err
	}
	if err := clearDataRows(f, w.sheet); err != nil {
		return err
	}
	for i, r := range rows {
		rowNum := i + 3
		vals := []any{
			r.No, r.StressCategory, r.UserPromptQ, r.IntentPurpose,
			r.ExpectedBehavior, r.ActualReply, r.HumanSummary, r.Notes,
			r.ResultStatus, r.PolitenessScore, r.HallucinationLvl,
		}
		if err := setRowValues(f, w.sheet, rowNum, vals); err != nil {
			return err
		}
		embedMedia(f, w.sheet, fmt.Sprintf("%s%d", w.mediaCol, rowNum), rowNum, r.MediaFiles, media, note)
	}
	return nil
}

func setRowValues(f *excelize.File, sheet string, rowNum int, vals []any) error {
	for i, v := range vals {
		name, _ := excelize.ColumnNumberToName(i + 1)
		if err := f.SetCellValue(sheet, fmt.Sprintf("%s%d", name, rowNum), v); err != nil {
			return err
		}
	}
	return nil
}

// embedMedia menempelkan gambar ke cell. Path yang tidak ditemukan dicatat
// lewat note (tidak menggagalkan export).
func embedMedia(f *excelize.File, sheet, cell string, rowNum int, paths []string, media map[string]string, note func(string)) {
	if len(paths) == 0 {
		return
	}
	enable := true
	placed := 0
	for _, rel := range paths {
		disk, ok := media[filepath.Base(rel)]
		if !ok {
			note(rel)
			continue
		}
		raw, err := os.ReadFile(disk)
		if err != nil || len(raw) == 0 {
			note(rel)
			continue
		}
		ext := strings.ToLower(filepath.Ext(disk))
		if ext == "" {
			ext = ".png"
		}
		if err := f.AddPictureFromBytes(sheet, cell, &excelize.Picture{
			Extension: ext,
			File:      raw,
			Format: &excelize.GraphicOptions{
				AltText:     "Lampiran Balasan WA: " + filepath.Base(rel),
				PrintObject: &enable,
				ScaleX:      0.28,
				ScaleY:      0.28,
				OffsetX:     8 + (placed * 95),
				OffsetY:     85,
				Positioning: "oneCell",
			},
		}); err != nil {
			// File ada tapi bukan gambar valid (atau tipe tak dikenali):
			// laporkan lewat note, jangan diam-diam dilewati.
			note(rel + " (gambar tidak valid: " + err.Error() + ")")
			continue
		}
		placed++
	}
	if placed > 0 {
		_ = f.SetRowHeight(sheet, rowNum, 165)
	}
}
