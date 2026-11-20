package livechat

import (
	"context"
	"database/sql"
	"errors"
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
	regErr    error
	registers int
}

func newFakeWA(replies ...string) *fakeWA {
	return &fakeWA{collector: &fakeCollector{replies: replies}, connected: true}
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
	return model.WAStatus{Connected: f.connected, LoggedIn: f.connected}
}
func (f *fakeWA) setConnected(v bool) {
	f.mu.Lock()
	f.connected = v
	f.mu.Unlock()
}

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
		Emitter:   nopEngineEmitter{},
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
	if fake.texts[1] != sess.NamaPengadu {
		t.Fatalf("second send should be nama, got %q", fake.texts[1])
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
	fake.collector.onWait = func() { fake.setConnected(false) }
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
