package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/model"
)

var (
	reBerkasLine   = regexp.MustCompile(`(?im)\[berkas\][^\n]*`)
	reKoordLine    = regexp.MustCompile(`(?im)\[koordinat\]\s*([-+]?\d+(?:\.\d+)?)\s*,\s*([-+]?\d+(?:\.\d+)?)`)
	reBubblePrefix = regexp.MustCompile(`(?m)^\s*\[\d+\]\s*`)
	reDatasetSlug  = regexp.MustCompile(`(?i)satudata\.medan\.go\.id/dataset/([a-z0-9\-]+)`)
	reSimpangTitle = regexp.MustCompile(`(?i)harga\s+(.+?)\s+terbaru`)
	reDigitsToken  = regexp.MustCompile(`\b\d{2,4}\b`)
	reMultiSpace   = regexp.MustCompile(`\s+`)
)

type Engine struct {
	cfg    *config.AppConfig
	client *http.Client
}

func NewEngine(cfg *config.AppConfig) *Engine {
	timeout := time.Duration(cfg.LLMTimeoutMs) * time.Millisecond
	if timeout < 5*time.Second {
		timeout = 20 * time.Second
	}
	return &Engine{
		cfg:    cfg,
		client: &http.Client{Timeout: timeout},
	}
}

func (e *Engine) GenerateQuestion(ctx context.Context, row model.TestRow, mode model.QuestionMode, variationSeed int64) (string, error) {
	ref := strings.TrimSpace(row.QuestionRef)
	if ref == "" {
		return "", errors.New("teks pertanyaan referensi kosong")
	}

	switch mode {
	case model.ModeExact:
		return ref, nil
	case model.ModeManual:
		if strings.TrimSpace(row.QuestionInput) != "" {
			return strings.TrimSpace(row.QuestionInput), nil
		}
		return ref, nil
	case model.ModeNatural:
		if e.isLLMEnabled() {
			if out, err := e.generateViaLLM(ctx, row, mode); err == nil && out != "" && preservesCriticalNumbers(ref, out) {
				return out, nil
			}
		}
		return generateNaturalLocal(ref, row.No, variationSeed), nil
	case model.ModeChaos:
		if e.isLLMEnabled() {
			if out, err := e.generateViaLLM(ctx, row, mode); err == nil && out != "" && preservesCriticalNumbers(ref, out) {
				return out, nil
			}
		}
		return generateChaosLocal(ref, row.No, variationSeed), nil
	default:
		return ref, nil
	}
}

func (e *Engine) EvaluateReply(ctx context.Context, row model.TestRow, actualReply string) (string, string) {
	if strings.EqualFold(strings.TrimSpace(row.StatusBank), "waiting") {
		if strings.TrimSpace(actualReply) == "" {
			return "Tidak", "Status bank waiting (belum ada jawaban di bank) & bot tidak membalas"
		}
		return "Tidak", "Status bank waiting (belum ada jawaban resmi di bank data)"
	}

	actTrim := strings.TrimSpace(actualReply)
	if actTrim == "" {
		return "Tidak", "Bot tidak mengirim balasan hingga batas waktu tunggu habis (timeout)"
	}

	expTrim := strings.TrimSpace(row.ExpectedReply)
	expHasFile := strings.Contains(strings.ToLower(expTrim), "[berkas]")
	actHasFile := strings.Contains(strings.ToLower(actTrim), "[berkas]")
	expCoords := extractCoords(expTrim)
	actCoords := extractCoords(actTrim)
	expHasCoord := len(expCoords) == 2
	actHasCoord := len(actCoords) == 2

	var missingParts []string
	if expHasFile && !actHasFile {
		missingParts = append(missingParts, "lampiran [Berkas] tidak dikirim bot")
	}
	if expHasCoord && !actHasCoord {
		missingParts = append(missingParts, "titik lokasi [Koordinat] tidak dikirim bot")
	} else if expHasCoord && actHasCoord {
		dist := math.Hypot(expCoords[0]-actCoords[0], expCoords[1]-actCoords[1])
		if dist > 0.02 {
			missingParts = append(missingParts, fmt.Sprintf("koordinat lokasi melenceng (data: %.5f,%.5f vs sistem: %.5f,%.5f)", expCoords[0], expCoords[1], actCoords[0], actCoords[1]))
		}
	}

	// Special handling for real-time commodity prices (Aplikasi Simpang)
	if strings.EqualFold(strings.TrimSpace(row.Kategori), "Aplikasi Simpang") || strings.Contains(strings.ToLower(expTrim), "simpang.medan.go.id") {
		okSimpang, reasonSimpang := evaluateSimpangDynamic(row, expTrim, actTrim)
		if okSimpang && len(missingParts) == 0 {
			return "Cocok", reasonSimpang
		}
		if okSimpang && len(missingParts) > 0 {
			return "Tidak", "Teks harga Simpang sesuai, namun " + strings.Join(missingParts, "; ")
		}
	}

	// Dataset URL slug verification if reference contains satudata dataset link
	expSlug := extractDatasetSlug(expTrim)
	actSlug := extractDatasetSlug(actTrim)
	slugMismatch := expSlug != "" && actSlug != "" && !strings.EqualFold(expSlug, actSlug)
	slugMissing := expSlug != "" && actSlug == ""

	normExp := normalizeBodyForCompare(expTrim)
	normAct := normalizeBodyForCompare(actTrim)

	if slugMismatch {
		return "Tidak", fmt.Sprintf("Bot mengirim tautan dataset berbeda (%s vs %s)", actSlug, expSlug)
	}

	// Fast deterministic match
	sim := tokenDiceSimilarity(normExp, normAct)
	exactOrHigh := normExp != "" && (normExp == normAct || strings.Contains(normAct, normExp) || sim >= 0.80)

	if exactOrHigh && !slugMissing {
		if len(missingParts) > 0 {
			return "Tidak", "Isi teks sesuai data, tetapi " + strings.Join(missingParts, "; ")
		}
		if actHasFile && !expHasFile {
			return "Cocok", "Makna jawaban sesuai data (bot menyertakan lampiran tambahan)"
		}
		return "Cocok", ""
	}

	// Semantic Judge via LLM if configured
	if e.isLLMEnabled() {
		llmMatch, llmReason, err := e.judgeViaLLM(ctx, row, expTrim, actTrim)
		if err == nil {
			if llmMatch && !slugMissing {
				if len(missingParts) > 0 {
					return "Tidak", "Makna teks cocok, namun " + strings.Join(missingParts, "; ")
				}
				return "Cocok", llmReason
			}
			if !llmMatch {
				if llmReason == "" {
					llmReason = "Makna balasan bot berbeda dari bank jawaban"
				}
				return "Tidak", llmReason
			}
		}
	}

	// Local Heuristic Semantic Fallback (checks key numbers, entities, URL slug & token overlap)
	numbersOK := preservesCriticalNumbers(normExp, normAct)
	if sim >= 0.58 && numbersOK && !slugMissing {
		if len(missingParts) > 0 {
			return "Tidak", "Inti jawaban mirip, namun " + strings.Join(missingParts, "; ")
		}
		return "Cocok", fmt.Sprintf("Kemiripan isi sesuai (skor %.0f%%)", sim*100)
	}

	var reasons []string
	if slugMissing {
		reasons = append(reasons, fmt.Sprintf("tautan dataset %s tidak ditemukan pada balasan", expSlug))
	}
	if !numbersOK {
		reasons = append(reasons, "angka/tahun/jam kunci berbeda dari data acuan")
	}
	reasons = append(reasons, missingParts...)
	if len(reasons) == 0 {
		reasons = append(reasons, fmt.Sprintf("isi jawaban berbeda dari data acuan (kemiripan %.0f%%)", sim*100))
	}
	return "Tidak", strings.Join(reasons, "; ")
}

func evaluateSimpangDynamic(row model.TestRow, exp, act string) (bool, string) {
	actLower := strings.ToLower(act)
	commodity := ""
	if m := reSimpangTitle.FindStringSubmatch(exp); len(m) > 1 {
		commodity = strings.ToLower(strings.TrimSpace(m[1]))
	}
	if commodity != "" && !strings.Contains(actLower, commodity) {
		// Check token-by-token for multi-word commodities
		for _, tok := range strings.Fields(commodity) {
			if len(tok) > 2 && !strings.Contains(actLower, tok) {
				return false, fmt.Sprintf("komoditas %q tidak disebutkan pada balasan bot", commodity)
			}
		}
	}
	hasPrice := strings.Contains(actLower, "rp") && (strings.Contains(actLower, "terendah") || strings.Contains(actLower, "rata-rata") || strings.Contains(actLower, "harga"))
	hasSource := strings.Contains(actLower, "simpang.medan.go.id") || strings.Contains(actLower, "simpang")
	if hasPrice && hasSource {
		return true, "Data harga harian Aplikasi Simpang sesuai komoditas & format sumber"
	}
	return false, "Format balasan harga Aplikasi Simpang tidak lengkap"
}

func (e *Engine) isLLMEnabled() bool {
	return strings.TrimSpace(e.cfg.LLMBaseURL) != "" && strings.TrimSpace(e.cfg.LLMModel) != ""
}

type chatCompletionReq struct {
	Model       string        `json:"model"`
	Temperature float64       `json:"temperature"`
	Messages    []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResp struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func (e *Engine) chatCompletion(ctx context.Context, systemPrompt, userPrompt string, temp float64) (string, error) {
	endpoint := strings.TrimRight(e.cfg.LLMBaseURL, "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		if strings.HasSuffix(endpoint, "/v1") {
			endpoint += "/chat/completions"
		} else {
			endpoint += "/v1/chat/completions"
		}
	}

	payload, err := json.Marshal(chatCompletionReq{
		Model:       e.cfg.LLMModel,
		Temperature: temp,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.cfg.LLMAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.LLMAPIKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, string(body))
	}

	var parsed chatCompletionResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("empty LLM choices")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

func (e *Engine) generateViaLLM(ctx context.Context, row model.TestRow, mode model.QuestionMode) (string, error) {
	var sys string
	if mode == model.ModeChaos {
		sys = "Kamu penguji QA chatbot WhatsApp Pemko Medan. Ubah pertanyaan acuan menjadi 1 kalimat chat warga yang agak berantakan/aneh namun TETAP LOGIS dan jelas maksudnya (boleh pakai singkatan chat seperti yg, thn, dmn, brp, gmn, klo, utk, susunan kalimat dibalik, atau 1 typo jari wajar pada kata umum). WAJIB JAGA UTUH semua angka tahun, nama kecamatan/jalan, nama komoditas, dan singkatan penting (PKL, KK, SPBE, BKAD, UPT, SMP, SD, BPJS). Balas HANYA kalimat pertanyaannya saja tanpa tanda kutip atau penjelasan."
	} else {
		sys = "Kamu warga Kota Medan yang bertanya sopan dan santai lewat WhatsApp ke Bot Pemko Medan. Ubah teks pertanyaan/judul data berikut menjadi 1 kalimat tanya percakapan natural sehari-hari. WAJIB JAGA UTUH semua angka tahun, nama kecamatan, komoditas, dan singkatan penting. Balas HANYA kalimat pertanyaannya saja tanpa tanda kutip."
	}
	usr := fmt.Sprintf("Kategori: %s\nOPD: %s\nTeks Acuan: %s", row.Kategori, row.OPD, row.QuestionRef)
	out, err := e.chatCompletion(ctx, sys, usr, 0.7)
	if err != nil {
		return "", err
	}
	out = strings.Trim(out, "\"'` \n\t")
	if idx := strings.IndexByte(out, '\n'); idx > 0 {
		out = strings.TrimSpace(out[:idx])
	}
	return out, nil
}

func (e *Engine) judgeViaLLM(ctx context.Context, row model.TestRow, exp, act string) (bool, string, error) {
	sys := `Kamu auditor QA Bot WhatsApp Pemerintah Kota Medan. Bandingkan "Balasan Acuan" dengan "Balasan Bot".
Aturan:
1. Pilih match=true bila inti informasi, angka kunci, syarat, atau tautan dataset memiliki makna yang sama walau susunan kata sedikit berbeda.
2. Abaikan perbedaan kode nama file internal setelah tag [Berkas] selama sama-sama mengirim info dataset yang tepat.
3. Balas HANYA JSON valid satu baris: {"match": true, "reason": "singkat jelas"}`
	usr := fmt.Sprintf("Pertanyaan: %s\n\nBalasan Acuan:\n%s\n\nBalasan Bot:\n%s", row.QuestionRef, exp, act)
	raw, err := e.chatCompletion(ctx, sys, usr, 0.1)
	if err != nil {
		return false, "", err
	}
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start >= 0 && end > start {
		raw = raw[start : end+1]
	}
	var res struct {
		Match  bool   `json:"match"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return false, "", err
	}
	return res.Match, strings.TrimSpace(res.Reason), nil
}

func generateNaturalLocal(ref string, rowNo int, seed int64) string {
	rng := seededRand(ref, rowNo, seed)
	clean := strings.TrimSpace(strings.ReplaceAll(ref, " ?", "?"))
	lower := strings.ToLower(clean)

	isQuestion := strings.HasSuffix(clean, "?") ||
		hasAnyPrefix(lower, "apakah ", "apa ", "berapa ", "dimana ", "di mana ", "kapan ", "siapa ", "bagaimana ")

	if !isQuestion {
		templates := []string{
			"Halo kak, boleh minta info %s?",
			"Permisi min, mau lihat data %s dong",
			"Info mengenai %s ada kak?",
			"Tolong tampilkan data %s ya",
			"Kak, bisa cek informasi %s?",
			"Mau tanya data %s dong min",
		}
		tpl := templates[rng.Intn(len(templates))]
		return fmt.Sprintf(tpl, clean)
	}

	body := strings.TrimSuffix(clean, "?")
	prefixes := []string{
		"Kak mau tanya, ",
		"Permisi min, ",
		"Halo, kalau boleh tahu ",
		"Mau nanya dong, ",
		"Info dong kak, ",
	}
	p := prefixes[rng.Intn(len(prefixes))]
	if len(body) > 1 && unicode.IsUpper(rune(body[0])) && unicode.IsLower(rune(body[1])) {
		body = strings.ToLower(body[:1]) + body[1:]
	}
	return p + body + "?"
}

var safeTypoPairs = [][2]string{
	{"tradisional", "tradisonal"},
	{"informasi", "infromasi"},
	{"persentase", "presentase"},
	{"pelayanan", "pelyanan"},
	{"masyarakat", "masyrakat"},
	{"fasilitas", "fasiltas"},
	{"kesehatan", "kesehatn"},
	{"sekolah", "seklah"},
	{"penduduk", "pnduduk"},
	{"perangkat", "prangkat"},
	{"pemerintah", "pemrintah"},
	{"kunjungan", "kunjungn"},
	{"kelurahan", "kelurhan"},
	{"kecamatan", "kecamtan"},
	{"berdasarkan", "berdasrkan"},
}

var chatSlangReplacer = strings.NewReplacer(
	"Tahun ", "thn ",
	"tahun ", "thn ",
	"Yang ", "yg ",
	"yang ", "yg ",
	"Di mana ", "dmn ",
	"di mana ", "dmn ",
	"Dimana ", "dmn ",
	"dimana ", "dmn ",
	"Berapa ", "brp ",
	"berapa ", "brp ",
	"Apakah ", "klo ",
	"apakah ", "klo ",
	"Bagaimana ", "gmn ",
	"bagaimana ", "gmn ",
	"Untuk ", "utk ",
	"untuk ", "utk ",
	"Dengan ", "dgn ",
	"dengan ", "dgn ",
	"Atau ", "ato ",
	"atau ", "ato ",
)

func generateChaosLocal(ref string, rowNo int, seed int64) string {
	rng := seededRand(ref+"-chaos", rowNo, seed)
	s := strings.TrimSpace(strings.TrimSuffix(ref, "?"))
	s = reMultiSpace.ReplaceAllString(s, " ")
	s = chatSlangReplacer.Replace(s)

	// Lowercase normal words while preserving ALL-CAPS acronyms (PKL, KK, SPBE, SMP, SD, BKAD, UPT, BPJS)
	words := strings.Fields(s)
	for i, w := range words {
		if isAcronymOrNumber(w) {
			continue
		}
		words[i] = strings.ToLower(w)
	}
	s = strings.Join(words, " ")

	// Apply 1 realistic safe typo on common Indonesian noun/adjective without touching entities/numbers
	appliedTypo := false
	startIdx := rng.Intn(len(safeTypoPairs))
	for k := 0; k < len(safeTypoPairs); k++ {
		pair := safeTypoPairs[(startIdx+k)%len(safeTypoPairs)]
		if strings.Contains(s, pair[0]) {
			s = strings.Replace(s, pair[0], pair[1], 1)
			appliedTypo = true
			break
		}
	}
	if !appliedTypo && len(words) >= 3 {
		// Drop a middle vowel in a safe common filler word if >= 6 letters and no digits
		for i, w := range words {
			if len(w) >= 6 && !isAcronymOrNumber(w) && !isProtectedPlaceWord(w) {
				words[i] = dropOneInnerVowel(w)
				s = strings.Join(words, " ")
				break
			}
		}
	}

	wrappers := []string{
		"min mau nnya dong %s ada ga ya",
		"bang %s itu gmn ya infonya",
		"eh kak klo cari %s bs liat dmn ya",
		"%s ada datanya gak min lg butuh nih",
		"kak tlg cekin %s dong makasih",
	}
	return fmt.Sprintf(wrappers[rng.Intn(len(wrappers))], s)
}

func isAcronymOrNumber(w string) bool {
	clean := strings.Trim(w, ".,?!/()-")
	if len(clean) < 2 {
		return false
	}
	hasUpper := false
	for _, r := range clean {
		if unicode.IsDigit(r) {
			return true
		}
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsUpper(r) {
			hasUpper = true
		}
	}
	return hasUpper
}

func isProtectedPlaceWord(w string) bool {
	switch strings.ToLower(w) {
	case "medan", "denai", "tembung", "labuhan", "selatan", "sumatera", "utara", "hortikultura", "simpang", "chaidir", "kramat":
		return true
	default:
		return false
	}
}

func dropOneInnerVowel(w string) string {
	runes := []rune(w)
	for i := len(runes) - 2; i >= 2; i-- {
		switch runes[i] {
		case 'a', 'i', 'u', 'e', 'o':
			return string(append(runes[:i], runes[i+1:]...))
		}
	}
	return w
}

func seededRand(text string, rowNo int, extraSeed int64) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	s := int64(h.Sum64()) ^ int64(rowNo*7919) ^ extraSeed
	return rand.New(rand.NewSource(s))
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func preservesCriticalNumbers(ref, candidate string) bool {
	nums := reDigitsToken.FindAllString(ref, -1)
	if len(nums) == 0 {
		return true
	}
	candLower := strings.ToLower(candidate)
	for _, n := range nums {
		if !strings.Contains(candLower, n) {
			return false
		}
	}
	return true
}

func extractCoords(s string) []float64 {
	m := reKoordLine.FindStringSubmatch(s)
	if len(m) != 3 {
		return nil
	}
	lat, err1 := strconv.ParseFloat(m[1], 64)
	lng, err2 := strconv.ParseFloat(m[2], 64)
	if err1 != nil || err2 != nil {
		return nil
	}
	return []float64{lat, lng}
}

func extractDatasetSlug(s string) string {
	m := reDatasetSlug.FindStringSubmatch(s)
	if len(m) > 1 {
		return strings.ToLower(m[1])
	}
	return ""
}

func normalizeBodyForCompare(s string) string {
	s = strings.ReplaceAll(s, "\uFFFD", "-")
	s = reBerkasLine.ReplaceAllString(s, " ")
	s = reKoordLine.ReplaceAllString(s, " ")
	s = reBubblePrefix.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "*", " ")

	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.TrimSpace(reMultiSpace.ReplaceAllString(b.String(), " "))
}

func tokenDiceSimilarity(a, b string) float64 {
	if a == "" && b == "" {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	ta := uniqueTokens(a)
	tb := uniqueTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	for k := range ta {
		if tb[k] {
			inter++
		}
	}
	return (2.0 * float64(inter)) / float64(len(ta)+len(tb))
}

func uniqueTokens(s string) map[string]bool {
	m := map[string]bool{}
	for _, f := range strings.Fields(s) {
		if len(f) > 1 || (len(f) == 1 && f[0] >= '0' && f[0] <= '9') {
			m[f] = true
		}
	}
	return m
}
