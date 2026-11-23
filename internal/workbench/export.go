package workbench

import (
	"bytes"
	"context"
	"fmt"
	"image"
	// excelize.AddPictureFromBytes memakai image.DecodeConfig, yang hanya
	// mengenal format yang decoder-nya terdaftar. Tanpa import blank ini,
	// PNG/JPEG gagal dengan "image: unknown format" (ditemukan di Tahap 8).
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xuri/excelize/v2"

	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/livechat"
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

// SheetDashboardOPD adalah nama sheet dashboard ringkasan OPD.
const SheetDashboardOPD = "Ringkasan OPD"

// ExportFilter menentukan kriteria penyaringan saat mengunduh Excel.
type ExportFilter struct {
	Sheets       []string  // "bank", "oob", "stress", "livechat", "summary"
	Category     string    // mode live chat ("benar", "typo", "ngasal", "celah") atau kategori OPD
	ResultStatus string    // "Cocok", "Tidak", "BelumDiuji", "finished", "failed", "timeout", "stopped"
	DateFrom     time.Time // Rentang tanggal awal (00:00:00)
	DateTo       time.Time // Rentang tanggal akhir (23:59:59)
}

func (f ExportFilter) IncludesSheet(name string) bool {
	if len(f.Sheets) == 0 {
		return true
	}
	name = strings.ToLower(strings.TrimSpace(name))
	for _, s := range f.Sheets {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "all" || s == "" {
			return true
		}
		if s == name {
			return true
		}
		if s == "bank" && (name == "bank" || name == strings.ToLower(excel.SheetBank)) {
			return true
		}
		if (s == "livechat" || s == "pengaduan") && (name == "livechat" || name == "pengaduan" || name == strings.ToLower(livechat.SummarySheet)) {
			return true
		}
		if (s == "oob" || s == "diluar_bank" || s == "diluarbank") && (name == "oob" || name == strings.ToLower(excel.SheetOutOfBank)) {
			return true
		}
		if s == "stress" && (name == "stress" || name == strings.ToLower(excel.SheetStress)) {
			return true
		}
		if (s == "summary" || s == "ringkasan") && (name == "summary" || name == strings.ToLower(excel.SheetRingkasan) || name == "petunjuk") {
			return true
		}
	}
	return false
}

func (f ExportFilter) IsActive() bool {
	return len(f.Sheets) > 0 || (f.Category != "" && f.Category != "all") || (f.ResultStatus != "" && f.ResultStatus != "all") || !f.DateFrom.IsZero() || !f.DateTo.IsZero()
}

// FilteredExporter mendefinisikan kemampuan store yang mendukung ekspor terfilter.
type FilteredExporter interface {
	ExportBytesFiltered(filter ExportFilter) ([]byte, string, error)
}

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

func filterBankRows(rows []model.TestRow, filter ExportFilter) []model.TestRow {
	if len(rows) == 0 {
		return rows
	}
	var out []model.TestRow
	for _, r := range rows {
		if filter.Category != "" && filter.Category != "all" {
			if !strings.EqualFold(r.Kategori, filter.Category) && !strings.EqualFold(r.OPD, filter.Category) {
				continue
			}
		}
		if filter.ResultStatus != "" && filter.ResultStatus != "all" {
			if filter.ResultStatus == "BelumDiuji" {
				if r.ResultStatus != "" {
					continue
				}
			} else if !strings.EqualFold(r.ResultStatus, filter.ResultStatus) {
				continue
			}
		}
		if !filter.DateFrom.IsZero() {
			if r.Metrics.TestedAt == 0 {
				continue
			}
			t := time.UnixMilli(r.Metrics.TestedAt)
			if t.Before(filter.DateFrom) {
				continue
			}
		}
		if !filter.DateTo.IsZero() {
			if r.Metrics.TestedAt == 0 {
				continue
			}
			t := time.UnixMilli(r.Metrics.TestedAt)
			if t.After(filter.DateTo) {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

func filterOOBRows(rows []model.OutOfBankRow, filter ExportFilter) []model.OutOfBankRow {
	if len(rows) == 0 {
		return rows
	}
	var out []model.OutOfBankRow
	for _, r := range rows {
		if filter.Category != "" && filter.Category != "all" {
			if !strings.EqualFold(r.ScenarioType, filter.Category) && !strings.EqualFold(r.SourceRef, filter.Category) {
				continue
			}
		}
		if filter.ResultStatus != "" && filter.ResultStatus != "all" {
			if filter.ResultStatus == "BelumDiuji" {
				if r.ResultStatus != "" {
					continue
				}
			} else if !strings.EqualFold(r.ResultStatus, filter.ResultStatus) {
				continue
			}
		}
		if !filter.DateFrom.IsZero() {
			if r.Metrics.TestedAt == 0 {
				continue
			}
			t := time.UnixMilli(r.Metrics.TestedAt)
			if t.Before(filter.DateFrom) {
				continue
			}
		}
		if !filter.DateTo.IsZero() {
			if r.Metrics.TestedAt == 0 {
				continue
			}
			t := time.UnixMilli(r.Metrics.TestedAt)
			if t.After(filter.DateTo) {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

func filterStressRows(rows []model.StressTestRow, filter ExportFilter) []model.StressTestRow {
	if len(rows) == 0 {
		return rows
	}
	var out []model.StressTestRow
	for _, r := range rows {
		if filter.Category != "" && filter.Category != "all" {
			if !strings.EqualFold(r.StressCategory, filter.Category) {
				continue
			}
		}
		if filter.ResultStatus != "" && filter.ResultStatus != "all" {
			if filter.ResultStatus == "BelumDiuji" {
				if r.ResultStatus != "" {
					continue
				}
			} else if !strings.EqualFold(r.ResultStatus, filter.ResultStatus) {
				continue
			}
		}
		if !filter.DateFrom.IsZero() {
			if r.Metrics.TestedAt == 0 {
				continue
			}
			t := time.UnixMilli(r.Metrics.TestedAt)
			if t.Before(filter.DateFrom) {
				continue
			}
		}
		if !filter.DateTo.IsZero() {
			if r.Metrics.TestedAt == 0 {
				continue
			}
			t := time.UnixMilli(r.Metrics.TestedAt)
			if t.After(filter.DateTo) {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// ExportBytes menghasilkan workbook .xlsx di memori dari isi PostgreSQL.
// File tidak pernah ditulis ke disk atau database.
func (s *DBStore) exportWorkbook(flt ...ExportFilter) ([]byte, string, error) {
	ctx := context.Background()

	var filter ExportFilter
	if len(flt) > 0 {
		filter = flt[0]
	}

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
	opdOrder, fileName := s.meta(ctx)

	index := buildMediaIndex(roots)
	var missing []string
	missingSeen := map[string]bool{}
	note := func(p string) {
		if !missingSeen[p] {
			missingSeen[p] = true
			missing = append(missing, p)
		}
	}

	if filter.IncludesSheet("bank") {
		filteredBank := filterBankRows(bank, filter)
		if err := fillBankSheet(f, filteredBank, index, note); err != nil {
			return nil, "", err
		}
	} else {
		_ = f.DeleteSheet(excel.SheetBank)
	}

	if filter.IncludesSheet("oob") {
		filteredOOB := filterOOBRows(oob, filter)
		if err := fillOOBSheet(f, filteredOOB, index, note); err != nil {
			return nil, "", err
		}
	} else {
		_ = f.DeleteSheet(excel.SheetOutOfBank)
	}

	if filter.IncludesSheet("stress") {
		filteredStress := filterStressRows(stress, filter)
		if err := fillStressSheet(f, filteredStress, index, note); err != nil {
			return nil, "", err
		}
	} else {
		_ = f.DeleteSheet(excel.SheetStress)
	}

	// Ringkasan Live Chat / Pengaduan (All-in-One Workbook)
	if filter.IncludesSheet("livechat") {
		if s.db != nil {
			lcStore := livechat.NewStore(s.db)
			_ = livechat.FillSummarySheet(ctx, f, lcStore, s.userID, livechat.LiveChatFilter{
				CategoryMode: filter.Category,
				Status:       filter.ResultStatus,
				DateFrom:     filter.DateFrom,
				DateTo:       filter.DateTo,
			})
		}
	} else {
		_ = f.DeleteSheet(livechat.SummarySheet)
	}

	// Dashboard Ringkasan OPD (Tabel + KPI Cards + Grafik Interaktif)
	if filter.IncludesSheet("summary") {
		_ = fillDashboardOPDSheet(f, bank, opdOrder, fileName)
	} else if len(filter.Sheets) > 0 {
		_ = f.DeleteSheet(SheetDashboardOPD)
		_ = f.DeleteSheet(excel.SheetRingkasan)
		_ = f.DeleteSheet("Petunjuk")
	}

	if idx, err := f.GetSheetIndex(SheetDashboardOPD); err == nil && idx >= 0 {
		f.SetActiveSheet(idx)
	} else {
		f.SetActiveSheet(0)
	}

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

// buildMinimalWorkbook membuat workbook 6 sheet dasar tanpa styling.
func buildMinimalWorkbook() *excelize.File {
	f := excelize.NewFile()
	_ = f.SetSheetName("Sheet1", SheetDashboardOPD)
	for _, name := range []string{excel.SheetBank, excel.SheetOutOfBank, excel.SheetStress, livechat.SummarySheet} {
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
	mediaCol string // kolom tempat gambar ditempel
}

type modernStyles struct {
	header   int
	cocok    int
	tidak    int
	neutral  int
	wrapText int
	center   int
	top      int
}

func initModernStyles(f *excelize.File) modernStyles {
	var ms modernStyles
	ms.header, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#FFFFFF", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#1E3A8A"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border: []excelize.Border{
			{Type: "top", Color: "#0F172A", Style: 1},
			{Type: "bottom", Color: "#0F172A", Style: 2},
			{Type: "left", Color: "#334155", Style: 1},
			{Type: "right", Color: "#334155", Style: 1},
		},
	})
	ms.cocok, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#166534", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#DCFCE7"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#BBF7D0", Style: 1},
			{Type: "bottom", Color: "#BBF7D0", Style: 1},
			{Type: "left", Color: "#BBF7D0", Style: 1},
			{Type: "right", Color: "#BBF7D0", Style: 1},
		},
	})
	ms.tidak, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#991B1B", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FEE2E2"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#FECACA", Style: 1},
			{Type: "bottom", Color: "#FECACA", Style: 1},
			{Type: "left", Color: "#FECACA", Style: 1},
			{Type: "right", Color: "#FECACA", Style: 1},
		},
	})
	ms.neutral, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: false, Color: "#64748B", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#F8FAFC"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
		},
	})
	ms.wrapText, _ = f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	})
	ms.center, _ = f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	ms.top, _ = f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "top"},
	})
	return ms
}

var (
	bankSheetWriter = sheetWriter{
		sheet: excel.SheetBank,
		headers: []string{
			"No", "Status Uji (Cocok/Tidak)", "Keterangan Evaluasi", "OPD", "Kategori",
			"Status Bank", "Pertanyaan Acuan", "Pertanyaan Diinput ke Bot",
			"Balasan Acuan", "Balasan Dikirim Sistem",
			"Waktu Chat", "WA Pengirim", "WA Target",
		},
		widths: map[string]float64{
			"A": 6, "B": 18, "C": 34, "D": 28, "E": 22,
			"F": 14, "G": 36, "H": 40, "I": 42, "J": 50,
			"K": 20, "L": 18, "M": 18,
		},
		mediaCol: "J",
	}
	oobSheetWriter = sheetWriter{
		sheet: excel.SheetOutOfBank,
		headers: []string{
			"No", "Status Uji (Cocok/Tidak)", "Keterangan Evaluasi", "Sumber / OPD Acuan",
			"Jenis Skenario Uji", "Pertanyaan Acuan di Bank (Data Asli)",
			"Pertanyaan Diinput ke Bot (Diluar Bank)", "Perilaku Bot yang Diharapkan",
			"Balasan Dikirim Sistem", "Detail Ritme Manusia (Anti-Spam)",
			"Tahun Uji Diluar Data", "Tahun Acuan Bank",
			"Waktu Chat", "WA Pengirim", "WA Target",
		},
		widths: map[string]float64{
			"A": 6, "B": 18, "C": 34, "D": 28, "E": 24,
			"F": 36, "G": 38, "H": 38, "I": 50, "J": 30,
			"K": 14, "L": 14, "M": 20, "N": 18, "O": 18,
		},
		mediaCol: "I",
	}
	stressSheetWriter = sheetWriter{
		sheet: excel.SheetStress,
		headers: []string{
			"No", "Status Uji (Cocok/Tidak)", "Keterangan Evaluasi Ketahanan AI",
			"Kategori Stress Test", "Pertanyaan Chat Warga (Diinput ke Bot)",
			"Maksud / Inti Jebakan Penguji", "Perilaku Bot yang Diharapkan (Standar Ketahanan AI)",
			"Balasan Dikirim Sistem (WA)", "Detail Ritme Manusia (Anti-Spam)",
			"Skor Kesantunan & Logika (1-5)", "Tingkat Halusinasi",
			"Waktu Chat", "WA Pengirim", "WA Target",
		},
		widths: map[string]float64{
			"A": 6, "B": 18, "C": 34, "D": 24, "E": 40,
			"F": 32, "G": 38, "H": 50, "I": 30, "J": 14,
			"K": 16, "L": 20, "M": 18, "N": 18,
		},
		mediaCol: "H",
	}
)

func formatExportTestedAt(testedAt int64) string {
	if testedAt <= 0 {
		return ""
	}
	return time.UnixMilli(testedAt).Format("2006-01-02 15:04:05")
}

func formatExportPhone(phone string) string {
	phone = strings.TrimSpace(phone)
	if idx := strings.IndexByte(phone, '@'); idx > 0 {
		phone = phone[:idx]
	}
	return phone
}

func exportSenderPhone(m model.RowHumanMetrics) string {
	if m.SenderPhone != "" {
		return formatExportPhone(m.SenderPhone)
	}
	if m.TestedByDevice != nil && m.TestedByDevice.PhoneNumber != "" {
		return formatExportPhone(m.TestedByDevice.PhoneNumber)
	}
	return ""
}

// ensureSheet membuat sheet bila template tidak memilikinya, lalu menulis
// header di baris 2 (menyamai layout file asli) dan lebar kolom.
func ensureSheet(f *excelize.File, w sheetWriter, st modernStyles) error {
	if idx, err := f.GetSheetIndex(w.sheet); err != nil || idx < 0 {
		if _, err := f.NewSheet(w.sheet); err != nil {
			return fmt.Errorf("buat sheet %s: %w", w.sheet, err)
		}
	}
	_ = f.SetRowHeight(w.sheet, 2, 28)
	for i, h := range w.headers {
		name, _ := excelize.ColumnNumberToName(i + 1)
		cell := fmt.Sprintf("%s2", name)
		if err := f.SetCellStr(w.sheet, cell, h); err != nil {
			return err
		}
		if st.header > 0 {
			_ = f.SetCellStyle(w.sheet, cell, cell, st.header)
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
	st := initModernStyles(f)
	if err := ensureSheet(f, w, st); err != nil {
		return err
	}
	if err := clearDataRows(f, w.sheet); err != nil {
		return err
	}
	for i, r := range rows {
		rowNum := i + 3
		vals := []any{
			r.No, r.ResultStatus, r.Notes,
			r.OPD, r.Kategori, r.StatusBank, r.QuestionRef,
			r.QuestionInput, r.ExpectedReply, r.ActualReply,
			formatExportTestedAt(r.Metrics.TestedAt), exportSenderPhone(r.Metrics), formatExportPhone(r.Metrics.BotTargetPhone),
		}
		if err := setRowValues(f, w.sheet, rowNum, vals); err != nil {
			return err
		}

		_ = f.SetCellStyle(w.sheet, fmt.Sprintf("A%d", rowNum), fmt.Sprintf("A%d", rowNum), st.center)
		bCell := fmt.Sprintf("B%d", rowNum)
		if r.ResultStatus == "Cocok" {
			_ = f.SetCellStyle(w.sheet, bCell, bCell, st.cocok)
		} else if r.ResultStatus == "Tidak" {
			_ = f.SetCellStyle(w.sheet, bCell, bCell, st.tidak)
		} else {
			_ = f.SetCellStyle(w.sheet, bCell, bCell, st.neutral)
		}
		_ = f.SetCellStyle(w.sheet, fmt.Sprintf("C%d", rowNum), fmt.Sprintf("C%d", rowNum), st.wrapText)
		_ = f.SetCellStyle(w.sheet, fmt.Sprintf("J%d", rowNum), fmt.Sprintf("J%d", rowNum), st.wrapText)

		embedMedia(f, w.sheet, fmt.Sprintf("%s%d", w.mediaCol, rowNum), rowNum, r.MediaFiles, media, note)
	}
	return nil
}

func fillOOBSheet(f *excelize.File, rows []model.OutOfBankRow, media map[string]string, note func(string)) error {
	w := oobSheetWriter
	st := initModernStyles(f)
	if err := ensureSheet(f, w, st); err != nil {
		return err
	}
	if err := clearDataRows(f, w.sheet); err != nil {
		return err
	}
	for i, r := range rows {
		rowNum := i + 3
		vals := []any{
			r.No, r.ResultStatus, r.Notes,
			r.SourceRef, r.ScenarioType, r.BaseQuestion, r.ProbeQuestion,
			r.ExpectedBehavior, r.ActualReply, r.HumanSummary,
			r.ForbiddenYear, r.ReferenceYear,
			formatExportTestedAt(r.Metrics.TestedAt), exportSenderPhone(r.Metrics), formatExportPhone(r.Metrics.BotTargetPhone),
		}
		if err := setRowValues(f, w.sheet, rowNum, vals); err != nil {
			return err
		}

		_ = f.SetCellStyle(w.sheet, fmt.Sprintf("A%d", rowNum), fmt.Sprintf("A%d", rowNum), st.center)
		bCell := fmt.Sprintf("B%d", rowNum)
		if r.ResultStatus == "Cocok" {
			_ = f.SetCellStyle(w.sheet, bCell, bCell, st.cocok)
		} else if r.ResultStatus == "Tidak" {
			_ = f.SetCellStyle(w.sheet, bCell, bCell, st.tidak)
		} else {
			_ = f.SetCellStyle(w.sheet, bCell, bCell, st.neutral)
		}
		_ = f.SetCellStyle(w.sheet, fmt.Sprintf("C%d", rowNum), fmt.Sprintf("C%d", rowNum), st.wrapText)
		_ = f.SetCellStyle(w.sheet, fmt.Sprintf("I%d", rowNum), fmt.Sprintf("I%d", rowNum), st.wrapText)

		embedMedia(f, w.sheet, fmt.Sprintf("%s%d", w.mediaCol, rowNum), rowNum, r.MediaFiles, media, note)
	}
	return nil
}

func fillStressSheet(f *excelize.File, rows []model.StressTestRow, media map[string]string, note func(string)) error {
	w := stressSheetWriter
	st := initModernStyles(f)
	if err := ensureSheet(f, w, st); err != nil {
		return err
	}
	if err := clearDataRows(f, w.sheet); err != nil {
		return err
	}
	for i, r := range rows {
		rowNum := i + 3
		vals := []any{
			r.No, r.ResultStatus, r.Notes,
			r.StressCategory, r.UserPromptQ, r.IntentPurpose,
			r.ExpectedBehavior, r.ActualReply, r.HumanSummary,
			r.PolitenessScore, r.HallucinationLvl,
			formatExportTestedAt(r.Metrics.TestedAt), exportSenderPhone(r.Metrics), formatExportPhone(r.Metrics.BotTargetPhone),
		}
		if err := setRowValues(f, w.sheet, rowNum, vals); err != nil {
			return err
		}

		_ = f.SetCellStyle(w.sheet, fmt.Sprintf("A%d", rowNum), fmt.Sprintf("A%d", rowNum), st.center)
		bCell := fmt.Sprintf("B%d", rowNum)
		if r.ResultStatus == "Cocok" {
			_ = f.SetCellStyle(w.sheet, bCell, bCell, st.cocok)
		} else if r.ResultStatus == "Tidak" {
			_ = f.SetCellStyle(w.sheet, bCell, bCell, st.tidak)
		} else {
			_ = f.SetCellStyle(w.sheet, bCell, bCell, st.neutral)
		}
		_ = f.SetCellStyle(w.sheet, fmt.Sprintf("C%d", rowNum), fmt.Sprintf("C%d", rowNum), st.wrapText)
		_ = f.SetCellStyle(w.sheet, fmt.Sprintf("H%d", rowNum), fmt.Sprintf("H%d", rowNum), st.wrapText)

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

// embedMedia menempelkan gambar ke cell dengan bounding box proporsional agar tidak menutupi teks.
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

		// Calculate proportional scaling within max bounding box (160x105)
		scale := 0.20
		imgCfg, _, decErr := image.DecodeConfig(bytes.NewReader(raw))
		if decErr == nil && imgCfg.Width > 0 && imgCfg.Height > 0 {
			const maxW = 160.0
			const maxH = 105.0
			scaleW := maxW / float64(imgCfg.Width)
			scaleH := maxH / float64(imgCfg.Height)
			scale = math.Min(scaleW, scaleH)
			if scale > 0.35 {
				scale = 0.35
			}
		}

		if err := f.AddPictureFromBytes(sheet, cell, &excelize.Picture{
			Extension: ext,
			File:      raw,
			Format: &excelize.GraphicOptions{
				AltText:     "Lampiran Balasan WA: " + filepath.Base(rel),
				PrintObject: &enable,
				ScaleX:      scale,
				ScaleY:      scale,
				OffsetX:     10 + (placed * 170),
				OffsetY:     70, // places thumbnail cleanly below text
				Positioning: "oneCell",
			},
		}); err != nil {
			note(rel + " (gambar tidak valid: " + err.Error() + ")")
			continue
		}
		placed++
	}
	if placed > 0 {
		_ = f.SetRowHeight(sheet, rowNum, 190)
	}
}

// fillDashboardOPDSheet membangun dashboard eksekutif lengkap dengan:
// 1. Banner Judul & KPI Cards (Total Soal, Cocok, Tidak, Tingkat Akurasi)
// 2. Tabel Rekapitulasi per OPD
// 3. Grafik Batang Interaktif (Clustered Column Chart: Cocok vs Tidak per OPD)
func fillDashboardOPDSheet(f *excelize.File, rows []model.TestRow, opdOrder []string, fileName string) error {
	sheet := SheetDashboardOPD
	idx, err := f.GetSheetIndex(sheet)
	if err != nil || idx < 0 {
		if oldIdx, _ := f.GetSheetIndex(excel.SheetRingkasan); oldIdx >= 0 {
			_ = f.SetSheetName(excel.SheetRingkasan, sheet)
		} else {
			_, _ = f.NewSheet(sheet)
		}
	}

	_ = clearSheetData(f, sheet)

	sum := buildBankSummary(rows, opdOrder, fileName)

	stTitle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#FFFFFF", Size: 13},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#1E3A8A"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	stSub, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: false, Color: "#475569", Size: 10},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#F1F5F9"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	stKpiTotal, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#1E40AF", Size: 12},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#DBEAFE"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border: []excelize.Border{
			{Type: "top", Color: "#93C5FD", Style: 1},
			{Type: "bottom", Color: "#93C5FD", Style: 1},
			{Type: "left", Color: "#93C5FD", Style: 1},
			{Type: "right", Color: "#93C5FD", Style: 1},
		},
	})
	stKpiCocok, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#166534", Size: 12},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#DCFCE7"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border: []excelize.Border{
			{Type: "top", Color: "#86EFAC", Style: 1},
			{Type: "bottom", Color: "#86EFAC", Style: 1},
			{Type: "left", Color: "#86EFAC", Style: 1},
			{Type: "right", Color: "#86EFAC", Style: 1},
		},
	})
	stKpiTidak, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#991B1B", Size: 12},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FEE2E2"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border: []excelize.Border{
			{Type: "top", Color: "#FCA5A5", Style: 1},
			{Type: "bottom", Color: "#FCA5A5", Style: 1},
			{Type: "left", Color: "#FCA5A5", Style: 1},
			{Type: "right", Color: "#FCA5A5", Style: 1},
		},
	})
	stKpiAkurasi, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#065F46", Size: 12},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#D1FAE5"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border: []excelize.Border{
			{Type: "top", Color: "#6EE7B7", Style: 1},
			{Type: "bottom", Color: "#6EE7B7", Style: 1},
			{Type: "left", Color: "#6EE7B7", Style: 1},
			{Type: "right", Color: "#6EE7B7", Style: 1},
		},
	})
	stTh, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#FFFFFF", Size: 10.5},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#1E3A8A"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border: []excelize.Border{
			{Type: "top", Color: "#0F172A", Style: 1},
			{Type: "bottom", Color: "#0F172A", Style: 2},
			{Type: "left", Color: "#334155", Style: 1},
			{Type: "right", Color: "#334155", Style: 1},
		},
	})
	stCenter, _ := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
		},
	})
	stLeft, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#1E293B", Size: 10.5},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
		},
	})
	stCocokCell, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#166534"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#F0FDF4"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
		},
	})
	stTidakCell, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#991B1B"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FEF2F2"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
		},
	})
	stAkurasiHigh, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#15803D"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#DCFCE7"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
		},
	})
	stAkurasiLow, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#B91C1C"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FEE2E2"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
		},
	})
	stTotalRow, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#0F172A", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#E2E8F0"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#334155", Style: 1},
			{Type: "bottom", Color: "#0F172A", Style: 2},
			{Type: "left", Color: "#334155", Style: 1},
			{Type: "right", Color: "#334155", Style: 1},
		},
	})
	stTotalLeft, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#0F172A", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#E2E8F0"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#334155", Style: 1},
			{Type: "bottom", Color: "#0F172A", Style: 2},
			{Type: "left", Color: "#334155", Style: 1},
			{Type: "right", Color: "#334155", Style: 1},
		},
	})

	// 1. Title Baris 1
	_ = f.SetRowHeight(sheet, 1, 30)
	_ = f.MergeCell(sheet, "A1", "G1")
	_ = f.SetCellValue(sheet, "A1", "DASHBOARD PERFORMA BOT WHATSAPP PER OPD")
	_ = f.SetCellStyle(sheet, "A1", "G1", stTitle)

	// 2. Subtitle Baris 2
	_ = f.SetRowHeight(sheet, 2, 20)
	_ = f.MergeCell(sheet, "A2", "G2")
	_ = f.SetCellValue(sheet, "A2", fmt.Sprintf("Pemerintah Kota Medan — Evaluasi Ketepatan Jawaban Bot (Total: %d Soal, Diuji: %d)", sum.Total, sum.Tested))
	_ = f.SetCellStyle(sheet, "A2", "G2", stSub)

	_ = f.SetRowHeight(sheet, 3, 10) // blank spacer

	// 3. KPI Cards (Baris 4 & 5)
	_ = f.SetRowHeight(sheet, 4, 38)
	_ = f.MergeCell(sheet, "A4", "B4")
	_ = f.SetCellValue(sheet, "A4", fmt.Sprintf("TOTAL SOAL\n%d", sum.Total))
	_ = f.SetCellStyle(sheet, "A4", "B4", stKpiTotal)

	_ = f.MergeCell(sheet, "C4", "C4")
	_ = f.SetCellValue(sheet, "C4", fmt.Sprintf("COCOK (LOLOS)\n%d", sum.Cocok))
	_ = f.SetCellStyle(sheet, "C4", "C4", stKpiCocok)

	_ = f.MergeCell(sheet, "D4", "D4")
	_ = f.SetCellValue(sheet, "D4", fmt.Sprintf("TIDAK (GAGAL)\n%d", sum.Tidak))
	_ = f.SetCellStyle(sheet, "D4", "D4", stKpiTidak)

	_ = f.MergeCell(sheet, "E4", "E4")
	_ = f.SetCellValue(sheet, "E4", fmt.Sprintf("BELUM DIUJI\n%d", sum.BelumDiuji))
	_ = f.SetCellStyle(sheet, "E4", "E4", stKpiTotal)

	_ = f.MergeCell(sheet, "F4", "G4")
	_ = f.SetCellValue(sheet, "F4", fmt.Sprintf("TINGKAT AKURASI\n%.1f%%", sum.AccuracyPct))
	_ = f.SetCellStyle(sheet, "F4", "G4", stKpiAkurasi)

	_ = f.SetRowHeight(sheet, 5, 12) // blank spacer

	// 4. Table Headers (Baris 6)
	_ = f.SetRowHeight(sheet, 6, 26)
	thCols := []string{"No", "Nama OPD / Unit Layanan", "Total Soal", "Cocok (Lolos)", "Tidak (Gagal)", "Belum Diuji", "Akurasi (%)"}
	for i, h := range thCols {
		colName, _ := excelize.ColumnNumberToName(i + 1)
		cell := fmt.Sprintf("%s6", colName)
		_ = f.SetCellValue(sheet, cell, h)
		_ = f.SetCellStyle(sheet, cell, cell, stTh)
	}

	// 5. Data rows per OPD
	startRow := 7
	for i, it := range sum.ByOPD {
		rNum := startRow + i
		_ = f.SetRowHeight(sheet, rNum, 21)

		_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", rNum), i+1)
		_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", rNum), fmt.Sprintf("A%d", rNum), stCenter)

		_ = f.SetCellValue(sheet, fmt.Sprintf("B%d", rNum), it.OPD)
		_ = f.SetCellStyle(sheet, fmt.Sprintf("B%d", rNum), fmt.Sprintf("B%d", rNum), stLeft)

		_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", rNum), it.Total)
		_ = f.SetCellStyle(sheet, fmt.Sprintf("C%d", rNum), fmt.Sprintf("C%d", rNum), stCenter)

		_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", rNum), it.Cocok)
		_ = f.SetCellStyle(sheet, fmt.Sprintf("D%d", rNum), fmt.Sprintf("D%d", rNum), stCocokCell)

		_ = f.SetCellValue(sheet, fmt.Sprintf("E%d", rNum), it.Tidak)
		if it.Tidak > 0 {
			_ = f.SetCellStyle(sheet, fmt.Sprintf("E%d", rNum), fmt.Sprintf("E%d", rNum), stTidakCell)
		} else {
			_ = f.SetCellStyle(sheet, fmt.Sprintf("E%d", rNum), fmt.Sprintf("E%d", rNum), stCenter)
		}

		_ = f.SetCellValue(sheet, fmt.Sprintf("F%d", rNum), it.BelumDiuji)
		_ = f.SetCellStyle(sheet, fmt.Sprintf("F%d", rNum), fmt.Sprintf("F%d", rNum), stCenter)

		tested := it.Cocok + it.Tidak
		if tested > 0 {
			_ = f.SetCellValue(sheet, fmt.Sprintf("G%d", rNum), fmt.Sprintf("%.1f%%", it.AccuracyPct))
			if it.AccuracyPct >= 80 {
				_ = f.SetCellStyle(sheet, fmt.Sprintf("G%d", rNum), fmt.Sprintf("G%d", rNum), stAkurasiHigh)
			} else {
				_ = f.SetCellStyle(sheet, fmt.Sprintf("G%d", rNum), fmt.Sprintf("G%d", rNum), stAkurasiLow)
			}
		} else {
			_ = f.SetCellValue(sheet, fmt.Sprintf("G%d", rNum), "-")
			_ = f.SetCellStyle(sheet, fmt.Sprintf("G%d", rNum), fmt.Sprintf("G%d", rNum), stCenter)
		}
	}

	lastDataRow := startRow + len(sum.ByOPD) - 1
	totRow := lastDataRow + 1
	_ = f.SetRowHeight(sheet, totRow, 24)

	_ = f.MergeCell(sheet, fmt.Sprintf("A%d", totRow), fmt.Sprintf("B%d", totRow))
	_ = f.SetCellValue(sheet, fmt.Sprintf("A%d", totRow), "TOTAL / RATA-RATA GLOBAL")
	_ = f.SetCellStyle(sheet, fmt.Sprintf("A%d", totRow), fmt.Sprintf("B%d", totRow), stTotalLeft)

	_ = f.SetCellValue(sheet, fmt.Sprintf("C%d", totRow), sum.Total)
	_ = f.SetCellStyle(sheet, fmt.Sprintf("C%d", totRow), fmt.Sprintf("C%d", totRow), stTotalRow)

	_ = f.SetCellValue(sheet, fmt.Sprintf("D%d", totRow), sum.Cocok)
	_ = f.SetCellStyle(sheet, fmt.Sprintf("D%d", totRow), fmt.Sprintf("D%d", totRow), stTotalRow)

	_ = f.SetCellValue(sheet, fmt.Sprintf("E%d", totRow), sum.Tidak)
	_ = f.SetCellStyle(sheet, fmt.Sprintf("E%d", totRow), fmt.Sprintf("E%d", totRow), stTotalRow)

	_ = f.SetCellValue(sheet, fmt.Sprintf("F%d", totRow), sum.BelumDiuji)
	_ = f.SetCellStyle(sheet, fmt.Sprintf("F%d", totRow), fmt.Sprintf("F%d", totRow), stTotalRow)

	_ = f.SetCellValue(sheet, fmt.Sprintf("G%d", totRow), fmt.Sprintf("%.1f%%", sum.AccuracyPct))
	_ = f.SetCellStyle(sheet, fmt.Sprintf("G%d", totRow), fmt.Sprintf("G%d", totRow), stTotalRow)

	// Set Col Widths
	_ = f.SetColWidth(sheet, "A", "A", 6)
	_ = f.SetColWidth(sheet, "B", "B", 36)
	_ = f.SetColWidth(sheet, "C", "C", 13)
	_ = f.SetColWidth(sheet, "D", "D", 15)
	_ = f.SetColWidth(sheet, "E", "E", 15)
	_ = f.SetColWidth(sheet, "F", "F", 13)
	_ = f.SetColWidth(sheet, "G", "G", 15)

	// 6. Grafik Performa OPD (Clustered Column Chart)
	if len(sum.ByOPD) > 0 {
		chartCell := "I6"
		_ = f.AddChart(sheet, chartCell, &excelize.Chart{
			Type: excelize.Col,
			Series: []excelize.ChartSeries{
				{
					Name:       fmt.Sprintf("'%s'!$D$6", sheet),
					Categories: fmt.Sprintf("'%s'!$B$7:$B$%d", sheet, lastDataRow),
					Values:     fmt.Sprintf("'%s'!$D$7:$D$%d", sheet, lastDataRow),
				},
				{
					Name:       fmt.Sprintf("'%s'!$E$6", sheet),
					Categories: fmt.Sprintf("'%s'!$B$7:$B$%d", sheet, lastDataRow),
					Values:     fmt.Sprintf("'%s'!$E$7:$E$%d", sheet, lastDataRow),
				},
			},
			Title: excelize.ChartTitle{
				Paragraph: []excelize.RichTextRun{
					{Text: "Perbandingan Hasil Uji Bot per OPD (Cocok vs Tidak)"},
				},
			},
			Legend: excelize.ChartLegend{
				Position: "top",
			},
			PlotArea: excelize.ChartPlotArea{
				ShowVal: true,
			},
			Format: excelize.GraphicOptions{
				ScaleX:  1.45,
				ScaleY:  1.25,
				OffsetX: 15,
				OffsetY: 5,
			},
		})
	}

	return nil
}

func clearSheetData(f *excelize.File, sheet string) error {
	rows, err := f.GetRows(sheet)
	if err != nil || len(rows) == 0 {
		return nil
	}
	for i := len(rows); i >= 1; i-- {
		_ = f.RemoveRow(sheet, i)
	}
	return nil
}
