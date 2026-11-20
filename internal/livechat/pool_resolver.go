package livechat

import (
	"fmt"

	"whatshap-gateway/internal/wa"
)

// PoolResolver maps (userID, deviceUUID) to the concrete WA client through the
// per-user WhatsApp pool. It is the default DeviceResolver used by the web
// wiring; unit tests skip it and supply their own fake.
type PoolResolver struct{ Pools *wa.Manager }

// ClientFor implements DeviceResolver. The device must belong to the same
// user: Pool.Resolve uses the per-user pool, so cross-account UUIDs fail here
// rather than later.
func (r PoolResolver) ClientFor(userID int64, deviceUUID string) (WAClient, error) {
	if r.Pools == nil {
		return nil, fmt.Errorf("whatsapp pool belum disiapkan")
	}
	pool := r.Pools.Pool(userID)
	c, err := pool.Resolve(deviceUUID)
	if err != nil {
		return nil, err
	}
	return NewWAClient(c), nil
}
