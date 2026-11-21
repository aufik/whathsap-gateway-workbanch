package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"whatshap-gateway/internal/livechat"
)

func livechatSessionInput() livechat.SessionInput {
	return livechat.SessionInput{
		DeviceUUID: "d", WAPhone: "1", BotPhone: "2", Title: "dl test", ReportText: "r",
		NamaPengadu: "n", TanggalKejadian: "2026-11-01", AlamatKejadian: "a",
		TitikLokasiSrc: livechat.SourceEmpty, MaxTurns: 5, CategoryMode: livechat.ModeBenar,
	}
}

// TestLivechatExcelDownloadFromDB memastikan endpoint membangun workbook dari
// schema livechat: 404 bila user belum punya sesi, 200 + byte xlsx bila ada,
// tanpa membaca file disk.
func TestLivechatExcelDownloadFromDB(t *testing.T) {
	env := newLivechatTestEnv(t)

	// User sintetis tanpa sesi -> 404.
	const emptyUser int64 = 900960
	cleanupLivechatUser(t, env.db, emptyUser)
	defer cleanupLivechatUser(t, env.db, emptyUser)
	h := env.withUser(emptyUser)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/livechat/excel/download", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("user tanpa sesi harus 404, dapat %d", rec.Code)
	}

	// User dengan satu sesi -> 200 dan body xlsx valid (magic PK).
	const withUser int64 = 900961
	cleanupLivechatUser(t, env.db, withUser)
	defer cleanupLivechatUser(t, env.db, withUser)
	if _, err := env.store.CreateSession(t.Context(), withUser, livechatSessionInput()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	h2 := env.withUser(withUser)
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, httptest.NewRequest("GET", "/api/livechat/excel/download", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("user dengan sesi harus 200, dapat %d body=%s", rec2.Code, rec2.Body.String())
	}
	if ct := rec2.Header().Get("Content-Type"); !strings.Contains(ct, "spreadsheetml") {
		t.Fatalf("content-type: %q", ct)
	}
	body := rec2.Body.Bytes()
	if len(body) < 1000 || body[0] != 'P' || body[1] != 'K' {
		t.Fatalf("body bukan xlsx valid: len=%d prefix=%q", len(body), string(body[:min(2, len(body))]))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
