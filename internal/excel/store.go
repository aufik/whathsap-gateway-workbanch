package excel

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/xuri/excelize/v2"

	"whatshap-gateway/internal/model"
)

const (
	SheetBank      = "Bank Pertanyaan"
	SheetRingkasan = "Ringkasan"
)

type Store struct {
	mu       sync.RWMutex
	filePath string
	rows     []model.TestRow
	opdOrder []string
}

func NewStore(filePath string) (*Store, error) {
	if err := ensureBackup(filePath); err != nil {
		return nil, fmt.Errorf("create safety backup: %w", err)
	}
	s := &Store{filePath: filePath}
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func BackupPathFor(filePath string) string {
	ext := filepath.Ext(filePath)
	base := strings.TrimSuffix(filePath, ext)
	if ext == "" {
		ext = ".xlsx"
	}
	return base + ".backup" + ext
}

func ensureBackup(filePath string) error {
	backupPath := BackupPathFor(filePath)
	if _, err := os.Stat(backupPath); err == nil {
		return nil
	}
	src, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer src.Close()

	tmp := backupPath + ".tmp"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, backupPath)
}

func (s *Store) reloadLocked() error {
	f, err := excelize.OpenFile(s.filePath)
	if err != nil {
		return fmt.Errorf("open excel %s: %w", s.filePath, err)
	}
	defer f.Close()

	return s.parseWorkbookLocked(f)
}

func (s *Store) parseWorkbookLocked(f *excelize.File) error {
	idx, err := f.GetSheetIndex(SheetBank)
	if err != nil || idx < 0 {
		return fmt.Errorf("sheet %q tidak ditemukan di file Excel", SheetBank)
	}

	rawRows, err := f.GetRows(SheetBank)
	if err != nil {
		return fmt.Errorf("read sheet %q: %w", SheetBank, err)
	}

	parsed := make([]model.TestRow, 0, len(rawRows))
	for i := 2; i < len(rawRows); i++ {
		cols := rawRows[i]
		getCol := func(c int) string {
			if c < len(cols) {
				return strings.TrimSpace(cols[c])
			}
			return ""
		}

		noStr := getCol(0)
		opd := getCol(1)
		questionRef := getCol(4)
		if noStr == "" && opd == "" && questionRef == "" {
			continue
		}
		no, err := strconv.Atoi(noStr)
		if err != nil || no <= 0 {
			no = len(parsed) + 1
		}

		row := model.TestRow{
			No:            no,
			ExcelRow:      i + 1,
			OPD:           opd,
			Kategori:      getCol(2),
			StatusBank:    strings.ToLower(getCol(3)),
			QuestionRef:   questionRef,
			QuestionInput: getCol(5),
			ExpectedReply: getCol(6),
			ActualReply:   getCol(7),
			Notes:         getCol(8),
			ResultStatus:  normalizeResultStatus(getCol(9)),
		}
		hydrateFlags(&row)
		parsed = append(parsed, row)
	}

	if len(parsed) == 0 {
		return errors.New("tidak ada baris pertanyaan yang valid pada sheet Bank Pertanyaan")
	}

	var opds []string
	seenOPD := map[string]bool{}
	if ringIdx, err := f.GetSheetIndex(SheetRingkasan); err == nil && ringIdx >= 0 {
		if ringRows, err := f.GetRows(SheetRingkasan); err == nil {
			for i := 2; i < len(ringRows); i++ {
				if len(ringRows[i]) == 0 {
					continue
				}
				name := strings.TrimSpace(ringRows[i][0])
				if name != "" && !strings.EqualFold(name, "Total") && !strings.EqualFold(name, "Jumlah") && !seenOPD[name] {
					seenOPD[name] = true
					opds = append(opds, name)
				}
			}
		}
	}
	for _, r := range parsed {
		if r.OPD != "" && !seenOPD[r.OPD] {
			seenOPD[r.OPD] = true
			opds = append(opds, r.OPD)
		}
	}

	s.rows = parsed
	s.opdOrder = opds
	return nil
}

func hydrateFlags(r *model.TestRow) {
	r.HasExpectedFile = strings.Contains(r.ExpectedReply, "[Berkas]")
	r.HasExpectedCoord = strings.Contains(r.ExpectedReply, "[Koordinat]")
	r.HasActualFile = strings.Contains(r.ActualReply, "[Berkas]")
	r.HasActualCoord = strings.Contains(r.ActualReply, "[Koordinat]")
}

func normalizeResultStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "cocok":
		return "Cocok"
	case "tidak", "tidak cocok":
		return "Tidak"
	default:
		return ""
	}
}

func (s *Store) FilePath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.filePath
}

func (s *Store) Snapshot() ([]model.TestRow, model.OverallSummary) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.TestRow, len(s.rows))
	copy(out, s.rows)
	return out, s.computeSummaryLocked()
}

func (s *Store) GetRow(no int) (model.TestRow, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.rows {
		if r.No == no {
			return r, true
		}
	}
	return model.TestRow{}, false
}

func (s *Store) PendingRowNumbers(forceAll bool) []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var nums []int
	for _, r := range s.rows {
		if forceAll || r.ResultStatus == "" {
			nums = append(nums, r.No)
		}
	}
	return nums
}

func (s *Store) UpdateRow(no int, patch model.RowPatch) (model.TestRow, model.OverallSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	targetIdx := -1
	for i := range s.rows {
		if s.rows[i].No == no {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return model.TestRow{}, model.OverallSummary{}, fmt.Errorf("baris No %d tidak ditemukan", no)
	}

	updated := s.rows[targetIdx]
	if patch.QuestionInput != nil {
		updated.QuestionInput = strings.TrimSpace(*patch.QuestionInput)
	}
	if patch.ActualReply != nil {
		updated.ActualReply = strings.TrimSpace(*patch.ActualReply)
	}
	if patch.Notes != nil {
		updated.Notes = strings.TrimSpace(*patch.Notes)
	}
	if patch.ResultStatus != nil {
		updated.ResultStatus = normalizeResultStatus(*patch.ResultStatus)
	}
	hydrateFlags(&updated)

	if err := s.writeRowAtomicLocked(updated); err != nil {
		return model.TestRow{}, model.OverallSummary{}, err
	}

	s.rows[targetIdx] = updated
	return updated, s.computeSummaryLocked(), nil
}

func (s *Store) writeRowAtomicLocked(row model.TestRow) error {
	f, err := excelize.OpenFile(s.filePath)
	if err != nil {
		return fmt.Errorf("open workbook for update: %w", err)
	}
	defer f.Close()

	rIdx := row.ExcelRow
	if err := f.SetCellStr(SheetBank, fmt.Sprintf("F%d", rIdx), row.QuestionInput); err != nil {
		return err
	}
	if err := f.SetCellStr(SheetBank, fmt.Sprintf("H%d", rIdx), row.ActualReply); err != nil {
		return err
	}
	if err := f.SetCellStr(SheetBank, fmt.Sprintf("I%d", rIdx), row.Notes); err != nil {
		return err
	}
	if err := f.SetCellStr(SheetBank, fmt.Sprintf("J%d", rIdx), row.ResultStatus); err != nil {
		return err
	}

	_ = f.UpdateLinkedValue()

	return writeWorkbookAtomically(f, s.filePath)
}

func writeWorkbookAtomically(f *excelize.File, destPath string) error {
	tmpPath := destPath + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("create temp workbook: %w", err)
	}
	if err := f.Write(out); err != nil {
		out.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp workbook: %w", err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync temp workbook: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp workbook: %w", err)
	}
	return os.Rename(tmpPath, destPath)
}

func (s *Store) ExportBytes() ([]byte, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return nil, "", err
	}
	return data, filepath.Base(s.filePath), nil
}

func (s *Store) ReplaceWorkbook(uploaded []byte, targetFileName string) (model.OverallSummary, error) {
	if len(uploaded) == 0 {
		return model.OverallSummary{}, errors.New("file kosong")
	}
	f, err := excelize.OpenReader(bytes.NewReader(uploaded))
	if err != nil {
		return model.OverallSummary{}, fmt.Errorf("format file .xlsx tidak valid: %w", err)
	}
	defer f.Close()

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.filePath)
	cleanName := filepath.Base(strings.TrimSpace(targetFileName))
	if cleanName == "" || !strings.HasSuffix(strings.ToLower(cleanName), ".xlsx") {
		cleanName = filepath.Base(s.filePath)
	}
	destPath := filepath.Join(dir, cleanName)

	probe := &Store{filePath: destPath}
	if err := probe.parseWorkbookLocked(f); err != nil {
		return model.OverallSummary{}, err
	}

	tmpPath := destPath + ".tmp"
	if err := os.WriteFile(tmpPath, uploaded, 0644); err != nil {
		return model.OverallSummary{}, err
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return model.OverallSummary{}, err
	}
	_ = ensureBackup(destPath)

	s.filePath = destPath
	s.rows = probe.rows
	s.opdOrder = probe.opdOrder
	return s.computeSummaryLocked(), nil
}

func (s *Store) computeSummaryLocked() model.OverallSummary {
	sum := model.OverallSummary{
		FileName: filepath.Base(s.filePath),
		Total:    len(s.rows),
	}

	opdMap := make(map[string]*model.OPDSummary, len(s.opdOrder))
	for _, name := range s.opdOrder {
		opdMap[name] = &model.OPDSummary{OPD: name}
	}
	catSet := map[string]bool{}

	for _, r := range s.rows {
		if r.StatusBank == "waiting" {
			sum.Waiting++
		} else {
			sum.Approved++
		}
		if r.Kategori != "" {
			catSet[r.Kategori] = true
		}

		item, ok := opdMap[r.OPD]
		if !ok {
			item = &model.OPDSummary{OPD: r.OPD}
			opdMap[r.OPD] = item
			s.opdOrder = append(s.opdOrder, r.OPD)
		}
		item.Total++

		switch r.ResultStatus {
		case "Cocok":
			sum.Cocok++
			sum.Tested++
			item.Cocok++
		case "Tidak":
			sum.Tidak++
			sum.Tested++
			item.Tidak++
		default:
			sum.BelumDiuji++
			item.BelumDiuji++
		}
	}

	if sum.Total > 0 {
		sum.ProgressPct = round1(float64(sum.Tested) * 100 / float64(sum.Total))
	}
	if sum.Tested > 0 {
		sum.AccuracyPct = round1(float64(sum.Cocok) * 100 / float64(sum.Tested))
	}

	sum.ByOPD = make([]model.OPDSummary, 0, len(s.opdOrder))
	for _, name := range s.opdOrder {
		it := opdMap[name]
		if it == nil || it.Total == 0 {
			continue
		}
		tested := it.Cocok + it.Tidak
		if tested > 0 {
			it.AccuracyPct = round1(float64(it.Cocok) * 100 / float64(tested))
		}
		sum.ByOPD = append(sum.ByOPD, *it)
	}

	sum.Categories = make([]string, 0, len(catSet))
	for c := range catSet {
		sum.Categories = append(sum.Categories, c)
	}
	sort.Strings(sum.Categories)

	return sum
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
