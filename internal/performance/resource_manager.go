// Package performance는 Git executor 서비스의 종합적인 성능 최적화와 리소스 관리 기능을 제공합니다.
// 메모리 및 연결 관리, 저장소 캐시, 요청 추적, 성능 모니터링 등을 포함합니다.
package performance

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"go.uber.org/zap"

	gogit "github.com/go-git/go-git/v5"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
)

// ResourceManager는 포괄적인 리소스 관리와 성능 최적화를 제공합니다.
// 저장소 풀, 메모리 모니터, 연결 풀, 요청 추적기 등을 통합 관리합니다.
type ResourceManager struct {
	config           *ResourceConfig
	logger           *logging.Logger
	repositoryPool   *RepositoryPool
	memoryMonitor    *MemoryMonitor
	connectionPool   *ConnectionPool
	requestTracker   *RequestTracker
	cacheManager     *CacheManager
	metricsCollector *MetricsCollector
}

// ResourceConfig는 리소스 관리를 위한 설정을 포함합니다.
// 저장소 풀, 메모리, 연결, 요청 관리 및 캐시 설정을 정의합니다.
type ResourceConfig struct {
	// Repository Pool Configuration
	MaxRepositories int           `json:"max_repositories"` // Maximum cached repositories
	RepositoryTTL   time.Duration `json:"repository_ttl"`   // Repository cache TTL
	CleanupInterval time.Duration `json:"cleanup_interval"` // Cache cleanup interval

	// Memory Management
	MaxMemoryUsage      int64         `json:"max_memory_usage"`      // Maximum memory usage (bytes)
	GCThreshold         float64       `json:"gc_threshold"`          // GC trigger threshold (0.0-1.0)
	MemoryCheckInterval time.Duration `json:"memory_check_interval"` // Memory monitoring interval

	// Connection Management
	MaxConnections    int           `json:"max_connections"`    // Maximum concurrent connections
	ConnectionTimeout time.Duration `json:"connection_timeout"` // Connection timeout
	IdleTimeout       time.Duration `json:"idle_timeout"`       // Idle connection timeout

	// Request Management
	MaxRequestDuration time.Duration `json:"max_request_duration"` // Maximum request duration
	RequestQueueSize   int           `json:"request_queue_size"`   // Request queue size
	ConcurrentRequests int           `json:"concurrent_requests"`  // Maximum concurrent requests

	// Cache Management
	EnableRepositoryCache bool `json:"enable_repository_cache"` // Enable repository caching
	EnableObjectCache     bool `json:"enable_object_cache"`     // Enable Git object caching
	CacheCompressionLevel int  `json:"cache_compression_level"` // Cache compression (0-9)

	// Streaming Configuration
	StreamingBufferSize int   `json:"streaming_buffer_size"` // Streaming buffer size
	MaxStreamingFile    int64 `json:"max_streaming_file"`    // Maximum file size for streaming
	EnableStreaming     bool  `json:"enable_streaming"`      // Enable streaming for large files
}

// DefaultResourceConfig returns default resource configuration
func DefaultResourceConfig() *ResourceConfig {
	return &ResourceConfig{
		MaxRepositories:       100,
		RepositoryTTL:         30 * time.Minute,
		CleanupInterval:       5 * time.Minute,
		MaxMemoryUsage:        1024 * 1024 * 1024, // 1GB
		GCThreshold:           0.8,
		MemoryCheckInterval:   30 * time.Second,
		MaxConnections:        1000,
		ConnectionTimeout:     30 * time.Second,
		IdleTimeout:           5 * time.Minute,
		MaxRequestDuration:    10 * time.Minute,
		RequestQueueSize:      10000,
		ConcurrentRequests:    500,
		EnableRepositoryCache: true,
		EnableObjectCache:     true,
		CacheCompressionLevel: 6,
		StreamingBufferSize:   64 * 1024,         // 64KB
		MaxStreamingFile:      100 * 1024 * 1024, // 100MB
		EnableStreaming:       true,
	}
}

// NewResourceManager는 새로운 리소스 매니저 인스턴스를 생성합니다.
// 설정을 바탕으로 모든 리소스 관리 컴포너트들을 초기화합니다.
func NewResourceManager(cfg *config.Config) (*ResourceManager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	// Use default resource config if not specified
	resourceCfg := DefaultResourceConfig()

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	// Initialize components
	repositoryPool := NewRepositoryPool(resourceCfg, pathConfig, logger)
	memoryMonitor := NewMemoryMonitor(resourceCfg, logger)
	connectionPool := NewConnectionPool(resourceCfg, logger)
	requestTracker := NewRequestTracker(resourceCfg, logger)
	cacheManager := NewCacheManager(resourceCfg, logger)
	metricsCollector := NewMetricsCollector(logger)

	rm := &ResourceManager{
		config:           resourceCfg,
		logger:           logger,
		repositoryPool:   repositoryPool,
		memoryMonitor:    memoryMonitor,
		connectionPool:   connectionPool,
		requestTracker:   requestTracker,
		cacheManager:     cacheManager,
		metricsCollector: metricsCollector,
	}

	// Start background processes
	if err := rm.start(); err != nil {
		return nil, fmt.Errorf("failed to start resource manager: %w", err)
	}

	return rm, nil
}

// RepositoryPool은 Git 저장소 인스턴스들의 풀을 관리합니다.
// 빈번하게 사용되는 저장소들을 메모리에 캐시하여 성능을 향상시킵니다.
type RepositoryPool struct {
	config     *ResourceConfig
	pathConfig *repositories.PathConfig
	logger     *logging.Logger
	repos      map[string]*RepositoryCacheEntry
	mu         sync.RWMutex
}

// RepositoryCacheEntry represents a cached repository entry
type RepositoryCacheEntry struct {
	Repository  *gogit.Repository
	LastAccess  time.Time
	AccessCount int64
	MemoryUsage int64
}

// NewRepositoryPool creates a new repository pool
func NewRepositoryPool(config *ResourceConfig, pathConfig *repositories.PathConfig, logger *logging.Logger) *RepositoryPool {
	return &RepositoryPool{
		config:     config,
		pathConfig: pathConfig,
		logger:     logger,
		repos:      make(map[string]*RepositoryCacheEntry),
	}
}

// GetRepository retrieves or creates a repository from the pool
func (rp *RepositoryPool) GetRepository(ctx context.Context, owner, repo string) (*gogit.Repository, error) {
	key := fmt.Sprintf("%s/%s", owner, repo)

	rp.mu.RLock()
	if entry, exists := rp.repos[key]; exists {
		entry.LastAccess = time.Now()
		entry.AccessCount++
		rp.mu.RUnlock()

		rp.logger.Debug("Repository retrieved from pool",
			zap.String("key", key),
			zap.Int64("access_count", entry.AccessCount))

		return entry.Repository, nil
	}
	rp.mu.RUnlock()

	// Repository not in pool, create it
	repoPath, err := repositories.GetRepoPath(rp.pathConfig, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to get repository path: %w", err)
	}

	// Use filesystem storage for better performance with cached repositories
	repository, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Calculate approximate memory usage
	memUsage := rp.estimateRepositoryMemoryUsage(repository)

	// Add to pool
	rp.mu.Lock()
	defer rp.mu.Unlock()

	// Check if we need to evict entries
	if len(rp.repos) >= rp.config.MaxRepositories {
		rp.evictLeastRecentlyUsed()
	}

	rp.repos[key] = &RepositoryCacheEntry{
		Repository:  repository,
		LastAccess:  time.Now(),
		AccessCount: 1,
		MemoryUsage: memUsage,
	}

	rp.logger.Info("Repository added to pool",
		zap.String("key", key),
		zap.Int64("memory_usage", memUsage),
		zap.Int("pool_size", len(rp.repos)))

	return repository, nil
}

// estimateRepositoryMemoryUsage estimates the memory usage of a repository
func (rp *RepositoryPool) estimateRepositoryMemoryUsage(repo *gogit.Repository) int64 {
	// This is a simplified estimation
	// In a real implementation, you might want to calculate based on:
	// - Number of references
	// - Object store size
	// - Index size, etc.
	return 1024 * 1024 // 1MB base estimate
}

// evictLeastRecentlyUsed removes the least recently used repository from the pool
func (rp *RepositoryPool) evictLeastRecentlyUsed() {
	var oldestKey string
	var oldestTime time.Time
	first := true

	for key, entry := range rp.repos {
		if first || entry.LastAccess.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.LastAccess
			first = false
		}
	}

	if oldestKey != "" {
		delete(rp.repos, oldestKey)
		rp.logger.Debug("Repository evicted from pool",
			zap.String("key", oldestKey),
			zap.Time("last_access", oldestTime))
	}
}

// Cleanup removes expired repositories from the pool
func (rp *RepositoryPool) Cleanup() {
	rp.mu.Lock()
	defer rp.mu.Unlock()

	now := time.Now()
	var removed []string

	for key, entry := range rp.repos {
		if now.Sub(entry.LastAccess) > rp.config.RepositoryTTL {
			delete(rp.repos, key)
			removed = append(removed, key)
		}
	}

	if len(removed) > 0 {
		rp.logger.Info("Repository pool cleanup completed",
			zap.Strings("removed", removed),
			zap.Int("remaining", len(rp.repos)))
	}
}

// MemoryMonitor는 메모리 사용량을 모니터링하고 관리합니다.
// 메모리 사용량을 백그라운드에서 감시하고 필요시 GC를 트리거합니다.
type MemoryMonitor struct {
	config   *ResourceConfig
	logger   *logging.Logger
	stats    *MemoryStats
	stopChan chan struct{}
	mu       sync.RWMutex
}

// MemoryStats contains memory usage statistics
type MemoryStats struct {
	Alloc        uint64    `json:"alloc"`          // Currently allocated bytes
	TotalAlloc   uint64    `json:"total_alloc"`    // Total allocated bytes
	Sys          uint64    `json:"sys"`            // System memory
	NumGC        uint32    `json:"num_gc"`         // Number of GC cycles
	LastGC       time.Time `json:"last_gc"`        // Last GC time
	GCPauseTotal uint64    `json:"gc_pause_total"` // Total GC pause time
	HeapAlloc    uint64    `json:"heap_alloc"`     // Heap allocated bytes
	HeapSys      uint64    `json:"heap_sys"`       // Heap system bytes
	HeapInuse    uint64    `json:"heap_inuse"`     // Heap in-use bytes
	StackInuse   uint64    `json:"stack_inuse"`    // Stack in-use bytes
	UsagePercent float64   `json:"usage_percent"`  // Memory usage percentage
	LastUpdated  time.Time `json:"last_updated"`   // Last update time
}

// NewMemoryMonitor creates a new memory monitor
func NewMemoryMonitor(config *ResourceConfig, logger *logging.Logger) *MemoryMonitor {
	return &MemoryMonitor{
		config:   config,
		logger:   logger,
		stats:    &MemoryStats{},
		stopChan: make(chan struct{}),
	}
}

// Start begins memory monitoring
func (mm *MemoryMonitor) Start() {
	go mm.monitorLoop()
}

// Stop stops memory monitoring
func (mm *MemoryMonitor) Stop() {
	select {
	case <-mm.stopChan:
		// Already closed
		return
	default:
		close(mm.stopChan)
	}
}

// monitorLoop runs the memory monitoring loop
func (mm *MemoryMonitor) monitorLoop() {
	ticker := time.NewTicker(mm.config.MemoryCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			mm.updateStats()
			mm.checkMemoryPressure()
		case <-mm.stopChan:
			return
		}
	}
}

// updateStats updates memory statistics
func (mm *MemoryMonitor) updateStats() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	mm.mu.Lock()
	defer mm.mu.Unlock()

	mm.stats.Alloc = m.Alloc
	mm.stats.TotalAlloc = m.TotalAlloc
	mm.stats.Sys = m.Sys
	mm.stats.NumGC = m.NumGC
	mm.stats.HeapAlloc = m.HeapAlloc
	mm.stats.HeapSys = m.HeapSys
	mm.stats.HeapInuse = m.HeapInuse
	mm.stats.StackInuse = m.StackInuse
	mm.stats.UsagePercent = float64(m.Alloc) / float64(mm.config.MaxMemoryUsage) * 100
	mm.stats.LastUpdated = time.Now()

	if m.NumGC > 0 {
		mm.stats.LastGC = time.Unix(0, int64(m.LastGC))
		mm.stats.GCPauseTotal = m.PauseTotalNs
	}
}

// checkMemoryPressure checks if memory usage is too high and triggers GC if needed
func (mm *MemoryMonitor) checkMemoryPressure() {
	mm.mu.RLock()
	usagePercent := mm.stats.UsagePercent
	mm.mu.RUnlock()

	if usagePercent > mm.config.GCThreshold*100 {
		mm.logger.Warn("High memory usage detected, triggering garbage collection",
			zap.Float64("usage_percent", usagePercent),
			zap.Float64("threshold", mm.config.GCThreshold*100))

		runtime.GC()

		// Update stats after GC
		mm.updateStats()

		mm.mu.RLock()
		newUsage := mm.stats.UsagePercent
		mm.mu.RUnlock()

		mm.logger.Info("Garbage collection completed",
			zap.Float64("usage_before", usagePercent),
			zap.Float64("usage_after", newUsage))
	}
}

// GetStats returns current memory statistics
func (mm *MemoryMonitor) GetStats() *MemoryStats {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	// Return a copy to avoid race conditions
	statsCopy := *mm.stats
	return &statsCopy
}

// ConnectionPool manages HTTP connections and resource limits
type ConnectionPool struct {
	config        *ResourceConfig
	logger        *logging.Logger
	activeConns   int
	totalConns    int64
	rejectedConns int64
	mu            sync.RWMutex
}

// NewConnectionPool creates a new connection pool
func NewConnectionPool(config *ResourceConfig, logger *logging.Logger) *ConnectionPool {
	return &ConnectionPool{
		config: config,
		logger: logger,
	}
}

// AcquireConnection attempts to acquire a connection slot
func (cp *ConnectionPool) AcquireConnection(ctx context.Context) error {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	if cp.activeConns >= cp.config.MaxConnections {
		cp.rejectedConns++
		return fmt.Errorf("connection limit exceeded: %d/%d active connections",
			cp.activeConns, cp.config.MaxConnections)
	}

	cp.activeConns++
	cp.totalConns++

	cp.logger.Debug("Connection acquired",
		zap.Int("active", cp.activeConns),
		zap.Int("max", cp.config.MaxConnections))

	return nil
}

// ReleaseConnection releases a connection slot
func (cp *ConnectionPool) ReleaseConnection() {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	if cp.activeConns > 0 {
		cp.activeConns--
	}

	cp.logger.Debug("Connection released",
		zap.Int("active", cp.activeConns))
}

// GetStats returns connection pool statistics
func (cp *ConnectionPool) GetStats() map[string]interface{} {
	cp.mu.RLock()
	defer cp.mu.RUnlock()

	return map[string]interface{}{
		"active_connections":   cp.activeConns,
		"max_connections":      cp.config.MaxConnections,
		"total_connections":    cp.totalConns,
		"rejected_connections": cp.rejectedConns,
		"utilization_percent":  float64(cp.activeConns) / float64(cp.config.MaxConnections) * 100,
	}
}

// RequestTracker tracks and manages active requests
type RequestTracker struct {
	config         *ResourceConfig
	logger         *logging.Logger
	activeRequests map[string]*RequestInfo
	requestQueue   chan *RequestInfo
	totalRequests  int64
	completedReqs  int64
	timeoutReqs    int64
}

// RequestInfo contains information about an active request
type RequestInfo struct {
	ID        string
	StartTime time.Time
	Context   context.Context
	Cancel    context.CancelFunc
	Type      string
	Resource  string
}

// NewRequestTracker creates a new request tracker
func NewRequestTracker(config *ResourceConfig, logger *logging.Logger) *RequestTracker {
	return &RequestTracker{
		config:         config,
		logger:         logger,
		activeRequests: make(map[string]*RequestInfo),
		requestQueue:   make(chan *RequestInfo, config.RequestQueueSize),
	}
}

// CacheManager manages various caches for performance optimization
type CacheManager struct {
	config      *ResourceConfig
	logger      *logging.Logger
	objectCache map[string]*CacheEntry
}

// CacheEntry represents a cached object entry
type CacheEntry struct {
	Data        interface{}
	Size        int64
	CreatedAt   time.Time
	LastAccess  time.Time
	AccessCount int64
}

// NewCacheManager creates a new cache manager
func NewCacheManager(config *ResourceConfig, logger *logging.Logger) *CacheManager {
	return &CacheManager{
		config:      config,
		logger:      logger,
		objectCache: make(map[string]*CacheEntry),
	}
}

// MetricsCollector collects and aggregates performance metrics
type MetricsCollector struct {
	logger  *logging.Logger
	metrics map[string]*MetricValue
}

// MetricValue represents a collected metric
type MetricValue struct {
	Value     float64
	Count     int64
	Sum       float64
	Min       float64
	Max       float64
	LastValue float64
	UpdatedAt time.Time
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector(logger *logging.Logger) *MetricsCollector {
	return &MetricsCollector{
		logger:  logger,
		metrics: make(map[string]*MetricValue),
	}
}

// start initializes and starts all background processes
func (rm *ResourceManager) start() error {
	// Start memory monitoring
	rm.memoryMonitor.Start()

	// Start cleanup routines
	go rm.cleanupLoop()

	rm.logger.Info("Resource manager started successfully",
		zap.Int("max_repositories", rm.config.MaxRepositories),
		zap.Int("max_connections", rm.config.MaxConnections),
		zap.Duration("cleanup_interval", rm.config.CleanupInterval))

	return nil
}

// cleanupLoop runs periodic cleanup operations
func (rm *ResourceManager) cleanupLoop() {
	ticker := time.NewTicker(rm.config.CleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		rm.repositoryPool.Cleanup()
		// Add other cleanup operations as needed
	}
}

// Stop gracefully stops the resource manager
func (rm *ResourceManager) Stop() {
	rm.memoryMonitor.Stop()
	rm.logger.Info("Resource manager stopped")
}

// GetResourceStats returns comprehensive resource statistics
func (rm *ResourceManager) GetResourceStats() map[string]interface{} {
	return map[string]interface{}{
		"memory":      rm.memoryMonitor.GetStats(),
		"connections": rm.connectionPool.GetStats(),
		"repository_pool": map[string]interface{}{
			"size": len(rm.repositoryPool.repos),
			"max":  rm.config.MaxRepositories,
		},
	}
}

// StreamingManager handles streaming operations for large files
type StreamingManager struct {
	config *ResourceConfig
	logger *logging.Logger
}

// NewStreamingManager creates a new streaming manager
func NewStreamingManager(config *ResourceConfig, logger *logging.Logger) *StreamingManager {
	return &StreamingManager{
		config: config,
		logger: logger,
	}
}

// ShouldStream determines if a file should be streamed based on size
func (sm *StreamingManager) ShouldStream(fileSize int64) bool {
	return sm.config.EnableStreaming && fileSize > sm.config.MaxStreamingFile
}
