package migrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/lib/pq"

	"whatshap-gateway/internal/model"
)

// AuditResult adalah hasil parity audit antara Plan (sumber: xlsx+metrics)
// dan isi tabel workbench.* di PostgreSQL.
type AuditResult struct {
	UserID int64

	BankExpected, BankFound     int
	OOBExpected, OOBFound       int
	StressExpected, StressFound int

	// Differences berisi deskripsi selisih yang ditemukan; kosong = paritas
	// sempurna. Dibatasi maxDiffs agar log tidak meledak.
	Differences []string
	// MediaMissing adalah path yang tidak resolve di disk (bukan selisih DB,
	// tetapi tetap dilaporkan sesuai DoD Tahap 6).
	MediaMissing []string
}

const maxDiffs = 25

func (r *AuditResult) addDiff(format string, args ...any) {
	if len(r.Differences) >= maxDiffs {
		return
	}
	r.Differences = append(r.Differences, fmt.Sprintf(format, args...))
}

// OK melaporkan apakah paritas tercapai: jumlah & isi identik.
func (r *AuditResult) OK() bool {
	return len(r.Differences) == 0 &&
		r.BankExpected == r.BankFound &&
		r.OOBExpected == r.OOBFound &&
		r.StressExpected == r.StressFound
}

// Audit membandingkan seluruh field Plan dengan isi tabel, per baris (kunci:
// no). Semua query memakai schema eksplisit dan filter user_id.
func Audit(ctx context.Context, db *sql.DB, plan *Plan) (*AuditResult, error) {
	if db == nil || plan == nil {
		return nil, fmt.Errorf("audit butuh db dan plan")
	}
	res := &AuditResult{UserID: plan.UserID}
	res.BankExpected = len(plan.Bank)
	res.OOBExpected = len(plan.OOB)
	res.StressExpected = len(plan.Stress)
	res.MediaMissing = append(res.MediaMissing, plan.MissingMedia...)

	// --- Bank ---
	bankByNo, err := loadBankRows(ctx, db, plan.UserID)
	if err != nil {
		return nil, err
	}
	res.BankFound = len(bankByNo)
	compareRows(res, "bank", plan.Bank, bankByNo,
		func(r model.TestRow) int { return r.No })

	// --- OOB ---
	oobByNo, err := loadOOBRows(ctx, db, plan.UserID)
	if err != nil {
		return nil, err
	}
	res.OOBFound = len(oobByNo)
	compareRows(res, "oob", plan.OOB, oobByNo,
		func(r model.OutOfBankRow) int { return r.No })

	// --- Stress ---
	stressByNo, err := loadStressRows(ctx, db, plan.UserID)
	if err != nil {
		return nil, err
	}
	res.StressFound = len(stressByNo)
	compareRows(res, "stress", plan.Stress, stressByNo,
		func(r model.StressTestRow) int { return r.No })

	// --- Metadata user_workbooks ---
	if err := auditMeta(ctx, db, plan, res); err != nil {
		return nil, err
	}

	return res, nil
}

// compareRows membandingkan elemen Plan ke map dari DB. Normalisasi dilakukan
// pada media_files (nil dan [] dianggap sama) agar tidak muncul false positive.
func compareRows[T any](res *AuditResult, label string, expected []T, found map[int]T, keyOf func(T) int) {
	seen := make(map[int]bool, len(expected))
	for _, want := range expected {
		no := keyOf(want)
		seen[no] = true
		got, ok := found[no]
		if !ok {
			res.addDiff("%s No=%d hilang di database", label, no)
			continue
		}
		if !equalsNormalized(want, got) {
			res.addDiff("%s No=%d berbeda: %s", label, no, firstFieldDiff(want, got))
		}
	}
	for no := range found {
		if !seen[no] {
			res.addDiff("%s No=%d ada di database tapi tidak ada di sumber", label, no)
		}
	}
}

func equalsNormalized(a, b any) bool {
	return reflect.DeepEqual(normalizeMedia(a), normalizeMedia(b))
}

// normalizeMedia membuat nil slice menjadi [] agar perbandingan dengan DB
// (yang selalu mengembalikan array kosong untuk '{}') konsisten.
func normalizeMedia(v any) any {
	switch s := v.(type) {
	case model.TestRow:
		if s.MediaFiles == nil {
			s.MediaFiles = []string{}
		}
		return s
	case model.OutOfBankRow:
		if s.MediaFiles == nil {
			s.MediaFiles = []string{}
		}
		return s
	case model.StressTestRow:
		if s.MediaFiles == nil {
			s.MediaFiles = []string{}
		}
		return s
	}
	return v
}

// firstFieldDiff menjelaskan field pertama yang berbeda supaya operator tahu
// di mana masalahnya tanpa membuka debugger.
func firstFieldDiff(a, b any) string {
	switch want := a.(type) {
	case model.TestRow:
		got := b.(model.TestRow)
		return diffFields(
			map[string]any{
				"OPD": want.OPD, "Kategori": want.Kategori, "StatusBank": want.StatusBank,
				"QuestionRef": want.QuestionRef, "QuestionInput": want.QuestionInput,
				"ExpectedReply": want.ExpectedReply, "ActualReply": want.ActualReply,
				"Notes": want.Notes, "ResultStatus": want.ResultStatus,
				"HasExpectedFile": want.HasExpectedFile, "HasActualFile": want.HasActualFile,
				"MediaFiles": want.MediaFiles, "Metrics": want.Metrics,
			},
			map[string]any{
				"OPD": got.OPD, "Kategori": got.Kategori, "StatusBank": got.StatusBank,
				"QuestionRef": got.QuestionRef, "QuestionInput": got.QuestionInput,
				"ExpectedReply": got.ExpectedReply, "ActualReply": got.ActualReply,
				"Notes": got.Notes, "ResultStatus": got.ResultStatus,
				"HasExpectedFile": got.HasExpectedFile, "HasActualFile": got.HasActualFile,
				"MediaFiles": got.MediaFiles, "Metrics": got.Metrics,
			})
	case model.OutOfBankRow:
		got := b.(model.OutOfBankRow)
		return diffFields(
			map[string]any{
				"SourceRef": want.SourceRef, "ScenarioType": want.ScenarioType,
				"ProbeQuestion": want.ProbeQuestion, "ExpectedBehavior": want.ExpectedBehavior,
				"ActualReply": want.ActualReply, "HumanSummary": want.HumanSummary,
				"Notes": want.Notes, "ResultStatus": want.ResultStatus,
				"ForbiddenYear": want.ForbiddenYear, "ReferenceYear": want.ReferenceYear,
				"MediaFiles": want.MediaFiles, "Metrics": want.Metrics,
			},
			map[string]any{
				"SourceRef": got.SourceRef, "ScenarioType": got.ScenarioType,
				"ProbeQuestion": got.ProbeQuestion, "ExpectedBehavior": got.ExpectedBehavior,
				"ActualReply": got.ActualReply, "HumanSummary": got.HumanSummary,
				"Notes": got.Notes, "ResultStatus": got.ResultStatus,
				"ForbiddenYear": got.ForbiddenYear, "ReferenceYear": got.ReferenceYear,
				"MediaFiles": got.MediaFiles, "Metrics": got.Metrics,
			})
	case model.StressTestRow:
		got := b.(model.StressTestRow)
		return diffFields(
			map[string]any{
				"StressCategory": want.StressCategory, "UserPromptQ": want.UserPromptQ,
				"ExpectedBehavior": want.ExpectedBehavior, "ActualReply": want.ActualReply,
				"Notes": want.Notes, "ResultStatus": want.ResultStatus,
				"PolitenessScore": want.PolitenessScore, "HallucinationLvl": want.HallucinationLvl,
				"MediaFiles": want.MediaFiles, "Metrics": want.Metrics,
			},
			map[string]any{
				"StressCategory": got.StressCategory, "UserPromptQ": got.UserPromptQ,
				"ExpectedBehavior": got.ExpectedBehavior, "ActualReply": got.ActualReply,
				"Notes": got.Notes, "ResultStatus": got.ResultStatus,
				"PolitenessScore": got.PolitenessScore, "HallucinationLvl": got.HallucinationLvl,
				"MediaFiles": got.MediaFiles, "Metrics": got.Metrics,
			})
	}
	return "perbedaan pada tipe tak dikenal"
}

func diffFields(want, got map[string]any) string {
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !reflect.DeepEqual(want[k], got[k]) {
			return fmt.Sprintf("%s (sumber=%v, db=%v)", k, truncateValue(want[k]), truncateValue(got[k]))
		}
	}
	return "tidak diketahui"
}

func truncateValue(v any) string {
	s := fmt.Sprintf("%v", v)
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// --- loaders ---

func loadBankRows(ctx context.Context, db *sql.DB, userID int64) (map[int]model.TestRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT no, excel_row, opd, kategori, status_bank, question_ref, question_input,
		       expected_reply, actual_reply, notes, result_status,
		       has_expected_file, has_expected_coord, has_actual_file, has_actual_coord,
		       media_files, metrics
		FROM workbench.bank_rows WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]model.TestRow{}
	for rows.Next() {
		var (
			r       model.TestRow
			metrics []byte
		)
		if err := rows.Scan(&r.No, &r.ExcelRow, &r.OPD, &r.Kategori, &r.StatusBank,
			&r.QuestionRef, &r.QuestionInput, &r.ExpectedReply, &r.ActualReply,
			&r.Notes, &r.ResultStatus, &r.HasExpectedFile, &r.HasExpectedCoord,
			&r.HasActualFile, &r.HasActualCoord, pq.Array(&r.MediaFiles), &metrics); err != nil {
			return nil, err
		}
		if len(metrics) > 0 {
			if err := json.Unmarshal(metrics, &r.Metrics); err != nil {
				return nil, fmt.Errorf("metrics bank No=%d: %w", r.No, err)
			}
		}
		out[r.No] = r
	}
	return out, rows.Err()
}

func loadOOBRows(ctx context.Context, db *sql.DB, userID int64) (map[int]model.OutOfBankRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT no, excel_row, source_ref, scenario_type, base_question, probe_question,
		       expected_behavior, actual_reply, human_summary, notes, result_status,
		       forbidden_year, reference_year, media_files, metrics
		FROM workbench.out_of_bank_rows WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]model.OutOfBankRow{}
	for rows.Next() {
		var (
			r       model.OutOfBankRow
			metrics []byte
		)
		if err := rows.Scan(&r.No, &r.ExcelRow, &r.SourceRef, &r.ScenarioType,
			&r.BaseQuestion, &r.ProbeQuestion, &r.ExpectedBehavior, &r.ActualReply,
			&r.HumanSummary, &r.Notes, &r.ResultStatus, &r.ForbiddenYear,
			&r.ReferenceYear, pq.Array(&r.MediaFiles), &metrics); err != nil {
			return nil, err
		}
		if len(metrics) > 0 {
			if err := json.Unmarshal(metrics, &r.Metrics); err != nil {
				return nil, fmt.Errorf("metrics oob No=%d: %w", r.No, err)
			}
		}
		out[r.No] = r
	}
	return out, rows.Err()
}

func loadStressRows(ctx context.Context, db *sql.DB, userID int64) (map[int]model.StressTestRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT no, excel_row, stress_category, user_prompt_q, intent_purpose,
		       expected_behavior, actual_reply, human_summary, notes, result_status,
		       politeness_score, hallucination_lvl, media_files, metrics
		FROM workbench.stress_test_rows WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]model.StressTestRow{}
	for rows.Next() {
		var (
			r       model.StressTestRow
			metrics []byte
		)
		if err := rows.Scan(&r.No, &r.ExcelRow, &r.StressCategory, &r.UserPromptQ,
			&r.IntentPurpose, &r.ExpectedBehavior, &r.ActualReply, &r.HumanSummary,
			&r.Notes, &r.ResultStatus, &r.PolitenessScore, &r.HallucinationLvl,
			pq.Array(&r.MediaFiles), &metrics); err != nil {
			return nil, err
		}
		if len(metrics) > 0 {
			if err := json.Unmarshal(metrics, &r.Metrics); err != nil {
				return nil, fmt.Errorf("metrics stress No=%d: %w", r.No, err)
			}
		}
		out[r.No] = r
	}
	return out, rows.Err()
}

func auditMeta(ctx context.Context, db *sql.DB, plan *Plan, res *AuditResult) error {
	var (
		fileName string
		opdOrder []string
	)
	err := db.QueryRowContext(ctx,
		`SELECT file_name, opd_order FROM workbench.user_workbooks WHERE user_id = $1`,
		plan.UserID).Scan(&fileName, pq.Array(&opdOrder))
	if err == sql.ErrNoRows {
		res.addDiff("user_workbooks tidak ada untuk user %d", plan.UserID)
		return nil
	}
	if err != nil {
		return err
	}
	if fileName != plan.FileName {
		res.addDiff("user_workbooks.file_name (sumber=%q, db=%q)", plan.FileName, fileName)
	}
	want := plan.OPDOrder
	if !reflect.DeepEqual(append([]string{}, want...), append([]string{}, opdOrder...)) {
		res.addDiff("user_workbooks.opd_order berbeda (sumber %d entri, db %d entri)",
			len(want), len(opdOrder))
	}
	return nil
}

// Summary memformat laporan audit untuk operator.
func (r *AuditResult) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "user_id            : %d\n", r.UserID)
	fmt.Fprintf(&b, "bank   expected/db : %d / %d\n", r.BankExpected, r.BankFound)
	fmt.Fprintf(&b, "oob    expected/db : %d / %d\n", r.OOBExpected, r.OOBFound)
	fmt.Fprintf(&b, "stress expected/db : %d / %d\n", r.StressExpected, r.StressFound)
	fmt.Fprintf(&b, "media missing      : %d\n", len(r.MediaMissing))
	for _, m := range r.MediaMissing {
		fmt.Fprintf(&b, "  MISSING %s\n", m)
	}
	fmt.Fprintf(&b, "differences        : %d\n", len(r.Differences))
	for _, d := range r.Differences {
		fmt.Fprintf(&b, "  DIFF %s\n", d)
	}
	if r.OK() {
		fmt.Fprintln(&b, "RESULT             : PARITY OK")
	} else {
		fmt.Fprintln(&b, "RESULT             : MISMATCH")
	}
	return b.String()
}
