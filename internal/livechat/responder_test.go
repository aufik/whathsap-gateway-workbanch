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
					if res.Text != sess.NamaPengadu {
						t.Fatalf("benar/nama: %q", res.Text)
					}
				case KindIsiLaporan:
					if res.Text != sess.ReportText {
						t.Fatalf("benar/isi: %q", res.Text)
					}
				case KindTanggal:
					if res.Text != sess.TanggalKejadian {
						t.Fatalf("benar/tanggal: %q", res.Text)
					}
				case KindAlamat:
					if res.Text != sess.AlamatKejadian {
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
					if !strings.Contains(strings.ToLower(res.Text), "iya") {
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

// hookStub verifies the AI hook is used for benar mode only, and that its
// output goes through sanitizeAIText.
type hookStub struct {
	called bool
	out    string
	err    error
}

func (h *hookStub) ComposeAnswer(_ context.Context, _ QuestionKind, _, _ string) (string, error) {
	h.called = true
	return h.out, h.err
}

func TestAIHookBenarOnly(t *testing.T) {
	sess := baseSession(ModeBenar)
	h := &hookStub{out: "  Budi\nSantoso  "}
	res := (&Responder{AI: h}).ComposeForKind(context.Background(), KindNama, AnswerInput{Session: sess})
	if !h.called || strings.Contains(res.Text, "\n") {
		t.Fatalf("expected hook to run + sanitize, got text=%q called=%v", res.Text, h.called)
	}

	// Typo mode must NOT hit the AI hook (determinism for QA).
	h2 := &hookStub{out: "irrelevant"}
	sess2 := baseSession(ModeTypo)
	_ = (&Responder{AI: h2}).ComposeForKind(context.Background(), KindNama, AnswerInput{Session: sess2, Random: seededRand(1)})
	if h2.called {
		t.Fatalf("typo mode must not call AI hook")
	}

	// Error from hook → fall back to deterministic draft.
	h3 := &hookStub{err: errors.New("boom")}
	res3 := (&Responder{AI: h3}).ComposeForKind(context.Background(), KindNama, AnswerInput{Session: sess})
	if res3.Text != sess.NamaPengadu {
		t.Fatalf("on AI error should fall back to draft, got %q", res3.Text)
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
