package cache

import (
	"context"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// 중복 등록을 방지하기 위한 기본 메트릭용 전역 싱글턴
var (
	defaultPrometheusMetrics *PrometheusMetrics
	defaultMetricsOnce       sync.Once
)

// PrometheusMetrics는 캐시에 대한 모든 OTel 메트릭을 저장합니다
type PrometheusMetrics struct {
	// 캐시 작업 추적을 위한 카운터 메트릭
	cacheHitsTotal      otelmetric.Int64Counter
	cacheMissesTotal    otelmetric.Int64Counter
	cacheEvictionsTotal otelmetric.Int64Counter

	// 현재 상태를 위한 게이지 메트릭
	cacheEntriesCurrent otelmetric.Float64Gauge
	cacheMemoryBytes    otelmetric.Float64Gauge

	// 추가 세부 메트릭
	cacheOperationsTotal otelmetric.Int64Counter
	cacheAccessDuration  otelmetric.Float64Histogram

	// Atomic counters for hit rate calculation
	hitCount  atomic.Int64
	missCount atomic.Int64
}

func ctx() context.Context { return context.Background() }

// must panics on error — only used during deterministic init.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// NewPrometheusMetrics는 캐시 모니터링을 위한 모든 OTel 메트릭을 생성합니다
func NewPrometheusMetrics() *PrometheusMetrics {
	defaultMetricsOnce.Do(func() {
		m := otel.Meter("git-executor-cache")

		defaultPrometheusMetrics = &PrometheusMetrics{
			cacheHitsTotal: must(m.Int64Counter("cache_hits",
				otelmetric.WithDescription("Total number of cache hits"),
			)),
			cacheMissesTotal: must(m.Int64Counter("cache_misses",
				otelmetric.WithDescription("Total number of cache misses"),
			)),
			cacheEvictionsTotal: must(m.Int64Counter("cache_evictions",
				otelmetric.WithDescription("Total number of cache evictions"),
			)),
			cacheEntriesCurrent: must(m.Float64Gauge("cache_entries_current",
				otelmetric.WithDescription("Current number of entries in the cache"),
			)),
			cacheMemoryBytes: must(m.Float64Gauge("cache_memory",
				otelmetric.WithDescription("Estimated memory usage of the cache in bytes"),
				otelmetric.WithUnit("By"),
			)),
			cacheOperationsTotal: must(m.Int64Counter("cache_operations",
				otelmetric.WithDescription("Total number of cache operations by type"),
			)),
			cacheAccessDuration: must(m.Float64Histogram("cache_access_duration",
				otelmetric.WithDescription("Duration of cache access operations"),
				otelmetric.WithUnit("s"),
			)),
		}
	})
	return defaultPrometheusMetrics
}

// RecordHit은 캐시 히트를 기록합니다
func (pm *PrometheusMetrics) RecordHit() {
	pm.hitCount.Add(1)
	pm.cacheHitsTotal.Add(ctx(), 1)
	pm.cacheOperationsTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("operation", "get"),
	))
}

// RecordMiss는 캐시 미스를 기록합니다
func (pm *PrometheusMetrics) RecordMiss() {
	pm.missCount.Add(1)
	pm.cacheMissesTotal.Add(ctx(), 1)
	pm.cacheOperationsTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("operation", "get"),
	))
}

// RecordEviction은 캐시 제거를 기록합니다
func (pm *PrometheusMetrics) RecordEviction() {
	pm.cacheEvictionsTotal.Add(ctx(), 1)
	pm.cacheOperationsTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("operation", "evict"),
	))
}

// RecordSet은 캐시 설정 작업을 기록합니다
func (pm *PrometheusMetrics) RecordSet() {
	pm.cacheOperationsTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("operation", "set"),
	))
}

// RecordCleanup은 정리 작업을 기록합니다
func (pm *PrometheusMetrics) RecordCleanup() {
	pm.cacheOperationsTotal.Add(ctx(), 1, otelmetric.WithAttributes(
		attribute.String("operation", "cleanup"),
	))
}

// UpdateEntries는 현재 캐시 항목 수를 업데이트합니다
func (pm *PrometheusMetrics) UpdateEntries(count int64) {
	pm.cacheEntriesCurrent.Record(ctx(), float64(count))
}

// UpdateMemoryUsage는 추정 메모리 사용량을 업데이트합니다
func (pm *PrometheusMetrics) UpdateMemoryUsage(bytes int64) {
	pm.cacheMemoryBytes.Record(ctx(), float64(bytes))
}

// RecordAccessDuration은 캐시 접근 작업의 지속 시간을 기록합니다
func (pm *PrometheusMetrics) RecordAccessDuration(operation, result string, duration float64) {
	pm.cacheAccessDuration.Record(ctx(), duration, otelmetric.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("result", result),
	))
}

// GetHitRate는 현재 캐시 히트율을 계산하고 반환합니다
func (pm *PrometheusMetrics) GetHitRate() float64 {
	hits := pm.hitCount.Load()
	misses := pm.missCount.Load()
	total := hits + misses

	if total == 0 {
		return 0.0
	}

	return float64(hits) / float64(total)
}
