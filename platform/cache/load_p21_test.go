package cache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/cache"
)

// TestGetOrLoadDoesNotCrossBetweenCaches pins per-root singleflight isolation
// (ROADMAP P2-1): two independent caches loading the SAME (unprefixed) key at the
// same time — with different value types — must each get their own loader's
// result. A process-global group would collapse them and hand one cache the
// other's value (here, a type mismatch).
func TestGetOrLoadDoesNotCrossBetweenCaches(t *testing.T) {
	a := cache.New(cache.NewMemory())
	b := cache.New(cache.NewMemory())
	const key = "shared"

	release := make(chan struct{})
	var wg sync.WaitGroup
	var (
		aVal       string
		bVal       int
		aErr, bErr error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		aVal, aErr = cache.GetOrLoad(context.Background(), a, key, func(context.Context) (string, error) {
			<-release
			return "from-A", nil
		})
	}()
	go func() {
		defer wg.Done()
		bVal, bErr = cache.GetOrLoad(context.Background(), b, key, func(context.Context) (int, error) {
			<-release
			return 42, nil
		})
	}()
	time.Sleep(30 * time.Millisecond) // both in flight on the same key
	close(release)
	wg.Wait()

	if aErr != nil || aVal != "from-A" {
		t.Errorf("cache A: val=%q err=%v, want from-A / nil", aVal, aErr)
	}
	if bErr != nil || bVal != 42 {
		t.Errorf("cache B: val=%d err=%v, want 42 / nil — a global singleflight crossed the two caches", bVal, bErr)
	}
}

// TestGetOrLoadCancelledWaiterReturns pins that a caller whose context is
// cancelled while waiting on another caller's in-flight load returns its context
// error rather than blocking (ROADMAP P2-1).
func TestGetOrLoadCancelledWaiterReturns(t *testing.T) {
	c := cache.New(cache.NewMemory())
	const key = "k"

	inLoad := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = cache.GetOrLoad(context.Background(), c, key, func(context.Context) (string, error) {
			close(inLoad)
			<-release
			return "v", nil
		})
	}()
	<-inLoad // the load is in flight and held

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := cache.GetOrLoad(ctx, c, key, func(context.Context) (string, error) {
			return "should-not-run", nil // this caller only waits; it never leads the load
		})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the second caller attach as a waiter
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled waiter did not return — it blocked on the in-flight load")
	}
	close(release) // let the first caller finish
}

// TestGetOrLoadAheadRefreshDrainsAtClose pins that a background refresh-ahead
// task is owned by the Cache and drains at Close rather than outliving it
// (ROADMAP P2-1).
func TestGetOrLoadAheadRefreshDrainsAtClose(t *testing.T) {
	c := cache.New(cache.NewMemory())
	s := c.Namespace("x")
	ctx := context.Background()

	// Seed the key so the next read finds a value to refresh behind.
	if _, err := cache.GetOrLoadAhead(ctx, s, "k", time.Hour, func(context.Context) (string, error) {
		return "v1", nil
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // make the entry's age > 0

	started := make(chan struct{})
	release := make(chan struct{})
	var finished atomic.Bool

	// refreshAfter=0 so the aged entry triggers a background refresh; the refresh
	// loader signals, blocks, then records completion.
	if _, err := cache.GetOrLoadAhead(ctx, s, "k", 0, func(context.Context) (string, error) {
		close(started)
		<-release
		finished.Store(true)
		return "v2", nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started // the refresh goroutine is running

	go func() {
		time.Sleep(20 * time.Millisecond)
		close(release)
	}()
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !finished.Load() {
		t.Fatal("Close returned before the in-flight refresh drained — it is not Cache-owned")
	}
}
