package performance

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewPerformanceConfig_Defaults(t *testing.T) {
	cfg := NewPerformanceConfig()
	assert.False(t, cfg.EnableCommitGraphOptimization)
	assert.False(t, cfg.EnablePerformanceMonitoring)
	assert.False(t, cfg.EnableCompetitiveBenchmarking)
	assert.False(t, cfg.EnableDetailedProfiling)
	assert.False(t, cfg.EnableObjectCache)
	assert.False(t, cfg.EnableFileDescriptorPool)
}

func TestNewPerformanceConfig_FromEnv(t *testing.T) {
	t.Setenv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_COMMIT_GRAPH_OPTIMIZATION", "true")
	t.Setenv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_OBJECT_CACHE", "1")
	t.Setenv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_FD_POOL", "  true  ")

	cfg := NewPerformanceConfig()
	assert.True(t, cfg.EnableCommitGraphOptimization)
	assert.True(t, cfg.EnableObjectCache)
	assert.True(t, cfg.EnableFileDescriptorPool)
}

func TestGetBoolEnv(t *testing.T) {
	tests := []struct {
		name     string
		set      bool
		value    string
		def      bool
		expected bool
	}{
		{"unset returns default true", false, "", true, true},
		{"unset returns default false", false, "", false, false},
		{"empty returns default", true, "", true, true},
		{"whitespace returns default", true, "   ", true, true},
		{"valid true", true, "true", false, true},
		{"valid false", true, "false", true, false},
		{"numeric 1", true, "1", false, true},
		{"numeric 0", true, "0", true, false},
		{"invalid returns default", true, "notabool", true, true},
		{"invalid returns default false", true, "xyz", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "COLLABOPS_TEST_BOOL_ENV"
			if tt.set {
				t.Setenv(key, tt.value)
			}
			got := getBoolEnv(key, tt.def)
			assert.Equal(t, tt.expected, got)
		})
	}
}
