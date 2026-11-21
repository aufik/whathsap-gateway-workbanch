package migrator

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"whatshap-gateway/internal/model"
)

// openMigratorDB membuka database dari DATABASE_URL (env atau .env) dan
// memastikan schema workbench ada. Test di-skip bila tidak tersedia.
func openMigratorDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		if raw, err := os.ReadFile(filepath.Join("..", "..", "..", ".env")); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "DATABASE_URL=") {
					dsn = strings.Trim(strings.TrimPrefix(line, "DATABASE_URL="), `"'`)
					break
				}
			}
		}
	}
	if dsn == "" {
		t.Skip("DATABASE_URL tidak tersedia")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Skipf("postgres tidak dapat dihubungi: %v", err)
	}
	var ok bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables
		WHERE table_schema='workbench' AND table_name='bank_rows')`).Scan(&ok); err != nil || !ok {
		db.Close()
		t.Skip("schema workbench belum dimigrasi")
	}
	return db
}

// testUserID sengaja jauh dari id nyata. FK workbench.* -> public.users diuji
// dengan membuat user sementara di public.users lalu menghapusnya di cleanup.
const testUserID int64 = 900901

// ensureTestUser membuat baris minimal di public.users agar FK terpenuhi.
// Hanya kolom NOT NULL yang diisi; password_hash tidak dipakai login.
func ensureTestUser(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO public.users (id, username, password_hash, full_name, role)
		VALUES ($1, $2, $3, $4, 'tester')
		ON CONFLICT (id) DO NOTHING`,
		testUserID, fmt.Sprintf("etl-test-%d", testUserID), "x", "ETL Test User")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func dropTestUser(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM public.users WHERE id=$1`, testUserID); err != nil {
		t.Errorf("drop test user: %v", err)
	}
}

func cleanupWorkbenchUser(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM workbench.bank_rows WHERE user_id=$1`,
		`DELETE FROM workbench.out_of_bank_rows WHERE user_id=$1`,
		`DELETE FROM workbench.stress_test_rows WHERE user_id=$1`,
		`DELETE FROM workbench.user_workbooks WHERE user_id=$1`,
	} {
		if _, err := db.Exec(q, userID); err != nil {
			t.Errorf("cleanup %q: %v", q, err)
		}
	}
}

// buildSmallPlan membuat Plan sintetis berisi 2 bank + 1 oob + 1 stress sehingga
// test tidak bergantung pada isi workbook nyata.
func buildSmallPlan(userID int64) *Plan {
	return &Plan{
		UserID:   userID,
		FileName: "test.xlsx",
		Bank: []model.TestRow{
			{No: 1, ExcelRow: 3, OPD: "Dinas A", Kategori: "Kat 1", StatusBank: "approved",
				QuestionRef: "q1", ExpectedReply: "e1", ActualReply: "a1", ResultStatus: "Cocok",
				MediaFiles: []string{"/media/x.png"}},
			{No: 2, ExcelRow: 4, OPD: "Dinas B", Kategori: "Kat 2", StatusBank: "waiting",
				QuestionRef: "q2", ResultStatus: ""},
		},
		OOB: []model.OutOfBankRow{
			{No: 1, ExcelRow: 3, ProbeQuestion: "p1", ResultStatus: "Tidak"},
		},
		Stress: []model.StressTestRow{
			{No: 1, ExcelRow: 3, UserPromptQ: "s1", PolitenessScore: 4},
		},
		OPDOrder: []string{"Dinas A", "Dinas B"},
	}
}

func TestApplyThenAudit(t *testing.T) {
	db := openMigratorDB(t)
	defer db.Close()
	ensureTestUser(t, db)
	cleanupWorkbenchUser(t, db, testUserID)
	defer func() {
		cleanupWorkbenchUser(t, db, testUserID)
		dropTestUser(t, db)
	}()

	plan := buildSmallPlan(testUserID)
	ctx := context.Background()
	if err := Apply(ctx, db, testUserID, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}

	res, err := Audit(ctx, db, plan)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !res.OK() {
		t.Fatalf("parity gagal:\n%s", res.Summary())
	}
	if res.BankFound != 2 || res.OOBFound != 1 || res.StressFound != 1 {
		t.Fatalf("counts: bank=%d oob=%d stress=%d", res.BankFound, res.OOBFound, res.StressFound)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	db := openMigratorDB(t)
	defer db.Close()
	ensureTestUser(t, db)
	cleanupWorkbenchUser(t, db, testUserID)
	defer func() {
		cleanupWorkbenchUser(t, db, testUserID)
		dropTestUser(t, db)
	}()

	plan := buildSmallPlan(testUserID)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := Apply(ctx, db, testUserID, plan); err != nil {
			t.Fatalf("apply #%d: %v", i+1, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM workbench.bank_rows WHERE user_id=$1`, testUserID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("setelah 3x apply bank_rows harus 2, dapat %d", n)
	}
}

// TestApplyRollbackOnConflict membuktikan transaksi utuh: plan dengan duplikat
// No ditolak Validasi lebih dulu, dan plan yang gagal di tengah (pelanggaran
// UNIQUE karena No duplikat dipaksa lewat plan tanpa validasi) tidak
// meninggalkan baris parsial.
func TestApplyRollbackOnConflict(t *testing.T) {
	db := openMigratorDB(t)
	defer db.Close()
	ensureTestUser(t, db)
	cleanupWorkbenchUser(t, db, testUserID)
	defer func() {
		cleanupWorkbenchUser(t, db, testUserID)
		dropTestUser(t, db)
	}()

	plan := buildSmallPlan(testUserID)
	ctx := context.Background()
	if err := Apply(ctx, db, testUserID, plan); err != nil {
		t.Fatalf("seed apply: %v", err)
	}

	// Plan baru yang gagal di tengah insert: No duplikat memicu UNIQUE.
	bad := buildSmallPlan(testUserID)
	bad.Bank = append(bad.Bank, model.TestRow{No: 1, OPD: "Duplikat"})
	if err := Apply(ctx, db, testUserID, bad); err == nil {
		t.Fatalf("apply dengan duplikat harus gagal")
	}

	// Data lama harus utuh (rollback), bukan setengah terhapus.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM workbench.bank_rows WHERE user_id=$1`, testUserID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("rollback gagal: bank_rows=%d, seharusnya tetap 2 (data lama)", n)
	}
	var opd string
	if err := db.QueryRow(`SELECT opd FROM workbench.bank_rows WHERE user_id=$1 AND no=1`, testUserID).Scan(&opd); err != nil {
		t.Fatalf("read row 1: %v", err)
	}
	if opd != "Dinas A" {
		t.Fatalf("isi baris lama berubah: opd=%q", opd)
	}
}

func TestAuditDetectsMismatch(t *testing.T) {
	db := openMigratorDB(t)
	defer db.Close()
	ensureTestUser(t, db)
	cleanupWorkbenchUser(t, db, testUserID)
	defer func() {
		cleanupWorkbenchUser(t, db, testUserID)
		dropTestUser(t, db)
	}()

	plan := buildSmallPlan(testUserID)
	ctx := context.Background()
	if err := Apply(ctx, db, testUserID, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Ubah satu kolom di DB secara manual; audit harus mendeteksinya.
	if _, err := db.Exec(`UPDATE workbench.bank_rows SET notes='diubah manual'
		WHERE user_id=$1 AND no=1`, testUserID); err != nil {
		t.Fatalf("update: %v", err)
	}
	res, err := Audit(ctx, db, plan)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if res.OK() {
		t.Fatalf("audit harus melaporkan perbedaan")
	}
	if len(res.Differences) == 0 {
		t.Fatalf("no differences collected")
	}
}

func TestApplyRejectsWrongUser(t *testing.T) {
	db := openMigratorDB(t)
	defer db.Close()
	plan := buildSmallPlan(testUserID)
	if err := Apply(context.Background(), db, testUserID+1, plan); err == nil {
		t.Fatalf("user id mismatch harus ditolak")
	}
}

func TestTextArrayEscaping(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "{}"},
		{[]string{}, "{}"},
		{[]string{"/media/a.png"}, `{"/media/a.png"}`},
		{[]string{`a"b`}, `{"a\"b"}`},
		{[]string{`a\b`}, `{"a\\b"}`},
	}
	for _, c := range cases {
		if got := textArray(c.in); got != c.want {
			t.Fatalf("textArray(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}
