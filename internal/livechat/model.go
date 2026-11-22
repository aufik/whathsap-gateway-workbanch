// Package livechat implements the automated conversation emulator that files
// a complaint (pengaduan) against the target WhatsApp bot. All persistent state
// lives in the dedicated `livechat` schema; queries always qualify tables with
// `livechat.` and filter by the session's user_id.
package livechat

import "time"

// Categories describe how faithfully the emulator answers the bot.
type CategoryMode string

const (
	ModeBenar  CategoryMode = "benar"
	ModeTypo   CategoryMode = "typo"
	ModeNgasal CategoryMode = "ngasal"
	ModeCelah  CategoryMode = "celah"
)

// Status is the session lifecycle state.
type Status string

const (
	StatusRunning  Status = "running"
	StatusFinished Status = "finished"
	StatusFailed   Status = "failed"
	StatusStopped  Status = "stopped"
	StatusTimeout  Status = "timeout"
)

// TitikLokasiSource records whether the location was auto-filled, edited, or
// cleared by the tester, so QC can audit it.
type TitikLokasiSource string

const (
	SourceRandom TitikLokasiSource = "random"
	SourceManual TitikLokasiSource = "manual"
	SourceEmpty  TitikLokasiSource = "empty"
)

// Direction of a chat bubble.
type Direction string

const (
	DirectionSent     Direction = "sent"
	DirectionReceived Direction = "received"
)

// MsgType is the shape of a bubble.
type MsgType string

const (
	MsgText     MsgType = "text"
	MsgImage    MsgType = "image"
	MsgDocument MsgType = "document"
	MsgLocation MsgType = "location"
)

// AssetKind is the accepted upload kind.
type AssetKind string

const (
	AssetImage    AssetKind = "image"
	AssetDocument AssetKind = "document"
)

// Session is the header for one complaint run.
type Session struct {
	ID              int64             `json:"id"`
	UserID          int64             `json:"userId"`
	DeviceUUID      string            `json:"deviceUuid"`
	WAPhone         string            `json:"waPhone"`
	BotPhone        string            `json:"botPhone"`
	Title           string            `json:"title"`
	ReportText      string            `json:"reportText"`
	NamaPengadu     string            `json:"namaPengadu"`
	TanggalKejadian string            `json:"tanggalKejadian"` // YYYY-MM-DD
	AlamatKejadian  string            `json:"alamatKejadian"`
	TitikLokasi     string            `json:"titikLokasi"`
	TitikLokasiSrc  TitikLokasiSource `json:"titikLokasiSource"`
	AssetIDs        []int64           `json:"assetIds"`
	MaxTurns        int               `json:"maxTurns"`
	CategoryMode    CategoryMode      `json:"categoryMode"`
	Status          Status            `json:"status"`
	EndReason       string            `json:"endReason"`
	LastChatExcerpt string            `json:"lastChatExcerpt"`
	TicketNo        string            `json:"ticketNo"`
	TotalTurns      int               `json:"totalTurns"`
	RNGSeed         int64             `json:"rngSeed"`
	CreatedAt       time.Time         `json:"createdAt"`
	StartedAt       *time.Time        `json:"startedAt,omitempty"`
	EndedAt         *time.Time        `json:"endedAt,omitempty"`
}

// Message is one chat bubble (either sent by us or received from the bot).
type Message struct {
	ID        int64     `json:"id"`
	SessionID int64     `json:"sessionId"`
	TurnNo    int       `json:"turnNo"`
	Direction Direction `json:"direction"`
	Body      string    `json:"body"`
	MediaJSON string    `json:"mediaJson,omitempty"`
	MsgType   MsgType   `json:"msgType"`
	SentAt    time.Time `json:"sentAt"`
	LatencyMs *int      `json:"latencyMs,omitempty"`
}

// Turn is the QC audit row for one bot question + our answer.
type Turn struct {
	ID            int64     `json:"id"`
	SessionID     int64     `json:"sessionId"`
	TurnNo        int       `json:"turnNo"`
	BotQuestion   string    `json:"botQuestion"`
	QuestionKind  string    `json:"questionKind"`
	OurAnswer     string    `json:"ourAnswer"`
	SentMediaJSON string    `json:"sentMediaJson,omitempty"`
	CategoryMode  string    `json:"categoryMode"`
	MatchStatus   string    `json:"matchStatus"`
	Note          string    `json:"note"`
	CreatedAt     time.Time `json:"createdAt"`
}

// Asset is a file the tester uploaded once and may send to the bot repeatedly.
type Asset struct {
	ID         int64     `json:"id"`
	UserID     int64     `json:"userId"`
	Kind       AssetKind `json:"kind"`
	Label      string    `json:"label"`
	FileName   string    `json:"fileName"`
	StoredPath string    `json:"storedPath"`
	Mimetype   string    `json:"mimetype"`
	SizeBytes  int64     `json:"sizeBytes"`
	SHA256     string    `json:"sha256"`
	CreatedAt  time.Time `json:"createdAt"`
}

// SessionDetail bundles a session with its chat history and audit log, used by
// the detail endpoint and the summary exporter.
type SessionDetail struct {
	Session  Session   `json:"session"`
	Messages []Message `json:"messages"`
	Turns    []Turn    `json:"turns"`
}
