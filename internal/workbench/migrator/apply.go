package migrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"whatshap-gateway/internal/model"
)

// Apply menulis Plan ke schema workbench dalam SATU transaksi.
//
// Urutan: DELETE seluruh baris milik user (idempoten, menjamin tidak ada
// duplikat meski dijalankan ulang), lalu INSERT ulang dari Plan, lalu
// UPSERT metadata user_workbooks (file_name + opd_order).
//
// Semua pernyataan memakai schema eksplisit `workbench.` dan WHERE user_id
// pada DELETE. Bila satu langkah gagal, seluruh transaksi di-rollback:
// data lama (jika ada) tetap utuh.
func Apply(ctx context.Context, db *sql.DB, userID int64, plan *Plan) error {
	if db == nil || plan == nil {
		return fmt.Errorf("apply butuh db dan plan")
	}
	if plan.UserID != userID {
		return fmt.Errorf("user id plan (%d) tidak sama dengan target (%d)", plan.UserID, userID)
	}
	if err := plan.Validate(); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// 1. Bersihkan data lama user ini (aman bila belum ada baris).
	for _, table := range []string{
		"workbench.bank_rows",
		"workbench.out_of_bank_rows",
		"workbench.stress_test_rows",
	} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE user_id = $1`, userID); err != nil {
			return fmt.Errorf("hapus %s: %w", table, err)
		}
	}

	// 2. Insert bank rows.
	for _, r := range plan.Bank {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workbench.bank_rows (
				user_id, no, excel_row, opd, kategori, status_bank,
				question_ref, question_input, expected_reply, actual_reply, notes,
				result_status, has_expected_file, has_expected_coord,
				has_actual_file, has_actual_coord, media_files, metrics
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18::jsonb)`,
			userID, r.No, r.ExcelRow, r.OPD, r.Kategori, r.StatusBank,
			r.QuestionRef, r.QuestionInput, r.ExpectedReply, r.ActualReply, r.Notes,
			r.ResultStatus, r.HasExpectedFile, r.HasExpectedCoord,
			r.HasActualFile, r.HasActualCoord,
			textArray(r.MediaFiles), mustJSON(r.Metrics),
		); err != nil {
			return fmt.Errorf("insert bank No=%d: %w", r.No, err)
		}
	}

	// 3. Insert out-of-bank rows.
	for _, r := range plan.OOB {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workbench.out_of_bank_rows (
				user_id, no, excel_row, source_ref, scenario_type, base_question,
				probe_question, expected_behavior, actual_reply, human_summary,
				notes, result_status, forbidden_year, reference_year,
				media_files, metrics
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb)`,
			userID, r.No, r.ExcelRow, r.SourceRef, r.ScenarioType, r.BaseQuestion,
			r.ProbeQuestion, r.ExpectedBehavior, r.ActualReply, r.HumanSummary,
			r.Notes, r.ResultStatus, r.ForbiddenYear, r.ReferenceYear,
			textArray(r.MediaFiles), mustJSON(r.Metrics),
		); err != nil {
			return fmt.Errorf("insert oob No=%d: %w", r.No, err)
		}
	}

	// 4. Insert stress rows.
	for _, r := range plan.Stress {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workbench.stress_test_rows (
				user_id, no, excel_row, stress_category, user_prompt_q,
				intent_purpose, expected_behavior, actual_reply, human_summary,
				notes, result_status, politeness_score, hallucination_lvl,
				media_files, metrics
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb)`,
			userID, r.No, r.ExcelRow, r.StressCategory, r.UserPromptQ,
			r.IntentPurpose, r.ExpectedBehavior, r.ActualReply, r.HumanSummary,
			r.Notes, r.ResultStatus, r.PolitenessScore, r.HallucinationLvl,
			textArray(r.MediaFiles), mustJSON(r.Metrics),
		); err != nil {
			return fmt.Errorf("insert stress No=%d: %w", r.No, err)
		}
	}

	// 5. Upsert metadata + urutan OPD.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workbench.user_workbooks (user_id, file_name, opd_order)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE
		SET file_name = excluded.file_name,
		    opd_order = excluded.opd_order,
		    imported_at = now(),
		    updated_at = now()`,
		userID, plan.FileName, textArray(plan.OPDOrder),
	); err != nil {
		return fmt.Errorf("upsert user_workbooks: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// mustJSON meng-encode metrics ke JSON. RowHumanMetrics hanya berisi tipe
// primitif + satu pointer struct, jadi marshal tidak bisa gagal.
func mustJSON(m model.RowHumanMetrics) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// textArray mengubah []string menjadi literal Postgres array, memakai
// parameter tunggal (bukan pq.Array) agar tidak perlu import driver di sini.
func textArray(v []string) string {
	if len(v) == 0 {
		return "{}"
	}
	quoted := make([]string, 0, len(v))
	for _, s := range v {
		quoted = append(quoted, `"`+strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`)+`"`)
	}
	return "{" + strings.Join(quoted, ",") + "}"
}
