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

type user struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func newCache(opts ...cache.Option) cache.Cache {
	return cache.New(cache.NewMemory(), opts...)
}

func TestRoundTrip(t *testing.T) {
	c := newCache()
	users := c.Namespace("users").TTL(time.Minute)
	ctx := context.Background()

	if err := users.Set(ctx, "1", user{ID: "1", Name: "A"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	var got user
	if err := users.Get(ctx, "1", &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "A" {
		t.Errorf("got %+v, want name A", got)
	}
}

// A miss must be distinguishable from an error. Callers branch on it, so
// returning something else would make every cache-aside path wrong.
func TestMissIsSentinel(t *testing.T) {
	c := newCache()
	var u user
	err := c.Namespace("users").Get(context.Background(), "absent", &u)
	if !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("err = %v, want ErrMiss", err)
	}
}

// Namespaces must actually isolate: the whole point is that two services
// sharing one Redis cannot collide on a bare key.
func TestNamespacesIsolate(t *testing.T) {
	c := newCache()
	ctx := context.Background()
	a, b := c.Namespace("a"), c.Namespace("b")

	if err := a.Set(ctx, "k", user{Name: "from-a"}); err != nil {
		t.Fatal(err)
	}
	var got user
	if err := b.Get(ctx, "k", &got); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("namespace b saw namespace a's key: %v / %+v", err, got)
	}
}

func TestNamespacesCompose(t *testing.T) {
	c := newCache(cache.WithPrefix("svc"))
	got := c.Namespace("org").Namespace("members").Key("7")
	if got != "svc:org:members:7" {
		t.Errorf("Key = %q, want svc:org:members:7", got)
	}
}

// Deriving a scope must not mutate its parent — scopes are stored on structs
// and shared across goroutines.
func TestScopesAreImmutable(t *testing.T) {
	c := newCache()
	base := c.Namespace("base")
	_ = base.Namespace("child").TTL(time.Hour)

	if got := base.Key("k"); got != "base:k" {
		t.Errorf("parent scope mutated: Key = %q, want base:k", got)
	}
}

func TestInvalidateDropsOnlyItsNamespace(t *testing.T) {
	c := newCache()
	ctx := context.Background()
	keep, drop := c.Namespace("keep"), c.Namespace("drop")

	if err := keep.Set(ctx, "k", user{Name: "keep"}); err != nil {
		t.Fatal(err)
	}
	if err := drop.Set(ctx, "k", user{Name: "drop"}); err != nil {
		t.Fatal(err)
	}
	if err := drop.Invalidate(ctx); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	var got user
	if err := keep.Get(ctx, "k", &got); err != nil {
		t.Errorf("Invalidate removed another namespace's key: %v", err)
	}
	if err := drop.Get(ctx, "k", &got); !errors.Is(err, cache.ErrMiss) {
		t.Errorf("Invalidate left its own key: %v", err)
	}
}

// Invalidating the root scope would flush every other service's cache on a
// shared Redis. It must refuse.
func TestInvalidateRefusesRootScope(t *testing.T) {
	if err := newCache().Invalidate(context.Background()); err == nil {
		t.Fatal("root Invalidate was allowed; on a shared Redis that flushes the whole keyspace")
	}
}

func TestTTLExpires(t *testing.T) {
	c := newCache()
	ctx := context.Background()
	s := c.Namespace("short").TTL(20 * time.Millisecond)

	if err := s.Set(ctx, "k", user{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)

	var got user
	if err := s.Get(ctx, "k", &got); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("entry outlived its TTL: %v", err)
	}
}

// A corrupt or type-mismatched entry must read as a miss and self-heal, not
// wedge the endpoint until someone flushes the key by hand.
func TestUndecodableValueReadsAsMissAndSelfHeals(t *testing.T) {
	c := newCache()
	ctx := context.Background()
	s := c.Namespace("n")

	if err := s.Set(ctx, "k", "a plain string"); err != nil {
		t.Fatal(err)
	}
	var got user
	if err := s.Get(ctx, "k", &got); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("err = %v, want ErrMiss for an undecodable entry", err)
	}
	if ok, _ := s.Exists(ctx, "k"); ok {
		t.Error("the poison entry was left in place; the next read would fail identically")
	}
}

// The stampede guarantee: a cold key hit concurrently must load ONCE.
func TestGetOrLoadCollapsesConcurrentLoads(t *testing.T) {
	c := newCache()
	s := c.Namespace("users").TTL(time.Minute)

	var calls atomic.Int32
	release := make(chan struct{})
	load := func(context.Context) (user, error) {
		calls.Add(1)
		<-release // hold every caller inside the loader
		return user{ID: "1", Name: "loaded"}, nil
	}

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := cache.GetOrLoad(context.Background(), s, "1", load)
			if err != nil {
				errs <- err
				return
			}
			if u.Name != "loaded" {
				errs <- errors.New("wrong value: " + u.Name)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond) // let them pile up on the same key
	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent GetOrLoad: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("loader ran %d times, want 1 — this is a cache stampede", got)
	}
}

// A failed load must not be cached: caching failure turns a transient blip
// into a sustained outage for the length of the TTL.
func TestGetOrLoadDoesNotCacheErrors(t *testing.T) {
	c := newCache()
	s := c.Namespace("users")
	ctx := context.Background()
	boom := errors.New("boom")

	var calls int
	load := func(context.Context) (user, error) {
		calls++
		if calls == 1 {
			return user{}, boom
		}
		return user{Name: "recovered"}, nil
	}

	if _, err := cache.GetOrLoad(ctx, s, "1", load); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	u, err := cache.GetOrLoad(ctx, s, "1", load)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if u.Name != "recovered" {
		t.Errorf("got %+v — the failure was cached", u)
	}
}

func TestGetOrLoadServesFromCache(t *testing.T) {
	c := newCache()
	s := c.Namespace("users").TTL(time.Minute)
	ctx := context.Background()

	var calls int
	load := func(context.Context) (user, error) {
		calls++
		return user{Name: "loaded"}, nil
	}
	for i := 0; i < 3; i++ {
		if _, err := cache.GetOrLoad(ctx, s, "1", load); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("loader ran %d times, want 1 — the value was not cached", calls)
	}
}

func TestInvalidatingDropsTheKeyEvenOnFailure(t *testing.T) {
	c := newCache()
	s := c.Namespace("users").TTL(time.Minute)
	ctx := context.Background()
	boom := errors.New("boom")

	if err := s.Set(ctx, "1", user{Name: "stale"}); err != nil {
		t.Fatal(err)
	}
	if err := cache.Invalidating(ctx, s, "1", func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	var got user
	if err := s.Get(ctx, "1", &got); !errors.Is(err, cache.ErrMiss) {
		t.Error("a partially-applied write left its cached copy in place")
	}
}

func TestLockExcludes(t *testing.T) {
	c := newCache()
	ctx := context.Background()
	s := c.Namespace("jobs")

	inner := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Lock(ctx, "j", time.Minute, func(context.Context) error {
			close(inner)
			time.Sleep(80 * time.Millisecond)
			return nil
		})
	}()
	<-inner

	err := s.Lock(ctx, "j", time.Minute, func(context.Context) error {
		t.Error("second holder ran while the lock was held")
		return nil
	})
	if !errors.Is(err, cache.ErrLockHeld) {
		t.Fatalf("err = %v, want ErrLockHeld", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("first holder: %v", err)
	}

	// Released after the first holder returns.
	if err := s.Lock(ctx, "j", time.Minute, func(context.Context) error { return nil }); err != nil {
		t.Errorf("lock not released: %v", err)
	}
}
