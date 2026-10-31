// Package rng implements the domain.Random port on top of math/rand with
// explicit, reproducible seeding per virtual client.
package rng

import (
	"math/rand"
	"sync"
	"time"
)

// RNG is a mutex-guarded, seedable Float64 source. It is safe for
// concurrent use: a single virtual client draws jitter from several track
// goroutines.
type RNG struct {
	mu sync.Mutex
	r  *rand.Rand
}

// New returns a client-local Random. seed == 0 means "derive from the
// current time"; otherwise the stream is fully reproducible and disjoint
// per client id.
func New(seed int64, clientID int) *RNG {
	var src int64
	if seed == 0 {
		src = time.Now().UnixNano() + int64(clientID)
	} else {
		src = seed + int64(clientID)*7919
	}
	return &RNG{r: rand.New(rand.NewSource(src))}
}

func (x *RNG) Float64() float64 {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.r.Float64()
}
