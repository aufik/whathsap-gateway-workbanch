package livechat

import (
	"context"
	"errors"
	"fmt"
	"sync"

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

// runHandle groups the engine with its cancel func and completion channel.
type runHandle struct {
	sessionID int64
	engine    *Engine
	cancel    context.CancelFunc
	done      chan struct{}
}

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

// StartSession creates the DB row and launches one goroutine. It returns the
// created Session so the HTTP handler can immediately serialise it back.
func (m *Manager) StartSession(ctx context.Context, userID int64, in SessionInput) (Session, error) {
	m.mu.Lock()
	if _, busy := m.active[userID]; busy {
		m.mu.Unlock()
		return Session{}, errors.New("masih ada sesi live chat aktif untuk akun ini; hentikan dulu")
	}
	m.mu.Unlock()

	// Attach the device client first so a missing/unconnected device fails
	// before we create a session row.
	client, err := m.wa.ClientFor(userID, in.DeviceUUID)
	if err != nil {
		return Session{}, err
	}

	sess, err := m.store.CreateSession(ctx, userID, in)
	if err != nil {
		return Session{}, err
	}

	// Pull the user's human-rhythm settings; fall back to defaults when absent.
	hs := defaultHumanSettings()
	if v, ok := m.settings(ctx, userID); ok {
		hs = v
	}

	engine, err := newEngine(engineConfig{
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
	handle := &runHandle{sessionID: sess.ID, engine: engine, cancel: cancel, done: make(chan struct{})}
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
		_ = engine.Run(runCtx)
	}()
	return sess, nil
}

// Stop cancels the running session for userID (if it matches sessionID). It
// returns false when there is nothing to stop.
func (m *Manager) Stop(userID, sessionID int64) bool {
	m.mu.Lock()
	handle, ok := m.active[userID]
	if !ok || (sessionID > 0 && handle.sessionID != sessionID) {
		m.mu.Unlock()
		return false
	}
	m.mu.Unlock()
	handle.cancel()
	<-handle.done
	return true
}

// ActiveSessionID returns the running session id for userID, or 0.
func (m *Manager) ActiveSessionID(userID int64) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.active[userID]; ok {
		return h.sessionID
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
