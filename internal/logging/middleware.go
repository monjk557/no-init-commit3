package logging

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

// RequestIDMiddleware generates and adds request IDs to the context
func RequestIDMiddleware(generator RequestIDGenerator) func(http.Handler) http.Handler {
	if generator == nil {
		generator = &DefaultRequestIDGenerator{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := generator.Generate()

			// Add request ID to context
			ctx := context.WithValue(r.Context(), RequestIDKey, requestID)

			// Add request ID to response header for tracing
			w.Header().Set("X-Request-ID", requestID)

			// Continue with the request
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// CorrelationIDMiddleware extracts correlation ID from headers and adds to context
func CorrelationIDMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract correlation ID from header
			correlationID := r.Header.Get("X-Correlation-ID")
			if correlationID == "" {
				// If not provided, use request ID as correlation ID
				if requestID, ok := r.Context().Value(RequestIDKey).(string); ok {
					correlationID = requestID
				}
			}

			// Add correlation ID to context
			ctx := context.WithValue(r.Context(), CorrelationIDKey, correlationID)

			// Add correlation ID to response header
			w.Header().Set("X-Correlation-ID", correlationID)

			// Continue with the request
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// LoggingMiddleware logs HTTP requests with context
func LoggingMiddleware(logger *Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap response writer to capture status code
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			// Continue with the request
			next.ServeHTTP(ww, r)

			// Log the request
			duration := time.Since(start)
			logger.HTTPRequest(
				r.Context(),
				r.Method,
				r.URL.Path,
				r.UserAgent(),
				r.RemoteAddr,
				ww.Status(),
				duration,
			)
		})
	}
}

// ErrorHandlingMiddleware provides centralized error handling and logging
func ErrorHandlingMiddleware(logger *Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					// Log the panic
					logger.LogError(
						r.Context(),
						err.(error),
						"HTTP request panic",
						zap.String("method", r.Method),
						zap.String("path", r.URL.Path),
						zap.String("user_agent", r.UserAgent()),
						zap.String("remote_addr", r.RemoteAddr),
					)

					// Return 500 Internal Server Error
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// HealthCheckMiddleware skips logging for health check endpoints
func HealthCheckMiddleware(healthPaths ...string) func(http.Handler) http.Handler {
	healthMap := make(map[string]bool)
	for _, path := range healthPaths {
		healthMap[path] = true
	}
	// Default health check paths
	healthMap["/health"] = true
	healthMap["/healthz"] = true
	healthMap["/ping"] = true

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip logging for health check endpoints
			if healthMap[r.URL.Path] {
				// Add a marker to context to skip logging
				ctx := context.WithValue(r.Context(), "skip_logging", true)
				next.ServeHTTP(w, r.WithContext(ctx))
			} else {
				next.ServeHTTP(w, r)
			}
		})
	}
}

// ConditionalLoggingMiddleware only logs if the context doesn't have skip_logging
func ConditionalLoggingMiddleware(logger *Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap response writer to capture status code
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			// Continue with the request
			next.ServeHTTP(ww, r)

			// Only log if not marked to skip
			if skip, ok := r.Context().Value("skip_logging").(bool); !ok || !skip {
				duration := time.Since(start)
				logger.HTTPRequest(
					r.Context(),
					r.Method,
					r.URL.Path,
					r.UserAgent(),
					r.RemoteAddr,
					ww.Status(),
					duration,
				)
			}
		})
	}
}

// GetLoggerFromContext extracts logger from request context
func GetLoggerFromContext(ctx context.Context) *zap.Logger {
	logger := GetGlobalLogger()
	return logger.WithContext(ctx)
}

// LogMiddleware sets up the complete logging middleware stack
func LogMiddleware(logger *Logger) []func(http.Handler) http.Handler {
	return []func(http.Handler) http.Handler{
		RequestIDMiddleware(nil), // Use default generator
		CorrelationIDMiddleware(),
		ErrorHandlingMiddleware(logger),
		HealthCheckMiddleware(), // Skip logging for health checks
		ConditionalLoggingMiddleware(logger),
	}
}

// SetupLoggingMiddleware is a convenience function to set up all logging middleware
func SetupLoggingMiddleware(r chi.Router, logger *Logger) {
	middlewares := LogMiddleware(logger)
	for _, mw := range middlewares {
		r.Use(mw)
	}
}
