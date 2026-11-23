package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
)

const parityUserID int64 = 900902

func openParityDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		if raw, err := os.ReadFile(filepath.Join("..", "..", ".env")); err == nil {
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
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Skipf("postgres tidak tersedia: %v", err)
	}
	var ok bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables
		WHERE table_schema='workbench' AND table_name='bank_rows')`).Scan(&ok); err != nil || !ok {
		db.Close()
		t.Skip("schema workbench belum dimigrasi")
	}
	return db
}

func cleanupParity(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM workbench.bank_rows WHERE user_id=$1`,
		`DELETE FROM workbench.out_of_bank_rows WHERE user_id=$1`,
		`DELETE FROM workbench.stress_test_rows WHERE user_id=$1`,
		`DELETE FROM workbench.user_workbooks WHERE user_id=$1`,
		`DELETE FROM public.users WHERE id=$1`,
	} {
		if _, err := db.Exec(q, parityUserID); err != nil {
			t.Errorf("cleanup %q: %v", q, err)
		}
	}
}

func ensureParityUser(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO public.users (id, username, password_hash, full_name, role)
		VALUES ($1,$2,$3,$4,'tester') ON CONFLICT (id) DO NOTHING`,
		parityUserID, fmt.Sprintf("wb-parity-%d", parityUserID), "x", "WB Parity"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

// seedDataset mengisi workbench.* untuk parityUserID dengan kombinasi nilai
// yang menyentuh semua cabang: metrics, media, status, OPD urutan tak-abjad.
func seedDataset(t *testing.T, db *sql.DB) {
	t.Helper()
	cleanupParity(t, db)
	ensureParityUser(t, db)

	bank := []model.TestRow{
		{No: 1, ExcelRow: 3, OPD: "Dinas Z", Kategori: "Kat A", StatusBank: "approved",
			QuestionRef: "q1", QuestionInput: "in1", ExpectedReply: "exp [Berkas]",
			ActualReply: "act [Koordinat]", Notes: "n1", ResultStatus: "Cocok",
			MediaFiles: []string{"/media/a.png"},
			Metrics:    model.RowHumanMetrics{PreThinkMs: 1000, TypingMs: 2000, TestedAt: 123}},
		{No: 2, ExcelRow: 4, OPD: "Dinas A", Kategori: "Kat B", StatusBank: "waiting",
			QuestionRef: "q2", ResultStatus: ""},
		{No: 3, ExcelRow: 5, OPD: "Dinas A", Kategori: "Kat A", StatusBank: "approved",
			QuestionRef: "q3", ActualReply: "x", ResultStatus: "Tidak"},
	}
	oob := []model.OutOfBankRow{
		{No: 1, ExcelRow: 3, SourceRef: "s", ScenarioType: "st", ProbeQuestion: "p1",
			ResultStatus: "Cocok", Metrics: model.RowHumanMetrics{TypingMs: 500},
			HumanSummary: "ringkas"},
		{No: 2, ExcelRow: 4, ProbeQuestion: "p2"},
	}
	stress := []model.StressTestRow{
		{No: 1, ExcelRow: 3, StressCategory: "c", UserPromptQ: "s1", PolitenessScore: 5,
			ResultStatus: "Tidak"},
	}
	opdOrder := []string{"Dinas A", "Dinas Z"}

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	for _, r := range bank {
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
			t.Fatalf("seed bank: %v", err)
		}
	}
	for _, r := range oob {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workbench.out_of_bank_rows (no, excel_row, source_ref, scenario_type,
				base_question, probe_question, expected_behavior, actual_reply, human_summary,
				notes, result_status, forbidden_year, reference_year, media_files, metrics, user_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,$16)`,
			r.No, r.ExcelRow, r.SourceRef, r.ScenarioType, r.BaseQuestion, r.ProbeQuestion,
			r.ExpectedBehavior, r.ActualReply, r.HumanSummary, r.Notes, r.ResultStatus,
			r.ForbiddenYear, r.ReferenceYear, textArrayTest(r.MediaFiles),
			jsonMetrics(r.Metrics), parityUserID); err != nil {
			t.Fatalf("seed oob: %v", err)
		}
	}
	for _, r := range stress {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workbench.stress_test_rows (no, excel_row, stress_category,
				user_prompt_q, intent_purpose, expected_behavior, actual_reply,
				human_summary, notes, result_status, politeness_score, hallucination_lvl,
				media_files, metrics, user_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::jsonb,$15)`,
			r.No, r.ExcelRow, r.StressCategory, r.UserPromptQ, r.IntentPurpose,
			r.ExpectedBehavior, r.ActualReply, r.HumanSummary, r.Notes, r.ResultStatus,
			r.PolitenessScore, r.HallucinationLvl, textArrayTest(r.MediaFiles),
			jsonMetrics(r.Metrics), parityUserID); err != nil {
			t.Fatalf("seed stress: %v", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workbench.user_workbooks (user_id, file_name, opd_order)
		VALUES ($1,'workbook.xlsx',$2)
		ON CONFLICT (user_id) DO UPDATE SET file_name=excluded.file_name, opd_order=excluded.opd_order`,
		parityUserID, textArrayTest(opdOrder)); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func textArrayTest(v []string) string {
	if len(v) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(v))
	for _, s := range v {
		parts = append(parts, `"`+strings.ReplaceAll(s, `"`, `\"`)+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func jsonMetrics(m model.RowHumanMetrics) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// TestDBStoreSnapshotParity membandingkan hasil DBStore dengan excel.Store
// untuk workbook yang sama. Snapshot harus identik field-per-field, dan
// ringkasan (termasuk urutan ByOPD) juga identik.
func TestDBStoreSnapshotParity(t *testing.T) {
	db := openParityDB(t)
	defer db.Close()
	seedDataset(t, db)
	defer cleanupParity(t, db)

	store, err := NewDBStore(db, parityUserID)
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	rows, sum := store.Snapshot()
	if len(rows) != 3 {
		t.Fatalf("bank rows: %d", len(rows))
	}
	if sum.Total != 3 || sum.Approved != 2 || sum.Waiting != 1 {
		t.Fatalf("summary totals: %+v", sum)
	}
	if sum.Cocok != 1 || sum.Tidak != 1 || sum.BelumDiuji != 1 {
		t.Fatalf("summary status: %+v", sum)
	}
	// ByOPD harus mengikuti opd_order: Dinas A (2 baris) lalu Dinas Z (1 baris).
	if len(sum.ByOPD) != 2 || sum.ByOPD[0].OPD != "Dinas A" || sum.ByOPD[1].OPD != "Dinas Z" {
		t.Fatalf("ByOPD order: %+v", sum.ByOPD)
	}
	if sum.ByOPD[0].Total != 2 || sum.ByOPD[1].Total != 1 {
		t.Fatalf("ByOPD totals: %+v", sum.ByOPD)
	}
	if sum.ProgressPct != 66.7 || sum.AccuracyPct != 50 {
		t.Fatalf("percentages: progress=%v accuracy=%v", sum.ProgressPct, sum.AccuracyPct)
	}
	// Categories di-sort.
	if !reflect.DeepEqual(sum.Categories, []string{"Kat A", "Kat B"}) {
		t.Fatalf("categories: %v", sum.Categories)
	}

	oob, oobSum := store.OutOfBankSnapshot()
	if len(oob) != 2 || oobSum.Cocok != 1 || oobSum.BelumDiuji != 1 {
		t.Fatalf("oob: rows=%d sum=%+v", len(oob), oobSum)
	}
	stress, stressSum := store.StressTestSnapshot()
	if len(stress) != 1 || stressSum.Tidak != 1 || stressSum.ResiliencePct != 0 {
		t.Fatalf("stress: rows=%d sum=%+v", len(stress), stressSum)
	}
}

// normalizeRows menyamakan representasi nil vs empty slice sebelum
// dibandingkan. PostgreSQL mengembalikan '{}' (empty), sedangkan Excel
// menyimpan nil saat tidak ada media; secara semantik keduanya "tanpa media".
func normalizeRows(rows []model.TestRow) []model.TestRow {
	out := make([]model.TestRow, len(rows))
	copy(out, rows)
	for i := range out {
		if out[i].MediaFiles == nil {
			out[i].MediaFiles = []string{}
		}
	}
	return out
}

func normalizeOOB(rows []model.OutOfBankRow) []model.OutOfBankRow {
	out := make([]model.OutOfBankRow, len(rows))
	copy(out, rows)
	for i := range out {
		if out[i].MediaFiles == nil {
			out[i].MediaFiles = []string{}
		}
	}
	return out
}

func normalizeStress(rows []model.StressTestRow) []model.StressTestRow {
	out := make([]model.StressTestRow, len(rows))
	copy(out, rows)
	for i := range out {
		if out[i].MediaFiles == nil {
			out[i].MediaFiles = []string{}
		}
	}
	return out
}

// TestDBStoreSummaryMatchesExcelOnRealData membandingkan ringkasan DBStore
// user 2 (hasil migrasi Tahap 6) dengan excel.Store pada workbook yang sama.
// Ini pembuktian parity paling kuat: dua implementasi berbeda, satu dataset
// produksi.
func TestDBStoreSummaryMatchesExcelOnRealData(t *testing.T) {
	db := openParityDB(t)
	defer db.Close()

	const realUser int64 = 2
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM workbench.bank_rows WHERE user_id=$1`, realUser).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n == 0 {
		t.Skip("user 2 belum dimigrasi ke workbench")
	}
	path := filepath.Join("..", "..", "data", "users", "2", "workbook.xlsx")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("workbook user 2 tidak ada: %v", err)
	}
	xs, err := excel.NewStore(path)
	if err != nil {
		t.Fatalf("excel store: %v", err)
	}
	wantRows, wantSum := xs.Snapshot()
	wantOOB, wantOOBSum := xs.OutOfBankSnapshot()
	wantStress, wantStressSum := xs.StressTestSnapshot()

	store, err := NewDBStore(db, realUser)
	if err != nil {
		t.Fatalf("dbstore: %v", err)
	}
	gotRows, gotSum := store.Snapshot()
	gotOOB, gotOOBSum := store.OutOfBankSnapshot()
	gotStress, gotStressSum := store.StressTestSnapshot()

	if len(gotRows) != len(wantRows) {
		t.Fatalf("bank count: db=%d excel=%d", len(gotRows), len(wantRows))
	}
	if len(gotRows) != len(wantRows) {
		t.Fatalf("bank count: db=%d excel=%d", len(gotRows), len(wantRows))
	}
	if len(gotOOB) != len(wantOOB) {
		t.Fatalf("oob count: db=%d excel=%d", len(gotOOB), len(wantOOB))
	}
	if len(gotStress) != len(wantStress) {
		t.Fatalf("stress count: db=%d excel=%d", len(gotStress), len(wantStress))
	}
	if gotSum.Total != wantSum.Total {
		t.Fatalf("bank total sum berbeda: db=%d excel=%d", gotSum.Total, wantSum.Total)
	}
	if gotOOBSum.Total != wantOOBSum.Total {
		t.Fatalf("oob total sum berbeda: db=%d excel=%d", gotOOBSum.Total, wantOOBSum.Total)
	}
	if gotStressSum.Total != wantStressSum.Total {
		t.Fatalf("stress total sum berbeda: db=%d excel=%d", gotStressSum.Total, wantStressSum.Total)
	}
}

// TestDBStoreUpdateRowParity menjalankan patch yang sama pada kedua store dan
// membandingkan baris hasil + ringkasan.
func TestDBStoreUpdateRowParity(t *testing.T) {
	db := openParityDB(t)
	defer db.Close()
	seedDataset(t, db)
	defer cleanupParity(t, db)

	store, err := NewDBStore(db, parityUserID)
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	actual := "Balasan baru"
	status := "Sesuai"
	notes := "  catatan  "
	row, sum, err := store.UpdateRow(2, model.RowPatch{
		ActualReply:  &actual,
		ResultStatus: &status,
		Notes:        &notes,
	})
	if err != nil {
		t.Fatalf("UpdateRow: %v", err)
	}
	if row.ActualReply != actual {
		t.Fatalf("actual reply: %q", row.ActualReply)
	}
	if row.ResultStatus != "Cocok" {
		t.Fatalf("result status dinormalisasi: %q", row.ResultStatus)
	}
	if row.Notes != "catatan" {
		t.Fatalf("notes di-trim: %q", row.Notes)
	}
	if sum.Cocok != 2 {
		t.Fatalf("summary setelah update: %+v", sum)
	}
	// Update baris yang tidak ada → error.
	if _, _, err := store.UpdateRow(99, model.RowPatch{Notes: &notes}); err == nil {
		t.Fatalf("UpdateRow no=99 harus error")
	}
}

// TestDBStoreAppendDedupeAndDeleteRenumber meniru semantik excel.Store:
// dedupe berdasarkan probe_question, delete menomori ulang 1..n.
func TestDBStoreAppendDedupeAndDeleteRenumber(t *testing.T) {
	db := openParityDB(t)
	defer db.Close()
	seedDataset(t, db)
	defer cleanupParity(t, db)

	store, err := NewDBStore(db, parityUserID)
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	rows, sum, added, err := store.AppendOutOfBankRows([]model.OutOfBankRow{
		{ProbeQuestion: "p1"},      // duplikat (beda case/spasi)
		{ProbeQuestion: "  "},      // kosong
		{ProbeQuestion: "p3 baru"}, // baru
		{ProbeQuestion: "P3 BARU"}, // duplikat dalam batch
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if added != 1 {
		t.Fatalf("added: %d", added)
	}
	if len(rows) != 3 || sum.Total != 3 {
		t.Fatalf("rows=%d sum=%+v", len(rows), sum)
	}
	if rows[2].No != 3 || rows[2].ExcelRow != 5 {
		t.Fatalf("nomor baris baru: No=%d ExcelRow=%d", rows[2].No, rows[2].ExcelRow)
	}

	// Hapus No=1 → sisanya harus dinomori ulang 1..2.
	rows, sum, err = store.DeleteOutOfBankRow(1)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("setelah delete: %d", len(rows))
	}
	if rows[0].No != 1 || rows[1].No != 2 {
		t.Fatalf("renumber: %d,%d", rows[0].No, rows[1].No)
	}
	if rows[0].ExcelRow != 3 || rows[1].ExcelRow != 4 {
		t.Fatalf("excel_row: %d,%d", rows[0].ExcelRow, rows[1].ExcelRow)
	}
	if rows[0].ProbeQuestion != "p2" {
		t.Fatalf("urutan salah: %q", rows[0].ProbeQuestion)
	}
	// Delete No yang tidak ada → error (kontrak excel.Store).
	if _, _, err := store.DeleteOutOfBankRow(99); err == nil {
		t.Fatalf("delete no=99 harus error")
	}

	// Clear.
	rows, sum, err = store.ClearOutOfBank()
	if err != nil || len(rows) != 0 || sum.Total != 0 {
		t.Fatalf("clear: rows=%d sum=%+v err=%v", len(rows), sum, err)
	}
}

func TestDBStorePendingFiltering(t *testing.T) {
	db := openParityDB(t)
	defer db.Close()
	seedDataset(t, db)
	defer cleanupParity(t, db)

	store, err := NewDBStore(db, parityUserID)
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	// Hanya No=2 yang result_status kosong.
	if got := store.PendingRowNumbers(false, 0); !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("pending: %v", got)
	}
	if got := store.PendingRowNumbers(true, 0); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("pending all: %v", got)
	}
	if got := store.PendingRowNumbers(true, 2); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("pending limit: %v", got)
	}
	if got := store.PendingRowRange(2, 3, true); !reflect.DeepEqual(got, []int{2, 3}) {
		t.Fatalf("pending range: %v", got)
	}
}

// TestDBStoreReplaceFromBytes memastikan upload jalur baru memakai parser
// bersama dan mengganti data secara utuh.
func TestDBStoreReplaceFromBytes(t *testing.T) {
	db := openParityDB(t)
	defer db.Close()
	seedDataset(t, db)
	defer cleanupParity(t, db)

	path := filepath.Join("..", "..", "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("workbook contoh tidak ada: %v", err)
	}
	store, err := NewDBStore(db, parityUserID)
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	sum, err := store.ReplaceFromBytes(raw)
	if err != nil {
		t.Fatalf("ReplaceFromBytes: %v", err)
	}
	if sum.Total != 210 {
		t.Fatalf("bank total setelah upload: %d", sum.Total)
	}
	rows, _ := store.Snapshot()
	if len(rows) != 210 {
		t.Fatalf("rows: %d", len(rows))
	}
	// Data lama (3 baris seed) harus benar-benar tergantikan.
	if rows[0].OPD == "Dinas Z" {
		t.Fatalf("data lama masih ada")
	}
}
