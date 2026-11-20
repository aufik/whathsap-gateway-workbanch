package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/lib/pq"

	"whatshap-gateway/internal/ai"
	"whatshap-gateway/internal/auth"
	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/database"
	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/livechat"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/runner"
)

// openLivechatTestDB reads DATABASE_URL from env or .env (mirrors livechat
// store tests). Skips when the schema has not been migrated.
func openLivechatTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		if raw, err := os.ReadFile(filepath.Join("..", "..", ".env")); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "DATABASE_URL=") {
					dsn = strings.Trim(strings.TrimPrefix(line, "DATABASE_URL="), `"'`)
					break
				}
			}
		}
	}
	if dsn == "" {
		t.Skip("DATABASE_URL tidak tersedia; melewati endpoint livechat")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	var present bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='livechat' AND table_name='sessions')`).Scan(&present); err != nil {
		db.Close()
		t.Skipf("cek schema livechat gagal (%v); skip", err)
	}
	if !present {
		db.Close()
		t.Skip("schema livechat belum migrasi; skip endpoint tests")
	}
	return db
}

func cleanupLivechatUser(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM livechat.sessions WHERE user_id=$1`, userID); err != nil {
		t.Errorf("cleanup sessions: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM livechat.assets WHERE user_id=$1`, userID); err != nil {
		t.Errorf("cleanup assets: %v", err)
	}
}

// fakeResolver returns a fake WA that satisfies the engine interface. For tests
// that only hit session/asset endpoints we never call StartSession, so the
// resolver can simply report "no device".
type errResolver struct{}

func (errResolver) ClientFor(int64, string) (livechat.WAClient, error) {
	return nil, errors.New("device tidak terhubung di test harness")
}

type livechatTestEnv struct {
	db      *sql.DB
	server  *Server
	handler http.Handler
	store   *livechat.Store
}

func newLivechatTestEnv(t *testing.T) *livechatTestEnv {
	t.Helper()
	db := openLivechatTestDB(t)
	t.Cleanup(func() { db.Close() })

	tmp := t.TempDir()
	// Minimal workbook so NewServer plumbing works without touching user data.
	cfg, err := config.Load(filepath.Join(tmp, ".env"))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	xlPath := filepath.Join(tmp, "workbook.xlsx")
	store := excel.NewEmptyStore()
	cfg.SetExcelFile(xlPath)
	aiEng := ai.NewEngine(cfg)
	run := runner.NewRunner(cfg, store, aiEng, nil, func(string, any) {})
	broker := NewSSEBroker()
	srv := NewServer(cfg, store, aiEng, nil, run, broker, nil, AuthWiring{
		DB: &database.Store{},
	})
	store2 := livechat.NewStore(db)
	assetBaseDir := filepath.Join(t.TempDir(), "assets")
	assets := livechat.NewAssetService(store2, assetBaseDir)
	t.Cleanup(func() { _ = os.RemoveAll(assetBaseDir) })
	mgr, err := livechat.NewManager(livechat.ManagerOptions{
		Store:     store2,
		Assets:    assets,
		Responder: &livechat.Responder{},
		WA:        errResolver{},
		Settings:  func(ctx context.Context, uid int64) (model.HumanSettings, bool) { return model.HumanSettings{}, false },
		MediaRoot: func(uid int64) string { return filepath.Join(t.TempDir(), "media") },
		AssetRoot: func(uid int64) string { return assetBaseDir },
		Emitter:   livechat.NopEmitter{},
	})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	srv.AttachLiveChat(store2, assets, mgr)

	env := &livechatTestEnv{db: db, server: srv, handler: srv.Handler(), store: store2}
	return env
}

// withUser wraps the handler to inject a fake session (mirrors server_test.go).
func (e *livechatTestEnv) withUser(userID int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithSession(r.Context(), &database.Session{UserID: userID, Username: "t", Role: "tester"})
		e.handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

func doJSON(t *testing.T, h http.Handler, method, url string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rc io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rc = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, url, rc)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestLivechatEndpointsRequireSession(t *testing.T) {
	env := newLivechatTestEnv(t)
	// Without session middleware the handler returns 401 via tenant wrapper.
	endpoints := []struct{ method, path string }{
		{"GET", "/api/livechat/sessions"},
		{"POST", "/api/livechat/sessions"},
		{"GET", "/api/livechat/assets"},
		{"POST", "/api/livechat/assets"},
		{"GET", "/api/livechat/preview"},
		{"GET", "/api/livechat/devices"},
	}
	for _, ep := range endpoints {
		req := httptest.NewRequest(ep.method, ep.path, nil)
		rec := httptest.NewRecorder()
		env.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s => %d, want 401", ep.method, ep.path, rec.Code)
		}
	}
}

func TestLivechatUserIsolation(t *testing.T) {
	env := newLivechatTestEnv(t)
	const u1, u2 int64 = 900401, 900402
	cleanupLivechatUser(t, env.db, u1)
	cleanupLivechatUser(t, env.db, u2)
	defer cleanupLivechatUser(t, env.db, u1)
	defer cleanupLivechatUser(t, env.db, u2)

	// Create session for u1 directly via store.
	sess, err := env.store.CreateSession(context.Background(), u1, livechat.SessionInput{
		DeviceUUID: "d1", WAPhone: "1", BotPhone: "628111", Title: "t1", ReportText: "r1",
		NamaPengadu: "n1", TanggalKejadian: "2026-11-01", AlamatKejadian: "a1",
		TitikLokasiSrc: livechat.SourceEmpty, MaxTurns: 5, CategoryMode: livechat.ModeBenar,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// u2 cannot see u1's session.
	h2 := env.withUser(u2)
	rec, _ := doJSON(t, h2, "GET", "/api/livechat/sessions", nil)
	if rec.Code != 200 {
		t.Fatalf("list u2: %d", rec.Code)
	}
	// Detail of u1's session from u2 → 404.
	r2 := httptest.NewRequest("GET", "/api/livechat/sessions/"+itoa(sess.ID), nil)
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, r2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("u2 detail u1 => %d, want 404", rec2.Code)
	}
	// u1 sees it.
	h1 := env.withUser(u1)
	r1 := httptest.NewRequest("GET", "/api/livechat/sessions/"+itoa(sess.ID), nil)
	rec1 := httptest.NewRecorder()
	h1.ServeHTTP(rec1, r1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("u1 detail => %d, want 200", rec1.Code)
	}
}

func TestLivechatAssetUploadAndPreview(t *testing.T) {
	env := newLivechatTestEnv(t)
	const uid int64 = 900403
	cleanupLivechatUser(t, env.db, uid)
	defer cleanupLivechatUser(t, env.db, uid)
	h := env.withUser(uid)

	// Small valid PNG (1x1).
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", "foto.png")
	_, _ = part.Write(png)
	_ = mw.WriteField("label", "Foto Jalan")
	mw.Close()
	req := httptest.NewRequest("POST", "/api/livechat/assets", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("upload => %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Asset struct {
			ID       int64  `json:"id"`
			Mimetype string `json:"mimetype"`
			SHA256   string `json:"sha256"`
		} `json:"asset"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Asset.ID == 0 || out.Asset.Mimetype != "image/png" {
		t.Fatalf("bad upload response: %+v", out)
	}

	// Preview must return the bytes (for the same user).
	prev := httptest.NewRequest("GET", "/api/livechat/assets/"+itoa(out.Asset.ID)+"/preview", nil)
	prec := httptest.NewRecorder()
	h.ServeHTTP(prec, prev)
	if prec.Code != 200 || !bytes.Equal(prec.Body.Bytes(), png) {
		t.Fatalf("preview => %d bodylen=%d", prec.Code, prec.Body.Len())
	}

	// Cross-user preview → 404.
	other := env.withUser(900404)
	defer cleanupLivechatUser(t, env.db, 900404)
	orec := httptest.NewRecorder()
	other.ServeHTTP(orec, httptest.NewRequest("GET", "/api/livechat/assets/"+itoa(out.Asset.ID)+"/preview", nil))
	if orec.Code != http.StatusNotFound {
		t.Fatalf("cross-user preview => %d, want 404", orec.Code)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
