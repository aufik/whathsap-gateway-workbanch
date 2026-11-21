// Command workbench-migrate mengisi schema workbench dari data workbook lama.
//
// Default: -dry-run=true (hanya cetak rencana, tidak menulis apa pun).
// Menulis ke PostgreSQL HANYA dengan -apply.
//
// Pemakaian:
//
//	go run ./cmd/workbench-migrate -user 2 -xlsx data/users/2/workbook.xlsx
//	go run ./cmd/workbench-migrate -user 2 -xlsx data/users/2/workbook.xlsx -apply
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"whatshap-gateway/internal/config"
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
	apply := flag.Bool("apply", false, "MENULIS ke schema workbench (transaksional, idempoten)")
	flag.Parse()

	if *userID <= 0 || strings.TrimSpace(*xlsx) == "" {
		fmt.Fprintln(os.Stderr, "wajib: -user <id> -xlsx <path>")
		flag.Usage()
		os.Exit(2)
	}
	if *apply && *dryRun {
		fmt.Fprintln(os.Stderr, "pilih salah satu: -dry-run (default) atau -apply")
		os.Exit(2)
	}

	roots := []string(mediaRoots)
	if len(roots) == 0 {
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

	fmt.Println("=== RENCANA ===")
	fmt.Printf("media roots        : %s\n", strings.Join(roots, ", "))
	fmt.Print(plan.Summary())

	if !*apply {
		fmt.Println("=== DRY RUN — tidak ada data yang ditulis ===")
		return
	}

	// --- APPLY ---
	cfg, err := config.Load(".env")
	if err != nil {
		fmt.Fprintf(os.Stderr, "gagal memuat .env: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gagal konek database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	start := time.Now()
	if err := migrator.Apply(ctx, db, *userID, plan); err != nil {
		fmt.Fprintf(os.Stderr, "APPLY GAGAL (seluruh transaksi di-rollback): %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("=== APPLIED dalam %s (satu transaksi) ===\n", time.Since(start).Round(time.Millisecond))

	// Audit otomatis setelah apply supaya hasilnya langsung terverifikasi.
	res, err := migrator.Audit(ctx, db, plan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply sukses, tetapi audit gagal: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("=== PARITY AUDIT ===")
	fmt.Print(res.Summary())
	if !res.OK() {
		os.Exit(1)
	}
}
