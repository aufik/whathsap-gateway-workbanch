package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"

	"whatshap-gateway/internal/model"
)

// File ini berisi seluruh akses SQL mentah untuk DBStore. Semua pernyataan
// memakai schema eksplisit `workbench.` dan filter `user_id`.

const bankColumns = `no, excel_row, opd, kategori, status_bank, question_ref,
	question_input, expected_reply, actual_reply, notes, result_status,
	has_expected_file, has_expected_coord, has_actual_file, has_actual_coord,
	media_files, metrics`

const oobColumns = `no, excel_row, source_ref, scenario_type, base_question,
	probe_question, expected_behavior, actual_reply, human_summary, notes,
	result_status, forbidden_year, reference_year, media_files, metrics`

const stressColumns = `no, excel_row, stress_category, user_prompt_q,
	intent_purpose, expected_behavior, actual_reply, human_summary, notes,
	result_status, politeness_score, hallucination_lvl, media_files, metrics`

// --- loaders ---

func (s *DBStore) loadBank(ctx context.Context) ([]model.TestRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+bankColumns+` FROM workbench.bank_rows WHERE user_id=$1 ORDER BY no`, s.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TestRow
	for rows.Next() {
		r, err := scanBank(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *DBStore) loadOOB(ctx context.Context) ([]model.OutOfBankRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+oobColumns+` FROM workbench.out_of_bank_rows WHERE user_id=$1 ORDER BY no`, s.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.OutOfBankRow
	for rows.Next() {
		r, err := scanOOB(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *DBStore) loadStress(ctx context.Context) ([]model.StressTestRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+stressColumns+` FROM workbench.stress_test_rows WHERE user_id=$1 ORDER BY no`, s.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.StressTestRow
	for rows.Next() {
		r, err := scanStress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *DBStore) fetchBank(ctx context.Context, no int) (model.TestRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+bankColumns+` FROM workbench.bank_rows WHERE user_id=$1 AND no=$2`, s.userID, no)
	return scanBank(row)
}

func (s *DBStore) fetchOOB(ctx context.Context, no int) (model.OutOfBankRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+oobColumns+` FROM workbench.out_of_bank_rows WHERE user_id=$1 AND no=$2`, s.userID, no)
	return scanOOB(row)
}

func (s *DBStore) fetchStress(ctx context.Context, no int) (model.StressTestRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+stressColumns+` FROM workbench.stress_test_rows WHERE user_id=$1 AND no=$2`, s.userID, no)
	return scanStress(row)
}

// --- scanners ---

type rowScanner interface{ Scan(dest ...any) error }

func scanBank(sc rowScanner) (model.TestRow, error) {
	var (
		r       model.TestRow
		metrics []byte
	)
	err := sc.Scan(&r.No, &r.ExcelRow, &r.OPD, &r.Kategori, &r.StatusBank, &r.QuestionRef,
		&r.QuestionInput, &r.ExpectedReply, &r.ActualReply, &r.Notes, &r.ResultStatus,
		&r.HasExpectedFile, &r.HasExpectedCoord, &r.HasActualFile, &r.HasActualCoord,
		pq.Array(&r.MediaFiles), &metrics)
	if err != nil {
		return model.TestRow{}, err
	}
	if r.MediaFiles == nil {
		r.MediaFiles = []string{}
	}
	if len(metrics) > 0 {
		if err := json.Unmarshal(metrics, &r.Metrics); err != nil {
			return model.TestRow{}, fmt.Errorf("metrics bank No=%d: %w", r.No, err)
		}
	}
	return r, nil
}

func scanOOB(sc rowScanner) (model.OutOfBankRow, error) {
	var (
		r       model.OutOfBankRow
		metrics []byte
	)
	err := sc.Scan(&r.No, &r.ExcelRow, &r.SourceRef, &r.ScenarioType, &r.BaseQuestion,
		&r.ProbeQuestion, &r.ExpectedBehavior, &r.ActualReply, &r.HumanSummary, &r.Notes,
		&r.ResultStatus, &r.ForbiddenYear, &r.ReferenceYear, pq.Array(&r.MediaFiles), &metrics)
	if err != nil {
		return model.OutOfBankRow{}, err
	}
	if r.MediaFiles == nil {
		r.MediaFiles = []string{}
	}
	if len(metrics) > 0 {
		if err := json.Unmarshal(metrics, &r.Metrics); err != nil {
			return model.OutOfBankRow{}, fmt.Errorf("metrics oob No=%d: %w", r.No, err)
		}
	}
	return r, nil
}

func scanStress(sc rowScanner) (model.StressTestRow, error) {
	var (
		r       model.StressTestRow
		metrics []byte
	)
	err := sc.Scan(&r.No, &r.ExcelRow, &r.StressCategory, &r.UserPromptQ, &r.IntentPurpose,
		&r.ExpectedBehavior, &r.ActualReply, &r.HumanSummary, &r.Notes, &r.ResultStatus,
		&r.PolitenessScore, &r.HallucinationLvl, pq.Array(&r.MediaFiles), &metrics)
	if err != nil {
		return model.StressTestRow{}, err
	}
	if r.MediaFiles == nil {
		r.MediaFiles = []string{}
	}
	if len(metrics) > 0 {
		if err := json.Unmarshal(metrics, &r.Metrics); err != nil {
			return model.StressTestRow{}, fmt.Errorf("metrics stress No=%d: %w", r.No, err)
		}
	}
	return r, nil
}

// --- inserter (dipakai DBStore dan ReplaceFromBytes) ---

func (s *DBStore) insertBank(ctx context.Context, r model.TestRow) error {
	return s.insertBankTx(ctx, s.db, r)
}

func (s *DBStore) insertBankTx(ctx context.Context, ex execer, r model.TestRow) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO workbench.bank_rows (`+bankColumns+`, user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::jsonb,$18)`,
		r.No, r.ExcelRow, r.OPD, r.Kategori, r.StatusBank, r.QuestionRef,
		r.QuestionInput, r.ExpectedReply, r.ActualReply, r.Notes, r.ResultStatus,
		r.HasExpectedFile, r.HasExpectedCoord, r.HasActualFile, r.HasActualCoord,
		pq.Array(nonNil(r.MediaFiles)), mustJSON(r.Metrics), s.userID)
	return err
}

func (s *DBStore) insertOOB(ctx context.Context, r model.OutOfBankRow) error {
	return s.insertOOBTx(ctx, s.db, r)
}

func (s *DBStore) insertOOBTx(ctx context.Context, ex execer, r model.OutOfBankRow) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO workbench.out_of_bank_rows (`+oobColumns+`, user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,$16)`,
		r.No, r.ExcelRow, r.SourceRef, r.ScenarioType, r.BaseQuestion, r.ProbeQuestion,
		r.ExpectedBehavior, r.ActualReply, r.HumanSummary, r.Notes, r.ResultStatus,
		r.ForbiddenYear, r.ReferenceYear, pq.Array(nonNil(r.MediaFiles)),
		mustJSON(r.Metrics), s.userID)
	return err
}

func (s *DBStore) insertStress(ctx context.Context, r model.StressTestRow) error {
	return s.insertStressTx(ctx, s.db, r)
}

func (s *DBStore) insertStressTx(ctx context.Context, ex execer, r model.StressTestRow) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO workbench.stress_test_rows (`+stressColumns+`, user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::jsonb,$15)`,
		r.No, r.ExcelRow, r.StressCategory, r.UserPromptQ, r.IntentPurpose,
		r.ExpectedBehavior, r.ActualReply, r.HumanSummary, r.Notes, r.ResultStatus,
		r.PolitenessScore, r.HallucinationLvl, pq.Array(nonNil(r.MediaFiles)),
		mustJSON(r.Metrics), s.userID)
	return err
}

// --- updater ---

func (s *DBStore) updateBank(ctx context.Context, r model.TestRow) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE workbench.bank_rows SET
			excel_row=$3, opd=$4, kategori=$5, status_bank=$6, question_ref=$7,
			question_input=$8, expected_reply=$9, actual_reply=$10, notes=$11,
			result_status=$12, has_expected_file=$13, has_expected_coord=$14,
			has_actual_file=$15, has_actual_coord=$16, media_files=$17,
			metrics=$18::jsonb, updated_at=now()
		WHERE user_id=$1 AND no=$2`,
		s.userID, r.No, r.ExcelRow, r.OPD, r.Kategori, r.StatusBank, r.QuestionRef,
		r.QuestionInput, r.ExpectedReply, r.ActualReply, r.Notes, r.ResultStatus,
		r.HasExpectedFile, r.HasExpectedCoord, r.HasActualFile, r.HasActualCoord,
		pq.Array(nonNil(r.MediaFiles)), mustJSON(r.Metrics))
	return err
}

func (s *DBStore) updateOOB(ctx context.Context, r model.OutOfBankRow) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE workbench.out_of_bank_rows SET
			excel_row=$3, source_ref=$4, scenario_type=$5, base_question=$6,
			probe_question=$7, expected_behavior=$8, actual_reply=$9,
			human_summary=$10, notes=$11, result_status=$12, forbidden_year=$13,
			reference_year=$14, media_files=$15, metrics=$16::jsonb, updated_at=now()
		WHERE user_id=$1 AND no=$2`,
		s.userID, r.No, r.ExcelRow, r.SourceRef, r.ScenarioType, r.BaseQuestion,
		r.ProbeQuestion, r.ExpectedBehavior, r.ActualReply, r.HumanSummary, r.Notes,
		r.ResultStatus, r.ForbiddenYear, r.ReferenceYear,
		pq.Array(nonNil(r.MediaFiles)), mustJSON(r.Metrics))
	return err
}

func (s *DBStore) updateStress(ctx context.Context, r model.StressTestRow) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE workbench.stress_test_rows SET
			excel_row=$3, stress_category=$4, user_prompt_q=$5, intent_purpose=$6,
			expected_behavior=$7, actual_reply=$8, human_summary=$9, notes=$10,
			result_status=$11, politeness_score=$12, hallucination_lvl=$13,
			media_files=$14, metrics=$15::jsonb, updated_at=now()
		WHERE user_id=$1 AND no=$2`,
		s.userID, r.No, r.ExcelRow, r.StressCategory, r.UserPromptQ, r.IntentPurpose,
		r.ExpectedBehavior, r.ActualReply, r.HumanSummary, r.Notes, r.ResultStatus,
		r.PolitenessScore, r.HallucinationLvl, pq.Array(nonNil(r.MediaFiles)),
		mustJSON(r.Metrics))
	return err
}

// --- renumber setelah delete (menyamai perilaku excel.Store) ---

// renumberOOB menomori ulang No=1..n berurutan dan ExcelRow=No+2.
// Dua fase (offset negatif dulu) diperlukan karena UNIQUE(user_id,no).
func (s *DBStore) renumberOOB(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT no FROM workbench.out_of_bank_rows WHERE user_id=$1 ORDER BY no`, s.userID)
	if err != nil {
		return err
	}
	var nos []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		nos = append(nos, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return renumberNo(ctx, tx, s.userID, "workbench.out_of_bank_rows", nos)
}

func (s *DBStore) renumberStress(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT no FROM workbench.stress_test_rows WHERE user_id=$1 ORDER BY no`, s.userID)
	if err != nil {
		return err
	}
	var nos []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		nos = append(nos, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return renumberNo(ctx, tx, s.userID, "workbench.stress_test_rows", nos)
}

// renumberNo menerapkan No=1..n / ExcelRow=No+2 untuk daftar no lama yang
// sudah terurut. table berasal dari konstanta internal.
func renumberNo(ctx context.Context, tx *sql.Tx, userID int64, table string, nos []int) error {
	if len(nos) == 0 {
		return nil
	}
	// Fase 1: geser ke nilai sementara negatif agar tidak bentrok dengan UNIQUE.
	for _, old := range nos {
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+table+` SET no = -no WHERE user_id=$1 AND no=$2`, userID, old); err != nil {
			return err
		}
	}
	// Fase 2: tetapkan nomor final 1..n.
	for i, old := range nos {
		newNo := i + 1
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+table+` SET no=$3, excel_row=$4 WHERE user_id=$1 AND no=$2`,
			userID, -old, newNo, newNo+2); err != nil {
			return err
		}
	}
	return nil
}

// --- util ---

// execer memungkinkan insert dipakai lintas *sql.DB dan *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
