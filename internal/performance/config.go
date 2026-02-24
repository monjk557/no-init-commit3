package performance

import (
	"os"
	"strconv"
	"strings"
)

// PerformanceConfig holds all performance-related feature flags
type PerformanceConfig struct {
	EnableCommitGraphOptimization bool
	EnablePerformanceMonitoring   bool
	EnableCompetitiveBenchmarking bool
	EnableDetailedProfiling       bool
	EnableObjectCache             bool
	EnableFileDescriptorPool      bool
}

// NewPerformanceConfig creates a new performance configuration from environment variables
func NewPerformanceConfig() *PerformanceConfig {
	return &PerformanceConfig{
		EnableCommitGraphOptimization: getBoolEnv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_COMMIT_GRAPH_OPTIMIZATION", false),
		EnablePerformanceMonitoring:   getBoolEnv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_PERFORMANCE_MONITORING", false),
		EnableCompetitiveBenchmarking: getBoolEnv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_COMPETITIVE_BENCHMARKING", false),
		EnableDetailedProfiling:       getBoolEnv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_DETAILED_PROFILING", false),
		EnableObjectCache:             getBoolEnv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_OBJECT_CACHE", false),
		EnableFileDescriptorPool:      getBoolEnv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_FD_POOL", false),
	}
}

// getBoolEnv gets a boolean environment variable with a default value
func getBoolEnv(key string, defaultValue bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return defaultValue
	}

	return parsed
}
