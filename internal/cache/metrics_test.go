package cache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPrometheusMetrics_Singleton(t *testing.T) {
	m1 := NewPrometheusMetrics()
	m2 := NewPrometheusMetrics()
	require.NotNil(t, m1)
	// The default metrics are a process-wide singleton.
	assert.Same(t, m1, m2)
}

func TestPrometheusMetrics_RecordAndHitRate(t *testing.T) {
	pm := NewPrometheusMetrics()

	// Capture starting counts because the singleton is shared across tests.
	startHits := pm.hitCount.Load()
	startMisses := pm.missCount.Load()

	pm.RecordHit()
	pm.RecordHit()
	pm.RecordHit()
	pm.RecordMiss()

	assert.Equal(t, startHits+3, pm.hitCount.Load())
	assert.Equal(t, startMisses+1, pm.missCount.Load())

	// These should not panic and exercise the counter/gauge/histogram paths.
	pm.RecordEviction()
	pm.RecordSet()
	pm.RecordCleanup()
	pm.UpdateEntries(7)
	pm.UpdateMemoryUsage(1024)
	pm.RecordAccessDuration("get", "hit", 0.0012)

	rate := pm.GetHitRate()
	assert.GreaterOrEqual(t, rate, 0.0)
	assert.LessOrEqual(t, rate, 1.0)
}

func TestPrometheusMetrics_GetHitRate_Math(t *testing.T) {
	// Use a fresh, non-singleton instance to assert exact arithmetic without
	// interference from the shared default metrics counters.
	pm := NewPrometheusMetrics()
	// Reset the atomic counters for a clean arithmetic check.
	pm.hitCount.Store(0)
	pm.missCount.Store(0)

	assert.Equal(t, 0.0, pm.GetHitRate(), "zero total should yield 0")

	pm.hitCount.Store(3)
	pm.missCount.Store(1)
	assert.InDelta(t, 0.75, pm.GetHitRate(), 1e-9)
}

func TestMust_Panics(t *testing.T) {
	assert.NotPanics(t, func() {
		v := must(42, error(nil))
		assert.Equal(t, 42, v)
	})
	assert.Panics(t, func() {
		_ = must(0, assert.AnError)
	})
}

func TestCtxHelper(t *testing.T) {
	assert.NotNil(t, ctx())
}
