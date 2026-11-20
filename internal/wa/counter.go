package wa

import "sync/atomic"

// atomicCounter is a tiny helper so client.go does not pull sync/atomic into
// its import block for a single increment.
type atomicCounter struct{ v atomic.Uint64 }

// Add increments the counter and returns the new value.
func (a *atomicCounter) Add(n uint64) uint64 { return a.v.Add(n) }
