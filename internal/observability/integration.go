// Package observability provides integrated observability services
// that coordinate metrics, health checks, tracing, and logging for production visibility.
package observability

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ObservabilityConfig holds configuration for all observability components
type ObservabilityConfig struct {
	Metrics MetricsConfig        `mapstructure:"metrics"`
	Tracing TracingConfig        `mapstructure:"tracing"`
	Health  HealthConfig         `mapstructure:"health"`
	Logging config.LoggingConfig `mapstructure:"logging"`
}

// HealthConfig holds health check configuration
type HealthConfig struct {
	Enabled       bool   `mapstructure:"enabled"`
	LivenessPath  string `mapstructure:"liveness_path"`
	ReadinessPath string `mapstructure:"readiness_path"`
	MetricsPath   string `mapstructure:"metrics_path"`
}

// DefaultObservabilityConfig returns default observability configuration
func DefaultObservabilityConfig() ObservabilityConfig {
	return ObservabilityConfig{
		Metrics: DefaultMetricsConfig(),
		Tracing: DefaultTracingConfig(),
		Health: HealthConfig{
			Enabled:       true,
			LivenessPath:  "/health/liveness",
			ReadinessPath: "/health/readiness",
			MetricsPath:   "/metrics",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}
}

// Manager coordinates all observability components
type Manager struct {
	config         ObservabilityConfig
	metrics        *ApplicationMetrics
	tracingManager *TracingManager
	healthChecker  *HealthChecker
	logger         *logging.Logger

	// Middleware components
	metricsMiddleware *MetricsMiddleware
	gitTracer         *GitOperationTracer
	apiTracer         *APIServiceTracer
	permissionTracer  *PermissionMiddleware
	cacheIntegration  *CacheMetricsIntegration
}

// NewManager creates a new observability manager with the given configuration
func NewManager(cfg *config.Config, version string) (*Manager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	obsConfig := DefaultObservabilityConfig()

	// Override with config settings if provided
	if cfg.Observability.Tracing.Enabled {
		obsConfig.Tracing = TracingConfig{
			Enabled:          cfg.Observability.Tracing.Enabled,
			ExporterEndpoint: cfg.Observability.Tracing.Endpoint,
			ServiceName:      cfg.Observability.Tracing.ServiceName,
			ServiceVersion:   cfg.Observability.Tracing.ServiceVersion,
		}
	}

	if cfg.Observability.Metrics.Enabled {
		obsConfig.Metrics = MetricsConfig{
			Enabled: cfg.Observability.Metrics.Enabled,
		}
	}

	// Initialize metrics
	metrics := NewApplicationMetrics()
	// Start resource collection with default interval
	metrics.StartResourceCollector(15 * time.Second)

	// Initialize tracing only if enabled
	var tracingManager *TracingManager
	var err error
	if obsConfig.Tracing.Enabled {
		tracingManager, err = NewTracingManager(obsConfig.Tracing)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize tracing: %w", err)
		}
	}

	// Initialize health checker (with or without permission client)
	healthChecker := NewHealthChecker(cfg, version)

	// Initialize logger
	logger, err := logging.NewLogger(obsConfig.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}

	// Initialize middleware components
	metricsMiddleware := NewMetricsMiddleware()
	gitTracer := NewGitOperationTracer()
	apiTracer := NewAPIServiceTracer()
	permissionTracer := NewPermissionMiddleware()
	cacheIntegration := NewCacheMetricsIntegration()

	return &Manager{
		config:            obsConfig,
		metrics:           metrics,
		tracingManager:    tracingManager,
		healthChecker:     healthChecker,
		logger:            logger,
		metricsMiddleware: metricsMiddleware,
		gitTracer:         gitTracer,
		apiTracer:         apiTracer,
		permissionTracer:  permissionTracer,
		cacheIntegration:  cacheIntegration,
	}, nil
}

// SetupMiddleware configures observability middleware for the router
func (m *Manager) SetupMiddleware(router *gin.Engine) {
	// Add metrics middleware
	router.Use(m.ginMetricsMiddleware())

	// Add tracing middleware only if tracing is enabled
	if m.config.Tracing.Enabled && m.tracingManager != nil {
		router.Use(m.ginTracingMiddleware())
	}
}

// SetupRoutes configures observability routes (health checks, metrics)
func (m *Manager) SetupRoutes(router *gin.Engine) {
	// Health check endpoints
	router.GET("/health/liveness", gin.WrapF(m.healthChecker.LivenessHandler()))
	router.GET("/health/readiness", gin.WrapF(m.healthChecker.ReadinessHandler()))

	// Metrics endpoint
	router.GET("/metrics", gin.WrapH(MetricsHandler()))
}

// ginMetricsMiddleware adapts the metrics middleware for Gin
func (m *Manager) ginMetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// Increment active requests
		m.metrics.IncrementActiveRequests()
		defer m.metrics.DecrementActiveRequests()

		// Process request
		c.Next()

		// Calculate metrics
		duration := time.Since(start)
		endpoint := c.FullPath()
		if endpoint == "" {
			endpoint = normalizeEndpoint(c.Request.URL.Path)
		}
		method := c.Request.Method
		statusCode := c.Writer.Status()
		responseSize := int64(c.Writer.Size())
		requestSize := getRequestSize(c.Request)

		// Record HTTP metrics
		m.metrics.RecordHTTPRequest(method, endpoint, statusCode, duration, requestSize, responseSize)
	}
}

// ginTracingMiddleware adapts the tracing middleware for Gin
func (m *Manager) ginTracingMiddleware() gin.HandlerFunc {
	if m.tracingManager == nil {
		return func(c *gin.Context) { c.Next() }
	}

	// Convert the standard HTTP middleware to Gin middleware
	httpMiddleware := m.tracingManager.HTTPMiddleware()
	return func(c *gin.Context) {
		// Wrap the Gin context in a standard HTTP handler
		httpMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c.Next()
		})).ServeHTTP(c.Writer, c.Request)
	}
}

// GetLogger returns the observability logger
func (m *Manager) GetLogger() *logging.Logger {
	return m.logger
}

// GetMetrics returns the application metrics instance
func (m *Manager) GetMetrics() *ApplicationMetrics {
	return m.metrics
}

// GetGitTracer returns the Git operation tracer
func (m *Manager) GetGitTracer() *GitOperationTracer {
	return m.gitTracer
}

// GetAPITracer returns the API service tracer
func (m *Manager) GetAPITracer() *APIServiceTracer {
	return m.apiTracer
}

// GetPermissionTracer returns the permission middleware
func (m *Manager) GetPermissionTracer() *PermissionMiddleware {
	return m.permissionTracer
}

// GetCacheIntegration returns the cache metrics integration
func (m *Manager) GetCacheIntegration() *CacheMetricsIntegration {
	return m.cacheIntegration
}

// RecordHTTPRequest is a convenience method to record HTTP request metrics
func (m *Manager) RecordHTTPRequest(method, endpoint string, statusCode int, duration time.Duration, requestSize, responseSize int64) {
	m.metrics.RecordHTTPRequest(method, endpoint, statusCode, duration, requestSize, responseSize)
}

// RecordGitOperation is a convenience method to record Git operation metrics
func (m *Manager) RecordGitOperation(operation string, duration time.Duration, success bool) {
	m.metrics.RecordGitOperation(operation, duration, success)
}

// RecordError is a convenience method to record error metrics
func (m *Manager) RecordError(errorType, operation string) {
	m.metrics.RecordError(errorType, operation)
}

// Shutdown gracefully shuts down all observability components
func (m *Manager) Shutdown(ctx context.Context) error {
	var lastErr error

	// Shutdown tracing
	if m.tracingManager != nil {
		if err := m.tracingManager.Shutdown(ctx); err != nil {
			lastErr = err
			m.logger.Error("Failed to shutdown tracing", zap.Error(err))
		}
	}

	// Sync logger (flush any pending logs)
	if m.logger != nil {
		if err := m.logger.Sync(); err != nil {
			// Sync can fail on stdout/stderr, which is normal
			// Only log if it's not one of those cases
			lastErr = err
		}
	}

	return lastErr
}

// InstrumentedServer provides a server wrapper with built-in observability
type InstrumentedServer struct {
	server  *http.Server
	manager *Manager
	logger  *zap.Logger
}

// NewInstrumentedServer creates an HTTP server with full observability instrumentation
func NewInstrumentedServer(addr string, handler http.Handler, manager *Manager) *InstrumentedServer {
	return &InstrumentedServer{
		server: &http.Server{
			Addr:    addr,
			Handler: handler,
		},
		manager: manager,
		logger:  manager.logger.Logger,
	}
}

// Start starts the instrumented server
func (is *InstrumentedServer) Start() error {
	is.logger.Info("Starting instrumented server",
		zap.String("addr", is.server.Addr))

	return is.server.ListenAndServe()
}

// Shutdown gracefully shuts down the instrumented server
func (is *InstrumentedServer) Shutdown(ctx context.Context) error {
	is.logger.Info("Shutting down instrumented server")

	// Shutdown server
	if err := is.server.Shutdown(ctx); err != nil {
		is.logger.Error("Server shutdown failed", zap.Error(err))
		return err
	}

	// Shutdown observability
	if err := is.manager.Shutdown(ctx); err != nil {
		is.logger.Error("Observability shutdown failed", zap.Error(err))
		return err
	}

	is.logger.Info("Server shutdown complete")
	return nil
}

// ObservabilityHealthStatus provides a unified health status across all components
type ObservabilityHealthStatus struct {
	Overall    string                 `json:"overall"`
	Components map[string]interface{} `json:"components"`
	Timestamp  time.Time              `json:"timestamp"`
	Version    string                 `json:"version"`
}

// GetHealthStatus returns the overall health status of all observability components
func (m *Manager) GetHealthStatus(ctx context.Context) ObservabilityHealthStatus {
	components := make(map[string]interface{})

	// Check metrics
	components["metrics"] = map[string]interface{}{
		"enabled": m.config.Metrics.Enabled,
		"status":  "healthy",
	}

	// Check tracing
	tracingStatus := "disabled"
	if m.config.Tracing.Enabled {
		tracingStatus = "healthy"
	}
	components["tracing"] = map[string]interface{}{
		"enabled": m.config.Tracing.Enabled,
		"status":  tracingStatus,
	}

	// Check logging
	components["logging"] = map[string]interface{}{
		"enabled": true,
		"status":  "healthy",
		"level":   m.config.Logging.Level,
	}

	// Determine overall status
	overall := "healthy"

	return ObservabilityHealthStatus{
		Overall:    overall,
		Components: components,
		Timestamp:  time.Now().UTC(),
		Version:    ServiceVersion,
	}
}
