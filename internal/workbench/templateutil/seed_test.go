package templateutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"whatshap-gateway/internal/excel"
)

func sourceWorkbook(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "..", "..", "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("workbook sumber tidak tersedia: %v", err)
	}
	return p
}

// TestBuildRemovesMediaKeepsFormulas adalah inti Tahap 9: template harus jauh
// lebih kecil (gambar dibuang), tetapi header, sheet, dan formula tetap utuh.
func TestBuildRemovesMediaKeepsFormulas(t *testing.T) {
	src := sourceWorkbook(t)
	dst := filepath.Join(t.TempDir(), "template.xlsx")

	st, err := Build(src, dst)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Gambar hilang total.
	if st.PicturesAfter != 0 {
		t.Fatalf("gambar sisa: %d", st.PicturesAfter)
	}
	if st.PicturesBefore == 0 {
		t.Fatalf("sumber seharusnya punya gambar")
	}
	// Jauh lebih kecil: gambar ±25 MB, jadi template harus < 1 MB.
	if st.OutputBytes >= 1<<20 {
		t.Fatalf("template %d byte, target < 1 MB", st.OutputBytes)
	}
	if st.OutputBytes >= st.SourceBytes {
		t.Fatalf("template (%d) tidak lebih kecil dari sumber (%d)", st.OutputBytes, st.SourceBytes)
	}
	// Formula Ringkasan dipertahankan.
	if st.FormulasKept < 100 {
		t.Fatalf("formula tahun lalu hilang: %d", st.FormulasKept)
	}

	f, err := excelize.OpenFile(dst)
	if err != nil {
		t.Fatalf("buka template hasil: %v", err)
	}
	defer f.Close()

	// Kelima sheet tetap ada.
	for _, want := range []string{
		excel.SheetBank, "Petunjuk", excel.SheetRingkasan, excel.SheetOutOfBank, excel.SheetStress,
	} {
		if idx, err := f.GetSheetIndex(want); err != nil || idx < 0 {
			t.Fatalf("sheet %q hilang dari template", want)
		}
	}

	// Header kolom tetap (baris 2) di ketiga sheet data.
	check := map[string]struct{ cell, want string }{
		excel.SheetOutOfBank: {"A2", "No"},
		excel.SheetStress:    {"A2", "No"},
	}
	for sheet, c := range check {
		got, err := f.GetCellValue(sheet, c.cell)
		if err != nil {
			t.Fatalf("baca %s!%s: %v", sheet, c.cell, err)
		}
		if got != c.want {
			t.Fatalf("%s!%s = %q, want %q", sheet, c.cell, got, c.want)
		}
	}

	// Baris data sudah tidak ada (baris 3 kosong di Bank Pertanyaan).
	if v, _ := f.GetCellValue(excel.SheetBank, "A3"); v != "" {
		t.Fatalf("baris data masih ada: Bank!A3 = %q", v)
	}

	// Formula Ringkasan masih formula, bukan nilai statis.
	formula, err := f.GetCellFormula(excel.SheetRingkasan, "C3")
	if err != nil || formula == "" {
		t.Fatalf("formula Ringkasan C3 hilang: %q err=%v", formula, err)
	}
	if got := formula; len(got) < 8 || got[:8] != "COUNTIFS" && got[:7] != "COUNTIF" {
		t.Fatalf("formula C3 tidak wajar: %q", got)
	}

	// Petunjuk tidak kosong.
	rows, err := f.GetRows("Petunjuk")
	if err != nil || len(rows) == 0 {
		t.Fatalf("sheet Petunjuk kosong: rows=%d err=%v", len(rows), err)
	}
}

func TestBuildRejectsMissingSource(t *testing.T) {
	_, err := Build(filepath.Join(t.TempDir(), "nope.xlsx"), filepath.Join(t.TempDir(), "out.xlsx"))
	if err == nil {
		t.Fatalf("sumber tidak ada harus error")
	}
}

func TestBuildRejectsEmptyPaths(t *testing.T) {
	if _, err := Build("", "x.xlsx"); err == nil {
		t.Fatalf("src kosong harus error")
	}
	if _, err := Build("x.xlsx", ""); err == nil {
		t.Fatalf("dst kosong harus error")
	}
}

// TestBuildDoesNotShiftFormulas adalah regresi untuk bug yang ditemukan saat
// mengerjakan Tahap 9: memakai RemoveRow untuk membuang baris data membuat
// excelize menggeser referensi formula Ringkasan (B3:B400 -> B2:B190),
// sehingga 20 baris terakhir tidak terhitung saat generator mengisi data.
//
// Test ini memastikan rentang kolom B/J pada formula tetap mulai baris 3.
func TestBuildDoesNotShiftFormulas(t *testing.T) {
	src := sourceWorkbook(t)
	dst := filepath.Join(t.TempDir(), "template.xlsx")
	if _, err := Build(src, dst); err != nil {
		t.Fatalf("Build: %v", err)
	}

	srcFile, err := excelize.OpenFile(src)
	if err != nil {
		t.Fatalf("buka sumber: %v", err)
	}
	defer srcFile.Close()
	outFile, err := excelize.OpenFile(dst)
	if err != nil {
		t.Fatalf("buka hasil: %v", err)
	}
	defer outFile.Close()

	wantFormula, err := srcFile.GetCellFormula(excel.SheetRingkasan, "C3")
	if err != nil || wantFormula == "" {
		t.Fatalf("sumber tidak punya formula C3: %q err=%v", wantFormula, err)
	}
	gotFormula, err := outFile.GetCellFormula(excel.SheetRingkasan, "C3")
	if err != nil {
		t.Fatalf("baca formula hasil: %v", err)
	}
	if gotFormula != wantFormula {
		t.Fatalf("formula Ringkasan!C3 bergeser:\n sumber=%q\n hasil =%q", wantFormula, gotFormula)
	}

	// Rentang harus dimulai baris 3, bukan 2.
	if strings.Contains(gotFormula, "B2:") {
		t.Fatalf("rentang formula bergeser ke baris 2: %q", gotFormula)
	}
}
