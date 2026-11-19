package excel

import (
	"bytes"
	"encoding/json"
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
	SheetOutOfBank = "Uji Diluar Bank"
	SheetStress    = "Uji Stress AI"
)

type persistedMetricsFile struct {
	Bank           map[int]model.RowHumanMetrics `json:"bank"`
	OutOfBank      map[int]model.RowHumanMetrics `json:"outOfBank"`
	Stress         map[int]model.RowHumanMetrics `json:"stress,omitempty"`
	BankMedia      map[int][]string              `json:"bankMedia,omitempty"`
	OutOfBankMedia map[int][]string              `json:"outOfBankMedia,omitempty"`
	StressMedia    map[int][]string              `json:"stressMedia,omitempty"`
}

type Store struct {
	mu            sync.RWMutex
	filePath      string
	rows          []model.TestRow
	oobRows       []model.OutOfBankRow
	stressRows    []model.StressTestRow
	opdOrder      []string
	bankMetrics   map[int]model.RowHumanMetrics
	oobMetrics    map[int]model.RowHumanMetrics
	stressMetrics map[int]model.RowHumanMetrics
	bankMedia     map[int][]string
	oobMedia      map[int][]string
	stressMedia   map[int][]string
}

func NewStore(filePath string) (*Store, error) {
	if err := ensureBackup(filePath); err != nil {
		return nil, fmt.Errorf("create safety backup: %w", err)
	}
	s := &Store{
		filePath:      filePath,
		bankMetrics:   map[int]model.RowHumanMetrics{},
		oobMetrics:    map[int]model.RowHumanMetrics{},
		stressMetrics: map[int]model.RowHumanMetrics{},
		bankMedia:     map[int][]string{},
		oobMedia:      map[int][]string{},
		stressMedia:   map[int][]string{},
	}
	s.loadMetricsSidecarLocked()
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

func MetricsPathFor(filePath string) string {
	ext := filepath.Ext(filePath)
	base := strings.TrimSuffix(filePath, ext)
	return base + ".metrics.json"
}

func FormatHumanMetricsSummary(m model.RowHumanMetrics) string {
	if m.TypingMs == 0 && m.TestedAt == 0 {
		return ""
	}
	bursts := m.TypingBursts
	if bursts <= 0 {
		bursts = 1
	}
	parts := []string{
		fmt.Sprintf("Fokus: %.1fs", float64(m.PreThinkMs)/1000),
		fmt.Sprintf("Ketik: %.1fs (%d hrf, %dx)", float64(m.TypingMs)/1000, m.TypingChars, bursts),
		fmt.Sprintf("Tunggu Bot: %.1fs (%d bbl)", float64(m.BotReplyWaitMs)/1000, m.BotBubbles),
		fmt.Sprintf("Sblm Read: %.1fs", float64(m.UnreadDelayMs)/1000),
		fmt.Sprintf("Baca: %.1fs", float64(m.ReadDurationMs)/1000),
	}
	if m.QueueDelayMs > 0 {
		label := "Jeda"
		switch m.BreakKind {
		case "micro_break":
			label = "Micro-Break"
		case "long_rest":
			label = "Long-Rest"
		case "spike":
			label = "Jeda Panjang"
		}
		parts = append(parts, fmt.Sprintf("%s: %.1fs", label, float64(m.QueueDelayMs)/1000))
	}
	return strings.Join(parts, " | ")
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

func (s *Store) loadMetricsSidecarLocked() {
	s.bankMetrics = map[int]model.RowHumanMetrics{}
	s.oobMetrics = map[int]model.RowHumanMetrics{}
	s.stressMetrics = map[int]model.RowHumanMetrics{}
	s.bankMedia = map[int][]string{}
	s.oobMedia = map[int][]string{}
	s.stressMedia = map[int][]string{}

	raw, err := os.ReadFile(MetricsPathFor(s.filePath))
	if err != nil {
		return
	}
	var pm persistedMetricsFile
	if err := json.Unmarshal(raw, &pm); err != nil {
		return
	}
	if pm.Bank != nil {
		s.bankMetrics = pm.Bank
	}
	if pm.OutOfBank != nil {
		s.oobMetrics = pm.OutOfBank
	}
	if pm.Stress != nil {
		s.stressMetrics = pm.Stress
	}
	if pm.BankMedia != nil {
		s.bankMedia = pm.BankMedia
	}
	if pm.OutOfBankMedia != nil {
		s.oobMedia = pm.OutOfBankMedia
	}
	if pm.StressMedia != nil {
		s.stressMedia = pm.StressMedia
	}
}

func (s *Store) saveMetricsSidecarLocked() {
	pm := persistedMetricsFile{
		Bank:           s.bankMetrics,
		OutOfBank:      s.oobMetrics,
		Stress:         s.stressMetrics,
		BankMedia:      s.bankMedia,
		OutOfBankMedia: s.oobMedia,
		StressMedia:    s.stressMedia,
	}
	raw, err := json.MarshalIndent(pm, "", "  ")
	if err != nil {
		return
	}
	dest := MetricsPathFor(s.filePath)
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err == nil {
		_ = os.Rename(tmp, dest)
	}
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
		if m, ok := s.bankMetrics[no]; ok {
			row.Metrics = m
		}
		if media, ok := s.bankMedia[no]; ok {
			row.MediaFiles = media
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

	// Parse separate SheetOutOfBank if present in workbook
	var parsedOOB []model.OutOfBankRow
	if oobIdx, err := f.GetSheetIndex(SheetOutOfBank); err == nil && oobIdx >= 0 {
		if oobRaw, err := f.GetRows(SheetOutOfBank); err == nil {
			for i := 2; i < len(oobRaw); i++ {
				cols := oobRaw[i]
				getCol := func(c int) string {
					if c < len(cols) {
						return strings.TrimSpace(cols[c])
					}
					return ""
				}
				probeQ := getCol(4)
				baseQ := getCol(3)
				if getCol(0) == "" && probeQ == "" && baseQ == "" {
					continue
				}
				no := len(parsedOOB) + 1
				if n, err := strconv.Atoi(getCol(0)); err == nil && n > 0 {
					no = n
				}
				item := model.OutOfBankRow{
					No:               no,
					ExcelRow:         i + 1,
					SourceRef:        getCol(1),
					ScenarioType:     getCol(2),
					BaseQuestion:     baseQ,
					ProbeQuestion:    probeQ,
					ExpectedBehavior: getCol(5),
					ActualReply:      getCol(6),
					HumanSummary:     getCol(7),
					Notes:            getCol(8),
					ResultStatus:     normalizeResultStatus(getCol(9)),
					ForbiddenYear:    getCol(10),
					ReferenceYear:    getCol(11),
				}
				if m, ok := s.oobMetrics[no]; ok {
					item.Metrics = m
					if item.HumanSummary == "" {
						item.HumanSummary = FormatHumanMetricsSummary(m)
					}
				}
				if media, ok := s.oobMedia[no]; ok {
					item.MediaFiles = media
				}
				parsedOOB = append(parsedOOB, item)
			}
		}
	}

	// Parse separate SheetStress if present in workbook
	var parsedStress []model.StressTestRow
	if stressIdx, err := f.GetSheetIndex(SheetStress); err == nil && stressIdx >= 0 {
		if stressRaw, err := f.GetRows(SheetStress); err == nil {
			for i := 2; i < len(stressRaw); i++ {
				cols := stressRaw[i]
				getCol := func(c int) string {
					if c < len(cols) {
						return strings.TrimSpace(cols[c])
					}
					return ""
				}
				promptQ := getCol(2)
				if getCol(0) == "" && promptQ == "" {
					continue
				}
				no := len(parsedStress) + 1
				if n, err := strconv.Atoi(getCol(0)); err == nil && n > 0 {
					no = n
				}
				score := 0
				if s, err := strconv.Atoi(getCol(9)); err == nil {
					score = s
				}
				item := model.StressTestRow{
					No:               no,
					ExcelRow:         i + 1,
					StressCategory:   getCol(1),
					UserPromptQ:      promptQ,
					IntentPurpose:    getCol(3),
					ExpectedBehavior: getCol(4),
					ActualReply:      getCol(5),
					HumanSummary:     getCol(6),
					Notes:            getCol(7),
					ResultStatus:     normalizeResultStatus(getCol(8)),
					PolitenessScore:  score,
					HallucinationLvl: getCol(10),
				}
				if m, ok := s.stressMetrics[no]; ok {
					item.Metrics = m
					if item.HumanSummary == "" {
						item.HumanSummary = FormatHumanMetricsSummary(m)
					}
				}
				if media, ok := s.stressMedia[no]; ok {
					item.MediaFiles = media
				}
				parsedStress = append(parsedStress, item)
			}
		}
	}

	s.rows = parsed
	s.oobRows = parsedOOB
	s.stressRows = parsedStress
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
	case "cocok", "aman", "sesuai":
		return "Cocok"
	case "tidak", "tidak cocok", "halusinasi", "salah":
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

func (s *Store) OutOfBankSnapshot() ([]model.OutOfBankRow, model.OutOfBankSummary) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.OutOfBankRow, len(s.oobRows))
	copy(out, s.oobRows)
	return out, s.computeOutOfBankSummaryLocked()
}

func (s *Store) StressTestSnapshot() ([]model.StressTestRow, model.StressTestSummary) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.StressTestRow, len(s.stressRows))
	copy(out, s.stressRows)
	return out, s.computeStressSummaryLocked()
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

func (s *Store) GetOutOfBankRow(no int) (model.OutOfBankRow, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.oobRows {
		if r.No == no {
			return r, true
		}
	}
	return model.OutOfBankRow{}, false
}

func (s *Store) GetStressTestRow(no int) (model.StressTestRow, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.stressRows {
		if r.No == no {
			return r, true
		}
	}
	return model.StressTestRow{}, false
}

func (s *Store) PendingRowNumbers(forceAll bool, limit int) []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var nums []int
	for _, r := range s.rows {
		if forceAll || r.ResultStatus == "" {
			nums = append(nums, r.No)
			if limit > 0 && len(nums) >= limit {
				break
			}
		}
	}
	return nums
}

func (s *Store) PendingRowRange(fromNo, toNo int, forceAll bool) []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if fromNo <= 0 {
		fromNo = 1
	}
	if toNo < fromNo {
		toNo = fromNo
	}
	var nums []int
	for _, r := range s.rows {
		if r.No >= fromNo && r.No <= toNo {
			if forceAll || r.ResultStatus == "" {
				nums = append(nums, r.No)
			}
		}
	}
	return nums
}

func (s *Store) PendingOutOfBankNumbers(forceAll bool, limit int) []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var nums []int
	for _, r := range s.oobRows {
		if forceAll || r.ResultStatus == "" {
			nums = append(nums, r.No)
			if limit > 0 && len(nums) >= limit {
				break
			}
		}
	}
	return nums
}

func (s *Store) PendingOutOfBankRange(fromNo, toNo int, forceAll bool) []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if fromNo <= 0 {
		fromNo = 1
	}
	if toNo < fromNo {
		toNo = fromNo
	}
	var nums []int
	for _, r := range s.oobRows {
		if r.No >= fromNo && r.No <= toNo {
			if forceAll || r.ResultStatus == "" {
				nums = append(nums, r.No)
			}
		}
	}
	return nums
}

func (s *Store) PendingStressNumbers(forceAll bool, limit int) []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var nums []int
	for _, r := range s.stressRows {
		if forceAll || r.ResultStatus == "" {
			nums = append(nums, r.No)
			if limit > 0 && len(nums) >= limit {
				break
			}
		}
	}
	return nums
}

func (s *Store) PendingStressRange(fromNo, toNo int, forceAll bool) []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if fromNo <= 0 {
		fromNo = 1
	}
	if toNo < fromNo {
		toNo = fromNo
	}
	var nums []int
	for _, r := range s.stressRows {
		if r.No >= fromNo && r.No <= toNo {
			if forceAll || r.ResultStatus == "" {
				nums = append(nums, r.No)
			}
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
	excelDirty := false

	if patch.QuestionInput != nil {
		updated.QuestionInput = strings.TrimSpace(*patch.QuestionInput)
		excelDirty = true
	}
	if patch.ActualReply != nil {
		updated.ActualReply = strings.TrimSpace(*patch.ActualReply)
		excelDirty = true
	}
	if patch.Notes != nil {
		updated.Notes = strings.TrimSpace(*patch.Notes)
		excelDirty = true
	}
	if patch.ResultStatus != nil {
		updated.ResultStatus = normalizeResultStatus(*patch.ResultStatus)
		excelDirty = true
	}
	if patch.MediaFiles != nil {
		updated.MediaFiles = *patch.MediaFiles
		if s.bankMedia == nil {
			s.bankMedia = map[int][]string{}
		}
		s.bankMedia[no] = updated.MediaFiles
		s.saveMetricsSidecarLocked()
		excelDirty = true
	}
	if patch.Metrics != nil {
		updated.Metrics = *patch.Metrics
		if s.bankMetrics == nil {
			s.bankMetrics = map[int]model.RowHumanMetrics{}
		}
		s.bankMetrics[no] = updated.Metrics
		s.saveMetricsSidecarLocked()
	}
	hydrateFlags(&updated)

	if excelDirty {
		if err := s.writeRowAtomicLocked(updated); err != nil {
			return model.TestRow{}, model.OverallSummary{}, err
		}
	}

	s.rows[targetIdx] = updated
	return updated, s.computeSummaryLocked(), nil
}

func (s *Store) AppendOutOfBankRows(items []model.OutOfBankRow) ([]model.OutOfBankRow, model.OutOfBankSummary, error) {
	if len(items) == 0 {
		return nil, model.OutOfBankSummary{}, errors.New("tidak ada baris uji baru untuk ditambahkan")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existingProbe := map[string]bool{}
	for _, r := range s.oobRows {
		existingProbe[strings.ToLower(strings.TrimSpace(r.ProbeQuestion))] = true
	}

	added := 0
	for _, it := range items {
		pq := strings.TrimSpace(it.ProbeQuestion)
		if pq == "" {
			continue
		}
		key := strings.ToLower(pq)
		if existingProbe[key] {
			continue
		}
		existingProbe[key] = true

		nextNo := len(s.oobRows) + 1
		it.No = nextNo
		it.ExcelRow = nextNo + 2
		it.ProbeQuestion = pq
		it.BaseQuestion = strings.TrimSpace(it.BaseQuestion)
		it.SourceRef = strings.TrimSpace(it.SourceRef)
		it.ScenarioType = strings.TrimSpace(it.ScenarioType)
		it.ExpectedBehavior = strings.TrimSpace(it.ExpectedBehavior)
		it.ResultStatus = normalizeResultStatus(it.ResultStatus)
		it.HumanSummary = FormatHumanMetricsSummary(it.Metrics)
		s.oobRows = append(s.oobRows, it)
		added++
	}

	if added == 0 {
		out := make([]model.OutOfBankRow, len(s.oobRows))
		copy(out, s.oobRows)
		return out, s.computeOutOfBankSummaryLocked(), nil
	}

	if err := s.writeOutOfBankSheetAtomicLocked(); err != nil {
		return nil, model.OutOfBankSummary{}, err
	}

	out := make([]model.OutOfBankRow, len(s.oobRows))
	copy(out, s.oobRows)
	return out, s.computeOutOfBankSummaryLocked(), nil
}

func (s *Store) UpdateOutOfBankRow(no int, patch model.OutOfBankPatch) (model.OutOfBankRow, model.OutOfBankSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	targetIdx := -1
	for i := range s.oobRows {
		if s.oobRows[i].No == no {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return model.OutOfBankRow{}, model.OutOfBankSummary{}, fmt.Errorf("baris Uji Diluar Bank #%d tidak ditemukan", no)
	}

	updated := s.oobRows[targetIdx]
	if patch.SourceRef != nil {
		updated.SourceRef = strings.TrimSpace(*patch.SourceRef)
	}
	if patch.ScenarioType != nil {
		updated.ScenarioType = strings.TrimSpace(*patch.ScenarioType)
	}
	if patch.BaseQuestion != nil {
		updated.BaseQuestion = strings.TrimSpace(*patch.BaseQuestion)
	}
	if patch.ProbeQuestion != nil {
		updated.ProbeQuestion = strings.TrimSpace(*patch.ProbeQuestion)
	}
	if patch.ExpectedBehavior != nil {
		updated.ExpectedBehavior = strings.TrimSpace(*patch.ExpectedBehavior)
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
	if patch.MediaFiles != nil {
		updated.MediaFiles = *patch.MediaFiles
		if s.oobMedia == nil {
			s.oobMedia = map[int][]string{}
		}
		s.oobMedia[no] = updated.MediaFiles
		s.saveMetricsSidecarLocked()
	}
	if patch.Metrics != nil {
		updated.Metrics = *patch.Metrics
		updated.HumanSummary = FormatHumanMetricsSummary(updated.Metrics)
		if s.oobMetrics == nil {
			s.oobMetrics = map[int]model.RowHumanMetrics{}
		}
		s.oobMetrics[no] = updated.Metrics
		s.saveMetricsSidecarLocked()
	}

	s.oobRows[targetIdx] = updated
	if err := s.writeOutOfBankSheetAtomicLocked(); err != nil {
		return model.OutOfBankRow{}, model.OutOfBankSummary{}, err
	}
	return updated, s.computeOutOfBankSummaryLocked(), nil
}

func (s *Store) DeleteOutOfBankRow(no int) ([]model.OutOfBankRow, model.OutOfBankSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := make([]model.OutOfBankRow, 0, len(s.oobRows))
	newMetrics := map[int]model.RowHumanMetrics{}
	found := false

	for _, r := range s.oobRows {
		if r.No == no {
			found = true
			continue
		}
		newNo := len(next) + 1
		r.No = newNo
		r.ExcelRow = newNo + 2
		if r.Metrics.TestedAt > 0 || r.Metrics.TypingMs > 0 {
			newMetrics[newNo] = r.Metrics
		}
		next = append(next, r)
	}
	if !found {
		return nil, model.OutOfBankSummary{}, fmt.Errorf("baris Uji Diluar Bank #%d tidak ditemukan", no)
	}

	s.oobRows = next
	s.oobMetrics = newMetrics
	s.saveMetricsSidecarLocked()

	if err := s.writeOutOfBankSheetAtomicLocked(); err != nil {
		return nil, model.OutOfBankSummary{}, err
	}

	out := make([]model.OutOfBankRow, len(s.oobRows))
	copy(out, s.oobRows)
	return out, s.computeOutOfBankSummaryLocked(), nil
}

func (s *Store) DeleteOutOfBankBatch(numbers []int) ([]model.OutOfBankRow, model.OutOfBankSummary, error) {
	if len(numbers) == 0 {
		rows, sum := s.OutOfBankSnapshot()
		return rows, sum, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	toDelete := make(map[int]bool, len(numbers))
	for _, n := range numbers {
		toDelete[n] = true
	}

	next := make([]model.OutOfBankRow, 0, len(s.oobRows))
	newMetrics := map[int]model.RowHumanMetrics{}
	newMedia := map[int][]string{}

	for _, r := range s.oobRows {
		if toDelete[r.No] {
			continue
		}
		newNo := len(next) + 1
		r.No = newNo
		r.ExcelRow = newNo + 2
		if r.Metrics.TestedAt > 0 || r.Metrics.TypingMs > 0 {
			newMetrics[newNo] = r.Metrics
		}
		if len(r.MediaFiles) > 0 {
			newMedia[newNo] = r.MediaFiles
		}
		next = append(next, r)
	}

	s.oobRows = next
	s.oobMetrics = newMetrics
	s.oobMedia = newMedia
	s.saveMetricsSidecarLocked()

	if err := s.writeOutOfBankSheetAtomicLocked(); err != nil {
		return nil, model.OutOfBankSummary{}, err
	}

	out := make([]model.OutOfBankRow, len(s.oobRows))
	copy(out, s.oobRows)
	return out, s.computeOutOfBankSummaryLocked(), nil
}

func (s *Store) ClearOutOfBank() ([]model.OutOfBankRow, model.OutOfBankSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.oobRows = []model.OutOfBankRow{}
	s.oobMetrics = map[int]model.RowHumanMetrics{}
	s.oobMedia = map[int][]string{}
	s.saveMetricsSidecarLocked()

	if err := s.writeOutOfBankSheetAtomicLocked(); err != nil {
		return nil, model.OutOfBankSummary{}, err
	}

	return []model.OutOfBankRow{}, s.computeOutOfBankSummaryLocked(), nil
}

func (s *Store) AppendStressTestRows(items []model.StressTestRow) ([]model.StressTestRow, model.StressTestSummary, error) {
	if len(items) == 0 {
		return nil, model.StressTestSummary{}, errors.New("tidak ada baris stress test untuk ditambahkan")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existingQuestions := map[string]bool{}
	for _, r := range s.stressRows {
		existingQuestions[strings.ToLower(strings.TrimSpace(r.UserPromptQ))] = true
	}

	added := 0
	for _, it := range items {
		pq := strings.TrimSpace(it.UserPromptQ)
		if pq == "" {
			continue
		}
		key := strings.ToLower(pq)
		if existingQuestions[key] {
			continue
		}
		existingQuestions[key] = true

		nextNo := len(s.stressRows) + 1
		it.No = nextNo
		it.ExcelRow = nextNo + 2
		it.UserPromptQ = pq
		it.StressCategory = strings.TrimSpace(it.StressCategory)
		it.IntentPurpose = strings.TrimSpace(it.IntentPurpose)
		it.ExpectedBehavior = strings.TrimSpace(it.ExpectedBehavior)
		it.ResultStatus = normalizeResultStatus(it.ResultStatus)
		it.HumanSummary = FormatHumanMetricsSummary(it.Metrics)
		s.stressRows = append(s.stressRows, it)
		added++
	}

	if added == 0 {
		out := make([]model.StressTestRow, len(s.stressRows))
		copy(out, s.stressRows)
		return out, s.computeStressSummaryLocked(), nil
	}

	if err := s.writeStressSheetAtomicLocked(); err != nil {
		return nil, model.StressTestSummary{}, err
	}

	out := make([]model.StressTestRow, len(s.stressRows))
	copy(out, s.stressRows)
	return out, s.computeStressSummaryLocked(), nil
}

func (s *Store) UpdateStressTestRow(no int, patch model.StressTestPatch) (model.StressTestRow, model.StressTestSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	targetIdx := -1
	for i := range s.stressRows {
		if s.stressRows[i].No == no {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return model.StressTestRow{}, model.StressTestSummary{}, fmt.Errorf("baris Uji Stress AI #%d tidak ditemukan", no)
	}

	updated := s.stressRows[targetIdx]
	if patch.StressCategory != nil {
		updated.StressCategory = strings.TrimSpace(*patch.StressCategory)
	}
	if patch.UserPromptQ != nil {
		updated.UserPromptQ = strings.TrimSpace(*patch.UserPromptQ)
	}
	if patch.IntentPurpose != nil {
		updated.IntentPurpose = strings.TrimSpace(*patch.IntentPurpose)
	}
	if patch.ExpectedBehavior != nil {
		updated.ExpectedBehavior = strings.TrimSpace(*patch.ExpectedBehavior)
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
	if patch.PolitenessScore != nil {
		updated.PolitenessScore = *patch.PolitenessScore
	}
	if patch.HallucinationLvl != nil {
		updated.HallucinationLvl = strings.TrimSpace(*patch.HallucinationLvl)
	}
	if patch.MediaFiles != nil {
		updated.MediaFiles = *patch.MediaFiles
		if s.stressMedia == nil {
			s.stressMedia = map[int][]string{}
		}
		s.stressMedia[no] = updated.MediaFiles
		s.saveMetricsSidecarLocked()
	}
	if patch.Metrics != nil {
		updated.Metrics = *patch.Metrics
		updated.HumanSummary = FormatHumanMetricsSummary(updated.Metrics)
		if s.stressMetrics == nil {
			s.stressMetrics = map[int]model.RowHumanMetrics{}
		}
		s.stressMetrics[no] = updated.Metrics
		s.saveMetricsSidecarLocked()
	}

	s.stressRows[targetIdx] = updated
	if err := s.writeStressSheetAtomicLocked(); err != nil {
		return model.StressTestRow{}, model.StressTestSummary{}, err
	}
	return updated, s.computeStressSummaryLocked(), nil
}

func (s *Store) DeleteStressTestRow(no int) ([]model.StressTestRow, model.StressTestSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := make([]model.StressTestRow, 0, len(s.stressRows))
	newMetrics := map[int]model.RowHumanMetrics{}
	found := false

	for _, r := range s.stressRows {
		if r.No == no {
			found = true
			continue
		}
		newNo := len(next) + 1
		r.No = newNo
		r.ExcelRow = newNo + 2
		if r.Metrics.TestedAt > 0 || r.Metrics.TypingMs > 0 {
			newMetrics[newNo] = r.Metrics
		}
		next = append(next, r)
	}
	if !found {
		return nil, model.StressTestSummary{}, fmt.Errorf("baris Uji Stress AI #%d tidak ditemukan", no)
	}

	s.stressRows = next
	s.stressMetrics = newMetrics
	s.saveMetricsSidecarLocked()

	if err := s.writeStressSheetAtomicLocked(); err != nil {
		return nil, model.StressTestSummary{}, err
	}

	out := make([]model.StressTestRow, len(s.stressRows))
	copy(out, s.stressRows)
	return out, s.computeStressSummaryLocked(), nil
}

func (s *Store) DeleteStressTestBatch(numbers []int) ([]model.StressTestRow, model.StressTestSummary, error) {
	if len(numbers) == 0 {
		rows, sum := s.StressTestSnapshot()
		return rows, sum, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	toDelete := make(map[int]bool, len(numbers))
	for _, n := range numbers {
		toDelete[n] = true
	}

	next := make([]model.StressTestRow, 0, len(s.stressRows))
	newMetrics := map[int]model.RowHumanMetrics{}
	newMedia := map[int][]string{}

	for _, r := range s.stressRows {
		if toDelete[r.No] {
			continue
		}
		newNo := len(next) + 1
		r.No = newNo
		r.ExcelRow = newNo + 2
		if r.Metrics.TestedAt > 0 || r.Metrics.TypingMs > 0 {
			newMetrics[newNo] = r.Metrics
		}
		if len(r.MediaFiles) > 0 {
			newMedia[newNo] = r.MediaFiles
		}
		next = append(next, r)
	}

	s.stressRows = next
	s.stressMetrics = newMetrics
	s.stressMedia = newMedia
	s.saveMetricsSidecarLocked()

	if err := s.writeStressSheetAtomicLocked(); err != nil {
		return nil, model.StressTestSummary{}, err
	}

	out := make([]model.StressTestRow, len(s.stressRows))
	copy(out, s.stressRows)
	return out, s.computeStressSummaryLocked(), nil
}

func (s *Store) ClearStressTest() ([]model.StressTestRow, model.StressTestSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stressRows = []model.StressTestRow{}
	s.stressMetrics = map[int]model.RowHumanMetrics{}
	s.stressMedia = map[int][]string{}
	s.saveMetricsSidecarLocked()

	if err := s.writeStressSheetAtomicLocked(); err != nil {
		return nil, model.StressTestSummary{}, err
	}

	return []model.StressTestRow{}, s.computeStressSummaryLocked(), nil
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

	// Format cell H so text stays at top and wraps cleanly
	hStyle, _ := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	})
	_ = f.SetCellStyle(SheetBank, fmt.Sprintf("H%d", rIdx), fmt.Sprintf("H%d", rIdx), hStyle)

	// If row has pictures: adjust row height so text sits on top and picture sits neatly below text
	if len(row.MediaFiles) > 0 {
		_ = f.SetRowHeight(SheetBank, rIdx, 165)
	}

	// Embed pictures into cell H positioned BELOW text
	embedRowPictures(f, SheetBank, fmt.Sprintf("H%d", rIdx), row.MediaFiles)

	_ = f.UpdateLinkedValue()
	return writeWorkbookAtomically(f, s.filePath)
}

func (s *Store) writeOutOfBankSheetAtomicLocked() error {
	f, err := excelize.OpenFile(s.filePath)
	if err != nil {
		return fmt.Errorf("open workbook for Uji Diluar Bank: %w", err)
	}
	defer f.Close()

	activeIdx := f.GetActiveSheetIndex()

	if idx, err := f.GetSheetIndex(SheetOutOfBank); err == nil && idx >= 0 {
		// Clear and recreate cleanly while preserving Bank Pertanyaan, Petunjuk, Ringkasan untouched
		_ = f.DeleteSheet(SheetOutOfBank)
	}
	if _, err := f.NewSheet(SheetOutOfBank); err != nil {
		return fmt.Errorf("create sheet %s: %w", SheetOutOfBank, err)
	}

	titleStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Size: 12, Color: "#0F172A"},
	})
	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 10, Color: "#FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#1E3A8A"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center", WrapText: true},
	})
	bodyStyle, _ := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	})

	_ = f.SetCellStr(SheetOutOfBank, "A1", "UJI PERTANYAAN DILUAR BANK DATA — TAHUN BERBEDA (2026+) / EDGE CASE / ANTI-HALUSINASI")
	_ = f.SetCellStyle(SheetOutOfBank, "A1", "A1", titleStyle)

	headers := []string{
		"No",
		"Sumber / OPD Acuan",
		"Jenis Skenario Uji",
		"Pertanyaan Acuan di Bank (Data Asli)",
		"Pertanyaan Diinput ke Bot (Diluar Bank)",
		"Perilaku Bot yang Diharapkan",
		"Balasan Dikirim Sistem",
		"Detail Ritme Manusia (Anti-Spam)",
		"Keterangan Evaluasi",
		"Status Uji (Cocok/Tidak)",
		"Tahun Uji Diluar Data",
		"Tahun Acuan Bank",
	}
	for cIdx, h := range headers {
		colName, _ := excelize.ColumnNumberToName(cIdx + 1)
		cell := fmt.Sprintf("%s2", colName)
		_ = f.SetCellStr(SheetOutOfBank, cell, h)
		_ = f.SetCellStyle(SheetOutOfBank, cell, cell, headerStyle)
	}

	widths := map[string]float64{
		"A": 6, "B": 28, "C": 24, "D": 36, "E": 38, "F": 38,
		"G": 42, "H": 34, "I": 30, "J": 16, "K": 14, "L": 14,
	}
	for col, w := range widths {
		_ = f.SetColWidth(SheetOutOfBank, col, col, w)
	}

	for i, r := range s.oobRows {
		rowNum := i + 3
		vals := []any{
			r.No,
			r.SourceRef,
			r.ScenarioType,
			r.BaseQuestion,
			r.ProbeQuestion,
			r.ExpectedBehavior,
			r.ActualReply,
			r.HumanSummary,
			r.Notes,
			r.ResultStatus,
			r.ForbiddenYear,
			r.ReferenceYear,
		}
		for cIdx, v := range vals {
			colName, _ := excelize.ColumnNumberToName(cIdx + 1)
			cell := fmt.Sprintf("%s%d", colName, rowNum)
			_ = f.SetCellValue(SheetOutOfBank, cell, v)
		}
		_ = f.SetCellStyle(SheetOutOfBank, fmt.Sprintf("A%d", rowNum), fmt.Sprintf("L%d", rowNum), bodyStyle)

		if len(r.MediaFiles) > 0 {
			_ = f.SetRowHeight(SheetOutOfBank, rowNum, 165)
		}

		// Embed pictures into cell G of Out-of-Bank sheet if present
		embedRowPictures(f, SheetOutOfBank, fmt.Sprintf("G%d", rowNum), r.MediaFiles)
	}

	if activeIdx >= 0 {
		f.SetActiveSheet(activeIdx)
	}

	return writeWorkbookAtomically(f, s.filePath)
}

func (s *Store) writeStressSheetAtomicLocked() error {
	f, err := excelize.OpenFile(s.filePath)
	if err != nil {
		return fmt.Errorf("open workbook for Uji Stress AI: %w", err)
	}
	defer f.Close()

	activeIdx := f.GetActiveSheetIndex()

	if idx, err := f.GetSheetIndex(SheetStress); err == nil && idx >= 0 {
		_ = f.DeleteSheet(SheetStress)
	}
	if _, err := f.NewSheet(SheetStress); err != nil {
		return fmt.Errorf("create sheet %s: %w", SheetStress, err)
	}

	titleStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Size: 12, Color: "#FFFFFF"},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"#4C1D95"}, Pattern: 1},
	})
	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 10, Color: "#FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#5B21B6"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center", WrapText: true},
	})
	bodyStyle, _ := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	})

	_ = f.SetCellStr(SheetStress, "A1", "UJI STRESS AI CHATBOT — SKENARIO CURHAT, ABSURD, AMBIGU, EMOSI, & TANPA JAWABAN")
	_ = f.SetCellStyle(SheetStress, "A1", "A1", titleStyle)

	headers := []string{
		"No",
		"Kategori Stress Test",
		"Pertanyaan Chat Warga (Diinput ke Bot)",
		"Maksud / Inti Jebakan Penguji",
		"Perilaku Bot yang Diharapkan (Standar Ketahanan AI)",
		"Balasan Dikirim Sistem (WA)",
		"Detail Ritme Manusia (Anti-Spam)",
		"Keterangan Evaluasi Ketahanan AI",
		"Status Uji (Cocok/Tidak)",
		"Skor Kesantunan & Logika (1-5)",
		"Tingkat Halusinasi",
	}
	for cIdx, h := range headers {
		colName, _ := excelize.ColumnNumberToName(cIdx + 1)
		cell := fmt.Sprintf("%s2", colName)
		_ = f.SetCellStr(SheetStress, cell, h)
		_ = f.SetCellStyle(SheetStress, cell, cell, headerStyle)
	}

	widths := map[string]float64{
		"A": 6, "B": 24, "C": 40, "D": 32, "E": 38, "F": 42,
		"G": 34, "H": 32, "I": 16, "J": 14, "K": 16,
	}
	for col, w := range widths {
		_ = f.SetColWidth(SheetStress, col, col, w)
	}

	for i, r := range s.stressRows {
		rowNum := i + 3
		vals := []any{
			r.No,
			r.StressCategory,
			r.UserPromptQ,
			r.IntentPurpose,
			r.ExpectedBehavior,
			r.ActualReply,
			r.HumanSummary,
			r.Notes,
			r.ResultStatus,
			r.PolitenessScore,
			r.HallucinationLvl,
		}
		for cIdx, v := range vals {
			colName, _ := excelize.ColumnNumberToName(cIdx + 1)
			cell := fmt.Sprintf("%s%d", colName, rowNum)
			_ = f.SetCellValue(SheetStress, cell, v)
		}
		_ = f.SetCellStyle(SheetStress, fmt.Sprintf("A%d", rowNum), fmt.Sprintf("K%d", rowNum), bodyStyle)

		if len(r.MediaFiles) > 0 {
			_ = f.SetRowHeight(SheetStress, rowNum, 165)
		}

		// Embed pictures into cell F of Stress sheet if present
		embedRowPictures(f, SheetStress, fmt.Sprintf("F%d", rowNum), r.MediaFiles)
	}

	if activeIdx >= 0 {
		f.SetActiveSheet(activeIdx)
	}

	return writeWorkbookAtomically(f, s.filePath)
}

func embedRowPictures(f *excelize.File, sheet, cell string, mediaPaths []string) {
	if len(mediaPaths) == 0 {
		return
	}
	enable := true
	for i, relPath := range mediaPaths {
		// Convert web relative path "/media/abc.png" to disk path "data/downloads/abc.png"
		clean := strings.TrimPrefix(relPath, "/media/")
		diskPath := filepath.Join("data", "downloads", clean)
		raw, err := os.ReadFile(diskPath)
		if err != nil || len(raw) == 0 {
			continue
		}
		ext := strings.ToLower(filepath.Ext(diskPath))
		if ext == "" {
			ext = ".png"
		}
		// Scale to neat height (~85px), offset horizontally if multiple pictures,
		// and place OffsetY at 85px to ensure picture sits UNDER text, never covering it
		offsetX := 8 + (i * 95)
		_ = f.AddPictureFromBytes(sheet, cell, &excelize.Picture{
			Extension: ext,
			File:      raw,
			Format: &excelize.GraphicOptions{
				AltText:      "Lampiran Balasan WA: " + clean,
				PrintObject:  &enable,
				ScaleX:       0.28,
				ScaleY:       0.28,
				OffsetX:      offsetX,
				OffsetY:      85,
				Positioning:  "oneCell",
			},
		})
	}
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

	probe := &Store{
		filePath:      destPath,
		bankMetrics:   map[int]model.RowHumanMetrics{},
		oobMetrics:    map[int]model.RowHumanMetrics{},
		stressMetrics: map[int]model.RowHumanMetrics{},
		bankMedia:     map[int][]string{},
		oobMedia:      map[int][]string{},
		stressMedia:   map[int][]string{},
	}
	probe.loadMetricsSidecarLocked()
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
	s.oobRows = probe.oobRows
	s.stressRows = probe.stressRows
	s.opdOrder = probe.opdOrder
	s.bankMetrics = probe.bankMetrics
	s.oobMetrics = probe.oobMetrics
	s.stressMetrics = probe.stressMetrics
	s.bankMedia = probe.bankMedia
	s.oobMedia = probe.oobMedia
	s.stressMedia = probe.stressMedia
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

func (s *Store) computeOutOfBankSummaryLocked() model.OutOfBankSummary {
	sum := model.OutOfBankSummary{
		Total: len(s.oobRows),
	}
	for _, r := range s.oobRows {
		switch r.ResultStatus {
		case "Cocok":
			sum.Cocok++
			sum.Tested++
		case "Tidak":
			sum.Tidak++
			sum.Tested++
		default:
			sum.BelumDiuji++
		}
	}
	if sum.Total > 0 {
		sum.ProgressPct = round1(float64(sum.Tested) * 100 / float64(sum.Total))
	}
	if sum.Tested > 0 {
		sum.SafetyPct = round1(float64(sum.Cocok) * 100 / float64(sum.Tested))
	}
	return sum
}

func (s *Store) computeStressSummaryLocked() model.StressTestSummary {
	sum := model.StressTestSummary{
		Total: len(s.stressRows),
	}
	for _, r := range s.stressRows {
		switch r.ResultStatus {
		case "Cocok":
			sum.Cocok++
			sum.Tested++
		case "Tidak":
			sum.Tidak++
			sum.Tested++
		default:
			sum.BelumDiuji++
		}
	}
	if sum.Total > 0 {
		sum.ProgressPct = round1(float64(sum.Tested) * 100 / float64(sum.Total))
	}
	if sum.Tested > 0 {
		sum.ResiliencePct = round1(float64(sum.Cocok) * 100 / float64(sum.Tested))
	}
	return sum
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
