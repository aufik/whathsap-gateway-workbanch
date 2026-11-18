package model

type QuestionMode string

const (
	ModeExact   QuestionMode = "exact"
	ModeNatural QuestionMode = "natural"
	ModeChaos   QuestionMode = "chaos"
	ModeManual  QuestionMode = "manual"
)

type TestRow struct {
	No               int    `json:"no"`
	ExcelRow         int    `json:"excelRow"`
	OPD              string `json:"opd"`
	Kategori         string `json:"kategori"`
	StatusBank       string `json:"statusBank"`
	QuestionRef      string `json:"questionRef"`
	QuestionInput    string `json:"questionInput"`
	ExpectedReply    string `json:"expectedReply"`
	ActualReply      string `json:"actualReply"`
	Notes            string `json:"notes"`
	ResultStatus     string `json:"resultStatus"`
	HasExpectedFile  bool   `json:"hasExpectedFile"`
	HasExpectedCoord bool   `json:"hasExpectedCoord"`
	HasActualFile    bool   `json:"hasActualFile"`
	HasActualCoord   bool   `json:"hasActualCoord"`
}

type RowPatch struct {
	QuestionInput *string `json:"questionInput,omitempty"`
	ActualReply   *string `json:"actualReply,omitempty"`
	Notes         *string `json:"notes,omitempty"`
	ResultStatus  *string `json:"resultStatus,omitempty"`
}

type OPDSummary struct {
	OPD         string  `json:"opd"`
	Total       int     `json:"total"`
	Cocok       int     `json:"cocok"`
	Tidak       int     `json:"tidak"`
	BelumDiuji  int     `json:"belumDiuji"`
	AccuracyPct float64 `json:"accuracyPct"`
}

type OverallSummary struct {
	FileName    string       `json:"fileName"`
	Total       int          `json:"total"`
	Approved    int          `json:"approved"`
	Waiting     int          `json:"waiting"`
	Cocok       int          `json:"cocok"`
	Tidak       int          `json:"tidak"`
	BelumDiuji  int          `json:"belumDiuji"`
	Tested      int          `json:"tested"`
	ProgressPct float64      `json:"progressPct"`
	AccuracyPct float64      `json:"accuracyPct"`
	ByOPD       []OPDSummary `json:"byOpd"`
	Categories  []string     `json:"categories"`
}

type HumanSettings struct {
	BotPhone         string       `json:"botPhone"`
	QuestionMode     QuestionMode `json:"questionMode"`
	ReadDelayMinMs   int          `json:"readDelayMinMs"`
	ReadDelayMaxMs   int          `json:"readDelayMaxMs"`
	TypingMsPerChar  int          `json:"typingMsPerChar"`
	TypingMinMs      int          `json:"typingMinMs"`
	TypingMaxMs      int          `json:"typingMaxMs"`
	DelayMinMs       int          `json:"delayMinMs"`
	DelayMaxMs       int          `json:"delayMaxMs"`
	CooldownEveryMin int          `json:"cooldownEveryMin"`
	CooldownEveryMax int          `json:"cooldownEveryMax"`
	CooldownMinMs    int          `json:"cooldownMinMs"`
	CooldownMaxMs    int          `json:"cooldownMaxMs"`
	QuietWindowMs    int          `json:"quietWindowMs"`
	ReplyTimeoutMs   int          `json:"replyTimeoutMs"`
}

type BehaviorLog struct {
	Timestamp int64  `json:"timestamp"`
	Phase     string `json:"phase"`
	RowNo     int    `json:"rowNo,omitempty"`
	Detail    string `json:"detail"`
}

type HumanTelemetry struct {
	Phase                 string        `json:"phase"`
	PhaseLabel            string        `json:"phaseLabel"`
	Presence              string        `json:"presence"`
	PhaseStartedAt        int64         `json:"phaseStartedAt"`
	PhaseEndsAt           int64         `json:"phaseEndsAt"`
	PhaseDurationMs       int           `json:"phaseDurationMs"`
	ActiveRowNo           int           `json:"activeRowNo"`
	SinceLastBreak        int           `json:"sinceLastBreak"`
	NextBreakTarget       int           `json:"nextBreakTarget"`
	TotalBreaksTaken      int           `json:"totalBreaksTaken"`
	LastTypingChars       int           `json:"lastTypingChars"`
	LastTypingDurationMs  int           `json:"lastTypingDurationMs"`
	LastReadDurationMs    int           `json:"lastReadDurationMs"`
	LastInterDelayMs      int           `json:"lastInterDelayMs"`
	LastCooldownMs        int           `json:"lastCooldownMs"`
	CircuitBreakerTripped bool          `json:"circuitBreakerTripped"`
	CircuitBreakerReason  string        `json:"circuitBreakerReason"`
	RecentLogs            []BehaviorLog `json:"recentLogs"`
}

type WAStatus struct {
	Connected   bool   `json:"connected"`
	LoggedIn    bool   `json:"loggedIn"`
	AccountJID  string `json:"accountJid"`
	PushName    string `json:"pushName"`
	QRDataURL   string `json:"qrDataUrl"`
	PairingCode string `json:"pairingCode"`
	LastError   string `json:"lastError"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type RunnerStatus struct {
	State          string `json:"state"`
	Mode           string `json:"mode"`
	CurrentRowNo   int    `json:"currentRowNo"`
	ProcessedInRun int    `json:"processedInRun"`
	TargetInRun    int    `json:"targetInRun"`
	StartedAt      int64  `json:"startedAt"`
	LastError      string `json:"lastError"`
}

type AIStatus struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type AppStateSnapshot struct {
	Settings  HumanSettings  `json:"settings"`
	AI        AIStatus       `json:"ai"`
	WA        WAStatus       `json:"wa"`
	Runner    RunnerStatus   `json:"runner"`
	Telemetry HumanTelemetry `json:"telemetry"`
	Summary   OverallSummary `json:"summary"`
	Rows      []TestRow      `json:"rows"`
}

type SSEEvent struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	Payload   any    `json:"payload"`
}
