package runner

import (
	"path/filepath"
	"sync"
	"testing"

	"whatshap-gateway/internal/ai"
	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
)

func TestRunnerManagerMultiTenantIsolation(t *testing.T) {
	origExcel := filepath.Join("..", "..", "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
	tmpDir := t.TempDir()

	store1, err := excel.NewStore(origExcel)
	if err != nil {
		t.Fatalf("create store1: %v", err)
	}
	store2, err := excel.NewStore(origExcel)
	if err != nil {
		t.Fatalf("create store2: %v", err)
	}

	cfg, _ := config.Load(filepath.Join(tmpDir, ".env"))
	aiEng := ai.NewEngine(cfg)

	var emittedEventsMu sync.Mutex
	emittedEvents := make(map[int64][]string)

	var rowSavedMu sync.Mutex
	rowSavedUsers := make(map[int64]int)

	mgr := NewManager(cfg, aiEng, func(userID int64, eventType string, payload any) {
		emittedEventsMu.Lock()
		defer emittedEventsMu.Unlock()
		emittedEvents[userID] = append(emittedEvents[userID], eventType)
	}, func(userID int64) {
		rowSavedMu.Lock()
		defer rowSavedMu.Unlock()
		rowSavedUsers[userID]++
	})

	settings1 := model.HumanSettings{BotPhone: "628111111111"}
	settings2 := model.HumanSettings{BotPhone: "628222222222"}

	r1 := mgr.ForUser(1, store1, nil, settings1)
	r2 := mgr.ForUser(2, store2, nil, settings2)

	if r1 == r2 {
		t.Fatal("expected separate runner instances for user 1 and user 2, got identical pointer")
	}

	if r1.activeSettings().BotPhone != "628111111111" {
		t.Fatalf("expected bot phone for user 1 to be 628111111111, got %s", r1.activeSettings().BotPhone)
	}
	if r2.activeSettings().BotPhone != "628222222222" {
		t.Fatalf("expected bot phone for user 2 to be 628222222222, got %s", r2.activeSettings().BotPhone)
	}

	// Verify event broadcasting is strictly scoped
	r1.broadcast("test_event_1", map[string]string{"msg": "hello from user 1"})
	r2.broadcast("test_event_2", map[string]string{"msg": "hello from user 2"})

	emittedEventsMu.Lock()
	if len(emittedEvents[1]) != 1 || emittedEvents[1][0] != "test_event_1" {
		t.Fatalf("expected test_event_1 for user 1, got %v", emittedEvents[1])
	}
	if len(emittedEvents[2]) != 1 || emittedEvents[2][0] != "test_event_2" {
		t.Fatalf("expected test_event_2 for user 2, got %v", emittedEvents[2])
	}
	emittedEventsMu.Unlock()

	// Verify onRowSaved hook is strictly scoped
	r1.notifyRowSaved()
	rowSavedMu.Lock()
	if rowSavedUsers[1] != 1 || rowSavedUsers[2] != 0 {
		t.Fatalf("expected user 1 row saved count 1, user 2 count 0, got %v", rowSavedUsers)
	}
	rowSavedMu.Unlock()

	// Verify StopAll does not panic and stops runners
	mgr.StopAll()
}
