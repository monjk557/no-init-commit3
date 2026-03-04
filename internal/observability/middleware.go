// Package observability provides metrics middleware for automatic HTTP request instrumentation
package observability

import (
	"net/http"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/logging"
)

// MetricsMiddleware provides automatic HTTP request instrumentation
type MetricsMiddleware struct {
	metrics *ApplicationMetrics
}

// NewMetricsMiddleware creates a new metrics middleware
func NewMetricsMiddleware() *MetricsMiddleware {
	return &MetricsMiddleware{
		metrics: GetApplicationMetrics(),
	}
}

// Handler returns the HTTP middleware handler that instruments requests
func (m *MetricsMiddleware) Handler() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Increment active requests
			m.metrics.IncrementActiveRequests()
			defer m.metrics.DecrementActiveRequests()

			// Wrap response writer to capture response details
			ww := logging.NewWrapResponseWriter(w)

			// Get request size
			requestSize := getRequestSize(r)

			// Execute the request
			next.ServeHTTP(ww, r)

			// Calculate metrics
			duration := time.Since(start)
			endpoint := normalizeEndpoint(r.URL.Path)
			method := r.Method
			statusCode := ww.Status()
			responseSize := int64(ww.BytesWritten())

			// Record HTTP metrics
			m.metrics.RecordHTTPRequest(method, endpoint, statusCode, duration, requestSize, responseSize)

			// Record errors if status indicates an error
			if statusCode >= 400 {
				errorType := categorizeHTTPError(statusCode)
				m.metrics.RecordError(errorType, "http_request")

				// Record specific error types
				if statusCode == 403 {
					m.metrics.RecordPermissionDenied("http_endpoint", method+" "+endpoint)
				}
				if statusCode == 400 {
					m.metrics.RecordValidationError("http_request", "bad_request")
				}
			}
		})
	}
}

// GitOperationMiddleware wraps Git operations to record metrics
type GitOperationMiddleware struct {
	metrics *ApplicationMetrics
}

// NewGitOperationMiddleware creates a new Git operation middleware
func NewGitOperationMiddleware() *GitOperationMiddleware {
	return &GitOperationMiddleware{
		metrics: GetApplicationMetrics(),
	}
}

// RecordOperation wraps a Git operation and records its metrics
func (g *GitOperationMiddleware) RecordOperation(operation string, fn func() error) error {
	start := time.Now()

	// Record repository access
	g.metrics.RecordRepositoryAccess()

	// Execute operation
	err := fn()

	// Record metrics
	duration := time.Since(start)
	success := err == nil
	g.metrics.RecordGitOperation(operation, duration, success)

	// Record error if operation failed
	if err != nil {
		errorType := categorizeGitError(err)
		g.metrics.RecordError(errorType, operation)
	}

	return err
}

// RecordGitObjectProcessing records processing of Git objects
func (g *GitOperationMiddleware) RecordGitObjectProcessing(objectType string, count int) {
	for i := 0; i < count; i++ {
		g.metrics.RecordGitObjectProcessed(objectType)
	}
}

// PermissionMiddleware wraps permission checks to record metrics
type PermissionMiddleware struct {
	metrics *ApplicationMetrics
}

// NewPermissionMiddleware creates a new permission middleware
func NewPermissionMiddleware() *PermissionMiddleware {
	return &PermissionMiddleware{
		metrics: GetApplicationMetrics(),
	}
}

// RecordPermissionCheck wraps a permission check and records its metrics
func (p *PermissionMiddleware) RecordPermissionCheck(fn func() (bool, error)) (bool, error) {
	start := time.Now()

	// Execute permission check
	hasAccess, err := fn()

	// Record metrics
	duration := time.Since(start)
	result := "success"
	if err != nil {
		result = "error"
	} else if !hasAccess {
		result = "denied"
	}

	p.metrics.PermissionCheckDuration.WithLabelValues(result).Observe(duration.Seconds())

	// Record errors
	if err != nil {
		p.metrics.RecordError("permission_check_error", "permission_service")
	}
	if !hasAccess && err == nil {
		p.metrics.RecordPermissionDenied("repository", "access_check")
	}

	return hasAccess, err
}

// Helper functions

// getRequestSize estimates the size of an HTTP request
func getRequestSize(r *http.Request) int64 {
	size := int64(0)

	// Add URL length
	size += int64(len(r.URL.String()))

	// Add headers size (rough estimate)
	for name, values := range r.Header {
		size += int64(len(name))
		for _, value := range values {
			size += int64(len(value))
		}
	}

	// Add content length if available
	if r.ContentLength > 0 {
		size += r.ContentLength
	}

	return size
}

// normalizeEndpoint converts URL paths to metric-friendly endpoint names
func normalizeEndpoint(path string) string {
	// Remove query parameters
	if idx := strings.Index(path, "?"); idx != -1 {
		path = path[:idx]
	}

	// Replace path parameters with placeholders
	path = strings.ReplaceAll(path, "/repos/", "/repos/{owner}/")

	// Handle common Git endpoints
	if strings.Contains(path, "/repos/{owner}/") {
		parts := strings.Split(path, "/")
		if len(parts) >= 4 {
			// Replace owner and repo with placeholders
			parts[2] = "{owner}"
			parts[3] = "{repo}"
			path = strings.Join(parts, "/")
		}
	}

	// Handle SSH protocol endpoints
	if strings.HasPrefix(path, "/ssh/") {
		path = "/ssh/{command}"
	}

	// Rune-safe truncation to prevent splitting multi-byte UTF-8 characters
	if len(path) > 200 {
		r := []rune(path)
		if len(r) > 60 {
			r = r[:60]
		}
		path = string(r) + "..."
	}

	return path
}

// categorizeHTTPError categorizes HTTP errors for metrics
func categorizeHTTPError(statusCode int) string {
	switch {
	case statusCode >= 500:
		return "server_error"
	case statusCode == 404:
		return "not_found"
	case statusCode == 403:
		return "permission_denied"
	case statusCode == 401:
		return "unauthorized"
	case statusCode == 400:
		return "bad_request"
	case statusCode >= 400:
		return "client_error"
	default:
		return "unknown_error"
	}
}

// categorizeGitError categorizes Git errors for metrics
func categorizeGitError(err error) string {
	if err == nil {
		return "none"
	}

	errStr := strings.ToLower(err.Error())

	switch {
	case strings.Contains(errStr, "not found"):
		return "not_found"
	case strings.Contains(errStr, "permission"):
		return "permission_denied"
	case strings.Contains(errStr, "timeout"):
		return "timeout"
	case strings.Contains(errStr, "invalid"):
		return "invalid_input"
	case strings.Contains(errStr, "network"):
		return "network_error"
	case strings.Contains(errStr, "repository"):
		return "repository_error"
	default:
		return "git_error"
	}
}

// MetricsConfig holds configuration for the metrics middleware
type MetricsMiddlewareConfig struct {
	// EnableResourceCollection enables automatic resource usage collection
	EnableResourceCollection bool `mapstructure:"enable_resource_collection"`

	// ResourceCollectionInterval defines how often to collect resource metrics
	ResourceCollectionInterval time.Duration `mapstructure:"resource_collection_interval"`

	// EndpointNormalization enables path normalization for metrics
	EndpointNormalization bool `mapstructure:"endpoint_normalization"`

	// MaxPathLength limits the maximum path length in metrics
	MaxPathLength int `mapstructure:"max_path_length"`
}

// DefaultMetricsMiddlewareConfig returns default configuration
func DefaultMetricsMiddlewareConfig() MetricsMiddlewareConfig {
	return MetricsMiddlewareConfig{
		EnableResourceCollection:   true,
		ResourceCollectionInterval: 15 * time.Second,
		EndpointNormalization:      true,
		MaxPathLength:              100,
	}
}

// CacheMetricsIntegration provides integration with existing cache metrics
type CacheMetricsIntegration struct {
	metrics *ApplicationMetrics
}

// NewCacheMetricsIntegration creates cache metrics integration
func NewCacheMetricsIntegration() *CacheMetricsIntegration {
	return &CacheMetricsIntegration{
		metrics: GetApplicationMetrics(),
	}
}

// UpdateCacheHitRate updates the repository cache hit rate metric
func (c *CacheMetricsIntegration) UpdateCacheHitRate(hitRate float64) {
	c.metrics.RepositoryCacheHitRate.Set(hitRate * 100) // Convert to percentage
}

// APIServiceCallsTracker tracks calls to external API services
type APIServiceCallsTracker struct {
	metrics *ApplicationMetrics
}

// NewAPIServiceCallsTracker creates an API service calls tracker
func NewAPIServiceCallsTracker() *APIServiceCallsTracker {
	return &APIServiceCallsTracker{
		metrics: GetApplicationMetrics(),
	}
}

// RecordAPICall records a call to an external API service
func (a *APIServiceCallsTracker) RecordAPICall(endpoint string, success bool) {
	result := "success"
	if !success {
		result = "error"
	}
	a.metrics.APIServiceCallsTotal.WithLabelValues(endpoint, result).Inc()
}
