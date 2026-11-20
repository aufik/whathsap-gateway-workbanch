package wa

import (
	"errors"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// Errors surfaced by the additive Collector/Send helpers. The legacy
// AskBotHumanized errors (ErrCircuitBreaker etc.) live in client.go.
var (
	errNoClient        = errors.New("whatsapp client belum siap")
	errInvalidBotPhone = errors.New("nomor whatsapp bot tidak valid (minimal 8 digit)")
)

// newUserJID returns the WhatsApp JID for a phone in E.164 form.
func newUserJID(phone string) types.JID { return types.NewJID(phone, types.DefaultUserServer) }

// whatsmeowClient is an internal alias so other files in this package can
// reference the concrete type without re-importing it in every file.
type whatsmeowClient = whatsmeow.Client
