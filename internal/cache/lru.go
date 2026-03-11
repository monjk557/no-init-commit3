package cache

import (
	"fmt"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	lru "github.com/hashicorp/golang-lru/v2"
)

// Repository는 메타데이터와 함께 캐시된 Git 저장소를 나타냅니다
type Repository struct {
	// GitRepo는 실제 go-git 저장소 객체입니다
	GitRepo *gogit.Repository

	// Path는 저장소의 파일시스템 경로입니다
	Path string

	// CreatedAt은 이 저장소가 캐시된 시간을 추적합니다
	CreatedAt time.Time

	// LastAccessed는 이 저장소가 마지막으로 접근된 시간을 추적합니다
	LastAccessed time.Time

	// AccessCount는 이 저장소가 접근된 횟수를 추적합니다
	AccessCount int64

	// mutex는 LastAccessed와 AccessCount에 대한 동시 접근을 보호합니다
	mutex sync.RWMutex
}

// Close는 저장소를 제대로 닫고 자원을 해제합니다
func (r *Repository) Close() error {
	// go-git 저장소는 대부분의 경우 명시적 닫기가 필요하지 않습니다.
	// 이 저장소에 대한 활성 참조가 있을 수 있고,
	// GitRepo를 nil로 설정하면 "저장소가 닫혔습니다" 오류로 실패할 수 있습니다.
	// Go 가비지 컩렉터가 참조가 더 이상 없을 때 정리합니다.
	return nil
}

// UpdateAccess는 마지막 접근 시간을 업데이트하고 접근 횟수를 증가시킵니다
func (r *Repository) UpdateAccess() {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.LastAccessed = time.Now()
	r.AccessCount++
}

// GetLastAccessed는 스레드 안전한 방식으로 마지막 접근 시간을 반환합니다
func (r *Repository) GetLastAccessed() time.Time {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return r.LastAccessed
}

// GetAccessCount는 스레드 안전한 방식으로 접근 횟수를 반환합니다
func (r *Repository) GetAccessCount() int64 {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return r.AccessCount
}

// IsExpired는 저장소가 TTL을 초과했는지 확인합니다
func (r *Repository) IsExpired(ttl time.Duration) bool {
	if ttl <= 0 {
		return false // TTL이 0 또는 음수이면 만료되지 않음
	}
	return time.Since(r.CreatedAt) > ttl
}

// LRUCache는 Git 저장소를 위한 스레드 안전한 LRU 캐시를 구현합니다
type LRUCache struct {
	// cache는 기본 LRU 캐시입니다
	cache *lru.Cache[string, *Repository]

	// config는 캐시 설정을 저장합니다
	config *CacheConfig

	// mutex는 캐시에 대한 동시 접근을 보호합니다
	mutex sync.RWMutex

	// metrics는 캐시 통계를 저장합니다
	metrics *CacheMetrics

	// prometheusMetrics는 Prometheus 메트릭을 저장합니다 (선택사항)
	prometheusMetrics *PrometheusMetrics

	// onEvicted는 저장소가 캐시에서 제거될 때 호출됩니다
	onEvicted func(key string, repository *Repository)

	// TTL 정리 고루틴 관리
	stopCleanup chan struct{}
	cleanupDone chan struct{}
}

// CacheMetrics는 캐시 성능 통계를 추적합니다
type CacheMetrics struct {
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Evictions int64 `json:"evictions"`
	Entries   int64 `json:"entries"`
	mutex     sync.RWMutex
}

// NewLRUCache는 주어진 설정으로 새로운 LRU 캐시를 생성합니다
func NewLRUCache(config *CacheConfig) (*LRUCache, error) {
	if config == nil {
		return nil, fmt.Errorf("cache config cannot be nil")
	}

	if err := config.ValidateAndApplyDefaults(); err != nil {
		return nil, fmt.Errorf("invalid cache config: %w", err)
	}

	// Create the LRU cache instance
	cache := &LRUCache{
		config:  config,
		metrics: &CacheMetrics{},
	}

	// Create the underlying LRU cache with eviction callback
	lruCache, err := lru.NewWithEvict(config.MaxRepositories, cache.handleEviction)
	if err != nil {
		return nil, fmt.Errorf("failed to create LRU cache: %w", err)
	}

	cache.cache = lruCache

	// Start TTL cleanup goroutine if TTL is enabled
	if config.Enabled && config.TTL > 0 {
		cache.stopCleanup = make(chan struct{})
		cache.cleanupDone = make(chan struct{})
		go cache.cleanupExpiredEntries()
	}

	return cache, nil
}

// NewLRUCacheForTesting creates a new LRU cache with the given configuration without validation
// This is intended for testing purposes only and bypasses configuration validation
func NewLRUCacheForTesting(config *CacheConfig) (*LRUCache, error) {
	if config == nil {
		return nil, fmt.Errorf("cache config cannot be nil")
	}

	// Apply defaults but skip validation for testing
	config.ApplyDefaults()

	// Create the LRU cache instance
	cache := &LRUCache{
		config:  config,
		metrics: &CacheMetrics{},
	}

	// Create the underlying LRU cache with eviction callback
	lruCache, err := lru.NewWithEvict(config.MaxRepositories, cache.handleEviction)
	if err != nil {
		return nil, fmt.Errorf("failed to create LRU cache: %w", err)
	}

	cache.cache = lruCache

	// Start TTL cleanup goroutine if TTL is enabled
	if config.Enabled && config.TTL > 0 {
		cache.stopCleanup = make(chan struct{})
		cache.cleanupDone = make(chan struct{})
		go cache.cleanupExpiredEntries()
	}

	return cache, nil
}

// Get retrieves a repository from the cache
func (c *LRUCache) Get(path string) (*Repository, bool) {
	if !c.config.Enabled {
		c.recordMiss()
		return nil, false
	}

	c.mutex.RLock()
	repo, found := c.cache.Get(path)
	c.mutex.RUnlock()

	if !found {
		c.recordMiss()
		return nil, false
	}

	// Check if the repository has expired
	if repo.IsExpired(c.config.TTL) {
		// Remove expired entry and return miss
		c.mutex.Lock()
		c.cache.Remove(path)
		c.decrementEntries()
		c.mutex.Unlock()
		c.recordMiss()
		return nil, false
	}

	// Update access information
	repo.UpdateAccess()
	c.recordHit()
	return repo, true
}

// Set stores a repository in the cache
func (c *LRUCache) Set(path string, gitRepo *gogit.Repository) error {
	if !c.config.Enabled {
		return nil // Cache disabled, silently ignore
	}

	if path == "" {
		return fmt.Errorf("repository path cannot be empty")
	}

	if gitRepo == nil {
		return fmt.Errorf("git repository cannot be nil")
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Create repository wrapper
	repo := &Repository{
		GitRepo:      gitRepo,
		Path:         path,
		CreatedAt:    time.Now(),
		LastAccessed: time.Now(),
		AccessCount:  1,
	}

	// Check if key already exists to avoid double counting
	exists := c.cache.Contains(path)

	// Store in cache (this may trigger eviction)
	c.cache.Add(path, repo)

	// Only increment entries if this is a new entry (not updating existing)
	if !exists {
		c.incrementEntries()
	}

	// Record Prometheus metrics if enabled
	if c.prometheusMetrics != nil {
		c.prometheusMetrics.RecordSet()
		// Update current entry count and memory usage
		c.updatePrometheusMetrics()
	}

	return nil
}

// Remove removes a repository from the cache
func (c *LRUCache) Remove(path string) bool {
	if !c.config.Enabled {
		return false
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	existed := c.cache.Remove(path)
	if existed {
		c.decrementEntries()
	}
	return existed
}

// Contains checks if a repository exists in the cache without updating LRU order
func (c *LRUCache) Contains(path string) bool {
	if !c.config.Enabled {
		return false
	}

	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.cache.Contains(path)
}

// Len returns the current number of items in the cache
func (c *LRUCache) Len() int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.cache.Len()
}

// Keys returns a copy of all keys in the cache
func (c *LRUCache) Keys() []string {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.cache.Keys()
}

// Purge removes all items from the cache
func (c *LRUCache) Purge() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.cache.Purge()
	c.resetEntries()
}

// cleanupExpiredEntries runs in a background goroutine to periodically remove expired entries
func (c *LRUCache) cleanupExpiredEntries() {
	defer close(c.cleanupDone)

	ticker := time.NewTicker(c.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCleanup:
			return
		case <-ticker.C:
			c.removeExpiredEntries()
		}
	}
}

// removeExpiredEntries scans and removes all expired entries from the cache
func (c *LRUCache) removeExpiredEntries() {
	if !c.config.Enabled || c.config.TTL <= 0 {
		return
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Get all keys to check for expiration
	keys := c.cache.Keys()
	expiredKeys := make([]string, 0)

	// Check each entry for expiration
	for _, key := range keys {
		if repo, found := c.cache.Peek(key); found && repo.IsExpired(c.config.TTL) {
			expiredKeys = append(expiredKeys, key)
		}
	}

	// Remove expired entries
	cleanupCount := len(expiredKeys)
	for _, key := range expiredKeys {
		c.cache.Remove(key)
		c.decrementEntries()
	}

	// Record cleanup metrics if enabled and entries were cleaned up
	if c.prometheusMetrics != nil && cleanupCount > 0 {
		c.prometheusMetrics.RecordCleanup()
	}
}

// Close shuts down the cache and cleans up all resources
func (c *LRUCache) Close() error {
	// Signal cleanup goroutine to stop
	if c.stopCleanup != nil {
		close(c.stopCleanup)
		// Wait for cleanup goroutine to finish
		if c.cleanupDone != nil {
			<-c.cleanupDone
		}
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Purge will trigger eviction callbacks for cleanup
	c.cache.Purge()
	return nil
}

// GetMetrics returns a copy of the current cache metrics
func (c *LRUCache) GetMetrics() CacheMetrics {
	c.metrics.mutex.RLock()
	defer c.metrics.mutex.RUnlock()

	return CacheMetrics{
		Hits:      c.metrics.Hits,
		Misses:    c.metrics.Misses,
		Evictions: c.metrics.Evictions,
		Entries:   c.metrics.Entries,
	}
}

// ResetMetrics resets all cache metrics to zero
func (c *LRUCache) ResetMetrics() {
	c.metrics.mutex.Lock()
	defer c.metrics.mutex.Unlock()

	c.metrics.Hits = 0
	c.metrics.Misses = 0
	c.metrics.Evictions = 0
	c.metrics.Entries = 0
}

// GetHitRatio returns the cache hit ratio as a percentage (0.0 - 1.0)
func (c *LRUCache) GetHitRatio() float64 {
	metrics := c.GetMetrics()
	total := metrics.Hits + metrics.Misses
	if total == 0 {
		return 0.0
	}
	return float64(metrics.Hits) / float64(total)
}

// handleEviction is called when an item is evicted from the cache
func (c *LRUCache) handleEviction(key string, repo *Repository) {
	// Clean up the repository
	if repo != nil {
		_ = repo.Close() // Ignore error as we're already evicting
	}

	// Call custom eviction handler if set
	if c.onEvicted != nil {
		c.onEvicted(key, repo)
	}

	// Update metrics
	c.recordEviction()
	c.decrementEntries()
}

// SetEvictionCallback sets a custom callback for when repositories are evicted
func (c *LRUCache) SetEvictionCallback(callback func(string, *Repository)) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.onEvicted = callback
}

// Private metrics methods

func (c *LRUCache) recordHit() {
	c.metrics.mutex.Lock()
	defer c.metrics.mutex.Unlock()
	c.metrics.Hits++

	// Record Prometheus metric if enabled
	if c.prometheusMetrics != nil {
		c.prometheusMetrics.RecordHit()
	}
}

func (c *LRUCache) recordMiss() {
	c.metrics.mutex.Lock()
	defer c.metrics.mutex.Unlock()
	c.metrics.Misses++

	// Record Prometheus metric if enabled
	if c.prometheusMetrics != nil {
		c.prometheusMetrics.RecordMiss()
	}
}

func (c *LRUCache) recordEviction() {
	c.metrics.mutex.Lock()
	defer c.metrics.mutex.Unlock()
	c.metrics.Evictions++

	// Record Prometheus metric if enabled
	if c.prometheusMetrics != nil {
		c.prometheusMetrics.RecordEviction()
	}
}

func (c *LRUCache) incrementEntries() {
	c.metrics.mutex.Lock()
	defer c.metrics.mutex.Unlock()
	c.metrics.Entries++

	// Update Prometheus metrics if enabled
	if c.prometheusMetrics != nil {
		c.prometheusMetrics.UpdateEntries(c.metrics.Entries)
		// Update memory usage estimate using the internal version to avoid recursive locking
		memoryBytes := c.estimateMemoryUsageWithEntries(c.metrics.Entries)
		c.prometheusMetrics.UpdateMemoryUsage(memoryBytes)
	}
}

func (c *LRUCache) decrementEntries() {
	c.metrics.mutex.Lock()
	defer c.metrics.mutex.Unlock()
	if c.metrics.Entries > 0 {
		c.metrics.Entries--
	}

	// Update Prometheus metrics if enabled
	if c.prometheusMetrics != nil {
		c.prometheusMetrics.UpdateEntries(c.metrics.Entries)
		// Update memory usage estimate using the internal version to avoid recursive locking
		memoryBytes := c.estimateMemoryUsageWithEntries(c.metrics.Entries)
		c.prometheusMetrics.UpdateMemoryUsage(memoryBytes)
	}
}

func (c *LRUCache) resetEntries() {
	c.metrics.mutex.Lock()
	defer c.metrics.mutex.Unlock()
	c.metrics.Entries = 0
}

// EnablePrometheusMetrics enables Prometheus metrics collection for this cache
// This uses the default Prometheus registry via promauto
func (c *LRUCache) EnablePrometheusMetrics() {
	if c.prometheusMetrics == nil {
		// NewPrometheusMetrics uses promauto which automatically registers with default registry
		c.prometheusMetrics = NewPrometheusMetrics()
		// Update current state
		c.updatePrometheusMetrics()
	}
}

// EnablePrometheusMetricsWithRegistry enables metrics (registry parameter ignored, kept for API compatibility).
func (c *LRUCache) EnablePrometheusMetricsWithRegistry(_ any) error {
	c.EnablePrometheusMetrics()
	return nil
}

// updatePrometheusMetrics synchronizes current cache state with Prometheus metrics
func (c *LRUCache) updatePrometheusMetrics() {
	if c.prometheusMetrics == nil {
		return
	}

	metrics := c.GetMetrics()

	// Update all current metrics
	c.prometheusMetrics.UpdateEntries(metrics.Entries)

	// Calculate estimated memory usage
	memoryBytes := c.estimateMemoryUsage()
	c.prometheusMetrics.UpdateMemoryUsage(memoryBytes)
}

// estimateMemoryUsage provides a rough estimate of cache memory usage
func (c *LRUCache) estimateMemoryUsage() int64 {
	// Rough estimation:
	// - Each entry has repository object overhead (~1KB)
	// - Git repo objects are harder to estimate, use conservative 10KB average
	// - Repository metadata ~500 bytes
	// This is a simplified estimation for monitoring purposes
	const avgRepoSize = 11 * 1024 // 11KB per repository (conservative estimate)

	metrics := c.GetMetrics()
	return metrics.Entries * avgRepoSize
}

// estimateMemoryUsageWithEntries provides memory estimate with given entry count
// This version is used internally when we already have the entry count to avoid recursive locking
func (c *LRUCache) estimateMemoryUsageWithEntries(entries int64) int64 {
	const avgRepoSize = 11 * 1024 // 11KB per repository (conservative estimate)
	return entries * avgRepoSize
}

// RepositoryCache interface implementation

// GetRepository retrieves a cached repository by its filesystem path.
// Returns the repository and true if found, or nil and false if not cached.
func (c *LRUCache) GetRepository(path string) (*gogit.Repository, bool) {
	// Use the existing Get method
	repo, exists := c.Get(path)
	if !exists || repo == nil {
		return nil, false
	}

	// Extract the git repository from our Repository wrapper
	return repo.GitRepo, true
}

// SetRepository stores a repository in the cache with its filesystem path as the key.
// The cache implementation handles eviction according to its policy.
func (c *LRUCache) SetRepository(path string, repo *gogit.Repository) error {
	// Use the existing Set method which handles the Repository wrapper creation
	return c.Set(path, repo)
}

// InvalidateRepository removes a repository from the cache.
// This should be called when a repository is modified (e.g., after a push).
func (c *LRUCache) InvalidateRepository(path string) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if !c.config.Enabled {
		return false
	}

	// Check if it exists
	if _, exists := c.cache.Get(path); !exists {
		return false
	}

	// Remove from cache
	removed := c.cache.Remove(path)
	if removed {
		c.decrementEntries()
	}

	return removed
}

// InvalidateAll removes all repositories from the cache.
// Useful for cache maintenance or when global repository changes occur.
func (c *LRUCache) InvalidateAll() {
	// Use the existing Purge method
	c.Purge()
}

// Ensure LRUCache implements RepositoryCache interface
var _ RepositoryCache = (*LRUCache)(nil)
