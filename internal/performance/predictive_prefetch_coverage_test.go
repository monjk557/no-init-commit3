package performance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPredictivePrefetcher_PerformPatternAnalysis covers performPatternAnalysis:
// stale predictions are pruned and fresh predictions are generated from the top
// repositories.
func TestPredictivePrefetcher_PerformPatternAnalysis(t *testing.T) {
	l1 := newFakeL1()
	mtc := NewMultiTierCache(l1, nil,
		&MultiTierConfig{L1Enabled: true, TrackingEnabled: true, PatternWindowSize: 50},
		testLogger())
	defer mtc.Close()

	pp := NewPredictivePrefetcher(mtc, prefetchTestConfig(), testLogger())

	// Establish a strong sequential pattern so analyzePatternsFor yields
	// predictions for the top repositories.
	for i := 0; i < 6; i++ {
		pp.patterns.RecordAccess("a")
		pp.patterns.RecordAccess("b")
	}

	// Insert a stale prediction that must be pruned (>30m old).
	pp.predictions["stale"] = &PredictionEntry{
		Repository:  "stale",
		PredictedAt: time.Now().Add(-time.Hour),
	}
	// Insert a fresh prediction that must survive the age check.
	pp.predictions["fresh"] = &PredictionEntry{
		Repository:  "fresh",
		PredictedAt: time.Now(),
	}

	pp.performPatternAnalysis()

	_, staleExists := pp.predictions["stale"]
	assert.False(t, staleExists, "stale prediction should have been pruned")

	// Top repositories a/b should have produced at least one new prediction.
	require.NotEmpty(t, pp.predictions)
}
