package performance

import (
	"container/list"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// ObjectCache provides high-performance caching for Git objects to reduce EncodedObject bottleneck
// Research target: Reduce 31.55% CPU overhead from BlobObject calls
type ObjectCache struct {
	config      *PerformanceConfig
	logger      *zap.Logger
	blobCache   *lruCache
	treeCache   *lruCache
	commitCache *lruCache
	stats       *ObjectCacheStats
	mutex       sync.RWMutex
}

// ObjectCacheStats tracks cache performance metrics
type ObjectCacheStats struct {
	BlobHits     int64     `json:"blob_hits"`
	BlobMisses   int64     `json:"blob_misses"`
	TreeHits     int64     `json:"tree_hits"`
	TreeMisses   int64     `json:"tree_misses"`
	CommitHits   int64     `json:"commit_hits"`
	CommitMisses int64     `json:"commit_misses"`
	EvictedItems int64     `json:"evicted_items"`
	CacheSize    int64     `json:"cache_size"`
	HitRate      float64   `json:"hit_rate"`
	LastUpdated  time.Time `json:"last_updated"`
}

// ObjectCacheEntry represents a cached object with metadata
type ObjectCacheEntry struct {
	Object      interface{}
	Hash        plumbing.Hash
	Size        int64
	AccessTime  time.Time
	AccessCount int64
	CreatedTime time.Time
}

// lruCache implements a thread-safe LRU cache
type lruCache struct {
	capacity  int
	items     map[plumbing.Hash]*list.Element
	evictList *list.List
	mutex     sync.RWMutex
}

// newLRUCache creates a new LRU cache with the given capacity
func newLRUCache(capacity int) *lruCache {
	return &lruCache{
		capacity:  capacity,
		items:     make(map[plumbing.Hash]*list.Element),
		evictList: list.New(),
	}
}

// NewObjectCache creates a new high-performance object cache
func NewObjectCache(config *PerformanceConfig, logger *zap.Logger) *ObjectCache {
	// Cache sizes based on typical repository patterns
	blobCacheSize := 1000  // Most frequently accessed blobs
	treeCacheSize := 500   // Directory trees (less frequent but important)
	commitCacheSize := 200 // Recent commits (for traversal optimization)

	return &ObjectCache{
		config:      config,
		logger:      logger,
		blobCache:   newLRUCache(blobCacheSize),
		treeCache:   newLRUCache(treeCacheSize),
		commitCache: newLRUCache(commitCacheSize),
		stats: &ObjectCacheStats{
			LastUpdated: time.Now(),
		},
	}
}

// GetBlob retrieves a blob from cache or loads it from repository
// TARGET: Reduce the 31.55% CPU overhead from BlobObject calls
func (oc *ObjectCache) GetBlob(repo *gogit.Repository, hash plumbing.Hash) (*object.Blob, error) {
	if !oc.config.EnableObjectCache {
		// Cache disabled, use direct access
		oc.updateStats(false, false, false, false, false, false)
		return repo.BlobObject(hash)
	}

	// Try cache first
	if cachedBlob, found := oc.getFromCache(oc.blobCache, hash); found {
		oc.updateStats(true, false, false, false, false, false)
		oc.logger.Debug("Blob cache hit", zap.String("hash", hash.String()))
		return cachedBlob.(*object.Blob), nil
	}

	// Cache miss - load from repository
	oc.updateStats(false, true, false, false, false, false)
	blob, err := repo.BlobObject(hash)
	if err != nil {
		return nil, err
	}

	// Store in cache
	oc.putInCache(oc.blobCache, hash, blob, blob.Size)
	oc.logger.Debug("Blob cached", zap.String("hash", hash.String()), zap.Int64("size", blob.Size))

	return blob, nil
}

// GetBlobBatch retrieves multiple blobs efficiently using batch loading
// OPTIMIZATION: Reduce multiple BlobObject calls overhead
func (oc *ObjectCache) GetBlobBatch(repo *gogit.Repository, hashes []plumbing.Hash) (map[plumbing.Hash]*object.Blob, error) {
	results := make(map[plumbing.Hash]*object.Blob, len(hashes))
	var missingHashes []plumbing.Hash

	// Check cache for all hashes first
	for _, hash := range hashes {
		if cachedBlob, found := oc.getFromCache(oc.blobCache, hash); found {
			results[hash] = cachedBlob.(*object.Blob)
			oc.updateStats(true, false, false, false, false, false)
		} else {
			missingHashes = append(missingHashes, hash)
			oc.updateStats(false, true, false, false, false, false)
		}
	}

	// Load missing objects in batch (future optimization: use packfile reading)
	for _, hash := range missingHashes {
		blob, err := repo.BlobObject(hash)
		if err != nil {
			oc.logger.Warn("Failed to load blob in batch", zap.String("hash", hash.String()), zap.Error(err))
			continue
		}

		results[hash] = blob
		oc.putInCache(oc.blobCache, hash, blob, blob.Size)
	}

	oc.logger.Debug("Batch blob loading completed",
		zap.Int("total", len(hashes)),
		zap.Int("cache_hits", len(hashes)-len(missingHashes)),
		zap.Int("cache_misses", len(missingHashes)))

	return results, nil
}

// GetTree retrieves a tree from cache or loads it from repository
func (oc *ObjectCache) GetTree(repo *gogit.Repository, hash plumbing.Hash) (*object.Tree, error) {
	if !oc.config.EnableObjectCache {
		oc.updateStats(false, false, false, false, false, false)
		return repo.TreeObject(hash)
	}

	if cachedTree, found := oc.getFromCache(oc.treeCache, hash); found {
		oc.updateStats(false, false, true, false, false, false)
		return cachedTree.(*object.Tree), nil
	}

	oc.updateStats(false, false, false, true, false, false)
	tree, err := repo.TreeObject(hash)
	if err != nil {
		return nil, err
	}

	oc.putInCache(oc.treeCache, hash, tree, estimateTreeSize(tree))
	return tree, nil
}

// GetCommit retrieves a commit from cache or loads it from repository
func (oc *ObjectCache) GetCommit(repo *gogit.Repository, hash plumbing.Hash) (*object.Commit, error) {
	if !oc.config.EnableObjectCache {
		oc.updateStats(false, false, false, false, false, false)
		return repo.CommitObject(hash)
	}

	if cachedCommit, found := oc.getFromCache(oc.commitCache, hash); found {
		oc.updateStats(false, false, false, false, true, false)
		return cachedCommit.(*object.Commit), nil
	}

	oc.updateStats(false, false, false, false, false, true)
	commit, err := repo.CommitObject(hash)
	if err != nil {
		return nil, err
	}

	oc.putInCache(oc.commitCache, hash, commit, estimateCommitSize(commit))
	return commit, nil
}

// getFromCache retrieves an item from the specified cache
func (oc *ObjectCache) getFromCache(cache *lruCache, hash plumbing.Hash) (interface{}, bool) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	if elem, found := cache.items[hash]; found {
		cache.evictList.MoveToFront(elem)
		entry := elem.Value.(*ObjectCacheEntry)
		entry.AccessTime = time.Now()
		entry.AccessCount++
		return entry.Object, true
	}

	return nil, false
}

// putInCache stores an item in the specified cache
func (oc *ObjectCache) putInCache(cache *lruCache, hash plumbing.Hash, obj interface{}, size int64) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	// Check if item already exists
	if elem, found := cache.items[hash]; found {
		cache.evictList.MoveToFront(elem)
		entry := elem.Value.(*ObjectCacheEntry)
		entry.Object = obj
		entry.AccessTime = time.Now()
		entry.AccessCount++
		return
	}

	// Create new entry
	entry := &ObjectCacheEntry{
		Object:      obj,
		Hash:        hash,
		Size:        size,
		AccessTime:  time.Now(),
		AccessCount: 1,
		CreatedTime: time.Now(),
	}

	// Add to front of eviction list
	elem := cache.evictList.PushFront(entry)
	cache.items[hash] = elem

	// Evict oldest items if over capacity
	if cache.evictList.Len() > cache.capacity {
		oc.evictOldest(cache)
	}
}

// evictOldest removes the oldest item from cache
func (oc *ObjectCache) evictOldest(cache *lruCache) {
	elem := cache.evictList.Back()
	if elem != nil {
		cache.evictList.Remove(elem)
		entry := elem.Value.(*ObjectCacheEntry)
		delete(cache.items, entry.Hash)

		oc.mutex.Lock()
		oc.stats.EvictedItems++
		oc.mutex.Unlock()
	}
}

// updateStats updates cache statistics
func (oc *ObjectCache) updateStats(blobHit, blobMiss, treeHit, treeMiss, commitHit, commitMiss bool) {
	oc.mutex.Lock()
	defer oc.mutex.Unlock()

	if blobHit {
		oc.stats.BlobHits++
	}
	if blobMiss {
		oc.stats.BlobMisses++
	}
	if treeHit {
		oc.stats.TreeHits++
	}
	if treeMiss {
		oc.stats.TreeMisses++
	}
	if commitHit {
		oc.stats.CommitHits++
	}
	if commitMiss {
		oc.stats.CommitMisses++
	}

	// Calculate hit rate
	totalHits := oc.stats.BlobHits + oc.stats.TreeHits + oc.stats.CommitHits
	totalRequests := totalHits + oc.stats.BlobMisses + oc.stats.TreeMisses + oc.stats.CommitMisses

	if totalRequests > 0 {
		oc.stats.HitRate = float64(totalHits) / float64(totalRequests) * 100
	}

	oc.stats.LastUpdated = time.Now()
}

// GetStats returns current cache statistics
func (oc *ObjectCache) GetStats() *ObjectCacheStats {
	oc.mutex.RLock()
	defer oc.mutex.RUnlock()

	// Create a copy to avoid race conditions
	statsCopy := *oc.stats

	// Add current cache sizes
	oc.blobCache.mutex.RLock()
	blobSize := int64(oc.blobCache.evictList.Len())
	oc.blobCache.mutex.RUnlock()

	oc.treeCache.mutex.RLock()
	treeSize := int64(oc.treeCache.evictList.Len())
	oc.treeCache.mutex.RUnlock()

	oc.commitCache.mutex.RLock()
	commitSize := int64(oc.commitCache.evictList.Len())
	oc.commitCache.mutex.RUnlock()

	statsCopy.CacheSize = blobSize + treeSize + commitSize

	return &statsCopy
}

// ClearCache clears all cached objects
func (oc *ObjectCache) ClearCache() {
	oc.mutex.Lock()
	defer oc.mutex.Unlock()

	// Clear all caches
	oc.blobCache = newLRUCache(1000)
	oc.treeCache = newLRUCache(500)
	oc.commitCache = newLRUCache(200)

	// Reset stats
	oc.stats = &ObjectCacheStats{
		LastUpdated: time.Now(),
	}

	oc.logger.Info("Object cache cleared")
}

// estimateTreeSize estimates the memory size of a tree object
func estimateTreeSize(tree *object.Tree) int64 {
	// Base size + estimated entry overhead
	return int64(256 + len(tree.Entries)*64)
}

// estimateCommitSize estimates the memory size of a commit object
func estimateCommitSize(commit *object.Commit) int64 {
	// Base size + message length + metadata
	return int64(512 + len(commit.Message))
}
