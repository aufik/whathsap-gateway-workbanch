package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"whatshap-gateway/internal/auth"
	"whatshap-gateway/internal/model"
)

type sseClient struct {
	userID int64
	ch     chan []byte
}

type SSEBroker struct {
	mu      sync.RWMutex
	clients map[*sseClient]struct{}
}

func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		clients: make(map[*sseClient]struct{}),
	}
}

// Subscribe registers a browser stream owned by userID. Events are delivered
// only to clients of the same user, so accounts never observe each other's
// rows, telemetry, or WhatsApp status.
func (b *SSEBroker) Subscribe(userID int64) (chan []byte, func()) {
	c := &sseClient{userID: userID, ch: make(chan []byte, 64)}

	b.mu.Lock()
	b.clients[c] = struct{}{}
	b.mu.Unlock()

	unsubscribe := func() {
		b.mu.Lock()
		if _, ok := b.clients[c]; ok {
			delete(b.clients, c)
			close(c.ch)
		}
		b.mu.Unlock()
	}
	return c.ch, unsubscribe
}

func (b *SSEBroker) Broadcast(userID int64, eventType string, payload any) {
	evt := model.SSEEvent{
		Type:      eventType,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
	raw, err := json.Marshal(evt)
	if err != nil {
		return
	}
	msg := []byte(fmt.Sprintf("data: %s\n\n", raw))

	b.mu.RLock()
	defer b.mu.RUnlock()
	for c := range b.clients {
		if c.userID != userID {
			continue
		}
		select {
		case c.ch <- msg:
		default:
		}
	}
}

func (b *SSEBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	var userID int64
	if sess, ok := auth.FromContext(r.Context()); ok {
		userID = sess.UserID
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := b.Subscribe(userID)
	defer unsub()

	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-pingTicker.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(msg); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
