package cache

import (
	"context"
	"errors"
	"sync"
)

// GetOrLoad is cache-aside: return the cached value, or load it, store it and
// return it.
//
// This is the pattern services actually want, and having it here rather than in
// each service is the difference between a cache module and a Redis wrapper.
// Written by hand it is ~15 lines that everyone gets subtly differently — some
// cache the error, some cache the zero value, most have a stampede.
//
// Three properties worth knowing:
//
//   - **Concurrent loads for the same key collapse into one.** A cold key hit
//     by 500 requests calls load ONCE; the rest wait for that result. Without
//     this, expiry of a hot key sends every in-flight request to the database
//     at the same instant — the cache stampede, and the reason a cache can
//     make an outage worse rather than better.
//   - **A load error is never cached.** The next caller retries. Caching a
//     failure turns a transient database blip into a sustained outage for the
//     length of the TTL.
//   - **A cache write failure does not fail the request.** The value was
//     loaded successfully; a degraded cache should cost latency, not
//     correctness. The error is deliberately swallowed here — if it mattered,
//     the caller would have used Set directly.
//
// It is a free function rather than a method because Go does not permit type
// parameters on methods, and a typed value is worth more than a fluent call:
//
//	u, err := cache.GetOrLoad(ctx, users, id, func(ctx context.Context) (User, error) {
//	    return repo.FindUser(ctx, id)
//	})
func GetOrLoad[T any](ctx context.Context, s Scope, key string, load func(context.Context) (T, error)) (T, error) {
	var zero T

	var out T
	err := s.Get(ctx, key, &out)
	switch {
	case err == nil:
		return out, nil
	case !errors.Is(err, ErrMiss):
		// A backend that is erroring is not a reason to fail the request:
		// fall through to the loader. The cache is an optimisation, and a
		// Redis outage should degrade latency, not availability.
	}

	full := s.Key(key)
	v, err, _ := loaders.Do(full, func() (any, error) {
		loaded, lerr := load(ctx)
		if lerr != nil {
			return nil, lerr
		}
		_ = s.Set(ctx, key, loaded) // see the note on write failures above
		return loaded, nil
	})
	if err != nil {
		return zero, err
	}

	typed, ok := v.(T)
	if !ok {
		// Only reachable if two call sites share a key with different types,
		// which is a programming error worth surfacing rather than papering
		// over with a zero value.
		return zero, errors.New("cache: concurrent load returned a different type for key " + full)
	}
	return typed, nil
}

// Invalidating writes through: it runs the mutation, then drops the key.
//
// Delete-after-write rather than write-through on purpose. Writing the new
// value into the cache means trusting that what the caller passed matches what
// the store actually persisted — after defaults, triggers and constraints. A
// delete is always correct, and the next read repopulates from the source of
// truth.
//
// The delete runs even when fn fails: a partially-applied write leaves the
// cached copy suspect either way, and a spurious miss is cheaper than serving
// a stale value indefinitely.
func Invalidating(ctx context.Context, s Scope, key string, fn func(context.Context) error) error {
	err := fn(ctx)
	_ = s.Delete(ctx, key)
	return err
}

// loaders collapses concurrent loads. It is package-level state, which the
// platform otherwise forbids — justified because it holds no configuration and
// no connection, only in-flight call bookkeeping keyed by fully-qualified cache
// key. Per-Cache instances would not collapse loads across two scopes derived
// from the same root, which is the common case.
var loaders singleflight

// singleflight deduplicates concurrent calls sharing a key.
//
// Implemented here rather than taking golang.org/x/sync/singleflight: it is
// ~30 lines, and platform/cache is L3 — a dependency added at this layer is
// inherited by every service in the fleet, so the bar for adding one is high.
type singleflight struct {
	mu sync.Mutex
	m  map[string]*call
}

type call struct {
	wg  sync.WaitGroup
	val any
	err error
}

// Do runs fn unless a call for key is already in flight, in which case it waits
// and returns that call's result. shared reports whether the result came from
// another caller's execution.
func (g *singleflight) Do(key string, fn func() (any, error)) (v any, err error, shared bool) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err, true
	}
	c := new(call)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	// Recover so a panicking loader does not leave every waiter blocked on a
	// WaitGroup that is never released.
	defer func() {
		if r := recover(); r != nil {
			c.err = errors.New("cache: loader panicked")
			g.finish(key, c)
			panic(r)
		}
	}()

	c.val, c.err = fn()
	g.finish(key, c)
	return c.val, c.err, false
}

func (g *singleflight) finish(key string, c *call) {
	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()
	c.wg.Done()
}
