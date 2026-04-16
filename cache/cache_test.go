package cache

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type TestData struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func TestMemoryCache_Set_Get(t *testing.T) {
	cache := NewMemoryCache()
	ctx := context.Background()

	// Set value
	err := cache.Set(ctx, "key1", "value1", 1*time.Hour)
	if err != nil {
		t.Fatalf("set failed: %v", err)
	}

	// Get value
	val, err := cache.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	if val != "value1" {
		t.Fatalf("expected 'value1', got %q", val)
	}
}

func TestMemoryCache_GetJSON(t *testing.T) {
	cache := NewMemoryCache()
	ctx := context.Background()

	data := TestData{Name: "John", Age: 30}
	cache.Set(ctx, "user:1", data, 1*time.Hour)

	var retrieved TestData
	err := cache.GetJSON(ctx, "user:1", &retrieved)
	if err != nil {
		t.Fatalf("getjson failed: %v", err)
	}

	if retrieved.Name != "John" || retrieved.Age != 30 {
		t.Fatalf("expected {John 30}, got {%s %d}", retrieved.Name, retrieved.Age)
	}
}

func TestMemoryCache_Delete(t *testing.T) {
	cache := NewMemoryCache()
	ctx := context.Background()

	cache.Set(ctx, "key1", "value1", 1*time.Hour)
	cache.Delete(ctx, "key1")

	val, _ := cache.Get(ctx, "key1")
	if val != "" {
		t.Fatal("expected deleted key to return empty string")
	}
}

func TestMemoryCache_Exists(t *testing.T) {
	cache := NewMemoryCache()
	ctx := context.Background()

	cache.Set(ctx, "key1", "value1", 1*time.Hour)

	exists, _ := cache.Exists(ctx, "key1")
	if !exists {
		t.Fatal("expected key to exist")
	}

	exists, _ = cache.Exists(ctx, "nonexistent")
	if exists {
		t.Fatal("expected key to not exist")
	}
}

func TestMemoryCache_TTL_Expiration(t *testing.T) {
	cache := NewMemoryCache()
	ctx := context.Background()

	cache.Set(ctx, "key1", "value1", 1*time.Millisecond)
	time.Sleep(2 * time.Millisecond)

	val, _ := cache.Get(ctx, "key1")
	if val != "" {
		t.Fatal("expected expired key to return empty string")
	}
}

func TestMemoryCache_Clear(t *testing.T) {
	cache := NewMemoryCache()
	ctx := context.Background()

	cache.Set(ctx, "key1", "value1", 1*time.Hour)
	cache.Set(ctx, "key2", "value2", 1*time.Hour)

	cache.Clear(ctx)

	val1, _ := cache.Get(ctx, "key1")
	val2, _ := cache.Get(ctx, "key2")

	if val1 != "" || val2 != "" {
		t.Fatal("expected all keys to be cleared")
	}
}

func TestCacheHelper_GetOrFetch_CacheHit(t *testing.T) {
	cache := NewMemoryCache()
	ctx := context.Background()

	helper := NewCacheHelper(cache, 1*time.Hour)

	// Pre-populate cache
	cache.Set(ctx, "data:1", "cached_value", 1*time.Hour)

	// GetOrFetch should return cached value
	result, err := helper.GetOrFetch(ctx, "data:1", func() (interface{}, error) {
		t.Fatal("should not call fetch when cache hit")
		return nil, nil
	})

	if err != nil || result != "cached_value" {
		t.Fatalf("expected cached value, got %v", result)
	}
}

func TestCacheHelper_GetOrFetch_CacheMiss(t *testing.T) {
	cache := NewMemoryCache()
	ctx := context.Background()

	helper := NewCacheHelper(cache, 1*time.Hour)

	// GetOrFetch should call fetch function
	result, err := helper.GetOrFetch(ctx, "data:1", func() (interface{}, error) {
		return "fetched_value", nil
	})

	if err != nil || result != "fetched_value" {
		t.Fatalf("expected fetched value, got %v", result)
	}

	// Verify it's cached
	cached, _ := cache.Get(ctx, "data:1")
	if cached == "" {
		t.Fatal("expected value to be cached")
	}
}

func TestCacheHelper_GetJSONOrFetch(t *testing.T) {
	cache := NewMemoryCache()
	ctx := context.Background()

	helper := NewCacheHelper(cache, 1*time.Hour)

	originalData := TestData{Name: "Alice", Age: 25}

	var retrieved TestData
	err := helper.GetJSONOrFetch(ctx, "user:2", &retrieved, func() (interface{}, error) {
		return originalData, nil
	})

	if err != nil {
		t.Fatalf("getjsonorfetch failed: %v", err)
	}

	if retrieved.Name != "Alice" || retrieved.Age != 25 {
		t.Fatalf("expected {Alice 25}, got {%s %d}", retrieved.Name, retrieved.Age)
	}
}

func TestCacheKey(t *testing.T) {
	key := CacheKey("user", "123", "profile")
	if key != "user:123:profile" {
		t.Fatalf("expected 'user:123:profile', got %q", key)
	}
}

func TestMemoryCache_GetJSON_MissingKey(t *testing.T) {
	cache := NewMemoryCache()
	ctx := context.Background()

	var data TestData
	err := cache.GetJSON(ctx, "nonexistent", &data)

	// Should return error for missing key
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestMemoryCache_SetJSON(t *testing.T) {
	cache := NewMemoryCache()
	ctx := context.Background()

	data := TestData{Name: "Bob", Age: 35}
	cache.Set(ctx, "user:3", data, 1*time.Hour)

	// Retrieve as JSON
	var retrieved TestData
	cache.GetJSON(ctx, "user:3", &retrieved)

	if retrieved.Name != "Bob" || retrieved.Age != 35 {
		t.Fatalf("json serialization failed")
	}
}

func TestMemoryCache_NoTTL(t *testing.T) {
	cache := NewMemoryCache()
	ctx := context.Background()

	// Set with no expiration (TTL=0)
	cache.Set(ctx, "persistent", "value", 0)

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	// Should still exist
	val, _ := cache.Get(ctx, "persistent")
	if val != "value" {
		t.Fatal("expected persistent value to exist")
	}
}
