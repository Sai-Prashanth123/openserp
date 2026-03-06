package core

import (
	"fmt"
	"math"
	"time"

	"github.com/sirupsen/logrus"
)

// RetryConfig controls how retries behave
type RetryConfig struct {
	MaxRetries     int           // Maximum number of retry attempts (default: 3)
	InitialBackoff time.Duration // Starting delay before first retry (default: 1s)
	MaxBackoff     time.Duration // Maximum delay between retries (default: 30s)
	BackoffFactor  float64       // Multiplier for each successive retry (default: 2.0)
}

// DefaultRetryConfig returns production-ready retry settings
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     30 * time.Second,
		BackoffFactor:  2.0,
	}
}

// RetryResult holds the outcome of a retry operation
type RetryResult struct {
	Results  []SearchResult
	Err      error
	Attempts int
	Engine   string
}

// RetryableSearch executes a search function with exponential backoff retries.
// If a search fails due to timeout or temporary errors, it waits progressively
// longer between retries (1s → 2s → 4s → ...) up to MaxBackoff.
// CAPTCHAs are NOT retried since they indicate IP-level blocking.
func RetryableSearch(cfg RetryConfig, engineName string, searchFn func() ([]SearchResult, error)) RetryResult {
	var lastErr error

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := calculateBackoff(cfg, attempt)
			logrus.Warnf("[%s] Retry attempt %d/%d after %s", engineName, attempt, cfg.MaxRetries, backoff)
			time.Sleep(backoff)
		}

		results, err := searchFn()
		if err == nil {
			if attempt > 0 {
				logrus.Infof("[%s] Succeeded on retry attempt %d", engineName, attempt)
			}
			return RetryResult{
				Results:  results,
				Err:      nil,
				Attempts: attempt + 1,
				Engine:   engineName,
			}
		}

		lastErr = err

		// Don't retry on CAPTCHA — it means the IP is flagged
		if err == ErrCaptcha {
			logrus.Warnf("[%s] CAPTCHA detected, skipping retries (IP may be flagged)", engineName)
			return RetryResult{
				Results:  nil,
				Err:      err,
				Attempts: attempt + 1,
				Engine:   engineName,
			}
		}

		logrus.Warnf("[%s] Attempt %d failed: %s", engineName, attempt+1, err)
	}

	return RetryResult{
		Results:  nil,
		Err:      fmt.Errorf("all %d attempts failed for %s: %w", cfg.MaxRetries+1, engineName, lastErr),
		Attempts: cfg.MaxRetries + 1,
		Engine:   engineName,
	}
}

// calculateBackoff returns the delay for the given attempt number using
// exponential backoff: delay = initialBackoff * (factor ^ attempt), capped at maxBackoff
func calculateBackoff(cfg RetryConfig, attempt int) time.Duration {
	backoff := float64(cfg.InitialBackoff) * math.Pow(cfg.BackoffFactor, float64(attempt-1))
	if backoff > float64(cfg.MaxBackoff) {
		backoff = float64(cfg.MaxBackoff)
	}
	return time.Duration(backoff)
}
