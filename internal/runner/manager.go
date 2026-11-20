package runner

import (
	"sync"

	"whatshap-gateway/internal/ai"
	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/wa"
)

type TenantEventSink func(userID int64, eventType string, payload any)
type TenantRowSavedHook func(userID int64)

// Manager manages independent Runner instances per tenant.
// Each tenant gets their own dedicated state machine, event sink, and row-saved
// hooks, completely eliminating race conditions, cross-tenant data leaks,
// or one tenant's batch blocking another tenant.
type Manager struct {
	mu         sync.Mutex
	cfg        *config.AppConfig
	aiEngine   *ai.Engine
	emit       TenantEventSink
	onRowSaved TenantRowSavedHook
	runners    map[int64]*Runner
}

func NewManager(
	cfg *config.AppConfig,
	aiEngine *ai.Engine,
	emit TenantEventSink,
	onRowSaved TenantRowSavedHook,
) *Manager {
	return &Manager{
		cfg:        cfg,
		aiEngine:   aiEngine,
		emit:       emit,
		onRowSaved: onRowSaved,
		runners:    make(map[int64]*Runner),
	}
}

// ForUser returns the dedicated Runner for userID, instantiating it lazily
// with its own scoped event sink and workbook dirty-callback.
// If the store, pool, or settings have changed, they are refreshed on the runner.
func (m *Manager) ForUser(userID int64, store *excel.Store, pool *wa.Pool, settings model.HumanSettings) *Runner {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, exists := m.runners[userID]
	if !exists {
		uID := userID
		r = NewRunner(m.cfg, store, m.aiEngine, nil, func(eventType string, payload any) {
			if m.emit != nil {
				m.emit(uID, eventType, payload)
			}
		})
		r.OnRowSaved(func() {
			if m.onRowSaved != nil {
				m.onRowSaved(uID)
			}
		})
		m.runners[userID] = r
	}

	if store != nil {
		r.SetStore(store)
	}
	if pool != nil {
		r.SetPool(pool)
	}
	r.SetSettings(userID, settings)

	return r
}

// Get returns the runner for userID if one was already instantiated.
func (m *Manager) Get(userID int64) (*Runner, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runners[userID]
	return r, ok
}

// ActiveCount returns how many tenants currently have a runner in "running" state.
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	active := 0
	for _, r := range m.runners {
		if r.Status().State == "running" {
			active++
		}
	}
	return active
}

// StopAll stops every tenant runner cleanly during graceful server shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	all := make([]*Runner, 0, len(m.runners))
	for _, r := range m.runners {
		all = append(all, r)
	}
	m.mu.Unlock()

	for _, r := range all {
		_ = r.Stop()
	}
}
