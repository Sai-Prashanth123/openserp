package core

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

type SearchEngine interface {
	Search(Query) ([]SearchResult, error)
	SearchImage(Query) ([]SearchResult, error)
	IsInitialized() bool
	Name() string
	GetRateLimiter() *rate.Limiter
}

type Server struct {
	app           *fiber.App
	addr          string
	searchEngines []SearchEngine
	cache         *ResponseCache
	resilient     *ResilientSearcher
	startTime     time.Time
}

// ServerOptions holds optional configuration for the HTTP server
type ServerOptions struct {
	CacheTTL     time.Duration // How long to cache search results (0 = disabled)
	CacheMaxSize int           // Maximum number of cached entries
	EnableCORS   bool          // Enable CORS headers for browser clients
	Resilience   ResilientConfig // Retry, circuit breaker, proxy rotation settings
}

// DefaultServerOptions returns sensible defaults for production use
func DefaultServerOptions() ServerOptions {
	return ServerOptions{
		CacheTTL:     5 * time.Minute,
		CacheMaxSize: 1000,
		EnableCORS:   true,
		Resilience:   DefaultResilientConfig(),
	}
}

func NewServer(host string, port int, searchEngines ...SearchEngine) *Server {
	return NewServerWithOptions(host, port, DefaultServerOptions(), searchEngines...)
}

func NewServerWithOptions(host string, port int, opts ServerOptions, searchEngines ...SearchEngine) *Server {
	addr := fmt.Sprintf("%s:%d", host, port)

	app := fiber.New(fiber.Config{
		ErrorHandler: JSONErrorMiddleware(),
	})

	serv := Server{
		app:           app,
		addr:          addr,
		searchEngines: searchEngines,
		startTime:     time.Now(),
	}

	// Initialize resilient searcher (retry + circuit breaker + fallback)
	serv.resilient = NewResilientSearcher(searchEngines, opts.Resilience)
	logrus.Info("Resilient search enabled: retry + circuit breaker + engine fallback")

	// Initialize cache if TTL > 0
	if opts.CacheTTL > 0 {
		serv.cache = NewResponseCache(opts.CacheTTL, opts.CacheMaxSize)
		logrus.Infof("Response cache enabled: TTL=%s, MaxSize=%d", opts.CacheTTL, opts.CacheMaxSize)
	}

	// Apply middleware
	if opts.EnableCORS {
		app.Use(CORSMiddleware(DefaultCORSConfig()))
	}
	app.Use(RequestLoggerMiddleware())

	// Health check endpoint — for Docker, load balancers, and monitoring
	app.Get("/health", serv.handleHealthCheck)

	// Cache stats endpoint
	if serv.cache != nil {
		app.Get("/cache/stats", serv.handleCacheStats)
	}

	// Circuit breaker stats endpoint
	app.Get("/resilience/stats", serv.handleResilienceStats)

	for _, engine := range searchEngines {
		locEngine := engine
		limiter := engine.GetRateLimiter()

		// Custom endpoint mapping for DuckDuckGo
		endpointName := strings.ToLower(locEngine.Name())
		if endpointName == "duckduckgo" {
			endpointName = "duck"
		}

		serv.app.Get(fmt.Sprintf("/%s/search", endpointName), func(c *fiber.Ctx) error {
			q := Query{}
			err := q.InitFromContext(c)
			if err != nil {
				logrus.Errorf("Error while setting %s query: %s", locEngine.Name(), err)
				return err
			}

			logrus.Infof("Starting SERP search request using %s engine for query: %s", locEngine.Name(), q.Text)

			// Check cache first
			if serv.cache != nil {
				cacheKey := BuildCacheKey(locEngine.Name()+"/search", q)
				if cached, ok := serv.cache.Get(cacheKey); ok {
					logrus.Infof("Cache hit for %s search: %s", locEngine.Name(), q.Text)
					c.Set("Content-Type", "application/json")
					c.Set("X-Cache", "HIT")
					return c.Send(cached)
				}
			}

			err = limiter.Wait(context.Background())
			if err != nil {
				logrus.Errorf("Ratelimiter error during %s query: %s", locEngine.Name(), err)
			}

			// Use resilient search: retry + circuit breaker + engine fallback
			res, usedEngine, searchErr := serv.resilient.Search(locEngine, q)
			if searchErr != nil {
				logrus.Errorf("Error during resilient %s search: %s", locEngine.Name(), searchErr)
				return fiber.NewError(fiber.StatusServiceUnavailable, searchErr.Error())
			}

			// Store in cache
			if serv.cache != nil && len(res) > 0 {
				cacheKey := BuildCacheKey(locEngine.Name()+"/search", q)
				if data, err := json.Marshal(res); err == nil {
					serv.cache.Set(cacheKey, data)
				}
			}

			// Report which engine actually served the results
			if usedEngine != locEngine.Name() {
				c.Set("X-Fallback-Engine", usedEngine)
				logrus.Infof("Request for %s was served by fallback engine: %s (%d results)", locEngine.Name(), usedEngine, len(res))
			} else {
				logrus.Infof("Successfully completed SERP search using %s engine, returned %d results", locEngine.Name(), len(res))
			}
			c.Set("X-Cache", "MISS")
			return c.JSON(res)
		})

		serv.app.Get(fmt.Sprintf("/%s/image", endpointName), func(c *fiber.Ctx) error {
			q := Query{}
			err := q.InitFromContext(c)
			if err != nil {
				logrus.Errorf("Error while setting %s query: %s", locEngine.Name(), err)
				return err
			}

			logrus.Infof("Starting SERP image search request using %s engine for query: %s", locEngine.Name(), q.Text)

			// Check cache first
			if serv.cache != nil {
				cacheKey := BuildCacheKey(locEngine.Name()+"/image", q)
				if cached, ok := serv.cache.Get(cacheKey); ok {
					logrus.Infof("Cache hit for %s image search: %s", locEngine.Name(), q.Text)
					c.Set("Content-Type", "application/json")
					c.Set("X-Cache", "HIT")
					return c.Send(cached)
				}
			}

			err = limiter.Wait(context.Background())
			if err != nil {
				logrus.Errorf("Ratelimiter error during %s query: %s", locEngine.Name(), err)
			}

			// Use resilient image search: retry + circuit breaker + engine fallback
			res, usedEngine, searchErr := serv.resilient.SearchImage(locEngine, q)
			if searchErr != nil {
				logrus.Errorf("Error during resilient %s image search: %s", locEngine.Name(), searchErr)
				return fiber.NewError(fiber.StatusServiceUnavailable, searchErr.Error())
			}

			// Store in cache
			if serv.cache != nil && len(res) > 0 {
				cacheKey := BuildCacheKey(locEngine.Name()+"/image", q)
				if data, err := json.Marshal(res); err == nil {
					serv.cache.Set(cacheKey, data)
				}
			}

			if usedEngine != locEngine.Name() {
				c.Set("X-Fallback-Engine", usedEngine)
				logrus.Infof("Image request for %s was served by fallback engine: %s (%d results)", locEngine.Name(), usedEngine, len(res))
			} else {
				logrus.Infof("Successfully completed SERP image search using [%s], returned %d results", locEngine.Name(), len(res))
			}
			c.Set("X-Cache", "MISS")
			return c.JSON(res)
		})
	}

	// Add megasearch endpoint
	serv.app.Get("/mega/search", serv.handleMegaSearch)

	// Add megasearch image endpoint
	serv.app.Get("/mega/image", serv.handleMegaImage)

	// Add endpoint to list available engines
	serv.app.Get("/mega/engines", serv.handleListEngines)

	return &serv
}

// HealthStatus represents the response from the /health endpoint
type HealthStatus struct {
	Status  string                 `json:"status"`
	Uptime  string                 `json:"uptime"`
	Engines []EngineHealth         `json:"engines"`
	System  map[string]interface{} `json:"system"`
}

// EngineHealth shows the status of an individual search engine
type EngineHealth struct {
	Name        string `json:"name"`
	Initialized bool   `json:"initialized"`
	Status      string `json:"status"`
}

// handleHealthCheck reports server health, engine status, and uptime.
// Use this for Docker HEALTHCHECK, Kubernetes probes, or monitoring dashboards.
func (s *Server) handleHealthCheck(c *fiber.Ctx) error {
	overallStatus := "healthy"

	engines := make([]EngineHealth, 0, len(s.searchEngines))
	for _, engine := range s.searchEngines {
		status := "ready"
		if !engine.IsInitialized() {
			status = "not_initialized"
			overallStatus = "degraded"
		}

		// Check circuit breaker state
		circuitState := "closed"
		if s.resilient != nil {
			for _, cbStat := range s.resilient.GetCircuitBreakerStats() {
				if cbStat["engine"] == engine.Name() {
					circuitState = cbStat["state"].(string)
					if circuitState == "open" {
						status = "circuit_open"
						overallStatus = "degraded"
					}
					break
				}
			}
		}

		engines = append(engines, EngineHealth{
			Name:        engine.Name(),
			Initialized: engine.IsInitialized(),
			Status:      status,
		})
	}

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	health := HealthStatus{
		Status:  overallStatus,
		Uptime:  time.Since(s.startTime).Round(time.Second).String(),
		Engines: engines,
		System: map[string]interface{}{
			"goroutines":  runtime.NumGoroutine(),
			"memory_mb":   memStats.Alloc / 1024 / 1024,
			"go_version":  runtime.Version(),
		},
	}

	if overallStatus != "healthy" {
		c.Status(fiber.StatusServiceUnavailable)
	}

	return c.JSON(health)
}

// handleCacheStats returns current cache statistics
func (s *Server) handleCacheStats(c *fiber.Ctx) error {
	if s.cache == nil {
		return c.JSON(map[string]string{"status": "disabled"})
	}
	return c.JSON(s.cache.Stats())
}

// MegaSearchResult represents a search result with engine information
type MegaSearchResult struct {
	SearchResult
	Engine string `json:"engine"`
}

// handleMegaSearch handles the /megasearch endpoint
func (s *Server) handleMegaSearch(c *fiber.Ctx) error {
	q := Query{}
	err := q.InitFromContext(c)
	if err != nil {
		logrus.Errorf("Error while setting megasearch query: %s", err)
		return err
	}

	// Get engines parameter to filter which engines to use
	enginesParam := c.Query("engines", "")
	var enginesToUse []SearchEngine

	if enginesParam != "" {
		// Parse comma-separated list of engines
		engineNames := strings.Split(enginesParam, ",")
		for _, engineName := range engineNames {
			engineName = strings.TrimSpace(strings.ToLower(engineName))
			for _, engine := range s.searchEngines {
				if strings.ToLower(engine.Name()) == engineName {
					enginesToUse = append(enginesToUse, engine)
					break
				}
			}
		}
	} else {
		// Use all engines if no specific engines specified
		enginesToUse = s.searchEngines
	}

	if len(enginesToUse) == 0 {
		return fiber.NewError(fiber.StatusBadRequest, "No valid search engines specified")
	}

	// Log which engines will be used
	engineNames := make([]string, len(enginesToUse))
	for i, engine := range enginesToUse {
		engineNames[i] = engine.Name()
	}
	logrus.Infof("Starting SERP megasearch request using engines: %s for query: %s", strings.Join(engineNames, ", "), q.Text)

	// Execute searches in parallel across selected engines with resilience
	var results []MegaSearchResult
	if s.resilient != nil {
		results = s.resilient.SearchAllParallel(q, enginesToUse)
	} else {
		results = s.searchSelectedEngines(q, enginesToUse)
	}

	// Deduplicate results while preserving engine information
	dedupedResults := s.deduplicateMegaResults(results)

	logrus.Infof("Successfully completed SERP megasearch using %d engines, returned %d deduplicated results", len(enginesToUse), len(dedupedResults))
	return c.JSON(dedupedResults)
}

// handleMegaImage handles the /mega/image endpoint
func (s *Server) handleMegaImage(c *fiber.Ctx) error {
	q := Query{}
	err := q.InitFromContext(c)
	if err != nil {
		logrus.Errorf("Error while setting megasearch image query: %s", err)
		return err
	}

	// Get engines parameter to filter which engines to use
	enginesParam := c.Query("engines", "")
	var enginesToUse []SearchEngine

	if enginesParam != "" {
		// Parse comma-separated list of engines
		engineNames := strings.Split(enginesParam, ",")
		for _, engineName := range engineNames {
			engineName = strings.TrimSpace(strings.ToLower(engineName))
			for _, engine := range s.searchEngines {
				if strings.ToLower(engine.Name()) == engineName {
					enginesToUse = append(enginesToUse, engine)
					break
				}
			}
		}
	} else {
		// Use all engines if no specific engines specified
		enginesToUse = s.searchEngines
	}

	if len(enginesToUse) == 0 {
		return fiber.NewError(fiber.StatusBadRequest, "No valid search engines specified")
	}

	// Log which engines will be used
	engineNames := make([]string, len(enginesToUse))
	for i, engine := range enginesToUse {
		engineNames[i] = engine.Name()
	}
	logrus.Infof("Starting SERP megasearch image request using engines: %s for query: %s", strings.Join(engineNames, ", "), q.Text)

	// Execute image searches in parallel across selected engines with resilience
	var results []MegaSearchResult
	if s.resilient != nil {
		results = s.resilient.SearchAllImageParallel(q, enginesToUse)
	} else {
		results = s.searchSelectedEnginesImage(q, enginesToUse)
	}

	// Deduplicate results while preserving engine information
	dedupedResults := s.deduplicateMegaResults(results)

	logrus.Infof("Successfully completed SERP megasearch image using %d engines, returned %d deduplicated results", len(enginesToUse), len(dedupedResults))
	return c.JSON(dedupedResults)
}

// handleListEngines lists all available search engines
func (s *Server) handleListEngines(c *fiber.Ctx) error {
	var engines []map[string]interface{}

	for _, engine := range s.searchEngines {
		engineInfo := map[string]interface{}{
			"name":        engine.Name(),
			"initialized": engine.IsInitialized(),
		}

		// Add circuit breaker state if resilient searcher is available
		if s.resilient != nil {
			for _, cbStat := range s.resilient.GetCircuitBreakerStats() {
				if cbStat["engine"] == engine.Name() {
					engineInfo["circuit_state"] = cbStat["state"]
					break
				}
			}
		}

		engines = append(engines, engineInfo)
	}

	return c.JSON(map[string]interface{}{
		"engines": engines,
		"total":   len(engines),
	})
}

// handleResilienceStats returns circuit breaker stats and proxy pool status
func (s *Server) handleResilienceStats(c *fiber.Ctx) error {
	stats := map[string]interface{}{
		"circuit_breakers": s.resilient.GetCircuitBreakerStats(),
	}

	if pool := s.resilient.GetProxyPool(); pool != nil {
		stats["proxy_pool"] = pool.Stats()
	} else {
		stats["proxy_pool"] = map[string]string{"status": "no proxies configured"}
	}

	return c.JSON(stats)
}

// searchSelectedEngines performs parallel searches across selected engines
func (s *Server) searchSelectedEngines(q Query, engines []SearchEngine) []MegaSearchResult {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var allResults []MegaSearchResult

	for _, engine := range engines {
		wg.Add(1)
		go func(eng SearchEngine) {
			defer wg.Done()

			// Apply rate limiting
			limiter := eng.GetRateLimiter()
			if limiter != nil {
				err := limiter.Wait(context.Background())
				if err != nil {
					logrus.Errorf("Ratelimiter error during %s megasearch: %s", eng.Name(), err)
				}
			}

			// Perform search
			results, err := eng.Search(q)
			if err != nil {
				logrus.Errorf("Error during %s megasearch: %s", eng.Name(), err)
				return
			}

			// Convert to MegaSearchResult with engine info
			mu.Lock()
			for _, result := range results {
				megaResult := MegaSearchResult{
					SearchResult: result,
					Engine:       eng.Name(),
				}
				allResults = append(allResults, megaResult)
			}
			mu.Unlock()
		}(engine)
	}

	wg.Wait()
	return allResults
}

// searchSelectedEnginesImage performs parallel image searches across selected engines
func (s *Server) searchSelectedEnginesImage(q Query, engines []SearchEngine) []MegaSearchResult {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var allResults []MegaSearchResult

	for _, engine := range engines {
		wg.Add(1)
		go func(eng SearchEngine) {
			defer wg.Done()

			// Apply rate limiting
			limiter := eng.GetRateLimiter()
			if limiter != nil {
				err := limiter.Wait(context.Background())
				if err != nil {
					logrus.Errorf("Ratelimiter error during %s megasearch image: %s", eng.Name(), err)
				}
			}

			// Perform image search
			results, err := eng.SearchImage(q)
			if err != nil {
				logrus.Errorf("Error during %s megasearch image: %s", eng.Name(), err)
				return
			}

			// Convert to MegaSearchResult with engine info
			mu.Lock()
			for _, result := range results {
				megaResult := MegaSearchResult{
					SearchResult: result,
					Engine:       eng.Name(),
				}
				allResults = append(allResults, megaResult)
			}
			mu.Unlock()
		}(engine)
	}

	wg.Wait()
	return allResults
}

// deduplicateMegaResults deduplicates results while preserving engine information
func (s *Server) deduplicateMegaResults(results []MegaSearchResult) []MegaSearchResult {
	urlMap := make(map[string]MegaSearchResult)

	// Process results and keep the first occurrence of each URL
	for _, result := range results {
		if result.URL == "" {
			continue
		}

		if _, exists := urlMap[result.URL]; !exists {
			urlMap[result.URL] = result
		}
	}

	// Convert map back to slice and sort by rank
	var deduped []MegaSearchResult
	for _, result := range urlMap {
		deduped = append(deduped, result)
	}

	// Sort by rank
	sort.Slice(deduped, func(i, j int) bool {
		return deduped[i].Rank < deduped[j].Rank
	})

	return deduped
}

func (s *Server) Listen() error {
	return s.app.Listen(s.addr)
}

func (s *Server) Shutdown() error {
	return s.app.Shutdown()
}
