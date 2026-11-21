// Command workbench-seed-template membangun template workbook runtime dari
// workbook uji asli: header, styling, Petunjuk, dan formula Ringkasan
// dipertahankan; gambar dan baris data dibuang.
//
//	go run ./cmd/workbench-seed-template \
//	    -src Uji-Bank-Pertanyaan-Programmer-4.xlsx \
//	    -dst data/templates/workbook_template.xlsx
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"whatshap-gateway/internal/workbench"
	"whatshap-gateway/internal/workbench/templateutil"
)

func main() {
	src := flag.String("src", "Uji-Bank-Pertanyaan-Programmer-4.xlsx", "workbook sumber (read-only)")
	dst := flag.String("dst", workbench.TemplatePathDefault, "path template hasil")
	flag.Parse()

	st, err := templateutil.Build(*src, *dst)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gagal membuat template: %v\n", err)
		os.Exit(1)
	}

	raw, err := os.ReadFile(*dst)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gagal membaca hasil: %v\n", err)
		os.Exit(1)
	}
	sum := sha256.Sum256(raw)

	fmt.Println("=== TEMPLATE WORKBOOK ===")
	fmt.Printf("sumber            : %s (%d byte)\n", *src, st.SourceBytes)
	fmt.Printf("hasil             : %s (%d byte)\n", *dst, st.OutputBytes)
	fmt.Printf("gambar dihapus    : %d (sisa %d)\n", st.PicturesBefore, st.PicturesAfter)
	fmt.Printf("baris data dihapus: %d\n", st.RowsRemoved)
	fmt.Printf("formula Ringkasan : %d dipertahankan\n", st.FormulasKept)
	fmt.Printf("sheet             : %v\n", st.Sheets)
	fmt.Printf("sha256            : %s\n", hex.EncodeToString(sum[:]))

	if st.OutputBytes >= 1<<20 {
		fmt.Fprintf(os.Stderr, "PERINGATAN: hasil %d byte melebihi target 1 MB\n", st.OutputBytes)
	}
}
