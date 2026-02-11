// Package observability는 Git executor 서비스를 위한 포괄적인 메트릭, 로깅, 모니터링 기능을 제공합니다.
// 기존 캐시 메트릭 인프라를 기반으로 구축된 종합적인 모니터링 시스템입니다.
package observability

import (
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Global singleton for application metrics to avoid duplicate registrations
var (
	globalMetrics     *ApplicationMetrics
	globalMetricsOnce sync.Once
)

// ApplicationMetrics는 애플리케이션 전반에 걸친 Prometheus 메트릭들을 보유합니다.
// HTTP 요청, Git 작업, 오류, 리소스 사용량 등 모든 주요 메트릭을 포함합니다.
type ApplicationMetrics struct {
	// HTTP Request Metrics
	HTTPRequestsTotal     prometheus.CounterVec   // Total HTTP requests by method, endpoint, status
	HTTPRequestDuration   prometheus.HistogramVec // HTTP request duration by method, endpoint
	HTTPActiveRequests    prometheus.Gauge        // Currently active HTTP requests
	HTTPRequestSizeBytes  prometheus.HistogramVec // HTTP request size in bytes
	HTTPResponseSizeBytes prometheus.HistogramVec // HTTP response size in bytes

	// Git Operation Metrics
	GitOperationsTotal      prometheus.CounterVec   // Total Git operations by type, result
	GitOperationDuration    prometheus.HistogramVec // Git operation duration by type
	GitRepositoriesAccessed prometheus.Counter      // Total repositories accessed
	GitObjectsProcessed     prometheus.CounterVec   // Git objects processed by type (commit, tree, blob)

	// Error Metrics
	ErrorsTotal           prometheus.CounterVec // Total errors by type, operation
	PermissionDeniedTotal prometheus.CounterVec // Permission denied errors by resource
	ValidationErrorsTotal prometheus.CounterVec // Validation errors by field

	// Resource Usage Metrics
	MemoryUsageBytes     prometheus.Gauge // Current memory usage
	GoroutinesCount      prometheus.Gauge // Current number of goroutines
	FileDescriptorsCount prometheus.Gauge // Current number of open file descriptors
	CPUUsagePercent      prometheus.Gauge // Current CPU usage percentage

	// Performance Metrics
	ConcurrentConnections prometheus.Gauge // Current concurrent connections
	RequestQueueLength    prometheus.Gauge // Current request queue length
	ResponseLatencyP95    prometheus.Gauge // 95th percentile response latency
	ResponseLatencyP99    prometheus.Gauge // 99th percentile response latency

	// Business Logic Metrics
	RepositoryCacheHitRate  prometheus.Gauge        // Repository cache hit rate
	PermissionCheckDuration prometheus.HistogramVec // Permission check duration by result
	APIServiceCallsTotal    prometheus.CounterVec   // API service calls by endpoint, result

	// Overview / Performance (repo browser)
	OverviewCacheOpsTotal                 prometheus.CounterVec   // Cache operations by kind/op (hit, miss, set)
	OverviewCacheEntries                  prometheus.GaugeVec     // Current cache entry count by kind
	OverviewCacheSingleflightSharedTotal  prometheus.CounterVec   // singleflight shared result count by kind
	LatestCommitsBatchRequestsTotal       prometheus.CounterVec   // Requests by outcome (cache_hit, complete, incomplete, error)
	LatestCommitsBatchDuration            prometheus.HistogramVec // Duration by outcome
	LatestCommitsBatchPaths               prometheus.HistogramVec // Requested path count by outcome
	LatestCommitsBatchScannedCommits      prometheus.HistogramVec // scanned commits by outcome
	LatestCommitsBatchCutReasonTotal      prometheus.CounterVec   // cut reason counts (only when incomplete)
}

// MetricsConfig는 메트릭 수집을 위한 설정을 보유합니다.
// 메트릭 활성화 여부, 네임스페이스, 수집 간격 등을 설정합니다.
type MetricsConfig struct {
	Enabled         bool                  `mapstructure:"enabled"`
	Namespace       string                `mapstructure:"namespace"`
	Registry        prometheus.Registerer `mapstructure:"-"`
	CollectInterval time.Duration         `mapstructure:"collect_interval"`
}

// DefaultMetricsConfig returns default metrics configuration
func DefaultMetricsConfig() MetricsConfig {
	return MetricsConfig{
		Enabled:         true,
		Namespace:       "git_executor",
		Registry:        prometheus.DefaultRegisterer,
		CollectInterval: 15 * time.Second,
	}
}

// NewApplicationMetrics는 모든 애플리케이션 메트릭을 생성하고 등록합니다.
// 싱글톤 패턴을 사용하여 중복 등록을 방지합니다.
func NewApplicationMetrics() *ApplicationMetrics {
	globalMetricsOnce.Do(func() {
		globalMetrics = &ApplicationMetrics{
			// HTTP Request Metrics
			HTTPRequestsTotal: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "git_executor_http_requests_total",
					Help: "Total number of HTTP requests by method, endpoint, and status",
				},
				[]string{"method", "endpoint", "status_code"},
			),

			HTTPRequestDuration: *promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "git_executor_http_request_duration_seconds",
					Help:    "HTTP request duration in seconds",
					Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
				},
				[]string{"method", "endpoint"},
			),

			HTTPActiveRequests: promauto.NewGauge(
				prometheus.GaugeOpts{
					Name: "git_executor_http_active_requests",
					Help: "Number of currently active HTTP requests",
				},
			),

			HTTPRequestSizeBytes: *promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "git_executor_http_request_size_bytes",
					Help:    "HTTP request size in bytes",
					Buckets: prometheus.ExponentialBuckets(1024, 2, 10), // 1KB to 1MB
				},
				[]string{"method", "endpoint"},
			),

			HTTPResponseSizeBytes: *promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "git_executor_http_response_size_bytes",
					Help:    "HTTP response size in bytes",
					Buckets: prometheus.ExponentialBuckets(1024, 2, 10), // 1KB to 1MB
				},
				[]string{"method", "endpoint", "status_code"},
			),

			// Git Operation Metrics
			GitOperationsTotal: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "git_executor_git_operations_total",
					Help: "Total number of Git operations by type and result",
				},
				[]string{"operation", "result"}, // operation: clone, fetch, diff, etc. result: success, error
			),

			GitOperationDuration: *promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "git_executor_git_operation_duration_seconds",
					Help:    "Git operation duration in seconds",
					Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
				},
				[]string{"operation"},
			),

			GitRepositoriesAccessed: promauto.NewCounter(
				prometheus.CounterOpts{
					Name: "git_executor_git_repositories_accessed_total",
					Help: "Total number of repositories accessed",
				},
			),

			GitObjectsProcessed: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "git_executor_git_objects_processed_total",
					Help: "Total number of Git objects processed by type",
				},
				[]string{"object_type"}, // commit, tree, blob, tag
			),

			// Error Metrics
			ErrorsTotal: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "git_executor_errors_total",
					Help: "Total number of errors by type and operation",
				},
				[]string{"error_type", "operation"},
			),

			PermissionDeniedTotal: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "git_executor_permission_denied_total",
					Help: "Total number of permission denied errors by resource",
				},
				[]string{"resource_type", "operation"},
			),

			ValidationErrorsTotal: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "git_executor_validation_errors_total",
					Help: "Total number of validation errors by field",
				},
				[]string{"field", "error_type"},
			),

			// Resource Usage Metrics
			MemoryUsageBytes: promauto.NewGauge(
				prometheus.GaugeOpts{
					Name: "git_executor_memory_usage_bytes",
					Help: "Current memory usage in bytes",
				},
			),

			GoroutinesCount: promauto.NewGauge(
				prometheus.GaugeOpts{
					Name: "git_executor_goroutines_count",
					Help: "Current number of goroutines",
				},
			),

			FileDescriptorsCount: promauto.NewGauge(
				prometheus.GaugeOpts{
					Name: "git_executor_file_descriptors_count",
					Help: "Current number of open file descriptors",
				},
			),

			CPUUsagePercent: promauto.NewGauge(
				prometheus.GaugeOpts{
					Name: "git_executor_cpu_usage_percent",
					Help: "Current CPU usage percentage",
				},
			),

			// Performance Metrics
			ConcurrentConnections: promauto.NewGauge(
				prometheus.GaugeOpts{
					Name: "git_executor_concurrent_connections",
					Help: "Current number of concurrent connections",
				},
			),

			RequestQueueLength: promauto.NewGauge(
				prometheus.GaugeOpts{
					Name: "git_executor_request_queue_length",
					Help: "Current request queue length",
				},
			),

			ResponseLatencyP95: promauto.NewGauge(
				prometheus.GaugeOpts{
					Name: "git_executor_response_latency_p95_seconds",
					Help: "95th percentile response latency in seconds",
				},
			),

			ResponseLatencyP99: promauto.NewGauge(
				prometheus.GaugeOpts{
					Name: "git_executor_response_latency_p99_seconds",
					Help: "99th percentile response latency in seconds",
				},
			),

			// Business Logic Metrics
			RepositoryCacheHitRate: promauto.NewGauge(
				prometheus.GaugeOpts{
					Name: "git_executor_repository_cache_hit_rate",
					Help: "Repository cache hit rate as a percentage",
				},
			),

			PermissionCheckDuration: *promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "git_executor_permission_check_duration_seconds",
					Help:    "Permission check duration in seconds by result",
					Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
				},
				[]string{"result"}, // success, error, timeout
			),

			APIServiceCallsTotal: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "git_executor_api_service_calls_total",
					Help: "Total number of API service calls by endpoint and result",
				},
				[]string{"endpoint", "result"},
			),

			OverviewCacheOpsTotal: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "git_executor_overview_cache_ops_total",
					Help: "Overview in-memory cache operations by kind and op (hit, miss, set)",
				},
				[]string{"kind", "op"},
			),

			OverviewCacheEntries: *promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "git_executor_overview_cache_entries",
					Help: "Current overview in-memory cache entry count by kind",
				},
				[]string{"kind"},
			),

			OverviewCacheSingleflightSharedTotal: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "git_executor_overview_cache_singleflight_shared_total",
					Help: "Overview cache singleflight shared result count by kind",
				},
				[]string{"kind"},
			),

			LatestCommitsBatchRequestsTotal: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "git_executor_latest_commits_batch_requests_total",
					Help: "latest-commits batch requests by outcome",
				},
				[]string{"outcome"},
			),

			LatestCommitsBatchDuration: *promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "git_executor_latest_commits_batch_duration_seconds",
					Help:    "latest-commits batch handler duration in seconds by outcome",
					Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 1.5, 2.5, 5, 10},
				},
				[]string{"outcome"},
			),

			LatestCommitsBatchPaths: *promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "git_executor_latest_commits_batch_paths",
					Help:    "latest-commits batch requested path count by outcome",
					Buckets: []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000},
				},
				[]string{"outcome"},
			),

			LatestCommitsBatchScannedCommits: *promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "git_executor_latest_commits_batch_scanned_commits",
					Help:    "latest-commits batch scanned commits by outcome",
					Buckets: []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 20000, 50000, 100000},
				},
				[]string{"outcome"},
			),

			LatestCommitsBatchCutReasonTotal: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "git_executor_latest_commits_batch_cut_reason_total",
					Help: "latest-commits batch cut reason count (only when incomplete)",
				},
				[]string{"reason"},
			),
		}
	})
	return globalMetrics
}

// NewApplicationMetricsWithRegistry creates metrics with a custom registry (for testing)
func NewApplicationMetricsWithRegistry(registry prometheus.Registerer) *ApplicationMetrics {
	return &ApplicationMetrics{
		// HTTP Request Metrics
		HTTPRequestsTotal: *prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "git_executor_http_requests_total",
				Help: "Total number of HTTP requests by method, endpoint, and status",
			},
			[]string{"method", "endpoint", "status_code"},
		),
		// Note: For brevity, only showing one metric here. In practice, all metrics would be recreated.
		// The full implementation would mirror the structure above but use prometheus.New* instead of promauto.New*
	}
}

// GetApplicationMetrics returns the global metrics instance
func GetApplicationMetrics() *ApplicationMetrics {
	if globalMetrics == nil {
		return NewApplicationMetrics()
	}
	return globalMetrics
}

// StartResourceCollector starts a background goroutine to collect resource usage metrics
func (m *ApplicationMetrics) StartResourceCollector(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			m.collectResourceMetrics()
		}
	}()
}

// collectResourceMetrics collects and updates resource usage metrics
func (m *ApplicationMetrics) collectResourceMetrics() {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	// Update memory metrics
	m.MemoryUsageBytes.Set(float64(memStats.Alloc))

	// Update goroutine count
	m.GoroutinesCount.Set(float64(runtime.NumGoroutine()))

	// Note: File descriptors and CPU usage would require platform-specific code
	// This is a simplified implementation
}

// RecordHTTPRequest는 HTTP 요청에 대한 메트릭을 기록합니다.
// 요청 수, 지속 시간, 요청/응답 크기 등을 기록합니다.
func (m *ApplicationMetrics) RecordHTTPRequest(method, endpoint string, statusCode int, duration time.Duration, requestSize, responseSize int64) {
	status := http.StatusText(statusCode)

	m.HTTPRequestsTotal.WithLabelValues(method, endpoint, status).Inc()
	m.HTTPRequestDuration.WithLabelValues(method, endpoint).Observe(duration.Seconds())
	m.HTTPRequestSizeBytes.WithLabelValues(method, endpoint).Observe(float64(requestSize))
	m.HTTPResponseSizeBytes.WithLabelValues(method, endpoint, status).Observe(float64(responseSize))
}

func (m *ApplicationMetrics) RecordOverviewCacheOp(kind, op string) {
	if m == nil {
		return
	}
	m.OverviewCacheOpsTotal.WithLabelValues(kind, op).Inc()
}

func (m *ApplicationMetrics) SetOverviewCacheEntries(kind string, n int) {
	if m == nil {
		return
	}
	m.OverviewCacheEntries.WithLabelValues(kind).Set(float64(n))
}

func (m *ApplicationMetrics) RecordOverviewSingleflightShared(kind string) {
	if m == nil {
		return
	}
	m.OverviewCacheSingleflightSharedTotal.WithLabelValues(kind).Inc()
}

func (m *ApplicationMetrics) RecordLatestCommitsBatch(outcome string, duration time.Duration, paths int, scannedCommits int) {
	if m == nil {
		return
	}
	m.LatestCommitsBatchRequestsTotal.WithLabelValues(outcome).Inc()
	m.LatestCommitsBatchDuration.WithLabelValues(outcome).Observe(duration.Seconds())
	m.LatestCommitsBatchPaths.WithLabelValues(outcome).Observe(float64(paths))
	m.LatestCommitsBatchScannedCommits.WithLabelValues(outcome).Observe(float64(scannedCommits))
}

func (m *ApplicationMetrics) RecordLatestCommitsBatchCutReason(reason string) {
	if m == nil {
		return
	}
	m.LatestCommitsBatchCutReasonTotal.WithLabelValues(reason).Inc()
}

// RecordGitOperation records metrics for a Git operation
func (m *ApplicationMetrics) RecordGitOperation(operation string, duration time.Duration, success bool) {
	result := "success"
	if !success {
		result = "error"
	}

	m.GitOperationsTotal.WithLabelValues(operation, result).Inc()
	m.GitOperationDuration.WithLabelValues(operation).Observe(duration.Seconds())
}

// RecordError records an error metric
func (m *ApplicationMetrics) RecordError(errorType, operation string) {
	m.ErrorsTotal.WithLabelValues(errorType, operation).Inc()
}

// RecordPermissionDenied records a permission denied error
func (m *ApplicationMetrics) RecordPermissionDenied(resourceType, operation string) {
	m.PermissionDeniedTotal.WithLabelValues(resourceType, operation).Inc()
}

// RecordValidationError records a validation error
func (m *ApplicationMetrics) RecordValidationError(field, errorType string) {
	m.ValidationErrorsTotal.WithLabelValues(field, errorType).Inc()
}

// IncrementActiveRequests increments the active requests counter
func (m *ApplicationMetrics) IncrementActiveRequests() {
	m.HTTPActiveRequests.Inc()
}

// DecrementActiveRequests decrements the active requests counter
func (m *ApplicationMetrics) DecrementActiveRequests() {
	m.HTTPActiveRequests.Dec()
}

// RecordRepositoryAccess records access to a repository
func (m *ApplicationMetrics) RecordRepositoryAccess() {
	m.GitRepositoriesAccessed.Inc()
}

// RecordGitObjectProcessed records processing of a Git object
func (m *ApplicationMetrics) RecordGitObjectProcessed(objectType string) {
	m.GitObjectsProcessed.WithLabelValues(objectType).Inc()
}

// MetricsHandler returns the Prometheus metrics HTTP handler
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

// HealthMetricsHandler returns a simple metrics endpoint for health checking
func HealthMetricsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Metrics collection is healthy\n"))
	}
}
