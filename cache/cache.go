package cache

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache provides a simple caching interface
type Cache interface {
	Get(ctx context.Context, key string) (string, error)
	GetJSON(ctx context.Context, key string, dest interface{}) error
	Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	Clear(ctx context.Context) error
}

// RedisCache wraps a Redis client
type RedisCache struct {
	client *redis.Client
}

// NewRedisCache creates a new Redis cache wrapper
func NewRedisCache(client *redis.Client) *RedisCache {
	return &RedisCache{
		client: client,
	}
}

// Get retrieves a string value from cache
func (rc *RedisCache) Get(ctx context.Context, key string) (string, error) {
	val, err := rc.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil // Key doesn't exist
	}
	return val, err
}

// GetJSON retrieves and unmarshals a JSON value from cache
func (rc *RedisCache) GetJSON(ctx context.Context, key string, dest interface{}) error {
	val, err := rc.Get(ctx, key)
	if err != nil {
		return err
	}

	if val == "" {
		return redis.Nil
	}

	return json.Unmarshal([]byte(val), dest)
}

// Set stores a value in cache with TTL
func (rc *RedisCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	var val interface{}

	// If value is not a string, marshal to JSON
	if s, ok := value.(string); ok {
		val = s
	} else {
		jsonBytes, err := json.Marshal(value)
		if err != nil {
			return err
		}
		val = string(jsonBytes)
	}

	return rc.client.Set(ctx, key, val, ttl).Err()
}

// Delete removes a key from cache
func (rc *RedisCache) Delete(ctx context.Context, key string) error {
	return rc.client.Del(ctx, key).Err()
}

// Exists checks if a key exists in cache
func (rc *RedisCache) Exists(ctx context.Context, key string) (bool, error) {
	count, err := rc.client.Exists(ctx, key).Result()
	return count > 0, err
}

// Clear removes all keys from cache (use with caution)
func (rc *RedisCache) Clear(ctx context.Context) error {
	return rc.client.FlushDB(ctx).Err()
}

// MemoryCache is an in-memory cache for local development/testing
type MemoryCache struct {
	data map[string]cacheEntry
	ttl  map[string]time.Time
}

type cacheEntry struct {
	value string
}

// NewMemoryCache creates a new in-memory cache
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		data: make(map[string]cacheEntry),
		ttl:  make(map[string]time.Time),
	}
}

// Get retrieves a value from memory cache
func (mc *MemoryCache) Get(ctx context.Context, key string) (string, error) {
	// Check if expired
	if expiry, ok := mc.ttl[key]; ok {
		if time.Now().After(expiry) {
			delete(mc.data, key)
			delete(mc.ttl, key)
			return "", nil
		}
	}

	if entry, ok := mc.data[key]; ok {
		return entry.value, nil
	}

	return "", nil
}

// GetJSON retrieves and unmarshals a JSON value from memory cache
func (mc *MemoryCache) GetJSON(ctx context.Context, key string, dest interface{}) error {
	val, err := mc.Get(ctx, key)
	if err != nil {
		return err
	}

	if val == "" {
		return redis.Nil
	}

	return json.Unmarshal([]byte(val), dest)
}

// Set stores a value in memory cache with TTL
func (mc *MemoryCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	var val string

	if s, ok := value.(string); ok {
		val = s
	} else {
		jsonBytes, _ := json.Marshal(value)
		val = string(jsonBytes)
	}

	mc.data[key] = cacheEntry{value: val}

	if ttl > 0 {
		mc.ttl[key] = time.Now().Add(ttl)
	} else {
		delete(mc.ttl, key) // No expiry
	}

	return nil
}

// Delete removes a key from memory cache
func (mc *MemoryCache) Delete(ctx context.Context, key string) error {
	delete(mc.data, key)
	delete(mc.ttl, key)
	return nil
}

// Exists checks if a key exists in memory cache
func (mc *MemoryCache) Exists(ctx context.Context, key string) (bool, error) {
	// Check if expired
	if expiry, ok := mc.ttl[key]; ok {
		if time.Now().After(expiry) {
			delete(mc.data, key)
			delete(mc.ttl, key)
			return false, nil
		}
	}

	_, ok := mc.data[key]
	return ok, nil
}

// Clear removes all keys from memory cache
func (mc *MemoryCache) Clear(ctx context.Context) error {
	mc.data = make(map[string]cacheEntry)
	mc.ttl = make(map[string]time.Time)
	return nil
}

// CacheHelper provides common caching patterns
type CacheHelper struct {
	cache Cache
	ttl   time.Duration
}

// NewCacheHelper creates a cache helper
func NewCacheHelper(cache Cache, ttl time.Duration) *CacheHelper {
	return &CacheHelper{
		cache: cache,
		ttl:   ttl,
	}
}

// GetOrFetch gets from cache or fetches via function and caches result
func (ch *CacheHelper) GetOrFetch(ctx context.Context, key string, fetch func() (interface{}, error)) (interface{}, error) {
	// Try cache first
	val, err := ch.cache.Get(ctx, key)
	if err == nil && val != "" {
		return val, nil
	}

	// Fetch and cache
	result, err := fetch()
	if err != nil {
		return nil, err
	}

	_ = ch.cache.Set(ctx, key, result, ch.ttl)
	return result, nil
}

// GetJSONOrFetch gets JSON from cache or fetches and caches result
func (ch *CacheHelper) GetJSONOrFetch(ctx context.Context, key string, dest interface{}, fetch func() (interface{}, error)) error {
	// Try cache first
	err := ch.cache.GetJSON(ctx, key, dest)
	if err == nil {
		return nil
	}

	// Fetch and cache
	result, err := fetch()
	if err != nil {
		return err
	}

	return ch.cache.Set(ctx, key, result, ch.ttl)
}

// CacheKey helper to create consistent cache keys
func CacheKey(prefix string, parts ...string) string {
	key := prefix
	for _, part := range parts {
		key += ":" + part
	}
	return key
}
