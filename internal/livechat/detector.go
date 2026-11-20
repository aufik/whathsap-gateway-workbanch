package livechat

import (
	"regexp"
	"strings"
)

// QuestionKind is the bot's detected intent for one turn. Empty string means
// "unknown" so a real zero value never collides with the lainnya constant.
type QuestionKind string

const (
	KindUnknown     QuestionKind = ""
	KindNama        QuestionKind = "nama"
	KindIsiLaporan  QuestionKind = "isi_laporan"
	KindTanggal     QuestionKind = "tanggal"
	KindAlamat      QuestionKind = "alamat"
	KindTitikLokasi QuestionKind = "titik_lokasi"
	KindBerkas      QuestionKind = "berkas"
	KindKonfirmasi  QuestionKind = "konfirmasi"
	KindLainnya     QuestionKind = "lainnya"
)

// kindRules is evaluated top-down; order matters when a bot message contains
// several cues (e.g. "Silakan isi nama lengkap dan alamat kejadian"). The most
// specific fields win so we never answer alamat with the name.
var kindRules = []struct {
	kind     QuestionKind
	patterns []string
}{
	{KindTitikLokasi, []string{
		"titik lokasi", "koordinat", "tautan peta", "link maps", "lokasi kejadian", "share lokasi",
	}},
	{KindTanggal, []string{
		"tanggal kejadian", "format: yyyy-mm-dd", "format yyyy-mm-dd", "kapan kejadian", "tanggal peristiwa",
	}},
	{KindNama, []string{
		"isi nama", "nama lengkap", "nama pengadu", "siapa nama", "nama pelapor",
	}},
	{KindAlamat, []string{
		"alamat kejadian", "alamat lengkap", "isi alamat", "dimana alamat", "di mana alamat",
	}},
	{KindBerkas, []string{
		"unggah", "upload", "lampirkan", "kirim foto", "kirimkan foto", "kirim dokumen", "bukti foto", "bukti",
	}},
	{KindIsiLaporan, []string{
		"isi laporan", "tuliskan aduan", "isi pengaduan", "tulis pengaduan", "ceritakan pengaduan", "laporan pengaduan",
	}},
	{KindKonfirmasi, []string{
		"apakah anda ingin", "apakah sudah benar", "ya/tidak", "konfirmasi", "benar? (ya/tidak)",
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

// NormalizeForDetect lowercases and collapses whitespace without removing
// punctuation: the bot uses em-dashes, quotes, and ellipses that must survive.
func NormalizeForDetect(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer(
		"\u2013", "-", "\u2014", "-", "\u2019", "'", "\u201c", `"`, "\u201d", `"`,
	).Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

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

// IsDone reports whether the bot announced completion and the human reason
// that matched, e.g. ("laporan anda telah kami terima", "laporan anda telah kami terima").
func IsDone(botText string) (bool, string) {
	if strings.TrimSpace(botText) == "" {
		return false, ""
	}
	norm := NormalizeForDetect(botText)
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
