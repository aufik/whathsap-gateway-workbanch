package livechat

import (
	"context"
	"time"

	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/wa"
)

// waAdapter turns a *wa.Client into the WAClient interface so the engine never
// imports whatsmeow directly. Tests provide their own fake.
type waAdapter struct{ c *wa.Client }

// NewWAClient wraps a concrete WhatsApp client for the livechat engine.
func NewWAClient(c *wa.Client) WAClient { return waAdapter{c: c} }

func (a waAdapter) SendText(ctx context.Context, phone, text string) error {
	return a.c.SendText(ctx, phone, text)
}
func (a waAdapter) SendImage(ctx context.Context, phone string, data []byte, mimetype, caption string) error {
	return a.c.SendImage(ctx, phone, data, mimetype, caption)
}
func (a waAdapter) SendDocument(ctx context.Context, phone string, data []byte, fileName, mimetype, caption string) error {
	return a.c.SendDocument(ctx, phone, data, fileName, mimetype, caption)
}
func (a waAdapter) SendLocation(ctx context.Context, phone string, lat, lng float64, name, address string) error {
	return a.c.SendLocation(ctx, phone, lat, lng, name, address)
}
func (a waAdapter) RegisterCollector(botPhone string) (ReplyCollector, error) {
	col, err := a.c.RegisterCollector(botPhone)
	if err != nil {
		return nil, err
	}
	return &collectorAdapter{c: col}, nil
}
func (a waAdapter) Status() model.WAStatus { return a.c.Status() }

// collectorAdapter bridges *wa.Collector to ReplyCollector while copying the
// bubbles into the livechat shape (avoids import cycle in the other direction).
type collectorAdapter struct{ c *wa.Collector }

func (a *collectorAdapter) Wait(ctx context.Context, quietWindow, hardTimeout time.Duration) ([]CollectedBubble, error) {
	raw, err := a.c.Wait(ctx, quietWindow, hardTimeout)
	if err != nil && len(raw) == 0 {
		return nil, err
	}
	out := make([]CollectedBubble, 0, len(raw))
	for _, b := range raw {
		out = append(out, CollectedBubble{
			ID:         b.ID,
			Text:       b.Text,
			MediaBytes: b.MediaBytes,
			MediaExt:   b.MediaExt,
			MediaName:  b.MediaName,
		})
	}
	return out, err
}

func (a *collectorAdapter) Close() { a.c.Close() }
