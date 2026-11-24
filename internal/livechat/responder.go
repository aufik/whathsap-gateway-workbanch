package livechat

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
)

// AIHook lets the caller inject an optional LLM. It is kept as an interface so
// the responder can fall back locally when the LLM is not configured (or takes
// too long) without pulling the ai package into every test.
type AIHook interface {
	// ComposeAnswer asks the LLM to rewrite a target answer into one chat
	// sentence according to the session's category mode (benar, typo, ngasal, celah).
	// Implementations must return ("", nil) when disabled.
	ComposeAnswer(ctx context.Context, mode CategoryMode, kind QuestionKind, draft, botQuestion string) (string, error)
}

// AnswerInput is the immutable session data the responder uses. It comes from
// the final values persisted to livechat.sessions, so they never re-roll.
type AnswerInput struct {
	Session     Session
	Assets      []Asset // owned by Session.UserID, ordered by creation
	Random      *rand.Rand
	OutsideUsed bool // set to true when this answer used an extra-Medan coord
	BotContext  string
	IsRepeat    bool
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

	// When AI hook is present and we don't have media/loc, let AI compose the answer
	// according to the selected mode (benar, typo, ngasal, celah).
	if r.AI != nil && media == nil && loc == nil {
		promptContext := in.BotContext
		if promptContext == "" {
			promptContext = in.Session.Title
		}
		factualDraft, _, _, _, _ := r.factualDraft(in, kind)
		draft := factualDraft
		if draft == "" {
			draft = base
		}
		if rewritten, err := r.AI.ComposeAnswer(ctx, mode, kind, draft, promptContext); err == nil {
			if clean := sanitizeAIText(rewritten); clean != "" {
				base = clean
				note = joinNote(note, fmt.Sprintf("ai:%s", mode))
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

// factualDraft generates the fact-correct draft for this field using phrase variations (Lapis A).
func (r *Responder) factualDraft(in AnswerInput, kind QuestionKind) (base string, media *Asset, loc *Location, note string, usedOutside bool) {
	sess := in.Session
	switch kind {
	case KindMenuAwal:
		base = "halo min, saya mau buat pengaduan baru"
	case KindNama:
		base = pickNama(sess.NamaPengadu, in.IsRepeat, in.Random)
	case KindJudulLaporan:
		base = pickJudul(sess.Title, in.IsRepeat, in.Random)
	case KindTanggal:
		base = pickTanggal(sess.TanggalKejadian, in.IsRepeat, in.Random)
	case KindAlamat:
		base = pickAlamat(sess.AlamatKejadian, in.IsRepeat, in.Random)
	case KindIsiLaporan:
		base = pickReportOrCoherent(sess.ReportText, sess.Title, in.IsRepeat, in.Random)
	case KindTitikLokasi:
		base, loc, note = r.locationAnswer(in)
	case KindBerkas:
		base, media, note = r.mediaAnswer(in)
	case KindBatalKonfirmasi:
		base = pickBatalTolak(in.Random)
	case KindKonfirmasi:
		base = pickKonfirmasi(in.Random)
	case KindLainnya, KindUnknown:
		base = pickLainnya(in.Random)
	}
	return base, media, loc, note, false
}

// localDraft is the deterministic fallback table. Every branch reads from the
// session's *final* values so typo / ngasal / celah transformations do not
// change the underlying facts.
func (r *Responder) localDraft(in AnswerInput, kind QuestionKind, mode CategoryMode) (text string, media *Asset, loc *Location, note string, usedOutside bool) {
	base, media, loc, note, _ := r.factualDraft(in, kind)

	// Apply mode transformations on top of the fact-correct answer.
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
	return captionForAsset(asset, in.Random), asset, "berkas terpilih dikirim"
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

func captionForAsset(a *Asset, rng *rand.Rand) string {
	if a.Kind == AssetImage {
		opts := []string{
			"ini fotonya pak",
			"ini lampiran fotonya min",
			"bukti fotonya ini ya pak",
			"lampiran berkas fotonya pak",
		}
		if rng == nil {
			return opts[0]
		}
		return opts[rng.Intn(len(opts))]
	}
	opts := []string{
		"ini dokumennya pak",
		"ini lampiran dokumennya min",
		"berkas pendukungnya ini pak",
	}
	if rng == nil {
		return opts[0]
	}
	return opts[rng.Intn(len(opts))]
}

func pickKonfirmasi(rng *rand.Rand) string {
	opts := []string{
		"iya pak, sudah benar",
		"betul min, data sudah sesuai",
		"ya sudah sesuai semua, silakan dikirim",
		"iya benar min, mohon diproses ya",
		"sudah sesuai pak, lanjutkan",
	}
	if rng == nil {
		return opts[0]
	}
	return opts[rng.Intn(len(opts))]
}

func pickBatalTolak(rng *rand.Rand) string {
	opts := []string{
		"tidak min, jangan dibatalkan, lanjutkan pengaduan saya",
		"tidak mau batal pak, tolong dilanjutkan ya",
		"jangan batal pak, tolong diproses laporannya",
		"tidak batal min, mohon dilanjutkan",
	}
	if rng == nil {
		return opts[0]
	}
	return opts[rng.Intn(len(opts))]
}

func pickNama(name string, isRepeat bool, rng *rand.Rand) string {
	if isRepeat {
		return "Nama saya tadi sudah diinfokan ya pak: " + name
	}
	opts := []string{
		name,
		"Nama saya " + name + " pak",
		name + " min",
		"Saya " + name + " pak",
	}
	if rng == nil {
		return opts[0]
	}
	return opts[rng.Intn(len(opts))]
}

func pickTanggal(tgl string, isRepeat bool, rng *rand.Rand) string {
	if isRepeat {
		return "Tanggal kejadiannya tadi " + tgl + " pak"
	}
	opts := []string{
		tgl,
		"Kejadiannya tanggal " + tgl + " pak",
		"Tanggal " + tgl + " min",
		"Pada tanggal " + tgl,
	}
	if rng == nil {
		return opts[0]
	}
	return opts[rng.Intn(len(opts))]
}

func pickAlamat(alamat string, isRepeat bool, rng *rand.Rand) string {
	if isRepeat {
		return "Alamatnya di " + alamat + " ya pak"
	}
	opts := []string{
		alamat,
		"Lokasinya di " + alamat + " pak",
		"Kejadian di " + alamat + " min",
		"Di " + alamat + " pak",
	}
	if rng == nil {
		return opts[0]
	}
	return opts[rng.Intn(len(opts))]
}

func pickReport(report string, isRepeat bool, rng *rand.Rand) string {
	if isRepeat {
		opts := []string{
			"Maksud saya laporannya mengenai " + report + " pak",
			"Masalah layanan publiknya: " + report + " min",
			"Laporannya terkait " + report + ", mohon dibantu ya pak",
			report + " pak, mohon ditindaklanjuti",
		}
		if rng == nil {
			return opts[0]
		}
		return opts[rng.Intn(len(opts))]
	}
	opts := []string{
		report,
		"Begini pak, " + report,
		"Ini laporannya min: " + report,
		"Masalahnya: " + report,
	}
	if rng == nil {
		return opts[0]
	}
	return opts[rng.Intn(len(opts))]
}

func pickJudul(title string, isRepeat bool, rng *rand.Rand) string {
	clean := strings.TrimSpace(title)
	if clean == "" || strings.HasPrefix(clean, "Live Chat ") || strings.HasPrefix(clean, "Sesi ") {
		clean = "Lampu jalan mati"
	}
	if idx := strings.Index(clean, " ("); idx > 0 {
		clean = strings.TrimSpace(clean[:idx])
	}
	if isRepeat {
		return clean
	}
	vars := []string{
		clean,
		clean + " min",
		"Mau lapor " + strings.ToLower(clean) + " min",
		"Saya mau buat laporan " + strings.ToLower(clean),
	}
	if rng == nil {
		return vars[0]
	}
	return vars[rng.Intn(len(vars))]
}

func pickReportOrCoherent(reportText, title string, isRepeat bool, rng *rand.Rand) string {
	clean := strings.TrimSpace(reportText)
	if clean != "" {
		return pickReport(clean, isRepeat, rng)
	}
	cleanTitle := strings.ToLower(title)
	for _, c := range randomCases {
		if strings.Contains(cleanTitle, strings.ToLower(c.Title)) || strings.Contains(strings.ToLower(c.Title), cleanTitle) {
			return pickReport(c.ReportText, isRepeat, rng)
		}
	}
	if len(randomCases) > 0 {
		idx := 0
		if rng != nil {
			idx = rng.Intn(len(randomCases))
		}
		return pickReport(randomCases[idx].ReportText, isRepeat, rng)
	}
	return "Ada kendala di lokasi tersebut min, tolong segera dibantu diperiksa."
}

func pickDynamicReport(botContext string, rng *rand.Rand) string {
	opts := []string{
		"Mengenai masalah tersebut tolong segera dibantu tindak lanjuti ya pak.",
		"Iya min, mohon diteruskan laporannya ke dinas terkait agar bisa dicek.",
		"Ada kendala di lokasi tersebut min, tolong segera dibantu diperiksa.",
		"Iya min, mohon bantuannya untuk menangani masalah yang saya laporkan ini.",
		"Benar min, tolong dibantu solusinya untuk kejadian tersebut.",
	}
	if rng == nil {
		return opts[0]
	}
	return opts[rng.Intn(len(opts))]
}

func pickLainnya(rng *rand.Rand) string {
	opts := []string{
		"mohon dilanjutkan pak, terima kasih",
		"lanjut aja pak",
		"siap min, silakan",
		"oke pak, bagaimana kelanjutannya?",
		"mohon bantuannya ya min",
	}
	if rng == nil {
		return opts[0]
	}
	return opts[rng.Intn(len(opts))]
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
	KindMenuAwal:        {"halo min, saya mau buat pengaduan baru"},
	KindNama:            {"hmm siapa ya", "boleh tebak-tebakan aja pak"},
	KindJudulLaporan:    {"masalahnya agak rumit min", "pokoknya ada yang rusak", "soal fasilitas umum ini min"},
	KindTanggal:         {"kayaknya minggu lalu deh", "lupa pastinya, campur aduk"},
	KindAlamat:          {"deket pasar yang rame itu pak", "tempetnya agak masuk ke dalam"},
	KindIsiLaporan:      {"ada yang bikin sebel aja pak", "pokoknya kurang enak dilihat"},
	KindTitikLokasi:     {"share lokasi gak bisa pak hehe", "lokasinya ada di hp aja"},
	KindBerkas:          {"fotonya belum sempat diambil", "nanti aja ya pak"},
	KindBatalKonfirmasi: {"jangan dibatalkan pak, lanjutin aja", "eh jangan batal dong min"},
	KindKonfirmasi:      {"terserah pak", "boleh iya boleh enggak"},
	KindLainnya:         {"iya pak", "begitulah pak"},
	KindUnknown:         {"mohon dibantu ya pak"},
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
