package excel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
	"whatshap-gateway/internal/model"
)

// ParseResult adalah keluaran parser workbook: tiga set baris + urutan OPD.
// Tidak ada IO, tidak ada state: fungsi ParseBytes/ParseFile hanya membaca
// bytes dan mengembalikan data. Dipakai oleh Store (jalur lama) dan oleh
// workbench DBStore (jalur baru) saat memproses upload.
type ParseResult struct {
	Bank     []model.TestRow
	OOB      []model.OutOfBankRow
	Stress   []model.StressTestRow
	OPDOrder []string
}

// MetricsBundle adalah sidecar metrics yang biasanya disimpan di
// workbook.metrics.json. Kosong (zero value) berarti tidak ada metrics.
// Bentuk field sengaja identik dengan persistedMetricsFile agar
// pemanggil bisa memakai struct yang sudah ada.
type MetricsBundle struct {
	Bank        map[int]model.RowHumanMetrics
	OOB         map[int]model.RowHumanMetrics
	Stress      map[int]model.RowHumanMetrics
	BankMedia   map[int][]string
	OOBMedia    map[int][]string
	StressMedia map[int][]string
}

// ParseBytes mem-parse berkas .xlsx dari memori. Metrics opsional: bila
// supplied, nilainya digabungkan ke setiap baris persis seperti perilaku
// Store (termasuk HumanSummary auto-fill untuk OOB dan Stress).
//
// Fungsi ini TIDAK menulis disk dan TIDAK memakai filePath. Inilah kontrak
// yang dipakai ETL (cmd/workbench-migrate) dan upload DBStore.
func ParseBytes(raw []byte, metrics *MetricsBundle) (ParseResult, error) {
	if len(raw) == 0 {
		return ParseResult{}, errors.New("file kosong")
	}
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		return ParseResult{}, fmt.Errorf("format file .xlsx tidak valid: %w", err)
	}
	defer f.Close()
	return parseFromFile(f, metrics, "")
}

// ParseFile membuka .xlsx dari path lalu memanggil ParseBytes. Dipakai untuk
// ETL dari file lokal.
func ParseFile(path string, metrics *MetricsBundle) (ParseResult, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return ParseResult{}, fmt.Errorf("open excel %s: %w", path, err)
	}
	defer f.Close()
	return parseFromFile(f, metrics, path)
}

// parseFromFile adalah inti parser. Semua aturan yang ada di
// Store.parseWorkbookLocked dipindah ke sini tanpa perubahan semantik.
func parseFromFile(f *excelize.File, metrics *MetricsBundle, sourcePath string) (ParseResult, error) {
	if metrics == nil {
		metrics = &MetricsBundle{}
	}

	idx, err := f.GetSheetIndex(SheetBank)
	if err != nil || idx < 0 {
		return ParseResult{}, fmt.Errorf("sheet %q tidak ditemukan di file Excel", SheetBank)
	}
	rawRows, err := f.GetRows(SheetBank)
	if err != nil {
		return ParseResult{}, fmt.Errorf("read sheet %q: %w", SheetBank, err)
	}

	hMap := buildHeaderMap(rawRows)
	cNo := findCol(hMap, 0, "no")
	cStatus := findCol(hMap, 9, "status uji", "status uji (cocok/tidak)")
	cNotes := findCol(hMap, 8, "keterangan", "keterangan evaluasi")
	cOPD := findCol(hMap, 1, "opd", "nama opd")
	cKat := findCol(hMap, 2, "kategori")
	cStatusBank := findCol(hMap, 3, "status bank")
	cQRef := findCol(hMap, 4, "pertanyaan acuan", "pertanyaan acuan di bank")
	cQInput := findCol(hMap, 5, "pertanyaan diinput", "pertanyaan diinput ke bot", "pertanyaan diinput ke bot (bank)")
	cExpected := findCol(hMap, 6, "balasan acuan", "balasan acuan di bank", "balasan acuan (bank)")
	cActual := findCol(hMap, 7, "balasan dikirim sistem", "balasan dikirim sistem (wa)", "balasan dikirim")

	parsed := make([]model.TestRow, 0, len(rawRows))
	for i := 2; i < len(rawRows); i++ {
		cols := rawRows[i]
		getCol := func(c int) string {
			if c < len(cols) {
				return strings.TrimSpace(cols[c])
			}
			return ""
		}
		noStr := getCol(cNo)
		opd := getCol(cOPD)
		questionRef := getCol(cQRef)
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
			Kategori:      getCol(cKat),
			StatusBank:    strings.ToLower(getCol(cStatusBank)),
			QuestionRef:   questionRef,
			QuestionInput: getCol(cQInput),
			ExpectedReply: getCol(cExpected),
			ActualReply:   getCol(cActual),
			Notes:         getCol(cNotes),
			ResultStatus:  normalizeResultStatus(getCol(cStatus)),
		}
		if m, ok := metrics.Bank[no]; ok {
			row.Metrics = m
		}
		if media, ok := metrics.BankMedia[no]; ok {
			row.MediaFiles = media
		}
		hydrateFlags(&row)
		parsed = append(parsed, row)
	}
	if len(parsed) == 0 {
		return ParseResult{}, errors.New("tidak ada baris pertanyaan yang valid pada sheet Bank Pertanyaan")
	}

	// Urutan OPD: utamakan sheet Ringkasan, lalu lengkapi dari baris bank.
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

	// Sheet Uji Diluar Bank (opsional)
	var parsedOOB []model.OutOfBankRow
	if oobIdx, err := f.GetSheetIndex(SheetOutOfBank); err == nil && oobIdx >= 0 {
		if oobRaw, err := f.GetRows(SheetOutOfBank); err == nil {
			oobHMap := buildHeaderMap(oobRaw)
			cOOBNo := findCol(oobHMap, 0, "no")
			cOOBStatus := findCol(oobHMap, 9, "status uji", "status uji (cocok/tidak)")
			cOOBNotes := findCol(oobHMap, 8, "keterangan", "keterangan evaluasi")
			cOOBSource := findCol(oobHMap, 1, "sumber / opd acuan", "sumber / opd", "sumber", "opd")
			cOOBScenario := findCol(oobHMap, 2, "jenis skenario uji", "jenis skenario", "skenario")
			cOOBBaseQ := findCol(oobHMap, 3, "pertanyaan acuan di bank (data asli)", "pertanyaan acuan di bank", "pertanyaan acuan")
			cOOBProbeQ := findCol(oobHMap, 4, "pertanyaan diinput ke bot (diluar bank)", "pertanyaan diinput ke bot", "pertanyaan diinput")
			cOOBExpected := findCol(oobHMap, 5, "perilaku bot yang diharapkan", "perilaku yang diharapkan")
			cOOBActual := findCol(oobHMap, 6, "balasan dikirim sistem", "balasan dikirim sistem (wa)", "balasan dikirim")
			cOOBHumanSum := findCol(oobHMap, 7, "detail ritme manusia (anti-spam)", "detail ritme manusia", "ritme manusia")
			cOOBForbYear := findCol(oobHMap, 10, "tahun uji diluar data", "tahun uji")
			cOOBRefYear := findCol(oobHMap, 11, "tahun acuan bank", "tahun acuan")

			for i := 2; i < len(oobRaw); i++ {
				cols := oobRaw[i]
				getCol := func(c int) string {
					if c < len(cols) {
						return strings.TrimSpace(cols[c])
					}
					return ""
				}
				probeQ := getCol(cOOBProbeQ)
				baseQ := getCol(cOOBBaseQ)
				if getCol(cOOBNo) == "" && probeQ == "" && baseQ == "" {
					continue
				}
				no := len(parsedOOB) + 1
				if n, err := strconv.Atoi(getCol(cOOBNo)); err == nil && n > 0 {
					no = n
				}
				item := model.OutOfBankRow{
					No:               no,
					ExcelRow:         i + 1,
					SourceRef:        getCol(cOOBSource),
					ScenarioType:     getCol(cOOBScenario),
					BaseQuestion:     baseQ,
					ProbeQuestion:    probeQ,
					ExpectedBehavior: getCol(cOOBExpected),
					ActualReply:      getCol(cOOBActual),
					HumanSummary:     getCol(cOOBHumanSum),
					Notes:            getCol(cOOBNotes),
					ResultStatus:     normalizeResultStatus(getCol(cOOBStatus)),
					ForbiddenYear:    getCol(cOOBForbYear),
					ReferenceYear:    getCol(cOOBRefYear),
				}
				if m, ok := metrics.OOB[no]; ok {
					item.Metrics = m
					if item.HumanSummary == "" {
						item.HumanSummary = FormatHumanMetricsSummary(m)
					}
				}
				if media, ok := metrics.OOBMedia[no]; ok {
					item.MediaFiles = media
				}
				parsedOOB = append(parsedOOB, item)
			}
		}
	}

	// Sheet Uji Stress AI (opsional)
	var parsedStress []model.StressTestRow
	if stressIdx, err := f.GetSheetIndex(SheetStress); err == nil && stressIdx >= 0 {
		if stressRaw, err := f.GetRows(SheetStress); err == nil {
			stressHMap := buildHeaderMap(stressRaw)
			cStressNo := findCol(stressHMap, 0, "no")
			cStressStatus := findCol(stressHMap, 8, "status uji", "status uji (cocok/tidak)")
			cStressNotes := findCol(stressHMap, 7, "keterangan evaluasi ketahanan ai", "keterangan evaluasi", "keterangan")
			cStressKat := findCol(stressHMap, 1, "kategori stress test", "kategori")
			cStressPromptQ := findCol(stressHMap, 2, "pertanyaan chat warga (diinput ke bot)", "pertanyaan chat warga", "chat warga")
			cStressIntent := findCol(stressHMap, 3, "maksud / inti jebakan penguji", "maksud / inti jebakan", "maksud")
			cStressExpected := findCol(stressHMap, 4, "perilaku bot yang diharapkan (standar ketahanan ai)", "perilaku bot yang diharapkan", "standar ketahanan")
			cStressActual := findCol(stressHMap, 5, "balasan dikirim sistem (wa)", "balasan dikirim sistem", "balasan dikirim")
			cStressHumanSum := findCol(stressHMap, 6, "detail ritme manusia (anti-spam)", "detail ritme manusia", "ritme manusia")
			cStressScore := findCol(stressHMap, 9, "skor kesantunan & logika (1-5)", "skor kesantunan & logika", "skor kesantunan", "skor")
			cStressHallucination := findCol(stressHMap, 10, "tingkat halusinasi", "halusinasi")

			for i := 2; i < len(stressRaw); i++ {
				cols := stressRaw[i]
				getCol := func(c int) string {
					if c < len(cols) {
						return strings.TrimSpace(cols[c])
					}
					return ""
				}
				promptQ := getCol(cStressPromptQ)
				if getCol(cStressNo) == "" && promptQ == "" {
					continue
				}
				no := len(parsedStress) + 1
				if n, err := strconv.Atoi(getCol(cStressNo)); err == nil && n > 0 {
					no = n
				}
				score := 0
				if s, err := strconv.Atoi(getCol(cStressScore)); err == nil {
					score = s
				}
				item := model.StressTestRow{
					No:               no,
					ExcelRow:         i + 1,
					StressCategory:   getCol(cStressKat),
					UserPromptQ:      promptQ,
					IntentPurpose:    getCol(cStressIntent),
					ExpectedBehavior: getCol(cStressExpected),
					ActualReply:      getCol(cStressActual),
					HumanSummary:     getCol(cStressHumanSum),
					Notes:            getCol(cStressNotes),
					ResultStatus:     normalizeResultStatus(getCol(cStressStatus)),
					PolitenessScore:  score,
					HallucinationLvl: getCol(cStressHallucination),
				}
				if m, ok := metrics.Stress[no]; ok {
					item.Metrics = m
					if item.HumanSummary == "" {
						item.HumanSummary = FormatHumanMetricsSummary(m)
					}
				}
				if media, ok := metrics.StressMedia[no]; ok {
					item.MediaFiles = media
				}
				parsedStress = append(parsedStress, item)
			}
		}
	}

	_ = sourcePath // disimpan untuk pemanggil yang ingin melaporkan path sumber
	return ParseResult{
		Bank:     parsed,
		OOB:      parsedOOB,
		Stress:   parsedStress,
		OPDOrder: opds,
	}, nil
}

// LoadMetricsFile membaca sidecar workbook.metrics.json (path apa adanya) dan
// mengembalikan MetricsBundle siap pakai untuk ParseBytes/ParseFile. Missing
// file menghasilkan bundle kosong tanpa error, sama seperti perilaku Store.
//
// Fungsi ini tidak mengubah file apa pun dan tidak menyentuh disk selain
// membaca satu file JSON.
func LoadMetricsFile(path string) (*MetricsBundle, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Bundle kosong dengan map non-nil: pemanggil boleh mengindeks
			// tanpa nil-check, sama seperti Store.loadMetricsSidecarLocked.
			return &MetricsBundle{
				Bank:        map[int]model.RowHumanMetrics{},
				OOB:         map[int]model.RowHumanMetrics{},
				Stress:      map[int]model.RowHumanMetrics{},
				BankMedia:   map[int][]string{},
				OOBMedia:    map[int][]string{},
				StressMedia: map[int][]string{},
			}, nil
		}
		return nil, err
	}
	var pm persistedMetricsFile
	if err := json.Unmarshal(raw, &pm); err != nil {
		return nil, fmt.Errorf("metrics %s tidak valid: %w", path, err)
	}
	return &MetricsBundle{
		Bank:        nonNilMetrics(pm.Bank),
		OOB:         nonNilMetrics(pm.OutOfBank),
		Stress:      nonNilMetrics(pm.Stress),
		BankMedia:   nonNilMedia(pm.BankMedia),
		OOBMedia:    nonNilMedia(pm.OutOfBankMedia),
		StressMedia: nonNilMedia(pm.StressMedia),
	}, nil
}

func nonNilMetrics(m map[int]model.RowHumanMetrics) map[int]model.RowHumanMetrics {
	if m == nil {
		return map[int]model.RowHumanMetrics{}
	}
	return m
}

func nonNilMedia(m map[int][]string) map[int][]string {
	if m == nil {
		return map[int][]string{}
	}
	return m
}

func buildHeaderMap(rawRows [][]string) map[string]int {
	m := make(map[string]int)
	if len(rawRows) > 1 {
		for i, h := range rawRows[1] {
			norm := strings.ToLower(strings.TrimSpace(h))
			if norm != "" {
				m[norm] = i
			}
		}
	}
	return m
}

func findCol(m map[string]int, defaultIdx int, names ...string) int {
	for _, n := range names {
		if idx, ok := m[strings.ToLower(strings.TrimSpace(n))]; ok {
			return idx
		}
	}
	return defaultIdx
}
