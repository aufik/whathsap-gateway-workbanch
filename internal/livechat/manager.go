package livechat

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"whatshap-gateway/internal/model"
)

// Manager owns one running Engine per (userID, sessionID). It is safe to call
// from HTTP handlers: Start is idempotent per user (one active session at a
// time), and Stop cancels the running worker without leaking goroutines.
type Manager struct {
	store     *Store
	assets    *AssetService
	responder *Responder
	wa        DeviceResolver
	settings  SettingsProvider
	mediaRoot func(userID int64) string
	assetRoot func(userID int64) string
	exporter  *Exporter
	emitter   engineEmitter

	mu     sync.Mutex
	active map[int64]*runHandle // userID -> current run
}

// SettingsProvider returns the persisted human-rhythm settings for a user.
type SettingsProvider func(ctx context.Context, userID int64) (settings model.HumanSettings, ok bool)

// ManagerOptions wires the manager. All fields are required except MediaRoot
// (defaults to <UserAssetMediaRoot>) and Emitter (defaults to no-op).
type ManagerOptions struct {
	Store     *Store
	Assets    *AssetService
	Responder *Responder
	WA        DeviceResolver
	Settings  SettingsProvider
	MediaRoot func(userID int64) string
	AssetRoot func(userID int64) string
	Exporter  *Exporter
	Emitter   engineEmitter
}

// NewManager validates options and returns a ready manager.
func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.Store == nil || opts.Assets == nil || opts.Responder == nil || opts.WA == nil || opts.Settings == nil {
		return nil, errors.New("manager livechat butuh store, assets, responder, wa, dan settings")
	}
	if opts.MediaRoot == nil {
		opts.MediaRoot = defaultMediaRoot
	}
	if opts.AssetRoot == nil {
		opts.AssetRoot = UserAssetsRoot
	}
	if opts.Emitter == nil {
		opts.Emitter = NopEmitter{}
	}
	return &Manager{
		store:     opts.Store,
		assets:    opts.Assets,
		responder: opts.Responder,
		wa:        opts.WA,
		settings:  opts.Settings,
		mediaRoot: opts.MediaRoot,
		assetRoot: opts.AssetRoot,
		exporter:  opts.Exporter,
		emitter:   opts.Emitter,
		active:    map[int64]*runHandle{},
	}, nil
}

// runHandle groups the engine with its cancel func, active session id and completion channel.
type runHandle struct {
	mu        sync.RWMutex
	sessionID int64
	engine    *Engine
	cancel    context.CancelFunc
	done      chan struct{}
}

func (h *runHandle) getSessionID() int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessionID
}

func (h *runHandle) setSession(sessID int64, eng *Engine) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessionID = sessID
	h.engine = eng
}

func randBetweenRange(min, max int) int {
	if max <= min {
		return min
	}
	return rand.Intn(max-min+1) + min
}

// StartSession creates the DB row and launches one goroutine. It returns the
// created Session so the HTTP handler can immediately serialise it back.
func (m *Manager) StartSession(ctx context.Context, userID int64, in SessionInput) (Session, error) {
	return m.StartBatch(ctx, userID, in, 1)
}

// StartBatch launches a sequential queue of N sessions (repeatCount >= 1).
// Session 1 starts immediately and is returned to caller. Subsequent sessions
// run after an inter-session human cooldown (15-30s) with freshly randomized
// Medan data matching the form template. Canceling via Stop halts the batch immediately.
func (m *Manager) StartBatch(ctx context.Context, userID int64, in SessionInput, repeatCount int) (Session, error) {
	if repeatCount <= 0 {
		repeatCount = 1
	}
	if repeatCount > 20 {
		repeatCount = 20
	}

	m.mu.Lock()
	if _, busy := m.active[userID]; busy {
		m.mu.Unlock()
		return Session{}, errors.New("masih ada sesi live chat aktif untuk akun ini; hentikan dulu")
	}
	m.mu.Unlock()

	client, err := m.wa.ClientFor(userID, in.DeviceUUID)
	if err != nil {
		return Session{}, err
	}

	hs := defaultHumanSettings()
	if v, ok := m.settings(ctx, userID); ok {
		hs = v
	}

	// Session 1 title suffix if repeatCount > 1
	baseTitle := in.Title
	if repeatCount > 1 {
		in.Title = fmt.Sprintf("%s (1/%d)", baseTitle, repeatCount)
	}

	sess, err := m.store.CreateSession(ctx, userID, in)
	if err != nil {
		return Session{}, err
	}

	eng1, err := newEngine(engineConfig{
		Store:     m.store,
		WA:        client,
		Responder: m.responder,
		Settings:  hs,
		MediaRoot: m.mediaRoot(userID),
		AssetRoot: m.assetRoot(userID),
		Emitter:   m.emitter,
		Exporter:  m.exporter,
	}, sess)
	if err != nil {
		return Session{}, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	handle := &runHandle{sessionID: sess.ID, engine: eng1, cancel: cancel, done: make(chan struct{})}

	m.mu.Lock()
	m.active[userID] = handle
	m.mu.Unlock()

	go func() {
		defer close(handle.done)
		defer func() {
			m.mu.Lock()
			if m.active[userID] == handle {
				delete(m.active, userID)
			}
			m.mu.Unlock()
			cancel()
		}()

		// Run session 1
		_ = eng1.Run(runCtx)

		// Sequential loop for remaining sessions in the batch
		for iter := 2; iter <= repeatCount; iter++ {
			select {
			case <-runCtx.Done():
				return // batch cancelled by user Stop
			default:
			}

			// Pre-check if client is still logged in before waiting or starting
			if client != nil && !client.Status().LoggedIn {
				if m.emitter != nil {
					m.emitter.Emit(handle.getSessionID(), "livechat_batch_info", map[string]any{
						"userId":  userID,
						"status":  "aborted",
						"message": "Perangkat WhatsApp terputus. Batch dihentikan demi keamanan.",
					})
				}
				return
			}

			// Inter-session human relaxation pause (15-30s anti-ban in production)
			cooldownMs := randBetweenRange(hs.DelayMinMs, hs.DelayMaxMs)
			if hs.DelayMaxMs >= 1000 && cooldownMs < 15000 {
				cooldownMs = 15000
			}
			if cooldownMs > 30000 {
				cooldownMs = 30000
			}

			if m.emitter != nil {
				m.emitter.Emit(handle.getSessionID(), "livechat_batch_info", map[string]any{
					"userId":     userID,
					"phase":      "cooldown",
					"current":    iter - 1,
					"next":       iter,
					"total":      repeatCount,
					"cooldownMs": cooldownMs,
					"message":    fmt.Sprintf("Sesi %d selesai. Jeda anti-ban (%ds) sebelum sesi %d dari %d...", iter-1, cooldownMs/1000, iter, repeatCount),
				})
			}

			sleepTimer := time.NewTimer(time.Duration(cooldownMs) * time.Millisecond)
			select {
			case <-runCtx.Done():
				sleepTimer.Stop()
				return
			case <-sleepTimer.C:
			}

			if client != nil && !client.Status().LoggedIn {
				if m.emitter != nil {
					m.emitter.Emit(handle.getSessionID(), "livechat_batch_info", map[string]any{
						"userId":  userID,
						"status":  "aborted",
						"message": "Perangkat WhatsApp terputus. Batch dihentikan demi keamanan.",
					})
				}
				return
			}

			// Fresh seed and random values for this iteration
			nextSeed := time.Now().UnixNano()
			nextRng := rand.New(rand.NewSource(nextSeed))
			preview := GenerateRandomPreview(nextRng, time.Now())

			nextIn := in
			nextIn.RNGSeed = nextSeed
			nextIn.Title = fmt.Sprintf("%s (%d/%d)", baseTitle, iter, repeatCount)
			if in.Title == "" || in.Title == sess.Title || strings.HasPrefix(in.Title, "Live Chat ") {
				nextIn.Title = fmt.Sprintf("%s (%d/%d)", preview.Title, iter, repeatCount)
			}
			if in.ReportText == "" || in.ReportText == sess.ReportText {
				nextIn.ReportText = preview.ReportText
			}
			// Only randomize fields that were empty in original input template
			if in.NamaPengadu == "" || in.NamaPengadu == sess.NamaPengadu {
				nextIn.NamaPengadu = preview.NamaPengadu
			}
			if in.TanggalKejadian == "" || in.TanggalKejadian == sess.TanggalKejadian {
				nextIn.TanggalKejadian = preview.TanggalKejadian
			}
			if in.AlamatKejadian == "" || in.AlamatKejadian == sess.AlamatKejadian {
				nextIn.AlamatKejadian = preview.AlamatKejadian
			}
			if in.TitikLokasi == "" || in.TitikLokasi == sess.TitikLokasi {
				nextIn.TitikLokasi = preview.TitikLokasi
				nextIn.TitikLokasiSrc = SourceRandom
			}

			nextSess, createErr := m.store.CreateSession(runCtx, userID, nextIn)
			if createErr != nil {
				return
			}

			nextEng, engErr := newEngine(engineConfig{
				Store:     m.store,
				WA:        client,
				Responder: m.responder,
				Settings:  hs,
				MediaRoot: m.mediaRoot(userID),
				AssetRoot: m.assetRoot(userID),
				Emitter:   m.emitter,
				Exporter:  m.exporter,
			}, nextSess)
			if engErr != nil {
				return
			}

			handle.setSession(nextSess.ID, nextEng)
			if m.emitter != nil {
				m.emitter.Emit(nextSess.ID, "livechat_session_created", map[string]any{
					"userId":  userID,
					"session": nextSess,
					"current": iter,
					"total":   repeatCount,
				})
			}
			_ = nextEng.Run(runCtx)
		}

		if repeatCount > 1 && m.emitter != nil {
			m.emitter.Emit(handle.getSessionID(), "livechat_batch_info", map[string]any{
				"userId":  userID,
				"phase":   "completed",
				"total":   repeatCount,
				"message": fmt.Sprintf("Seluruh batch (%d sesi) selesai.", repeatCount),
			})
		}
	}()
	return sess, nil
}

// Stop cancels the running session and any remaining queued batch sessions
// for userID. It returns false when there is nothing to stop.
func (m *Manager) Stop(userID, sessionID int64) bool {
	m.mu.Lock()
	handle, ok := m.active[userID]
	if !ok || (sessionID > 0 && handle.getSessionID() != sessionID) {
		m.mu.Unlock()
		return false
	}
	m.mu.Unlock()
	handle.cancel()
	<-handle.done
	return true
}

// ActiveSessionID returns the currently executing session id for userID, or 0.
func (m *Manager) ActiveSessionID(userID int64) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.active[userID]; ok {
		return h.getSessionID()
	}
	return 0
}

// Shutdown cancels every running engine and waits for all goroutines.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	handles := make([]*runHandle, 0, len(m.active))
	for _, h := range m.active {
		handles = append(handles, h)
	}
	m.mu.Unlock()
	for _, h := range handles {
		h.cancel()
	}
	for _, h := range handles {
		select {
		case <-h.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func defaultMediaRoot(userID int64) string {
	return UserMediaRoot(userID)
}

// defaultHumanSettings mirrors config defaults when a user has not saved any.
func defaultHumanSettings() model.HumanSettings {
	return model.HumanSettings{
		PreThinkMinMs:       2000,
		PreThinkMaxMs:       5000,
		TypingMsPerChar:     105,
		TypingMinMs:         4500,
		TypingMaxMs:         20000,
		DelayMinMs:          15000,
		DelayMaxMs:          30000,
		DistractionSpikePct: 22,
		DistractionMinMs:    32000,
		DistractionMaxMs:    60000,
		CooldownEveryMin:    12,
		CooldownEveryMax:    18,
		CooldownMinMs:       90000,
		CooldownMaxMs:       165000,
		LongRestEveryMin:    40,
		LongRestEveryMax:    50,
		LongRestMinMs:       240000,
		LongRestMaxMs:       420000,
		QuietWindowMs:       5500,
		ReplyTimeoutMs:      35000,
	}
}

// UserMediaRoot returns data/users/{id}/livechat_media per plan §6.5.
func UserMediaRoot(userID int64) string {
	return fmt.Sprintf("data/users/%d/livechat_media", userID)
}
