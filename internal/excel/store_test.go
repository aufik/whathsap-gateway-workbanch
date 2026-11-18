package excel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"whatshap-gateway/internal/model"
)

func TestStoreReadUpdatePreservesFormulasAndBackup(t *testing.T) {
	origPath := filepath.Join("..", "..", "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
	rawBytes, err := os.ReadFile(origPath)
	if err != nil {
		t.Fatalf("read original excel: %v", err)
	}

	tmpDir := t.TempDir()
	workPath := filepath.Join(tmpDir, "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
	if err := os.WriteFile(workPath, rawBytes, 0644); err != nil {
		t.Fatalf("write temp copy: %v", err)
	}

	store, err := NewStore(workPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// Verify automatic safety backup exists
	backupPath := BackupPathFor(workPath)
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("expected safety backup %s to exist: %v", backupPath, err)
	}

	rows, sum := store.Snapshot()
	if sum.Total != 210 {
		t.Fatalf("expected 210 rows, got %d", sum.Total)
	}
	if sum.Approved != 207 || sum.Waiting != 3 {
		t.Fatalf("expected 207 approved / 3 waiting, got %d / %d", sum.Approved, sum.Waiting)
	}
	if len(rows) != 210 || !rows[0].HasExpectedFile {
		t.Fatalf("unexpected row #1 flags: %+v", rows[0])
	}

	// Update Row #1 and verify persistence + Ringkasan formula preservation
	qIn := "minta data pasar tradisional kota medan tahun 2022 dong"
	aOut := "*Pasar Tradisional Kota Medan Tahun 2022*\n[Berkas] image/png"
	note := "Teks dan berkas sesuai"
	status := "Cocok"

	updatedRow, newSum, err := store.UpdateRow(1, model.RowPatch{
		QuestionInput: &qIn,
		ActualReply:   &aOut,
		Notes:         &note,
		ResultStatus:  &status,
	})
	if err != nil {
		t.Fatalf("UpdateRow failed: %v", err)
	}
	if updatedRow.ResultStatus != "Cocok" || !updatedRow.HasActualFile || newSum.Cocok != 1 {
		t.Fatalf("unexpected updated state: row=%+v sum=%+v", updatedRow, newSum)
	}

	// Re-open from disk with excelize and verify cells + Ringkasan COUNTIFS formula
	f, err := excelize.OpenFile(workPath)
	if err != nil {
		t.Fatalf("reopen updated file: %v", err)
	}
	defer f.Close()

	gotF3, _ := f.GetCellValue(SheetBank, "F3")
	gotJ3, _ := f.GetCellValue(SheetBank, "J3")
	if gotF3 != qIn || gotJ3 != "Cocok" {
		t.Fatalf("disk values mismatch: F3=%q J3=%q", gotF3, gotJ3)
	}

	formulaC3, err := f.GetCellFormula(SheetRingkasan, "C3")
	if err != nil {
		t.Fatalf("read formula Ringkasan!C3: %v", err)
	}
	if !strings.Contains(strings.ToUpper(formulaC3), "COUNTIFS") {
		t.Fatalf("expected COUNTIFS formula preserved in Ringkasan!C3, got %q", formulaC3)
	}

	if idx, err := f.GetSheetIndex("Petunjuk"); err != nil || idx < 0 {
		t.Fatalf("expected Petunjuk sheet preserved, got idx=%d err=%v", idx, err)
	}
}
