package wa

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/model"
)

// SendImage uploads data as a WhatsApp image and sends it to phone.
func (c *Client) SendImage(ctx context.Context, phone string, data []byte, mimetype, caption string) error {
	cli, jid, err := c.jidFor(phone)
	if err != nil {
		return err
	}
	up, err := cli.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return err
	}
	_, err = cli.SendMessage(ctx, jid, &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{
			Caption:       optionalString(caption),
			Mimetype:      proto.String(mimetype),
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
		},
	})
	return err
}

// SendDocument uploads data as a WhatsApp document and sends it to phone.
func (c *Client) SendDocument(ctx context.Context, phone string, data []byte, fileName, mimetype, caption string) error {
	cli, jid, err := c.jidFor(phone)
	if err != nil {
		return err
	}
	if strings.TrimSpace(fileName) == "" {
		fileName = "berkas"
	}
	up, err := cli.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return err
	}
	_, err = cli.SendMessage(ctx, jid, &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			FileName:      proto.String(fileName),
			Title:         proto.String(fileName),
			Caption:       optionalString(caption),
			Mimetype:      proto.String(mimetype),
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
		},
	})
	return err
}

// SendLocation sends a real LocationMessage; no upload is needed.
func (c *Client) SendLocation(ctx context.Context, phone string, lat, lng float64, name, address string) error {
	cli, jid, err := c.jidFor(phone)
	if err != nil {
		return err
	}
	_, err = cli.SendMessage(ctx, jid, &waE2E.Message{
		LocationMessage: &waE2E.LocationMessage{
			DegreesLatitude:  proto.Float64(lat),
			DegreesLongitude: proto.Float64(lng),
			Name:             optionalString(name),
			Address:          optionalString(address),
		},
	})
	return err
}

// SendText sends a plain chat message, the companion to AskBotHumanized when
// the caller wants no collect/wait logic (Live Chat engine loop).
func (c *Client) SendText(ctx context.Context, phone, text string) error {
	cli, jid, err := c.jidFor(phone)
	if err != nil {
		return err
	}
	_, err = cli.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(text)})
	return err
}

// MarkBubblesRead sends blue-tick read receipts for the given message IDs to target bot phone.
func (c *Client) MarkBubblesRead(ctx context.Context, phone string, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	cli, targetJID, err := c.jidFor(phone)
	if err != nil {
		return err
	}
	ids := make([]types.MessageID, 0, len(messageIDs))
	for _, id := range messageIDs {
		if id != "" {
			ids = append(ids, types.MessageID(id))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return cli.MarkRead(ctx, ids, time.Now(), targetJID, targetJID)
}

// SimulateTyping sends ChatPresenceComposing to the target bot with human multi-burst pauses.
func (c *Client) SimulateTyping(ctx context.Context, phone string, text string, s model.HumanSettings) error {
	cli, targetJID, err := c.jidFor(phone)
	if err != nil {
		return err
	}
	charCount := len([]rune(text))
	typingMs := computeTypingDurationMs(charCount, s)
	plannedBursts := estimateBurstCount(typingMs)
	return simulateMultiBurstTyping(ctx, cli, targetJID, typingMs, plannedBursts)
}

// PauseTyping sends ChatPresencePaused to the target bot.
func (c *Client) PauseTyping(ctx context.Context, phone string) error {
	cli, targetJID, err := c.jidFor(phone)
	if err != nil {
		return err
	}
	return cli.SendChatPresence(ctx, targetJID, types.ChatPresencePaused, types.ChatPresenceMediaText)
}

func (c *Client) jidFor(phone string) (*whatsmeow.Client, types.JID, error) {
	clean := config.NormalizePhone(phone)
	if len(clean) < 8 {
		return nil, types.JID{}, errInvalidBotPhone
	}
	c.mu.RLock()
	cli := c.cli
	c.mu.RUnlock()
	if cli == nil || !cli.IsConnected() || cli.Store.ID == nil {
		return nil, types.JID{}, errors.New("whatsapp penguji belum terhubung/login")
	}
	return cli, types.NewJID(clean, types.DefaultUserServer), nil
}

func optionalString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return proto.String(s)
}
