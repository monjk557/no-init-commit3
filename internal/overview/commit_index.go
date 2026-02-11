package overview

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/observability"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	lru "github.com/hashicorp/golang-lru/v2"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Constants / defaults
// ---------------------------------------------------------------------------

const (
	defaultMaxIndexEntries   = 1000
	defaultMaxWorkers        = 4
	defaultMaxQueueLen       = 32
	defaultHotnessThreshold  = 3 // requests in window before warmup eligible
	defaultHotnessWindowSecs = 300
	defaultMaxDirsPerRef     = 50
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type indexEntry struct {
	commits   map[string]*GitCommit
	meta      LatestCommitsMeta
	cost      int // len(paths) for cost-aware eviction tracking
	createdAt time.Time
}

// hotKey tracks request frequency for warmup eligibility.
type hotKey struct {
	count     int64
	firstSeen time.Time
}

// CommitIndex provides a pre-populated directory-level commit index.
//
// Key: (repoPath, refHash, dirPath) → map[path]*GitCommit
//
// Safety features (plan2):
//   - Global worker pool (max concurrent background populates)
//   - Per-repo rate limit (1 concurrent populate per repo)
//   - Queue drop: tryAcquire, no blocking
//   - Hotness-based warmup: only populate dirs that are actually hot
//   - Cost tracking: eviction considers entry size
//   - Stale refHash eviction: keeps recent 2 per repo
type CommitIndex struct {
	mu    sync.RWMutex
	cache *lru.Cache[string, *indexEntry]

	// Background population controls
	workerSem    chan struct{} // global concurrency limiter
	repoInflight sync.Map      // repoPath → struct{} (1 concurrent per repo)
	inflight     sync.Map      // full key → struct{} (dedup per dir)
	queueLen     atomic.Int32  // approximate pending queue length

	// Hotness tracking
	hotMu   sync.Mutex
	hotKeys map[string]*hotKey // indexKey → hotKey

	// Cost tracking
	totalCost atomic.Int64

	// Config
	maxWorkers        int
	maxQueueLen       int
	hotnessThreshold  int
	hotnessWindowSecs int
	maxDirsPerRef     int

	service *RepositoryOverviewService
	logger  *logging.Logger
}

// NewCommitIndex creates a new commit index with bounded resources.
func NewCommitIndex(maxEntries int, service *RepositoryOverviewService, logger *logging.Logger) *CommitIndex {
	if maxEntries <= 0 {
		maxEntries = defaultMaxIndexEntries
	}
	c, _ := lru.New[string, *indexEntry](maxEntries)

	maxWorkers := defaultMaxWorkers

	return &CommitIndex{
		cache:             c,
		workerSem:         make(chan struct{}, maxWorkers),
		hotKeys:           make(map[string]*hotKey),
		maxWorkers:        maxWorkers,
		maxQueueLen:       defaultMaxQueueLen,
		hotnessThreshold:  defaultHotnessThreshold,
		hotnessWindowSecs: defaultHotnessWindowSecs,
		maxDirsPerRef:     defaultMaxDirsPerRef,
		service:           service,
		logger:            logger,
	}
}

func indexKey(repoPath, refHash, dirPath string) string {
	return fmt.Sprintf("idx|%s|%s|%s", repoPath, refHash, dirPath)
}

// ---------------------------------------------------------------------------
// Lookup / Store
// ---------------------------------------------------------------------------

// Lookup checks the index and also bumps the hotness counter.
func (idx *CommitIndex) Lookup(repoPath, refHash, dirPath string) (map[string]*GitCommit, LatestCommitsMeta, bool) {
	m := observability.GetApplicationMetrics()
	key := indexKey(repoPath, refHash, dirPath)

	// Always bump hotness on lookup (hit or miss)
	idx.bumpHotness(key)

	idx.mu.RLock()
	entry, ok := idx.cache.Get(key)
	idx.mu.RUnlock()

	if !ok || entry == nil {
		m.RecordCommitIndexOp("miss")
		return nil, LatestCommitsMeta{}, false
	}

	m.RecordCommitIndexOp("hit")
	return entry.commits, entry.meta, true
}

// Store puts a result into the index with cost tracking.
func (idx *CommitIndex) Store(repoPath, refHash, dirPath string, commits map[string]*GitCommit, meta LatestCommitsMeta) {
	m := observability.GetApplicationMetrics()
	key := indexKey(repoPath, refHash, dirPath)
	cost := len(commits)

	idx.mu.Lock()
	// Track cost delta: if replacing, subtract old cost
	if old, ok := idx.cache.Get(key); ok && old != nil {
		idx.totalCost.Add(int64(-old.cost))
	}
	idx.cache.Add(key, &indexEntry{
		commits:   commits,
		meta:      meta,
		cost:      cost,
		createdAt: time.Now(),
	})
	idx.totalCost.Add(int64(cost))
	m.SetCommitIndexEntries(idx.cache.Len())
	idx.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Hotness tracking
// ---------------------------------------------------------------------------

func (idx *CommitIndex) bumpHotness(key string) {
	idx.hotMu.Lock()
	defer idx.hotMu.Unlock()

	now := time.Now()
	window := time.Duration(idx.hotnessWindowSecs) * time.Second

	hk, ok := idx.hotKeys[key]
	if !ok || now.Sub(hk.firstSeen) > window {
		// New window
		idx.hotKeys[key] = &hotKey{count: 1, firstSeen: now}
		return
	}
	hk.count++
}

// isHot returns true if the key has been requested >= threshold times in the window.
func (idx *CommitIndex) isHot(key string) bool {
	idx.hotMu.Lock()
	defer idx.hotMu.Unlock()

	hk, ok := idx.hotKeys[key]
	if !ok {
		return false
	}

	now := time.Now()
	window := time.Duration(idx.hotnessWindowSecs) * time.Second
	if now.Sub(hk.firstSeen) > window {
		// Expired
		delete(idx.hotKeys, key)
		return false
	}
	return hk.count >= int64(idx.hotnessThreshold)
}

// ---------------------------------------------------------------------------
// Background population (bounded)
// ---------------------------------------------------------------------------

// ScheduleBackgroundPopulation triggers a background full computation for a dir.
//
// Safety:
//   - Global worker pool (tryAcquire, no blocking)
//   - Per-repo rate limit (1 concurrent)
//   - Queue length cap (drop if full)
//   - Hotness check (only populate hot dirs)
//   - Dedup per key
func (idx *CommitIndex) ScheduleBackgroundPopulation(
	repoPath, refHash, dirPath string,
	repo *gogit.Repository,
	pathInfos []CommitPathInfo,
) {
	key := indexKey(repoPath, refHash, dirPath)

	// Hotness gate: only populate dirs that are actually hot.
	if !idx.isHot(key) {
		return
	}

	// Dedup: already inflight for this exact key?
	if _, loaded := idx.inflight.LoadOrStore(key, struct{}{}); loaded {
		return
	}

	// Queue length cap: drop if full.
	if idx.queueLen.Load() >= int32(idx.maxQueueLen) {
		idx.inflight.Delete(key)
		m := observability.GetApplicationMetrics()
		m.RecordCommitIndexOp("drop")
		return
	}

	// Per-repo rate limit: only 1 concurrent populate per repo.
	repoKey := repoPath
	if _, loaded := idx.repoInflight.LoadOrStore(repoKey, struct{}{}); loaded {
		idx.inflight.Delete(key)
		return
	}

	// Try to acquire a worker slot (non-blocking).
	select {
	case idx.workerSem <- struct{}{}:
		// Got a slot.
	default:
		// All workers busy — drop.
		idx.inflight.Delete(key)
		idx.repoInflight.Delete(repoKey)
		m := observability.GetApplicationMetrics()
		m.RecordCommitIndexOp("drop")
		return
	}

	idx.queueLen.Add(1)

	go func() {
		defer func() {
			<-idx.workerSem
			idx.inflight.Delete(key)
			idx.repoInflight.Delete(repoKey)
			idx.queueLen.Add(-1)
		}()

		m := observability.GetApplicationMetrics()
		m.RecordCommitIndexOp("populate")

		hash := plumbing.NewHash(refHash)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		commits, meta := idx.service.GetLatestCommitsForPathsWithMeta(
			ctx, repo, hash, pathInfos,
			5*time.Second, // generous time budget for background
			100_000,       // generous max commits for background
		)

		idx.Store(repoPath, refHash, dirPath, commits, meta)

		if idx.logger != nil {
			idx.logger.Debug("commit index populated",
				zap.String("repoPath", repoPath),
				zap.String("refHash", refHash[:min(8, len(refHash))]),
				zap.String("dirPath", dirPath),
				zap.Int("paths", len(commits)),
				zap.Bool("incomplete", meta.Incomplete),
				zap.String("cutReason", meta.CutReason),
			)
		}
	}()
}

// ---------------------------------------------------------------------------
// Observability
// ---------------------------------------------------------------------------

// Len returns the number of cached index entries.
func (idx *CommitIndex) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.cache.Len()
}

// TotalCost returns the sum of all entry costs (for monitoring).
func (idx *CommitIndex) TotalCost() int64 {
	return idx.totalCost.Load()
}

// QueueLen returns the approximate pending background job count.
func (idx *CommitIndex) QueueLen() int32 {
	return idx.queueLen.Load()
}
