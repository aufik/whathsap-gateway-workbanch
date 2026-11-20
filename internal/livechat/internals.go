package livechat

import (
	"context"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"time"

	"whatshap-gateway/internal/model"
)

// The engine talks to WA through this interface so tests can drive the loop
// without a real WhatsApp connection. wa.Client satisfies it as-is.
type WAClient interface {
	SendText(ctx context.Context, phone, text string) error
	SendImage(ctx context.Context, phone string, data []byte, mimetype, caption string) error
	SendDocument(ctx context.Context, phone string, data []byte, fileName, mimetype, caption string) error
	SendLocation(ctx context.Context, phone string, lat, lng float64, name, address string) error
	RegisterCollector(botPhone string) (ReplyCollector, error)
	Status() model.WAStatus
}

// ReplyCollector is the subset of *wa.Collector the engine needs.
type ReplyCollector interface {
	Wait(ctx context.Context, quietWindow, hardTimeout time.Duration) ([]CollectedBubble, error)
	Close()
}

// CollectedBubble mirrors wa.CollectedBubble by field names + JSON tags; the
// engine consumes only these fields, so adapters in prod/tests stay trivial.
type CollectedBubble struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	MediaBytes []byte `json:"-"`
	MediaExt   string `json:"-"`
	MediaName  string `json:"-"`
}

// DeviceResolver maps a session's device_uuid to the caller's WA client.
type DeviceResolver interface {
	ClientFor(userID int64, deviceUUID string) (WAClient, error)
}

// SessionEmitter receives incremental events for the SSE/UI. A nil emitter is
// allowed (engine still runs to completion).
type SessionEmitter func(sessionID int64, eventType string, payload any)

// EngineEmitter is the public shape the engine uses to push live updates. The
// web layer adapts its SSEBroker; tests use a no-op.
type EngineEmitter interface {
	Emit(sessionID int64, eventType string, payload any)
}

// engineEmitter is an internal alias so ManagerOptions can stay package-local.
type engineEmitter = EngineEmitter

// NopEmitter is used when no broker is available (engine still runs).
type NopEmitter struct{}

func (NopEmitter) Emit(int64, string, any) {}

// humanizer bundles the existing human-rhythm helpers so livechat never
// duplicates them. Typing is the simplest reusable wrapper: pre-think delay ->
// multi-burst composing presence -> send. It reuses randBetween + the runner's
// typing duration formula so live chats look like manual typing, not bots.
type humanizer struct {
	settings model.HumanSettings
	rngMu    sync.Mutex
	rng      *rand.Rand
	sleeper  func(ctx context.Context, ms int) error
}

func newHumanizer(settings model.HumanSettings) *humanizer {
	return &humanizer{
		settings: settings,
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
		sleeper:  sleepCtx,
	}
}

// randBetween is a thread-safe local copy so livechat does not touch wa
// internals. Same distribution as wa's helper.
func (h *humanizer) randBetween(min, max int) int {
	if max <= min {
		return min
	}
	h.rngMu.Lock()
	v := h.rng.Intn(max-min+1) + min
	h.rngMu.Unlock()
	return v
}

// typingMs looks like model.HumanSettings computation in internal/runner:
// chars*TypingMsPerChar + jitter, clamped to [TypingMinMs, TypingMaxMs].
func (h *humanizer) typingMs(chars int) int {
	s := h.settings
	if s.TypingMsPerChar <= 0 {
		s.TypingMinMs, s.TypingMaxMs, s.TypingMsPerChar = 2000, 9000, 105
	}
	base := chars * s.TypingMsPerChar
	h.rngMu.Lock()
	jitter := 0.78 + h.rng.Float64()*0.56
	h.rngMu.Unlock()
	val := int(float64(base)*jitter) + h.randBetween(350, 1600)
	if val < s.TypingMinMs {
		val = s.TypingMinMs
	}
	if val > s.TypingMaxMs {
		val = s.TypingMaxMs
	}
	return val
}

// humanBursts mirrors wa.estimateBurstCount.
func humanBursts(totalMs int) int {
	switch {
	case totalMs <= 5500:
		return 1
	case totalMs <= 11500:
		return 2
	default:
		return 3
	}
}

// preThink models the "focus before typing" pause, so the bot sees a realistic
// delay between read and reply.
func (h *humanizer) preThink(ctx context.Context) error {
	s := h.settings
	ms := h.randBetween(s.PreThinkMinMs, s.PreThinkMaxMs)
	if ms <= 0 {
		return nil
	}
	return h.sleeper(ctx, ms)
}

// typing then sends text; the duration equals what the runner would spend on a
// row of the same length.
func (h *humanizer) typedSend(ctx context.Context, send func() error, text string) (int, error) {
	ms := h.typingMs(len([]rune(text)))
	if err := h.sleeper(ctx, ms); err != nil {
		return 0, err
	}
	if send != nil {
		if err := send(); err != nil {
			return ms, err
		}
	}
	return ms, nil
}

// interTurnDelay mirrors the runner's pauseBetweenQuestions cadence without
// touching the row telemetry. It keeps the same 8–16s delay + 22% distraction
// spikes, then yields a "cooldown" once per CooldownEveryX. The engine counts
// turns itself.
func (h *humanizer) interTurnDelay(ctx context.Context, turnsSinceBreak int, nextBreakAt int) (int, error) {
	s := h.settings
	if turnsSinceBreak >= nextBreakAt {
		ms := h.randBetween(s.CooldownMinMs, s.CooldownMaxMs)
		return ms, h.sleeper(ctx, ms)
	}
	ms := h.randBetween(s.DelayMinMs, s.DelayMaxMs)
	if s.DistractionSpikePct > 0 && h.randBetween(1, 100) <= s.DistractionSpikePct {
		ms = h.randBetween(s.DistractionMinMs, s.DistractionMaxMs)
	}
	return ms, h.sleeper(ctx, ms)
}

func (h *humanizer) nextBreakGap() int {
	return h.randBetween(h.settings.CooldownEveryMin, h.settings.CooldownEveryMax)
}

// sleepCtx is a package-local copy to avoid importing internal/wa from tests.
func sleepCtx(ctx context.Context, ms int) error {
	if ms <= 0 {
		return nil
	}
	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// openingLine is sent once at session start so the bot opens the complaint
// form. Kept neutral and short.
const openingLine = "halo min, saya mau buat pengaduan"

// safeFileBase returns a safe file name for a received media blob.
var unsafeFilenameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeFileBase(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "media"
	}
	name = unsafeFilenameRe.ReplaceAllString(name, "_")
	if strings.Contains(name, "..") {
		return "media"
	}
	return name
}
