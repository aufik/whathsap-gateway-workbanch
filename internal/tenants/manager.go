package tenants

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"whatshap-gateway/internal/database"
	"whatshap-gateway/internal/excel"
)

const (
	WorkbookFileName = "workbook.xlsx"
	DownloadsDirName = "downloads"
)

// Manager resolves the per-user Excel store. Every user owns an isolated
// directory (data/users/{id}) holding workbook.xlsx plus its metrics sidecar,
// so rows, media, and results never leak between accounts.
type Manager struct {
	db *database.Store

	mu    sync.Mutex
	cache map[int64]*excel.Store
	dirty map[int64]bool
}

func NewManager(db *database.Store) *Manager {
	return &Manager{db: db, cache: map[int64]*excel.Store{}, dirty: map[int64]bool{}}
}

func UserDir(userID int64) string {
	return filepath.Join("data", "users", fmt.Sprintf("%d", userID))
}

func (m *Manager) workbookPath(userID int64) string {
	return filepath.Join(UserDir(userID), WorkbookFileName)
}

// Load returns the store for a user, materialising it from PostgreSQL on first
// access. A user without a workbook gets an empty (read-only) store.
func (m *Manager) Load(ctx context.Context, userID int64) (*excel.Store, error) {
	if userID <= 0 {
		return nil, errors.New("user id tidak valid")
	}

	m.mu.Lock()
	if s, ok := m.cache[userID]; ok {
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()

	store, err := m.materialise(ctx, userID)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.cache[userID] = store
	m.mu.Unlock()
	return store, nil
}

func (m *Manager) materialise(ctx context.Context, userID int64) (*excel.Store, error) {
	dir := UserDir(userID)
	if err := os.MkdirAll(filepath.Join(dir, DownloadsDirName), 0755); err != nil {
		return nil, err
	}

	path := m.workbookPath(userID)
	if _, err := os.Stat(path); err != nil {
		if m.db == nil {
			return excel.NewEmptyStore(), nil
		}
		_, data, metrics, dbErr := m.db.LoadWorkbook(ctx, userID)
		if dbErr != nil {
			if errors.Is(dbErr, sql.ErrNoRows) {
				return excel.NewEmptyStore(), nil
			}
			return nil, dbErr
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			return nil, err
		}
		if len(metrics) > 0 {
			if err := os.WriteFile(excel.MetricsPathFor(path), metrics, 0644); err != nil {
				return nil, err
			}
		}
	}

	return excel.NewStore(path)
}

// Store replaces a user's workbook (upload path) and refreshes the cache.
func (m *Manager) Store(ctx context.Context, userID int64, uploaded []byte, fileName string) (*excel.Store, error) {
	dir := UserDir(userID)
	if err := os.MkdirAll(filepath.Join(dir, DownloadsDirName), 0755); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	path := m.workbookPath(userID)
	if err := os.WriteFile(path+".upload", uploaded, 0644); err != nil {
		return nil, err
	}
	if err := os.Rename(path+".upload", path); err != nil {
		return nil, err
	}
	// Drop stale sidecar so the uploaded file's own data wins.
	_ = os.Remove(excel.MetricsPathFor(path))

	store, err := excel.NewStore(path)
	if err != nil {
		return nil, err
	}
	if m.db != nil {
		metrics, _ := os.ReadFile(excel.MetricsPathFor(path))
		if err := m.db.SaveWorkbook(ctx, userID, fileName, uploaded, metrics); err != nil {
			return nil, err
		}
	}
	m.cache[userID] = store
	return store, nil
}

// Persist pushes the on-disk workbook and metrics back to PostgreSQL so the
// user keeps their results across restarts.
func (m *Manager) Persist(ctx context.Context, userID int64) error {
	if m.db == nil {
		return nil
	}
	m.mu.Lock()
	store, ok := m.cache[userID]
	m.mu.Unlock()
	if !ok || store == nil {
		return nil
	}

	data, fileName, err := store.ExportBytes()
	if err != nil {
		return err
	}
	metrics, _ := os.ReadFile(excel.MetricsPathFor(m.workbookPath(userID)))
	return m.db.SaveWorkbook(ctx, userID, fileName, data, metrics)
}

// MediaDir returns the per-user attachment directory.
func (m *Manager) MediaDir(userID int64) string {
	return filepath.Join(UserDir(userID), DownloadsDirName)
}

// Invalidate drops a cached store so the next Load re-reads from disk.
func (m *Manager) Invalidate(userID int64) {
	m.mu.Lock()
	delete(m.cache, userID)
	m.mu.Unlock()
}

// PersistAll flushes every cached workbook back to PostgreSQL. Used after
// PersistAll flushes every cached workbook back to PostgreSQL. Used after
// batch results so a restart does not lose work.
func (m *Manager) PersistAll(ctx context.Context) error {
	m.mu.Lock()
	ids := make([]int64, 0, len(m.cache))
	for id := range m.cache {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	var firstErr error
	for _, id := range ids {
		if err := m.Persist(ctx, id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// MarkDirty records that a user's workbook changed and needs a flush.
func (m *Manager) MarkDirty(userID int64) {
	m.mu.Lock()
	if m.dirty == nil {
		m.dirty = map[int64]bool{}
	}
	m.dirty[userID] = true
	m.mu.Unlock()
}

// FlushDirty persists only workbooks whose results changed since the last
// flush, so periodic syncs stay cheap on large uploads.
func (m *Manager) FlushDirty(ctx context.Context) error {
	m.mu.Lock()
	pending := m.dirty
	m.dirty = map[int64]bool{}
	m.mu.Unlock()

	var firstErr error
	for id := range pending {
		if err := m.Persist(ctx, id); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			m.mu.Lock()
			m.dirty[id] = true
			m.mu.Unlock()
		}
	}
	return firstErr
}
