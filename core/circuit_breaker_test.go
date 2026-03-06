package core

import (
	"errors"
	"testing"
	"time"
)

func TestCircuitBreaker_StartsClosedAndAllowsRequests(t *testing.T) {
	cb := NewCircuitBreaker("test-engine", DefaultCircuitBreakerConfig())

	if cb.State() != CircuitClosed {
		t.Errorf("expected CircuitClosed, got: %s", cb.State())
	}
	if !cb.AllowRequest() {
		t.Error("expected request to be allowed in closed state")
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cfg := CircuitBreakerConfig{FailureThreshold: 3, RecoveryTimeout: 1 * time.Second, SuccessThreshold: 1}
	cb := NewCircuitBreaker("test-engine", cfg)

	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != CircuitClosed {
		t.Errorf("should still be closed after 2 failures, got: %s", cb.State())
	}

	cb.RecordFailure() // 3rd failure = threshold
	if cb.State() != CircuitOpen {
		t.Errorf("expected CircuitOpen after %d failures, got: %s", cfg.FailureThreshold, cb.State())
	}
	if cb.AllowRequest() {
		t.Error("expected request to be blocked when circuit is open")
	}
}

func TestCircuitBreaker_RecoveryToHalfOpen(t *testing.T) {
	cfg := CircuitBreakerConfig{FailureThreshold: 2, RecoveryTimeout: 50 * time.Millisecond, SuccessThreshold: 1}
	cb := NewCircuitBreaker("test-engine", cfg)

	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != CircuitOpen {
		t.Fatal("expected open")
	}

	// Wait for recovery timeout
	time.Sleep(60 * time.Millisecond)

	if !cb.AllowRequest() {
		t.Error("should allow request after recovery timeout (half-open)")
	}
	if cb.State() != CircuitHalfOpen {
		t.Errorf("expected CircuitHalfOpen, got: %s", cb.State())
	}
}

func TestCircuitBreaker_HalfOpenToClosedOnSuccess(t *testing.T) {
	cfg := CircuitBreakerConfig{FailureThreshold: 2, RecoveryTimeout: 50 * time.Millisecond, SuccessThreshold: 2}
	cb := NewCircuitBreaker("test-engine", cfg)

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()
	time.Sleep(60 * time.Millisecond)
	cb.AllowRequest() // transitions to half-open

	cb.RecordSuccess()
	if cb.State() != CircuitHalfOpen {
		t.Errorf("expected still half-open after 1 success, got: %s", cb.State())
	}

	cb.RecordSuccess() // 2nd success = threshold
	if cb.State() != CircuitClosed {
		t.Errorf("expected CircuitClosed after %d successes, got: %s", cfg.SuccessThreshold, cb.State())
	}
}

func TestCircuitBreaker_HalfOpenToOpenOnFailure(t *testing.T) {
	cfg := CircuitBreakerConfig{FailureThreshold: 2, RecoveryTimeout: 50 * time.Millisecond, SuccessThreshold: 1}
	cb := NewCircuitBreaker("test-engine", cfg)

	cb.RecordFailure()
	cb.RecordFailure()
	time.Sleep(60 * time.Millisecond)
	cb.AllowRequest() // half-open

	cb.RecordFailure() // fail during half-open
	if cb.State() != CircuitOpen {
		t.Errorf("expected CircuitOpen after failure in half-open, got: %s", cb.State())
	}
}

func TestCircuitBreaker_SuccessResetsClosed(t *testing.T) {
	cfg := CircuitBreakerConfig{FailureThreshold: 3, RecoveryTimeout: 1 * time.Second, SuccessThreshold: 1}
	cb := NewCircuitBreaker("test-engine", cfg)

	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordSuccess() // Should reset failure count

	cb.RecordFailure() // Now only 1 failure, not 3
	if cb.State() != CircuitClosed {
		t.Errorf("expected still closed (success should reset count), got: %s", cb.State())
	}
}

func TestCircuitBreaker_Stats(t *testing.T) {
	cb := NewCircuitBreaker("test-engine", DefaultCircuitBreakerConfig())
	cb.RecordFailure()

	stats := cb.Stats()
	if stats["name"] != "test-engine" {
		t.Errorf("expected name=test-engine, got: %v", stats["name"])
	}
	if stats["state"] != "closed" {
		t.Errorf("expected state=closed, got: %v", stats["state"])
	}
	if stats["failure_count"].(int) != 1 {
		t.Errorf("expected failure_count=1, got: %v", stats["failure_count"])
	}
}

func TestCircuitBreakerManager_GetCreatesNew(t *testing.T) {
	mgr := NewCircuitBreakerManager(DefaultCircuitBreakerConfig())

	cb1 := mgr.Get("google")
	cb2 := mgr.Get("yandex")
	cb3 := mgr.Get("google") // same as cb1

	if cb1 != cb3 {
		t.Error("expected same circuit breaker for same engine name")
	}
	if cb1 == cb2 {
		t.Error("expected different circuit breakers for different engines")
	}
}

func TestCircuitBreakerManager_AllStats(t *testing.T) {
	mgr := NewCircuitBreakerManager(DefaultCircuitBreakerConfig())
	mgr.Get("google")
	mgr.Get("yandex")

	stats := mgr.AllStats()
	if len(stats) != 2 {
		t.Errorf("expected 2 entries in stats, got: %d", len(stats))
	}
}

func TestErrCircuitOpen(t *testing.T) {
	if !errors.Is(ErrCircuitOpen, ErrCircuitOpen) {
		t.Error("ErrCircuitOpen should match itself")
	}
}
