package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"whatshap-gateway/internal/model"
)

type SSEBroker struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}
}

func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		clients: make(map[chan []byte]struct{}),
	}
}

func (b *SSEBroker) Subscribe() (chan []byte, func()) {
	ch := make(chan []byte, 64)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()

	unsubscribe := func() {
		b.mu.Lock()
		if _, ok := b.clients[ch]; ok {
			delete(b.clients, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, unsubscribe
}

func (b *SSEBroker) Broadcast(eventType string, payload any) {
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
	for ch := range b.clients {
		select {
		case ch <- msg:
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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := b.Subscribe()
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
