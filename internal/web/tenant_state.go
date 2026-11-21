package web

import (
	"context"

	"whatshap-gateway/internal/runner"
	"whatshap-gateway/internal/wa"
)

// persistAfterWrite marks the tenant workbook dirty so the background flusher
// pushes its results to PostgreSQL without blocking the request.
func (s *Server) persistAfterWrite(userID int64) {
	if s.workbooks != nil && userID > 0 {
		s.workbooks.MarkDirty(userID)
	}
}

// tenantRunner returns the dedicated Runner for userID when RunnerManager is wired.
// Each tenant gets their own isolated worker, eliminating cross-tenant race conditions
// or batch execution blocking between concurrent users.
func (s *Server) tenantRunner(ctx context.Context, userID int64) *runner.Runner {
	if s.runners == nil {
		s.attachTenant(ctx, userID)
		return s.runner
	}
	store := s.tenantStore(ctx, userID)
	var pool *wa.Pool
	if s.waPool != nil {
		pool = s.waPool.Pool(userID)
	}
	settings := s.userSettings(ctx, userID)
	return s.runners.ForUser(userID, store, pool, settings)
}

// attachTenant wires the caller's own workbook, settings and WA pool into the
// fallback runner (used if runners manager is nil).
func (s *Server) attachTenant(ctx context.Context, userID int64) {
	if s.runner == nil {
		return
	}
	store := s.tenantStore(ctx, userID)
	// Interface bisa berisi typed-nil; jangan menimpa store yang sedang
	// dipakai dengan nilai yang akan panic saat dipanggil.
	if store != nil {
		s.runner.SetStore(store)
	}
	s.runner.SetSettings(userID, s.userSettings(ctx, userID))
	if s.waPool != nil {
		s.runner.SetPool(s.waPool.Pool(userID))
	}
}
