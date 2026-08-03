package cache

import (
	"context"
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
	locks map[string]time.Time
	// now is injectable so expiry is testable without sleeping.
	now func() time.Time
}

type entry struct {
	val []byte
	// exp is the zero time when the entry never expires.
	exp time.Time
}

// NewMemory builds an in-process store.
func NewMemory() *Memory {
	return &Memory{
		items: make(map[string]entry),
		locks: make(map[string]time.Time),
		now:   time.Now,
	}
}

func (m *Memory) Get(ctx context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	e, ok := m.items[key]
	m.mu.RUnlock()
	if !ok || e.expired(m.now()) {
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

	var exp time.Time
	if ttl > 0 {
		exp = m.now().Add(ttl)
	}
	m.mu.Lock()
	m.items[key] = entry{val: stored, exp: exp}
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
func (m *Memory) Lock(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()

	if until, held := m.locks[key]; held && now.Before(until) {
		return nil, false, nil
	}
	m.locks[key] = now.Add(ttl)

	return func(context.Context) error {
		m.mu.Lock()
		delete(m.locks, key)
		m.mu.Unlock()
		return nil
	}, true, nil
}

// Close drops everything held. Safe to call more than once.
func (m *Memory) Close() error {
	m.mu.Lock()
	m.items = make(map[string]entry)
	m.locks = make(map[string]time.Time)
	m.mu.Unlock()
	return nil
}

func (e entry) expired(now time.Time) bool {
	return !e.exp.IsZero() && now.After(e.exp)
}
