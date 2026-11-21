package workbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/lib/pq"

	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
)

// DBStore adalah implementasi Store di atas schema workbench pada PostgreSQL.
// Semua query memakai nama schema eksplisit dan filter user_id, sehingga data
// antar akun tidak pernah tercampur.
//
// Setiap mutasi dijalankan dalam satu transaksi; tulis per user diserialisasi
// oleh mu agar dua permintaan bersamaan tidak saling menimpa (plan §10).
type DBStore struct {
	db     *sql.DB
	userID int64
	mu     sync.Mutex
}

// NewDBStore membuat store untuk satu user. db harus sudah terbuka dan schema
// workbench sudah dimigrasi (Tahap 2).
func NewDBStore(db *sql.DB, userID int64) (*DBStore, error) {
	if db == nil {
		return nil, errors.New("workbench: db nil")
	}
	if userID <= 0 {
		return nil, errors.New("workbench: user id tidak valid")
	}
	return &DBStore{db: db, userID: userID}, nil
}

// UserID mengembalikan pemilik store (dipakai logging/diagnostik).
func (s *DBStore) UserID() int64 { return s.userID }

// --- Snapshot ---

func (s *DBStore) Snapshot() ([]model.TestRow, model.OverallSummary) {
	ctx := context.Background()
	rows, err := s.loadBank(ctx)
	if err != nil {
		// Interface tidak mengembalikan error (mengikuti excel.Store). Log
		// lewat summary kosong dan slice nil supaya pemanggil tetap aman.
		return nil, model.OverallSummary{FileName: s.fileName(ctx)}
	}
	order, name := s.meta(ctx)
	return rows, buildBankSummary(rows, order, name)
}

func (s *DBStore) OutOfBankSnapshot() ([]model.OutOfBankRow, model.OutOfBankSummary) {
	rows, err := s.loadOOB(context.Background())
	if err != nil {
		return nil, model.OutOfBankSummary{}
	}
	return rows, buildOOBSummary(rows)
}

func (s *DBStore) StressTestSnapshot() ([]model.StressTestRow, model.StressTestSummary) {
	rows, err := s.loadStress(context.Background())
	if err != nil {
		return nil, model.StressTestSummary{}
	}
	return rows, buildStressSummary(rows)
}

// --- Single row ---

func (s *DBStore) GetRow(no int) (model.TestRow, bool) {
	r, err := s.fetchBank(context.Background(), no)
	if err != nil {
		return model.TestRow{}, false
	}
	return r, true
}

func (s *DBStore) GetOutOfBankRow(no int) (model.OutOfBankRow, bool) {
	r, err := s.fetchOOB(context.Background(), no)
	if err != nil {
		return model.OutOfBankRow{}, false
	}
	return r, true
}

func (s *DBStore) GetStressTestRow(no int) (model.StressTestRow, bool) {
	r, err := s.fetchStress(context.Background(), no)
	if err != nil {
		return model.StressTestRow{}, false
	}
	return r, true
}

// --- Pending queue ---

func (s *DBStore) PendingRowNumbers(forceAll bool, limit int) []int {
	return s.pendingNos("workbench.bank_rows", forceAll, limit, 0, 0)
}

func (s *DBStore) PendingRowRange(fromNo, toNo int, forceAll bool) []int {
	return s.pendingNos("workbench.bank_rows", forceAll, 0, fromNo, toNo)
}

func (s *DBStore) PendingOutOfBankNumbers(forceAll bool, limit int) []int {
	return s.pendingNos("workbench.out_of_bank_rows", forceAll, limit, 0, 0)
}

func (s *DBStore) PendingOutOfBankRange(fromNo, toNo int, forceAll bool) []int {
	return s.pendingNos("workbench.out_of_bank_rows", forceAll, 0, fromNo, toNo)
}

func (s *DBStore) PendingStressNumbers(forceAll bool, limit int) []int {
	return s.pendingNos("workbench.stress_test_rows", forceAll, limit, 0, 0)
}

func (s *DBStore) PendingStressRange(fromNo, toNo int, forceAll bool) []int {
	return s.pendingNos("workbench.stress_test_rows", forceAll, 0, fromNo, toNo)
}

// pendingNos menghasilkan daftar No yang belum diuji (result_status = ”)
// atau seluruh baris bila forceAll. table berasal dari konstanta internal,
// bukan input user, sehingga aman diinterpolasi.
func (s *DBStore) pendingNos(table string, forceAll bool, limit, fromNo, toNo int) []int {
	q := "SELECT no FROM " + table + " WHERE user_id = $1"
	args := []any{s.userID}
	if !forceAll {
		q += " AND result_status = ''"
	}
	if fromNo > 0 || toNo > 0 {
		lo, hi := fromNo, toNo
		if lo <= 0 {
			lo = 1
		}
		if hi < lo {
			hi = lo
		}
		q += fmt.Sprintf(" AND no BETWEEN $%d AND $%d", len(args)+1, len(args)+2)
		args = append(args, lo, hi)
	}
	q += " ORDER BY no"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT $%d", len(args)+1)
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var no int
		if err := rows.Scan(&no); err != nil {
			return out
		}
		out = append(out, no)
	}
	return out
}

// --- Mutasi bank ---

func (s *DBStore) UpdateRow(no int, patch model.RowPatch) (model.TestRow, model.OverallSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()

	current, err := s.fetchBank(ctx, no)
	if err != nil {
		return model.TestRow{}, model.OverallSummary{}, fmt.Errorf("baris No %d tidak ditemukan", no)
	}
	if patch.QuestionInput != nil {
		current.QuestionInput = strings.TrimSpace(*patch.QuestionInput)
	}
	if patch.ActualReply != nil {
		current.ActualReply = strings.TrimSpace(*patch.ActualReply)
	}
	if patch.Notes != nil {
		current.Notes = strings.TrimSpace(*patch.Notes)
	}
	if patch.ResultStatus != nil {
		current.ResultStatus = normalizeResultStatus(*patch.ResultStatus)
	}
	if patch.MediaFiles != nil {
		current.MediaFiles = *patch.MediaFiles
	}
	if patch.Metrics != nil {
		current.Metrics = *patch.Metrics
	}
	hydrateFlags(&current)

	if err := s.updateBank(ctx, current); err != nil {
		return model.TestRow{}, model.OverallSummary{}, err
	}
	return s.rowAndSummary(ctx, no)
}

// --- Mutasi OOB ---

func (s *DBStore) AppendOutOfBankRows(items []model.OutOfBankRow) ([]model.OutOfBankRow, model.OutOfBankSummary, int, error) {
	if len(items) == 0 {
		return nil, model.OutOfBankSummary{}, 0, errors.New("tidak ada baris uji baru untuk ditambahkan")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()

	existing, err := s.loadOOB(ctx)
	if err != nil {
		return nil, model.OutOfBankSummary{}, 0, err
	}
	seen := make(map[string]bool, len(existing))
	maxNo := 0
	for _, r := range existing {
		seen[strings.ToLower(strings.TrimSpace(r.ProbeQuestion))] = true
		if r.No > maxNo {
			maxNo = r.No
		}
	}

	added := 0
	for _, it := range items {
		pq := strings.TrimSpace(it.ProbeQuestion)
		if pq == "" {
			continue
		}
		key := strings.ToLower(pq)
		if seen[key] {
			continue
		}
		seen[key] = true
		maxNo++
		it.No = maxNo
		it.ExcelRow = maxNo + 2
		it.ProbeQuestion = pq
		it.BaseQuestion = strings.TrimSpace(it.BaseQuestion)
		it.SourceRef = strings.TrimSpace(it.SourceRef)
		it.ScenarioType = strings.TrimSpace(it.ScenarioType)
		it.ExpectedBehavior = strings.TrimSpace(it.ExpectedBehavior)
		it.ResultStatus = normalizeResultStatus(it.ResultStatus)
		it.HumanSummary = formatHumanMetricsSummary(it.Metrics)
		if err := s.insertOOB(ctx, it); err != nil {
			return nil, model.OutOfBankSummary{}, 0, err
		}
		added++
	}
	rows, err := s.loadOOB(ctx)
	if err != nil {
		return nil, model.OutOfBankSummary{}, 0, err
	}
	return rows, buildOOBSummary(rows), added, nil
}

func (s *DBStore) UpdateOutOfBankRow(no int, patch model.OutOfBankPatch) (model.OutOfBankRow, model.OutOfBankSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()

	cur, err := s.fetchOOB(ctx, no)
	if err != nil {
		return model.OutOfBankRow{}, model.OutOfBankSummary{}, fmt.Errorf("baris Uji Diluar Bank #%d tidak ditemukan", no)
	}
	if patch.SourceRef != nil {
		cur.SourceRef = strings.TrimSpace(*patch.SourceRef)
	}
	if patch.ScenarioType != nil {
		cur.ScenarioType = strings.TrimSpace(*patch.ScenarioType)
	}
	if patch.BaseQuestion != nil {
		cur.BaseQuestion = strings.TrimSpace(*patch.BaseQuestion)
	}
	if patch.ProbeQuestion != nil {
		cur.ProbeQuestion = strings.TrimSpace(*patch.ProbeQuestion)
	}
	if patch.ExpectedBehavior != nil {
		cur.ExpectedBehavior = strings.TrimSpace(*patch.ExpectedBehavior)
	}
	if patch.ActualReply != nil {
		cur.ActualReply = strings.TrimSpace(*patch.ActualReply)
	}
	if patch.Notes != nil {
		cur.Notes = strings.TrimSpace(*patch.Notes)
	}
	if patch.ResultStatus != nil {
		cur.ResultStatus = normalizeResultStatus(*patch.ResultStatus)
	}
	if patch.MediaFiles != nil {
		cur.MediaFiles = *patch.MediaFiles
	}
	if patch.Metrics != nil {
		cur.Metrics = *patch.Metrics
		cur.HumanSummary = formatHumanMetricsSummary(cur.Metrics)
	}
	if err := s.updateOOB(ctx, cur); err != nil {
		return model.OutOfBankRow{}, model.OutOfBankSummary{}, err
	}
	return s.oobAndSummary(ctx, no)
}

func (s *DBStore) DeleteOutOfBankRow(no int) ([]model.OutOfBankRow, model.OutOfBankSummary, error) {
	return s.DeleteOutOfBankBatch([]int{no})
}

func (s *DBStore) DeleteOutOfBankBatch(numbers []int) ([]model.OutOfBankRow, model.OutOfBankSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()

	rows, err := s.loadOOB(ctx)
	if err != nil {
		return nil, model.OutOfBankSummary{}, err
	}
	if len(numbers) == 0 {
		return rows, buildOOBSummary(rows), nil
	}
	toDelete := make(map[int]bool, len(numbers))
	for _, n := range numbers {
		toDelete[n] = true
	}
	// Pastikan baris yang diminta ada (kontrak excel.Store mengembalikan error
	// bila No tunggal tidak ditemukan).
	found := false
	for _, r := range rows {
		if toDelete[r.No] {
			found = true
			break
		}
	}
	if !found {
		if len(numbers) == 1 {
			return nil, model.OutOfBankSummary{}, fmt.Errorf("baris Uji Diluar Bank #%d tidak ditemukan", numbers[0])
		}
		return rows, buildOOBSummary(rows), nil
	}

	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, delErr := tx.ExecContext(ctx,
			`DELETE FROM workbench.out_of_bank_rows WHERE user_id=$1 AND no = ANY($2)`,
			s.userID, pq.Array(numbers)); delErr != nil {
			return delErr
		}
		return s.renumberOOB(ctx, tx)
	}); err != nil {
		return nil, model.OutOfBankSummary{}, err
	}
	rows, err = s.loadOOB(ctx)
	if err != nil {
		return nil, model.OutOfBankSummary{}, err
	}
	return rows, buildOOBSummary(rows), nil
}

func (s *DBStore) ClearOutOfBank() ([]model.OutOfBankRow, model.OutOfBankSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM workbench.out_of_bank_rows WHERE user_id=$1`, s.userID); err != nil {
		return nil, model.OutOfBankSummary{}, err
	}
	return []model.OutOfBankRow{}, model.OutOfBankSummary{}, nil
}

// --- Mutasi stress ---

func (s *DBStore) AppendStressTestRows(items []model.StressTestRow) ([]model.StressTestRow, model.StressTestSummary, int, error) {
	if len(items) == 0 {
		return nil, model.StressTestSummary{}, 0, errors.New("tidak ada baris stress test untuk ditambahkan")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()

	existing, err := s.loadStress(ctx)
	if err != nil {
		return nil, model.StressTestSummary{}, 0, err
	}
	seen := make(map[string]bool, len(existing))
	maxNo := 0
	for _, r := range existing {
		seen[strings.ToLower(strings.TrimSpace(r.UserPromptQ))] = true
		if r.No > maxNo {
			maxNo = r.No
		}
	}
	added := 0
	for _, it := range items {
		pq := strings.TrimSpace(it.UserPromptQ)
		if pq == "" {
			continue
		}
		key := strings.ToLower(pq)
		if seen[key] {
			continue
		}
		seen[key] = true
		maxNo++
		it.No = maxNo
		it.ExcelRow = maxNo + 2
		it.UserPromptQ = pq
		it.StressCategory = strings.TrimSpace(it.StressCategory)
		it.IntentPurpose = strings.TrimSpace(it.IntentPurpose)
		it.ExpectedBehavior = strings.TrimSpace(it.ExpectedBehavior)
		it.ResultStatus = normalizeResultStatus(it.ResultStatus)
		it.HumanSummary = formatHumanMetricsSummary(it.Metrics)
		if err := s.insertStress(ctx, it); err != nil {
			return nil, model.StressTestSummary{}, 0, err
		}
		added++
	}
	rows, err := s.loadStress(ctx)
	if err != nil {
		return nil, model.StressTestSummary{}, 0, err
	}
	return rows, buildStressSummary(rows), added, nil
}

func (s *DBStore) UpdateStressTestRow(no int, patch model.StressTestPatch) (model.StressTestRow, model.StressTestSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()

	cur, err := s.fetchStress(ctx, no)
	if err != nil {
		return model.StressTestRow{}, model.StressTestSummary{}, fmt.Errorf("baris Uji Stress AI #%d tidak ditemukan", no)
	}
	if patch.StressCategory != nil {
		cur.StressCategory = strings.TrimSpace(*patch.StressCategory)
	}
	if patch.UserPromptQ != nil {
		cur.UserPromptQ = strings.TrimSpace(*patch.UserPromptQ)
	}
	if patch.IntentPurpose != nil {
		cur.IntentPurpose = strings.TrimSpace(*patch.IntentPurpose)
	}
	if patch.ExpectedBehavior != nil {
		cur.ExpectedBehavior = strings.TrimSpace(*patch.ExpectedBehavior)
	}
	if patch.ActualReply != nil {
		cur.ActualReply = strings.TrimSpace(*patch.ActualReply)
	}
	if patch.Notes != nil {
		cur.Notes = strings.TrimSpace(*patch.Notes)
	}
	if patch.ResultStatus != nil {
		cur.ResultStatus = normalizeResultStatus(*patch.ResultStatus)
	}
	if patch.PolitenessScore != nil {
		cur.PolitenessScore = *patch.PolitenessScore
	}
	if patch.HallucinationLvl != nil {
		cur.HallucinationLvl = strings.TrimSpace(*patch.HallucinationLvl)
	}
	if patch.MediaFiles != nil {
		cur.MediaFiles = *patch.MediaFiles
	}
	if patch.Metrics != nil {
		cur.Metrics = *patch.Metrics
		cur.HumanSummary = formatHumanMetricsSummary(cur.Metrics)
	}
	if err := s.updateStress(ctx, cur); err != nil {
		return model.StressTestRow{}, model.StressTestSummary{}, err
	}
	return s.stressAndSummary(ctx, no)
}

func (s *DBStore) DeleteStressTestRow(no int) ([]model.StressTestRow, model.StressTestSummary, error) {
	return s.DeleteStressTestBatch([]int{no})
}

func (s *DBStore) DeleteStressTestBatch(numbers []int) ([]model.StressTestRow, model.StressTestSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()

	rows, err := s.loadStress(ctx)
	if err != nil {
		return nil, model.StressTestSummary{}, err
	}
	if len(numbers) == 0 {
		return rows, buildStressSummary(rows), nil
	}
	toDelete := make(map[int]bool, len(numbers))
	for _, n := range numbers {
		toDelete[n] = true
	}
	found := false
	for _, r := range rows {
		if toDelete[r.No] {
			found = true
			break
		}
	}
	if !found {
		if len(numbers) == 1 {
			return nil, model.StressTestSummary{}, fmt.Errorf("baris Uji Stress AI #%d tidak ditemukan", numbers[0])
		}
		return rows, buildStressSummary(rows), nil
	}
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, delErr := tx.ExecContext(ctx,
			`DELETE FROM workbench.stress_test_rows WHERE user_id=$1 AND no = ANY($2)`,
			s.userID, pq.Array(numbers)); delErr != nil {
			return delErr
		}
		return s.renumberStress(ctx, tx)
	}); err != nil {
		return nil, model.StressTestSummary{}, err
	}
	rows, err = s.loadStress(ctx)
	if err != nil {
		return nil, model.StressTestSummary{}, err
	}
	return rows, buildStressSummary(rows), nil
}

func (s *DBStore) ClearStressTest() ([]model.StressTestRow, model.StressTestSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM workbench.stress_test_rows WHERE user_id=$1`, s.userID); err != nil {
		return nil, model.StressTestSummary{}, err
	}
	return []model.StressTestRow{}, model.StressTestSummary{}, nil
}

// --- Upload & export ---

// ReplaceFromBytes mem-parse workbook dengan parser bersama (tanpa menulis
// file), lalu mengganti seluruh data user dalam satu transaksi.
func (s *DBStore) ReplaceFromBytes(uploaded []byte) (model.OverallSummary, error) {
	if len(uploaded) == 0 {
		return model.OverallSummary{}, errors.New("file kosong")
	}
	res, err := excel.ParseBytes(uploaded, nil)
	if err != nil {
		return model.OverallSummary{}, err
	}
	if len(res.Bank) == 0 {
		return model.OverallSummary{}, errors.New("tidak ada baris pertanyaan yang valid pada sheet Bank Pertanyaan")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()

	name := "workbook.xlsx"
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		for _, t := range []string{
			"workbench.bank_rows", "workbench.out_of_bank_rows", "workbench.stress_test_rows",
		} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE user_id=$1`, s.userID); err != nil {
				return err
			}
		}
		for _, r := range res.Bank {
			hydrateFlags(&r)
			if err := s.insertBankTx(ctx, tx, r); err != nil {
				return err
			}
		}
		for _, r := range res.OOB {
			r.HumanSummary = strings.TrimSpace(r.HumanSummary)
			if r.HumanSummary == "" {
				r.HumanSummary = formatHumanMetricsSummary(r.Metrics)
			}
			if err := s.insertOOBTx(ctx, tx, r); err != nil {
				return err
			}
		}
		for _, r := range res.Stress {
			r.HumanSummary = strings.TrimSpace(r.HumanSummary)
			if r.HumanSummary == "" {
				r.HumanSummary = formatHumanMetricsSummary(r.Metrics)
			}
			if err := s.insertStressTx(ctx, tx, r); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO workbench.user_workbooks (user_id, file_name, opd_order)
			VALUES ($1,$2,$3)
			ON CONFLICT (user_id) DO UPDATE
			SET file_name=excluded.file_name, opd_order=excluded.opd_order,
			    imported_at=now(), updated_at=now()`,
			s.userID, name, pq.Array(res.OPDOrder))
		return err
	}); err != nil {
		return model.OverallSummary{}, err
	}

	rows, err := s.loadBank(ctx)
	if err != nil {
		return model.OverallSummary{}, err
	}
	_, fileName := s.meta(ctx)
	return buildBankSummary(rows, res.OPDOrder, fileName), nil
}

// ExportBytes menghasilkan workbook di memori dari data PostgreSQL.
// Diimplementasikan pada Tahap 8 (internal/workbench/export.go); untuk saat
// ini mengembalikan error eksplisit agar tidak diam-diam salah.
func (s *DBStore) ExportBytes() ([]byte, string, error) {
	return s.exportWorkbook()
}

// --- internal helpers (implementasi di dbstore_queries.go) ---

func (s *DBStore) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *DBStore) fileName(ctx context.Context) string {
	_, name := s.meta(ctx)
	return name
}

func (s *DBStore) meta(ctx context.Context) (opdOrder []string, fileName string) {
	err := s.db.QueryRowContext(ctx,
		`SELECT opd_order, file_name FROM workbench.user_workbooks WHERE user_id=$1`,
		s.userID).Scan(pq.Array(&opdOrder), &fileName)
	if err != nil {
		return nil, "workbook.xlsx"
	}
	return opdOrder, fileName
}

func (s *DBStore) rowAndSummary(ctx context.Context, no int) (model.TestRow, model.OverallSummary, error) {
	row, err := s.fetchBank(ctx, no)
	if err != nil {
		return model.TestRow{}, model.OverallSummary{}, err
	}
	rows, err := s.loadBank(ctx)
	if err != nil {
		return model.TestRow{}, model.OverallSummary{}, err
	}
	order, name := s.meta(ctx)
	return row, buildBankSummary(rows, order, name), nil
}

func (s *DBStore) oobAndSummary(ctx context.Context, no int) (model.OutOfBankRow, model.OutOfBankSummary, error) {
	row, err := s.fetchOOB(ctx, no)
	if err != nil {
		return model.OutOfBankRow{}, model.OutOfBankSummary{}, err
	}
	rows, err := s.loadOOB(ctx)
	if err != nil {
		return model.OutOfBankRow{}, model.OutOfBankSummary{}, err
	}
	return row, buildOOBSummary(rows), nil
}

func (s *DBStore) stressAndSummary(ctx context.Context, no int) (model.StressTestRow, model.StressTestSummary, error) {
	row, err := s.fetchStress(ctx, no)
	if err != nil {
		return model.StressTestRow{}, model.StressTestSummary{}, err
	}
	rows, err := s.loadStress(ctx)
	if err != nil {
		return model.StressTestRow{}, model.StressTestSummary{}, err
	}
	return row, buildStressSummary(rows), nil
}

// mustJSON meng-encode metrics untuk kolom JSONB.
func mustJSON(m model.RowHumanMetrics) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}
