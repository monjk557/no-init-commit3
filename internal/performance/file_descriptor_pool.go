package performance

import (
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"go.uber.org/zap"
)

// FileDescriptorPool manages repository file descriptors to reduce OpenFile overhead
// Research target: Reduce 28.21% CPU overhead from file I/O operations
type FileDescriptorPool struct {
	config        *PerformanceConfig
	logger        *zap.Logger
	repositories  map[string]*pooledRepository
	mutex         sync.RWMutex
	maxPoolSize   int
	maxIdleTime   time.Duration
	cleanupTicker *time.Ticker
	stopCleanup   chan struct{}
	stats         *FDPoolStats
}

// FDPoolStats tracks file descriptor pool performance metrics
type FDPoolStats struct {
	PoolHits    int64     `json:"pool_hits"`
	PoolMisses  int64     `json:"pool_misses"`
	ActiveFDs   int64     `json:"active_fds"`
	PoolSize    int64     `json:"pool_size"`
	EvictedFDs  int64     `json:"evicted_fds"`
	HitRate     float64   `json:"hit_rate"`
	LastUpdated time.Time `json:"last_updated"`
}

// pooledRepository represents a cached repository connection
type pooledRepository struct {
	repo        *gogit.Repository
	path        string
	lastUsed    time.Time
	refCount    int64
	accessCount int64
	createdTime time.Time
	mutex       sync.RWMutex
}

// NewFileDescriptorPool creates a new file descriptor pool
func NewFileDescriptorPool(config *PerformanceConfig, logger *zap.Logger) *FileDescriptorPool {
	pool := &FileDescriptorPool{
		config:       config,
		logger:       logger,
		repositories: make(map[string]*pooledRepository),
		maxPoolSize:  100,             // Reasonable pool size for file descriptors
		maxIdleTime:  5 * time.Minute, // Close idle connections after 5 minutes
		stopCleanup:  make(chan struct{}),
		stats: &FDPoolStats{
			LastUpdated: time.Now(),
		},
	}

	// Start cleanup goroutine if optimization is enabled
	if config.EnableCommitGraphOptimization || config.EnableObjectCache {
		pool.startCleanupWorker()
	}

	return pool
}

// GetRepository returns a repository from the pool or opens a new one
// TARGET: Reduce the 28.21% CPU overhead from OpenFile operations
func (fdp *FileDescriptorPool) GetRepository(repoPath string) (*gogit.Repository, error) {
	if !fdp.isOptimizationEnabled() {
		// No optimization, open repository directly
		fdp.updateStats(false, true)
		return gogit.PlainOpen(repoPath)
	}

	fdp.mutex.RLock()
	pooled, exists := fdp.repositories[repoPath]
	fdp.mutex.RUnlock()

	if exists {
		// Update access information
		pooled.mutex.Lock()
		pooled.refCount++
		pooled.lastUsed = time.Now()
		pooled.accessCount++
		pooled.mutex.Unlock()

		fdp.updateStats(true, false)

		fdp.logger.Debug("Repository FD reused from pool",
			zap.String("path", repoPath),
			zap.Int64("ref_count", pooled.refCount),
			zap.Int64("access_count", pooled.accessCount))

		return pooled.repo, nil
	}

	// Pool miss - need to open new repository
	fdp.updateStats(false, true)

	// Open new repository
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}

	// Add to pool if there's space
	fdp.mutex.Lock()
	defer fdp.mutex.Unlock()

	// Check pool size limit
	if len(fdp.repositories) >= fdp.maxPoolSize {
		// Evict least recently used repository
		fdp.evictLeastRecentlyUsed()
	}

	// Add new repository to pool
	fdp.repositories[repoPath] = &pooledRepository{
		repo:        repo,
		path:        repoPath,
		lastUsed:    time.Now(),
		refCount:    1,
		accessCount: 1,
		createdTime: time.Now(),
	}

	fdp.logger.Debug("Repository added to FD pool",
		zap.String("path", repoPath),
		zap.Int("pool_size", len(fdp.repositories)))

	return repo, nil
}

// ReleaseRepository decrements the reference count for a repository
func (fdp *FileDescriptorPool) ReleaseRepository(repoPath string) {
	if !fdp.isOptimizationEnabled() {
		return
	}

	fdp.mutex.RLock()
	pooled, exists := fdp.repositories[repoPath]
	fdp.mutex.RUnlock()

	if exists {
		pooled.mutex.Lock()
		pooled.refCount--
		if pooled.refCount < 0 {
			pooled.refCount = 0
		}
		pooled.mutex.Unlock()

		fdp.logger.Debug("Repository FD released",
			zap.String("path", repoPath),
			zap.Int64("ref_count", pooled.refCount))
	}
}

// evictLeastRecentlyUsed removes the least recently used repository from pool
func (fdp *FileDescriptorPool) evictLeastRecentlyUsed() {
	var oldestPath string
	var oldestTime time.Time
	var oldestRefCount int64 = -1

	// Find repository with lowest reference count and oldest access time
	for path, pooled := range fdp.repositories {
		pooled.mutex.RLock()
		refCount := pooled.refCount
		lastUsed := pooled.lastUsed
		pooled.mutex.RUnlock()

		// Prioritize repositories with zero references
		if refCount == 0 {
			if oldestRefCount == -1 || lastUsed.Before(oldestTime) {
				oldestPath = path
				oldestTime = lastUsed
				oldestRefCount = refCount
			}
		} else if oldestRefCount != 0 && (oldestRefCount == -1 || refCount < oldestRefCount) {
			// If no zero-ref repositories, pick lowest ref count
			oldestPath = path
			oldestTime = lastUsed
			oldestRefCount = refCount
		}
	}

	if oldestPath != "" {
		delete(fdp.repositories, oldestPath)

		fdp.stats.EvictedFDs++

		fdp.logger.Debug("Repository evicted from FD pool",
			zap.String("path", oldestPath),
			zap.Time("last_used", oldestTime),
			zap.Int64("ref_count", oldestRefCount))
	}
}

// startCleanupWorker periodically removes idle repository connections
func (fdp *FileDescriptorPool) startCleanupWorker() {
	fdp.cleanupTicker = time.NewTicker(2 * time.Minute) // Cleanup every 2 minutes

	go func() {
		for {
			select {
			case <-fdp.cleanupTicker.C:
				fdp.cleanup()
			case <-fdp.stopCleanup:
				fdp.cleanupTicker.Stop()
				return
			}
		}
	}()
}

// cleanup removes idle repository connections from the pool
func (fdp *FileDescriptorPool) cleanup() {
	fdp.mutex.Lock()
	defer fdp.mutex.Unlock()

	now := time.Now()
	var removed []string

	for path, pooled := range fdp.repositories {
		pooled.mutex.RLock()
		idle := now.Sub(pooled.lastUsed) > fdp.maxIdleTime && pooled.refCount == 0
		pooled.mutex.RUnlock()

		if idle {
			delete(fdp.repositories, path)
			removed = append(removed, path)
			fdp.stats.EvictedFDs++
		}
	}

	if len(removed) > 0 {
		fdp.logger.Debug("Cleaned up idle repository FDs",
			zap.Strings("removed_paths", removed),
			zap.Int("remaining", len(fdp.repositories)))
	}
}

// updateStats updates pool statistics
func (fdp *FileDescriptorPool) updateStats(hit, miss bool) {
	if hit {
		fdp.stats.PoolHits++
	}
	if miss {
		fdp.stats.PoolMisses++
	}

	// Calculate hit rate
	totalRequests := fdp.stats.PoolHits + fdp.stats.PoolMisses
	if totalRequests > 0 {
		fdp.stats.HitRate = float64(fdp.stats.PoolHits) / float64(totalRequests) * 100
	}

	// Update pool size
	fdp.mutex.RLock()
	fdp.stats.PoolSize = int64(len(fdp.repositories))

	// Count active file descriptors
	activeFDs := int64(0)
	for _, pooled := range fdp.repositories {
		pooled.mutex.RLock()
		if pooled.refCount > 0 {
			activeFDs++
		}
		pooled.mutex.RUnlock()
	}
	fdp.stats.ActiveFDs = activeFDs
	fdp.mutex.RUnlock()

	fdp.stats.LastUpdated = time.Now()
}

// GetStats returns current file descriptor pool statistics
func (fdp *FileDescriptorPool) GetStats() *FDPoolStats {
	fdp.updateStats(false, false) // Update stats without changing counters

	// Return a copy to avoid race conditions
	statsCopy := *fdp.stats
	return &statsCopy
}

// isOptimizationEnabled checks if FD pool optimization should be active
func (fdp *FileDescriptorPool) isOptimizationEnabled() bool {
	return fdp.config.EnableCommitGraphOptimization || fdp.config.EnableObjectCache
}

// Close stops the cleanup worker and clears the pool
func (fdp *FileDescriptorPool) Close() {
	// Stop cleanup worker
	close(fdp.stopCleanup)

	// Clear pool
	fdp.mutex.Lock()
	defer fdp.mutex.Unlock()

	fdp.repositories = make(map[string]*pooledRepository)
	fdp.stats = &FDPoolStats{
		LastUpdated: time.Now(),
	}

	fdp.logger.Info("File descriptor pool closed")
}

// GetPoolUtilization returns the current pool utilization percentage
func (fdp *FileDescriptorPool) GetPoolUtilization() float64 {
	fdp.mutex.RLock()
	defer fdp.mutex.RUnlock()

	if fdp.maxPoolSize == 0 {
		return 0
	}

	return float64(len(fdp.repositories)) / float64(fdp.maxPoolSize) * 100
}
