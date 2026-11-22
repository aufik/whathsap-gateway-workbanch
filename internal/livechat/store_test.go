package livechat

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// These tests run against the real PostgreSQL instance configured in
// DATABASE_URL. Each test creates its own throwaway rows scoped to synthetic
// user IDs (>= 900001) that do not exist in public.users, and deletes them on
// cleanup. They never touch public.* or any existing livechat session that
// belongs to a real user.

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		if raw, err := os.ReadFile(filepath.Join("..", "..", ".env")); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "DATABASE_URL=") {
					dsn = strings.TrimPrefix(line, "DATABASE_URL=")
					dsn = strings.Trim(dsn, `"'`)
					break
				}
			}
		}
	}
	if dsn == "" {
		t.Skip("DATABASE_URL tidak tersedia; melewati test store livechat")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Skipf("PostgreSQL tidak dapat dihubungi (%v); melewati test store livechat", err)
	}
	return db
}

func cleanupUser(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM livechat.sessions WHERE user_id = $1`, userID); err != nil {
		t.Errorf("cleanup sessions: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM livechat.assets WHERE user_id = $1`, userID); err != nil {
		t.Errorf("cleanup assets: %v", err)
	}
}

func sampleInput() SessionInput {
	return SessionInput{
		DeviceUUID:      "test-device-uuid",
		WAPhone:         "628000000001",
		BotPhone:        "628000000000",
		Title:           "Jalan rusak test",
		ReportText:      "Ada lubang besar di depan pasar.",
		NamaPengadu:     "Budi Santoso",
		TanggalKejadian: "2026-11-10",
		AlamatKejadian:  "Jl. Merdeka No. 1, Medan",
		TitikLokasi:     "https://maps.google.com/?q=3.5952,98.6722",
		TitikLokasiSrc:  SourceRandom,
		AssetIDs:        nil,
		MaxTurns:        40,
		CategoryMode:    ModeBenar,
	}
}

func TestStoreSessionCRUDAndCascade(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	st := NewStore(db)
	ctx := context.Background()
	const u1, u2 int64 = 900001, 900002
	cleanupUser(t, db, u1)
	cleanupUser(t, db, u2)
	defer cleanupUser(t, db, u1)
	defer cleanupUser(t, db, u2)

	if err := st.MarkStarted(ctx, u1, 0); err != nil {
		t.Logf("MarkStarted with invalid id is expected no-op: %v", err)
	}

	// 1. Create
	input := sampleInput()
	input.RNGSeed = 123456789
	sess, err := st.CreateSession(ctx, u1, input)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if sess.ID == 0 || sess.UserID != u1 {
		t.Fatalf("unexpected session: %+v", sess)
	}
	if sess.RNGSeed != 123456789 {
		t.Fatalf("rng_seed wrong: got %d, want 123456789", sess.RNGSeed)
	}
	if sess.Status != StatusRunning || sess.MaxTurns != 40 || sess.CategoryMode != ModeBenar {
		t.Fatalf("session defaults wrong: %+v", sess)
	}
	if sess.AssetIDs == nil || len(sess.AssetIDs) != 0 {
		t.Fatalf("asset_ids should be non-nil empty slice, got %#v", sess.AssetIDs)
	}

	// 1b. Create with empty ReportText and Title (must auto-fill Title and allow empty ReportText)
	emptyIn := sampleInput()
	emptyIn.ReportText = ""
	emptyIn.Title = ""
	emptySess, err := st.CreateSession(ctx, u1, emptyIn)
	if err != nil {
		t.Fatalf("create session with empty report text: %v", err)
	}
	if emptySess.ReportText != "" {
		t.Fatalf("expected empty ReportText string, got %q", emptySess.ReportText)
	}
	if emptySess.Title == "" {
		t.Fatalf("expected auto-filled title, got empty")
	}
	_ = st.DeleteSession(ctx, u1, emptySess.ID)

	// 2. List per user
	listU1, err := st.ListSessions(ctx, u1, 0)
	if err != nil || len(listU1) != 1 {
		t.Fatalf("list u1: n=%d err=%v", len(listU1), err)
	}
	listU2, err := st.ListSessions(ctx, u2, 0)
	if err != nil || len(listU2) != 0 {
		t.Fatalf("isolation broken: u2 saw %d rows (err=%v)", len(listU2), err)
	}

	// 3. Get detail only when owned
	detail, err := st.GetSessionDetail(ctx, u1, sess.ID)
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if detail.Session.ID != sess.ID || len(detail.Messages) != 0 || len(detail.Turns) != 0 {
		t.Fatalf("detail initial state wrong: %+v", detail)
	}
	if _, err := st.GetSessionDetail(ctx, u2, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("u2 must not see u1 session, got %v", err)
	}

	// 4. Insert messages + turns
	if _, err := st.InsertMessage(ctx, u1, Message{SessionID: sess.ID, TurnNo: 0, Direction: DirectionSent, Body: "saya mau buat pengaduan", MsgType: MsgText}); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	latency := 1200
	if _, err := st.InsertMessage(ctx, u1, Message{SessionID: sess.ID, TurnNo: 1, Direction: DirectionReceived, Body: "Silakan isi nama", MsgType: MsgText, LatencyMs: &latency}); err != nil {
		t.Fatalf("insert received message: %v", err)
	}
	if _, err := st.InsertMessage(ctx, u2, Message{SessionID: sess.ID, TurnNo: 2, Direction: DirectionSent, Body: "hijack", MsgType: MsgText}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign insert must be rejected, got %v", err)
	}
	if _, err := st.InsertTurn(ctx, u1, Turn{SessionID: sess.ID, TurnNo: 1, BotQuestion: "Siapa nama lengkap?", QuestionKind: "nama", OurAnswer: "Budi Santoso", CategoryMode: "benar", MatchStatus: "ok"}); err != nil {
		t.Fatalf("insert turn: %v", err)
	}
	if _, err := st.InsertTurn(ctx, u1, Turn{SessionID: sess.ID, TurnNo: 1, BotQuestion: "dup", QuestionKind: "nama", OurAnswer: "x"}); err == nil {
		t.Fatalf("duplicate turn must fail via unique index")
	}

	// 5. Progress + Finish
	if err := st.UpdateSessionProgress(ctx, u1, sess.ID, 1, "385692"); err != nil {
		t.Fatalf("update progress: %v", err)
	}
	if err := st.FinishSession(ctx, u1, sess.ID, StatusFinished, "bot menyebut diterima", "385692", "bubble 1\nbubble 2\nbubble 3"); err != nil {
		t.Fatalf("finish session: %v", err)
	}
	if err := st.FinishSession(ctx, u1, sess.ID, StatusFinished, "again", "", ""); !errors.Is(err, ErrAlreadyClosed) {
		t.Fatalf("second FinishSession must report ErrAlreadyClosed, got %v", err)
	}
	if _, err := st.InsertMessage(ctx, u1, Message{SessionID: sess.ID, TurnNo: 5, Direction: DirectionSent, Body: "late", MsgType: MsgText}); err != nil {
		// Messages aren't gated on status; just ensure no panic.
		t.Logf("late insert note: %v", err)
	}

	// 6. Final detail matches
	detail, err = st.GetSessionDetail(ctx, u1, sess.ID)
	if err != nil {
		t.Fatalf("final detail: %v", err)
	}
	if detail.Session.Status != StatusFinished || detail.Session.TicketNo != "385692" || detail.Session.TotalTurns != 1 {
		t.Fatalf("session final state wrong: %+v", detail.Session)
	}
	if len(detail.Messages) < 2 || len(detail.Turns) != 1 {
		t.Fatalf("detail should carry messages+turns, got msgs=%d turns=%d", len(detail.Messages), len(detail.Turns))
	}

	// 7. Delete + cascade
	if err := st.DeleteSession(ctx, u2, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign delete must fail, got %v", err)
	}
	if err := st.DeleteSession(ctx, u1, sess.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	var msgCount, turnCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM livechat.messages WHERE session_id = $1`, sess.ID).Scan(&msgCount); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM livechat.turns WHERE session_id = $1`, sess.ID).Scan(&turnCount); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if msgCount != 0 || turnCount != 0 {
		t.Fatalf("cascade failed: messages=%d turns=%d remain", msgCount, turnCount)
	}
}

func TestStoreAssetLifecycle(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	st := NewStore(db)
	ctx := context.Background()
	const u1, u2 int64 = 900001, 900002
	cleanupUser(t, db, u1)
	cleanupUser(t, db, u2)
	defer cleanupUser(t, db, u1)
	defer cleanupUser(t, db, u2)

	in := AssetInput{
		Kind:       AssetImage,
		Label:      "Foto Jalan Rusak",
		FileName:   "foto.jpg",
		StoredPath: "aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.jpg",
		Mimetype:   "image/jpeg",
		SizeBytes:  12345,
		SHA256:     strings.Repeat("a", 64),
	}
	a, err := st.CreateAsset(ctx, u1, in)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	// Duplicate (same sha) must report ErrDuplicate
	if _, err := st.CreateAsset(ctx, u1, in); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate asset must report ErrDuplicate, got %v", err)
	}

	// Cross-user isolation
	if _, err := st.GetAsset(ctx, u2, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("u2 must not see u1 asset, got %v", err)
	}

	// Asset-in-use guard
	sess, err := st.CreateSession(ctx, u1, SessionInput{
		DeviceUUID: "dev", WAPhone: "1", BotPhone: "2", Title: "t", ReportText: "r",
		NamaPengadu: "n", TanggalKejadian: "2026-11-01", AlamatKejadian: "a",
		TitikLokasiSrc: SourceEmpty, AssetIDs: []int64{a.ID}, MaxTurns: 5, CategoryMode: ModeBenar,
	})
	if err != nil {
		t.Fatalf("create session with asset: %v", err)
	}
	if inUse, err := st.AssetInUse(ctx, u1, a.ID); err != nil || !inUse {
		t.Fatalf("asset should be in use, inUse=%v err=%v", inUse, err)
	}
	if err := st.DeleteAsset(ctx, u1, a.ID); !errors.Is(err, ErrAssetInUse) {
		t.Fatalf("delete must be refused when in use, got %v", err)
	}

	// Delete session first, then asset
	if err := st.DeleteSession(ctx, u1, sess.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if err := st.DeleteAsset(ctx, u1, a.ID); err != nil {
		t.Fatalf("delete asset after session: %v", err)
	}
}
