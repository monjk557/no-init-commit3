package cache

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	dto "github.com/prometheus/client_model/go"
)

// 중복 등록을 방지하기 위한 기본 Prometheus 메트릭용 전역 싱글턴
var (
	defaultPrometheusMetrics *PrometheusMetrics
	defaultMetricsOnce       sync.Once
)

// PrometheusMetrics는 캐시에 대한 모든 Prometheus 메트릭을 저장합니다
type PrometheusMetrics struct {
	// 캐시 작업 추적을 위한 카운터 메트릭
	CacheHitsTotal      prometheus.Counter
	CacheMissesTotal    prometheus.Counter
	CacheEvictionsTotal prometheus.Counter

	// 현재 상태를 위한 게이지 메트릭
	CacheEntriesCurrent prometheus.Gauge
	CacheMemoryBytes    prometheus.Gauge

	// 추가 세부 메트릭
	CacheOperationsTotal prometheus.CounterVec   // 작업 타입별 레이블
	CacheAccessDuration  prometheus.HistogramVec // 캐시 접근 레이턴시
}

// NewPrometheusMetrics는 캐시 모니터링을 위한 모든 Prometheus 메트릭을 생성하고 등록합니다
// 기본 레지스트리에서 중복 등록을 방지하기 위해 싱글턴 패턴을 사용합니다
func NewPrometheusMetrics() *PrometheusMetrics {
	defaultMetricsOnce.Do(func() {
		defaultPrometheusMetrics = &PrometheusMetrics{
			// 기본 캐시 작업 카운터
			CacheHitsTotal: promauto.NewCounter(prometheus.CounterOpts{
				Name: "cache_hits_total",
				Help: "Total number of cache hits",
			}),

			CacheMissesTotal: promauto.NewCounter(prometheus.CounterOpts{
				Name: "cache_misses_total",
				Help: "Total number of cache misses",
			}),

			CacheEvictionsTotal: promauto.NewCounter(prometheus.CounterOpts{
				Name: "cache_evictions_total",
				Help: "Total number of cache evictions",
			}),

			// 현재 상태 게이지
			CacheEntriesCurrent: promauto.NewGauge(prometheus.GaugeOpts{
				Name: "cache_entries_current",
				Help: "Current number of entries in the cache",
			}),

			CacheMemoryBytes: promauto.NewGauge(prometheus.GaugeOpts{
				Name: "cache_memory_bytes",
				Help: "Estimated memory usage of the cache in bytes",
			}),

			// 세부 작업 메트릭
			CacheOperationsTotal: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "cache_operations_total",
					Help: "Total number of cache operations by type",
				},
				[]string{"operation"}, // 레이블: get, set, evict, cleanup
			),

			// 성능 메트릭
			CacheAccessDuration: *promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "cache_access_duration_seconds",
					Help:    "Duration of cache access operations",
					Buckets: prometheus.DefBuckets,
				},
				[]string{"operation", "result"}, // 작업: get/set, 결과: hit/miss/success
			),
		}
	})
	return defaultPrometheusMetrics
}

// NewPrometheusMetricsWithRegistry는 사용자 정의 레지스트리로 메트릭을 생성합니다 (테스트에 유용)
func NewPrometheusMetricsWithRegistry(registry prometheus.Registerer) *PrometheusMetrics {
	return &PrometheusMetrics{
		CacheHitsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cache_hits_total",
			Help: "Total number of cache hits",
		}),

		CacheMissesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cache_misses_total",
			Help: "Total number of cache misses",
		}),

		CacheEvictionsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cache_evictions_total",
			Help: "Total number of cache evictions",
		}),

		CacheEntriesCurrent: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cache_entries_current",
			Help: "Current number of entries in the cache",
		}),

		CacheMemoryBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cache_memory_bytes",
			Help: "Estimated memory usage of the cache in bytes",
		}),

		CacheOperationsTotal: *prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "cache_operations_total",
				Help: "Total number of cache operations by type",
			},
			[]string{"operation"},
		),

		CacheAccessDuration: *prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "cache_access_duration_seconds",
				Help:    "Duration of cache access operations",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"operation", "result"},
		),
	}
}

// RegisterMetrics는 제공된 레지스트리에 모든 메트릭을 등록합니다
func (m *PrometheusMetrics) RegisterMetrics(registry prometheus.Registerer) error {
	collectors := []prometheus.Collector{
		m.CacheHitsTotal,
		m.CacheMissesTotal,
		m.CacheEvictionsTotal,
		m.CacheEntriesCurrent,
		m.CacheMemoryBytes,
		&m.CacheOperationsTotal,
		&m.CacheAccessDuration,
	}

	for _, collector := range collectors {
		if err := registry.Register(collector); err != nil {
			return err
		}
	}

	return nil
}

// RecordHit은 캐시 히트를 기록합니다
func (m *PrometheusMetrics) RecordHit() {
	m.CacheHitsTotal.Inc()
	m.CacheOperationsTotal.WithLabelValues("get").Inc()
}

// RecordMiss는 캐시 미스를 기록합니다
func (m *PrometheusMetrics) RecordMiss() {
	m.CacheMissesTotal.Inc()
	m.CacheOperationsTotal.WithLabelValues("get").Inc()
}

// RecordEviction은 캐시 제거를 기록합니다
func (m *PrometheusMetrics) RecordEviction() {
	m.CacheEvictionsTotal.Inc()
	m.CacheOperationsTotal.WithLabelValues("evict").Inc()
}

// RecordSet은 캐시 설정 작업을 기록합니다
func (m *PrometheusMetrics) RecordSet() {
	m.CacheOperationsTotal.WithLabelValues("set").Inc()
}

// RecordCleanup은 정리 작업을 기록합니다
func (m *PrometheusMetrics) RecordCleanup() {
	m.CacheOperationsTotal.WithLabelValues("cleanup").Inc()
}

// UpdateEntries는 현재 캐시 항목 수를 업데이트합니다
func (m *PrometheusMetrics) UpdateEntries(count int64) {
	m.CacheEntriesCurrent.Set(float64(count))
}

// UpdateMemoryUsage는 추정 메모리 사용량을 업데이트합니다
func (m *PrometheusMetrics) UpdateMemoryUsage(bytes int64) {
	m.CacheMemoryBytes.Set(float64(bytes))
}

// RecordAccessDuration은 캐시 접근 작업의 지속 시간을 기록합니다
func (m *PrometheusMetrics) RecordAccessDuration(operation, result string, duration float64) {
	m.CacheAccessDuration.WithLabelValues(operation, result).Observe(duration)
}

// GetHitRate는 현재 캐시 히트율을 계산하고 반환합니다
func (m *PrometheusMetrics) GetHitRate() float64 {
	hits := getCounterValue(m.CacheHitsTotal)
	misses := getCounterValue(m.CacheMissesTotal)
	total := hits + misses

	if total == 0 {
		return 0.0
	}

	return hits / total
}

// getCounterValue는 카운터 값을 가져오는 도우미 함수입니다 (테스트 및 모니터링용)
func getCounterValue(counter prometheus.Counter) float64 {
	pb := &dto.Metric{}
	if err := counter.Write(pb); err != nil {
		return 0
	}
	return pb.GetCounter().GetValue()
}
