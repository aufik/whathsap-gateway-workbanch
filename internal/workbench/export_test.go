package workbench

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/livechat"
	"whatshap-gateway/internal/model"
)

// TestExportRoundTrip parity paling kuat untuk generator: data dari DB →
// xlsx di memori → di-parse ulang dengan parser bersama → harus sama dengan
// data DB semula (baris, kolom, nilai). Gambar tidak diuji nilainya di sini
// (sudah diuji terpisah).
func TestExportRoundTrip(t *testing.T) {
	db := openParityDB(t)
	defer db.Close()
	seedDataset(t, db)
	defer cleanupParity(t, db)

	store, err := NewDBStore(db, parityUserID)
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	store.SetExportConfig(ExportConfig{MediaRoots: []string{t.TempDir()}})

	raw, name, err := store.ExportBytes()
	if err != nil {
		t.Fatalf("ExportBytes: %v", err)
	}
	if name != "workbook.xlsx" {
		t.Fatalf("nama file: %q", name)
	}
	if len(raw) < 1000 {
		t.Fatalf("workbook terlalu kecil: %d byte", len(raw))
	}

	res, err := excel.ParseBytes(raw, nil)
	if err != nil {
		t.Fatalf("parse hasil export: %v", err)
	}

	wantBank, _ := store.Snapshot()
	// Flags turunan (HasExpectedFile/HasActualCoord) DIHITUNG ULANG oleh parser
	// dari teks ExpectedReply/ActualReply, sedangkan DB bisa menyimpan nilai
	// lama. Metrics juga tidak punya kolom di sheet Excel. Jadi parity yang
	// benar untuk round-trip adalah membandingkan kolom yang memang ada di
	// file: No, OPD, Kategori, StatusBank, QRef, QInput, Expected, Actual,
	// Notes, ResultStatus.
	if len(res.Bank) != len(wantBank) {
		t.Fatalf("bank count: export=%d db=%d", len(res.Bank), len(wantBank))
	}
	for i := range wantBank {
		if sheetProjection(res.Bank[i]) != sheetProjection(wantBank[i]) {
			t.Fatalf("bank row %d berbeda setelah round-trip:\n export=%+v\n db    =%+v",
				i, sheetProjection(res.Bank[i]), sheetProjection(wantBank[i]))
		}
	}
	wantOOB, _ := store.OutOfBankSnapshot()
	if len(res.OOB) != len(wantOOB) {
		t.Fatalf("oob count: export=%d db=%d", len(res.OOB), len(wantOOB))
	}
	// HumanSummary di-generate dari metrics saat DB diisi; bila xlsx tidak
	// menyimpan kolom metrics, round-trip membandingkan kolom sheet saja.
	for i := range wantOOB {
		if oobProjection(res.OOB[i]) != oobProjection(wantOOB[i]) {
			t.Fatalf("oob row %d berbeda:\n export=%+v\n db    =%+v", i, oobProjection(res.OOB[i]), oobProjection(wantOOB[i]))
		}
	}
	wantStress, _ := store.StressTestSnapshot()
	if len(res.Stress) != len(wantStress) {
		t.Fatalf("stress count: export=%d db=%d", len(res.Stress), len(wantStress))
	}
	for i := range wantStress {
		if stressProjection(res.Stress[i]) != stressProjection(wantStress[i]) {
			t.Fatalf("stress row %d berbeda:\n export=%+v\n db    =%+v", i, stressProjection(res.Stress[i]), stressProjection(wantStress[i]))
		}
	}
}

// TestExportEmptyUser memastikan user tanpa data tetap menghasilkan workbook
// yang valid (bukan error), dengan header saja.
func TestExportEmptyUser(t *testing.T) {
	db := openParityDB(t)
	defer db.Close()
	cleanupParity(t, db)
	ensureParityUser(t, db)
	defer cleanupParity(t, db)

	store, err := NewDBStore(db, parityUserID)
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	store.SetExportConfig(ExportConfig{MediaRoots: []string{t.TempDir()}})

	raw, _, err := store.ExportBytes()
	if err != nil {
		t.Fatalf("ExportBytes: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("hasil bukan xlsx valid: %v", err)
	}
	defer f.Close()
	for _, sheet := range []string{excel.SheetBank, excel.SheetOutOfBank, excel.SheetStress, livechat.SummarySheet} {
		idx, err := f.GetSheetIndex(sheet)
		if err != nil || idx < 0 {
			t.Fatalf("sheet %s hilang", sheet)
		}
	}
}

// TestExportEmbedsPictures memastikan gambar dari disk benar-benar ditempel
// saat media path resolve, dan path hilang hanya dilaporkan (tidak error).
func TestExportEmbedsPictures(t *testing.T) {
	db := openParityDB(t)
	defer db.Close()
	cleanupParity(t, db)
	ensureParityUser(t, db)
	defer cleanupParity(t, db)

	// Media dir dengan satu PNG valid.
	mediaDir := t.TempDir()
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x04, 0x08, 0x02, 0x00, 0x00, 0x00, 0x26, 0x93, 0x09, 0x29, 0x00, 0x00, 0x00, 0x10, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x38, 0x21, 0x27, 0x07, 0x47, 0x0C, 0xC4, 0x71, 0x00, 0xB1, 0x63, 0x10, 0x41, 0x3B, 0x4B, 0x63, 0x82, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}
	if err := os.WriteFile(filepath.Join(mediaDir, "found.png"), png, 0644); err != nil {
		t.Fatalf("seed media: %v", err)
	}

	if err := seedBankRow(db, model.TestRow{
		No: 1, ExcelRow: 3, OPD: "X", ResultStatus: "Cocok",
		MediaFiles: []string{"/media/found.png", "/media/2/missing.png"},
	}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	var dbMedia string
	_ = db.QueryRow(`SELECT media_files::text FROM workbench.bank_rows WHERE user_id=$1 AND no=1`, parityUserID).Scan(&dbMedia)
	t.Logf("media_files di DB = %s", dbMedia)

	store, err := NewDBStore(db, parityUserID)
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	store.SetExportConfig(ExportConfig{MediaRoots: []string{mediaDir}})

	raw, _, err := store.ExportBytes()
	if err != nil {
		t.Fatalf("ExportBytes harus sukses meski ada path hilang: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("xlsx tidak valid: %v", err)
	}
	defer f.Close()
	pics, err := f.GetPictures(excel.SheetBank, "J3")
	if err != nil {
		t.Fatalf("GetPictures: %v", err)
	}
	if len(pics) != 1 {
		t.Fatalf("harus ada 1 gambar ditempel, dapat %d", len(pics))
	}
}

// seedBankRow menyisipkan satu baris bank untuk parityUserID.
func seedBankRow(db *sql.DB, r model.TestRow) error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workbench.bank_rows (no, excel_row, opd, kategori, status_bank,
			question_ref, question_input, expected_reply, actual_reply, notes,
			result_status, has_expected_file, has_expected_coord, has_actual_file,
			has_actual_coord, media_files, metrics, user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::jsonb,$18)`,
		r.No, r.ExcelRow, r.OPD, r.Kategori, r.StatusBank, r.QuestionRef,
		r.QuestionInput, r.ExpectedReply, r.ActualReply, r.Notes, r.ResultStatus,
		r.HasExpectedFile, r.HasExpectedCoord, r.HasActualFile, r.HasActualCoord,
		textArrayTest(r.MediaFiles), jsonMetrics(r.Metrics), parityUserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workbench.user_workbooks (user_id, file_name, opd_order)
		VALUES ($1,'workbook.xlsx','{}'::text[])
		ON CONFLICT (user_id) DO NOTHING`, parityUserID); err != nil {
		return err
	}
	return tx.Commit()
}

// TestExportRealUserData menjalankan generator pada data produksi user 2 dan
// memastikan hasilnya bisa dibuka serta jumlah barisnya sesuai.
func TestExportRealUserData(t *testing.T) {
	db := openParityDB(t)
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM workbench.bank_rows WHERE user_id=2`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n == 0 {
		t.Skip("user 2 belum dimigrasi")
	}
	store, err := NewDBStore(db, 2)
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	raw, _, err := store.ExportBytes()
	if err != nil {
		t.Fatalf("ExportBytes: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("xlsx valid: %v", err)
	}
	defer f.Close()
	sheetRows := func(name string) int {
		rows, err := f.GetRows(name)
		if err != nil {
			return 0
		}
		return len(rows)
	}
	// 2 baris judul/header + 210 data.
	if got := sheetRows(excel.SheetBank); got != 212 {
		t.Fatalf("bank sheet rows: %d want 212", got)
	}
	if got := sheetRows(excel.SheetOutOfBank); got != 54 {
		t.Fatalf("oob sheet rows: %d want 54", got)
	}
	if got := sheetRows(excel.SheetStress); got != 17 {
		t.Fatalf("stress sheet rows: %d want 17", got)
	}
	fmt.Printf("export user 2: bank=%d oob=%d stress=%d bytes=%d\n",
		sheetRows(excel.SheetBank), sheetRows(excel.SheetOutOfBank),
		sheetRows(excel.SheetStress), len(raw))
}

// Proyeksi kolom yang benar-benar ditulis ke sheet. Dipakai test round-trip
// supaya perbedaan representasi turunan (flags, metrics) tidak menjadi false
// positive.
type bankProj struct {
	No, ExcelRow                                 int
	OPD, Kategori, StatusBank                    string
	QuestionRef, QuestionInput, Expected, Actual string
	Notes, ResultStatus                          string
}

func sheetProjection(r model.TestRow) bankProj {
	return bankProj{r.No, r.ExcelRow, r.OPD, r.Kategori, r.StatusBank,
		r.QuestionRef, r.QuestionInput, r.ExpectedReply, r.ActualReply, r.Notes, r.ResultStatus}
}

func oobProjection(r model.OutOfBankRow) string {
	return fmt.Sprintf("%d|%d|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s",
		r.No, r.ExcelRow, r.SourceRef, r.ScenarioType, r.BaseQuestion,
		r.ProbeQuestion, r.ExpectedBehavior, r.ActualReply, r.HumanSummary,
		r.Notes, r.ResultStatus, r.ForbiddenYear+"|"+r.ReferenceYear)
}

func stressProjection(r model.StressTestRow) string {
	return fmt.Sprintf("%d|%d|%s|%s|%s|%s|%s|%s|%s|%d|%s",
		r.No, r.ExcelRow, r.StressCategory, r.UserPromptQ, r.IntentPurpose,
		r.ExpectedBehavior, r.ActualReply, r.HumanSummary, r.Notes,
		r.PolitenessScore, r.HallucinationLvl)
}

func TestExportIncludesChatTimeAndPhones(t *testing.T) {
	f := buildMinimalWorkbook()
	defer f.Close()

	bankRows := []model.TestRow{
		{
			No: 1, OPD: "Dinas Kominfo", Kategori: "Layanan", StatusBank: "approved",
			QuestionRef: "Pertanyaan", QuestionInput: "Input", ExpectedReply: "Acuan",
			ActualReply: "Jawaban", Notes: "Catatan", ResultStatus: "Cocok",
			Metrics: model.RowHumanMetrics{
				TestedAt:       1763460000000,
				SenderPhone:    "6281234567890@s.whatsapp.net",
				BotTargetPhone: "6289876543210",
			},
		},
	}
	note := func(string) {}
	if err := fillBankSheet(f, bankRows, nil, note); err != nil {
		t.Fatalf("fillBankSheet: %v", err)
	}

	valK2, _ := f.GetCellValue(excel.SheetBank, "K2")
	valL2, _ := f.GetCellValue(excel.SheetBank, "L2")
	valM2, _ := f.GetCellValue(excel.SheetBank, "M2")
	if valK2 != "Waktu Chat" || valL2 != "WA Pengirim" || valM2 != "WA Target" {
		t.Fatalf("header mismatch: K2=%q L2=%q M2=%q", valK2, valL2, valM2)
	}

	valK3, _ := f.GetCellValue(excel.SheetBank, "K3")
	valL3, _ := f.GetCellValue(excel.SheetBank, "L3")
	valM3, _ := f.GetCellValue(excel.SheetBank, "M3")
	if valK3 != "2025-11-18 17:00:00" && valK3 != time.UnixMilli(1763460000000).Format("2006-01-02 15:04:05") {
		t.Fatalf("testedAt mismatch: got %q", valK3)
	}
	if valL3 != "6281234567890" {
		t.Fatalf("sender mismatch: got %q want 6281234567890", valL3)
	}
	if valM3 != "6289876543210" {
		t.Fatalf("target mismatch: got %q want 6289876543210", valM3)
	}
}

func TestExportBytesFilteredSheetsAndStatus(t *testing.T) {
	db := openParityDB(t)
	defer db.Close()
	cleanupParity(t, db)
	ensureParityUser(t, db)
	defer cleanupParity(t, db)

	store, err := NewDBStore(db, parityUserID)
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}

	raw, _, err := store.ExportBytesFiltered(ExportFilter{
		Sheets: []string{"livechat"},
	})
	if err != nil {
		t.Fatalf("ExportBytesFiltered: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	if idx, _ := f.GetSheetIndex(livechat.SummarySheet); idx < 0 {
		t.Fatalf("sheet %s must exist in filtered export", livechat.SummarySheet)
	}
	if idx, _ := f.GetSheetIndex(excel.SheetBank); idx >= 0 {
		t.Fatalf("sheet %s must be excluded in filtered export", excel.SheetBank)
	}
}

func TestDashboardOPDTableAndChart(t *testing.T) {
	db := openParityDB(t)
	defer db.Close()
	cleanupParity(t, db)
	ensureParityUser(t, db)
	defer cleanupParity(t, db)

	_ = seedBankRow(db, model.TestRow{No: 1, ExcelRow: 3, OPD: "Dinas Kominfo", ResultStatus: "Cocok"})
	_ = seedBankRow(db, model.TestRow{No: 2, ExcelRow: 4, OPD: "Dinas Kominfo", ResultStatus: "Tidak"})
	_ = seedBankRow(db, model.TestRow{No: 3, ExcelRow: 5, OPD: "Dinas Kesehatan", ResultStatus: "Cocok"})

	store, err := NewDBStore(db, parityUserID)
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}

	raw, _, err := store.ExportBytes()
	if err != nil {
		t.Fatalf("ExportBytes: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	idx, err := f.GetSheetIndex(SheetDashboardOPD)
	if err != nil || idx < 0 {
		t.Fatalf("sheet %s tidak ditemukan", SheetDashboardOPD)
	}

	title, _ := f.GetCellValue(SheetDashboardOPD, "A1")
	if !strings.Contains(title, "DASHBOARD PERFORMA") {
		t.Fatalf("title mismatch: %q", title)
	}

	kpiTotal, _ := f.GetCellValue(SheetDashboardOPD, "A4")
	if !strings.Contains(kpiTotal, "TOTAL SOAL") {
		t.Fatalf("kpiTotal mismatch: %q", kpiTotal)
	}

	thOPD, _ := f.GetCellValue(SheetDashboardOPD, "B6")
	if thOPD != "Nama OPD / Unit Layanan" {
		t.Fatalf("thOPD mismatch: %q", thOPD)
	}
}
