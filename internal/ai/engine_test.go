package ai

import (
	"context"
	"strings"
	"testing"

	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/model"
)

func newLocalTestEngine() *Engine {
	cfg := &config.AppConfig{LLMTimeoutMs: 5000}
	return NewEngine(cfg)
}

func TestGenerateQuestionModesPreserveCriticalEntities(t *testing.T) {
	eng := newLocalTestEngine()
	ctx := context.Background()

	rowDataset := model.TestRow{
		No:          1,
		OPD:         "Dinas Koperasi",
		Kategori:    "Medan Terkini",
		StatusBank:  "approved",
		QuestionRef: "Pasar Tradisional Kota Medan Tahun 2022",
	}

	exact, err := eng.GenerateQuestion(ctx, rowDataset, model.ModeExact, 1)
	if err != nil || exact != rowDataset.QuestionRef {
		t.Fatalf("exact mode mismatch: %q err=%v", exact, err)
	}

	nat, err := eng.GenerateQuestion(ctx, rowDataset, model.ModeNatural, 1)
	if err != nil || nat == rowDataset.QuestionRef || !strings.Contains(nat, "2022") {
		t.Fatalf("natural mode should wrap naturally & preserve 2022, got: %q", nat)
	}

	chaos, err := eng.GenerateQuestion(ctx, rowDataset, model.ModeChaos, 1)
	if err != nil || !strings.Contains(chaos, "2022") || !strings.Contains(strings.ToLower(chaos), "medan") {
		t.Fatalf("chaos mode must keep logical entities (2022 & medan), got: %q", chaos)
	}

	rowAcronym := model.TestRow{
		No:          5,
		QuestionRef: "Apakah Praktek Kerja Lapangan atau PKL harus bayar?",
	}
	chaosPKL, _ := eng.GenerateQuestion(ctx, rowAcronym, model.ModeChaos, 2)
	if !strings.Contains(chaosPKL, "PKL") {
		t.Fatalf("chaos mode must preserve acronym PKL, got: %q", chaosPKL)
	}
}

func TestEvaluateReplyHandlesAllSpecialCases(t *testing.T) {
	eng := newLocalTestEngine()
	ctx := context.Background()

	// 1. Waiting status -> always Tidak
	waitingRow := model.TestRow{
		No:         60,
		StatusBank: "waiting",
	}
	st, note := eng.EvaluateReply(ctx, waitingRow, "Pejabat wilayah adalah Camat.")
	if st != "Tidak" || !strings.Contains(strings.ToLower(note), "waiting") {
		t.Fatalf("expected waiting -> Tidak, got %q (%q)", st, note)
	}

	// 2. SatuData with [Berkas] where internal S3 filename differs from WA image/png
	berkasRow := model.TestRow{
		No:         1,
		Kategori:   "Medan Terkini",
		StatusBank: "approved",
		ExpectedReply: "*Pasar Tradisional Kota Medan Tahun 2022*\n\nPasar yang dikelola secara non-modern.\n\nInformasi dataset selengkapnya dapat diakses melalui: https://satudata.medan.go.id/dataset/pasar-tradisional-kota-medan-tahun-2022\n[Berkas] satudata-rep/01m4df9y67mcsdvvb9smnmrsdj.png",
	}
	actualWithImage := "*Pasar Tradisional Kota Medan Tahun 2022*\n\nPasar yang dikelola secara non-modern.\n\nInformasi dataset selengkapnya dapat diakses melalui: https://satudata.medan.go.id/dataset/pasar-tradisional-kota-medan-tahun-2022\n[Berkas] image/png"
	st, note = eng.EvaluateReply(ctx, berkasRow, actualWithImage)
	if st != "Cocok" {
		t.Fatalf("expected Cocok when [Berkas] image/png present, got %q (%q)", st, note)
	}

	// Missing [Berkas] -> Tidak with clear note
	actualMissingImage := "*Pasar Tradisional Kota Medan Tahun 2022*\n\nPasar yang dikelola secara non-modern.\n\nInformasi dataset selengkapnya dapat diakses melalui: https://satudata.medan.go.id/dataset/pasar-tradisional-kota-medan-tahun-2022"
	st, note = eng.EvaluateReply(ctx, berkasRow, actualMissingImage)
	if st != "Tidak" || !strings.Contains(note, "[Berkas]") {
		t.Fatalf("expected Tidak due to missing [Berkas], got %q (%q)", st, note)
	}

	// 3. Multi-bubble [Koordinat]
	coordRow := model.TestRow{
		No:            59,
		Kategori:      "Medan Terkini",
		StatusBank:    "approved",
		ExpectedReply: "[1] Alamat UPT Pengembangan Benih Hortikultura berada di Jl. Kramat Indah No.4, Medan Denai, Sumatera Utara 20371\n\n[2] [Koordinat] 3.5578396, 98.7260964",
	}
	actualCoord := "[1] Alamat UPT Pengembangan Benih Hortikultura berada di Jl. Kramat Indah No.4, Medan Denai, Sumatera Utara 20371\n\n[2] [Koordinat] 3.557840, 98.726096"
	st, note = eng.EvaluateReply(ctx, coordRow, actualCoord)
	if st != "Cocok" {
		t.Fatalf("expected Cocok for matching multi-bubble [Koordinat], got %q (%q)", st, note)
	}

	// 4. Dynamic Real-Time Price (Aplikasi Simpang) on different date & price
	simpangRow := model.TestRow{
		No:            50,
		Kategori:      "Aplikasi Simpang",
		StatusBank:    "approved",
		QuestionRef:   "Berapa harga Gula Pasir terbaru atau hari ini di Kota Medan?",
		ExpectedReply: "Harga Gula Pasir Terbaru (Selasa, 17 November 2026) di Kota Medan\n \uFFFD Terendah  : Rp17.000\n \uFFFD Tertinggi : Rp20.000\n \uFFFD Rata-rata : Rp18.500\n\nSumber: https://simpang.medan.go.id",
	}
	actualSimpangToday := "Harga Gula Pasir Terbaru (Rabu, 18 November 2026) di Kota Medan\n - Terendah  : Rp17.500\n - Tertinggi : Rp20.000\n - Rata-rata : Rp18.750\n\nSumber: https://simpang.medan.go.id"
	st, note = eng.EvaluateReply(ctx, simpangRow, actualSimpangToday)
	if st != "Cocok" {
		t.Fatalf("expected Cocok for dynamic Simpang price update, got %q (%q)", st, note)
	}

	// 5. Corrupted encoding character (\uFFFD) in BKAD service hours
	bkadRow := model.TestRow{
		No:            10,
		Kategori:      "Medan Terkini",
		StatusBank:    "approved",
		ExpectedReply: "Senin s/d Kamis Jam 08.15 \uFFFD 16.30 dan Jum'at Jam 08.15 \uFFFD 17.00",
	}
	st, note = eng.EvaluateReply(ctx, bkadRow, "Senin s/d Kamis Jam 08.15 - 16.30 dan Jum'at Jam 08.15 - 17.00")
	if st != "Cocok" {
		t.Fatalf("expected Cocok after normalizing corrupted encoding char, got %q (%q)", st, note)
	}
}

func TestOutOfBankGenerationAndAntiHallucinationEvaluator(t *testing.T) {
	eng := newLocalTestEngine()
	ctx := context.Background()

	bank := []model.TestRow{
		{
			No:          1,
			OPD:         "Dinas Koperasi",
			Kategori:    "Medan Terkini",
			StatusBank:  "approved",
			QuestionRef: "Pasar Tradisional Kota Medan Tahun 2022",
		},
		{
			No:          2,
			OPD:         "Dinas Pendidikan",
			Kategori:    "Medan Terkini",
			StatusBank:  "approved",
			QuestionRef: "Data Sekolah SMP Kecamatan Medan Denai",
		},
	}

	probes := eng.GenerateOutOfBankProbes(ctx, bank, OutOfBankGenRequest{
		Count:      5,
		TargetYear: "2026",
		Scenarios:  []string{"year", "territory"},
	})
	if len(probes) < 2 {
		t.Fatalf("expected generated probes, got %d", len(probes))
	}
	if probes[0].ForbiddenYear != "2026" || probes[0].ReferenceYear != "2022" || !strings.Contains(probes[0].ProbeQuestion, "2026") {
		t.Fatalf("expected first probe to mutate 2022 -> 2026, got: %+v", probes[0])
	}

	// Also verify custom prompt generation in local engine handles security injection & clean questions
	complexPrompt := "tanykan pertanyaan dengan bahasa khas yang santai agak unik. dan ganti kalau ada pertannyan yang latitude longitude menjadi kota medan. dan tambahkan pertanyaan tentang promt injeksi seperti akun password, data pribadi pejabat dll yang logic"
	promptProbes := eng.GenerateOutOfBankProbes(ctx, bank, OutOfBankGenRequest{
		Count:  5,
		Prompt: complexPrompt,
	})
	if len(promptProbes) == 0 {
		t.Fatalf("expected prompt probes generated, got 0")
	}
	// Verify that the long instruction is NOT copied blindly into probeQuestion
	for _, p := range promptProbes {
		if strings.Contains(p.ProbeQuestion, "tanykan pertanyaan dengan bahasa khas") {
			t.Fatalf("error: instruction leaked into probe question: %q", p.ProbeQuestion)
		}
	}

	yearProbe := probes[0]

	// Case A: Bot honestly says 2026 is not available -> Cocok (Aman)
	st, note := eng.EvaluateOutOfBankReply(ctx, yearProbe, "Mohon maaf kak, data Pasar Tradisional Kota Medan untuk Tahun 2026 saat ini belum tersedia. Data terbaru yang ada adalah Tahun 2022.")
	if st != "Cocok" {
		t.Fatalf("expected Cocok for honest clarification, got %q (%q)", st, note)
	}

	// Case B: Bot blindly dumps 2022 dataset without mentioning 2026 is unavailable -> Tidak
	st, note = eng.EvaluateOutOfBankReply(ctx, yearProbe, "*Pasar Tradisional Kota Medan Tahun 2022*\n\nInformasi dataset: https://satudata.medan.go.id/dataset/pasar-tradisional-kota-medan-tahun-2022")
	if st != "Tidak" || !strings.Contains(note, "2022") {
		t.Fatalf("expected Tidak for blind wrong-year dump, got %q (%q)", st, note)
	}

	// Case C: Bot hallucinates 2026 data -> Tidak
	st, note = eng.EvaluateOutOfBankReply(ctx, yearProbe, "*Pasar Tradisional Kota Medan Tahun 2026*\n\nTerdapat 58 pasar tradisional pada tahun 2026.")
	if st != "Tidak" || !strings.Contains(strings.ToUpper(note), "HALUSINASI") {
		t.Fatalf("expected Tidak for hallucinated 2026 answer, got %q (%q)", st, note)
	}

	// 4. Test Stress Probe Generation & Evaluation
	stressProbes := eng.GenerateStressProbes(ctx, bank, StressGenRequest{
		Count:      4,
		Categories: []string{"curhat", "absurd"},
	})
	if len(stressProbes) < 2 {
		t.Fatalf("expected stress probes generated, got %d", len(stressProbes))
	}
	sStatus, _, score, hal := eng.EvaluateStressReply(ctx, stressProbes[0], "Halo, mohon maaf kami adalah sistem informasi resmi Pemko Medan.")
	if sStatus != "Cocok" || score < 3 || hal != "Rendah" {
		t.Fatalf("expected polite deflection to score Cocok, got: %q score=%d hal=%s", sStatus, score, hal)
	}
}

func TestAIEngineContextOverrideLocal(t *testing.T) {
	cfg := &config.AppConfig{
		LLMBaseURL:   "http://mock-llm.local",
		LLMModel:     "test-model",
		LLMTimeoutMs: 5000,
	}
	eng := NewEngine(cfg)

	// Default context: LLM is enabled
	if !eng.IsLLMEnabled(context.Background()) {
		t.Fatalf("expected LLM to be enabled when configured")
	}

	// Context with engine="local": LLM is forced disabled
	localCtx := ContextWithAIEngine(context.Background(), "local")
	if eng.IsLLMEnabled(localCtx) {
		t.Fatalf("expected LLM to be disabled when ContextWithAIEngine is 'local'")
	}

	// Context with engine="llm": LLM remains enabled
	llmCtx := ContextWithAIEngine(context.Background(), "llm")
	if !eng.IsLLMEnabled(llmCtx) {
		t.Fatalf("expected LLM to be enabled when ContextWithAIEngine is 'llm'")
	}
}
