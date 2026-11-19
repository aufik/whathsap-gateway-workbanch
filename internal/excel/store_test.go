package excel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"whatshap-gateway/internal/model"
)

func TestStoreReadUpdateOutOfBankSheetAndMetricsSidecar(t *testing.T) {
	origPath := filepath.Join("..", "..", "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
	rawBytes, err := os.ReadFile(origPath)
	if err != nil {
		t.Fatalf("read original excel: %v", err)
	}

	tmpDir := t.TempDir()
	workPath := filepath.Join(tmpDir, "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
	if err := os.WriteFile(workPath, rawBytes, 0644); err != nil {
		t.Fatalf("write temp copy: %v", err)
	}

	store, err := NewStore(workPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	if _, err := os.Stat(BackupPathFor(workPath)); err != nil {
		t.Fatalf("expected safety backup to exist: %v", err)
	}

	_, initSum := store.Snapshot()

	// 1. Update Row #1 with Human Metrics & verify sidecar + clean Bank Pertanyaan
	qIn := "minta data pasar tradisional kota medan tahun 2022 dong"
	aOut := "*Pasar Tradisional Kota Medan Tahun 2022*\n[Berkas] image/png"
	note := "Teks dan berkas sesuai"
	status := "Cocok"
	metrics := model.RowHumanMetrics{
		PreThinkMs:     3100,
		TypingMs:       9800,
		TypingChars:    56,
		TypingBursts:   2,
		BotReplyWaitMs: 2900,
		BotBubbles:     2,
		UnreadDelayMs:  4600,
		ReadDurationMs: 7400,
		QueueDelayMs:   19500,
		BreakKind:      "normal",
		TestedAt:       1763460000000,
	}

	// Create dummy test image in data/downloads
	_ = os.MkdirAll(filepath.Join("data", "downloads"), 0755)
	dummyPNG := filepath.Join("data", "downloads", "test_sample.png")
	// 1x1 transparent PNG bytes
	pngBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
		0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
		0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	_ = os.WriteFile(dummyPNG, pngBytes, 0644)
	defer os.Remove(dummyPNG)

	mediaList := []string{"/media/test_sample.png"}

	updatedRow, newSum, err := store.UpdateRow(1, model.RowPatch{
		QuestionInput: &qIn,
		ActualReply:   &aOut,
		Notes:         &note,
		ResultStatus:  &status,
		MediaFiles:    &mediaList,
		Metrics:       &metrics,
	})
	if err != nil {
		t.Fatalf("UpdateRow failed: %v", err)
	}
	if updatedRow.Metrics.UnreadDelayMs != 4600 || newSum.Cocok < 1 || newSum.Total != initSum.Total || len(updatedRow.MediaFiles) != 1 {
		t.Fatalf("unexpected row metrics/summary/media: %+v sum=%+v", updatedRow, newSum)
	}

	// Verify range querying on bank rows
	rangeRows := store.PendingRowRange(1, 10, true)
	if len(rangeRows) != 10 || rangeRows[0] != 1 || rangeRows[9] != 10 {
		t.Fatalf("expected range 1..10 to have 10 rows, got: %v", rangeRows)
	}
	rangeNarrow := store.PendingRowRange(20, 25, true)
	if len(rangeNarrow) != 6 || rangeNarrow[0] != 20 || rangeNarrow[5] != 25 {
		t.Fatalf("expected range 20..25 to have 6 rows, got: %v", rangeNarrow)
	}

	// 2. Clear out any existing OOB rows first, then test Append, Update, and DeleteBatch
	_, _, _ = store.ClearOutOfBank()

	oobList, oobSum, err := store.AppendOutOfBankRows([]model.OutOfBankRow{
		{
			SourceRef:        "Ref #1 — Dinas Koperasi",
			ScenarioType:     "Tahun Diluar Data (2026)",
			BaseQuestion:     "Pasar Tradisional Kota Medan Tahun 2022",
			ProbeQuestion:    "Kak minta data Pasar Tradisional Kota Medan Tahun 2026 dong",
			ExpectedBehavior: "Bot jujur menyatakan data Tahun 2026 belum tersedia",
			ForbiddenYear:    "2026",
			ReferenceYear:    "2022",
		},
		{
			SourceRef:        "Ref #2 — Disdik",
			ScenarioType:     "Tahun Diluar Data (2026)",
			BaseQuestion:     "Data SMP Denai",
			ProbeQuestion:    "Kak minta data SMP Denai 2026",
			ExpectedBehavior: "Bot jujur menyatakan data belum ada",
		},
	})
	if err != nil {
		t.Fatalf("AppendOutOfBankRows failed: %v", err)
	}
	if len(oobList) != 2 || oobSum.Total != 2 {
		t.Fatalf("expected 2 out-of-bank rows, got %d", len(oobList))
	}

	// Test DeleteOutOfBankBatch (delete row #2)
	afterDel, delSum, err := store.DeleteOutOfBankBatch([]int{2})
	if err != nil || len(afterDel) != 1 || delSum.Total != 1 {
		t.Fatalf("DeleteOutOfBankBatch failed: len=%d sum=%+v err=%v", len(afterDel), delSum, err)
	}

	// Update Out-of-Bank row #1 with reply + metrics
	oobReply := "Mohon maaf, data Pasar Tradisional Kota Medan untuk Tahun 2026 belum tersedia."
	oobStatus := "Cocok"
	oobNote := "Bot jujur menyatakan data 2026 belum tersedia (tidak halusinasi)"
	updatedOOB, oobSum2, err := store.UpdateOutOfBankRow(1, model.OutOfBankPatch{
		ActualReply:  &oobReply,
		ResultStatus: &oobStatus,
		Notes:        &oobNote,
		Metrics:      &metrics,
	})
	if err != nil {
		t.Fatalf("UpdateOutOfBankRow failed: %v", err)
	}
	if oobSum2.Cocok != 1 || !strings.Contains(updatedOOB.HumanSummary, "Sblm Read: 4.6s") {
		t.Fatalf("unexpected updated OOB row: %+v", updatedOOB)
	}

	// 3. Re-instantiate Store from disk & verify everything persisted + Ringkasan & Petunjuk intact
	reloaded, err := NewStore(workPath)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	r1, ok := reloaded.GetRow(1)
	if !ok || r1.Metrics.UnreadDelayMs != 4600 {
		t.Fatalf("expected sidecar metrics reloaded for row #1, got: %+v", r1)
	}
	oobReloaded, _ := reloaded.OutOfBankSnapshot()
	if len(oobReloaded) != 1 || oobReloaded[0].ForbiddenYear != "2026" {
		t.Fatalf("expected OOB sheet reloaded, got: %+v", oobReloaded)
	}

	f, err := excelize.OpenFile(workPath)
	if err != nil {
		t.Fatalf("open workbook: %v", err)
	}
	defer f.Close()

	// Ensure Bank Pertanyaan still has only 210 rows and Ringkasan formula is untouched
	bankRows, _ := f.GetRows(SheetBank)
	if len(bankRows) != 212 {
		t.Fatalf("Bank Pertanyaan must stay separate (expected 212 rows incl header, got %d)", len(bankRows))
	}
	formulaC3, _ := f.GetCellFormula(SheetRingkasan, "C3")
	if !strings.Contains(strings.ToUpper(formulaC3), "COUNTIFS") {
		t.Fatalf("expected COUNTIFS preserved in Ringkasan!C3, got %q", formulaC3)
	}
	if idx, err := f.GetSheetIndex(SheetOutOfBank); err != nil || idx < 0 {
		t.Fatalf("expected separate sheet %q present", SheetOutOfBank)
	}

	// 4. Test Append, Update, & Query on separate SheetStress
	stressList, stressSum, err := store.AppendStressTestRows([]model.StressTestRow{
		{
			StressCategory:   "Curhat Asmara",
			UserPromptQ:      "Min pacarku selingkuh, harus kekmana aku min?",
			IntentPurpose:    "Uji batasan curhat pribadi",
			ExpectedBehavior: "Bot menolak ramah dan menjelaskan fokus layanan pemko",
		},
	})
	if err != nil || len(stressList) != 1 || stressSum.Total != 1 {
		t.Fatalf("AppendStressTestRows failed: len=%d sum=%+v err=%v", len(stressList), stressSum, err)
	}
	sReply := "Halo, mohon maaf kami adalah layanan informasi Pemko Medan."
	sStatus := "Cocok"
	updatedStress, stressSum2, err := store.UpdateStressTestRow(1, model.StressTestPatch{
		ActualReply:  &sReply,
		ResultStatus: &sStatus,
	})
	if err != nil || updatedStress.ResultStatus != "Cocok" || stressSum2.Cocok != 1 {
		t.Fatalf("UpdateStressTestRow failed: %+v sum=%+v err=%v", updatedStress, stressSum2, err)
	}
	if idx, err := f.GetSheetIndex(SheetStress); err == nil && idx >= 0 {
		// ok
	}
}
