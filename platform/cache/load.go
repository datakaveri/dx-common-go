package cache

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sync/singleflight"
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
//     make an outage worse rather than better. Collapse is scoped to the cache
//     ROOT: sibling scopes share it, two independent caches never do.
//   - **A cancelled waiter returns.** A caller whose context is cancelled while
//     waiting on another caller's in-flight load returns its context error
//     rather than blocking; the load still completes for the callers left.
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
	ch := groupOf(s).DoChan(full, func() (any, error) {
		loaded, lerr := load(ctx)
		if lerr != nil {
			return nil, lerr
		}
		_ = s.Set(ctx, key, loaded) // see the note on write failures above
		return loaded, nil
	})
	return waitTyped[T](ctx, ch, full)
}

// waitTyped waits on a singleflight result while honouring ctx: a cancelled
// waiter returns ctx.Err() rather than blocking on a load another caller
// started (ROADMAP P2-1). The load itself is never cancelled here — it completes
// and populates the cache for the callers still waiting.
func waitTyped[T any](ctx context.Context, ch <-chan singleflight.Result, full string) (T, error) {
	var zero T
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return zero, r.Err
		}
		typed, ok := r.Val.(T)
		if !ok {
			// Only reachable if two call sites share a key with different types,
			// a programming error worth surfacing rather than papering over with
			// a zero value.
			return zero, errors.New("cache: concurrent load returned a different type for key " + full)
		}
		return typed, nil
	}
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

// groupOf returns the per-root singleflight group carried by s. Every scope from
// one root shares it (so sibling scopes collapse a shared load) and two roots
// never do (so independent caches cannot hand each other an in-flight value —
// ROADMAP P2-1). A Scope not produced by New is not expected; if one appears it
// gets a throwaway group — correct, just without cross-call collapse.
func groupOf(s Scope) *singleflight.Group {
	if sc, ok := s.(*scope); ok && sc.loaders != nil {
		return sc.loaders
	}
	return &singleflight.Group{}
}

// runRefresh runs fn on s's refresh executor so it drains at Close, or as a bare
// goroutine when s is not a scope produced by New.
func runRefresh(s Scope, fn func()) {
	if sc, ok := s.(*scope); ok && sc.refresh != nil {
		sc.refresh.run(fn)
		return
	}
	go fn()
}

// GetOrLoadAhead is GetOrLoad with refresh-ahead: a value older than refreshAfter
// is still SERVED, and a single background reload is kicked off.
//
// It exists for the expensive-and-hot case — a value that costs a second to
// compute and is read constantly. Plain GetOrLoad makes whichever unlucky
// request finds the expired key pay that second; this one never does.
//
// The value is wrapped so its load time travels with it, which is why entries
// written by this function are not readable by a plain Get. That is deliberate:
// mixing the two on one key would silently produce misses.
//
// The background reload uses a context DETACHED from the caller's, because the
// request that triggered it returns immediately — an inherited context would be
// cancelled the moment the response is written, so the refresh would never
// complete and every subsequent request would trigger another. It is owned by
// the Cache's refresher, so it drains at Close rather than leaking (ROADMAP P2-1).
func GetOrLoadAhead[T any](
	ctx context.Context,
	s Scope,
	key string,
	refreshAfter time.Duration,
	load func(context.Context) (T, error),
) (T, error) {
	var env envelope[T]
	err := s.Get(ctx, key, &env)
	if err == nil {
		if time.Since(env.At) > refreshAfter {
			// Detached, deduplicated by singleflight, and owned by the refresher
			// so a hot key under refresh neither spawns one goroutine per request
			// nor leaves goroutines running past shutdown.
			runRefresh(s, func() {
				bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
				defer cancel()
				_, _, _ = groupOf(s).Do(s.Key(key)+"\x00refresh", func() (any, error) {
					v, lerr := load(bg)
					if lerr == nil {
						_ = s.Set(bg, key, envelope[T]{Value: v, At: time.Now()})
					}
					return nil, lerr
				})
			})
		}
		return env.Value, nil
	}

	full := s.Key(key)
	ch := groupOf(s).DoChan(full, func() (any, error) {
		loaded, lerr := load(ctx)
		if lerr != nil {
			return nil, lerr
		}
		_ = s.Set(ctx, key, envelope[T]{Value: loaded, At: time.Now()})
		return loaded, nil
	})
	return waitTyped[T](ctx, ch, full)
}

// refreshTimeout bounds a background reload. Without it a wedged loader leaks a
// goroutine for the process's lifetime.
const refreshTimeout = 30 * time.Second

// envelope carries a value with the time it was loaded.
type envelope[T any] struct {
	Value T         `json:"v"`
	At    time.Time `json:"at"`
}
