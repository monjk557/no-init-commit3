// Package observability는 Git executor 서비스를 위한 포괄적인 메트릭, 로깅, 모니터링 기능을 제공합니다.
// OTel metrics API + Prometheus exporter 기반 종합 모니터링 시스템입니다.
package observability

import (
	"context"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

// Global singleton for application metrics to avoid duplicate registrations
var (
	globalMetrics     *ApplicationMetrics
	globalMetricsOnce sync.Once
	globalMeterProv   *sdkmetric.MeterProvider
)

// ApplicationMetrics는 애플리케이션 전반에 걸친 OTel 메트릭들을 보유합니다.
type ApplicationMetrics struct {
	// Git Operation Metrics
	gitOperationsTotal      otelmetric.Int64Counter
	gitOperationDuration    otelmetric.Float64Histogram
	gitRepositoriesAccessed otelmetric.Int64Counter
	gitObjectsProcessed     otelmetric.Int64Counter

	// Error Metrics
	errorsTotal           otelmetric.Int64Counter
	permissionDeniedTotal otelmetric.Int64Counter
	validationErrorsTotal otelmetric.Int64Counter

	// Resource Usage Metrics
	memoryUsageBytes     otelmetric.Float64Gauge
	goroutinesCount      otelmetric.Float64Gauge
	fileDescriptorsCount otelmetric.Float64Gauge
	cpuUsagePercent      otelmetric.Float64Gauge

	// Performance Metrics
	concurrentConnections otelmetric.Float64Gauge
	requestQueueLength    otelmetric.Float64Gauge
	responseLatencyP95    otelmetric.Float64Gauge
	responseLatencyP99    otelmetric.Float64Gauge

	// Business Logic Metrics
	repositoryCacheHitRate  otelmetric.Float64Gauge
	permissionCheckDuration otelmetric.Float64Histogram
	apiServiceCallsTotal    otelmetric.Int64Counter

	// Overview / Performance (repo browser)
	overviewCacheOpsTotal                otelmetric.Int64Counter
	overviewCacheEntries                 otelmetric.Float64Gauge
	overviewCacheSingleflightSharedTotal otelmetric.Int64Counter
	latestCommitsBatchRequestsTotal      otelmetric.Int64Counter
	latestCommitsBatchDuration           otelmetric.Float64Histogram
	latestCommitsBatchPaths              otelmetric.Float64Histogram
	latestCommitsBatchScannedCommits     otelmetric.Float64Histogram
	latestCommitsBatchCutReasonTotal     otelmetric.Int64Counter

	// Tree-with-commit endpoint
	treeWithCommitRequestsTotal otelmetric.Int64Counter
	treeWithCommitDuration      otelmetric.Float64Histogram
	treeWithCommitEntries       otelmetric.Float64Histogram

	// Commit index
	commitIndexOpsTotal otelmetric.Int64Counter
	commitIndexEntries  otelmetric.Float64Gauge
}

// MetricsConfig는 메트릭 수집을 위한 설정을 보유합니다.
type MetricsConfig struct {
	Enabled         bool          `mapstructure:"enabled"`
	Namespace       string        `mapstructure:"namespace"`
	CollectInterval time.Duration `mapstructure:"collect_interval"`
}

// DefaultMetricsConfig returns default metrics configuration
func DefaultMetricsConfig() MetricsConfig {
	return MetricsConfig{
		Enabled:         true,
		Namespace:       "git_executor",
		CollectInterval: 15 * time.Second,
	}
}

// initMeterProvider sets up the OTel MeterProvider with Prometheus exporter.
func initMeterProvider() {
	exporter, err := promexporter.New(
		promexporter.WithoutScopeInfo(),
		promexporter.WithoutTargetInfo(),
	)
	if err != nil {
		panic("failed to create prometheus exporter: " + err.Error())
	}

	globalMeterProv = sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	otel.SetMeterProvider(globalMeterProv)
}

// must panics on error — only used during deterministic init.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// NewApplicationMetrics는 모든 애플리케이션 메트릭을 생성하고 등록합니다.
func NewApplicationMetrics() *ApplicationMetrics {
	globalMetricsOnce.Do(func() {
		initMeterProvider()
		m := otel.Meter("git-executor")

		globalMetrics = &ApplicationMetrics{
			// Git Operation Metrics
			gitOperationsTotal: must(m.Int64Counter("git_executor_git_operations",
				otelmetric.WithDescription("Total number of Git operations"),
			)),
			gitOperationDuration: must(m.Float64Histogram("git_executor_git_operation_duration",
				otelmetric.WithDescription("Git operation duration in seconds"),
				otelmetric.WithUnit("s"),
				otelmetric.WithExplicitBucketBoundaries(0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60),
			)),
			gitRepositoriesAccessed: must(m.Int64Counter("git_executor_git_repositories_accessed",
				otelmetric.WithDescription("Total number of repositories accessed"),
			)),
			gitObjectsProcessed: must(m.Int64Counter("git_executor_git_objects_processed",
				otelmetric.WithDescription("Total number of Git objects processed"),
			)),

			// Error Metrics
			errorsTotal: must(m.Int64Counter("git_executor_errors",
				otelmetric.WithDescription("Total number of errors"),
			)),
			permissionDeniedTotal: must(m.Int64Counter("git_executor_permission_denied",
				otelmetric.WithDescription("Total number of permission denied errors"),
			)),
			validationErrorsTotal: must(m.Int64Counter("git_executor_validation_errors",
				otelmetric.WithDescription("Total number of validation errors"),
			)),

			// Resource Usage Metrics
			memoryUsageBytes: must(m.Float64Gauge("git_executor_memory_usage",
				otelmetric.WithDescription("Current memory usage in bytes"),
				otelmetric.WithUnit("By"),
			)),
			goroutinesCount: must(m.Float64Gauge("git_executor_goroutines_count",
				otelmetric.WithDescription("Current number of goroutines"),
			)),
			fileDescriptorsCount: must(m.Float64Gauge("git_executor_file_descriptors_count",
				otelmetric.WithDescription("Current number of open file descriptors"),
			)),
			cpuUsagePercent: must(m.Float64Gauge("git_executor_cpu_usage_percent",
				otelmetric.WithDescription("Current CPU usage percentage"),
			)),

			// Performance Metrics
			concurrentConnections: must(m.Float64Gauge("git_executor_concurrent_connections",
				otelmetric.WithDescription("Current number of concurrent connections"),
			)),
			requestQueueLength: must(m.Float64Gauge("git_executor_request_queue_length",
				otelmetric.WithDescription("Current request queue length"),
			)),
			responseLatencyP95: must(m.Float64Gauge("git_executor_response_latency_p95",
				otelmetric.WithDescription("95th percentile response latency in seconds"),
				otelmetric.WithUnit("s"),
			)),
			responseLatencyP99: must(m.Float64Gauge("git_executor_response_latency_p99",
				otelmetric.WithDescription("99th percentile response latency in seconds"),
				otelmetric.WithUnit("s"),
			)),

			// Business Logic Metrics
			repositoryCacheHitRate: must(m.Float64Gauge("git_executor_repository_cache_hit_rate",
				otelmetric.WithDescription("Repository cache hit rate as a percentage"),
			)),
			permissionCheckDuration: must(m.Float64Histogram("git_executor_permission_check_duration",
				otelmetric.WithDescription("Permission check duration in seconds"),
				otelmetric.WithUnit("s"),
				otelmetric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1),
			)),
			apiServiceCallsTotal: must(m.Int64Counter("git_executor_api_service_calls",
				otelmetric.WithDescription("Total number of API service calls"),
			)),

			// Overview cache
			overviewCacheOpsTotal: must(m.Int64Counter("git_executor_overview_cache_ops",
				otelmetric.WithDescription("Overview in-memory cache operations"),
			)),
			overviewCacheEntries: must(m.Float64Gauge("git_executor_overview_cache_entries",
				otelmetric.WithDescription("Current overview in-memory cache entry count"),
			)),
			overviewCacheSingleflightSharedTotal: must(m.Int64Counter("git_executor_overview_cache_singleflight_shared",
				otelmetric.WithDescription("Overview cache singleflight shared result count"),
			)),
			latestCommitsBatchRequestsTotal: must(m.Int64Counter("git_executor_latest_commits_batch_requests",
				otelmetric.WithDescription("latest-commits batch requests by outcome"),
			)),
			latestCommitsBatchDuration: must(m.Float64Histogram("git_executor_latest_commits_batch_duration",
				otelmetric.WithDescription("latest-commits batch handler duration in seconds"),
				otelmetric.WithUnit("s"),
				otelmetric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 1.5, 2.5, 5, 10),
			)),
			latestCommitsBatchPaths: must(m.Float64Histogram("git_executor_latest_commits_batch_paths",
				otelmetric.WithDescription("latest-commits batch requested path count"),
				otelmetric.WithExplicitBucketBoundaries(1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000),
			)),
			latestCommitsBatchScannedCommits: must(m.Float64Histogram("git_executor_latest_commits_batch_scanned_commits",
				otelmetric.WithDescription("latest-commits batch scanned commits"),
				otelmetric.WithExplicitBucketBoundaries(1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 20000, 50000, 100000),
			)),
			latestCommitsBatchCutReasonTotal: must(m.Int64Counter("git_executor_latest_commits_batch_cut_reason",
				otelmetric.WithDescription("latest-commits batch cut reason count"),
			)),

			// Tree-with-commit endpoint
			treeWithCommitRequestsTotal: must(m.Int64Counter("git_executor_tree_with_commit_requests",
				otelmetric.WithDescription("tree-with-commit requests by outcome"),
			)),
			treeWithCommitDuration: must(m.Float64Histogram("git_executor_tree_with_commit_duration",
				otelmetric.WithDescription("tree-with-commit handler duration in seconds"),
				otelmetric.WithUnit("s"),
				otelmetric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 1, 1.5, 2.5, 5, 10),
			)),
			treeWithCommitEntries: must(m.Float64Histogram("git_executor_tree_with_commit_entries",
				otelmetric.WithDescription("tree-with-commit entry count (N)"),
				otelmetric.WithExplicitBucketBoundaries(1, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000),
			)),

			// Commit index
			commitIndexOpsTotal: must(m.Int64Counter("git_executor_commit_index_ops",
				otelmetric.WithDescription("commit index operations"),
			)),
			commitIndexEntries: must(m.Float64Gauge("git_executor_commit_index_entries",
				otelmetric.WithDescription("current commit index entry count"),
			)),
		}
	})
	return globalMetrics
}

// GetApplicationMetrics returns the global metrics instance
func GetApplicationMetrics() *ApplicationMetrics {
	if globalMetrics == nil {
		return NewApplicationMetrics()
	}
	return globalMetrics
}

// ctx returns a background context for recording metrics.
func ctx() context.Context { return context.Background() }

// StartResourceCollector starts a background goroutine to collect resource usage metrics
func (am *ApplicationMetrics) StartResourceCollector(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			am.collectResourceMetrics()
		}
	}()
}

// collectResourceMetrics collects and updates resource usage metrics
func (am *ApplicationMetrics) collectResourceMetrics() {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	am.memoryUsageBytes.Record(ctx(), float64(memStats.Alloc))
	am.goroutinesCount.Record(ctx(), float64(runtime.NumGoroutine()))
}

func (am *ApplicationMetrics) RecordOverviewCacheOp(kind, op string) {
	if am == nil {
		return
	}
	am.overviewCacheOpsTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("kind", kind),
		attribute.String("op", op),
	))
}

func (am *ApplicationMetrics) SetOverviewCacheEntries(kind string, n int) {
	if am == nil {
		return
	}
	am.overviewCacheEntries.Record(ctx(), float64(n), otelmetric.WithAttributes(
		attribute.String("kind", kind),
	))
}

func (am *ApplicationMetrics) RecordOverviewSingleflightShared(kind string) {
	if am == nil {
		return
	}
	am.overviewCacheSingleflightSharedTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("kind", kind),
	))
}

func (am *ApplicationMetrics) RecordLatestCommitsBatch(outcome string, duration time.Duration, paths int, scannedCommits int) {
	if am == nil {
		return
	}
	attrs := otelmetric.WithAttributes(attribute.String("outcome", outcome))
	am.latestCommitsBatchRequestsTotal.Add(ctx(), 1, attrs)
	am.latestCommitsBatchDuration.Record(ctx(), duration.Seconds(), attrs)
	am.latestCommitsBatchPaths.Record(ctx(), float64(paths), attrs)
	am.latestCommitsBatchScannedCommits.Record(ctx(), float64(scannedCommits), attrs)
}

func (am *ApplicationMetrics) RecordLatestCommitsBatchCutReason(reason string) {
	if am == nil {
		return
	}
	am.latestCommitsBatchCutReasonTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("reason", reason),
	))
}

// RecordTreeWithCommit records metrics for the tree-with-commit endpoint.
func (am *ApplicationMetrics) RecordTreeWithCommit(outcome string, duration time.Duration, entries int) {
	if am == nil {
		return
	}
	attrs := otelmetric.WithAttributes(attribute.String("outcome", outcome))
	am.treeWithCommitRequestsTotal.Add(ctx(), 1, attrs)
	am.treeWithCommitDuration.Record(ctx(), duration.Seconds(), attrs)
	am.treeWithCommitEntries.Record(ctx(), float64(entries), attrs)
}

// RecordCommitIndexOp records a commit-index operation (hit, miss, populate, evict).
func (am *ApplicationMetrics) RecordCommitIndexOp(op string) {
	if am == nil {
		return
	}
	am.commitIndexOpsTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("op", op),
	))
}

// SetCommitIndexEntries sets the current commit index entry count gauge.
func (am *ApplicationMetrics) SetCommitIndexEntries(n int) {
	if am == nil {
		return
	}
	am.commitIndexEntries.Record(ctx(), float64(n))
}

// RecordGitOperation records metrics for a Git operation
func (am *ApplicationMetrics) RecordGitOperation(operation string, duration time.Duration, success bool) {
	result := "success"
	if !success {
		result = "error"
	}

	am.gitOperationsTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("result", result),
	))
	am.gitOperationDuration.Record(ctx(), duration.Seconds(), otelmetric.WithAttributes(
		attribute.String("operation", operation),
	))
}

// RecordError records an error metric
func (am *ApplicationMetrics) RecordError(errorType, operation string) {
	am.errorsTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("error_type", errorType),
		attribute.String("operation", operation),
	))
}

// RecordPermissionDenied records a permission denied error
func (am *ApplicationMetrics) RecordPermissionDenied(resourceType, operation string) {
	am.permissionDeniedTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("resource_type", resourceType),
		attribute.String("operation", operation),
	))
}

// RecordValidationError records a validation error
func (am *ApplicationMetrics) RecordValidationError(field, errorType string) {
	am.validationErrorsTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("field", field),
		attribute.String("error_type", errorType),
	))
}

// RecordRepositoryAccess records access to a repository
func (am *ApplicationMetrics) RecordRepositoryAccess() {
	am.gitRepositoriesAccessed.Add(ctx(), 1)
}

// RecordGitObjectProcessed records processing of a Git object
func (am *ApplicationMetrics) RecordGitObjectProcessed(objectType string) {
	am.gitObjectsProcessed.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("object_type", objectType),
	))
}

// RecordPermissionCheckDuration records permission check duration
func (am *ApplicationMetrics) RecordPermissionCheckDuration(result string, duration time.Duration) {
	am.permissionCheckDuration.Record(ctx(), duration.Seconds(), otelmetric.WithAttributes(
		attribute.String("result", result),
	))
}

// RecordAPIServiceCall records an API service call
func (am *ApplicationMetrics) RecordAPIServiceCall(endpoint, result string) {
	am.apiServiceCallsTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("endpoint", endpoint),
		attribute.String("result", result),
	))
}

// SetCacheHitRate sets the cache hit rate gauge
func (am *ApplicationMetrics) SetCacheHitRate(rate float64) {
	am.repositoryCacheHitRate.Record(ctx(), rate*100)
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
