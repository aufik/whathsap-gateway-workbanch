package wa

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	qrcode "github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/model"
)

func init() {
	store.SetOSInfo("Linux", [3]uint32{6, 8, 0})
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()
}

var ErrCircuitBreaker = errors.New("circuit breaker triggered")

type TelemetryHook func(phase, label, presence string, durationMs int, rowNo int, extra func(*model.HumanTelemetry))

type collectedBubble struct {
	id     types.MessageID
	chat   types.JID
	sender types.JID
	text   string
}

type replyCollector struct {
	botPhone string
	botLID   string
	sentAt   time.Time
	ch       chan collectedBubble
}

type Client struct {
	mu        sync.RWMutex
	dbPath    string
	container *sqlstore.Container
	cli       *whatsmeow.Client
	status    model.WAStatus
	collector *replyCollector
	onStatus  func(model.WAStatus)
}

func NewClient(ctx context.Context, dbPath string, onStatus func(model.WAStatus)) (*Client, error) {
	dsn := fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000", dbPath)
	container, err := sqlstore.New(ctx, "sqlite3", dsn, waLog.Noop)
	if err != nil {
		return nil, fmt.Errorf("init sqlite session store: %w", err)
	}

	c := &Client{
		dbPath:    dbPath,
		container: container,
		onStatus:  onStatus,
		status: model.WAStatus{
			UpdatedAt: time.Now().UnixMilli(),
		},
	}

	if err := c.initDeviceClientLocked(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) initDeviceClientLocked(ctx context.Context) error {
	dev, err := c.container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("get wa device: %w", err)
	}
	if c.cli != nil {
		c.cli.Disconnect()
	}
	cli := whatsmeow.NewClient(dev, waLog.Noop)
	cli.AddEventHandler(c.handleEvent)
	c.cli = cli

	if cli.Store.ID != nil {
		c.status.LoggedIn = true
		c.status.AccountJID = cli.Store.ID.ToNonAD().String()
		c.status.PushName = cli.Store.PushName
	}
	return nil
}

func (c *Client) AutoConnectIfLoggedIn(ctx context.Context) {
	c.mu.RLock()
	hasSession := c.cli != nil && c.cli.Store.ID != nil
	c.mu.RUnlock()
	if hasSession {
		_ = c.Connect(ctx)
	}
}

func (c *Client) Status() model.WAStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

func (c *Client) updateStatus(mut func(*model.WAStatus)) {
	c.mu.Lock()
	mut(&c.status)
	c.status.UpdatedAt = time.Now().UnixMilli()
	snap := c.status
	cb := c.onStatus
	c.mu.Unlock()
	if cb != nil {
		cb(snap)
	}
}

func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	cli := c.cli
	if cli == nil {
		if err := c.initDeviceClientLocked(ctx); err != nil {
			c.mu.Unlock()
			return err
		}
		cli = c.cli
	}
	if cli.IsConnected() {
		c.mu.Unlock()
		return nil
	}

	needQR := cli.Store.ID == nil
	var qrChan <-chan whatsmeow.QRChannelItem
	if needQR {
		ch, err := cli.GetQRChannel(context.Background())
		if err == nil {
			qrChan = ch
		}
	}
	c.mu.Unlock()

	if qrChan != nil {
		go c.consumeQRChannel(qrChan)
	}

	if err := cli.Connect(); err != nil {
		c.updateStatus(func(s *model.WAStatus) {
			s.LastError = err.Error()
		})
		return err
	}
	return nil
}

func (c *Client) consumeQRChannel(qrChan <-chan whatsmeow.QRChannelItem) {
	for item := range qrChan {
		switch item.Event {
		case "code":
			png, err := qrcode.Encode(item.Code, qrcode.Medium, 280)
			dataURL := ""
			if err == nil {
				dataURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
			}
			c.updateStatus(func(s *model.WAStatus) {
				s.QRDataURL = dataURL
				s.LastError = ""
			})
		case "success":
			c.updateStatus(func(s *model.WAStatus) {
				s.LoggedIn = true
				s.QRDataURL = ""
				s.PairingCode = ""
				s.LastError = ""
				if c.cli != nil && c.cli.Store.ID != nil {
					s.AccountJID = c.cli.Store.ID.ToNonAD().String()
					s.PushName = c.cli.Store.PushName
				}
			})
		case "timeout", "err-client-outdated", "err-scanned-without-multidevice":
			c.updateStatus(func(s *model.WAStatus) {
				s.QRDataURL = ""
				s.PairingCode = ""
				s.LastError = "Sesi QR berakhir (" + item.Event + "). Silakan klik Hubungkan ulang."
			})
		}
	}
}

func (c *Client) RequestPairingCode(ctx context.Context, phone string) (string, error) {
	cleanPhone := config.NormalizePhone(phone)
	if len(cleanPhone) < 9 {
		return "", errors.New("nomor WhatsApp penguji tidak valid (gunakan format 628xxx)")
	}
	if err := c.Connect(ctx); err != nil {
		return "", err
	}

	// Give login websocket a moment to establish QR handshake per whatsmeow docs
	select {
	case <-time.After(1400 * time.Millisecond):
	case <-ctx.Done():
		return "", ctx.Err()
	}

	c.mu.RLock()
	cli := c.cli
	c.mu.RUnlock()
	if cli == nil {
		return "", errors.New("client belum siap")
	}

	code, err := cli.PairPhone(ctx, cleanPhone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		c.updateStatus(func(s *model.WAStatus) {
			s.LastError = fmt.Sprintf("Gagal membuat pairing code: %v", err)
		})
		return "", err
	}
	c.updateStatus(func(s *model.WAStatus) {
		s.PairingCode = code
		s.LastError = ""
	})
	return code, nil
}

func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	cli := c.cli
	c.mu.Unlock()

	if cli != nil {
		if err := cli.Logout(ctx); err != nil {
			cli.Disconnect()
			if cli.Store != nil {
				_ = cli.Store.Delete(ctx)
			}
		}
	}

	c.mu.Lock()
	_ = c.initDeviceClientLocked(ctx)
	c.mu.Unlock()

	c.updateStatus(func(s *model.WAStatus) {
		s.Connected = false
		s.LoggedIn = false
		s.AccountJID = ""
		s.PushName = ""
		s.QRDataURL = ""
		s.PairingCode = ""
		s.LastError = ""
	})
	return nil
}

func (c *Client) Close() {
	c.mu.Lock()
	cli := c.cli
	container := c.container
	c.mu.Unlock()

	if cli != nil {
		if cli.IsConnected() {
			_ = cli.SendPresence(context.Background(), types.PresenceUnavailable)
		}
		cli.Disconnect()
	}
	if container != nil {
		_ = container.Close()
	}
}

func (c *Client) SetPresenceAvailable(ctx context.Context, available bool) {
	c.mu.RLock()
	cli := c.cli
	c.mu.RUnlock()
	if cli == nil || !cli.IsConnected() {
		return
	}
	if available {
		_ = cli.SendPresence(ctx, types.PresenceAvailable)
	} else {
		_ = cli.SendPresence(ctx, types.PresenceUnavailable)
	}
}

func (c *Client) AskBotHumanized(
	ctx context.Context,
	rowNo int,
	botPhone string,
	questionText string,
	settings model.HumanSettings,
	hook TelemetryHook,
) (string, error) {
	cleanBot := config.NormalizePhone(botPhone)
	if len(cleanBot) < 8 {
		return "", errors.New("nomor WhatsApp Bot target belum diisi atau tidak valid")
	}

	c.mu.RLock()
	cli := c.cli
	c.mu.RUnlock()
	if cli == nil || !cli.IsConnected() || cli.Store.ID == nil {
		return "", fmt.Errorf("%w: WhatsApp penguji belum terhubung/login", ErrCircuitBreaker)
	}

	targetJID := types.NewJID(cleanBot, types.DefaultUserServer)
	var botLIDUser string
	if cli.Store.LIDs != nil {
		if lid, err := cli.Store.LIDs.GetLIDForPN(ctx, targetJID); err == nil && lid.User != "" {
			botLIDUser = lid.User
		}
	}

	// 1. Presence Available
	_ = cli.SendPresence(ctx, types.PresenceAvailable)

	// 2. Proportional Human Typing Speed Simulation
	charCount := len([]rune(questionText))
	typingMs := computeTypingDurationMs(charCount, settings)
	if hook != nil {
		hook(
			"typing",
			fmt.Sprintf("Baris #%d: Mengetik %d huruf (%.1f dtk, ~%d ms/huruf)...", rowNo, charCount, float64(typingMs)/1000, maxInt(15, typingMs/maxInt(1, charCount))),
			"composing",
			typingMs,
			rowNo,
			func(t *model.HumanTelemetry) {
				t.LastTypingChars = charCount
				t.LastTypingDurationMs = typingMs
			},
		)
	}

	if err := simulateTyping(ctx, cli, targetJID, typingMs); err != nil {
		return "", err
	}

	// 3. Register Collector BEFORE Sending Message (eliminates fast-reply race condition)
	coll := &replyCollector{
		botPhone: cleanBot,
		botLID:   botLIDUser,
		sentAt:   time.Now(),
		ch:       make(chan collectedBubble, 32),
	}
	c.mu.Lock()
	c.collector = coll
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.collector == coll {
			c.collector = nil
		}
		c.mu.Unlock()
	}()

	// 4. Send Question Message
	_, err := cli.SendMessage(ctx, targetJID, &waE2E.Message{
		Conversation: proto.String(questionText),
	})
	_ = cli.SendChatPresence(ctx, targetJID, types.ChatPresencePaused, types.ChatPresenceMediaText)
	if err != nil {
		return "", fmt.Errorf("%w: gagal mengirim pesan ke WA (%v)", ErrCircuitBreaker, err)
	}

	if hook != nil {
		hook(
			"waiting_reply",
			fmt.Sprintf("Baris #%d: Pesan terkirim, menunggu balasan Bot (batas %d dtk)...", rowNo, settings.ReplyTimeoutMs/1000),
			"available",
			settings.ReplyTimeoutMs,
			rowNo,
			nil,
		)
	}

	// 5. Quiet-Window Multi-Bubble Collection
	bubbles, err := collectBotBubbles(ctx, coll, rowNo, settings.QuietWindowMs, settings.ReplyTimeoutMs, hook)
	if err != nil {
		return "", err
	}

	combinedReply := formatCollectedBubbles(bubbles)

	// 6. Blue-Tick Read Receipt (MarkRead) + Human Reading Delay
	if len(bubbles) > 0 {
		readMs := computeReadingDurationMs(len([]rune(combinedReply)), settings)
		if hook != nil {
			hook(
				"mark_read_reading",
				fmt.Sprintf("Baris #%d: Mengirim centang biru (MarkRead) & membaca %d gelembung balasan (%.1f dtk)...", rowNo, len(bubbles), float64(readMs)/1000),
				"available",
				readMs,
				rowNo,
				func(t *model.HumanTelemetry) {
					t.LastReadDurationMs = readMs
				},
			)
		}

		// Brief natural eye-focus delay (~700ms) before marking read, then finish reading
		focusWait := minInt(750, readMs/2)
		if err := sleepCtx(ctx, time.Duration(focusWait)*time.Millisecond); err != nil {
			return combinedReply, err
		}
		markBubblesRead(ctx, cli, bubbles)
		if rem := readMs - focusWait; rem > 0 {
			if err := sleepCtx(ctx, time.Duration(rem)*time.Millisecond); err != nil {
				return combinedReply, err
			}
		}
	}

	return combinedReply, nil
}

func simulateTyping(ctx context.Context, cli *whatsmeow.Client, targetJID types.JID, totalMs int) error {
	_ = cli.SendChatPresence(ctx, targetJID, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	if totalMs <= 5200 {
		return sleepCtx(ctx, time.Duration(totalMs)*time.Millisecond)
	}

	// Long sentence: type first ~60%, pause briefly (0.7s thinking), then resume typing
	firstHalf := (totalMs * 6) / 10
	pauseMs := 700
	secondHalf := maxInt(800, totalMs-firstHalf-pauseMs)

	if err := sleepCtx(ctx, time.Duration(firstHalf)*time.Millisecond); err != nil {
		return err
	}
	_ = cli.SendChatPresence(ctx, targetJID, types.ChatPresencePaused, types.ChatPresenceMediaText)
	if err := sleepCtx(ctx, time.Duration(pauseMs)*time.Millisecond); err != nil {
		return err
	}
	_ = cli.SendChatPresence(ctx, targetJID, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	return sleepCtx(ctx, time.Duration(secondHalf)*time.Millisecond)
}

func collectBotBubbles(
	ctx context.Context,
	coll *replyCollector,
	rowNo int,
	quietWindowMs int,
	replyTimeoutMs int,
	hook TelemetryHook,
) ([]collectedBubble, error) {
	hardTimer := time.NewTimer(time.Duration(replyTimeoutMs) * time.Millisecond)
	defer hardTimer.Stop()

	var quietTimer *time.Timer
	var quietCh <-chan time.Time
	defer func() {
		if quietTimer != nil {
			quietTimer.Stop()
		}
	}()

	var bubbles []collectedBubble
	for {
		select {
		case <-ctx.Done():
			return bubbles, ctx.Err()
		case <-hardTimer.C:
			return bubbles, nil
		case <-quietCh:
			return bubbles, nil
		case b := <-coll.ch:
			bubbles = append(bubbles, b)
			if quietTimer == nil {
				quietTimer = time.NewTimer(time.Duration(quietWindowMs) * time.Millisecond)
				quietCh = quietTimer.C
			} else {
				if !quietTimer.Stop() {
					select {
					case <-quietTimer.C:
					default:
					}
				}
				quietTimer.Reset(time.Duration(quietWindowMs) * time.Millisecond)
			}
			if hook != nil {
				hook(
					"waiting_reply",
					fmt.Sprintf("Baris #%d: Gelembung balasan #%d diterima, pantau lanjutan %.1f dtk...", rowNo, len(bubbles), float64(quietWindowMs)/1000),
					"available",
					quietWindowMs,
					rowNo,
					nil,
				)
			}
		}
	}
}

func markBubblesRead(ctx context.Context, cli *whatsmeow.Client, bubbles []collectedBubble) {
	if len(bubbles) == 0 {
		return
	}
	ids := make([]types.MessageID, 0, len(bubbles))
	chatJID := bubbles[0].chat
	senderJID := bubbles[0].sender
	for _, b := range bubbles {
		if b.id != "" {
			ids = append(ids, b.id)
		}
	}
	if len(ids) > 0 {
		_ = cli.MarkRead(ctx, ids, time.Now(), chatJID, senderJID)
	}
}

func (c *Client) handleEvent(rawEvt any) {
	switch evt := rawEvt.(type) {
	case *events.Connected:
		c.updateStatus(func(s *model.WAStatus) {
			s.Connected = true
			s.LoggedIn = true
			s.QRDataURL = ""
			s.PairingCode = ""
			s.LastError = ""
			if c.cli != nil && c.cli.Store.ID != nil {
				s.AccountJID = c.cli.Store.ID.ToNonAD().String()
				s.PushName = c.cli.Store.PushName
			}
		})
	case *events.Disconnected:
		c.updateStatus(func(s *model.WAStatus) {
			s.Connected = false
		})
	case *events.LoggedOut:
		c.updateStatus(func(s *model.WAStatus) {
			s.Connected = false
			s.LoggedIn = false
			s.AccountJID = ""
			s.PushName = ""
			s.LastError = "Perangkat telah logout dari WhatsApp."
		})
	case *events.StreamReplaced:
		c.updateStatus(func(s *model.WAStatus) {
			s.Connected = false
			s.LastError = "Sesi digantikan oleh koneksi lain (StreamReplaced)."
		})
	case *events.Message:
		c.handleIncomingMessage(evt)
	}
}

func (c *Client) handleIncomingMessage(evt *events.Message) {
	if evt == nil || evt.Info.IsFromMe || evt.Info.IsGroup || evt.Info.Chat == types.StatusBroadcastJID {
		return
	}

	c.mu.RLock()
	coll := c.collector
	cli := c.cli
	c.mu.RUnlock()
	if coll == nil {
		return
	}

	if !evt.Info.Timestamp.IsZero() && evt.Info.Timestamp.Before(coll.sentAt.Add(-12*time.Second)) {
		return
	}

	if !c.matchesBotSource(cli, coll, evt.Info.MessageSource) {
		return
	}

	text := ExtractMessageBubble(evt.Message)
	if strings.TrimSpace(text) == "" {
		return
	}

	select {
	case coll.ch <- collectedBubble{
		id:     evt.Info.ID,
		chat:   evt.Info.Chat,
		sender: evt.Info.Sender,
		text:   text,
	}:
	default:
	}
}

func (c *Client) matchesBotSource(cli *whatsmeow.Client, coll *replyCollector, src types.MessageSource) bool {
	candidates := []types.JID{src.Chat, src.Sender, src.SenderAlt}
	for _, jid := range candidates {
		if jid.User == "" {
			continue
		}
		if jid.User == coll.botPhone {
			return true
		}
		if coll.botLID != "" && jid.User == coll.botLID {
			return true
		}
		if jid.Server == types.HiddenUserServer && cli != nil && cli.Store != nil && cli.Store.LIDs != nil {
			if pn, err := cli.Store.LIDs.GetPNForLID(context.Background(), jid); err == nil && pn.User == coll.botPhone {
				return true
			}
		}
	}
	return false
}

func ExtractMessageBubble(msg *waE2E.Message) string {
	msg = unwrapContainerMessage(msg)
	if msg == nil {
		return ""
	}

	var parts []string
	addText := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" {
			parts = append(parts, s)
		}
	}

	if conv := msg.GetConversation(); conv != "" {
		addText(conv)
	} else if ext := msg.GetExtendedTextMessage(); ext != nil && ext.GetText() != "" {
		addText(ext.GetText())
	}

	if img := msg.GetImageMessage(); img != nil {
		addText(img.GetCaption())
		mime := img.GetMimetype()
		if mime == "" {
			mime = "image/jpeg"
		}
		addText("[Berkas] " + mime)
	}

	if doc := msg.GetDocumentMessage(); doc != nil {
		addText(doc.GetCaption())
		label := doc.GetFileName()
		if label == "" {
			label = doc.GetMimetype()
		}
		if label == "" {
			label = "document"
		}
		addText("[Berkas] " + label)
	}

	if vid := msg.GetVideoMessage(); vid != nil {
		addText(vid.GetCaption())
		mime := vid.GetMimetype()
		if mime == "" {
			mime = "video/mp4"
		}
		addText("[Berkas] " + mime)
	}

	if loc := msg.GetLocationMessage(); loc != nil {
		if loc.GetName() != "" || loc.GetAddress() != "" {
			addText(strings.TrimSpace(loc.GetName() + " " + loc.GetAddress()))
		}
		addText(fmt.Sprintf("[Koordinat] %v, %v", loc.GetDegreesLatitude(), loc.GetDegreesLongitude()))
	} else if liveLoc := msg.GetLiveLocationMessage(); liveLoc != nil {
		addText(liveLoc.GetCaption())
		addText(fmt.Sprintf("[Koordinat] %v, %v", liveLoc.GetDegreesLatitude(), liveLoc.GetDegreesLongitude()))
	}

	if btn := msg.GetButtonsMessage(); btn != nil {
		addText(btn.GetContentText())
		addText(btn.GetFooterText())
	}
	if list := msg.GetListMessage(); list != nil {
		addText(list.GetTitle())
		addText(list.GetDescription())
	}
	if tpl := msg.GetTemplateMessage(); tpl != nil {
		if h := tpl.GetHydratedTemplate(); h != nil {
			addText(h.GetHydratedTitleText())
			addText(h.GetHydratedContentText())
		}
	}

	return strings.Join(parts, "\n")
}

func unwrapContainerMessage(msg *waE2E.Message) *waE2E.Message {
	for depth := 0; depth < 4 && msg != nil; depth++ {
		if eph := msg.GetEphemeralMessage(); eph != nil && eph.GetMessage() != nil {
			msg = eph.GetMessage()
			continue
		}
		if vo := msg.GetViewOnceMessage(); vo != nil && vo.GetMessage() != nil {
			msg = vo.GetMessage()
			continue
		}
		if vo2 := msg.GetViewOnceMessageV2(); vo2 != nil && vo2.GetMessage() != nil {
			msg = vo2.GetMessage()
			continue
		}
		if docCap := msg.GetDocumentWithCaptionMessage(); docCap != nil && docCap.GetMessage() != nil {
			msg = docCap.GetMessage()
			continue
		}
		if ed := msg.GetEditedMessage(); ed != nil && ed.GetMessage() != nil {
			inner := ed.GetMessage()
			if pm := inner.GetProtocolMessage(); pm != nil && pm.GetEditedMessage() != nil {
				msg = pm.GetEditedMessage()
				continue
			}
			msg = inner
			continue
		}
		break
	}
	return msg
}

func formatCollectedBubbles(bubbles []collectedBubble) string {
	if len(bubbles) == 0 {
		return ""
	}
	if len(bubbles) == 1 {
		return strings.TrimSpace(bubbles[0].text)
	}
	parts := make([]string, 0, len(bubbles))
	for i, b := range bubbles {
		parts = append(parts, fmt.Sprintf("[%d] %s", i+1, strings.TrimSpace(b.text)))
	}
	return strings.Join(parts, "\n\n")
}

func computeTypingDurationMs(chars int, s model.HumanSettings) int {
	base := chars * s.TypingMsPerChar
	jitterFactor := 0.82 + rand.Float64()*0.36 // 82% .. 118%
	val := int(float64(base) * jitterFactor)
	if val < s.TypingMinMs {
		val = s.TypingMinMs + rand.Intn(maxInt(1, minInt(600, s.TypingMaxMs-s.TypingMinMs+1)))
	}
	if val > s.TypingMaxMs {
		val = s.TypingMaxMs
	}
	return val
}

func computeReadingDurationMs(replyChars int, s model.HumanSettings) int {
	// ~18 ms/char reading speed + base reaction time
	val := 1400 + replyChars*16 + rand.Intn(900)
	if val < s.ReadDelayMinMs {
		val = s.ReadDelayMinMs
	}
	if val > s.ReadDelayMaxMs {
		val = s.ReadDelayMaxMs
	}
	return val
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
