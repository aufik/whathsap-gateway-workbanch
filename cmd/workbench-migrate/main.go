// Command workbench-migrate mengisi schema workbench dari data workbook lama.
//
// Tahap 5 (sekarang): HANYA perencanaan. Default -dry-run=true mencetak
// hitungan tanpa menyentuh database.
//
// Pemakaian:
//
//	go run ./cmd/workbench-migrate -user 2 \
//	    -xlsx data/users/2/workbook.xlsx \
//	    -media data/users/2/downloads -media data/downloads
//
// Flag -apply (menulis ke PostgreSQL) ditambahkan pada Tahap 6.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"whatshap-gateway/internal/workbench/migrator"
)

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func main() {
	userID := flag.Int64("user", 0, "user id pemilik workbook (wajib)")
	xlsx := flag.String("xlsx", "", "path workbook .xlsx (wajib)")
	metrics := flag.String("metrics", "", "path sidecar metrics.json (default: <xlsx>.metrics.json)")
	var mediaRoots multiFlag
	flag.Var(&mediaRoots, "media", "direktori pencarian lampiran (boleh diulang)")
	dryRun := flag.Bool("dry-run", true, "hanya cetak rencana tanpa menulis database (default true)")
	flag.Parse()

	if *userID <= 0 || strings.TrimSpace(*xlsx) == "" {
		fmt.Fprintln(os.Stderr, "wajib: -user <id> -xlsx <path>")
		flag.Usage()
		os.Exit(2)
	}
	if !*dryRun {
		fmt.Fprintln(os.Stderr, "mode -apply belum tersedia (ditambahkan pada Tahap 6); gunakan -dry-run")
		os.Exit(2)
	}

	roots := []string(mediaRoots)
	if len(roots) == 0 {
		// Default: direktori milik user + folder global lama, keduanya read-only.
		roots = []string{
			filepath.Join("data", "users", fmt.Sprintf("%d", *userID), "downloads"),
			filepath.Join("data", "downloads"),
		}
	}

	plan, err := migrator.Build(migrator.Options{
		UserID:      *userID,
		XLSXPath:    *xlsx,
		MetricsPath: *metrics,
		MediaRoots:  roots,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gagal menyiapkan rencana: %v\n", err)
		os.Exit(1)
	}
	if err := plan.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "rencana tidak valid: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== DRY RUN — tidak ada data yang ditulis ===")
	fmt.Printf("media roots        : %s\n", strings.Join(roots, ", "))
	fmt.Print(plan.Summary())
	fmt.Println("=== OK ===")
}
