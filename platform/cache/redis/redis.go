// Package redis is the Redis-backed cache store.
//
// It is the ONLY package in the platform that imports a Redis driver, on the
// same principle as platform/database/sql/pgx: the dependency is not hidden,
// it is made locatable. `grep -rl platform/cache/redis` is the complete answer
// to "what depends on Redis?".
//
// A service never imports this package for cache operations — it wires the
// store once in main and depends on cache.Scope thereafter.
//
// Layer: L3 (adapter).
package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/datakaveri/dx-common-go/platform/cache"
)

// Config is the connection configuration. Field names and mapstructure tags
// match the legacy database/redis client so deployed config binds unchanged.
type Config struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`

	// PoolSize defaults to the driver's own sizing when zero.
	PoolSize int `mapstructure:"pool_size"`
	// DialTimeout bounds connection establishment; without it a wedged
	// Redis blocks a request for the driver's default, which is long.
	DialTimeout  time.Duration `mapstructure:"dial_timeout"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

// Store implements cache.Store over Redis.
type Store struct {
	c *goredis.Client
}

// Open connects and verifies reachability.
//
// It PINGs deliberately: a cache that silently fails every operation is worse
// than one that refuses to start, because the failure surfaces later as
// inexplicable latency rather than as a boot error naming the address.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	c := goredis.NewClient(&goredis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	})
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return &Store{c: c}, nil
}

// NewStore wraps an existing client, for a service that already holds one
// during migration.
func NewStore(c *goredis.Client) *Store { return &Store{c: c} }

// Client exposes the driver, for the features the platform has no opinion
// about — streams, Lua, cluster topology. Using it is an explicit step outside
// the abstraction, which is the point of it being named.
func (s *Store) Client() *goredis.Client { return s.c }

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := s.c.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, cache.ErrMiss
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Store) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return s.c.Set(ctx, key, val, ttl).Err()
}

func (s *Store) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return s.c.Del(ctx, keys...).Err()
}

func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	n, err := s.c.Exists(ctx, key).Result()
	return n > 0, err
}

// DeletePrefix removes every key under prefix using SCAN, never KEYS.
//
// KEYS is O(N) over the entire keyspace and blocks the single-threaded server
// for its duration — on a shared Redis that is a platform-wide stall. SCAN is
// incremental, and deleting in batches keeps each pipeline bounded.
func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	const batch = 256
	iter := s.c.Scan(ctx, 0, prefix+"*", batch).Iterator()

	keys := make([]string, 0, batch)
	flush := func() error {
		if len(keys) == 0 {
			return nil
		}
		err := s.c.Del(ctx, keys...).Err()
		keys = keys[:0]
		return err
	}

	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
		if len(keys) >= batch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := iter.Err(); err != nil {
		return err
	}
	return flush()
}

// Lock takes a non-blocking lock via SET NX PX.
//
// Release is guarded by a token compared in Lua, so a holder whose lock already
// expired cannot delete a lock since acquired by someone else — the classic
// way a "working" distributed lock lets two holders run at once.
func (s *Store) Lock(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error) {
	token, err := newToken()
	if err != nil {
		return nil, false, err
	}
	ok, err := s.c.SetNX(ctx, key, token, ttl).Result()
	if err != nil || !ok {
		return nil, false, err
	}
	release := func(rctx context.Context) error {
		return releaseScript.Run(rctx, s.c, []string{key}, token).Err()
	}
	return release, true, nil
}

// releaseScript deletes the key only if it still holds our token.
var releaseScript = goredis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)

// Incr increments and sets the TTL on creation, in one round trip.
//
// EXPIRE is issued unconditionally in the pipeline rather than only on the
// first increment: a counter that somehow lost its TTL would otherwise live
// forever, and re-setting it on a fixed-window key is harmless because the key
// name already changes each window.
func (s *Store) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := s.c.TxPipeline()
	n := pipe.Incr(ctx, key)
	if ttl > 0 {
		pipe.Expire(ctx, key, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return n.Val(), nil
}

func (s *Store) Close() error { return s.c.Close() }

// Check satisfies observability/health.Checker, so a service can register the
// cache as a readiness probe without knowing it is Redis.
func (s *Store) Check(ctx context.Context) error { return s.c.Ping(ctx).Err() }

// newToken returns a random lock token. Randomness matters: a predictable or
// shared token would let one holder release another's lock.
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
