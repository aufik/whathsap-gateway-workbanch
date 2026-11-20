package wa

import (
	"errors"
	"fmt"
	"sync"

	"whatshap-gateway/internal/model"
)

// Pool owns the WhatsApp devices of a single user. Each device keeps its own
// session database, collector and media scope, so several numbers can work in
// parallel without crossing replies or leaking another account's data.
type Pool struct {
	mu      sync.RWMutex
	userID  int64
	devices map[string]*Client
	order   []string
	next    int
	emit    func(userID int64, eventType string, payload any)
}

func NewPool(userID int64, emit func(int64, string, any)) *Pool {
	return &Pool{
		userID:  userID,
		devices: map[string]*Client{},
		emit:    emit,
	}
}

func (p *Pool) Emit(eventType string, payload any) {
	if p.emit != nil {
		p.emit(p.userID, eventType, payload)
	}
}

// Add registers a device client created from its own session file.
func (p *Pool) Add(deviceUUID string, client *Client) {
	p.mu.Lock()
	if _, exists := p.devices[deviceUUID]; !exists {
		p.order = append(p.order, deviceUUID)
	}
	p.devices[deviceUUID] = client
	p.mu.Unlock()
}

func (p *Pool) Remove(deviceUUID string) *Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.devices[deviceUUID]
	delete(p.devices, deviceUUID)
	for i, id := range p.order {
		if id == deviceUUID {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	return c
}

// Devices returns the registered clients in stable order.
func (p *Pool) Devices() []*Client {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Client, 0, len(p.order))
	for _, id := range p.order {
		if c, ok := p.devices[id]; ok {
			out = append(out, c)
		}
	}
	return out
}

// NextIdle returns the next connected device in round-robin order, skipping
// devices that are busy or disconnected.
func (p *Pool) NextIdle() *Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.order)
	for i := 0; i < n; i++ {
		idx := (p.next + i) % n
		c := p.devices[p.order[idx]]
		if c == nil {
			continue
		}
		st := c.Status()
		if !st.LoggedIn || !st.Connected {
			continue
		}
		p.next = (idx + 1) % n
		return c
	}
	return nil
}

func (p *Pool) Statuses() map[string]model.WAStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]model.WAStatus, len(p.devices))
	for id, c := range p.devices {
		out[id] = c.Status()
	}
	return out
}

func (p *Pool) ConnectedCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := 0
	for _, c := range p.devices {
		st := c.Status()
		if st.LoggedIn && st.Connected {
			n++
		}
	}
	return n
}

func (p *Pool) Get(deviceUUID string) (*Client, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c, ok := p.devices[deviceUUID]
	return c, ok
}

// Resolve returns the pool's device or an error when the uuid is unknown.
func (p *Pool) Resolve(deviceUUID string) (*Client, error) {
	if c, ok := p.Get(deviceUUID); ok && c != nil {
		return c, nil
	}
	return nil, fmt.Errorf("perangkat %q tidak terdaftar untuk akun ini", deviceUUID)
}

// Close disconnects every device owned by this pool.
func (p *Pool) Close() {
	for _, c := range p.Devices() {
		c.Close()
	}
}

var errNoDevice = errors.New("tidak ada perangkat WhatsApp yang terhubung")
