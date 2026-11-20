package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"whatshap-gateway/internal/database"
)

const CookieName = "wa_gw_session"

type Manager struct {
	store  *database.Store
	secret []byte
}

func NewManager(store *database.Store, secret string) *Manager {
	return &Manager{store: store, secret: []byte(secret)}
}

type ctxKey struct{}

// WithSession attaches a session to a context. Exported for tests and for
// internal callers that already resolved the session themselves.
func WithSession(ctx context.Context, sess *database.Session) context.Context {
	return context.WithValue(ctx, ctxKey{}, sess)
}

func (m *Manager) sign(token string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

func (m *Manager) verify(token, signature string) bool {
	return hmac.Equal([]byte(m.sign(token)), []byte(signature))
}

func (m *Manager) Login(ctx context.Context, w http.ResponseWriter, userID int64) error {
	token, expires, err := m.store.CreateSession(ctx, userID)
	if err != nil {
		return err
	}
	value := token + "." + m.sign(token)
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
	})
	return nil
}

func (m *Manager) Logout(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		if idx := indexByte(c.Value, '.'); idx > 0 {
			_ = m.store.DeleteSession(ctx, c.Value[:idx])
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func (m *Manager) SessionFromRequest(ctx context.Context, r *http.Request) (*database.Session, error) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return nil, errors.New("session cookie tidak ditemukan")
	}
	idx := indexByte(c.Value, '.')
	if idx <= 0 {
		return nil, errors.New("format cookie tidak valid")
	}
	token := c.Value[:idx]
	sig := c.Value[idx+1:]
	if !m.verify(token, sig) {
		return nil, errors.New("signature session tidak valid")
	}
	sess, err := m.store.ResolveSession(ctx, token)
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := m.SessionFromRequest(r.Context(), r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func FromContext(ctx context.Context) (*database.Session, bool) {
	s, ok := ctx.Value(ctxKey{}).(*database.Session)
	return s, ok
}

func HexSHA256(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}
