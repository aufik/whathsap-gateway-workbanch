package web

import (
	"bytes"
	"context"
	"encoding/json"
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

	run := runner.NewRunner(cfg, store, aiEng, waCli, broker.Broadcast)
	mockFS := fstest.MapFS{
		"index.html": {Data: []byte("<html><body>QA Workbench</body></html>")},
	}

	srv := NewServer(cfg, store, aiEng, waCli, run, broker, mockFS)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. GET /api/state
	res, err := http.Get(ts.URL + "/api/state")
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
	genBody := bytes.NewBufferString(`{"mode":"chaos","save":true}`)
	res, err = http.Post(ts.URL+"/api/rows/1/generate", "application/json", genBody)
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
	patchReq, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/rows/1", bytes.NewBufferString(`{"resultStatus":"Cocok","notes":"Uji E2E berhasil"}`))
	patchReq.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(patchReq)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("PATCH /api/rows/1 failed: status=%v err=%v", res, err)
	}
	res.Body.Close()

	// 4. GET /api/excel/download
	res, err = http.Get(ts.URL + "/api/excel/download")
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/excel/download failed: status=%v err=%v", res, err)
	}
	res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Disposition"), "Uji-Bank-Pertanyaan-Programmer-4.xlsx") {
		t.Fatalf("unexpected content-disposition: %q", res.Header.Get("Content-Disposition"))
	}
}
