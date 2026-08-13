package cache

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// fakeClock returns a Memory wired to a clock the test advances by reassigning
// the returned time; the closure reads it by reference on every call.
func fakeClock() (*Memory, *time.Time) {
	m := NewMemory()
	now := time.Now()
	m.now = func() time.Time { return now }
	return m, &now
}

// TestMemoryLockDoesNotStealAcrossExpiry pins the owner-token fix (ROADMAP P2-4):
// once a lock's TTL lapses and a second caller takes the key, the first holder's
// late release must NOT free the second holder's lock.
func TestMemoryLockDoesNotStealAcrossExpiry(t *testing.T) {
	ctx := context.Background()
	m, now := fakeClock()

	releaseA, okA, _ := m.Lock(ctx, "job", time.Minute)
	if !okA {
		t.Fatal("A should acquire a free lock")
	}

	*now = now.Add(2 * time.Minute) // A's TTL lapses

	releaseB, okB, _ := m.Lock(ctx, "job", time.Minute)
	if !okB {
		t.Fatal("B should acquire the lock A let expire")
	}

	// A's stale release: it no longer holds the lock, so it must be a no-op.
	if err := releaseA(ctx); err != nil {
		t.Fatalf("stale release errored: %v", err)
	}

	// If A's release had stolen B's lock, C would acquire — the bug.
	if _, okC, _ := m.Lock(ctx, "job", time.Minute); okC {
		t.Fatal("a third caller acquired the lock B still holds — A's stale release stole it")
	}

	// B's own release frees it; then the key is takeable again.
	if err := releaseB(ctx); err != nil {
		t.Fatalf("B release errored: %v", err)
	}
	if _, okD, _ := m.Lock(ctx, "job", time.Minute); !okD {
		t.Fatal("after B released, the lock should be free")
	}
}

// TestMemoryEvictsExpiredOnRead: a Get past the TTL not only misses, it removes
// the entry rather than leaving it in the map (ROADMAP P2-4).
func TestMemoryEvictsExpiredOnRead(t *testing.T) {
	ctx := context.Background()
	m, now := fakeClock()

	if err := m.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	n := len(m.items)
	m.mu.RUnlock()
	if n != 1 {
		t.Fatalf("after Set, items = %d, want 1", n)
	}

	*now = now.Add(2 * time.Minute)
	if _, err := m.Get(ctx, "k"); err != ErrMiss {
		t.Fatalf("Get past TTL = %v, want ErrMiss", err)
	}

	m.mu.RLock()
	n = len(m.items)
	m.mu.RUnlock()
	if n != 0 {
		t.Fatalf("expired entry not evicted on read: items = %d, want 0", n)
	}
}

// TestMemoryChurnStaysBounded is the leak regression (ROADMAP P2-4): many keys
// Set once with a TTL and never read again must not grow the map without bound —
// the amortized sweep removes the expired ones.
func TestMemoryChurnStaysBounded(t *testing.T) {
	ctx := context.Background()
	m, now := fakeClock()

	const writes = 5 * sweepInterval // comfortably past several sweeps
	for i := 0; i < writes; i++ {
		if err := m.Set(ctx, fmt.Sprintf("k-%d", i), []byte("v"), time.Millisecond); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(2 * time.Millisecond) // each key expires before the next Set
	}

	m.mu.RLock()
	n := len(m.items)
	m.mu.RUnlock()
	// Without eviction this would be `writes` (every key leaked). With the sweep
	// it is at most one sweep window of just-written entries.
	if n > 2*sweepInterval {
		t.Fatalf("map grew unbounded under churn: items = %d after %d writes (want <= %d)", n, writes, 2*sweepInterval)
	}
}
