package cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// MultiTierCache implements intelligent 2-tier caching (L1 Memory + L2 Redis)
type MultiTierCache[T any] struct {
	L1      *MemoryCache[T] // High-speed memory cache (microseconds)
	L2      *RedisCache[T]  // Persistent Redis cache (milliseconds)
	logger  *zap.Logger
	metrics *MultiTierMetrics
	config  *MultiTierConfig

	// Cache warming and coordination
	warmingMu   sync.RWMutex
	warmingKeys map[string]bool // Track keys being warmed to prevent duplicate work
}

// MultiTierConfig contains configuration for multi-tier cache
type MultiTierConfig struct {
	// L1 Memory cache settings
	L1MaxSize         int           `json:"l1_max_size" yaml:"l1_max_size"`
	L1DefaultTTL      time.Duration `json:"l1_default_ttl" yaml:"l1_default_ttl"`
	L1CleanupInterval time.Duration `json:"l1_cleanup_interval" yaml:"l1_cleanup_interval"`

	// L2 Redis cache settings
	L2Enabled    bool          `json:"l2_enabled" yaml:"l2_enabled"` // Whether to initialize L2 Redis cache
	L2DefaultTTL time.Duration `json:"l2_default_ttl" yaml:"l2_default_ttl"`
	L2Address    string        `json:"l2_address" yaml:"l2_address"`
	L2Password   string        `json:"l2_password,omitempty" yaml:"l2_password,omitempty"`
	L2DB         int           `json:"l2_db" yaml:"l2_db"`
	L2PoolSize   int           `json:"l2_pool_size" yaml:"l2_pool_size"`

	// Coordination settings
	EnableCacheWarming    bool `json:"enable_cache_warming" yaml:"enable_cache_warming"`
	WarmL1FromL2          bool `json:"warm_l1_from_l2" yaml:"warm_l1_from_l2"`
	EnablePerformanceMode bool `json:"enable_performance_mode" yaml:"enable_performance_mode"`

	// Performance tuning
	MaxConcurrentWarming int           `json:"max_concurrent_warming" yaml:"max_concurrent_warming"`
	WarmingTimeout       time.Duration `json:"warming_timeout" yaml:"warming_timeout"`
}

// MultiTierMetrics tracks performance across both cache tiers
type MultiTierMetrics struct {
	mu sync.RWMutex

	// Request tracking
	TotalRequests int64 `json:"total_requests"`

	// L1 (Memory) metrics
	L1Hits   int64 `json:"l1_hits"`
	L1Misses int64 `json:"l1_misses"`

	// L2 (Redis) metrics
	L2Hits   int64 `json:"l2_hits"`
	L2Misses int64 `json:"l2_misses"`

	// Cache warming metrics
	WarmingEvents    int64 `json:"warming_events"`
	SuccessfulWarmup int64 `json:"successful_warmup"`
	FailedWarmup     int64 `json:"failed_warmup"`

	// Performance tracking
	AverageL1ResponseTime time.Duration `json:"average_l1_response_time"`
	AverageL2ResponseTime time.Duration `json:"average_l2_response_time"`
}

// NewMultiTierCache creates a new intelligent multi-tier cache system
func NewMultiTierCache[T any](config *MultiTierConfig, logger *zap.Logger) (*MultiTierCache[T], error) {
	if config == nil {
		config = &MultiTierConfig{
			L1MaxSize:             1000,
			L1DefaultTTL:          10 * time.Minute,
			L1CleanupInterval:     1 * time.Minute,
			L2DefaultTTL:          30 * time.Minute,
			L2Address:             "localhost:6379",
			L2DB:                  0,
			L2PoolSize:            10,
			EnableCacheWarming:    true,
			WarmL1FromL2:          true,
			EnablePerformanceMode: true,
			MaxConcurrentWarming:  5,
			WarmingTimeout:        5 * time.Second,
		}
	}

	// Apply defaults
	if config.L1MaxSize <= 0 {
		config.L1MaxSize = 1000
	}
	if config.L1DefaultTTL <= 0 {
		config.L1DefaultTTL = 10 * time.Minute
	}
	if config.L1CleanupInterval <= 0 {
		config.L1CleanupInterval = 1 * time.Minute
	}
	if config.L2DefaultTTL <= 0 {
		config.L2DefaultTTL = 30 * time.Minute
	}
	if config.L2Address == "" {
		config.L2Address = "localhost:6379"
	}
	if config.L2PoolSize <= 0 {
		config.L2PoolSize = 10
	}
	if config.MaxConcurrentWarming <= 0 {
		config.MaxConcurrentWarming = 5
	}
	if config.WarmingTimeout <= 0 {
		config.WarmingTimeout = 5 * time.Second
	}

	// Initialize L1 memory cache
	l1Config := &MemoryCacheConfig{
		MaxSize:         config.L1MaxSize,
		DefaultTTL:      config.L1DefaultTTL,
		CleanupInterval: config.L1CleanupInterval,
		EnableMetrics:   true,
	}
	l1Cache := NewMemoryCache[T](l1Config, logger)

	// Initialize L2 Redis cache (only if enabled)
	var l2Cache *RedisCache[T]
	if config.L2Enabled {
		l2Config := &RedisCacheConfig{
			Address:         config.L2Address,
			Password:        config.L2Password,
			DB:              config.L2DB,
			PoolSize:        config.L2PoolSize,
			Prefix:          "collabops:multi:",
			DialTimeout:     5 * time.Second,
			ReadTimeout:     3 * time.Second,
			WriteTimeout:    3 * time.Second,
			PoolTimeout:     4 * time.Second,
			MaxRetries:      3,
			MinRetryBackoff: 8 * time.Millisecond,
			MaxRetryBackoff: 512 * time.Millisecond,
		}
		var err error
		l2Cache, err = NewRedisCache[T](l2Config, logger)
		if err != nil {
			l1Cache.Close()
			return nil, fmt.Errorf("failed to initialize L2 Redis cache: %w", err)
		}
	}

	logger.Info("Multi-tier cache system initialized",
		zap.Int("l1_max_size", config.L1MaxSize),
		zap.Duration("l1_ttl", config.L1DefaultTTL),
		zap.Bool("l2_enabled", config.L2Enabled),
		zap.Bool("cache_warming", config.EnableCacheWarming))

	return &MultiTierCache[T]{
		L1:          l1Cache,
		L2:          l2Cache,
		logger:      logger,
		metrics:     &MultiTierMetrics{},
		config:      config,
		warmingKeys: make(map[string]bool),
	}, nil
}

// Get attempts to retrieve data from L1 (memory), then L2 (Redis) if not found
func (m *MultiTierCache[T]) Get(ctx context.Context, key string) (T, bool) {
	var zero T
	m.recordRequest()

	// Try L1 first (microsecond performance)
	startL1 := time.Now()
	if value, found := m.L1.Get(key); found {
		m.recordL1Hit()
		m.updateL1ResponseTime(time.Since(startL1))
		return value, true
	}
	m.recordL1Miss()

	// Try L2 (millisecond performance) - only if Redis is enabled
	if m.L2 != nil {
		startL2 := time.Now()
		value, found, err := m.L2.Get(ctx, key)
		m.updateL2ResponseTime(time.Since(startL2))

		if err != nil {
			m.logger.Error("L2 cache error", zap.String("key", key), zap.Error(err))
			m.recordL2Miss()
			return zero, false
		}

		if found {
			m.recordL2Hit()

			// Warm L1 cache if enabled
			if m.config.WarmL1FromL2 {
				go m.warmL1Cache(key, value)
			}

			return value, true
		}

		m.recordL2Miss()
	}

	return zero, false
}

// Set stores data in both L1 and L2 caches for maximum performance
func (m *MultiTierCache[T]) Set(ctx context.Context, key string, value T) error {
	// Set in L1 first (immediate availability)
	m.L1.Set(key, value, m.config.L1DefaultTTL)

	// Set in L2 for persistence (only if Redis is enabled)
	if m.L2 != nil {
		if err := m.L2.Set(ctx, key, value, m.config.L2DefaultTTL); err != nil {
			m.logger.Error("Failed to set L2 cache", zap.String("key", key), zap.Error(err))
			return err
		}
	}

	return nil
}

// InvalidateByPattern removes all keys matching a pattern from both cache layers
func (m *MultiTierCache[T]) InvalidateByPattern(ctx context.Context, pattern string) error {
	// Invalidate L2 first (more comprehensive pattern matching)
	if m.L2 != nil {
		if err := m.L2.DeleteByPattern(ctx, pattern); err != nil {
			m.logger.Error("Failed to invalidate L2 pattern", zap.String("pattern", pattern), zap.Error(err))
		}
	}

	// Note: L1 memory cache doesn't support pattern matching efficiently
	// For now, we clear entire L1 cache when pattern invalidation is requested
	// This is acceptable as L1 is designed to be fast-rebuilding
	m.L1.Clear()

	m.logger.Info("Cache invalidated by pattern",
		zap.String("pattern", pattern))

	return nil
}

// InvalidateKey removes a specific key from both cache layers
func (m *MultiTierCache[T]) InvalidateKey(ctx context.Context, key string) error {
	// Remove from L1
	m.L1.Delete(key)

	// Remove from L2 (only if Redis is enabled)
	if m.L2 != nil {
		if err := m.L2.Delete(ctx, key); err != nil {
			m.logger.Error("Failed to invalidate L2 key", zap.String("key", key), zap.Error(err))
			return err
		}
	}

	return nil
}

// warmL1Cache asynchronously warms L1 cache from L2 data
func (m *MultiTierCache[T]) warmL1Cache(key string, data T) {
	if !m.config.EnableCacheWarming {
		return
	}

	m.warmingMu.Lock()
	if m.warmingKeys[key] {
		m.warmingMu.Unlock()
		return // Already warming this key
	}
	m.warmingKeys[key] = true
	m.warmingMu.Unlock()

	defer func() {
		m.warmingMu.Lock()
		delete(m.warmingKeys, key)
		m.warmingMu.Unlock()
	}()

	// Set in L1 with appropriate TTL
	m.L1.Set(key, data, m.config.L1DefaultTTL)

	m.recordWarming()
	m.logger.Debug("L1 cache warmed from L2", zap.String("key", key))
}

// GetMetrics returns comprehensive multi-tier cache metrics
func (m *MultiTierCache[T]) GetMetrics() MultiTierMetrics {
	m.metrics.mu.RLock()
	defer m.metrics.mu.RUnlock()

	return MultiTierMetrics{
		TotalRequests:         m.metrics.TotalRequests,
		L1Hits:                m.metrics.L1Hits,
		L1Misses:              m.metrics.L1Misses,
		L2Hits:                m.metrics.L2Hits,
		L2Misses:              m.metrics.L2Misses,
		WarmingEvents:         m.metrics.WarmingEvents,
		SuccessfulWarmup:      m.metrics.SuccessfulWarmup,
		FailedWarmup:          m.metrics.FailedWarmup,
		AverageL1ResponseTime: m.metrics.AverageL1ResponseTime,
		AverageL2ResponseTime: m.metrics.AverageL2ResponseTime,
	}
}

// GetOverallHitRatio returns the combined hit ratio across both cache tiers
func (m *MultiTierCache[T]) GetOverallHitRatio() float64 {
	m.metrics.mu.RLock()
	defer m.metrics.mu.RUnlock()

	totalHits := m.metrics.L1Hits + m.metrics.L2Hits
	totalRequests := m.metrics.TotalRequests

	if totalRequests == 0 {
		return 0.0
	}

	return float64(totalHits) / float64(totalRequests)
}

// Close gracefully shuts down both cache layers
func (m *MultiTierCache[T]) Close() error {
	var errors []error

	if err := m.L1.Close(); err != nil {
		errors = append(errors, fmt.Errorf("L1 cache close error: %w", err))
	}

	if m.L2 != nil {
		if err := m.L2.Close(); err != nil {
			errors = append(errors, fmt.Errorf("L2 cache close error: %w", err))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("cache close errors: %v", errors)
	}

	m.logger.Info("Multi-tier cache system shut down successfully")
	return nil
}

// HealthCheck provides comprehensive health status for both cache layers
func (m *MultiTierCache[T]) HealthCheck(ctx context.Context) map[string]interface{} {
	health := map[string]interface{}{
		"overall_status": "healthy",
		"l1_cache":       m.L1.GetCacheInfo(),
		"l2_cache":       make(map[string]interface{}),
		"metrics":        m.GetMetrics(),
		"hit_ratio":      m.GetOverallHitRatio(),
	}

	// Test L2 connection (only if Redis is enabled)
	if m.L2 != nil {
		if err := m.L2.Ping(ctx); err != nil {
			health["overall_status"] = "degraded"
			health["l2_cache"] = map[string]interface{}{
				"status": "unhealthy",
				"error":  err.Error(),
			}
		} else {
			if info, err := m.L2.GetInfo(ctx); err == nil {
				health["l2_cache"] = info
			}
		}
	} else {
		health["l2_cache"] = map[string]interface{}{
			"status": "disabled",
		}
	}

	return health
}

// Performance tracking methods

func (m *MultiTierCache[T]) recordRequest() {
	m.metrics.mu.Lock()
	m.metrics.TotalRequests++
	m.metrics.mu.Unlock()
}

func (m *MultiTierCache[T]) recordL1Hit() {
	m.metrics.mu.Lock()
	m.metrics.L1Hits++
	m.metrics.mu.Unlock()
}

func (m *MultiTierCache[T]) recordL1Miss() {
	m.metrics.mu.Lock()
	m.metrics.L1Misses++
	m.metrics.mu.Unlock()
}

func (m *MultiTierCache[T]) recordL2Hit() {
	m.metrics.mu.Lock()
	m.metrics.L2Hits++
	m.metrics.mu.Unlock()
}

func (m *MultiTierCache[T]) recordL2Miss() {
	m.metrics.mu.Lock()
	m.metrics.L2Misses++
	m.metrics.mu.Unlock()
}

func (m *MultiTierCache[T]) recordWarming() {
	m.metrics.mu.Lock()
	m.metrics.WarmingEvents++
	m.metrics.SuccessfulWarmup++
	m.metrics.mu.Unlock()
}

func (m *MultiTierCache[T]) updateL1ResponseTime(duration time.Duration) {
	m.metrics.mu.Lock()
	if m.metrics.L1Hits == 1 {
		m.metrics.AverageL1ResponseTime = duration
	} else {
		// Simple moving average
		m.metrics.AverageL1ResponseTime = time.Duration(
			(int64(m.metrics.AverageL1ResponseTime)*9 + int64(duration)) / 10,
		)
	}
	m.metrics.mu.Unlock()
}

func (m *MultiTierCache[T]) updateL2ResponseTime(duration time.Duration) {
	m.metrics.mu.Lock()
	if m.metrics.L2Hits == 1 {
		m.metrics.AverageL2ResponseTime = duration
	} else {
		// Simple moving average
		m.metrics.AverageL2ResponseTime = time.Duration(
			(int64(m.metrics.AverageL2ResponseTime)*9 + int64(duration)) / 10,
		)
	}
	m.metrics.mu.Unlock()
}

// GetCacheInfo returns detailed information about the multi-tier cache
func (m *MultiTierCache[T]) GetCacheInfo() map[string]interface{} {
	info := map[string]interface{}{
		"type":                "multi_tier_cache",
		"l1_info":             m.L1.GetCacheInfo(),
		"l2_enabled":          m.L2 != nil,
		"overall_hit_ratio":   m.GetOverallHitRatio(),
		"cache_warming":       m.config.EnableCacheWarming,
		"performance_mode":    m.config.EnablePerformanceMode,
		"warming_in_progress": len(m.warmingKeys),
		"metrics":             m.GetMetrics(),
	}
	if m.L2 != nil {
		info["l2_hit_ratio"] = m.L2.GetHitRatio()
	}
	return info
}
