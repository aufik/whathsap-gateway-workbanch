package web

import (
	"context"
	"errors"
	"testing"

	"whatshap-gateway/internal/ai"
	"whatshap-gateway/internal/auth"
	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/database"
	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/runner"
	"whatshap-gateway/internal/workbench"
)

// fakeStore memenuhi workbench.Store minimal untuk menguji pemilihan backend.
// Semua method memakai embedding agar tidak perlu menulis 25 stub.
type fakeStore struct {
	workbench.Store
	tag string
}

func (f *fakeStore) Snapshot() ([]model.TestRow, model.OverallSummary) {
	return []model.TestRow{{No: 1, OPD: f.tag}}, model.OverallSummary{Total: 1}
}

// TestTenantStoreSelectsBackend memastikan factory workbench menang atas jalur
// file ketika dipasang, dan jalur file kembali dipakai setelah factory
// dilepas (rollback tanpa revert kode).
func TestTenantStoreSelectsBackend(t *testing.T) {
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("cfg: %v", err)
	}
	xl := excel.NewEmptyStore()
	aiEng := ai.NewEngine(cfg)
	run := runner.NewRunner(cfg, xl, aiEng, nil, nil)
	srv := NewServer(cfg, xl, aiEng, nil, run, NewSSEBroker(), nil, AuthWiring{DB: &database.Store{}})

	// 1) Tanpa factory → jalur file (tidak nil).
	if srv.HasWorkbenchFactory() {
		t.Fatalf("factory seharusnya belum aktif")
	}
	if got := srv.tenantStore(context.Background(), 7); got == nil {
		t.Fatalf("tenantStore file-path tidak boleh nil")
	}

	// 2) Factory aktif → DBStore/fake dipakai.
	var seenUser int64
	srv.SetWorkbenchFactory(func(userID int64) (workbench.Store, error) {
		seenUser = userID
		return &fakeStore{tag: "db"}, nil
	})
	if !srv.HasWorkbenchFactory() {
		t.Fatalf("factory seharusnya aktif")
	}
	got := srv.tenantStore(context.Background(), 42)
	if seenUser != 42 {
		t.Fatalf("factory dipanggil dengan user %d, want 42", seenUser)
	}
	rows, sum := got.Snapshot()
	if len(rows) != 1 || rows[0].OPD != "db" || sum.Total != 1 {
		t.Fatalf("tenantStore tidak memakai backend DB: rows=%v sum=%+v", rows, sum)
	}

	// 3) Factory error → fallback ke file, bukan panic.
	srv.SetWorkbenchFactory(func(int64) (workbench.Store, error) {
		return nil, errors.New("db down")
	})
	if got := srv.tenantStore(context.Background(), 1); got == nil {
		t.Fatalf("factory error harus fallback, bukan nil")
	}

	// 4) Lebpas factory → kembali ke file (rollback).
	srv.SetWorkbenchFactory(nil)
	if srv.HasWorkbenchFactory() {
		t.Fatalf("factory seharusnya nonaktif setelah dilepas")
	}
}

// TestConfigWorkbenchFlag memastikan env WORKBENCH_DB dibaca dengan benar.
func TestConfigWorkbenchFlag(t *testing.T) {
	cases := map[string]bool{
		"":      true, // default Tahap 12: data di database
		"0":     false,
		"false": false,
		"1":     true,
		"true":  true,
		"yes":   true,
		"on":    true,
	}
	for raw, want := range cases {
		// Kosong berarti env tidak diset -> default dipakai.
		if raw == "" {
			t.Setenv("WORKBENCH_DB", " ")
		} else {
			t.Setenv("WORKBENCH_DB", raw)
		}
		cfg, err := config.Load("")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.WorkbenchDB != want {
			t.Fatalf("WORKBENCH_DB=%q -> %v, want %v", raw, cfg.WorkbenchDB, want)
		}
	}
}

var _ = auth.FromContext
