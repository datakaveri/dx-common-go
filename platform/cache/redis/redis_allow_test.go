package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/dxtest/containers"
	rediscache "github.com/datakaveri/dx-common-go/platform/cache/redis"
)

// Allow's contract is the fixed window on a STATIC key. The failure it guards
// against is not "off by one" — it is a counter whose TTL keeps being pushed
// out, so a steady caller is locked out permanently instead of for one window.
func TestAllowFixedWindowOnAStaticKey(t *testing.T) {
	h := containers.Redis(t)
	ctx := context.Background()

	store, err := rediscache.Open(ctx, rediscache.Config{Addr: h.Addr})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const key = "test:allow:static"
	const limit = 3
	window := 300 * time.Millisecond

	for i := 1; i <= limit; i++ {
		ok, err := store.Allow(ctx, key, limit, window)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("call %d denied, want allowed within the limit", i)
		}
	}

	ok, err := store.Allow(ctx, key, limit, window)
	if err != nil {
		t.Fatalf("over-limit call: %v", err)
	}
	if ok {
		t.Fatal("call over the limit was allowed")
	}

	// The window must actually expire. Keep calling throughout — this is what
	// distinguishes a fixed window from Incr's sliding one: with the TTL
	// re-armed on every call the key would never expire and this never recovers.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if ok, err := store.Allow(ctx, key, limit, window); err == nil && ok {
			return // window rotated
		}
	}
	t.Fatal("the window never reset while calls kept arriving — the TTL is being " +
		"re-armed per call, so a steady caller is blocked forever rather than for one window")
}

func TestAllowRejectsANonPositiveLimit(t *testing.T) {
	h := containers.Redis(t)
	ctx := context.Background()
	store, err := rediscache.Open(ctx, rediscache.Config{Addr: h.Addr})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if ok, _ := store.Allow(ctx, "test:allow:zero", 0, time.Minute); ok {
		t.Error("a limit of zero allowed a call — it must deny, not mean unlimited")
	}
}
