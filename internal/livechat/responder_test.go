package livechat

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func baseSession(mode CategoryMode) Session {
	return Session{
		ID:              100,
		UserID:          900100,
		WAPhone:         "628000000001",
		BotPhone:        "628975275737",
		Title:           "Jalan rusak",
		ReportText:      "Ada lubang besar di depan pasar",
		NamaPengadu:     "Budi Santoso",
		TanggalKejadian: "2026-11-05",
		AlamatKejadian:  "Jl. Merdeka No. 1, Medan",
		TitikLokasi:     "https://maps.google.com/?q=3.5952,98.6722",
		TitikLokasiSrc:  SourceRandom,
		CategoryMode:    mode,
	}
}

func responderFixture() *Responder {
	return &Responder{}
}

func seededRand(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// TestAnswerMatrix exercises every kind × mode. The invariant checked below is
// what gives QC value: the factual fields are never corrupted, only the
// surface (typo/ngasal/celah probe).
func TestAnswerMatrix(t *testing.T) {
	kinds := []QuestionKind{
		KindNama, KindIsiLaporan, KindTanggal, KindAlamat,
		KindTitikLokasi, KindBerkas, KindKonfirmasi, KindLainnya,
	}
	modes := []CategoryMode{ModeBenar, ModeTypo, ModeNgasal, ModeCelah}

	assets := []Asset{
		{ID: 1, Kind: AssetImage, Label: "foto", FileName: "foto.png", Mimetype: "image/png", SizeBytes: 100},
		{ID: 2, Kind: AssetDocument, Label: "bukti", FileName: "bukti.pdf", Mimetype: "application/pdf", SizeBytes: 200},
	}

	for _, mode := range modes {
		for _, kind := range kinds {
			sess := baseSession(mode)
			sess.AssetIDs = []int64{1, 2}
			in := AnswerInput{
				Session: sess,
				Assets:  assets,
				Random:  seededRand(42),
			}
			res := responderFixture().ComposeForKind(context.Background(), kind, in)

			if res.MatchStatus != string(mode) {
				t.Fatalf("[%s/%s] matchStatus=%q, want %q", mode, kind, res.MatchStatus, mode)
			}
			if res.Text == "" && res.MediaAsset == nil && res.SendLoc == nil {
				t.Fatalf("[%s/%s] empty result", mode, kind)
			}

			// Fact preservation rules per plan §7 (no lying about numbers/almt).
			switch mode {
			case ModeBenar:
				switch kind {
				case KindNama:
					if !strings.Contains(res.Text, sess.NamaPengadu) {
						t.Fatalf("benar/nama: %q", res.Text)
					}
				case KindIsiLaporan:
					if !strings.Contains(res.Text, sess.ReportText) {
						t.Fatalf("benar/isi: %q", res.Text)
					}
				case KindTanggal:
					if !strings.Contains(res.Text, sess.TanggalKejadian) {
						t.Fatalf("benar/tanggal: %q", res.Text)
					}
				case KindAlamat:
					if !strings.Contains(res.Text, sess.AlamatKejadian) {
						t.Fatalf("benar/alamat: %q", res.Text)
					}
				case KindTitikLokasi:
					if res.SendLoc == nil || res.SendLoc.Lat != 3.5952 || res.SendLoc.Lng != 98.6722 {
						t.Fatalf("benar/titik_lokasi should send real LocationMessage, got %+v", res.SendLoc)
					}
				case KindBerkas:
					if res.MediaAsset == nil || res.MediaAsset.ID != 1 {
						t.Fatalf("benar/berkas should send first asset, got %+v", res.MediaAsset)
					}
				case KindKonfirmasi:
					low := strings.ToLower(res.Text)
					if !strings.Contains(low, "iya") && !strings.Contains(low, "betul") && !strings.Contains(low, "ya") && !strings.Contains(low, "benar") && !strings.Contains(low, "sesuai") {
						t.Fatalf("benar/konfirmasi text %q", res.Text)
					}
				}
			case ModeTypo:
				// Typo may mutate the surface but must not destructively change the
				// factual content (we simply verify the result is non-empty).
				if strings.TrimSpace(res.Text) == "" && res.MediaAsset == nil && res.SendLoc == nil {
					t.Fatalf("typo/%s empty", kind)
				}
			case ModeNgasal:
				// Sanity: not the fact value verbatim (otherwise it's not ngasal).
				if kind != KindTitikLokasi && kind != KindBerkas {
					if res.Text == sess.NamaPengadu || res.Text == sess.AlamatKejadian {
						t.Fatalf("ngasal/%s leaked fact %q", kind, res.Text)
					}
				}
			case ModeCelah:
				if kind == KindTanggal && res.Text == sess.TanggalKejadian {
					t.Fatalf("celah/tanggal should be impossible, got real date %q", res.Text)
				}
				if kind == KindAlamat && res.Text != "-" {
					t.Fatalf("celah/alamat should be empty placeholder, got %q", res.Text)
				}
			}
		}
	}
}

func TestTitikLokasiEmptyAnsweredLewat(t *testing.T) {
	sess := baseSession(ModeBenar)
	sess.TitikLokasi = ""
	sess.TitikLokasiSrc = SourceEmpty
	res := responderFixture().ComposeForKind(context.Background(), KindTitikLokasi, AnswerInput{Session: sess, Random: seededRand(1)})
	if res.Text != "lewat" || res.SendLoc != nil {
		t.Fatalf("expected 'lewat' text + no Loc, got text=%q loc=%+v", res.Text, res.SendLoc)
	}
}

func TestTitikLokasiUnparseableFallbacksToText(t *testing.T) {
	sess := baseSession(ModeBenar)
	sess.TitikLokasi = "depan pasar rame"
	sess.TitikLokasiSrc = SourceManual
	res := responderFixture().ComposeForKind(context.Background(), KindTitikLokasi, AnswerInput{Session: sess})
	if res.Text != "depan pasar rame" || res.SendLoc != nil {
		t.Fatalf("expected raw text fallback, got text=%q loc=%+v", res.Text, res.SendLoc)
	}
	if !strings.Contains(res.Note, "gagal diparse") {
		t.Fatalf("missing fallback note, got %q", res.Note)
	}
}

func TestBerkasNoAssetAnsweredLewat(t *testing.T) {
	sess := baseSession(ModeBenar)
	sess.AssetIDs = nil
	res := responderFixture().ComposeForKind(context.Background(), KindBerkas, AnswerInput{Session: sess})
	if res.Text != "lewat" || res.MediaAsset != nil {
		t.Fatalf("expected lewat without media, got %q / %+v", res.Text, res.MediaAsset)
	}
}

func TestCelahLokasiOutsideMedanOnceOnly(t *testing.T) {
	sess := baseSession(ModeCelah)
	in := AnswerInput{Session: sess, Random: seededRand(2)}
	res := responderFixture().ComposeForKind(context.Background(), KindTitikLokasi, in)
	if res.SendLoc == nil || !res.DidUseOutsideLoc {
		t.Fatalf("first celah/titik should send outer coord, got %+v used=%v", res.SendLoc, res.DidUseOutsideLoc)
	}
	if !IsOutsideMedan(res.SendLoc.Lat, res.SendLoc.Lng) {
		t.Fatalf("coord %v,%v should be outside Medan", res.SendLoc.Lat, res.SendLoc.Lng)
	}
	// Second turn with OutsideUsed=true must fall back to text.
	in.OutsideUsed = true
	res2 := responderFixture().ComposeForKind(context.Background(), KindTitikLokasi, in)
	if res2.SendLoc != nil || res2.Text != "lewat" {
		t.Fatalf("second celah/titik must not re-send outer coord; got loc=%+v text=%q", res2.SendLoc, res2.Text)
	}
}

func TestCelahBerkasToDocument(t *testing.T) {
	sess := baseSession(ModeCelah)
	sess.AssetIDs = []int64{1, 2}
	assets := []Asset{
		{ID: 1, Kind: AssetImage, FileName: "foto.png"},
		{ID: 2, Kind: AssetDocument, FileName: "bukti.pdf"},
	}
	res := responderFixture().ComposeForKind(context.Background(), KindBerkas, AnswerInput{Session: sess, Assets: assets})
	if res.MediaAsset == nil || res.MediaAsset.ID != 2 {
		t.Fatalf("celah/berkas should pick document asset, got %+v", res.MediaAsset)
	}
}

// hookStub verifies the AI hook is invoked for all modes, and that its
// output goes through sanitizeAIText.
type hookStub struct {
	called   bool
	lastMode CategoryMode
	lastKind QuestionKind
	out      string
	err      error
}

func (h *hookStub) ComposeAnswer(_ context.Context, mode CategoryMode, kind QuestionKind, _, _ string) (string, error) {
	h.called = true
	h.lastMode = mode
	h.lastKind = kind
	return h.out, h.err
}

func TestAIHookAllModes(t *testing.T) {
	modes := []CategoryMode{ModeBenar, ModeTypo, ModeNgasal, ModeCelah}
	for _, m := range modes {
		sess := baseSession(m)
		h := &hookStub{out: "  Budi\nSantoso  "}
		res := (&Responder{AI: h}).ComposeForKind(context.Background(), KindNama, AnswerInput{Session: sess, Random: seededRand(1)})
		if !h.called {
			t.Fatalf("expected hook to be called for mode %s", m)
		}
		if h.lastMode != m {
			t.Fatalf("expected lastMode=%s, got %s", m, h.lastMode)
		}
		if h.lastKind != KindNama {
			t.Fatalf("expected lastKind=%s, got %s", KindNama, h.lastKind)
		}
		if strings.Contains(res.Text, "\n") {
			t.Fatalf("expected text to be sanitized without newline, got %q", res.Text)
		}
	}

	// Error from hook → fall back to deterministic draft.
	sess := baseSession(ModeBenar)
	hErr := &hookStub{err: errors.New("boom")}
	resErr := (&Responder{AI: hErr}).ComposeForKind(context.Background(), KindNama, AnswerInput{Session: sess})
	if !strings.Contains(resErr.Text, sess.NamaPengadu) {
		t.Fatalf("on AI error should fall back to draft, got %q", resErr.Text)
	}
}

func TestApplyTypoPreservesFact(t *testing.T) {
	// No word in safeTypoPairs is present → unchanged.
	if got := applyTypo("Jl. Merdeka No. 1", seededRand(1)); got != "Jl. Merdeka No. 1" {
		t.Fatalf("address must not be mutated: %q", got)
	}
	// A target word is replaced exactly once.
	got := applyTypo("mohon dilanjutkan pak, terima kasih", seededRand(1))
	if strings.Count(got, "makasih")+strings.Count(got, "lanjut aja")+strings.Count(got, "mhon") == 0 {
		t.Fatalf("typo did not change any target word: %q", got)
	}
}

func TestGenerateRandomPreviewDeterministic(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	a := GenerateRandomPreview(seededRand(99), now)
	b := GenerateRandomPreview(seededRand(99), now)
	if a != b {
		t.Fatalf("preview must be deterministic: %+v vs %+v", a, b)
	}
	if _, _, err := ParseCoordinates(a.TitikLokasi); err != nil {
		t.Fatalf("preview maps link must parse: %q (%v)", a.TitikLokasi, err)
	}
	if a.TitikLokasiSrc != SourceRandom {
		t.Fatalf("source must be random, got %q", a.TitikLokasiSrc)
	}
	if a.TanggalKejadian == "2026-11-20" {
		t.Fatalf("tanggal should be >=1 day back, got %s", a.TanggalKejadian)
	}
}

// TestPhraseVariationsAndRepetition verifies Lapis A (variations) & Lapis B (repetition reminder)
func TestPhraseVariationsAndRepetition(t *testing.T) {
	sess := baseSession(ModeBenar)
	resp := responderFixture()

	// 1. Repetition detection for name
	inRepeat := AnswerInput{
		Session:  sess,
		Random:   seededRand(1),
		IsRepeat: true,
	}
	resRepeat := resp.ComposeForKind(context.Background(), KindNama, inRepeat)
	if !strings.Contains(resRepeat.Text, "Nama saya tadi sudah diinfokan") || !strings.Contains(resRepeat.Text, sess.NamaPengadu) {
		t.Fatalf("expected repetition prefix for name, got %q", resRepeat.Text)
	}

	// 2. Variations across different seeds
	seenPhrases := map[string]bool{}
	for i := int64(1); i <= 20; i++ {
		res := resp.ComposeForKind(context.Background(), KindKonfirmasi, AnswerInput{
			Session: sess,
			Random:  seededRand(i),
		})
		seenPhrases[res.Text] = true
	}
	if len(seenPhrases) < 3 {
		t.Fatalf("expected at least 3 distinct phrase variations for konfirmasi, got %d: %v", len(seenPhrases), seenPhrases)
	}
}

// TestDynamicReportWhenEmpty verifies Option A: when ReportText is empty,
// a dynamic contextual report is generated rather than failing or returning empty.
func TestDynamicReportWhenEmpty(t *testing.T) {
	sess := baseSession(ModeBenar)
	sess.ReportText = "" // empty report text
	resp := responderFixture()

	res := resp.ComposeForKind(context.Background(), KindIsiLaporan, AnswerInput{
		Session:    sess,
		Random:     seededRand(42),
		BotContext: "Boleh dijelaskan lebih dulu kejadian atau masalah yang ingin disampaikan?",
	})
	if strings.TrimSpace(res.Text) == "" {
		t.Fatalf("expected dynamic response when report text is empty, got empty")
	}
	if !strings.Contains(res.Text, "masalah") && !strings.Contains(res.Text, "lapor") && !strings.Contains(res.Text, "kendala") {
		t.Fatalf("expected context-relevant dynamic text, got %q", res.Text)
	}
}

// TestSeedReproducibility verifies that the exact same seed produces the exact same response series.
func TestSeedReproducibility(t *testing.T) {
	sess := baseSession(ModeBenar)
	resp := responderFixture()

	res1 := resp.ComposeForKind(context.Background(), KindNama, AnswerInput{Session: sess, Random: seededRand(777)})
	res2 := resp.ComposeForKind(context.Background(), KindNama, AnswerInput{Session: sess, Random: seededRand(777)})
	if res1.Text != res2.Text {
		t.Fatalf("same seed must produce identical response: %q vs %q", res1.Text, res2.Text)
	}
}
