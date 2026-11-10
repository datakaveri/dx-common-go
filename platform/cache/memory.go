package cache

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Memory is an in-process Store.
//
// Two real uses, and it is genuinely production-grade for both:
//
//   - Tests. A service under test gets a working cache with no container and
//     no network, so cache behaviour is exercised rather than stubbed out.
//   - Single-replica or Redis-less deployments. A local dev stack, or a
//     service whose cache is a latency optimisation rather than shared state.
//
// It is NOT a distributed cache. Its Lock excludes only within this process,
// which is stated on that method because a lock that silently fails to exclude
// across replicas is the kind of defect that surfaces as rare, unexplainable
// duplicate work.
type Memory struct {
	mu    sync.RWMutex
	items map[string]entry
	locks map[string]lockHolder
	// lockSeq mints a unique token per acquisition so an unlock only releases
	// the lock it took, never a later holder's.
	lockSeq uint64
	// writesSinceSweep counts writes toward the next amortized eviction sweep,
	// which keeps the map bounded under churn without a background goroutine.
	writesSinceSweep int
	// now is injectable so expiry is testable without sleeping.
	now func() time.Time
}

type entry struct {
	val []byte
	// exp is the zero time when the entry never expires.
	exp time.Time
}

// lockHolder is one advisory-lock acquisition: when it expires, and a token
// that identifies THIS acquisition so a stale unlock cannot free a successor.
type lockHolder struct {
	until time.Time
	token uint64
}

// sweepInterval is how many writes pass between full expired-entry sweeps. A
// sweep is O(n), so amortized it is O(1) per write; the bound on the map is the
// live set plus at most this many just-expired entries.
const sweepInterval = 256

// NewMemory builds an in-process store.
func NewMemory() *Memory {
	return &Memory{
		items: make(map[string]entry),
		locks: make(map[string]lockHolder),
		now:   time.Now,
	}
}

func (m *Memory) Get(ctx context.Context, key string) ([]byte, error) {
	now := m.now()
	m.mu.RLock()
	e, ok := m.items[key]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrMiss
	}
	if e.expired(now) {
		// Evict on observation rather than merely reporting a miss, so an
		// expired entry that is read again is freed instead of lingering. The
		// re-check under the write lock avoids deleting a value that was
		// concurrently re-Set with a fresh expiry.
		m.mu.Lock()
		if cur, ok := m.items[key]; ok && cur.expired(m.now()) {
			delete(m.items, key)
		}
		m.mu.Unlock()
		return nil, ErrMiss
	}
	// Copy: handing the caller our slice would let a mutation of the decoded
	// buffer corrupt the cached entry for everyone else.
	out := make([]byte, len(e.val))
	copy(out, e.val)
	return out, nil
}

func (m *Memory) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	stored := make([]byte, len(val))
	copy(stored, val)

	now := m.now()
	var exp time.Time
	if ttl > 0 {
		exp = now.Add(ttl)
	}
	m.mu.Lock()
	m.items[key] = entry{val: stored, exp: exp}
	m.sweepExpiredLocked(now)
	m.mu.Unlock()
	return nil
}

func (m *Memory) Delete(ctx context.Context, keys ...string) error {
	m.mu.Lock()
	for _, k := range keys {
		delete(m.items, k)
	}
	m.mu.Unlock()
	return nil
}

func (m *Memory) Exists(ctx context.Context, key string) (bool, error) {
	m.mu.RLock()
	e, ok := m.items[key]
	m.mu.RUnlock()
	return ok && !e.expired(m.now()), nil
}

func (m *Memory) DeletePrefix(ctx context.Context, prefix string) error {
	m.mu.Lock()
	for k := range m.items {
		if strings.HasPrefix(k, prefix) {
			delete(m.items, k)
		}
	}
	m.mu.Unlock()
	return nil
}

// Lock excludes within THIS PROCESS ONLY.
//
// It is honest about that rather than approximating a distributed lock: if you
// need cross-replica exclusion, use the Redis store. A single-replica
// deployment is exactly as safe here as it would be with Redis.
//
// The returned release closure carries a token identifying THIS acquisition and
// frees the lock only while it still holds it. Without that, a holder whose TTL
// had lapsed — letting a second caller acquire the same key — would, on its own
// late release, delete the second holder's lock and admit a third: the classic
// lease-expiry lock-steal.
func (m *Memory) Lock(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()

	if h, held := m.locks[key]; held && now.Before(h.until) {
		return nil, false, nil
	}
	m.lockSeq++
	token := m.lockSeq
	m.locks[key] = lockHolder{until: now.Add(ttl), token: token}

	return func(context.Context) error {
		m.mu.Lock()
		if h, ok := m.locks[key]; ok && h.token == token {
			delete(m.locks, key)
		}
		m.mu.Unlock()
		return nil
	}, true, nil
}

// Incr atomically increments a counter under the store's own lock.
func (m *Memory) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()

	var n int64
	if e, ok := m.items[key]; ok && !e.expired(now) {
		// Counters are stored as their decimal text so the Store contract
		// stays bytes-only and the Redis and memory encodings agree.
		n, _ = strconv.ParseInt(string(e.val), 10, 64)
	}
	n++

	exp := time.Time{}
	if ttl > 0 {
		exp = now.Add(ttl)
	}
	m.items[key] = entry{val: []byte(strconv.FormatInt(n, 10)), exp: exp}
	m.sweepExpiredLocked(now)
	return n, nil
}

// sweepExpiredLocked runs an amortized eviction: once every sweepInterval writes
// it removes every expired item and lapsed lock. That is what keeps churn — many
// keys Set once with a TTL and never read or re-Set — from growing the maps
// without bound, and it needs no background goroutine to do it. The caller must
// hold m.mu for writing.
func (m *Memory) sweepExpiredLocked(now time.Time) {
	m.writesSinceSweep++
	if m.writesSinceSweep < sweepInterval {
		return
	}
	m.writesSinceSweep = 0
	for k, e := range m.items {
		if e.expired(now) {
			delete(m.items, k)
		}
	}
	for k, h := range m.locks {
		if now.After(h.until) {
			delete(m.locks, k)
		}
	}
}

// Close drops everything held. Safe to call more than once.
func (m *Memory) Close() error {
	m.mu.Lock()
	m.items = make(map[string]entry)
	m.locks = make(map[string]lockHolder)
	m.mu.Unlock()
	return nil
}

func (e entry) expired(now time.Time) bool {
	return !e.exp.IsZero() && now.After(e.exp)
}
