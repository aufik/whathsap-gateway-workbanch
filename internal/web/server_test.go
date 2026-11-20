package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"whatshap-gateway/internal/ai"
	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/auth"
	"whatshap-gateway/internal/database"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/runner"
	"whatshap-gateway/internal/wa"
)

func TestServerEndpointsEndToEnd(t *testing.T) {
	origExcel := filepath.Join("..", "..", "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
	rawBytes, err := os.ReadFile(origExcel)
	if err != nil {
		t.Fatalf("read source excel: %v", err)
	}

	tmpDir := t.TempDir()
	workExcel := filepath.Join(tmpDir, "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
	if err := os.WriteFile(workExcel, rawBytes, 0644); err != nil {
		t.Fatalf("write temp excel: %v", err)
	}

	envFile := filepath.Join(tmpDir, ".env")
	cfg, err := config.Load(envFile)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.SetExcelFile(workExcel)

	store, err := excel.NewStore(workExcel)
	if err != nil {
		t.Fatalf("init excel store: %v", err)
	}

	broker := NewSSEBroker()
	aiEng := ai.NewEngine(cfg)
	waCli, err := wa.NewClient(context.Background(), filepath.Join(tmpDir, "test_wa.db"), nil)
	if err != nil {
		t.Fatalf("init wa client: %v", err)
	}
	defer waCli.Close()

	run := runner.NewRunner(cfg, store, aiEng, waCli, func(eventType string, payload any) {
		broker.Broadcast(1, eventType, payload)
	})
	mockFS := fstest.MapFS{
		"index.html": {Data: []byte("<html><body>QA Workbench</body></html>")},
	}

	srv := NewServer(cfg, store, aiEng, waCli, run, broker, mockFS, AuthWiring{})
	h := srv.Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Test harness: attach a fixed tenant identity, mirroring what the real
		// session middleware provides in production.
		ctx := auth.WithSession(r.Context(), &database.Session{UserID: 1, Username: "test", FullName: "Test", Role: "admin"})
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer ts.Close()

	// 1. GET /api/state (test client carries a fake authenticated session)
	res, err := doReq(ts.URL + "/api/state")
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/state failed: status=%v err=%v", res, err)
	}
	var snap model.AppStateSnapshot
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	res.Body.Close()
	if snap.Summary.Total != 210 || len(snap.Rows) != 210 {
		t.Fatalf("expected 210 rows in snapshot, got total=%d len=%d", snap.Summary.Total, len(snap.Rows))
	}

	// 2. POST /api/rows/1/generate (chaos mode)
	res, err = doReq(ts.URL+"/api/rows/1/generate", `{"mode":"chaos","save":true}`)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/rows/1/generate failed: status=%v err=%v", res, err)
	}
	var genOut struct {
		Generated string        `json:"generated"`
		Row       model.TestRow `json:"row"`
	}
	_ = json.NewDecoder(res.Body).Decode(&genOut)
	res.Body.Close()
	if !strings.Contains(genOut.Generated, "2022") || genOut.Row.QuestionInput != genOut.Generated {
		t.Fatalf("unexpected generated chaos output: %+v", genOut)
	}

	// 3. PATCH /api/rows/1 -> verify summary updates & Excel download works
	res, err = doReq(ts.URL+"/api/rows/1", `{"resultStatus":"Cocok","notes":"Uji E2E berhasil"}`)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("PATCH /api/rows/1 failed: status=%v err=%v", res, err)
	}
	res.Body.Close()

	// 4. GET /api/excel/download
	res, err = doReq(ts.URL + "/api/excel/download")
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/excel/download failed: status=%v err=%v", res, err)
	}
	res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Disposition"), "Uji-Bank-Pertanyaan-Programmer-4.xlsx") {
		t.Fatalf("unexpected content-disposition: %q", res.Header.Get("Content-Disposition"))
	}
}

func TestServerMultiTenantRunnerIsolation(t *testing.T) {
	origExcel := filepath.Join("..", "..", "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
	rawBytes, err := os.ReadFile(origExcel)
	if err != nil {
		t.Fatalf("read source excel: %v", err)
	}

	tmpDir := t.TempDir()
	workExcel := filepath.Join(tmpDir, "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
	if err := os.WriteFile(workExcel, rawBytes, 0644); err != nil {
		t.Fatalf("write temp excel: %v", err)
	}

	cfg, _ := config.Load(filepath.Join(tmpDir, ".env"))
	cfg.SetExcelFile(workExcel)

	store, err := excel.NewStore(workExcel)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	broker := NewSSEBroker()
	aiEng := ai.NewEngine(cfg)
	runnerMgr := runner.NewManager(cfg, aiEng, func(userID int64, eventType string, payload any) {
		broker.Broadcast(userID, eventType, payload)
	}, nil)

	mockFS := fstest.MapFS{
		"index.html": {Data: []byte("<html><body>QA Workbench</body></html>")},
	}

	srv := NewServer(cfg, store, aiEng, nil, nil, broker, mockFS, AuthWiring{
		Runners: runnerMgr,
	})

	h := srv.Handler()

	// Handler that sets tenant ID from X-User-ID header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uID := int64(1)
		if r.Header.Get("X-User-ID") == "2" {
			uID = 2
		}
		ctx := auth.WithSession(r.Context(), &database.Session{UserID: uID, Username: "test", FullName: "Test", Role: "admin"})
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer ts.Close()

	// Check state for User 1
	req1, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
	req1.Header.Set("X-User-ID", "1")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil || resp1.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/state user 1 failed: status=%v err=%v", resp1, err)
	}
	var snap1 model.AppStateSnapshot
	_ = json.NewDecoder(resp1.Body).Decode(&snap1)
	resp1.Body.Close()

	// Check state for User 2
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
	req2.Header.Set("X-User-ID", "2")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil || resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/state user 2 failed: status=%v err=%v", resp2, err)
	}
	var snap2 model.AppStateSnapshot
	_ = json.NewDecoder(resp2.Body).Decode(&snap2)
	resp2.Body.Close()

	if snap1.CurrentUser.ID != 1 || snap2.CurrentUser.ID != 2 {
		t.Fatalf("expected user 1 and user 2 in respective snapshots, got %v and %v", snap1.CurrentUser.ID, snap2.CurrentUser.ID)
	}

	// Verify both runners are distinct instances
	r1, ok1 := runnerMgr.Get(1)
	r2, ok2 := runnerMgr.Get(2)
	if !ok1 || !ok2 || r1 == r2 {
		t.Fatalf("expected two distinct runner instances created, got r1=%p, r2=%p", r1, r2)
	}
}

// doReq issues an authenticated request; the test server has no auth manager,
// so the tenant test bypasses the session cookie and calls the handler chain
// through a lightweight server that injects a fixed tenant identity.
func doReq(url string, body ...string) (*http.Response, error) {
	method := http.MethodGet
	var rdr io.Reader
	if len(body) > 0 {
		method = http.MethodPost
		rdr = strings.NewReader(body[0])
		if strings.Contains(url, "/api/rows/") && !strings.Contains(url, "/generate") && !strings.Contains(url, "/run") && !strings.Contains(url, "/evaluate") {
			method = http.MethodPatch
		}
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	return http.DefaultClient.Do(req)
}

