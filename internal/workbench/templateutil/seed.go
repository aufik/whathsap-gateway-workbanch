// Package templateutil membangun template workbook runtime dari workbook uji
// asli: mempertahankan header, styling, sheet Petunjuk, dan seluruh formula
// Ringkasan, tetapi membuang seluruh gambar serta baris data uji.
//
// Dipakai oleh cmd/workbench-seed-template (Tahap 9 PLAN_WORKBENCH_DB v4).
package templateutil

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xuri/excelize/v2"

	"whatshap-gateway/internal/excel"
)

// Stats merangkum perubahan yang dilakukan seeder.
type Stats struct {
	SourceBytes    int64
	OutputBytes    int64
	PicturesBefore int
	PicturesAfter  int
	RowsRemoved    int
	FormulasKept   int
	Sheets         []string
}

// Build membaca src dan menulis template bersih ke dst.
//
// Yang dibuang:
//   - semua gambar (107 file = ±25 MB) dari ketiga sheet data
//   - baris data (mulai baris 3) pada Bank Pertanyaan / Uji Diluar Bank / Uji Stress AI
//
// Yang dipertahankan:
//   - header baris 1-2 (judul + kolom) dan lebar kolom
//   - sheet Petunjuk apa adanya
//   - seluruh formula Ringkasan (COUNTIF/COUNTIFS/E) dan nilai A3:A..
//   - urutan sheet
func Build(src, dst string) (Stats, error) {
	var st Stats
	if strings.TrimSpace(src) == "" || strings.TrimSpace(dst) == "" {
		return st, errors.New("path src/dst wajib diisi")
	}
	if fi, err := os.Stat(src); err != nil {
		return st, fmt.Errorf("sumber tidak terbaca: %w", err)
	} else {
		st.SourceBytes = fi.Size()
	}

	f, err := excelize.OpenFile(src)
	if err != nil {
		return st, fmt.Errorf("buka sumber: %w", err)
	}
	defer f.Close()

	st.Sheets = f.GetSheetList()

	for _, sheet := range []string{excel.SheetBank, excel.SheetOutOfBank, excel.SheetStress} {
		if idx, err := f.GetSheetIndex(sheet); err != nil || idx < 0 {
			continue
		}
		removed, err := removePictures(f, sheet)
		if err != nil {
			return st, err
		}
		st.PicturesBefore += removed

		rows, err := f.GetRows(sheet)
		if err != nil {
			return st, fmt.Errorf("baca sheet %s: %w", sheet, err)
		}
		if len(rows) <= 2 {
			continue
		}
		// PENTING: jangan memakai RemoveRow. Menghapus baris membuat excelize
		// menggeser referensi formula di sheet Ringkasan (B3:B400 menjadi
		// B2:B190), sehingga baris data hasil generate tidak lagi tercakup dan
		// Ringkasan menghitung salah tanpa terlihat. Cukup kosongkan nilai
		// cell: baris kosong tidak memengaruhi COUNTIF/COUNTIFS.
		removedRows, err := clearDataRows(f, sheet, len(rows))
		if err != nil {
			return st, err
		}
		st.RowsRemoved += removedRows
	}

	// Hitung sisa gambar + formula setelah pembersihan.
	for _, sheet := range st.Sheets {
		cells, err := f.GetPictureCells(sheet)
		if err != nil {
			continue
		}
		st.PicturesAfter += len(cells)
	}
	st.FormulasKept = countFormulas(f, excel.SheetRingkasan)

	f.SetActiveSheet(0)

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return st, err
	}
	if err := f.SaveAs(dst); err != nil {
		return st, fmt.Errorf("tulis template: %w", err)
	}
	if err := pruneOrphanDrawings(dst); err != nil {
		return st, err
	}
	if fi, err := os.Stat(dst); err == nil {
		st.OutputBytes = fi.Size()
	}
	return st, nil
}

// clearDataRows mengosongkan nilai cell pada baris 3..lastRow di semua kolom
// yang dipakai (A..maxCol). Baris tidak dihapus agar referensi formula tetap
// stabil.
func clearDataRows(f *excelize.File, sheet string, lastRow int) (int, error) {
	// Tentukan lebar tabel dari header baris 2.
	headers, err := f.GetRows(sheet)
	if err != nil {
		return 0, fmt.Errorf("baca header %s: %w", sheet, err)
	}
	maxCol := 1
	if len(headers) >= 2 {
		maxCol = len(headers[1])
	}
	if maxCol < 1 {
		maxCol = 1
	}
	// Batasi sampai kolom yang lazim dipakai (maks 16) agar tidak menyapu
	// area jauh yang mungkin berisi catatan manual.
	if maxCol > 16 {
		maxCol = 16
	}

	cleared := 0
	for row := 3; row <= lastRow; row++ {
		rowHasData := false
		for col := 1; col <= maxCol; col++ {
			cell, err := excelize.CoordinatesToCellName(col, row)
			if err != nil {
				continue
			}
			val, err := f.GetCellValue(sheet, cell)
			if err != nil || val == "" {
				continue
			}
			rowHasData = true
			_ = f.SetCellStr(sheet, cell, "")
		}
		if rowHasData {
			cleared++
		}
	}
	return cleared, nil
}

// removePictures menghapus semua gambar di satu sheet.
//
// GetPictureCells hanya mengembalikan sel unik, sedangkan satu sel bisa
// berisi beberapa gambar bertumpuk. Jadi penghapusan diulang sampai tidak ada
// sel bergambar lagi (maks 20 putaran sebagai pengaman).
func removePictures(f *excelize.File, sheet string) (int, error) {
	total := 0
	for round := 0; round < 20; round++ {
		cells, err := f.GetPictureCells(sheet)
		if err != nil {
			return total, fmt.Errorf("daftar gambar %s: %w", sheet, err)
		}
		if len(cells) == 0 {
			return total, nil
		}
		removedThisRound := 0
		for _, cell := range cells {
			if err := f.DeletePicture(sheet, cell); err != nil {
				return total, fmt.Errorf("hapus gambar %s!%s: %w", sheet, cell, err)
			}
			removedThisRound++
		}
		total += removedThisRound
	}
	return total, fmt.Errorf("gambar di sheet %s tidak habis setelah 20 putaran", sheet)
}

// countFormulas menghitung sel berisi formula pada satu sheet.
func countFormulas(f *excelize.File, sheet string) int {
	rows, err := f.GetRows(sheet)
	if err != nil {
		return 0
	}
	n := 0
	for r := 1; r <= len(rows); r++ {
		for c := 1; c <= 8; c++ {
			cell, err := excelize.CoordinatesToCellName(c, r)
			if err != nil {
				continue
			}
			if formula, err := f.GetCellFormula(sheet, cell); err == nil && formula != "" {
				n++
			}
		}
	}
	return n
}

// pruneOrphanDrawings membuang artefak yang tidak bisa dihapus lewat API
// excelize: file xl/drawings/* yang masih menunjuk ke xl/media/* walau sudah
// tidak dipakai sheet mana pun.
//
// Latar belakang: workbook produksi terbentuk dari penulisan berulang oleh
// excel.Store, sehingga menumpuk 22 drawing relationship yang semuanya
// menunjuk 5 gambar yang sama. GetPictureCells hanya melihat anchor di sheet,
// jadi sisa media (±1,98 MB) tetap tertinggal di file.
//
// Pendekatan: (1) buang semua part drawing + rels-nya, (2) buang semua
// xl/media/*, (3) bersihkan referensi <drawing r:id> di sheet XML, (4) hapus
// rels sheet yang menunjuk ke drawing yang sudah dibuang.
func pruneOrphanDrawings(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return fmt.Errorf("baca template: %w", err)
	}

	dropDrawing := func(name string) bool {
		return strings.HasPrefix(name, "xl/drawings/")
	}
	dropMedia := func(name string) bool {
		return strings.HasPrefix(name, "xl/media/")
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		if dropDrawing(f.Name) || dropMedia(f.Name) {
			continue
		}
		body, err := readZipFile(f)
		if err != nil {
			return err
		}
		if strings.HasSuffix(f.Name, ".rels") || strings.HasSuffix(f.Name, ".xml") {
			body = stripDrawingRefs(body)
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: f.Method, Modified: f.Modified})
		if err != nil {
			return err
		}
		if _, err := w.Write(body); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

var (
	// <drawing r:id="rIdN"/> pada worksheet.
	reDrawingTag = regexp.MustCompile(`<drawing[^>]*/>`)
	// <Relationship ... Target="../drawings/drawingN.xml" .../> pada rels.
	reDrawingRel = regexp.MustCompile(`<Relationship[^>]*Target="[^"]*drawings/[^"]*"[^>]*/>`)
)

func stripDrawingRefs(body []byte) []byte {
	body = reDrawingTag.ReplaceAll(body, nil)
	body = reDrawingRel.ReplaceAll(body, nil)
	return body
}
