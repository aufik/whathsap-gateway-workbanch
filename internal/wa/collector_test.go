package wa

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// newTestCollector builds a Collector bound to a Client without touching the
// WhatsApp connection, so registry logic can be exercised in unit tests.
func newTestCollector(c *Client, botPhone string) *Collector {
	col := &Collector{
		client:   c,
		id:       c.collectorSeq.Add(1),
		botPhone: botPhone,
		sentAt:   time.Now().Add(-time.Second),
		ch:       make(chan CollectedBubble, 8),
		closed:   make(chan struct{}),
	}
	c.collectors.Store(col.id, col)
	return col
}

func evtFor(bot, text string) *receivedEvent {
	return &receivedEvent{
		id:         types.MessageID("msg-" + text),
		when:       time.Now(),
		chatUser:   bot,
		senderUser: bot,
		text:       text,
	}
}

func TestDeliverToCollectorsFanOut(t *testing.T) {
	c := &Client{}
	a := newTestCollector(c, "628111")
	b := newTestCollector(c, "628111")
	defer a.Close()
	defer b.Close()

	c.deliverToCollectors(evtFor("628111", "halo"))

	for i, col := range []*Collector{a, b} {
		select {
		case got := <-col.Bubbles():
			if got.Text != "halo" {
				t.Fatalf("collector %d text=%q", i, got.Text)
			}
		case <-time.After(50 * time.Millisecond):
			t.Fatalf("collector %d received nothing", i)
		}
	}
}

func TestDeliverToCollectorsIgnoredForOtherBotAndOldMessages(t *testing.T) {
	c := &Client{}
	a := newTestCollector(c, "628111")
	defer a.Close()

	// Different bot number must not receive anything.
	c.deliverToCollectors(evtFor("628999", "bukan bot saya"))
	// Timestamp older than collector's sentAt-12s must be dropped.
	old := evtFor("628111", "lama")
	old.when = a.sentAt.Add(-30 * time.Second)
	c.deliverToCollectors(old)

	select {
	case got := <-a.Bubbles():
		t.Fatalf("unexpected delivery %+v", got)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestCollectorCloseStopsDelivery(t *testing.T) {
	c := &Client{}
	a := newTestCollector(c, "628111")
	b := newTestCollector(c, "628111")
	defer b.Close()

	a.Close()
	c.deliverToCollectors(evtFor("628111", "post-close"))

	// B still receives; A's channel is closed (zero value with no Text).
	select {
	case got := <-a.Bubbles():
		if got.Text != "" {
			t.Fatalf("closed collector leaked text %q", got.Text)
		}
	case <-time.After(30 * time.Millisecond):
		t.Fatalf("closed collector channel was not closed")
	}
	select {
	case got := <-b.Bubbles():
		if got.Text != "post-close" {
			t.Fatalf("bubble B got %q, want post-close", got.Text)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatalf("B must keep receiving after A.Close")
	}

	if _, ok := c.collectors.Load(a.id); ok {
		t.Fatalf("closed collector still registered")
	}
}

// TestLegacyReplyCollectorUnaffected asserts the old single-slot collector is
// still what AskBotHumanized/collectBotBubbles use and that registering additive
// collectors does not mutate it.
func TestLegacyReplyCollectorUnaffected(t *testing.T) {
	c := &Client{}
	legacy := &replyCollector{
		botPhone: "628111",
		sentAt:   time.Now(),
		ch:       make(chan collectedBubble, 8),
	}
	c.collector = legacy

	additive := newTestCollector(c, "628111")
	defer additive.Close()

	if c.collector != legacy {
		t.Fatalf("RegisterCollector replaced the legacy field; runners would break")
	}

	// Simulate legacy push + additive fan-out side by side (what
	// handleIncomingMessage does today).
	legacy.ch <- collectedBubble{id: "m1", text: "old-path"}
	c.deliverToCollectors(evtFor("628111", "new-path"))

	select {
	case got := <-legacy.ch:
		if got.text != "old-path" {
			t.Fatalf("legacy got %q", got.text)
		}
	default:
		t.Fatalf("legacy path stopped receiving")
	}
	select {
	case got := <-additive.Bubbles():
		if got.Text != "new-path" {
			t.Fatalf("additive got %q", got.Text)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatalf("additive collector received nothing")
	}
}

func TestCollectorWaitQuietWindow(t *testing.T) {
	c := &Client{}
	col := newTestCollector(c, "628111")
	defer col.Close()

	done := make(chan []CollectedBubble, 1)
	go func() {
		out, err := col.Wait(context.Background(), 40*time.Millisecond, time.Second)
		if err != nil {
			t.Errorf("wait err: %v", err)
		}
		done <- out
	}()

	c.deliverToCollectors(evtFor("628111", "satu"))
	time.Sleep(5 * time.Millisecond)
	c.deliverToCollectors(evtFor("628111", "dua"))

	out := <-done
	if len(out) != 2 || out[0].Text != "satu" || out[1].Text != "dua" {
		t.Fatalf("expected two bubbles in order, got %+v", out)
	}
}

func TestCollectorWaitContextCancel(t *testing.T) {
	c := &Client{}
	col := newTestCollector(c, "628111")
	defer col.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := col.Wait(ctx, 10*time.Millisecond, time.Second)
	if err == nil || len(out) != 0 {
		t.Fatalf("expected ctx error, got out=%d err=%v", len(out), err)
	}
}
