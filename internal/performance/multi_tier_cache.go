package performance

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"go.uber.org/zap"
)

// MultiTierCache provides intelligent multi-layer caching system
// Research target: Improve cache hit rate from 40% to 90% using intelligent prefetching
type MultiTierCache struct {
	logger *zap.Logger

	// L1: Fast local memory cache (existing LRU-based)
	l1Cache RepositoryCache

	// L2: Redis distributed cache (for multi-instance setups)
	l2Cache DistributedCache

	// L3: Predictive prefetch engine
	prefetcher *PredictivePrefetcher

	// Access pattern tracking for intelligence
	accessTracker *AccessPatternTracker

	// Configuration
	config *MultiTierConfig

	// Statistics
	stats *MultiTierStats
	mutex sync.RWMutex
}

// RepositoryCache interface for L1 cache compatibility
type RepositoryCache interface {
	GetRepository(path string) (*gogit.Repository, bool)
	SetRepository(path string, repo *gogit.Repository) error
	InvalidateRepository(path string) bool
	InvalidateAll()
	Close() error
	GetMetrics() CacheMetrics
}

// CacheMetrics represents cache performance metrics
type CacheMetrics struct {
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Evictions int64 `json:"evictions"`
	Entries   int64 `json:"entries"`
}

// DistributedCache interface for L2 cache (Redis-based)
type DistributedCache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	Close() error
}

// MultiTierConfig configures the multi-tier caching system
type MultiTierConfig struct {
	// L1 Cache settings (local memory)
	L1MaxEntries int           `json:"l1_max_entries"`
	L1TTL        time.Duration `json:"l1_ttl"`
	L1Enabled    bool          `json:"l1_enabled"`

	// L2 Cache settings (Redis)
	L2TTL       time.Duration `json:"l2_ttl"`
	L2Enabled   bool          `json:"l2_enabled"`
	L2KeyPrefix string        `json:"l2_key_prefix"`

	// Prefetcher settings
	PrefetchEnabled     bool    `json:"prefetch_enabled"`
	PrefetchConfidence  float64 `json:"prefetch_confidence"`   // Minimum confidence to prefetch
	PrefetchLookahead   int     `json:"prefetch_lookahead"`    // How many repositories to prefetch
	PrefetchWorkerCount int     `json:"prefetch_worker_count"` // Parallel prefetch workers

	// Access pattern tracking
	TrackingEnabled      bool          `json:"tracking_enabled"`
	PatternWindowSize    int           `json:"pattern_window_size"`    // Number of recent accesses to track
	PatternAnalysisDelay time.Duration `json:"pattern_analysis_delay"` // How often to analyze patterns
}

// NewMultiTierCache creates a new intelligent multi-tier cache
func NewMultiTierCache(l1Cache RepositoryCache, l2Cache DistributedCache, config *MultiTierConfig, logger *zap.Logger) *MultiTierCache {
	if config == nil {
		config = DefaultMultiTierConfig()
	}

	mtc := &MultiTierCache{
		logger:  logger,
		l1Cache: l1Cache,
		l2Cache: l2Cache,
		config:  config,
		stats:   &MultiTierStats{},
	}

	// Initialize access pattern tracker
	if config.TrackingEnabled {
		mtc.accessTracker = NewAccessPatternTracker(config.PatternWindowSize, logger)
	}

	// Initialize predictive prefetcher
	if config.PrefetchEnabled {
		mtc.prefetcher = NewPredictivePrefetcher(mtc, config, logger)
		mtc.prefetcher.Start()
	}

	return mtc
}

// GetRepository retrieves repository using intelligent multi-tier strategy
func (mtc *MultiTierCache) GetRepository(ctx context.Context, path string) (*gogit.Repository, bool) {
	// Record access for pattern analysis
	if mtc.accessTracker != nil {
		mtc.accessTracker.RecordAccess(path)
	}

	// L1 Cache: Check local memory cache first (fastest)
	if mtc.config.L1Enabled {
		if repo, found := mtc.l1Cache.GetRepository(path); found {
			mtc.recordL1Hit()
			mtc.logger.Debug("L1 cache hit", zap.String("repo", path))

			// Trigger predictive prefetching based on access patterns
			if mtc.prefetcher != nil {
				mtc.prefetcher.TriggerPredictivePrefetch(path)
			}

			return repo, true
		}
		mtc.recordL1Miss()
	}

	// L2 Cache: Check distributed cache (Redis)
	if mtc.config.L2Enabled && mtc.l2Cache != nil {
		if repoData, err := mtc.l2Cache.Get(ctx, mtc.getL2Key(path)); err == nil && len(repoData) > 0 {
			// Deserialize repository (simplified - would need proper serialization)
			if repo := mtc.deserializeRepository(repoData); repo != nil {
				mtc.recordL2Hit()
				mtc.logger.Debug("L2 cache hit", zap.String("repo", path))

				// Promote to L1 cache
				if mtc.config.L1Enabled {
					_ = mtc.l1Cache.SetRepository(path, repo)
				}

				return repo, true
			}
		}
		mtc.recordL2Miss()
	}

	// Cache miss - record for analytics
	mtc.recordCacheMiss()
	return nil, false
}

// SetRepository stores repository across appropriate cache tiers
func (mtc *MultiTierCache) SetRepository(ctx context.Context, path string, repo *gogit.Repository) error {
	// Store in L1 cache (local memory)
	if mtc.config.L1Enabled {
		if err := mtc.l1Cache.SetRepository(path, repo); err != nil {
			mtc.logger.Warn("Failed to store in L1 cache", zap.Error(err))
		}
	}

	// Store in L2 cache (distributed)
	if mtc.config.L2Enabled && mtc.l2Cache != nil {
		if repoData := mtc.serializeRepository(repo); len(repoData) > 0 {
			key := mtc.getL2Key(path)
			if err := mtc.l2Cache.Set(ctx, key, repoData, mtc.config.L2TTL); err != nil {
				mtc.logger.Warn("Failed to store in L2 cache", zap.Error(err))
			}
		}
	}

	return nil
}

// InvalidateRepository removes repository from all cache tiers
func (mtc *MultiTierCache) InvalidateRepository(ctx context.Context, path string) {
	// Remove from L1
	if mtc.config.L1Enabled {
		mtc.l1Cache.InvalidateRepository(path)
	}

	// Remove from L2
	if mtc.config.L2Enabled && mtc.l2Cache != nil {
		_ = mtc.l2Cache.Delete(ctx, mtc.getL2Key(path))
	}

	mtc.logger.Debug("Repository invalidated from all cache tiers", zap.String("repo", path))
}

// GetOptimizedHitRate returns the current cache hit rate across all tiers
func (mtc *MultiTierCache) GetOptimizedHitRate() float64 {
	mtc.mutex.RLock()
	defer mtc.mutex.RUnlock()

	totalRequests := mtc.stats.L1Hits + mtc.stats.L1Misses
	if totalRequests == 0 {
		return 0.0
	}

	totalHits := mtc.stats.L1Hits + mtc.stats.L2Hits
	return float64(totalHits) / float64(totalRequests) * 100
}

// GetStats returns comprehensive caching statistics
func (mtc *MultiTierCache) GetStats() *MultiTierStats {
	mtc.mutex.RLock()
	defer mtc.mutex.RUnlock()

	// Create a copy to avoid race conditions
	stats := *mtc.stats

	// Add L1 cache metrics if available
	if mtc.l1Cache != nil {
		l1Metrics := mtc.l1Cache.GetMetrics()
		stats.L1Entries = l1Metrics.Entries
		stats.L1Evictions = l1Metrics.Evictions
	}

	return &stats
}

// Close shuts down all cache components
func (mtc *MultiTierCache) Close() error {
	// Stop prefetcher
	if mtc.prefetcher != nil {
		mtc.prefetcher.Stop()
	}

	// Close L1 cache
	if mtc.l1Cache != nil {
		if err := mtc.l1Cache.Close(); err != nil {
			mtc.logger.Error("Error closing L1 cache", zap.Error(err))
		}
	}

	// Close L2 cache
	if mtc.l2Cache != nil {
		if err := mtc.l2Cache.Close(); err != nil {
			mtc.logger.Error("Error closing L2 cache", zap.Error(err))
		}
	}

	return nil
}

// Helper methods

func (mtc *MultiTierCache) getL2Key(path string) string {
	return fmt.Sprintf("%s:repo:%s", mtc.config.L2KeyPrefix, path)
}

func (mtc *MultiTierCache) serializeRepository(repo *gogit.Repository) []byte {
	// Simplified serialization - in production would need proper git object serialization
	// For now, just store metadata that can help with cache validation
	metadata := map[string]interface{}{
		"cached_at": time.Now().Unix(),
		"repo_path": "placeholder", // Would store actual repository data
	}

	data, _ := json.Marshal(metadata)
	return data
}

func (mtc *MultiTierCache) deserializeRepository(data []byte) *gogit.Repository {
	// Simplified deserialization - in production would reconstruct git repository
	// For now, return nil to indicate we need to load from disk
	// This is a placeholder for the actual implementation
	return nil
}

// Statistics tracking methods
func (mtc *MultiTierCache) recordL1Hit() {
	mtc.mutex.Lock()
	defer mtc.mutex.Unlock()
	mtc.stats.L1Hits++
}

func (mtc *MultiTierCache) recordL1Miss() {
	mtc.mutex.Lock()
	defer mtc.mutex.Unlock()
	mtc.stats.L1Misses++
}

func (mtc *MultiTierCache) recordL2Hit() {
	mtc.mutex.Lock()
	defer mtc.mutex.Unlock()
	mtc.stats.L2Hits++
}

func (mtc *MultiTierCache) recordL2Miss() {
	mtc.mutex.Lock()
	defer mtc.mutex.Unlock()
	mtc.stats.L2Misses++
}

func (mtc *MultiTierCache) recordCacheMiss() {
	mtc.mutex.Lock()
	defer mtc.mutex.Unlock()
	mtc.stats.TotalMisses++
}

func (mtc *MultiTierCache) recordPrefetch() {
	mtc.mutex.Lock()
	defer mtc.mutex.Unlock()
	mtc.stats.PrefetchCount++
}

// MultiTierStats tracks comprehensive caching performance
type MultiTierStats struct {
	L1Hits      int64 `json:"l1_hits"`
	L1Misses    int64 `json:"l1_misses"`
	L1Entries   int64 `json:"l1_entries"`
	L1Evictions int64 `json:"l1_evictions"`

	L2Hits   int64 `json:"l2_hits"`
	L2Misses int64 `json:"l2_misses"`

	TotalMisses   int64 `json:"total_misses"`
	PrefetchCount int64 `json:"prefetch_count"`
}

// DefaultMultiTierConfig returns sensible default configuration
func DefaultMultiTierConfig() *MultiTierConfig {
	return &MultiTierConfig{
		// L1 Cache (local memory)
		L1MaxEntries: 500,              // Increased from 100
		L1TTL:        15 * time.Minute, // Shorter for freshness
		L1Enabled:    true,

		// L2 Cache (Redis)
		L2TTL:       2 * time.Hour, // Longer persistence
		L2Enabled:   true,
		L2KeyPrefix: "gitcache",

		// Prefetching
		PrefetchEnabled:     true,
		PrefetchConfidence:  0.7, // 70% confidence threshold
		PrefetchLookahead:   3,   // Prefetch 3 related repos
		PrefetchWorkerCount: 2,   // Parallel prefetch workers

		// Access tracking
		TrackingEnabled:      true,
		PatternWindowSize:    100, // Track last 100 accesses
		PatternAnalysisDelay: 1 * time.Minute,
	}
}
