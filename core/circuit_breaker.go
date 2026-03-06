package core

import (
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// CircuitState represents the current state of a circuit breaker
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // Normal operation — requests flow through
	CircuitOpen                         // Engine is broken — requests are blocked
	CircuitHalfOpen                     // Testing — allow one request to check recovery
)

func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig controls circuit breaker behavior
type CircuitBreakerConfig struct {
	FailureThreshold int           // Failures before opening circuit (default: 5)
	RecoveryTimeout  time.Duration // How long to wait before trying again (default: 60s)
	SuccessThreshold int           // Successes in half-open to close circuit (default: 2)
}

// DefaultCircuitBreakerConfig returns production-ready defaults
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold: 5,
		RecoveryTimeout:  60 * time.Second,
		SuccessThreshold: 2,
	}
}

// CircuitBreaker implements the circuit breaker pattern for a single search engine.
//
// State machine:
//
//	CLOSED (normal) --[failures >= threshold]--> OPEN (blocked)
//	OPEN --[recovery timeout expires]--> HALF-OPEN (testing)
//	HALF-OPEN --[success]--> CLOSED
//	HALF-OPEN --[failure]--> OPEN
//
// This prevents wasting time on engines that are consistently failing
// (e.g., due to IP bans or structural changes) and gives them time to recover.
type CircuitBreaker struct {
	mu              sync.RWMutex
	name            string
	state           CircuitState
	config          CircuitBreakerConfig
	failureCount    int
	successCount    int
	lastFailureTime time.Time
	lastStateChange time.Time
}

// NewCircuitBreaker creates a circuit breaker for the named engine
func NewCircuitBreaker(name string, cfg CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		name:            name,
		state:           CircuitClosed,
		config:          cfg,
		lastStateChange: time.Now(),
	}
}

// AllowRequest checks if a request should be allowed through.
// Returns true if the circuit is closed or half-open (testing).
func (cb *CircuitBreaker) AllowRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true

	case CircuitOpen:
		// Check if recovery timeout has passed
		if time.Since(cb.lastFailureTime) >= cb.config.RecoveryTimeout {
			cb.setState(CircuitHalfOpen)
			logrus.Infof("[CircuitBreaker][%s] Recovery timeout elapsed, moving to half-open", cb.name)
			return true
		}
		return false

	case CircuitHalfOpen:
		// Allow one request through for testing
		return true

	default:
		return true
	}
}

// RecordSuccess records a successful request
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitHalfOpen:
		cb.successCount++
		if cb.successCount >= cb.config.SuccessThreshold {
			cb.setState(CircuitClosed)
			cb.failureCount = 0
			cb.successCount = 0
			logrus.Infof("[CircuitBreaker][%s] Recovered! Circuit closed after %d successes", cb.name, cb.config.SuccessThreshold)
		}
	case CircuitClosed:
		// Reset failure count on success
		cb.failureCount = 0
	}
}

// RecordFailure records a failed request
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.lastFailureTime = time.Now()

	switch cb.state {
	case CircuitClosed:
		cb.failureCount++
		if cb.failureCount >= cb.config.FailureThreshold {
			cb.setState(CircuitOpen)
			logrus.Warnf("[CircuitBreaker][%s] Circuit OPENED after %d consecutive failures (will retry in %s)",
				cb.name, cb.failureCount, cb.config.RecoveryTimeout)
		}
	case CircuitHalfOpen:
		// Failed during testing — go back to open
		cb.setState(CircuitOpen)
		cb.successCount = 0
		logrus.Warnf("[CircuitBreaker][%s] Failed during half-open test, circuit re-opened", cb.name)
	}
}

// State returns the current circuit state
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Stats returns current circuit breaker statistics
func (cb *CircuitBreaker) Stats() map[string]interface{} {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	stats := map[string]interface{}{
		"engine":        cb.name,
		"state":         cb.state.String(),
		"failure_count": cb.failureCount,
		"last_changed":  cb.lastStateChange.Format(time.RFC3339),
	}

	if cb.state == CircuitOpen {
		remaining := cb.config.RecoveryTimeout - time.Since(cb.lastFailureTime)
		if remaining < 0 {
			remaining = 0
		}
		stats["retry_in"] = remaining.Round(time.Second).String()
	}

	return stats
}

func (cb *CircuitBreaker) setState(state CircuitState) {
	cb.state = state
	cb.lastStateChange = time.Now()
}

// ---- Circuit Breaker Manager ----

// CircuitBreakerManager manages circuit breakers for all search engines
type CircuitBreakerManager struct {
	mu       sync.RWMutex
	breakers map[string]*CircuitBreaker
	config   CircuitBreakerConfig
}

// NewCircuitBreakerManager creates a manager with the given config
func NewCircuitBreakerManager(cfg CircuitBreakerConfig) *CircuitBreakerManager {
	return &CircuitBreakerManager{
		breakers: make(map[string]*CircuitBreaker),
		config:   cfg,
	}
}

// Get returns the circuit breaker for the named engine, creating one if needed
func (m *CircuitBreakerManager) Get(engineName string) *CircuitBreaker {
	m.mu.RLock()
	if cb, exists := m.breakers[engineName]; exists {
		m.mu.RUnlock()
		return cb
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if cb, exists := m.breakers[engineName]; exists {
		return cb
	}

	cb := NewCircuitBreaker(engineName, m.config)
	m.breakers[engineName] = cb
	return cb
}

// AllStats returns stats for all circuit breakers
func (m *CircuitBreakerManager) AllStats() []map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make([]map[string]interface{}, 0, len(m.breakers))
	for _, cb := range m.breakers {
		stats = append(stats, cb.Stats())
	}
	return stats
}

// ErrCircuitOpen is returned when a request is blocked by an open circuit
var ErrCircuitOpen = fmt.Errorf("circuit breaker is open — engine temporarily disabled")
