package livechat

import (
	"context"
	"math/rand"
	"strings"
)

// AIHook lets the caller inject an optional LLM. It is kept as an interface so
// the responder can fall back locally when the LLM is not configured (or takes
// too long) without pulling the ai package into every test.
type AIHook interface {
	// ComposeAnswer asks the LLM to rewrite a target answer into one chat
	// sentence. Implementations must return ("", nil) when disabled.
	ComposeAnswer(ctx context.Context, kind QuestionKind, draft, reportText string) (string, error)
}

// AnswerInput is the immutable session data the responder uses. It comes from
// the final values persisted to livechat.sessions, so they never re-roll.
type AnswerInput struct {
	Session     Session
	Assets      []Asset // owned by Session.UserID, ordered by creation
	Random      *rand.Rand
	OutsideUsed bool // set to true when this answer used an extra-Medan coord
}

// AnswerResult is one turn's outgoing message + the audit trail the engine
// writes into livechat.turns.
type AnswerResult struct {
	Kind        QuestionKind // what we detected on the bot side
	Text        string       // the text we will send (caption for media)
	MediaAsset  *Asset       // non-nil when a file should be sent this turn
	SendLoc     *Location    // non-nil when a LocationMessage should be sent
	MatchStatus string       // "benar" | "typo" | "ngasal" | "celah"
	Note        string       // free-form explanation for turns.note
	// DidUseOutsideLoc tells the engine to flip AnswerInput.OutsideUsed for the
	// next turn; it is only ever set by celah mode (max 1 per session).
	DidUseOutsideLoc bool
}

// Location is a parsed map coordinate ready to be sent as a LocationMessage.
type Location struct {
	Lat     float64
	Lng     float64
	Name    string
	Address string
}

// Responder turns a bot question into the next outgoing answer. It never calls
// the network; the optional AI hook only *rewrites* the deterministic draft.
type Responder struct {
	AI AIHook
}

// ComposeForKind is the primary entrypoint used by the engine: the engine
// detects the kind and asks the responder for the deterministic answer.
func (r *Responder) ComposeForKind(ctx context.Context, kind QuestionKind, in AnswerInput) AnswerResult {
	mode := in.Session.CategoryMode
	base, media, loc, note, usedOutside := r.localDraft(in, kind, mode)
	if r.AI != nil && media == nil && loc == nil && base != "" && mode == ModeBenar {
		// Keep AI strictly for the "benar" mode: typo/ngasal/celah must stay
		// deterministic so the QA signal is reproducible.
		if rewritten, err := r.AI.ComposeAnswer(ctx, kind, base, in.Session.ReportText); err == nil {
			if clean := sanitizeAIText(rewritten); clean != "" {
				base = clean
			}
		}
	}
	if len(base) > 900 {
		base = base[:900] + "…"
	}
	return AnswerResult{
		Kind:             kind,
		Text:             base,
		MediaAsset:       media,
		SendLoc:          loc,
		MatchStatus:      string(mode),
		Note:             note,
		DidUseOutsideLoc: usedOutside,
	}
}

// localDraft is the deterministic fallback table. Every branch reads from the
// session's *final* values so typo / ngasal / celah transformations do not
// change the underlying facts.
func (r *Responder) localDraft(in AnswerInput, kind QuestionKind, mode CategoryMode) (text string, media *Asset, loc *Location, note string, usedOutside bool) {
	sess := in.Session

	// 1. Build the correct answer for this field.
	base := ""
	switch kind {
	case KindNama:
		base = sess.NamaPengadu
	case KindTanggal:
		base = sess.TanggalKejadian
	case KindAlamat:
		base = sess.AlamatKejadian
	case KindIsiLaporan:
		base = sess.ReportText
	case KindTitikLokasi:
		base, loc, note = r.locationAnswer(in)
	case KindBerkas:
		base, media, note = r.mediaAnswer(in)
	case KindKonfirmasi:
		base = "iya, sudah benar"
	case KindLainnya, KindUnknown:
		base = "mohon dilanjutkan pak, terima kasih"
	}

	// 2. Apply mode transformations on top of the fact-correct answer.
	switch mode {
	case ModeBenar:
		return base, media, loc, note, false
	case ModeTypo:
		return applyTypo(base, in.Random), media, loc, joinNote(note, "mode typo: singkatan/typo ringan"), false
	case ModeNgasal:
		return applyNgasal(kind, in.Random), media, loc, joinNote(note, "mode ngasal: jawaban di luar konteks"), false
	case ModeCelah:
		celahText, celahLoc, celahMedia, extra := r.celahVariant(in, kind, base, loc, media)
		return celahText, celahMedia, celahLoc, joinNote(note, extra), celahLoc != nil
	}
	return base, media, loc, note, false
}

// locationAnswer respects titik_lokasi_source:
//   - empty → the bot should get "lewat"
//   - parseable → a real LocationMessage
//   - unparseable → the raw text (bot usually continues)
func (r *Responder) locationAnswer(in AnswerInput) (text string, loc *Location, note string) {
	if in.Session.TitikLokasiSrc == SourceEmpty || strings.TrimSpace(in.Session.TitikLokasi) == "" {
		return "lewat", nil, "titik lokasi dikosongkan penguji; dijawab lewat"
	}
	lat, lng, err := ParseCoordinates(in.Session.TitikLokasi)
	if err != nil {
		return in.Session.TitikLokasi, nil, "koordinat gagal diparse; lokasi dikirim sebagai teks"
	}
	return "", &Location{Lat: lat, Lng: lng, Name: "Lokasi Pengaduan", Address: in.Session.AlamatKejadian},
		"lokasi terkirim sebagai LocationMessage"
}

// mediaAnswer picks the first owned asset. When celah mode deliberately sends a
// wrong kind it is handled in celahVariant, not here.
func (r *Responder) mediaAnswer(in AnswerInput) (text string, media *Asset, note string) {
	asset := firstAssetForSession(in.Assets, in.Session.AssetIDs)
	if asset == nil {
		return "lewat", nil, "tidak ada berkas terpilih; dijawab lewat"
	}
	return captionForAsset(asset), asset, "berkas terpilih dikirim"
}

// ownsAsset reports whether assetID is one of the session's configured assets.
// It is the last gate before the engine streams bytes; no other asset may be
// sent under this session's identity.
func ownsAsset(in AnswerInput, assetID int64) bool {
	for _, id := range in.Session.AssetIDs {
		if id == assetID {
			return true
		}
	}
	return false
}

// FirstOwnedDocument returns the first document-kind asset that the session
// owns, or nil when none. Exposed for the engine's celah media rule (plan §6.5).
func FirstOwnedDocument(in AnswerInput) *Asset {
	for i := range in.Assets {
		if in.Assets[i].Kind == AssetDocument && ownsAsset(in, in.Assets[i].ID) {
			a := in.Assets[i]
			return &a
		}
	}
	return nil
}

// celahVariant produces a deliberate, bounded probe for celah mode.
func (r *Responder) celahVariant(in AnswerInput, kind QuestionKind, base string, loc *Location, media *Asset) (string, *Location, *Asset, string) {
	switch kind {
	case KindTitikLokasi:
		if !in.OutsideUsed {
			lat, lng := OuterMedanCoord(in.Random)
			return "", &Location{Lat: lat, Lng: lng, Name: "Lokasi Di Luar Medan", Address: "Di luar batas administratif Kota Medan"},
				nil, "celah: koordinat di luar Kota Medan (maks 1x per sesi)"
		}
		return "lewat", nil, nil, "celah: lokasi di luar Medan sudah dipakai; lanjut lewat"
	case KindBerkas:
		// Pick a document when one exists to test type validation; otherwise
		// behave like benar so we do not dead-end the bot.
		if doc := FirstOwnedDocument(in); doc != nil {
			return "ini dokumennya pak", nil, doc, "celah: dokumen dikirim saat bot meminta foto"
		}
		return "lewat", nil, nil, "celah: tidak ada dokumen untuk uji jenis berkas"
	case KindTanggal:
		return "2026-13-45", nil, nil, "celah: tanggal mustahil"
	case KindAlamat:
		return "-", nil, nil, "celah: alamat kosong"
	}
	// Other fields fall back to the correct answer so the session can finish.
	return base, loc, media, ""
}

func firstAssetForSession(assets []Asset, ids []int64) *Asset {
	want := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	for i := range assets {
		if _, ok := want[assets[i].ID]; ok {
			a := assets[i]
			return &a
		}
	}
	return nil
}

func captionForAsset(a *Asset) string {
	if a.Kind == AssetImage {
		return "ini fotonya pak"
	}
	return "ini dokumennya pak"
}

// applyTypo uses the safe-replace table copied from internal/ai's chaos
// generator. Numbers, dates, and alamat are preserved verbatim.
func applyTypo(text string, rng *rand.Rand) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	out := text
	if rng != nil {
		start := rng.Intn(len(safeTypoPairs))
		for k := 0; k < len(safeTypoPairs); k++ {
			pair := safeTypoPairs[(start+k)%len(safeTypoPairs)]
			if strings.Contains(out, pair[0]) {
				return strings.Replace(out, pair[0], pair[1], 1)
			}
		}
	}
	return out
}

// safeTypoPairs mirrors internal/ai/engine.go but only common noun/adjective
// words. Numbers, street names, dates are excluded.
var safeTypoPairs = [][2]string{
	{"informasi", "infomasi"},
	{"pelayanan", "pelyanan"},
	{"masyarakat", "masyrakat"},
	{"fasilitas", "fasiltas"},
	{"kesehatan", "kesehatn"},
	{"pemerintah", "pemrintah"},
	{"kelurahan", "kelurhan"},
	{"kecamatan", "kecamtan"},
	{"berdasarkan", "berdasrkan"},
	{"terima kasih", "makasih"},
	{"mohon", "mhon"},
	{"mohon dilanjutkan", "lanjut aja pak"},
}

var ngasalByKind = map[QuestionKind][]string{
	KindNama:        {"hmm siapa ya", "boleh tebak-tebakan aja pak"},
	KindTanggal:     {"kayaknya minggu lalu deh", "lupa pastinya, campur aduk"},
	KindAlamat:      {"deket pasar yang rame itu pak", "tempetnya agak masuk ke dalam"},
	KindIsiLaporan:  {"ada yang bikin sebel aja pak", "pokoknya kurang enak dilihat"},
	KindTitikLokasi: {"share lokasi gak bisa pak hehe", "lokasinya ada di hp aja"},
	KindBerkas:      {"fotonya belum sempat diambil", "nanti aja ya pak"},
	KindKonfirmasi:  {"terserah pak", "boleh iya boleh enggak"},
	KindLainnya:     {"iya pak", "begitulah pak"},
}

func applyNgasal(kind QuestionKind, rng *rand.Rand) string {
	pool := ngasalByKind[kind]
	if len(pool) == 0 {
		pool = ngasalByKind[KindLainnya]
	}
	if rng == nil {
		return pool[0]
	}
	return pool[rng.Intn(len(pool))]
}

// sanitizeAIText trims and rejects obviously unusable LLM output (empty,
// clearly a prompt echo, or a bot-internal word leak). Keeps the answer one line.
func sanitizeAIText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	if len(s) > 900 {
		s = s[:900]
	}
	return s
}

func joinNote(parts ...string) string {
	filled := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			filled = append(filled, strings.TrimSpace(p))
		}
	}
	return strings.Join(filled, " · ")
}
