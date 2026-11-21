package workbench

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"whatshap-gateway/internal/model"
)

// Summary builders ini meniru persis logika excel.compute*SummaryLocked
// (termasuk Approved/Waiting dari status_bank, urutan ByOPD dari opd_order,
// dan Categories yang di-sort). Test parity di tahap 7 membandingkan hasilnya
// field-per-field dengan excel.Store.

// round1 menyamakan pembulatan dengan excel.round1 (math.Round ke 1 desimal).
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// buildBankSummary menghitung model.OverallSummary dari baris tersortir
// berdasarkan No dan urutan OPD yang tersimpan.
func buildBankSummary(rows []model.TestRow, opdOrder []string, fileName string) model.OverallSummary {
	sum := model.OverallSummary{
		FileName: fileName,
		Total:    len(rows),
	}
	opdMap := make(map[string]*model.OPDSummary, len(opdOrder))
	order := append([]string(nil), opdOrder...)
	for _, name := range order {
		opdMap[name] = &model.OPDSummary{OPD: name}
	}
	catSet := map[string]bool{}
	for _, r := range rows {
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
			order = append(order, r.OPD)
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
	sum.ByOPD = make([]model.OPDSummary, 0, len(order))
	for _, name := range order {
		it := opdMap[name]
		if it == nil || it.Total == 0 {
			continue
		}
		if tested := it.Cocok + it.Tidak; tested > 0 {
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

func buildOOBSummary(rows []model.OutOfBankRow) model.OutOfBankSummary {
	sum := model.OutOfBankSummary{Total: len(rows)}
	for _, r := range rows {
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

func buildStressSummary(rows []model.StressTestRow) model.StressTestSummary {
	sum := model.StressTestSummary{Total: len(rows)}
	for _, r := range rows {
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

// normalizeResultStatus dan hydrateFlags adalah salinan perilaku paket excel.
// Disalin (bukan diekspor dari excel) supaya paket workbench tetap mandiri dan
// tidak memaksa perubahan API di paket lama. Nilai kontraknya dikunci oleh
// test parity: bila salah satu menyimpang, test akan gagal.

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

func hydrateFlags(r *model.TestRow) {
	r.HasExpectedFile = strings.Contains(r.ExpectedReply, "[Berkas]")
	r.HasExpectedCoord = strings.Contains(r.ExpectedReply, "[Koordinat]")
	r.HasActualFile = strings.Contains(r.ActualReply, "[Berkas]")
	r.HasActualCoord = strings.Contains(r.ActualReply, "[Koordinat]")
}

// formatHumanMetricsSummary menyalin excel.FormatHumanMetricsSummary karena
// kolom HumanSummary OOB/Stress dihitung dari metrics saat Update/Append.
func formatHumanMetricsSummary(m model.RowHumanMetrics) string {
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
