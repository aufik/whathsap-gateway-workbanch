package livechat

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whatshap-gateway/internal/model"
)

// --- fakes ---

// fakeCollector delivers scripted bot replies through Wait, and records Close.
type fakeCollector struct {
	mu      sync.Mutex
	replies []string
	next    int
	closed  atomic.Int32
	onWait  func()
}

func (f *fakeCollector) Wait(ctx context.Context, _quiet, _hard time.Duration) ([]CollectedBubble, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	f.mu.Lock()
	if f.next >= len(f.replies) {
		f.mu.Unlock()
		return nil, nil // simulate "no reply before timeout"
	}
	body := f.replies[f.next]
	f.next++
	f.mu.Unlock()
	if f.onWait != nil {
		f.onWait()
	}
	return []CollectedBubble{{ID: "id", Text: body}}, nil
}

func (f *fakeCollector) Close() { f.closed.Add(1) }

// fakeWA captures outgoing messages and serves the next collector.
type fakeWA struct {
	mu        sync.Mutex
	collector *fakeCollector
	texts     []string
	images    int
	docs      int
	locs      int
	connected bool
	loggedIn  bool
	regErr    error
	registers int
}

func newFakeWA(replies ...string) *fakeWA {
	return &fakeWA{collector: &fakeCollector{replies: replies}, connected: true, loggedIn: true}
}

func (f *fakeWA) SendText(_ context.Context, _ string, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.connected {
		return errors.New("disconnected")
	}
	f.texts = append(f.texts, text)
	return nil
}

func (f *fakeWA) SendImage(_ context.Context, _ string, _ []byte, _, caption string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images++
	f.texts = append(f.texts, "IMG:"+caption)
	return nil
}
func (f *fakeWA) SendDocument(_ context.Context, _ string, _ []byte, _ string, _, caption string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.docs++
	f.texts = append(f.texts, "DOC:"+caption)
	return nil
}
func (f *fakeWA) SendLocation(_ context.Context, _ string, _, _ float64, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.locs++
	f.texts = append(f.texts, "LOC")
	return nil
}
func (f *fakeWA) RegisterCollector(_ string) (ReplyCollector, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registers++
	if f.regErr != nil {
		return nil, f.regErr
	}
	return f.collector, nil
}
func (f *fakeWA) Status() model.WAStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return model.WAStatus{Connected: f.connected, LoggedIn: f.loggedIn}
}
func (f *fakeWA) setDisconnectedOnly() {
	f.mu.Lock()
	f.connected = false
	f.loggedIn = true
	f.mu.Unlock()
}
func (f *fakeWA) setLoggedOut() {
	f.mu.Lock()
	f.connected = false
	f.loggedIn = false
	f.mu.Unlock()
}
func (f *fakeWA) setConnected(v bool) {
	f.mu.Lock()
	f.connected = v
	f.mu.Unlock()
}
func (f *fakeWA) SetPresenceAvailable(_ context.Context, _ bool) {}
func (f *fakeWA) MarkBubblesRead(_ context.Context, _ string, _ []string) error { return nil }
func (f *fakeWA) SimulateTyping(_ context.Context, _ string, _ string, _ model.HumanSettings) error { return nil }
func (f *fakeWA) PauseTyping(_ context.Context, _ string) error { return nil }

// --- fixtures ---

func engineTestDeps(t *testing.T) (*Store, *sql.DB, int64) {
	t.Helper()
	db := openAssetsTestDB(t)
	t.Cleanup(func() { db.Close() })
	st := NewStore(db)
	uid := int64(900300)
	t.Cleanup(func() { cleanupUser(t, db, uid) })
	return st, db, uid
}

func newTestSession(t *testing.T, st *Store, userID int64, mode CategoryMode, maxTurns int) Session {
	t.Helper()
	in := sampleInput()
	in.CategoryMode = mode
	in.MaxTurns = maxTurns
	in.DeviceUUID = "test-device"
	sess, err := st.CreateSession(context.Background(), userID, in)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func fastSettings() model.HumanSettings {
	return model.HumanSettings{
		PreThinkMinMs:       0,
		PreThinkMaxMs:       0,
		TypingMsPerChar:     1,
		TypingMinMs:         0,
		TypingMaxMs:         1,
		DelayMinMs:          0,
		DelayMaxMs:          0,
		DistractionSpikePct: 0,
		CooldownEveryMin:    100,
		CooldownEveryMax:    101,
		CooldownMinMs:       0,
		CooldownMaxMs:       0,
		QuietWindowMs:       1,
		ReplyTimeoutMs:      400,
	}
}

func newTestEngine(t *testing.T, sess Session, fake *fakeWA, st *Store) *Engine {
	t.Helper()
	eng, err := newEngine(engineConfig{
		Store:     st,
		WA:        fake,
		Responder: &Responder{},
		Settings:  fastSettings(),
		AssetRoot: t.TempDir(),
		MediaRoot: t.TempDir(),
		Emitter:   NopEmitter{},
	}, sess)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return eng
}

// --- tests ---

func TestEngineFinishesOnDoneSignal(t *testing.T) {
	st, _, uid := engineTestDeps(t)
	sess := newTestSession(t, st, uid, ModeBenar, 10)
	fake := newFakeWA(
		"Silakan isi nama lengkap Anda.",
		"Pengaduan Anda telah kami terima.\nNomor tiket #385692",
	)
	eng := newTestEngine(t, sess, fake, st)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := eng.Run(ctx); err != nil {
		t.Fatalf("engine run: %v", err)
	}

	detail, err := st.GetSessionDetail(context.Background(), uid, sess.ID)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Session.Status != StatusFinished {
		t.Fatalf("status=%s, want finished", detail.Session.Status)
	}
	if detail.Session.TicketNo != "385692" {
		t.Fatalf("ticket=%q", detail.Session.TicketNo)
	}
	if fake.collector.closed.Load() != 1 {
		t.Fatalf("collector must be closed exactly once, got %d", fake.collector.closed.Load())
	}
	// opening text + nama answer were sent; the done line was NOT answered.
	if len(fake.texts) != 2 {
		t.Fatalf("expected 2 sent texts, got %d (%v)", len(fake.texts), fake.texts)
	}
	if !strings.Contains(fake.texts[1], sess.NamaPengadu) {
		t.Fatalf("second send should contain nama, got %q", fake.texts[1])
	}
	if len(detail.Turns) != 1 {
		t.Fatalf("expected 1 audited turn, got %d", len(detail.Turns))
	}
	if detail.Session.LastChatExcerpt == "" {
		t.Fatalf("last_chat_excerpt must be populated on close")
	}
}

func TestEngineTimesOutAtMaxTurns(t *testing.T) {
	st, _, uid := engineTestDeps(t)
	sess := newTestSession(t, st, uid, ModeBenar, 1) // only 1 turn allowed
	fake := newFakeWA("Silakan isi nama lengkap", "Alamat kejadian lengkap?")
	eng := newTestEngine(t, sess, fake, st)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = eng.Run(ctx)

	detail, _ := st.GetSessionDetail(context.Background(), uid, sess.ID)
	if detail.Session.Status != StatusTimeout {
		t.Fatalf("status=%s, want timeout", detail.Session.Status)
	}
	if detail.Session.EndReason == "" {
		t.Fatalf("end_reason must record the timeout reason")
	}
}

func TestEngineFailsOnDisconnect(t *testing.T) {
	st, _, uid := engineTestDeps(t)
	sess := newTestSession(t, st, uid, ModeBenar, 10)
	fake := newFakeWA("Silakan isi nama lengkap", "Alamat?")
	fake.collector.onWait = func() { fake.setLoggedOut() }
	eng := newTestEngine(t, sess, fake, st)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = eng.Run(ctx)

	detail, _ := st.GetSessionDetail(context.Background(), uid, sess.ID)
	if detail.Session.Status != StatusFailed {
		t.Fatalf("status=%s, want failed", detail.Session.Status)
	}
	if detail.Session.EndReason == "" {
		t.Fatalf("end_reason must record the disconnect")
	}
}

func TestEngineStopManual(t *testing.T) {
	st, _, uid := engineTestDeps(t)
	sess := newTestSession(t, st, uid, ModeBenar, 100)
	fake := newFakeWA()
	// No replies → engine blocks in Wait(); we cancel after 30ms.
	fake.collector.onWait = func() { time.Sleep(30 * time.Millisecond) }
	eng := newTestEngine(t, sess, fake, st)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	_ = eng.Run(ctx)

	detail, _ := st.GetSessionDetail(context.Background(), uid, sess.ID)
	if detail.Session.Status != StatusStopped {
		t.Fatalf("status=%s, want stopped", detail.Session.Status)
	}
}

func TestEngineRegistersCollectorFails(t *testing.T) {
	st, _, uid := engineTestDeps(t)
	sess := newTestSession(t, st, uid, ModeBenar, 5)
	fake := newFakeWA()
	fake.regErr = errors.New("no client")
	eng := newTestEngine(t, sess, fake, st)
	if err := eng.Run(context.Background()); err == nil {
		t.Fatalf("expected error when collector cannot register")
	}
	detail, _ := st.GetSessionDetail(context.Background(), uid, sess.ID)
	if detail.Session.Status != StatusFailed {
		t.Fatalf("status=%s, want failed", detail.Session.Status)
	}
	if fake.collector.closed.Load() != 0 {
		t.Fatalf("collector never registered must not be closed")
	}
}

func TestEngineCelahLocationOncePerSession(t *testing.T) {
	st, _, uid := engineTestDeps(t)
	sess := newTestSession(t, st, uid, ModeCelah, 5)
	fake := newFakeWA("Silakan kirim titik lokasi", "Silakan share lokasi lagi")
	eng := newTestEngine(t, sess, fake, st)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = eng.Run(ctx)

	if fake.locs < 1 {
		t.Fatalf("first celah/titik_lokasi turn should send a real LocationMessage")
	}
	if fake.locs > 1 {
		t.Fatalf("celah location must fire at most once per session, got %d", fake.locs)
	}
}

// TestEngineTriggersCooldown proves the cooldown cadence fires. We lower the
// delay to zero but keep the cooldown slow enough to be visible; the test
// counts how many sleeps used a cooldown duration.
func TestEngineTriggersCooldown(t *testing.T) {
	st, _, uid := engineTestDeps(t)
	sess := newTestSession(t, st, uid, ModeBenar, 30)
	replies := make([]string, 6)
	for i := range replies {
		replies[i] = "Silakan isi nama lengkap" // never triggers IsDone
	}
	fake := newFakeWA(replies...)

	var sleeps []int
	h, err := newEngine(engineConfig{
		Store:     st,
		WA:        fake,
		Responder: &Responder{},
		Settings: model.HumanSettings{
			// Zero everywhere except the bits we are measuring.
			PreThinkMinMs: 0, PreThinkMaxMs: 0,
			TypingMsPerChar: 1, TypingMinMs: 1, TypingMaxMs: 1,
			DelayMinMs: 1, DelayMaxMs: 1,
			DistractionSpikePct: 0,
			CooldownEveryMin:    2, CooldownEveryMax: 2,
			CooldownMinMs: 500, CooldownMaxMs: 500, // visibly different from 1 ms
			QuietWindowMs: 1, ReplyTimeoutMs: 400,
		},
		AssetRoot: t.TempDir(), MediaRoot: t.TempDir(), Emitter: NopEmitter{},
	}, sess)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	// Replace humanizer inside Run by intercepting the sleeper via package-level
	// hook (tests only): we cannot access the humanizer inside Run() directly,
	// so instead we run under fast ctx and read the counts via a wrapper that
	// records any sleep >= 400 ms (only cooldown uses it here).
	origHuman := humanSleeperHook
	humanSleeperHook = func(ms int) { sleeps = append(sleeps, ms) }
	defer func() { humanSleeperHook = origHuman }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.Run(ctx)

	cooldowns := 0
	for _, ms := range sleeps {
		if ms >= 400 {
			cooldowns++
		}
	}
	if cooldowns == 0 {
		t.Fatalf("expected at least one cooldown sleep (>=400ms), got sleeps=%v", sleeps)
	}
}

// TestEngineEscalatesWhenKindUnknown drives the loop with replies the detector
// cannot classify and asserts the turns progress through the essential fields.
func TestEngineEscalatesWhenKindUnknown(t *testing.T) {
	st, _, uid := engineTestDeps(t)
	sess := newTestSession(t, st, uid, ModeBenar, 5)
	fake := newFakeWA("Halo kak", "Terima kasih", "Lanjut pak", "Detail tiket belum ada")
	eng := newTestEngine(t, sess, fake, st)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = eng.Run(ctx)

	detail, _ := st.GetSessionDetail(context.Background(), uid, sess.ID)
	kinds := make([]string, 0, len(detail.Turns))
	for _, tr := range detail.Turns {
		kinds = append(kinds, tr.QuestionKind)
	}
	if len(kinds) < 3 {
		t.Fatalf("expected >=3 audit turns, got %v", kinds)
	}
	// First three fallbacks in order (laporan, nama, tanggal).
	want := []string{string(KindIsiLaporan), string(KindNama), string(KindTanggal)}
	for i, w := range want {
		if kinds[i] != w {
			t.Fatalf("turn %d kind=%q, want %q (all=%v)", i, kinds[i], w, kinds)
		}
	}
}

// TestEngineStreamReplacedKeepsRunning: Connected=false + LoggedIn=true must
// be treated as "still connected", matching whatsmeow's StreamReplaced event
// (the session is auto-restored; only LoggedOut means dead). This is pure
// predicate testing to avoid racing the fast fake's send timing.
func TestEngineStreamReplacedKeepsRunning(t *testing.T) {
	fake := newFakeWA()
	fake.setDisconnectedOnly()
	eng := &Engine{cfg: engineConfig{WA: fake}}
	if !eng.isWAConnected() {
		t.Fatalf("Connected=false + LoggedIn=true must be connected from the engine's view")
	}
	fake.setLoggedOut()
	if eng.isWAConnected() {
		t.Fatalf("LoggedIn=false must fail")
	}
}

// TestEngineDoesNotWriteLivechatFile memastikan engine TIDAK lagi menulis
// livechat.xlsx ke disk saat sesi ditutup (Tahap 12). Ringkasan kini dibuat
// on-demand dari database.
func TestEngineDoesNotWriteLivechatFile(t *testing.T) {
	st, _, uid := engineTestDeps(t)
	sess := newTestSession(t, st, uid, ModeBenar, 5)
	fake := newFakeWA("Pengaduan Anda telah kami terima. Nomor tiket #123456")

	// Arahkan output file ke direktori sementara bila (secara tidak sengaja)
	// masih ada yang menulis, agar test bisa mendeteksinya tanpa menyentuh
	// data/users milik user sungguhan.
	dir := t.TempDir()
	t.Chdir(dir)

	cfg := engineConfig{
		Store:     st,
		WA:        fake,
		Responder: &Responder{},
		Settings:  fastSettings(),
		AssetRoot: filepath.Join(dir, "assets"),
		MediaRoot: filepath.Join(dir, "media"),
		Emitter:   NopEmitter{},
		// Exporter sengaja DIPASANG: engine harus tetap tidak memanggilnya.
		Exporter: NewExporter(),
	}
	eng, err := newEngine(cfg, sess)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := eng.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Status harus finished di database (bukti data tersimpan).
	detail, err := st.GetSessionDetail(context.Background(), uid, sess.ID)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Session.Status != StatusFinished {
		t.Fatalf("status=%s, want finished", detail.Session.Status)
	}

	// Tidak boleh ada file yang dibuat di direktori kerja.
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		t.Fatalf("engine menulis file yang tidak diharapkan: %s", path)
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
}

type fakeResolver struct {
	wa *fakeWA
}

func (r fakeResolver) ClientFor(int64, string) (WAClient, error) {
	return r.wa, nil
}

// TestManagerStartBatch verifies sequential loop for multiple repeatCount sessions.
func TestManagerStartBatch(t *testing.T) {
	st, _, uid := engineTestDeps(t)
	fake := newFakeWA(
		"Pengaduan Anda telah kami terima #991122",
		"Pengaduan Anda telah kami terima #991123",
	)

	mgr, err := NewManager(ManagerOptions{
		Store:     st,
		Assets:    NewAssetService(st, t.TempDir()),
		Responder: &Responder{},
		WA:        fakeResolver{wa: fake},
		Settings: func(context.Context, int64) (model.HumanSettings, bool) {
			s := fastSettings()
			s.DelayMinMs = 1
			s.DelayMaxMs = 1
			return s, true
		},
		MediaRoot: func(int64) string { return t.TempDir() },
		AssetRoot: func(int64) string { return t.TempDir() },
		Emitter:   NopEmitter{},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	in := sampleInput()
	in.Title = "Batch Test"
	firstSess, err := mgr.StartBatch(context.Background(), uid, in, 2)
	if err != nil {
		t.Fatalf("StartBatch: %v", err)
	}
	if !strings.Contains(firstSess.Title, "(1/2)") {
		t.Fatalf("session 1 title should contain (1/2), got %q", firstSess.Title)
	}

	// Wait for batch to finish (both sessions)
	timeout := time.After(5 * time.Second)
	for {
		if mgr.ActiveSessionID(uid) == 0 {
			break
		}
		select {
		case <-timeout:
			mgr.Stop(uid, 0)
			t.Fatalf("timed out waiting for batch sessions to complete")
		case <-time.After(50 * time.Millisecond):
		}
	}

	list, err := st.ListSessions(context.Background(), uid, 10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) < 2 {
		t.Fatalf("expected at least 2 sessions created in batch, got %d", len(list))
	}
}

// TestManagerStopBatch verifies that calling Stop during a batch cancels remaining queue.
func TestManagerStopBatch(t *testing.T) {
	st, _, uid := engineTestDeps(t)
	fake := newFakeWA() // blocks in Wait
	fake.collector.onWait = func() { time.Sleep(50 * time.Millisecond) }

	mgr, err := NewManager(ManagerOptions{
		Store:     st,
		Assets:    NewAssetService(st, t.TempDir()),
		Responder: &Responder{},
		WA:        fakeResolver{wa: fake},
		Settings: func(context.Context, int64) (model.HumanSettings, bool) {
			return fastSettings(), true
		},
		MediaRoot: func(int64) string { return t.TempDir() },
		AssetRoot: func(int64) string { return t.TempDir() },
		Emitter:   NopEmitter{},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	in := sampleInput()
	sess, err := mgr.StartBatch(context.Background(), uid, in, 5)
	if err != nil {
		t.Fatalf("StartBatch: %v", err)
	}

	// Allow session 1 to start, then stop
	time.Sleep(30 * time.Millisecond)
	stopped := mgr.Stop(uid, sess.ID)
	if !stopped {
		t.Fatalf("expected Stop to succeed")
	}

	time.Sleep(100 * time.Millisecond)
	list, err := st.ListSessions(context.Background(), uid, 10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	// Only session 1 should have been created, sessions 2-5 never started
	if len(list) != 1 {
		t.Fatalf("batch cancellation should prevent subsequent sessions, got %d sessions", len(list))
	}
	if list[0].Status != StatusStopped {
		t.Fatalf("session 1 should be stopped, got %s", list[0].Status)
	}
}

// TestEngineHandlesBotCancellation verifies that when a bot cancels a session,
// it is marked as failed/cancelled rather than finished with "diterima".
func TestEngineHandlesBotCancellation(t *testing.T) {
	st, _, uid := engineTestDeps(t)
	fake := newFakeWA()
	fake.collector.replies = []string{
		"*Baik*, pengaduan sudah kami batalkan ya *.* Pengaduan diterima dan diverifikasi.",
	}

	mgr, err := NewManager(ManagerOptions{
		Store:     st,
		Assets:    NewAssetService(st, t.TempDir()),
		Responder: &Responder{},
		WA:        fakeResolver{wa: fake},
		Settings: func(context.Context, int64) (model.HumanSettings, bool) {
			return fastSettings(), true
		},
		MediaRoot: func(int64) string { return t.TempDir() },
		AssetRoot: func(int64) string { return t.TempDir() },
		Emitter:   NopEmitter{},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	in := sampleInput()
	sess, err := mgr.StartSession(context.Background(), uid, in)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Wait for loop to process the cancellation reply
	for i := 0; i < 60; i++ {
		time.Sleep(50 * time.Millisecond)
		det, err := st.GetSessionDetail(context.Background(), uid, sess.ID)
		if err == nil && det.Session.Status != StatusRunning {
			sess = det.Session
			break
		}
	}

	if sess.Status == StatusFinished {
		t.Fatalf("cancelled session must NOT be finished, got status=%s endReason=%s", sess.Status, sess.EndReason)
	}
	if !strings.Contains(sess.EndReason, "dibatalkan") {
		t.Fatalf("expected endReason to mention dibatalkan, got %q", sess.EndReason)
	}
}
