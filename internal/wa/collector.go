package wa

import (
	"context"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
	"whatshap-gateway/internal/config"
)

// Collector is a first-class subscriber that can coexist with the legacy
// single-slot replyCollector used by AskBotHumanized. The runners (Bank/OOB/
// Stress) keep using the legacy field untouched; the Live Chat engine opens one
// Collector per running session so two consumers never steal each other's
// replies.
type Collector struct {
	client   *Client
	id       uint64
	botPhone string
	botLID   string
	sentAt   time.Time
	ch       chan CollectedBubble

	closeOnce sync.Once
	closed    chan struct{}
}

// receivedEvent is the minimal bubble envelope the additive collector path
// needs. Keeping it separate from replyCollector means no existing runner
// types are touched when a second collector is active.
type receivedEvent struct {
	id            types.MessageID
	when          time.Time
	chatUser      string
	senderUser    string
	senderAltUser string
	text          string
	mediaName     string
	// mediaBytes + mediaExt were already decoded by the existing helper in
	// client.go (downloadMediaIfPresent). When the caller passes mediaBytes=nil
	// the collector uses MediaName only (legacy path untouched).
	mediaBytes []byte
	mediaExt   string
}

// RegisterCollector attaches an additional subscriber on this client. The
// returned Collector must be released with Close when the caller is done.
func (c *Client) RegisterCollector(botPhone string) (*Collector, error) {
	c.mu.RLock()
	cli := c.cli
	c.mu.RUnlock()
	if cli == nil {
		return nil, errNoClient
	}
	cleanBot := config.NormalizePhone(botPhone)
	if len(cleanBot) < 8 {
		return nil, errInvalidBotPhone
	}
	var botLID string
	if cli.Store != nil && cli.Store.LIDs != nil {
		if lid, err := cli.Store.LIDs.GetLIDForPN(context.Background(), newUserJID(cleanBot)); err == nil && lid.User != "" {
			botLID = lid.User
		}
	}
	coll := &Collector{
		client:   c,
		id:       c.collectorSeq.Add(1),
		botPhone: cleanBot,
		botLID:   botLID,
		sentAt:   time.Now(),
		ch:       make(chan CollectedBubble, 32),
		closed:   make(chan struct{}),
	}
	c.collectors.Store(coll.id, coll)
	return coll, nil
}

// Bubbles delivers incoming bot bubbles until the channel is closed.
func (col *Collector) Bubbles() <-chan CollectedBubble { return col.ch }

// Wait blocks for one collection round with a quiet window and a hard timeout,
// mirroring collectBotBubbles semantics without hooks/telemetry.
func (col *Collector) Wait(ctx context.Context, quietWindow, hardTimeout time.Duration) ([]CollectedBubble, error) {
	hard := time.NewTimer(hardTimeout)
	defer hard.Stop()
	var quiet *time.Timer
	var quietCh <-chan time.Time
	defer func() {
		if quiet != nil {
			quiet.Stop()
		}
	}()
	var out []CollectedBubble
	for {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-col.closed:
			return out, nil
		case <-hard.C:
			return out, nil
		case <-quietCh:
			return out, nil
		case b := <-col.ch:
			out = append(out, b)
			if quiet == nil {
				quiet = time.NewTimer(quietWindow)
				quietCh = quiet.C
			} else {
				if !quiet.Stop() {
					select {
					case <-quiet.C:
					default:
					}
				}
				quiet.Reset(quietWindow)
			}
		}
	}
}

// Close detaches the collector from the client and closes its channel.
func (col *Collector) Close() {
	col.closeOnce.Do(func() {
		col.client.collectors.Delete(col.id)
		close(col.closed)
		close(col.ch)
	})
}

// hasCollectorsRegistered is a cheap fast-path: {sync.Map.Range} with a count
// short-circuit. It avoids downloading media when only the legacy runner is
// active (which downloads on its own).
func hasCollectorsRegistered(c *Client) bool {
	if c == nil {
		return false
	}
	found := false
	c.collectors.Range(func(_, _ any) bool {
		found = true
		return false
	})
	return found
}

// CollectedBubble is the public shape of a received bot bubble. The legacy
// replyCollector channel keeps using the unexported collectedBubble so no
// existing runner code changes.
type CollectedBubble struct {
	ID         string
	Chat       string // sender chat JID (raw)
	Sender     string // sender JID (raw)
	Text       string
	MediaName  string // display name for the "message envelope", empty when text-only
	MediaBytes []byte // decoded attachment (nil for text-only or oversize)
	MediaExt   string
}

// matchesFilters checks the bubble's timestamp and bot number without touching
// the legacy replyCollector filter (callers pass the chat + sender directly).
func (col *Collector) matches(chatUser, senderUser, senderAltUser string) bool {
	for _, u := range []string{chatUser, senderUser, senderAltUser} {
		if u == "" {
			continue
		}
		if u == col.botPhone {
			return true
		}
		if col.botLID != "" && u == col.botLID {
			return true
		}
	}
	return false
}

// deliverToCollectors pushes a received bubble into every registered collector
// whose bot matches. It never blocks: collectors have their own buffered channel
// and a slow subscriber drops bubbles rather than delaying the caller.
func (c *Client) deliverToCollectors(evt *receivedEvent) {
	if evt == nil {
		return
	}
	c.collectors.Range(func(_, value any) bool {
		coll, ok := value.(*Collector)
		if !ok || coll == nil {
			return true
		}
		if !evt.when.IsZero() && evt.when.Before(coll.sentAt.Add(-12*time.Second)) {
			return true
		}
		if !coll.matches(evt.chatUser, evt.senderUser, evt.senderAltUser) {
			return true
		}
		select {
		case coll.ch <- CollectedBubble{
			ID:         string(evt.id),
			Chat:       evt.chatUser,
			Sender:     evt.senderUser,
			Text:       evt.text,
			MediaName:  evt.mediaName,
			MediaBytes: evt.mediaBytes,
			MediaExt:   evt.mediaExt,
		}:
		default:
		}
		return true
	})
}
