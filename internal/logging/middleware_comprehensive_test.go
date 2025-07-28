package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Tests for PRD Endpoint Access Control Matrix compliance

func TestEndpointAccessControl_PRDCompliance(t *testing.T) {
	// PRD specifies strict endpoint access control by component source
	testCases := []struct {
		name           string
		source         string // X-Source header
		method         string
		path           string
		expectedStatus int
		description    string
	}{
		// HTTP Gateway - Allowed endpoints
		{
			name:           "HTTP Gateway - Git Info Refs",
			source:         "http-gateway",
			method:         "GET",
			path:           "/owner/repo/info/refs",
			expectedStatus: 200,
			description:    "Git reference advertisement",
		},
		{
			name:           "HTTP Gateway - Upload Pack",
			source:         "http-gateway",
			method:         "POST",
			path:           "/owner/repo/git-upload-pack",
			expectedStatus: 200,
			description:    "Git fetch/clone operations",
		},
		{
			name:           "HTTP Gateway - Receive Pack",
			source:         "http-gateway",
			method:         "POST",
			path:           "/owner/repo/git-receive-pack",
			expectedStatus: 200,
			description:    "Git push operations",
		},

		// SSH Gateway - Allowed endpoints
		{
			name:           "SSH Gateway - SSH Upload Pack",
			source:         "ssh-gateway",
			method:         "POST",
			path:           "/ssh/upload-pack",
			expectedStatus: 200,
			description:    "SSH fetch/clone operations",
		},
		{
			name:           "SSH Gateway - SSH Receive Pack",
			source:         "ssh-gateway",
			method:         "POST",
			path:           "/ssh/receive-pack",
			expectedStatus: 200,
			description:    "SSH push operations",
		},

		// Orchestrator - Allowed endpoints
		{
			name:           "Orchestrator - Browse Tree",
			source:         "orchestrator",
			method:         "GET",
			path:           "/repos/owner/repo/tree",
			expectedStatus: 200,
			description:    "Browse repository tree",
		},
		{
			name:           "Orchestrator - Get Blob",
			source:         "orchestrator",
			method:         "GET",
			path:           "/repos/owner/repo/blob",
			expectedStatus: 200,
			description:    "Get file content",
		},
		{
			name:           "Orchestrator - Get Commits",
			source:         "orchestrator",
			method:         "GET",
			path:           "/repos/owner/repo/commits",
			expectedStatus: 200,
			description:    "Get commit history",
		},
		{
			name:           "Orchestrator - Get Diff",
			source:         "orchestrator",
			method:         "GET",
			path:           "/repos/owner/repo/diff",
			expectedStatus: 200,
			description:    "Compare branches/commits",
		},

		// Common endpoints - All sources allowed
		{
			name:           "Health Check - HTTP Gateway",
			source:         "http-gateway",
			method:         "GET",
			path:           "/health",
			expectedStatus: 200,
			description:    "Health check",
		},
		{
			name:           "Health Check - SSH Gateway",
			source:         "ssh-gateway",
			method:         "GET",
			path:           "/health",
			expectedStatus: 200,
			description:    "Health check",
		},
		{
			name:           "Health Check - Orchestrator",
			source:         "orchestrator",
			method:         "GET",
			path:           "/health",
			expectedStatus: 200,
			description:    "Health check",
		},

		// Forbidden access attempts (violating PRD access matrix)
		{
			name:           "HTTP Gateway accessing REST API - FORBIDDEN",
			source:         "http-gateway",
			method:         "GET",
			path:           "/repos/owner/repo/tree",
			expectedStatus: 403,
			description:    "HTTP Gateway should NOT access REST endpoints",
		},
		{
			name:           "SSH Gateway accessing HTTP Git Protocol - FORBIDDEN",
			source:         "ssh-gateway",
			method:         "POST",
			path:           "/owner/repo/git-upload-pack",
			expectedStatus: 403,
			description:    "SSH Gateway should NOT access HTTP Git endpoints",
		},
		{
			name:           "Orchestrator accessing Git Protocol - FORBIDDEN",
			source:         "orchestrator",
			method:         "POST",
			path:           "/owner/repo/git-receive-pack",
			expectedStatus: 403,
			description:    "Orchestrator should NOT access Git protocol endpoints",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Capture logs
			var logBuf bytes.Buffer
			core := zapcore.NewCore(
				zapcore.NewJSONEncoder(zapcore.EncoderConfig{
					MessageKey:  "message",
					LevelKey:    "level",
					EncodeLevel: zapcore.LowercaseLevelEncoder,
				}),
				zapcore.AddSync(&logBuf),
				zapcore.DebugLevel,
			)
			logger := &Logger{Logger: zap.New(core)}

			// Create mock access control middleware
			accessControlMiddleware := func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					source := r.Header.Get("X-Source")
					path := r.URL.Path

					// Implement PRD access control matrix
					if !isAuthorizedAccess(source, path) {
						logger.LogWarning(r.Context(), "Unauthorized access attempt",
							zap.String("source", source),
							zap.String("path", path),
							zap.String("method", r.Method),
						)
						http.Error(w, "Forbidden", http.StatusForbidden)
						return
					}

					next.ServeHTTP(w, r)
				})
			}

			// Setup router with middleware
			r := chi.NewRouter()
			r.Use(RequestIDMiddleware(nil))
			r.Use(CorrelationIDMiddleware())
			r.Use(accessControlMiddleware)
			r.Use(LoggingMiddleware(logger))

			// Add test endpoints
			r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("OK"))
			})
			r.Get("/{owner}/{repo}/info/refs", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			r.Post("/{owner}/{repo}/git-upload-pack", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			r.Post("/{owner}/{repo}/git-receive-pack", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			r.Post("/ssh/upload-pack", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			r.Post("/ssh/receive-pack", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			r.Get("/repos/{owner}/{repo}/tree", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			r.Get("/repos/{owner}/{repo}/blob", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			r.Get("/repos/{owner}/{repo}/commits", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			r.Get("/repos/{owner}/{repo}/diff", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			// Create request
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("X-Source", tc.source)
			req.Header.Set("X-User-ID", "test-user-123")
			req.Header.Set("X-Request-ID", "test-request-456")

			// Execute request
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			// Verify status code
			assert.Equal(t, tc.expectedStatus, w.Code, "Status code mismatch for %s", tc.description)

			// Verify logging occurred
			logOutput := logBuf.String()
			if tc.expectedStatus == 403 {
				// Should have warning log for forbidden access
				assert.Contains(t, logOutput, "Unauthorized access attempt")
				assert.Contains(t, logOutput, "warn")
			} else {
				// Should have info log for successful access
				assert.Contains(t, logOutput, "info")
			}
		})
	}
}

// Helper function implementing PRD access control matrix
func isAuthorizedAccess(source, path string) bool {
	switch source {
	case "http-gateway":
		// HTTP Gateway can only access Git Smart HTTP endpoints
		return strings.Contains(path, "/info/refs") ||
			strings.Contains(path, "/git-upload-pack") ||
			strings.Contains(path, "/git-receive-pack") ||
			path == "/health" || path == "/metrics"

	case "ssh-gateway":
		// SSH Gateway can only access SSH endpoints
		return strings.HasPrefix(path, "/ssh/") ||
			path == "/health" || path == "/metrics"

	case "orchestrator":
		// Orchestrator can only access REST API endpoints
		return strings.HasPrefix(path, "/repos/") ||
			path == "/health" || path == "/metrics"

	default:
		// Unknown source - deny access
		return false
	}
}

func TestRequestIDMiddleware_Comprehensive(t *testing.T) {
	t.Run("WithDefaultGenerator", func(t *testing.T) {
		middleware := RequestIDMiddleware(nil)

		// Create test handler
		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify request ID in context
			requestID, ok := r.Context().Value(RequestIDKey).(string)
			assert.True(t, ok)
			assert.NotEmpty(t, requestID)

			// Verify request ID in response header
			assert.Equal(t, requestID, w.Header().Get("X-Request-ID"))

			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("WithCustomGenerator", func(t *testing.T) {
		customGen := &customRequestIDGenerator{prefix: "custom"}
		middleware := RequestIDMiddleware(customGen)

		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID, ok := r.Context().Value(RequestIDKey).(string)
			assert.True(t, ok)
			assert.True(t, strings.HasPrefix(requestID, "custom-"))

			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

type customRequestIDGenerator struct {
	prefix  string
	counter int
}

func (g *customRequestIDGenerator) Generate() string {
	g.counter++
	return fmt.Sprintf("%s-%d", g.prefix, g.counter)
}

func TestCorrelationIDMiddleware_Comprehensive(t *testing.T) {
	middleware := CorrelationIDMiddleware()

	t.Run("WithProvidedCorrelationID", func(t *testing.T) {
		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			correlationID, ok := r.Context().Value(CorrelationIDKey).(string)
			assert.True(t, ok)
			assert.Equal(t, "test-correlation-123", correlationID)

			// Verify correlation ID in response header
			assert.Equal(t, "test-correlation-123", w.Header().Get("X-Correlation-ID"))

			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("X-Correlation-ID", "test-correlation-123")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("WithoutCorrelationID_UsesRequestID", func(t *testing.T) {
		// First apply request ID middleware
		requestIDMiddleware := RequestIDMiddleware(nil)
		combined := requestIDMiddleware(middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID, ok := r.Context().Value(RequestIDKey).(string)
			assert.True(t, ok)

			correlationID, ok := r.Context().Value(CorrelationIDKey).(string)
			assert.True(t, ok)
			assert.Equal(t, requestID, correlationID, "Should use request ID as correlation ID")

			w.WriteHeader(http.StatusOK)
		})))

		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		combined.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestErrorHandlingMiddleware_Comprehensive(t *testing.T) {
	var logBuf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			MessageKey:  "message",
			LevelKey:    "level",
			EncodeLevel: zapcore.LowercaseLevelEncoder,
		}),
		zapcore.AddSync(&logBuf),
		zapcore.ErrorLevel,
	)
	logger := &Logger{Logger: zap.New(core)}

	middleware := ErrorHandlingMiddleware(logger)

	t.Run("PanicRecovery", func(t *testing.T) {
		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic(fmt.Errorf("test panic"))
		}))

		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()

		// Should not panic
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Contains(t, w.Body.String(), "Internal Server Error")

		// Verify panic was logged
		logOutput := logBuf.String()
		assert.Contains(t, logOutput, "HTTP request panic")
		assert.Contains(t, logOutput, "error")
	})

	t.Run("NormalExecution", func(t *testing.T) {
		logBuf.Reset()

		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("success"))
		}))

		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "success", w.Body.String())

		// Should not have logged any errors
		assert.Empty(t, logBuf.String())
	})
}

func TestHealthCheckMiddleware_Comprehensive(t *testing.T) {
	var logBuf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			MessageKey: "message",
		}),
		zapcore.AddSync(&logBuf),
		zapcore.InfoLevel,
	)
	logger := &Logger{Logger: zap.New(core)}

	healthMiddleware := HealthCheckMiddleware("/custom-health")
	loggingMiddleware := ConditionalLoggingMiddleware(logger)

	combined := healthMiddleware(loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	testCases := []struct {
		path        string
		shouldLog   bool
		description string
	}{
		{"/health", false, "Default health check should not log"},
		{"/healthz", false, "Default healthz should not log"},
		{"/ping", false, "Default ping should not log"},
		{"/custom-health", false, "Custom health check should not log"},
		{"/api/users", true, "Regular API should log"},
		{"/repos/owner/repo", true, "Repository API should log"},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			logBuf.Reset()

			req := httptest.NewRequest("GET", tc.path, nil)
			w := httptest.NewRecorder()
			combined.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			logOutput := logBuf.String()
			if tc.shouldLog {
				assert.NotEmpty(t, logOutput, "Should have logged for %s", tc.path)
			} else {
				assert.Empty(t, logOutput, "Should not have logged for %s", tc.path)
			}
		})
	}
}

func TestLogMiddleware_CompleteStack(t *testing.T) {
	// Test the complete logging middleware stack
	var logBuf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			MessageKey:  "message",
			LevelKey:    "level",
			TimeKey:     "timestamp",
			EncodeLevel: zapcore.LowercaseLevelEncoder,
			EncodeTime:  zapcore.ISO8601TimeEncoder,
		}),
		zapcore.AddSync(&logBuf),
		zapcore.InfoLevel,
	)
	logger := &Logger{Logger: zap.New(core)}

	// Setup router with complete middleware stack
	r := chi.NewRouter()
	middlewares := LogMiddleware(logger)
	for _, mw := range middlewares {
		r.Use(mw)
	}

	r.Get("/api/test", func(w http.ResponseWriter, r *http.Request) {
		// Verify all context values are present
		requestID, ok := r.Context().Value(RequestIDKey).(string)
		assert.True(t, ok)
		assert.NotEmpty(t, requestID)

		correlationID, ok := r.Context().Value(CorrelationIDKey).(string)
		assert.True(t, ok)
		assert.NotEmpty(t, correlationID)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("test response"))
	})

	// Make request
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-Correlation-ID", "test-corr-123")
	req.Header.Set("User-Agent", "test-agent/1.0")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	// Verify response
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "test response", w.Body.String())

	// Verify headers
	assert.NotEmpty(t, w.Header().Get("X-Request-ID"))
	assert.Equal(t, "test-corr-123", w.Header().Get("X-Correlation-ID"))

	// Verify logging
	logOutput := logBuf.String()
	assert.NotEmpty(t, logOutput)

	var logEntry map[string]interface{}
	err := json.Unmarshal([]byte(logOutput), &logEntry)
	require.NoError(t, err)

	assert.Equal(t, "info", logEntry["level"])
	assert.Equal(t, "GET", logEntry["method"])
	assert.Equal(t, "/api/test", logEntry["path"])
	assert.Equal(t, "test-agent/1.0", logEntry["user_agent"])
	assert.Equal(t, float64(200), logEntry["status_code"])
	assert.Contains(t, logEntry, "request_id")
	assert.Equal(t, "test-corr-123", logEntry["correlation_id"])
	assert.Contains(t, logEntry, "duration")
}

func TestSetupLoggingMiddleware_Integration(t *testing.T) {
	// Test the convenience setup function
	var logBuf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			MessageKey: "message",
		}),
		zapcore.AddSync(&logBuf),
		zapcore.InfoLevel,
	)
	logger := &Logger{Logger: zap.New(core)}

	r := chi.NewRouter()
	SetupLoggingMiddleware(r, logger)

	r.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, logBuf.String())
}

func TestGetLoggerFromContext_PRDCompliance(t *testing.T) {
	// Test logger extraction from context for PRD compliance
	ctx := context.Background()
	ctx = context.WithValue(ctx, RequestIDKey, "ctx-req-123")
	ctx = context.WithValue(ctx, CorrelationIDKey, "ctx-corr-456")

	logger := GetLoggerFromContext(ctx)
	assert.NotNil(t, logger)

	// Capture log to verify context is included
	var buf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			MessageKey: "message",
		}),
		zapcore.AddSync(&buf),
		zapcore.InfoLevel,
	)

	// Replace the logger's core to capture output
	globalLogger = &Logger{Logger: zap.New(core)}

	logger = GetLoggerFromContext(ctx)
	logger.Info("Test context logger")

	var logEntry map[string]interface{}
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	assert.Equal(t, "ctx-req-123", logEntry["request_id"])
	assert.Equal(t, "ctx-corr-456", logEntry["correlation_id"])
}
