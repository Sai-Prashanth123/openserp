package core

import (
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"
)

// ResilientSearcher wraps search engines with retry, circuit breaker, and
// automatic engine fallback. When one engine fails, it automatically tries
// the next available engine instead of returning an error.
//
// Protection layers (in order):
//  1. Circuit Breaker — skip engines that are consistently failing
//  2. Retry with Backoff — retry transient failures (timeouts, network errors)
//  3. Engine Fallback — if the primary engine fails completely, try alternatives
type ResilientSearcher struct {
	engines    []SearchEngine
	cbManager  *CircuitBreakerManager
	retryCfg   RetryConfig
	proxyPool  *ProxyPool
}

// ResilientConfig holds all resilience configuration
type ResilientConfig struct {
	Retry          RetryConfig
	CircuitBreaker CircuitBreakerConfig
	ProxyURLs      []string // List of proxy URLs for rotation
}

// DefaultResilientConfig returns production-ready resilience settings
func DefaultResilientConfig() ResilientConfig {
	return ResilientConfig{
		Retry:          DefaultRetryConfig(),
		CircuitBreaker: DefaultCircuitBreakerConfig(),
		ProxyURLs:      nil,
	}
}

// NewResilientSearcher creates a resilient search wrapper around the given engines
func NewResilientSearcher(engines []SearchEngine, cfg ResilientConfig) *ResilientSearcher {
	rs := &ResilientSearcher{
		engines:   engines,
		cbManager: NewCircuitBreakerManager(cfg.CircuitBreaker),
		retryCfg:  cfg.Retry,
	}

	if len(cfg.ProxyURLs) > 0 {
		rs.proxyPool = NewProxyPool(cfg.ProxyURLs)
		logrus.Infof("Proxy rotation enabled with %d proxies", rs.proxyPool.Size())
	}

	return rs
}

// Search performs a resilient search on the specified engine with automatic
// fallback to other engines if the primary one fails.
//
// Flow:
//  1. Check circuit breaker for primary engine → skip if open
//  2. Retry the search with exponential backoff
//  3. If all retries fail, try fallback engines one by one
//  4. Return results from whichever engine succeeds first
func (rs *ResilientSearcher) Search(primaryEngine SearchEngine, q Query) ([]SearchResult, string, error) {
	// Try primary engine first
	results, err := rs.searchWithProtection(primaryEngine, q, false)
	if err == nil {
		return results, primaryEngine.Name(), nil
	}

	logrus.Warnf("[Resilient] Primary engine %s failed: %s. Trying fallback engines...", primaryEngine.Name(), err)

	// Try fallback engines
	for _, fallbackEngine := range rs.engines {
		if fallbackEngine.Name() == primaryEngine.Name() {
			continue // Skip the already-failed primary
		}
		if !fallbackEngine.IsInitialized() {
			continue
		}

		cb := rs.cbManager.Get(fallbackEngine.Name())
		if !cb.AllowRequest() {
			logrus.Debugf("[Resilient] Skipping fallback %s (circuit open)", fallbackEngine.Name())
			continue
		}

		results, err := rs.searchWithProtection(fallbackEngine, q, true)
		if err == nil {
			logrus.Infof("[Resilient] Fallback to %s succeeded with %d results", fallbackEngine.Name(), len(results))
			return results, fallbackEngine.Name(), nil
		}

		logrus.Warnf("[Resilient] Fallback engine %s also failed: %s", fallbackEngine.Name(), err)
	}

	return nil, primaryEngine.Name(), ErrAllEnginesFailed
}

// SearchImage performs a resilient image search with the same protection layers
func (rs *ResilientSearcher) SearchImage(primaryEngine SearchEngine, q Query) ([]SearchResult, string, error) {
	results, err := rs.searchImageWithProtection(primaryEngine, q, false)
	if err == nil {
		return results, primaryEngine.Name(), nil
	}

	logrus.Warnf("[Resilient] Primary engine %s image search failed: %s. Trying fallback engines...", primaryEngine.Name(), err)

	for _, fallbackEngine := range rs.engines {
		if fallbackEngine.Name() == primaryEngine.Name() {
			continue
		}
		if !fallbackEngine.IsInitialized() {
			continue
		}

		cb := rs.cbManager.Get(fallbackEngine.Name())
		if !cb.AllowRequest() {
			continue
		}

		results, err := rs.searchImageWithProtection(fallbackEngine, q, true)
		if err == nil {
			logrus.Infof("[Resilient] Image fallback to %s succeeded with %d results", fallbackEngine.Name(), len(results))
			return results, fallbackEngine.Name(), nil
		}
	}

	return nil, primaryEngine.Name(), ErrAllEnginesFailed
}

// searchWithProtection wraps a single engine search with circuit breaker + retry
func (rs *ResilientSearcher) searchWithProtection(engine SearchEngine, q Query, isFallback bool) ([]SearchResult, error) {
	cb := rs.cbManager.Get(engine.Name())

	// Check circuit breaker
	if !cb.AllowRequest() {
		return nil, ErrCircuitOpen
	}

	// Execute with retry
	result := RetryableSearch(rs.retryCfg, engine.Name(), func() ([]SearchResult, error) {
		return engine.Search(q)
	})

	if result.Err != nil {
		cb.RecordFailure()
		return nil, result.Err
	}

	cb.RecordSuccess()
	return result.Results, nil
}

// searchImageWithProtection wraps a single engine image search with circuit breaker + retry
func (rs *ResilientSearcher) searchImageWithProtection(engine SearchEngine, q Query, isFallback bool) ([]SearchResult, error) {
	cb := rs.cbManager.Get(engine.Name())

	if !cb.AllowRequest() {
		return nil, ErrCircuitOpen
	}

	result := RetryableSearch(rs.retryCfg, engine.Name(), func() ([]SearchResult, error) {
		return engine.SearchImage(q)
	})

	if result.Err != nil {
		cb.RecordFailure()
		return nil, result.Err
	}

	cb.RecordSuccess()
	return result.Results, nil
}

// SearchAllParallel searches all available engines in parallel with full protection.
// Used by megasearch. Engines with open circuits are skipped entirely.
func (rs *ResilientSearcher) SearchAllParallel(q Query, engines []SearchEngine) []MegaSearchResult {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var allResults []MegaSearchResult

	for _, engine := range engines {
		cb := rs.cbManager.Get(engine.Name())
		if !cb.AllowRequest() {
			logrus.Infof("[Resilient] Skipping %s in megasearch (circuit open)", engine.Name())
			continue
		}

		wg.Add(1)
		go func(eng SearchEngine) {
			defer wg.Done()

			result := RetryableSearch(rs.retryCfg, eng.Name(), func() ([]SearchResult, error) {
				return eng.Search(q)
			})

			if result.Err != nil {
				rs.cbManager.Get(eng.Name()).RecordFailure()
				return
			}

			rs.cbManager.Get(eng.Name()).RecordSuccess()

			mu.Lock()
			for _, r := range result.Results {
				allResults = append(allResults, MegaSearchResult{
					SearchResult: r,
					Engine:       eng.Name(),
				})
			}
			mu.Unlock()
		}(engine)
	}

	wg.Wait()
	return allResults
}

// SearchAllImageParallel searches all available engines for images in parallel
func (rs *ResilientSearcher) SearchAllImageParallel(q Query, engines []SearchEngine) []MegaSearchResult {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var allResults []MegaSearchResult

	for _, engine := range engines {
		cb := rs.cbManager.Get(engine.Name())
		if !cb.AllowRequest() {
			logrus.Infof("[Resilient] Skipping %s in megaimage (circuit open)", engine.Name())
			continue
		}

		wg.Add(1)
		go func(eng SearchEngine) {
			defer wg.Done()

			result := RetryableSearch(rs.retryCfg, eng.Name(), func() ([]SearchResult, error) {
				return eng.SearchImage(q)
			})

			if result.Err != nil {
				rs.cbManager.Get(eng.Name()).RecordFailure()
				return
			}

			rs.cbManager.Get(eng.Name()).RecordSuccess()

			mu.Lock()
			for _, r := range result.Results {
				allResults = append(allResults, MegaSearchResult{
					SearchResult: r,
					Engine:       eng.Name(),
				})
			}
			mu.Unlock()
		}(engine)
	}

	wg.Wait()
	return allResults
}

// GetProxyPool returns the proxy pool (may be nil if no proxies configured)
func (rs *ResilientSearcher) GetProxyPool() *ProxyPool {
	return rs.proxyPool
}

// GetCircuitBreakerStats returns stats for all circuit breakers
func (rs *ResilientSearcher) GetCircuitBreakerStats() []map[string]interface{} {
	return rs.cbManager.AllStats()
}

// ErrAllEnginesFailed is returned when the primary engine and all fallback engines fail
var ErrAllEnginesFailed = fmt.Errorf("all search engines failed")
