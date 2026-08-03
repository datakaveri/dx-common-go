// Package cache is the platform's caching layer.
//
// Services depend on this package, never on a cache driver. Nothing in a
// service's code should reveal whether the backend is Redis, an in-process map,
// or something else — that is the whole point, and it is what lets a deployment
// swap backends, or run a service with no Redis at all in tests, without
// touching business logic.
//
// The API is a scoped, fluent DSL rather than a flat Get/Set:
//
//	users := c.Namespace("users").TTL(5 * time.Minute)
//	if err := users.Get(ctx, id, &u); errors.Is(err, cache.ErrMiss) { … }
//
// A Scope is an immutable value. Deriving one never mutates the parent, so a
// scope built once at construction can be shared across goroutines and further
// narrowed per call site without coordination.
//
// # Why namespaces are mandatory-feeling
//
// Every key is written under its namespace prefix, which makes two things
// possible that a flat keyspace cannot offer: bulk invalidation of one logical
// set (Invalidate), and a guarantee that two services sharing one Redis cannot
// collide on a bare key like "user:1".
//
// Layer: L3 (adapter). Depends on L0 only.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrMiss reports that a key is absent or expired.
//
// A miss is NOT an error condition in the usual sense — it is the common case a
// cache exists to handle. It is a sentinel rather than a (value, bool) return
// so that a miss propagates through GetOrLoad and the other helpers uniformly,
// and so a caller that forgets to check it fails loudly instead of silently
// operating on a zero value.
var ErrMiss = errors.New("cache: miss")

// ErrLockHeld reports that a distributed lock is held elsewhere. Lock does not
// block, so this is the expected outcome under contention, not a failure.
var ErrLockHeld = errors.New("cache: lock held by another holder")

// keySeparator joins namespace segments. Colon is the Redis convention and is
// what every existing key in the fleet already uses.
const keySeparator = ":"

// Cache is the entry point, and is itself the root Scope.
//
// Close is on Cache and not on Scope: a scope is a view, and letting a view
// close the underlying connection would make lifetime ownership ambiguous.
type Cache interface {
	Scope
	Close() error
}

// Scope is a namespaced, TTL-bearing view of the cache.
//
// Namespace and TTL return NEW scopes; the receiver is never modified. That
// immutability is what makes a Scope safe to store on a struct and share.
type Scope interface {
	// Namespace returns a scope nested under name. Calls compose:
	// c.Namespace("org").Namespace("members") keys under "org:members".
	Namespace(name string) Scope
	// TTL returns a scope whose writes expire after d. Zero means no
	// expiry — permitted, but a cache entry that never expires is a leak
	// unless something invalidates it, so prefer an explicit TTL.
	TTL(d time.Duration) Scope

	// Get decodes the value at key into dest, or returns ErrMiss.
	Get(ctx context.Context, key string, dest any) error
	// Set encodes and stores value under this scope's TTL.
	Set(ctx context.Context, key string, value any) error
	// SetTTL is Set with a one-off TTL, for the case where a single entry
	// differs from its scope's default.
	SetTTL(ctx context.Context, key string, value any, ttl time.Duration) error
	// Delete removes keys. Deleting an absent key is not an error.
	Delete(ctx context.Context, keys ...string) error
	// Exists reports presence without decoding — cheaper than Get when the
	// value is not needed.
	Exists(ctx context.Context, key string) (bool, error)

	// Invalidate removes every key in this scope. It is the reason
	// namespaces exist: without a prefix there is no way to drop one
	// logical set without dropping everything.
	Invalidate(ctx context.Context) error

	// Lock runs fn holding a distributed lock on key, returning ErrLockHeld
	// immediately if another holder has it. It does NOT block.
	//
	// Use it to make a non-idempotent job single-flighted across replicas.
	// For cache stampedes prefer GetOrLoad, which already collapses
	// concurrent loads in-process without a round trip.
	Lock(ctx context.Context, key string, ttl time.Duration, fn func(context.Context) error) error

	// Allow reports whether an action keyed by key is within limit for the
	// current window, and how many remain. It is a fixed window, not a
	// sliding one: cheap (one atomic op), predictable, and adequate for
	// protecting a backend. It permits up to 2x limit across a window
	// boundary — if that matters, this is the wrong primitive.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, remaining int, err error)

	// Key renders the fully-qualified key, for logging and for the rare
	// caller that must hand a real key to something outside this package.
	Key(key string) string
}

// Store is the driver seam. One implementation per backend; services never see
// it. It speaks bytes, not values, so encoding is decided once here rather than
// differently by each backend.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error) // ErrMiss when absent
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
	Exists(ctx context.Context, key string) (bool, error)
	// DeletePrefix removes every key under prefix, backing Invalidate.
	DeletePrefix(ctx context.Context, prefix string) error
	// Lock acquires a non-blocking lock. acquired=false means held elsewhere.
	Lock(ctx context.Context, key string, ttl time.Duration) (release func(context.Context) error, acquired bool, err error)
	// Incr atomically increments a counter, setting ttl on first creation,
	// and returns the new value. Atomicity is the whole requirement: a
	// read-modify-write would let concurrent requests share a slot and
	// silently exceed the limit under exactly the load a limiter exists for.
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
	Close() error
}

// New builds a Cache over a Store.
func New(s Store, opts ...Option) Cache {
	c := &scope{store: s}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Option configures the root scope.
type Option func(*scope)

// WithDefaultTTL sets the TTL inherited by scopes that do not set their own.
func WithDefaultTTL(d time.Duration) Option { return func(s *scope) { s.ttl = d } }

// WithPrefix sets a root namespace, typically the service name, so several
// services can share one Redis without colliding.
func WithPrefix(p string) Option { return func(s *scope) { s.prefix = p } }

// scope implements both Cache and Scope. One type serves both because the root
// IS a scope — a separate root type would duplicate every method to no benefit.
type scope struct {
	store  Store
	prefix string
	ttl    time.Duration
}

func (s *scope) Namespace(name string) Scope {
	next := *s
	next.prefix = joinKey(s.prefix, name)
	return &next
}

func (s *scope) TTL(d time.Duration) Scope {
	next := *s
	next.ttl = d
	return &next
}

func (s *scope) Key(key string) string { return joinKey(s.prefix, key) }

func (s *scope) Get(ctx context.Context, key string, dest any) error {
	b, err := s.store.Get(ctx, s.Key(key))
	if err != nil {
		return err // ErrMiss propagates unchanged
	}
	if err := json.Unmarshal(b, dest); err != nil {
		// A value that will not decode is corrupt or was written under a
		// different type. Treat it as a MISS and drop it: the caller then
		// reloads from source and self-heals, where returning an error
		// would wedge the endpoint until someone flushed the key by hand.
		_ = s.store.Delete(ctx, s.Key(key))
		return ErrMiss
	}
	return nil
}

func (s *scope) Set(ctx context.Context, key string, value any) error {
	return s.SetTTL(ctx, key, value, s.ttl)
}

func (s *scope) SetTTL(ctx context.Context, key string, value any, ttl time.Duration) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cache: encode %s: %w", s.Key(key), err)
	}
	return s.store.Set(ctx, s.Key(key), b, ttl)
}

func (s *scope) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = s.Key(k)
	}
	return s.store.Delete(ctx, full...)
}

func (s *scope) Exists(ctx context.Context, key string) (bool, error) {
	return s.store.Exists(ctx, s.Key(key))
}

func (s *scope) Invalidate(ctx context.Context) error {
	if s.prefix == "" {
		// Refuse to flush the entire keyspace through an ordinary scope.
		// An unprefixed Invalidate is almost always a bug — a namespace was
		// meant and forgotten — and in a shared Redis it would take out
		// every other service's cache too.
		return errors.New("cache: Invalidate requires a namespace; refusing to flush the root scope")
	}
	return s.store.DeletePrefix(ctx, s.prefix+keySeparator)
}

func (s *scope) Lock(ctx context.Context, key string, ttl time.Duration, fn func(context.Context) error) error {
	release, acquired, err := s.store.Lock(ctx, s.Key(key), ttl)
	if err != nil {
		return err
	}
	if !acquired {
		return ErrLockHeld
	}
	// Release on a context that cannot already be cancelled: a lock we fail
	// to release is held until its TTL expires, which is a stall for every
	// other replica.
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = release(rctx)
	}()
	return fn(ctx)
}

// Allow implements a fixed-window limiter.
//
// The window is derived from the clock rather than from first use, so every
// replica agrees on the boundary without coordination — two gateway pods
// limiting the same subject must not each grant a full quota.
func (s *scope) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error) {
	if limit <= 0 {
		return false, 0, nil
	}
	slot := time.Now().UnixNano() / int64(window)
	windowKey := fmt.Sprintf("%s%s%d", s.Key(key), keySeparator, slot)

	n, err := s.store.Incr(ctx, windowKey, window)
	if err != nil {
		// Fail OPEN. A limiter is a protection, not a gate: if the cache is
		// down, refusing all traffic converts a cache outage into a total
		// outage. Callers that need fail-closed must check err themselves.
		return true, 0, err
	}
	remaining := limit - int(n)
	if remaining < 0 {
		remaining = 0
	}
	return int(n) <= limit, remaining, nil
}

func (s *scope) Close() error { return s.store.Close() }

func joinKey(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.Trim(p, keySeparator); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, keySeparator)
}
