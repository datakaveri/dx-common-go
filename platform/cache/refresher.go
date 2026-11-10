package cache

import (
	"sync"
	"time"
)

// refresher owns the background refresh-ahead goroutines a Cache spawns
// (GetOrLoadAhead), so they drain at Close rather than outliving the process
// (ROADMAP P2-1). One refresher is shared by every scope derived from a root and
// is distinct per root, matching the singleflight group.
type refresher struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// run starts fn as a tracked background task — unless the owning Cache is already
// closing, in which case it is dropped: a refresh whose result nothing will read
// is pure waste during shutdown, and spawning it would only delay the drain.
func (r *refresher) run(fn func()) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.wg.Done()
		fn()
	}()
}

// drain stops accepting new tasks and waits for the in-flight ones, bounded by
// timeout so one wedged loader cannot hang shutdown. Idempotent.
func (r *refresher) drain(timeout time.Duration) {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}
