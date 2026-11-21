// Command workbench-audit membandingkan baris-per-baris sumber (xlsx+metrics)
// dengan isi schema workbench untuk satu user. READ-ONLY: tidak menulis apa pun.
//
//	go run ./cmd/workbench-audit -user 2 -xlsx data/users/2/workbook.xlsx
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
	userID := flag.Int64("user", 0, "user id (wajib)")
	xlsx := flag.String("xlsx", "", "path workbook .xlsx (wajib)")
	metrics := flag.String("metrics", "", "path sidecar metrics.json (default: <xlsx>.metrics.json)")
	var mediaRoots multiFlag
	flag.Var(&mediaRoots, "media", "direktori pencarian lampiran (boleh diulang)")
	flag.Parse()

	if *userID <= 0 || strings.TrimSpace(*xlsx) == "" {
		fmt.Fprintln(os.Stderr, "wajib: -user <id> -xlsx <path>")
		flag.Usage()
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
		UserID: *userID, XLSXPath: *xlsx, MetricsPath: *metrics, MediaRoots: roots,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gagal menyiapkan rencana: %v\n", err)
		os.Exit(1)
	}

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

	res, err := migrator.Audit(ctx, db, plan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit gagal: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("=== PARITY AUDIT (read-only) ===")
	fmt.Print(res.Summary())
	if !res.OK() {
		os.Exit(1)
	}
}
