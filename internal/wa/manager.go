package wa

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"whatshap-gateway/internal/model"
)

// Manager owns one Pool per user. Pools are independent: devices of user A can
// never observe or drive devices of user B.
type Manager struct {
	mu   sync.Mutex
	pool map[int64]*Pool
	emit func(int64, string, any)
}

func NewManager(emit func(int64, string, any)) *Manager {
	return &Manager{pool: map[int64]*Pool{}, emit: emit}
}

func (m *Manager) Pool(userID int64) *Pool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pool[userID]; ok {
		return p
	}
	p := NewPool(userID, m.emit)
	m.pool[userID] = p
	return p
}

// DeviceSessionPath follows the documented layout:
// data/users/{userID}/wa/{deviceUUID}.db
func DeviceSessionPath(userID int64, deviceUUID string) string {
	return filepath.Join("data", "users", fmt.Sprintf("%d", userID), "wa", deviceUUID+".db")
}

// NewDeviceUUID returns a v4 UUID rendered as hex without dashes.
func NewDeviceUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return hex.EncodeToString(buf), nil
}

// AttachDevice creates (or returns) the client for a user device, scoping its
// session file, media directory and event stream to that user.
func (m *Manager) AttachDevice(ctx context.Context, userID int64, deviceUUID string, onStatus func(model.WAStatus)) (*Client, error) {
	p := m.Pool(userID)
	if existing, ok := p.Get(deviceUUID); ok && existing != nil {
		return existing, nil
	}

	sessionPath := DeviceSessionPath(userID, deviceUUID)
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0755); err != nil {
		return nil, err
	}

	client, err := NewClient(ctx, sessionPath, onStatus)
	if err != nil {
		return nil, err
	}
	mediaRoot := filepath.Join("data", "users", fmt.Sprintf("%d", userID), "downloads")
	_ = os.MkdirAll(mediaRoot, 0755)
	client.SetDeviceMeta(userID, deviceUUID, mediaRoot)
	p.Add(deviceUUID, client)
	return client, nil
}

func (m *Manager) DetachDevice(userID int64, deviceUUID string) {
	if c := m.Pool(userID).Remove(deviceUUID); c != nil {
		c.Close()
	}
}

// CloseAll disconnects every device of every user.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	pools := make([]*Pool, 0, len(m.pool))
	for _, p := range m.pool {
		pools = append(pools, p)
	}
	m.mu.Unlock()
	for _, p := range pools {
		p.Close()
	}
}
