package attestation

import (
	"sync"
	"time"
)

// MemoryGuard is an in-process ReplayGuard for tests and single-replica dev. A
// data plane across replicas uses a shared store (e.g. Redis SETNX) instead.
type MemoryGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
	now  func() time.Time
}

// NewMemoryGuard builds an empty guard using the wall clock.
func NewMemoryGuard() *MemoryGuard { return NewMemoryGuardWithClock(time.Now) }

// NewMemoryGuardWithClock builds a guard with an injectable clock, so a test can
// prune consistently with the verifier's clock.
func NewMemoryGuardWithClock(clock func() time.Time) *MemoryGuard {
	if clock == nil {
		clock = time.Now
	}
	return &MemoryGuard{seen: map[string]time.Time{}, now: clock}
}

// Use consumes a jti, returning false if it was already used. Expired entries
// are opportunistically pruned using the guard's clock.
func (g *MemoryGuard) Use(jti string, exp time.Time) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for k, e := range g.seen {
		if now.After(e) {
			delete(g.seen, k)
		}
	}
	if _, ok := g.seen[jti]; ok {
		return false, nil
	}
	g.seen[jti] = exp
	return true, nil
}
