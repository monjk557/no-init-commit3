package cache

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	dto "github.com/prometheus/client_model/go"
)

// Global singleton for default prometheus metrics to avoid duplicate registrations
var (
	defaultPrometheusMetrics *PrometheusMetrics
	defaultMetricsOnce       sync.Once
)

// PrometheusMetrics holds all Prometheus metrics for the cache
type PrometheusMetrics struct {
	// Counter metrics for tracking cache operations
	CacheHitsTotal      prometheus.Counter
	CacheMissesTotal    prometheus.Counter
	CacheEvictionsTotal prometheus.Counter

	// Gauge metrics for current state
	CacheEntriesCurrent prometheus.Gauge
	CacheMemoryBytes    prometheus.Gauge

	// Additional detailed metrics
	CacheOperationsTotal prometheus.CounterVec   // labeled by operation type
	CacheAccessDuration  prometheus.HistogramVec // cache access latency
}

// NewPrometheusMetrics creates and registers all Prometheus metrics for cache monitoring
// This uses a singleton pattern to avoid duplicate registrations with the default registry
func NewPrometheusMetrics() *PrometheusMetrics {
	defaultMetricsOnce.Do(func() {
		defaultPrometheusMetrics = &PrometheusMetrics{
			// Basic cache operation counters
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

			// Current state gauges
			CacheEntriesCurrent: promauto.NewGauge(prometheus.GaugeOpts{
				Name: "cache_entries_current",
				Help: "Current number of entries in the cache",
			}),

			CacheMemoryBytes: promauto.NewGauge(prometheus.GaugeOpts{
				Name: "cache_memory_bytes",
				Help: "Estimated memory usage of the cache in bytes",
			}),

			// Detailed operation metrics
			CacheOperationsTotal: *promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "cache_operations_total",
					Help: "Total number of cache operations by type",
				},
				[]string{"operation"}, // labels: get, set, evict, cleanup
			),

			// Performance metrics
			CacheAccessDuration: *promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "cache_access_duration_seconds",
					Help:    "Duration of cache access operations",
					Buckets: prometheus.DefBuckets,
				},
				[]string{"operation", "result"}, // operation: get/set, result: hit/miss/success
			),
		}
	})
	return defaultPrometheusMetrics
}

// NewPrometheusMetricsWithRegistry creates metrics with a custom registry (useful for testing)
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

// RegisterMetrics registers all metrics with the provided registry
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

// RecordHit records a cache hit
func (m *PrometheusMetrics) RecordHit() {
	m.CacheHitsTotal.Inc()
	m.CacheOperationsTotal.WithLabelValues("get").Inc()
}

// RecordMiss records a cache miss
func (m *PrometheusMetrics) RecordMiss() {
	m.CacheMissesTotal.Inc()
	m.CacheOperationsTotal.WithLabelValues("get").Inc()
}

// RecordEviction records a cache eviction
func (m *PrometheusMetrics) RecordEviction() {
	m.CacheEvictionsTotal.Inc()
	m.CacheOperationsTotal.WithLabelValues("evict").Inc()
}

// RecordSet records a cache set operation
func (m *PrometheusMetrics) RecordSet() {
	m.CacheOperationsTotal.WithLabelValues("set").Inc()
}

// RecordCleanup records a cleanup operation
func (m *PrometheusMetrics) RecordCleanup() {
	m.CacheOperationsTotal.WithLabelValues("cleanup").Inc()
}

// UpdateEntries updates the current number of cache entries
func (m *PrometheusMetrics) UpdateEntries(count int64) {
	m.CacheEntriesCurrent.Set(float64(count))
}

// UpdateMemoryUsage updates the estimated memory usage
func (m *PrometheusMetrics) UpdateMemoryUsage(bytes int64) {
	m.CacheMemoryBytes.Set(float64(bytes))
}

// RecordAccessDuration records the duration of a cache access operation
func (m *PrometheusMetrics) RecordAccessDuration(operation, result string, duration float64) {
	m.CacheAccessDuration.WithLabelValues(operation, result).Observe(duration)
}

// GetHitRate calculates and returns the current cache hit rate
func (m *PrometheusMetrics) GetHitRate() float64 {
	hits := getCounterValue(m.CacheHitsTotal)
	misses := getCounterValue(m.CacheMissesTotal)
	total := hits + misses

	if total == 0 {
		return 0.0
	}

	return hits / total
}

// Helper function to get counter value (for testing and monitoring)
func getCounterValue(counter prometheus.Counter) float64 {
	pb := &dto.Metric{}
	if err := counter.Write(pb); err != nil {
		return 0
	}
	return pb.GetCounter().GetValue()
}
