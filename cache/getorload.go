package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"
)

// loadGroup dedups concurrent loads process-wide by cache key (stampede
// protection): under N concurrent misses of the same key, load runs once.
var loadGroup singleflight.Group

// GetOrLoad returns the cached value for key, or runs load exactly once per
// in-flight key (singleflight), caches the result (best-effort, JSON) and
// returns it. A cache miss is never an error; load errors are returned as-is
// and nothing is cached. One key must always decode into the same T.
func GetOrLoad[T any](ctx context.Context, c Cache, key string, ttl time.Duration, load func(context.Context) (T, error)) (T, error) {
	var zero T
	if v, ok := cached[T](ctx, c, key); ok {
		return v, nil
	}
	out, err, _ := loadGroup.Do(key, func() (any, error) {
		if v, ok := cached[T](ctx, c, key); ok { // filled while we waited
			return v, nil
		}
		v, err := load(ctx)
		if err != nil {
			return zero, err
		}
		if b, merr := json.Marshal(v); merr == nil {
			_ = c.Set(ctx, key, string(b), ttl) // best-effort: a failed write is a future miss
		}
		return v, nil
	})
	if err != nil {
		return zero, err
	}
	v, ok := out.(T)
	if !ok {
		// Unreachable in correct use — the singleflight result is the value
		// load returned, always a T. Handled rather than a bare assertion so a
		// future misuse surfaces as an error, not a panic on the hot path.
		return zero, fmt.Errorf("cache: GetOrLoad key %q decoded into an unexpected type", key)
	}
	return v, nil
}

// cached reads and decodes key; any error or decode failure is a miss.
func cached[T any](ctx context.Context, c Cache, key string) (T, bool) {
	var v T
	s, err := c.Get(ctx, key)
	if err != nil || s == "" {
		return v, false
	}
	if json.Unmarshal([]byte(s), &v) != nil {
		return v, false // corrupt entry → reload overwrites it
	}
	return v, true
}
