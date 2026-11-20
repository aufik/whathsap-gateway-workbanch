package livechat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"whatshap-gateway/internal/model"
)

// tickInterval is the disconnect poll cadence decided with the user: 3 s.
const tickInterval = 3 * time.Second

// maxMediaPreviewBytes caps what we keep on disk for a received bot attachment.
const maxMediaPreviewBytes = 8 << 20

// engineConfig is everything the engine needs beyond the session itself.
type engineConfig struct {
	Store     *Store
	WA        WAClient
	Responder *Responder
	Settings  model.HumanSettings
	// MediaRoot is data/users/{id}/livechat_media; empty disables persistence.
	MediaRoot string
	// AssetRoot is data/users/{id}/livechat_assets (source of uploads).
	AssetRoot string
	// Emitter is called with session_id on every meaningful phase change.
	Emitter engineEmitter
	// AI is wired into the responder by the manager; not needed here.
}

// Engine runs one Live Chat session to completion. One engine per session; use
// the Manager to run several in parallel.
type Engine struct {
	cfg     engineConfig
	session Session
	col     ReplyCollector
	cancel  context.CancelFunc

	turnsMu  sync.Mutex
	turns    int
	excerpt  []string
	outside  bool
	autoStop bool

	stopOnce sync.Once
	closed   bool
	ticket   string
}

// newEngine binds a session to its config and returns a ready-to-run engine.
func newEngine(cfg engineConfig, sess Session) (*Engine, error) {
	if cfg.Store == nil || cfg.WA == nil {
		return nil, errors.New("engine livechat butuh store dan WA client")
	}
	if sess.CategoryMode != ModeBenar && sess.CategoryMode != ModeTypo && sess.CategoryMode != ModeNgasal && sess.CategoryMode != ModeCelah {
		return nil, fmt.Errorf("mode kategori tidak dikenal: %q", sess.CategoryMode)
	}
	return &Engine{cfg: cfg, session: sess}, nil
}

// Run blocks until the session reaches a terminal status. It is safe to call
// once; a second call returns an error.
func (e *Engine) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	e.cancel = cancel
	defer cancel()

	col, err := e.cfg.WA.RegisterCollector(e.session.BotPhone)
	if err != nil {
		_ = e.closeWith(StatusFailed, "gagal mendaftarkan kolektor: "+err.Error(), "")
		return err
	}
	e.col = col
	defer col.Close()

	e.emit("livechat_status", map[string]any{"sessionId": e.session.ID, "status": string(StatusRunning)})
	_ = e.cfg.Store.MarkStarted(ctx, e.session.UserID, e.session.ID)

	human := newHumanizer(e.cfg.Settings)
	if err := e.sendOpening(ctx, human); err != nil {
		_ = e.closeWith(StatusFailed, "gagal mengirim pesan pembuka: "+err.Error(), "")
		return err
	}

	breakAt := human.nextBreakGap()
	turnsSince := 0
	for {
		select {
		case <-ctx.Done():
			return e.handleStop("dihentikan oleh pengguna")
		default:
		}

		if e.turns >= e.session.MaxTurns {
			return e.handleTimeout("batas giliran tercapai")
		}
		if !e.isWAConnected() {
			return e.handleDisconnect()
		}

		quiet := time.Duration(e.cfg.Settings.QuietWindowMs) * time.Millisecond
		hard := time.Duration(e.cfg.Settings.ReplyTimeoutMs) * time.Millisecond
		if quiet <= 0 {
			quiet = 5500 * time.Millisecond
		}
		if hard <= 0 {
			hard = 35 * time.Second
		}
		bubbles, werr := e.col.Wait(ctx, quiet, hard)

		// Poll every tickInterval while waiting so ctx cancel and disconnect
		// detection remain responsive even when the collect round is long.
		waitStarted := time.Now()
		for len(bubbles) == 0 && werr == nil && time.Since(waitStarted) < hard {
			select {
			case <-ctx.Done():
				return e.handleStop("dihentikan oleh pengguna")
			case <-time.After(tickInterval):
				if !e.isWAConnected() {
					return e.handleDisconnect()
				}
			}
			remain := hard - time.Since(waitStarted)
			if remain <= 0 {
				break
			}
			bubbles, werr = e.col.Wait(ctx, quiet, remain)
		}
		if werr != nil && len(bubbles) == 0 {
			if errors.Is(werr, context.Canceled) {
				return e.handleStop("dihentikan oleh pengguna")
			}
			return e.handleTimeout("tidak ada balasan bot: " + werr.Error())
		}

		if len(bubbles) == 0 {
			return e.handleTimeout("tidak ada balasan bot dalam batas waktu")
		}

		if done, err := e.consumeTurn(ctx, bubbles, human); err != nil {
			return e.handleFailure(err)
		} else if done {
			return nil
		}

		turnsSince++
		if turnsSince >= breakAt {
			turnsSince = 0
			breakAt = human.nextBreakGap()
		} else {
			_, derr := human.interTurnDelay(ctx, 0, breakAt)
			if derr != nil {
				return e.handleStop("dihentikan oleh pengguna")
			}
		}
	}
}

// consumeTurn persists the received bubbles, composes the reply, sends it, and
// reports whether the bot announced "selesai".
func (e *Engine) consumeTurn(ctx context.Context, bubbles []CollectedBubble, human *humanizer) (bool, error) {
	// 1. Persist & excerpt every received bubble.
	var lastText string
	for _, b := range bubbles {
		msgType := MsgText
		mediaJSON := ""
		if len(b.MediaBytes) > 0 || b.MediaName != "" {
			msgType = MsgImage
			mediaJSON = e.persistIncomingMedia(b)
		}
		latencyMs := 0
		if _, err := e.cfg.Store.InsertMessage(ctx, e.session.UserID, Message{
			SessionID: e.session.ID,
			TurnNo:    e.turns + 1,
			Direction: DirectionReceived,
			Body:      b.Text,
			MediaJSON: mediaJSON,
			MsgType:   msgType,
			LatencyMs: &latencyMs,
		}); err != nil {
			return false, err
		}
		e.excerpt = append(e.excerpt, b.Text)
		lastText = b.Text
		e.emit("livechat_message", map[string]any{
			"sessionId": e.session.ID,
			"direction": "received",
			"msgType":   msgType,
			"body":      b.Text,
			"mediaJson": mediaJSON,
		})
	}

	// 2. Detect done first so we never answer the "selesai" line.
	if done, reason := IsDone(lastText); done {
		// Ticket can be split across bubbles: try the last bubble first, then
		// the full excerpt. The persisted header gets it via closeWith argument.
		ticket := ExtractTicket(lastText)
		if ticket == "" {
			ticket = ExtractTicket(e.combineExcerpt())
		}
		if ticket != "" {
			e.ticket = ticket
		}
		return true, e.closeWith(StatusFinished, reason, e.ticket)
	}

	// 3. Compose the answer for the latest bubble.
	kind := DetectQuestionKind(lastText)
	assets, _ := e.cfg.Store.ListAssets(ctx, e.session.UserID)
	assets = filterOwned(assets, e.session.AssetIDs)
	rng := rand.New(rand.NewSource(time.Now().UnixNano() ^ int64(e.session.ID)))
	answer := e.cfg.Responder.ComposeForKind(ctx, kind, AnswerInput{
		Session:     e.session,
		Assets:      assets,
		Random:      rng,
		OutsideUsed: e.outside,
	})
	if answer.DidUseOutsideLoc {
		e.outside = true
	}

	e.turns++
	sentMediaJSON, err := e.sendAnswer(ctx, answer, human)
	if err != nil {
		return false, err
	}

	// 4. Persist outgoing + turn log. A failed turn insert must not crash the
	//    whole session; we surface it in the note.
	var note string
	if _, err := e.cfg.Store.InsertTurn(ctx, e.session.UserID, Turn{
		SessionID:     e.session.ID,
		TurnNo:        e.turns,
		BotQuestion:   lastText,
		QuestionKind:  string(answer.Kind),
		OurAnswer:     renderTurnAnswer(answer),
		SentMediaJSON: sentMediaJSON,
		CategoryMode:  string(e.session.CategoryMode),
		MatchStatus:   answer.MatchStatus,
		Note:          answer.Note,
	}); err != nil {
		note = "gagal menyimpan turn: " + err.Error()
	}
	_ = note
	e.emit("livechat_turn", map[string]any{
		"sessionId":   e.session.ID,
		"turnNo":      e.turns,
		"kind":        string(answer.Kind),
		"botQuestion": lastText,
		"ourAnswer":   renderTurnAnswer(answer),
		"matchStatus": answer.MatchStatus,
	})

	// 5. Advance turn counter in the header (best-effort).
	ticket := e.ticket
	_ = e.cfg.Store.UpdateSessionProgress(ctx, e.session.UserID, e.session.ID, e.turns, ticket)
	return false, nil
}

func (e *Engine) sendAnswer(ctx context.Context, a AnswerResult, human *humanizer) (string, error) {
	if a.SendLoc != nil {
		lat, lng := a.SendLoc.Lat, a.SendLoc.Lng
		_, err := human.typedSend(ctx, func() error {
			return e.cfg.WA.SendLocation(ctx, e.session.BotPhone, lat, lng, a.SendLoc.Name, a.SendLoc.Address)
		}, "share lokasi")
		if err != nil {
			return "", err
		}
		media := fmt.Sprintf(`[{"kind":"location","file":"","mimetype":"","size":0,"caption":%q}]`, a.Text)
		return media, e.persistOutgoing(ctx, MsgLocation, "", media)
	}
	if a.MediaAsset != nil {
		a.MediaAsset = ensureOwned(a.MediaAsset, e.session.AssetIDs)
	}
	if a.MediaAsset != nil {
		// Verify bytes before sending (tamper check per plan §6.5).
		path := filepath.Join(e.assetRoot(), a.MediaAsset.StoredPath)
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return "", fmt.Errorf("aset %d tidak tersedia: %w", a.MediaAsset.ID, rerr)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != a.MediaAsset.SHA256 {
			return "", errors.New("checksum aset berubah; tolong unggah ulang")
		}
		var sendErr error
		_, sErr := human.typedSend(ctx, func() error {
			if a.MediaAsset.Kind == AssetImage {
				sendErr = e.cfg.WA.SendImage(ctx, e.session.BotPhone, data, a.MediaAsset.Mimetype, a.Text)
			} else {
				sendErr = e.cfg.WA.SendDocument(ctx, e.session.BotPhone, data, a.MediaAsset.FileName, a.MediaAsset.Mimetype, a.Text)
			}
			return sendErr
		}, a.Text)
		if sErr != nil {
			return "", sErr
		}
		kind := "document"
		if a.MediaAsset.Kind == AssetImage {
			kind = "image"
		}
		media := fmt.Sprintf(`[{"kind":%q,"file":%q,"mimetype":%q,"size":%d,"caption":%q}]`,
			kind, a.MediaAsset.FileName, a.MediaAsset.Mimetype, a.MediaAsset.SizeBytes, a.Text)
		return media, e.persistOutgoing(ctx, MsgType(kind), a.Text, media)
	}
	// Plain text.
	_, err := human.typedSend(ctx, func() error {
		return e.cfg.WA.SendText(ctx, e.session.BotPhone, a.Text)
	}, a.Text)
	if err != nil {
		return "", err
	}
	return "", e.persistOutgoing(ctx, MsgText, a.Text, "")
}

func (e *Engine) persistOutgoing(ctx context.Context, kind MsgType, body, mediaJSON string) error {
	if _, err := e.cfg.Store.InsertMessage(ctx, e.session.UserID, Message{
		SessionID: e.session.ID,
		TurnNo:    e.turns,
		Direction: DirectionSent,
		Body:      body,
		MediaJSON: mediaJSON,
		MsgType:   kind,
	}); err != nil {
		return err
	}
	e.emit("livechat_message", map[string]any{
		"sessionId": e.session.ID,
		"direction": "sent",
		"msgType":   kind,
		"body":      body,
		"mediaJson": mediaJSON,
	})
	return nil
}

// sendOpening pushes the first bubble. It only uses text; no media this early
// so the bot's greeting logic stays simple.
func (e *Engine) sendOpening(ctx context.Context, human *humanizer) error {
	if err := human.preThink(ctx); err != nil {
		return err
	}
	turn := 0
	if _, err := e.cfg.Store.InsertMessage(ctx, e.session.UserID, Message{
		SessionID: e.session.ID,
		TurnNo:    turn,
		Direction: DirectionSent,
		Body:      openingLine,
		MsgType:   MsgText,
	}); err != nil {
		return err
	}
	e.excerpt = append(e.excerpt, openingLine)
	_, sendErr := human.typedSend(ctx, func() error {
		return e.cfg.WA.SendText(ctx, e.session.BotPhone, openingLine)
	}, openingLine)
	return sendErr
}

func (e *Engine) handleStop(reason string) error {
	_ = e.closeWith(StatusStopped, reason, e.ticket)
	return nil
}

func (e *Engine) handleTimeout(reason string) error {
	_ = e.closeWith(StatusTimeout, reason, e.ticket)
	return nil
}

func (e *Engine) handleDisconnect() error {
	_ = e.closeWith(StatusFailed, "koneksi WhatsApp terputus", e.ticket)
	return nil
}

func (e *Engine) handleFailure(err error) error {
	_ = e.closeWith(StatusFailed, err.Error(), e.ticket)
	return err
}

// closeWith persists the final status + last_chat_excerpt. It is idempotent
// (FinishSession refuses a second close) but always emits the status event so
// the UI converges eventually.
func (e *Engine) closeWith(status Status, reason, ticket string) error {
	excerpt := LastNExcerpt(e.excerpt, 3)
	err := e.cfg.Store.FinishSession(context.Background(), e.session.UserID, e.session.ID, status, reason, ticket, excerpt)
	_ = e.cfg.Store.UpdateSessionProgress(context.Background(), e.session.UserID, e.session.ID, e.turns, ticket)
	e.emit("livechat_status", map[string]any{
		"sessionId": e.session.ID,
		"status":    string(status),
		"reason":    reason,
		"ticket":    ticket,
	})
	return err
}

func (e *Engine) isWAConnected() bool {
	st := e.cfg.WA.Status()
	return st.Connected && st.LoggedIn
}

func (e *Engine) emit(eventType string, payload any) {
	if e.cfg.Emitter == nil {
		return
	}
	// The adapter needs the owning user id; stash it inside the payload so the
	// engine never imports the web broker directly. Existing keys (sessionId,
	// status, ...) are preserved by mutating the map in place.
	if m, ok := payload.(map[string]any); ok {
		m["userId"] = e.session.UserID
	}
	e.cfg.Emitter.Emit(e.session.ID, eventType, payload)
}

// persistIncomingMedia writes the bot's attachment to data/users/{id}/livechat_media
// and returns the JSON stored in messages.media_json. Failures are non-fatal:
// they degrade to a name-only note.
func (e *Engine) persistIncomingMedia(b CollectedBubble) string {
	if len(b.MediaBytes) == 0 {
		if b.MediaName == "" {
			return ""
		}
		return fmt.Sprintf(`[{"kind":"image","file":%q,"mimetype":"","size":0,"caption":%q}]`, safeFileBase(b.MediaName), b.Text)
	}
	if len(b.MediaBytes) > maxMediaPreviewBytes || e.cfg.MediaRoot == "" {
		return fmt.Sprintf(`[{"kind":"image","file":%q,"mimetype":"","size":%d,"caption":%q}]`, safeFileBase(b.MediaName), len(b.MediaBytes), b.Text)
	}
	if err := os.MkdirAll(e.cfg.MediaRoot, 0755); err != nil {
		return ""
	}
	name := safeFileBase(strings.TrimSuffix(b.ID, "/")) + "-" + safeFileBase(b.MediaName)
	ext := b.MediaExt
	if ext == "" {
		ext = ".bin"
	}
	if !strings.HasSuffix(name, ext) {
		name = name + ext
	}
	if err := os.WriteFile(filepath.Join(e.cfg.MediaRoot, name), b.MediaBytes, 0644); err != nil {
		return ""
	}
	mimetype := ""
	if b.MediaExt == ".png" {
		mimetype = "image/png"
	} else if b.MediaExt == ".jpg" || b.MediaExt == ".jpeg" {
		mimetype = "image/jpeg"
	}
	return fmt.Sprintf(`[{"kind":"image","file":%q,"mimetype":%q,"size":%d,"caption":%q}]`,
		name, mimetype, len(b.MediaBytes), b.Text)
}

func (e *Engine) assetRoot() string {
	if e.cfg.AssetRoot != "" {
		return e.cfg.AssetRoot
	}
	return UserAssetsRoot(e.session.UserID)
}

func (e *Engine) combineExcerpt() string { return strings.Join(e.excerpt, "\n") }

func renderTurnAnswer(a AnswerResult) string {
	switch {
	case a.SendLoc != nil:
		return fmt.Sprintf("[Koordinat] %.6f, %.6f", a.SendLoc.Lat, a.SendLoc.Lng)
	case a.MediaAsset != nil:
		kind := "Berkas"
		if a.MediaAsset.Kind == AssetImage {
			kind = "Foto"
		}
		if a.Text != "" {
			return fmt.Sprintf("[%s] %s", kind, a.Text)
		}
		return "[" + kind + "]"
	default:
		return a.Text
	}
}

func filterOwned(assets []Asset, ids []int64) []Asset {
	if len(ids) == 0 || len(assets) == 0 {
		return assets
	}
	want := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := make([]Asset, 0, len(ids))
	for _, a := range assets {
		if _, ok := want[a.ID]; ok {
			out = append(out, a)
		}
	}
	return out
}

func ensureOwned(a *Asset, ids []int64) *Asset {
	for _, id := range ids {
		if id == a.ID {
			return a
		}
	}
	return nil
}
