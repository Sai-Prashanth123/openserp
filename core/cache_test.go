package core

import (
	"testing"
	"time"
)

func TestResponseCache_SetAndGet(t *testing.T) {
	cache := NewResponseCache(5*time.Second, 100)

	q := Query{Text: "golang", LangCode: "EN", Limit: 10}
	key := BuildCacheKey("google/search", q)

	// Should miss initially
	_, ok := cache.Get(key)
	if ok {
		t.Error("Expected cache miss, got hit")
	}

	// Store data
	data := []byte(`[{"rank":1,"url":"https://go.dev","title":"Go","description":"Go lang"}]`)
	cache.Set(key, data)

	// Should hit now
	cached, ok := cache.Get(key)
	if !ok {
		t.Error("Expected cache hit, got miss")
	}
	if string(cached) != string(data) {
		t.Errorf("Cached data mismatch: got %s, want %s", cached, data)
	}
}

func TestResponseCache_Expiration(t *testing.T) {
	cache := NewResponseCache(100*time.Millisecond, 100)

	q := Query{Text: "test", Limit: 5}
	key := BuildCacheKey("bing/search", q)

	cache.Set(key, []byte(`[{"rank":1}]`))

	// Should hit immediately
	_, ok := cache.Get(key)
	if !ok {
		t.Error("Expected cache hit before expiration")
	}

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	_, ok = cache.Get(key)
	if ok {
		t.Error("Expected cache miss after expiration")
	}
}

func TestResponseCache_MaxSize(t *testing.T) {
	cache := NewResponseCache(10*time.Second, 5)

	// Fill cache beyond max size
	for i := 0; i < 10; i++ {
		q := Query{Text: string(rune('a' + i)), Limit: 1}
		key := BuildCacheKey("google/search", q)
		cache.Set(key, []byte(`[]`))
	}

	stats := cache.Stats()
	total := stats["total_entries"].(int)
	maxSize := stats["max_size"].(int)

	if total > maxSize {
		t.Errorf("Cache exceeded max size: got %d, max %d", total, maxSize)
	}
}

func TestResponseCache_Stats(t *testing.T) {
	cache := NewResponseCache(5*time.Second, 100)

	q := Query{Text: "stats test", Limit: 10}
	key := BuildCacheKey("yandex/search", q)
	cache.Set(key, []byte(`[{"rank":1}]`))

	stats := cache.Stats()

	if stats["total_entries"].(int) != 1 {
		t.Errorf("Expected 1 total entry, got %d", stats["total_entries"])
	}
	if stats["active_entries"].(int) != 1 {
		t.Errorf("Expected 1 active entry, got %d", stats["active_entries"])
	}
	if stats["max_size"].(int) != 100 {
		t.Errorf("Expected max_size 100, got %d", stats["max_size"])
	}
}

func TestBuildCacheKey_Deterministic(t *testing.T) {
	q1 := Query{Text: "golang", LangCode: "EN", Limit: 10}
	q2 := Query{Text: "golang", LangCode: "EN", Limit: 10}
	q3 := Query{Text: "python", LangCode: "EN", Limit: 10}

	key1 := BuildCacheKey("google/search", q1)
	key2 := BuildCacheKey("google/search", q2)
	key3 := BuildCacheKey("google/search", q3)

	if key1 != key2 {
		t.Error("Same query should produce same cache key")
	}
	if key1 == key3 {
		t.Error("Different queries should produce different cache keys")
	}
}

func TestBuildCacheKey_EngineIsolation(t *testing.T) {
	q := Query{Text: "golang", Limit: 10}

	googleKey := BuildCacheKey("google/search", q)
	bingKey := BuildCacheKey("bing/search", q)

	if googleKey == bingKey {
		t.Error("Different engines should produce different cache keys")
	}
}
