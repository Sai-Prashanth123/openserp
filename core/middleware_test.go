package core

import (
	"testing"
)

func TestStatusText(t *testing.T) {
	tests := []struct {
		code     int
		expected string
	}{
		{400, "bad_request"},
		{404, "not_found"},
		{429, "rate_limited"},
		{503, "service_unavailable"},
		{401, "client_error"},
		{500, "server_error"},
		{502, "server_error"},
		{200, "error"}, // shouldn't happen but tests the default
	}

	for _, tt := range tests {
		result := statusText(tt.code)
		if result != tt.expected {
			t.Errorf("statusText(%d) = %s, want %s", tt.code, result, tt.expected)
		}
	}
}

func TestDefaultCORSConfig(t *testing.T) {
	cfg := DefaultCORSConfig()

	if cfg.AllowOrigins != "*" {
		t.Errorf("Expected AllowOrigins=*, got %s", cfg.AllowOrigins)
	}
	if cfg.AllowMethods == "" {
		t.Error("AllowMethods should not be empty")
	}
	if cfg.AllowHeaders == "" {
		t.Error("AllowHeaders should not be empty")
	}
	if cfg.MaxAge <= 0 {
		t.Error("MaxAge should be positive")
	}
}
