package core

import (
	"errors"
	"testing"
	"time"
)

func TestRetryableSearch_SuccessOnFirstAttempt(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 3, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, BackoffFactor: 2.0}
	calls := 0

	result := RetryableSearch(cfg, "test", func() ([]SearchResult, error) {
		calls++
		return []SearchResult{{Title: "result1"}}, nil
	})

	if result.Err != nil {
		t.Fatalf("expected no error, got: %v", result.Err)
	}
	if result.Attempts != 1 {
		t.Errorf("expected 1 attempt, got: %d", result.Attempts)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got: %d", calls)
	}
	if len(result.Results) != 1 || result.Results[0].Title != "result1" {
		t.Errorf("unexpected results: %+v", result.Results)
	}
}

func TestRetryableSearch_SuccessOnSecondAttempt(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 3, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, BackoffFactor: 2.0}
	calls := 0

	result := RetryableSearch(cfg, "test", func() ([]SearchResult, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("temporary failure")
		}
		return []SearchResult{{Title: "result1"}}, nil
	})

	if result.Err != nil {
		t.Fatalf("expected no error, got: %v", result.Err)
	}
	if result.Attempts != 2 {
		t.Errorf("expected 2 attempts, got: %d", result.Attempts)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got: %d", calls)
	}
}

func TestRetryableSearch_AllAttemptsFail(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 2, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond, BackoffFactor: 2.0}
	calls := 0

	result := RetryableSearch(cfg, "test", func() ([]SearchResult, error) {
		calls++
		return nil, errors.New("persistent failure")
	})

	if result.Err == nil {
		t.Fatal("expected error, got nil")
	}
	// 1 initial + 2 retries = 3
	if calls != 3 {
		t.Errorf("expected 3 calls (1 + 2 retries), got: %d", calls)
	}
	if result.Attempts != 3 {
		t.Errorf("expected 3 attempts, got: %d", result.Attempts)
	}
}

func TestRetryableSearch_CaptchaNotRetried(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 3, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, BackoffFactor: 2.0}
	calls := 0

	result := RetryableSearch(cfg, "test", func() ([]SearchResult, error) {
		calls++
		return nil, ErrCaptcha
	})

	if !errors.Is(result.Err, ErrCaptcha) {
		t.Fatalf("expected ErrCaptcha, got: %v", result.Err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retries for CAPTCHA), got: %d", calls)
	}
}

func TestRetryableSearch_ZeroRetriesOnlyOneAttempt(t *testing.T) {
	cfg := RetryConfig{MaxRetries: 0, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, BackoffFactor: 2.0}
	calls := 0

	result := RetryableSearch(cfg, "test", func() ([]SearchResult, error) {
		calls++
		return nil, errors.New("fail")
	})

	if result.Err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call with 0 retries, got: %d", calls)
	}
}

func TestCalculateBackoff(t *testing.T) {
	cfg := RetryConfig{InitialBackoff: 1 * time.Second, MaxBackoff: 10 * time.Second, BackoffFactor: 2.0}

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{1, 1 * time.Second},  // 1 * 2^0
		{2, 2 * time.Second},  // 1 * 2^1
		{3, 4 * time.Second},  // 1 * 2^2
		{4, 8 * time.Second},  // 1 * 2^3
		{5, 10 * time.Second}, // capped at MaxBackoff
	}

	for _, tt := range tests {
		got := calculateBackoff(cfg, tt.attempt)
		if got != tt.expected {
			t.Errorf("attempt %d: expected %s, got %s", tt.attempt, tt.expected, got)
		}
	}
}
