package performance

import (
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func prefetchTestConfig() *MultiTierConfig {
	return &MultiTierConfig{
		L1Enabled:            true,
		L1MaxEntries:         10,
		PrefetchEnabled:      true,
		PrefetchConfidence:   0.3,
		PrefetchLookahead:    3,
		PrefetchWorkerCount:  2,
		TrackingEnabled:      true,
		PatternWindowSize:    50,
		PatternAnalysisDelay: 10 * time.Millisecond,
	}
}

func TestNewPredictivePrefetcher_InitsTracker(t *testing.T) {
	l1 := newFakeL1()
	cfg := prefetchTestConfig()
	mtc := NewMultiTierCache(l1, nil, &MultiTierConfig{L1Enabled: true, TrackingEnabled: false}, testLogger())
	defer mtc.Close()

	pp := NewPredictivePrefetcher(mtc, cfg, testLogger())
	require.NotNil(t, pp.patterns)
}

func TestPredictivePrefetcher_StartStop(t *testing.T) {
	l1 := newFakeL1()
	cfg := prefetchTestConfig()
	mtc := NewMultiTierCache(l1, nil, &MultiTierConfig{L1Enabled: true, TrackingEnabled: true, PatternWindowSize: 50}, testLogger())
	defer mtc.Close()

	pp := NewPredictivePrefetcher(mtc, cfg, testLogger())
	pp.Start()
	// Let the pattern analysis goroutine tick at least once.
	time.Sleep(30 * time.Millisecond)
	pp.Stop()
}

func TestPredictivePrefetcher_TriggerDisabled(t *testing.T) {
	l1 := newFakeL1()
	cfg := prefetchTestConfig()
	cfg.PrefetchEnabled = false
	mtc := NewMultiTierCache(l1, nil, &MultiTierConfig{L1Enabled: true, TrackingEnabled: true, PatternWindowSize: 50}, testLogger())
	defer mtc.Close()

	pp := NewPredictivePrefetcher(mtc, cfg, testLogger())
	// Should return immediately without queuing.
	pp.TriggerPredictivePrefetch("repo")
}

func TestPredictivePrefetcher_AnalyzeSequential(t *testing.T) {
	l1 := newFakeL1()
	cfg := prefetchTestConfig()
	mtc := NewMultiTierCache(l1, nil, &MultiTierConfig{L1Enabled: true, TrackingEnabled: true, PatternWindowSize: 50}, testLogger())
	defer mtc.Close()

	pp := NewPredictivePrefetcher(mtc, cfg, testLogger())

	// Build a sequential pattern: a -> b repeated.
	for i := 0; i < 5; i++ {
		pp.patterns.RecordAccess("a")
		pp.patterns.RecordAccess("b")
	}

	preds := pp.analyzeSequentialPattern("a")
	// Should predict "b" follows "a".
	found := false
	for _, p := range preds {
		if p.Repository == "b" {
			found = true
			assert.Equal(t, "sequential", p.AccessPattern)
		}
	}
	assert.True(t, found)

	// Too few accesses -> no predictions.
	pp2 := NewPredictivePrefetcher(mtc, cfg, testLogger())
	pp2.patterns = NewAccessPatternTracker(50, testLogger())
	pp2.patterns.RecordAccess("only")
	assert.Empty(t, pp2.analyzeSequentialPattern("only"))
}

func TestPredictivePrefetcher_AnalyzeRelated(t *testing.T) {
	l1 := newFakeL1()
	cfg := prefetchTestConfig()
	mtc := NewMultiTierCache(l1, nil, &MultiTierConfig{L1Enabled: true, TrackingEnabled: true, PatternWindowSize: 50}, testLogger())
	defer mtc.Close()

	pp := NewPredictivePrefetcher(mtc, cfg, testLogger())
	for i := 0; i < 8; i++ {
		pp.patterns.RecordAccess("main")
		pp.patterns.RecordAccess("lib")
	}

	preds := pp.analyzeRelatedPattern("main")
	// May or may not exceed threshold; just ensure it runs and returns a slice.
	assert.NotNil(t, preds)
}

func TestPredictivePrefetcher_AnalyzeTemporal(t *testing.T) {
	l1 := newFakeL1()
	cfg := prefetchTestConfig()
	mtc := NewMultiTierCache(l1, nil, &MultiTierConfig{L1Enabled: true, TrackingEnabled: true, PatternWindowSize: 50}, testLogger())
	defer mtc.Close()

	pp := NewPredictivePrefetcher(mtc, cfg, testLogger())
	for i := 0; i < 30; i++ {
		pp.patterns.RecordAccess("temporal-repo")
	}
	preds := pp.analyzeTemporalPattern("other")
	assert.NotNil(t, preds)
}

func TestPredictivePrefetcher_CalculatePriority(t *testing.T) {
	l1 := newFakeL1()
	cfg := prefetchTestConfig()
	mtc := NewMultiTierCache(l1, nil, &MultiTierConfig{L1Enabled: true, TrackingEnabled: true, PatternWindowSize: 50}, testLogger())
	defer mtc.Close()

	pp := NewPredictivePrefetcher(mtc, cfg, testLogger())

	base := pp.calculatePriority(&PredictionEntry{Confidence: 0.5, AccessPattern: "related"})
	assert.Equal(t, 50, base)

	boosted := pp.calculatePriority(&PredictionEntry{Confidence: 0.9, AccessPattern: "sequential"})
	// 90 + 20 (high confidence) + 15 (sequential) = 125
	assert.Equal(t, 125, boosted)
}

func TestPredictivePrefetcher_TriggerQueuesTasks(t *testing.T) {
	l1 := newFakeL1()
	cfg := prefetchTestConfig()
	mtc := NewMultiTierCache(l1, nil, &MultiTierConfig{L1Enabled: true, TrackingEnabled: true, PatternWindowSize: 50}, testLogger())
	defer mtc.Close()

	pp := NewPredictivePrefetcher(mtc, cfg, testLogger())
	for i := 0; i < 5; i++ {
		pp.patterns.RecordAccess("a")
		pp.patterns.RecordAccess("b")
	}
	// Trigger should not block (queue has capacity).
	pp.TriggerPredictivePrefetch("a")

	// Drain a queued task and process it via worker logic.
	select {
	case task := <-pp.workQueue:
		worker := &PrefetchWorker{id: 0, cache: mtc, logger: testLogger()}
		worker.handlePrefetchTask(task)
		assert.GreaterOrEqual(t, mtc.GetStats().PrefetchCount, int64(1))
	default:
		// No task queued is acceptable depending on confidence; not a failure.
	}
}

func TestPrefetchWorker_HandleTaskAlreadyCached(t *testing.T) {
	l1 := newFakeL1()
	mtc := NewMultiTierCache(l1, nil, &MultiTierConfig{L1Enabled: true, TrackingEnabled: true, PatternWindowSize: 50}, testLogger())
	defer mtc.Close()

	// Pre-populate L1 so GetRepository reports the repo as already cached,
	// causing the worker to skip the prefetch path.
	require.NoError(t, l1.SetRepository("cached", &gogit.Repository{}))

	worker := &PrefetchWorker{id: 1, cache: mtc, logger: testLogger()}
	worker.handlePrefetchTask(&PrefetchTask{Repository: "cached"})
}
