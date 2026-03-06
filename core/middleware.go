package core

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/sirupsen/logrus"
)

// JSONErrorResponse is the standard error response format for all API errors
type JSONErrorResponse struct {
	Error   string `json:"error"`
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

// CORSConfig holds configuration for the CORS middleware
type CORSConfig struct {
	AllowOrigins string
	AllowMethods string
	AllowHeaders string
	MaxAge       int // Preflight cache duration in seconds
}

// DefaultCORSConfig returns a permissive CORS configuration suitable for development
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowOrigins: "*",
		AllowMethods: "GET, POST, OPTIONS",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		MaxAge:       86400,
	}
}

// CORSMiddleware adds Cross-Origin Resource Sharing headers to all responses.
// This is essential for browser-based frontends to consume the API.
func CORSMiddleware(cfg CORSConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Set("Access-Control-Allow-Origin", cfg.AllowOrigins)
		c.Set("Access-Control-Allow-Methods", cfg.AllowMethods)
		c.Set("Access-Control-Allow-Headers", cfg.AllowHeaders)
		c.Set("Access-Control-Max-Age", fmt.Sprintf("%d", cfg.MaxAge))

		// Handle preflight OPTIONS requests immediately
		if c.Method() == "OPTIONS" {
			return c.SendStatus(fiber.StatusNoContent)
		}

		return c.Next()
	}
}

// RequestLoggerMiddleware logs every incoming request with method, path, status code,
// and response latency. Essential for monitoring and debugging in production.
func RequestLoggerMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		// Process the request
		err := c.Next()

		latency := time.Since(start)
		status := c.Response().StatusCode()

		logFields := logrus.Fields{
			"method":  c.Method(),
			"path":    c.Path(),
			"status":  status,
			"latency": latency.String(),
			"ip":      c.IP(),
		}

		// Add query parameter for search requests
		if query := c.Query("text"); query != "" {
			logFields["query"] = query
		}

		entry := logrus.WithFields(logFields)

		if status >= 500 {
			entry.Error("Request failed")
		} else if status >= 400 {
			entry.Warn("Request error")
		} else {
			entry.Info("Request completed")
		}

		return err
	}
}

// JSONErrorMiddleware converts all Fiber errors into structured JSON responses
// instead of plain text. This makes the API consistent and machine-parseable.
func JSONErrorMiddleware() fiber.ErrorHandler {
	return func(c *fiber.Ctx, err error) error {
		code := fiber.StatusInternalServerError

		// Extract status code from Fiber errors
		if e, ok := err.(*fiber.Error); ok {
			code = e.Code
		}

		resp := JSONErrorResponse{
			Error:   statusText(code),
			Code:    code,
			Message: err.Error(),
		}

		c.Set("Content-Type", "application/json")
		return c.Status(code).JSON(resp)
	}
}

// statusText returns a short error category for the given HTTP status code
func statusText(code int) string {
	switch {
	case code == 400:
		return "bad_request"
	case code == 404:
		return "not_found"
	case code == 429:
		return "rate_limited"
	case code == 503:
		return "service_unavailable"
	case code >= 400 && code < 500:
		return "client_error"
	case code >= 500:
		return "server_error"
	default:
		return "error"
	}
}
