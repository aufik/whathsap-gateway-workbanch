package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"whatshap-gateway/internal/model"
)

type AppConfig struct {
	mu          sync.RWMutex
	EnvPath     string
	Host        string
	Port        int
	ExcelFile   string
	WASessionDB string

	LLMBaseURL   string
	LLMAPIKey    string
	LLMModel     string
	LLMTimeoutMs int

	Settings model.HumanSettings
}

func Load(envPath string) (*AppConfig, error) {
	fileVals := map[string]string{}
	if envPath != "" {
		if f, err := os.Open(envPath); err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				line = strings.TrimPrefix(line, "export ")
				idx := strings.IndexByte(line, '=')
				if idx <= 0 {
					continue
				}
				k := strings.TrimSpace(line[:idx])
				v := strings.TrimSpace(line[idx+1:])
				v = strings.Trim(v, `"'`)
				fileVals[k] = v
			}
		}
	}

	getStr := func(key, def string) string {
		if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		if v, ok := fileVals[key]; ok && v != "" {
			return v
		}
		return def
	}

	getInt := func(key string, def int) int {
		raw := getStr(key, "")
		if raw == "" {
			return def
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return def
		}
		return n
	}

	cfg := &AppConfig{
		EnvPath:      envPath,
		Host:         getStr("HOST", "0.0.0.0"),
		Port:         getInt("PORT", 3020),
		ExcelFile:    getStr("EXCEL_FILE", "Uji-Bank-Pertanyaan-Programmer-4.xlsx"),
		WASessionDB:  getStr("WA_SESSION_DB", "wa_session.db"),
		LLMBaseURL:   strings.TrimRight(getStr("LLM_BASE_URL", ""), "/"),
		LLMAPIKey:    getStr("LLM_API_KEY", ""),
		LLMModel:     getStr("LLM_MODEL", ""),
		LLMTimeoutMs: getInt("LLM_TIMEOUT_MS", 20000),
		Settings: model.HumanSettings{
			BotPhone:            NormalizePhone(getStr("BOT_PHONE", "")),
			QuestionMode:        normalizeMode(getStr("DEFAULT_QUESTION_MODE", "natural")),
			PreThinkMinMs:       getInt("PRE_THINK_MIN_MS", 2000),
			PreThinkMaxMs:       getInt("PRE_THINK_MAX_MS", 5000),
			TypingMsPerChar:     getInt("TYPING_MS_PER_CHAR", 105),
			TypingMinMs:         getInt("TYPING_MIN_MS", 4500),
			TypingMaxMs:         getInt("TYPING_MAX_MS", 20000),
			UnreadDelayMinMs:    getInt("UNREAD_DELAY_MIN_MS", 2500),
			UnreadDelayMaxMs:    getInt("UNREAD_DELAY_MAX_MS", 8000),
			ReadDelayMinMs:      getInt("READ_DELAY_MIN_MS", 4500),
			ReadDelayMaxMs:      getInt("READ_DELAY_MAX_MS", 13000),
			DelayMinMs:          getInt("DELAY_MIN_MS", 15000),
			DelayMaxMs:          getInt("DELAY_MAX_MS", 30000),
			DistractionSpikePct: getInt("DISTRACTION_SPIKE_PCT", 22),
			DistractionMinMs:    getInt("DISTRACTION_MIN_MS", 32000),
			DistractionMaxMs:    getInt("DISTRACTION_MAX_MS", 60000),
			CooldownEveryMin:    getInt("COOLDOWN_EVERY_MIN", 12),
			CooldownEveryMax:    getInt("COOLDOWN_EVERY_MAX", 18),
			CooldownMinMs:       getInt("COOLDOWN_MIN_MS", 90000),
			CooldownMaxMs:       getInt("COOLDOWN_MAX_MS", 165000),
			LongRestEveryMin:    getInt("LONG_REST_EVERY_MIN", 40),
			LongRestEveryMax:    getInt("LONG_REST_EVERY_MAX", 50),
			LongRestMinMs:       getInt("LONG_REST_MIN_MS", 240000),
			LongRestMaxMs:       getInt("LONG_REST_MAX_MS", 420000),
			QuietWindowMs:       getInt("QUIET_WINDOW_MS", 5500),
			ReplyTimeoutMs:      getInt("REPLY_TIMEOUT_MS", 35000),
		},
	}
	cfg.Settings = sanitizeSettings(cfg.Settings)
	return cfg, nil
}

func (c *AppConfig) ReloadFromDisk() error {
	fresh, err := Load(c.EnvPath)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Host = fresh.Host
	c.Port = fresh.Port
	c.ExcelFile = fresh.ExcelFile
	c.WASessionDB = fresh.WASessionDB
	c.LLMBaseURL = fresh.LLMBaseURL
	c.LLMAPIKey = fresh.LLMAPIKey
	c.LLMModel = fresh.LLMModel
	c.LLMTimeoutMs = fresh.LLMTimeoutMs
	c.Settings = fresh.Settings
	return nil
}

func (c *AppConfig) GetSettings() model.HumanSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Settings
}

func (c *AppConfig) UpdateSettings(next model.HumanSettings) (model.HumanSettings, error) {
	c.mu.Lock()
	c.Settings = sanitizeSettings(next)
	saved := c.Settings
	envPath := c.EnvPath
	c.mu.Unlock()

	if envPath != "" {
		_ = persistEnvSettings(envPath, saved)
	}
	return saved, nil
}

func (c *AppConfig) SetExcelFile(path string) {
	c.mu.Lock()
	c.ExcelFile = path
	c.mu.Unlock()
}

func (c *AppConfig) GetExcelFile() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ExcelFile
}

func (c *AppConfig) AIStatus() model.AIStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.LLMBaseURL != "" && c.LLMModel != "" {
		return model.AIStatus{
			Enabled:  true,
			Provider: c.LLMBaseURL,
			Model:    c.LLMModel,
		}
	}
	return model.AIStatus{
		Enabled:  false,
		Provider: "Smart Local Heuristic Engine (Gratis / Offline)",
		Model:    "local-id-qa-v2",
	}
}

func NormalizePhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if strings.HasPrefix(digits, "08") {
		digits = "62" + digits[1:]
	} else if strings.HasPrefix(digits, "8") && len(digits) >= 9 {
		digits = "62" + digits
	}
	return digits
}

func normalizeMode(raw string) model.QuestionMode {
	switch model.QuestionMode(strings.ToLower(strings.TrimSpace(raw))) {
	case model.ModeExact:
		return model.ModeExact
	case model.ModeChaos:
		return model.ModeChaos
	case model.ModeManual:
		return model.ModeManual
	default:
		return model.ModeNatural
	}
}

func clampRange(minVal, maxVal, floorMin, ceilMax int) (int, int) {
	if minVal < floorMin {
		minVal = floorMin
	}
	if minVal > ceilMax {
		minVal = ceilMax
	}
	if maxVal < minVal {
		maxVal = minVal
	}
	if maxVal > ceilMax {
		maxVal = ceilMax
	}
	return minVal, maxVal
}

func sanitizeSettings(s model.HumanSettings) model.HumanSettings {
	s.BotPhone = NormalizePhone(s.BotPhone)
	s.QuestionMode = normalizeMode(string(s.QuestionMode))

	if s.PreThinkMinMs == 0 && s.PreThinkMaxMs == 0 {
		s.PreThinkMinMs, s.PreThinkMaxMs = 2000, 5000
	}
	s.PreThinkMinMs, s.PreThinkMaxMs = clampRange(s.PreThinkMinMs, s.PreThinkMaxMs, 500, 30000)

	if s.TypingMsPerChar < 20 {
		s.TypingMsPerChar = 20
	}
	if s.TypingMsPerChar > 350 {
		s.TypingMsPerChar = 350
	}
	s.TypingMinMs, s.TypingMaxMs = clampRange(s.TypingMinMs, s.TypingMaxMs, 1000, 60000)

	if s.UnreadDelayMinMs == 0 && s.UnreadDelayMaxMs == 0 {
		s.UnreadDelayMinMs, s.UnreadDelayMaxMs = 2500, 8000
	}
	s.UnreadDelayMinMs, s.UnreadDelayMaxMs = clampRange(s.UnreadDelayMinMs, s.UnreadDelayMaxMs, 500, 60000)

	s.ReadDelayMinMs, s.ReadDelayMaxMs = clampRange(s.ReadDelayMinMs, s.ReadDelayMaxMs, 1000, 90000)
	s.DelayMinMs, s.DelayMaxMs = clampRange(s.DelayMinMs, s.DelayMaxMs, 3000, 300000)

	if s.DistractionSpikePct < 0 {
		s.DistractionSpikePct = 0
	}
	if s.DistractionSpikePct > 80 {
		s.DistractionSpikePct = 80
	}
	if s.DistractionMinMs == 0 && s.DistractionMaxMs == 0 {
		s.DistractionMinMs, s.DistractionMaxMs = 32000, 60000
	}
	s.DistractionMinMs, s.DistractionMaxMs = clampRange(s.DistractionMinMs, s.DistractionMaxMs, s.DelayMinMs, 360000)

	s.CooldownEveryMin, s.CooldownEveryMax = clampRange(s.CooldownEveryMin, s.CooldownEveryMax, 3, 250)
	s.CooldownMinMs, s.CooldownMaxMs = clampRange(s.CooldownMinMs, s.CooldownMaxMs, 10000, 900000)

	if s.LongRestEveryMin == 0 && s.LongRestEveryMax == 0 {
		s.LongRestEveryMin, s.LongRestEveryMax = 40, 50
	}
	s.LongRestEveryMin, s.LongRestEveryMax = clampRange(s.LongRestEveryMin, s.LongRestEveryMax, 15, 500)
	if s.LongRestMinMs == 0 && s.LongRestMaxMs == 0 {
		s.LongRestMinMs, s.LongRestMaxMs = 240000, 420000
	}
	s.LongRestMinMs, s.LongRestMaxMs = clampRange(s.LongRestMinMs, s.LongRestMaxMs, 30000, 1800000)

	if s.QuietWindowMs < 2000 {
		s.QuietWindowMs = 2000
	}
	if s.QuietWindowMs > 30000 {
		s.QuietWindowMs = 30000
	}
	if s.ReplyTimeoutMs < s.QuietWindowMs+4000 {
		s.ReplyTimeoutMs = s.QuietWindowMs + 4000
	}
	if s.ReplyTimeoutMs > 240000 {
		s.ReplyTimeoutMs = 240000
	}
	return s
}

func persistEnvSettings(envPath string, s model.HumanSettings) error {
	updates := map[string]string{
		"BOT_PHONE":             s.BotPhone,
		"DEFAULT_QUESTION_MODE": string(s.QuestionMode),
		"PRE_THINK_MIN_MS":      strconv.Itoa(s.PreThinkMinMs),
		"PRE_THINK_MAX_MS":      strconv.Itoa(s.PreThinkMaxMs),
		"TYPING_MS_PER_CHAR":    strconv.Itoa(s.TypingMsPerChar),
		"TYPING_MIN_MS":         strconv.Itoa(s.TypingMinMs),
		"TYPING_MAX_MS":         strconv.Itoa(s.TypingMaxMs),
		"UNREAD_DELAY_MIN_MS":   strconv.Itoa(s.UnreadDelayMinMs),
		"UNREAD_DELAY_MAX_MS":   strconv.Itoa(s.UnreadDelayMaxMs),
		"READ_DELAY_MIN_MS":     strconv.Itoa(s.ReadDelayMinMs),
		"READ_DELAY_MAX_MS":     strconv.Itoa(s.ReadDelayMaxMs),
		"DELAY_MIN_MS":          strconv.Itoa(s.DelayMinMs),
		"DELAY_MAX_MS":          strconv.Itoa(s.DelayMaxMs),
		"DISTRACTION_SPIKE_PCT": strconv.Itoa(s.DistractionSpikePct),
		"DISTRACTION_MIN_MS":    strconv.Itoa(s.DistractionMinMs),
		"DISTRACTION_MAX_MS":    strconv.Itoa(s.DistractionMaxMs),
		"COOLDOWN_EVERY_MIN":    strconv.Itoa(s.CooldownEveryMin),
		"COOLDOWN_EVERY_MAX":    strconv.Itoa(s.CooldownEveryMax),
		"COOLDOWN_MIN_MS":       strconv.Itoa(s.CooldownMinMs),
		"COOLDOWN_MAX_MS":       strconv.Itoa(s.CooldownMaxMs),
		"LONG_REST_EVERY_MIN":   strconv.Itoa(s.LongRestEveryMin),
		"LONG_REST_EVERY_MAX":   strconv.Itoa(s.LongRestEveryMax),
		"LONG_REST_MIN_MS":      strconv.Itoa(s.LongRestMinMs),
		"LONG_REST_MAX_MS":      strconv.Itoa(s.LongRestMaxMs),
		"QUIET_WINDOW_MS":       strconv.Itoa(s.QuietWindowMs),
		"REPLY_TIMEOUT_MS":      strconv.Itoa(s.ReplyTimeoutMs),
	}

	rawBytes, err := os.ReadFile(envPath)
	var lines []string
	if err == nil {
		lines = strings.Split(string(rawBytes), "\n")
	}

	seen := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		idx := strings.IndexByte(trimmed, '=')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(strings.TrimPrefix(trimmed[:idx], "export "))
		if val, ok := updates[key]; ok {
			lines[i] = fmt.Sprintf("%s=%s", key, val)
			seen[key] = true
		}
	}

	for k, v := range updates {
		if !seen[k] {
			lines = append(lines, fmt.Sprintf("%s=%s", k, v))
		}
	}

	tmpPath := envPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, envPath)
}
