// Package migrator menyediakan logika ETL workbook → schema workbench.
// Tahap 5 hanya mengimplementasikan perencanaan (dry-run); penulisan ke
// database ditambahkan pada Tahap 6 di balik flag -apply.
package migrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
)

// Plan adalah hasil perencanaan migrasi untuk satu user. Dry-run mencetak
// struktur ini; Tahap 6 memakainya untuk menulis ke PostgreSQL.
type Plan struct {
	UserID   int64
	XLSXPath string
	FileName string

	Bank   []model.TestRow
	OOB    []model.OutOfBankRow
	Stress []model.StressTestRow

	OPDOrder []string

	// BankMediaCount/OOBMediaCount/StressMediaCount = jumlah entri map metrics
	// (satu entri bisa berisi >1 path).
	BankMediaCount   int
	OOBMediaCount    int
	StressMediaCount int

	// MediaPaths = seluruh path unik dari ketiga map.
	MediaPaths []string
	// MissingMedia = path yang tidak ditemukan di disk (kandidat peringatan,
	// bukan kegagalan migrasi).
	MissingMedia []string
}

// Options mengatur cara Plan dibangun.
type Options struct {
	UserID      int64
	XLSXPath    string
	MetricsPath string // opsional; default: <xlsx tanpa .xlsx>.metrics.json
	// MediaRoots adalah direktori yang dicari saat memverifikasi path media.
	// Path relatif dari metrics ("/media/..." atau "/media/2/...") di-resolve
	// dengan membuang prefix lalu mencari nama file di root-root ini (berurutan).
	MediaRoots []string
}

// Build membaca workbook + metrics dan mengembalikan Plan tanpa menyentuh DB.
func Build(opts Options) (*Plan, error) {
	if opts.UserID <= 0 {
		return nil, errors.New("user id wajib > 0")
	}
	if strings.TrimSpace(opts.XLSXPath) == "" {
		return nil, errors.New("path workbook wajib diisi")
	}
	if _, err := os.Stat(opts.XLSXPath); err != nil {
		return nil, fmt.Errorf("workbook tidak dapat dibaca: %w", err)
	}

	metricsPath := opts.MetricsPath
	if metricsPath == "" {
		metricsPath = excel.MetricsPathFor(opts.XLSXPath)
	}
	metrics, err := excel.LoadMetricsFile(metricsPath)
	if err != nil {
		return nil, err
	}

	res, err := excel.ParseFile(opts.XLSXPath, metrics)
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		UserID:           opts.UserID,
		XLSXPath:         opts.XLSXPath,
		FileName:         filepath.Base(opts.XLSXPath),
		Bank:             res.Bank,
		OOB:              res.OOB,
		Stress:           res.Stress,
		OPDOrder:         res.OPDOrder,
		BankMediaCount:   len(metrics.BankMedia),
		OOBMediaCount:    len(metrics.OOBMedia),
		StressMediaCount: len(metrics.StressMedia),
	}
	plan.MediaPaths, plan.MissingMedia = collectMedia(metrics, opts.MediaRoots)
	return plan, nil
}

// collectMedia mengumpulkan path unik dan memeriksa keberadaannya di disk.
// Urutan hash-stable (sort) agar output dry-run deterministik.
func collectMedia(m *excel.MetricsBundle, roots []string) (all, missing []string) {
	seen := map[string]bool{}
	add := func(paths []string) {
		for _, p := range paths {
			p = strings.TrimSpace(p)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			all = append(all, p)
		}
	}
	for _, paths := range m.BankMedia {
		add(paths)
	}
	for _, paths := range m.OOBMedia {
		add(paths)
	}
	for _, paths := range m.StressMedia {
		add(paths)
	}
	sort.Strings(all)

	// Indeks nama file → path disk sekali saja, agar O(n) bukan O(n*m).
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
	for _, p := range all {
		if _, ok := index[filepath.Base(p)]; !ok {
			missing = append(missing, p)
		}
	}
	return all, missing
}

// Summary memformat hasil dry-run dalam bentuk yang dapat dibaca operator dan
// dapat dibandingkan di test.
func (p *Plan) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "user_id            : %d\n", p.UserID)
	fmt.Fprintf(&b, "workbook           : %s\n", p.XLSXPath)
	fmt.Fprintf(&b, "bank rows          : %d\n", len(p.Bank))
	fmt.Fprintf(&b, "oob rows           : %d\n", len(p.OOB))
	fmt.Fprintf(&b, "stress rows        : %d\n", len(p.Stress))
	fmt.Fprintf(&b, "opd order entries  : %d\n", len(p.OPDOrder))
	fmt.Fprintf(&b, "media entries      : bank=%d oob=%d stress=%d\n",
		p.BankMediaCount, p.OOBMediaCount, p.StressMediaCount)
	fmt.Fprintf(&b, "media paths unique : %d\n", len(p.MediaPaths))
	fmt.Fprintf(&b, "media missing      : %d\n", len(p.MissingMedia))
	for _, m := range p.MissingMedia {
		fmt.Fprintf(&b, "  MISSING %s\n", m)
	}
	return b.String()
}

// Validate menjalankan pemeriksaan kewarasan yang murah sebelum Tahap 6.
// Sengaja tidak menolak dataset kosong (user baru bisa saja belum upload),
// tetapi menolak duplikat nomor urut karena akan melanggar UNIQUE(user_id,no).
func (p *Plan) Validate() error {
	if dup := duplicateNo(len(p.Bank), func(i int) int { return p.Bank[i].No }); dup != 0 {
		return fmt.Errorf("duplikat No=%d pada bank rows", dup)
	}
	if dup := duplicateNo(len(p.OOB), func(i int) int { return p.OOB[i].No }); dup != 0 {
		return fmt.Errorf("duplikat No=%d pada oob rows", dup)
	}
	if dup := duplicateNo(len(p.Stress), func(i int) int { return p.Stress[i].No }); dup != 0 {
		return fmt.Errorf("duplikat No=%d pada stress rows", dup)
	}
	for i := range p.Bank {
		switch p.Bank[i].ResultStatus {
		case "", "Cocok", "Tidak":
		default:
			return fmt.Errorf("bank No=%d memiliki result_status tidak valid %q",
				p.Bank[i].No, p.Bank[i].ResultStatus)
		}
	}
	return nil
}

func duplicateNo(n int, get func(int) int) int {
	seen := make(map[int]bool, n)
	for i := 0; i < n; i++ {
		v := get(i)
		if seen[v] {
			return v
		}
		seen[v] = true
	}
	return 0
}
