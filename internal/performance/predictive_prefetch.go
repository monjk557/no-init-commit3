package performance

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// PredictivePrefetcher provides intelligent repository prefetching based on access patterns
type PredictivePrefetcher struct {
	cache  *MultiTierCache
	config *MultiTierConfig
	logger *zap.Logger

	// Pattern analysis
	patterns    *AccessPatternTracker
	predictions map[string]*PredictionEntry
	mutex       sync.RWMutex

	// Worker management
	workers    []*PrefetchWorker
	workQueue  chan *PrefetchTask
	stopSignal chan struct{}
	wg         sync.WaitGroup
}

// PredictionEntry represents a predicted access pattern
type PredictionEntry struct {
	Repository    string    `json:"repository"`
	Confidence    float64   `json:"confidence"` // 0.0-1.0 prediction confidence
	PredictedAt   time.Time `json:"predicted_at"`
	LastAccess    time.Time `json:"last_access"`
	AccessPattern string    `json:"access_pattern"` // e.g., "sequential", "related", "temporal"
	RelatedRepos  []string  `json:"related_repos"`  // Repositories often accessed together
}

// PrefetchTask represents a repository prefetch operation
type PrefetchTask struct {
	Repository string
	Confidence float64
	Priority   int    // Higher = more urgent
	Reason     string // Why this prefetch was triggered
	RelatedTo  string // Which repository triggered this prefetch
}

// PrefetchWorker handles background prefetching
type PrefetchWorker struct {
	id       int
	cache    *MultiTierCache
	logger   *zap.Logger
	workChan chan *PrefetchTask
	stopChan chan struct{}
}

// NewPredictivePrefetcher creates a new predictive prefetcher
func NewPredictivePrefetcher(cache *MultiTierCache, config *MultiTierConfig, logger *zap.Logger) *PredictivePrefetcher {
	pp := &PredictivePrefetcher{
		cache:       cache,
		config:      config,
		logger:      logger,
		predictions: make(map[string]*PredictionEntry),
		workQueue:   make(chan *PrefetchTask, 100),
		stopSignal:  make(chan struct{}),
	}

	// Initialize access pattern tracker if not already set
	if cache.accessTracker == nil {
		cache.accessTracker = NewAccessPatternTracker(config.PatternWindowSize, logger)
	}
	pp.patterns = cache.accessTracker

	return pp
}

// Start initializes and starts the prefetching workers
func (pp *PredictivePrefetcher) Start() {
	pp.logger.Info("Starting predictive prefetcher",
		zap.Int("worker_count", pp.config.PrefetchWorkerCount),
		zap.Float64("confidence_threshold", pp.config.PrefetchConfidence))

	// Create prefetch workers
	for i := 0; i < pp.config.PrefetchWorkerCount; i++ {
		worker := &PrefetchWorker{
			id:       i,
			cache:    pp.cache,
			logger:   pp.logger,
			workChan: pp.workQueue,
			stopChan: pp.stopSignal,
		}
		pp.workers = append(pp.workers, worker)

		pp.wg.Add(1)
		go worker.Run(&pp.wg)
	}

	// Start pattern analysis goroutine
	pp.wg.Add(1)
	go pp.runPatternAnalysis(&pp.wg)
}

// Stop gracefully shuts down the prefetcher
func (pp *PredictivePrefetcher) Stop() {
	pp.logger.Info("Stopping predictive prefetcher")

	close(pp.stopSignal)
	pp.wg.Wait()

	pp.logger.Info("Predictive prefetcher stopped")
}

// TriggerPredictivePrefetch analyzes access patterns and triggers prefetching
func (pp *PredictivePrefetcher) TriggerPredictivePrefetch(recentlyAccessed string) {
	if !pp.config.PrefetchEnabled {
		return
	}

	// Analyze patterns for recently accessed repository
	predictions := pp.analyzePatternsFor(recentlyAccessed)

	for _, prediction := range predictions {
		if prediction.Confidence >= pp.config.PrefetchConfidence {
			// Create prefetch task
			task := &PrefetchTask{
				Repository: prediction.Repository,
				Confidence: prediction.Confidence,
				Priority:   pp.calculatePriority(prediction),
				Reason:     prediction.AccessPattern,
				RelatedTo:  recentlyAccessed,
			}

			// Queue for prefetching
			select {
			case pp.workQueue <- task:
				pp.logger.Debug("Queued prefetch task",
					zap.String("repo", task.Repository),
					zap.Float64("confidence", task.Confidence),
					zap.String("reason", task.Reason))
			default:
				pp.logger.Warn("Prefetch queue full, dropping task", zap.String("repo", task.Repository))
			}
		}
	}
}

// analyzePatternsFor analyzes access patterns for a specific repository
func (pp *PredictivePrefetcher) analyzePatternsFor(repository string) []*PredictionEntry {
	var predictions []*PredictionEntry

	// Sequential access pattern analysis
	sequentialPreds := pp.analyzeSequentialPattern(repository)
	predictions = append(predictions, sequentialPreds...)

	// Related repository pattern analysis
	relatedPreds := pp.analyzeRelatedPattern(repository)
	predictions = append(predictions, relatedPreds...)

	// Temporal pattern analysis (time-based patterns)
	temporalPreds := pp.analyzeTemporalPattern(repository)
	predictions = append(predictions, temporalPreds...)

	return predictions
}

// analyzeSequentialPattern finds repositories accessed in sequence
func (pp *PredictivePrefetcher) analyzeSequentialPattern(repository string) []*PredictionEntry {
	var predictions []*PredictionEntry

	recentAccesses := pp.patterns.GetRecentAccesses(20)
	if len(recentAccesses) < 3 {
		return predictions
	}

	// Find position of current repository in recent accesses
	currentIndex := -1
	for i, access := range recentAccesses {
		if access.Repository == repository {
			currentIndex = i
			break
		}
	}

	if currentIndex == -1 || currentIndex >= len(recentAccesses)-1 {
		return predictions
	}

	// Analyze what typically comes next after this repository
	subsequentRepos := make(map[string]int)
	for i := currentIndex + 1; i < len(recentAccesses) && i < currentIndex+pp.config.PrefetchLookahead; i++ {
		next := recentAccesses[i].Repository
		if next != repository { // Don't prefetch the same repository
			subsequentRepos[next]++
		}
	}

	// Create predictions based on frequency
	for repo, frequency := range subsequentRepos {
		confidence := float64(frequency) / float64(pp.config.PrefetchLookahead)
		if confidence > 0.3 { // Minimum 30% likelihood
			predictions = append(predictions, &PredictionEntry{
				Repository:    repo,
				Confidence:    confidence,
				PredictedAt:   time.Now(),
				AccessPattern: "sequential",
			})
		}
	}

	return predictions
}

// analyzeRelatedPattern finds repositories often accessed together
func (pp *PredictivePrefetcher) analyzeRelatedPattern(repository string) []*PredictionEntry {
	var predictions []*PredictionEntry

	relatedRepos := pp.patterns.GetRelatedRepositories(repository, 5)

	for _, related := range relatedRepos {
		// Calculate confidence based on co-occurrence frequency
		coOccurrence := pp.patterns.GetCoOccurrenceScore(repository, related.Repository)
		confidence := coOccurrence * 0.8 // Scale down for related pattern

		if confidence >= 0.4 { // Minimum threshold for related patterns
			predictions = append(predictions, &PredictionEntry{
				Repository:    related.Repository,
				Confidence:    confidence,
				PredictedAt:   time.Now(),
				AccessPattern: "related",
				RelatedRepos:  []string{repository},
			})
		}
	}

	return predictions
}

// analyzeTemporalPattern finds time-based access patterns
func (pp *PredictivePrefetcher) analyzeTemporalPattern(repository string) []*PredictionEntry {
	var predictions []*PredictionEntry

	// Analyze what repositories are typically accessed around this time
	now := time.Now()
	timeWindow := pp.patterns.GetTimeBasedPatterns(now.Hour(), now.Weekday())

	for _, pattern := range timeWindow {
		if pattern.Repository != repository && pattern.Frequency > 0.6 {
			predictions = append(predictions, &PredictionEntry{
				Repository:    pattern.Repository,
				Confidence:    pattern.Frequency * 0.6, // Scale for temporal pattern
				PredictedAt:   time.Now(),
				AccessPattern: "temporal",
			})
		}
	}

	return predictions
}

// calculatePriority assigns priority to prefetch tasks
func (pp *PredictivePrefetcher) calculatePriority(prediction *PredictionEntry) int {
	priority := int(prediction.Confidence * 100)

	// Boost priority for highly confident predictions
	if prediction.Confidence > 0.8 {
		priority += 20
	}

	// Boost priority for sequential patterns (most likely to be accessed soon)
	if prediction.AccessPattern == "sequential" {
		priority += 15
	}

	return priority
}

// runPatternAnalysis periodically analyzes access patterns and updates predictions
func (pp *PredictivePrefetcher) runPatternAnalysis(wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(pp.config.PatternAnalysisDelay)
	defer ticker.Stop()

	for {
		select {
		case <-pp.stopSignal:
			return
		case <-ticker.C:
			pp.performPatternAnalysis()
		}
	}
}

// performPatternAnalysis analyzes recent access patterns and updates predictions
func (pp *PredictivePrefetcher) performPatternAnalysis() {
	pp.mutex.Lock()
	defer pp.mutex.Unlock()

	// Clear old predictions
	now := time.Now()
	for key, prediction := range pp.predictions {
		if now.Sub(prediction.PredictedAt) > 30*time.Minute {
			delete(pp.predictions, key)
		}
	}

	// Analyze top repositories for proactive prefetching
	topRepos := pp.patterns.GetTopRepositories(10)
	for _, repo := range topRepos {
		// Generate predictions for top repositories
		predictions := pp.analyzePatternsFor(repo.Repository)
		for _, pred := range predictions {
			pp.predictions[pred.Repository] = pred
		}
	}

	pp.logger.Debug("Pattern analysis completed",
		zap.Int("predictions", len(pp.predictions)))
}

// PrefetchWorker implementation

// Run starts the prefetch worker
func (pw *PrefetchWorker) Run(wg *sync.WaitGroup) {
	defer wg.Done()

	pw.logger.Debug("Prefetch worker started", zap.Int("worker_id", pw.id))

	for {
		select {
		case task := <-pw.workChan:
			pw.handlePrefetchTask(task)
		case <-pw.stopChan:
			pw.logger.Debug("Prefetch worker stopping", zap.Int("worker_id", pw.id))
			return
		}
	}
}

// handlePrefetchTask processes a single prefetch task
func (pw *PrefetchWorker) handlePrefetchTask(task *PrefetchTask) {
	ctx := context.Background()

	// Check if repository is already in cache
	if _, exists := pw.cache.GetRepository(ctx, task.Repository); exists {
		pw.logger.Debug("Repository already cached, skipping prefetch",
			zap.String("repo", task.Repository))
		return
	}

	pw.logger.Debug("Executing prefetch task",
		zap.String("repo", task.Repository),
		zap.Float64("confidence", task.Confidence),
		zap.String("reason", task.Reason),
		zap.String("related_to", task.RelatedTo))

	// Simulate repository loading (in practice, this would load from disk)
	// This is a placeholder for the actual repository loading logic
	start := time.Now()

	// Record prefetch attempt
	pw.cache.recordPrefetch()

	duration := time.Since(start)
	pw.logger.Debug("Prefetch completed",
		zap.String("repo", task.Repository),
		zap.Duration("duration", duration))
}
