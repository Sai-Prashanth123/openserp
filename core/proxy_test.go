package core

import (
	"testing"
)

func TestProxyPool_RoundRobin(t *testing.T) {
	pool := NewProxyPool([]string{"http://proxy1", "http://proxy2", "http://proxy3"})

	// Track which proxies we see over several rounds
	seen := make(map[string]int)
	for i := 0; i < 9; i++ {
		p := pool.Next()
		if p == "" {
			t.Fatal("expected a proxy, got empty string")
		}
		seen[p]++
	}

	// Each proxy should have been used 3 times
	for url, count := range seen {
		if count != 3 {
			t.Errorf("proxy %s used %d times, expected 3", url, count)
		}
	}
}

func TestProxyPool_EmptyPool(t *testing.T) {
	pool := NewProxyPool(nil)
	if got := pool.Next(); got != "" {
		t.Errorf("expected empty string for empty pool, got: %s", got)
	}
	if pool.Size() != 0 {
		t.Errorf("expected size 0, got: %d", pool.Size())
	}
}

func TestProxyPool_FailureDisablesProxy(t *testing.T) {
	pool := NewProxyPool([]string{"http://bad-proxy", "http://good-proxy"})

	// Force the bad proxy to be used first by getting and checking
	firstProxy := pool.Next()

	// Report 3 failures for whichever proxy was first
	pool.ReportFailure(firstProxy)
	pool.ReportFailure(firstProxy)
	pool.ReportFailure(firstProxy)

	// Now the pool should only return the other proxy
	for i := 0; i < 5; i++ {
		p := pool.Next()
		if p == firstProxy {
			t.Errorf("iteration %d: disabled proxy %s was returned", i, firstProxy)
		}
		if p == "" {
			t.Errorf("iteration %d: expected a proxy, got empty string", i)
		}
	}
}

func TestProxyPool_AllDisabledReEnables(t *testing.T) {
	pool := NewProxyPool([]string{"http://proxy1", "http://proxy2"})

	// Disable all proxies
	pool.ReportFailure("http://proxy1")
	pool.ReportFailure("http://proxy1")
	pool.ReportFailure("http://proxy1")
	pool.ReportFailure("http://proxy2")
	pool.ReportFailure("http://proxy2")
	pool.ReportFailure("http://proxy2")

	// Both are disabled — Next() should re-enable all
	p := pool.Next()
	if p == "" {
		t.Fatal("expected a proxy after re-enable, got empty string")
	}

	if pool.ActiveCount() != 2 {
		t.Errorf("expected 2 active proxies after re-enable, got: %d", pool.ActiveCount())
	}
}

func TestProxyPool_SuccessResetsFailures(t *testing.T) {
	pool := NewProxyPool([]string{"http://proxy1"})

	pool.ReportFailure("http://proxy1")
	pool.ReportFailure("http://proxy1")
	pool.ReportSuccess("http://proxy1") // Should reset

	pool.ReportFailure("http://proxy1") // Now 1 failure (not 3)

	p := pool.Next()
	if p == "" || p != "http://proxy1" {
		t.Errorf("proxy should still be active, got: %s", p)
	}
}

func TestProxyPool_Stats(t *testing.T) {
	pool := NewProxyPool([]string{"http://proxy1", "http://proxy2"})
	stats := pool.Stats()

	if stats["total"] != 2 {
		t.Errorf("expected total=2, got: %v", stats["total"])
	}
	if stats["active"] != 2 {
		t.Errorf("expected active=2, got: %v", stats["active"])
	}
}

func TestProxyPool_SkipsEmptyStrings(t *testing.T) {
	pool := NewProxyPool([]string{"", "http://proxy1", "", "http://proxy2", ""})
	if pool.Size() != 2 {
		t.Errorf("expected 2 proxies (empty strings filtered), got: %d", pool.Size())
	}
}
