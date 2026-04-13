package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// SetJSON marshals v as JSON and stores it in Redis under key with the given TTL.
// A zero TTL means the key never expires.
func SetJSON[T any](ctx context.Context, c *Client, key string, v T, ttl time.Duration) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("redis.SetJSON: marshal: %w", err)
	}
	if err := c.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("redis.SetJSON: set: %w", err)
	}
	return nil
}

// GetJSON retrieves the value stored at key and unmarshals it into dest.
// Returns (false, nil) when the key does not exist.
func GetJSON[T any](ctx context.Context, c *Client, key string, dest *T) (bool, error) {
	data, err := c.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("redis.GetJSON: get: %w", err)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return false, fmt.Errorf("redis.GetJSON: unmarshal: %w", err)
	}
	return true, nil
}

// Delete removes one or more keys from Redis.
func Delete(ctx context.Context, c *Client, keys ...string) error {
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("redis.Delete: %w", err)
	}
	return nil
}

// Exists returns true if key exists in Redis.
func Exists(ctx context.Context, c *Client, key string) (bool, error) {
	n, err := c.rdb.Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("redis.Exists: %w", err)
	}
	return n > 0, nil
}

// TTL returns the remaining time-to-live of key.
// Returns -1 if the key has no expiry; -2 if it does not exist.
func TTL(ctx context.Context, c *Client, key string) (time.Duration, error) {
	d, err := c.rdb.TTL(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("redis.TTL: %w", err)
	}
	return d, nil
}

// Increment atomically increments key by 1 and returns the new value.
func Increment(ctx context.Context, c *Client, key string) (int64, error) {
	n, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("redis.Increment: %w", err)
	}
	return n, nil
}
