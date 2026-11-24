package livechat

import (
	"regexp"
	"strings"
)

// QuestionKind is the bot's detected intent for one turn. Empty string means
// "unknown" so a real zero value never collides with the lainnya constant.
type QuestionKind string

const (
	KindUnknown         QuestionKind = ""
	KindNama            QuestionKind = "nama"
	KindJudulLaporan    QuestionKind = "judul_laporan"
	KindIsiLaporan      QuestionKind = "isi_laporan"
	KindTanggal         QuestionKind = "tanggal"
	KindAlamat          QuestionKind = "alamat"
	KindTitikLokasi     QuestionKind = "titik_lokasi"
	KindBerkas          QuestionKind = "berkas"
	KindBatalKonfirmasi QuestionKind = "batal_konfirmasi"
	KindKonfirmasi      QuestionKind = "konfirmasi"
	KindMenuAwal        QuestionKind = "menu_awal"
	KindLainnya         QuestionKind = "lainnya"
)

// kindRules is evaluated top-down; order matters when a bot message contains
// several cues (e.g. "Silakan isi nama lengkap dan alamat kejadian"). The most
// specific fields win so we never answer alamat with the name.
var kindRules = []struct {
	kind     QuestionKind
	patterns []string
}{
	// 1. Menu Awal / Sesi Selesai / Reset menu bot (harus dipotong dengan minta pengaduan baru)
	{KindMenuAwal, []string{
		"sesi layanan anda sebelumnya sudah berakhir",
		"sesi layanan anda telah berakhir",
		"sesi layanan berakhir",
		"chat nomor resmi pemko medan",
		"aduan yang tercatat pada nomor ini",
		"sebutkan nomor aduannya",
		"nomor aduan:",
		"selamat siang. saya wa ai pemko medan",
		"selamat pagi. saya wa ai pemko medan",
		"selamat malam. saya wa ai pemko medan",
		"saya wa ai pemko medan",
		"siap membantu anda",
	}},
	// 2. Anti-cancellation prompt must win before general confirmation
	{KindBatalKonfirmasi, []string{
		"apakah anda ingin membatalkan", "ingin membatalkan", "batalkan pengaduan",
		"membatalkan pengaduan", "batal pengaduan",
	}},
	// 3. Konfirmasi (ringkasan aduan)
	{KindKonfirmasi, []string{
		"ringkasan aduan", "sudah sesuai untuk dikirim", "apakah anda ingin",
		"apakah sudah benar", "ya/tidak", "benar? (ya/tidak)", "ketik batal",
		"ingin dikirim", "pengaduan mengenai hal", "aduan mengenai hal", "konfirmasi",
	}},
	// 4. Titik Lokasi
	{KindTitikLokasi, []string{
		"titik lokasi", "koordinat", "tautan peta", "link maps",
		"lokasi kejadian", "share lokasi", "tautan google", "google maps",
		"titik koordinat",
	}},
	// 5. Tanggal Kejadian
	{KindTanggal, []string{
		"tanggal kejadian", "format: yyyy-mm-dd", "format yyyy-mm-dd",
		"kapan kejadian", "tanggal peristiwa", "kapan kejadiannya",
		"kapan kejadiannya terjadi", "kapan terjadinya",
	}},
	// 6. Nama Pelapor
	{KindNama, []string{
		"isi nama", "nama lengkap", "nama pengadu", "siapa nama", "nama pelapor",
		"boleh diinfokan nama", "diinfokan nama", "nama anda",
	}},
	// 7. Alamat Kejadian
	{KindAlamat, []string{
		"alamat kejadian", "alamat lengkap", "isi alamat", "dimana alamat",
		"di mana alamat", "alamatnya",
	}},
	// 8. Berkas Lampiran
	{KindBerkas, []string{
		"unggah", "upload", "lampirkan", "kirim foto", "kirimkan foto",
		"kirim dokumen", "bukti foto", "bukti",
		"kirim lampiran", "lampiran terkait", "foto atau dokumen",
		"lampiran berkas", "kirim berkas",
	}},
	// 9. Judul Laporan (topik singkat masalah layanan publik)
	{KindJudulLaporan, []string{
		"judul laporan", "judul aduan", "ingin mengadukan tentang apa",
		"sebutkan secara singkat masalah", "masalah layanan publik yang ingin anda adukan",
		"ingin anda adukan", "hal yang ingin diadukan", "hal yang ingin dilaporkan",
		"tentang apa yang ingin diadukan", "aduan tentang apa", "ingin melaporkan apa",
		"apa yang ingin diadukan", "apa yang ingin dilaporkan",
	}},
	// 10. Isi Laporan / Kejadian (deskripsi kronologi kejadian detail)
	{KindIsiLaporan, []string{
		"apa isi kejadiannya", "isi kejadiannya", "isi kejadian", "apa kejadiannya",
		"isi laporan", "isi pengaduan", "tuliskan aduan", "tulis pengaduan",
		"ceritakan pengaduan", "ceritakan kejadiannya", "ceritakan kejadian",
		"kronologi kejadian", "kronologi", "uraian kejadian", "uraian masalah",
		"detail kejadian", "detail masalah", "rincian kejadian", "deskripsi kejadian",
		"laporan pengaduan", "dijelaskan lebih dulu kejadian", "kejadian atau masalah",
		"masalah yang ingin disampaikan", "kejadian yang ingin disampaikan",
		"jelaskan kejadian", "jelaskan masalah",
	}},
}

// doneSignals mark that the bot accepted the complaint. Order matters for the
// Excel "alasan selesai" column: the first match is written verbatim to end_reason.
var doneSignals = []string{
	"berhasil dikirim",
	"laporan anda telah kami terima",
	"pengaduan anda telah kami terima",
	"pengaduan berhasil",
	"tiket anda",
	"nomor tiket",
	"terkirim ke",
	"diterima",
	"selesai",
	"terkirim",
}

var ticketRe = regexp.MustCompile(`#\s?([0-9]{5,6})\b`)

// answerFallbackEscalation is used when the bot took more turns than we could
// classify (last line was "lainnya"). We progressively fill laporan -> nama ->
// tanggal so the bot can move on instead of repeating the same question.
var answerFallbackEscalation = []QuestionKind{
	KindIsiLaporan, KindNama, KindTanggal, KindAlamat, KindTitikLokasi,
}

// NextFallback returns the next best kind to answer when a bot turn was not
// classified. reported is how many times we already fell back in this session.
func NextFallback(reported int) QuestionKind {
	if reported < 0 {
		reported = 0
	}
	if reported >= len(answerFallbackEscalation) {
		return KindLainnya
	}
	return answerFallbackEscalation[reported]
}

// NormalizeForDetect lowercases and collapses whitespace without removing
// punctuation: the bot uses em-dashes, quotes, and ellipses that must survive.
func NormalizeForDetect(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer(
		"\u2013", "-", "\u2014", "-", "\u2019", "'", "\u201c", `"`, "\u201d", `"`,
	).Replace(s)
	// Strip WhatsApp markdown markers (*bold*, _italic_, `code`) and the
	// zero-width padding bytes the bot sprinkles between bubbles. Without this,
	// phrases like "*Kapan* kejadiannya" never match "kapan kejadian".
	s = markdownStripper.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

var markdownStripper = strings.NewReplacer("*", " ", "_", " ", "`", " ", "\u200b", " ", "\u200c", " ", "\u200d", " ")

// DetectQuestionKind classifies one bot message.
func DetectQuestionKind(botText string) QuestionKind {
	if strings.TrimSpace(botText) == "" {
		return KindUnknown
	}
	norm := NormalizeForDetect(botText)
	for _, rule := range kindRules {
		for _, pat := range rule.patterns {
			if strings.Contains(norm, pat) {
				return rule.kind
			}
		}
	}
	return KindLainnya
}

var cancelSignals = []string{
	"pengaduan sudah kami batalkan",
	"pengaduan telah dibatalkan",
	"pengaduan dibatalkan",
	"sudah kami batalkan",
	"kami batalkan ya",
}

// IsCancelled reports whether the bot announced that the complaint was cancelled.
func IsCancelled(botText string) (bool, string) {
	if strings.TrimSpace(botText) == "" {
		return false, ""
	}
	norm := NormalizeForDetect(botText)
	for _, sig := range cancelSignals {
		if strings.Contains(norm, sig) {
			return true, sig
		}
	}
	return false, ""
}

// IsDone reports whether the bot announced completion and the human reason
// that matched, e.g. ("laporan anda telah kami terima", "laporan anda telah kami terima").
func IsDone(botText string) (bool, string) {
	if strings.TrimSpace(botText) == "" {
		return false, ""
	}
	norm := NormalizeForDetect(botText)
	for _, csig := range cancelSignals {
		if strings.Contains(norm, csig) {
			return false, ""
		}
	}
	for _, sig := range doneSignals {
		if strings.Contains(norm, sig) {
			return true, sig
		}
	}
	return false, ""
}

// ExtractTicket pulls a 5-6 digit ticket number written as `#385692` (with or
// without a space after `#`). Returns "" when the bot did not include one.
func ExtractTicket(botText string) string {
	if strings.TrimSpace(botText) == "" {
		return ""
	}
	m := ticketRe.FindStringSubmatch(botText)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// LastNExcerpt returns the concatenation of the last n non-empty bubbles, in
// their original order. It is what gets persisted to sessions.last_chat_excerpt
// when the session closes.
func LastNExcerpt(bodies []string, n int) string {
	collected := make([]string, 0, n)
	for i := len(bodies) - 1; i >= 0 && len(collected) < n; i-- {
		if strings.TrimSpace(bodies[i]) == "" {
			continue
		}
		collected = append(collected, strings.TrimSpace(bodies[i]))
	}
	// collected is reversed; flip back.
	for i, j := 0, len(collected)-1; i < j; i, j = i+1, j-1 {
		collected[i], collected[j] = collected[j], collected[i]
	}
	return strings.Join(collected, "\n")
}
