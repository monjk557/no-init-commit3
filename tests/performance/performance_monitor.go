package performance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"go.uber.org/zap"
)

// PerformanceMonitor provides comprehensive system performance monitoring
// for Phase 2 testing and validation
type PerformanceMonitor struct {
	logger         *zap.Logger
	startTime      time.Time
	measurements   map[string]*MetricSeries
	mutex          sync.RWMutex
	samplingTicker *time.Ticker
	ctx            context.Context
	cancel         context.CancelFunc
}

// MetricSeries stores time-series performance data
type MetricSeries struct {
	Name       string       `json:"name"`
	Unit       string       `json:"unit"`
	Values     []float64    `json:"values"`
	Timestamps []time.Time  `json:"timestamps"`
	Statistics *MetricStats `json:"statistics"`
}

// MetricStats provides statistical analysis of performance metrics
type MetricStats struct {
	Count       int64     `json:"count"`
	Min         float64   `json:"min"`
	Max         float64   `json:"max"`
	Mean        float64   `json:"mean"`
	Median      float64   `json:"median"`
	P95         float64   `json:"p95"`
	P99         float64   `json:"p99"`
	StdDev      float64   `json:"std_dev"`
	LastUpdated time.Time `json:"last_updated"`
}

// PerformanceSnapshot represents a point-in-time system performance snapshot
type PerformanceSnapshot struct {
	Timestamp       time.Time `json:"timestamp"`
	CPUUsagePercent float64   `json:"cpu_usage_percent"`
	MemoryUsageMB   float64   `json:"memory_usage_mb"`
	GoroutineCount  int       `json:"goroutine_count"`
	GCPauseTimeMS   float64   `json:"gc_pause_time_ms"`
	AllocatedMB     float64   `json:"allocated_mb"`
	HeapSizeMB      float64   `json:"heap_size_mb"`
}

// NewPerformanceMonitor creates a new performance monitoring instance
func NewPerformanceMonitor(logger *zap.Logger) *PerformanceMonitor {
	ctx, cancel := context.WithCancel(context.Background())

	monitor := &PerformanceMonitor{
		logger:       logger,
		startTime:    time.Now(),
		measurements: make(map[string]*MetricSeries),
		ctx:          ctx,
		cancel:       cancel,
	}

	// Start background monitoring
	monitor.startBackgroundMonitoring()

	return monitor
}

// Record adds a performance measurement
func (pm *PerformanceMonitor) Record(name string, value float64, unit string) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	series, exists := pm.measurements[name]
	if !exists {
		series = &MetricSeries{
			Name:       name,
			Unit:       unit,
			Values:     make([]float64, 0),
			Timestamps: make([]time.Time, 0),
			Statistics: &MetricStats{},
		}
		pm.measurements[name] = series
	}

	// Add new measurement
	series.Values = append(series.Values, value)
	series.Timestamps = append(series.Timestamps, time.Now())

	// Update statistics
	pm.updateStatistics(series)

	pm.logger.Debug("Performance metric recorded",
		zap.String("metric", name),
		zap.Float64("value", value),
		zap.String("unit", unit))
}

// RecordDuration records a time duration measurement
func (pm *PerformanceMonitor) RecordDuration(name string, duration time.Duration) {
	pm.Record(name, float64(duration.Nanoseconds())/1e6, "ms") // Convert to milliseconds
}

// RecordLatency records request latency with automatic statistics
func (pm *PerformanceMonitor) RecordLatency(operation string, startTime time.Time) {
	latency := time.Since(startTime)
	pm.RecordDuration(fmt.Sprintf("latency_%s", operation), latency)
}

// StartOperation returns a function to record operation completion time
func (pm *PerformanceMonitor) StartOperation(operation string) func() {
	startTime := time.Now()
	return func() {
		pm.RecordLatency(operation, startTime)
	}
}

// GetSnapshot returns current system performance snapshot
func (pm *PerformanceMonitor) GetSnapshot() *PerformanceSnapshot {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return &PerformanceSnapshot{
		Timestamp:       time.Now(),
		CPUUsagePercent: pm.getCPUUsage(),
		MemoryUsageMB:   float64(memStats.Sys) / (1024 * 1024),
		GoroutineCount:  runtime.NumGoroutine(),
		GCPauseTimeMS:   float64(memStats.PauseNs[(memStats.NumGC+255)%256]) / 1e6,
		AllocatedMB:     float64(memStats.Alloc) / (1024 * 1024),
		HeapSizeMB:      float64(memStats.HeapSys) / (1024 * 1024),
	}
}

// GetMetrics returns all collected performance metrics
func (pm *PerformanceMonitor) GetMetrics() map[string]*MetricSeries {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	// Create a deep copy to avoid race conditions
	result := make(map[string]*MetricSeries)
	for name, series := range pm.measurements {
		// Create copy of the series
		seriesCopy := &MetricSeries{
			Name:       series.Name,
			Unit:       series.Unit,
			Values:     make([]float64, len(series.Values)),
			Timestamps: make([]time.Time, len(series.Timestamps)),
			Statistics: &MetricStats{},
		}

		copy(seriesCopy.Values, series.Values)
		copy(seriesCopy.Timestamps, series.Timestamps)

		if series.Statistics != nil {
			*seriesCopy.Statistics = *series.Statistics
		}

		result[name] = seriesCopy
	}

	return result
}

// ExportMetrics exports all metrics to JSON file
func (pm *PerformanceMonitor) ExportMetrics(filepath string) error {
	metrics := pm.GetMetrics()

	data, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metrics: %w", err)
	}

	if err := os.WriteFile(filepath, data, 0644); err != nil {
		return fmt.Errorf("failed to write metrics file: %w", err)
	}

	pm.logger.Info("Performance metrics exported",
		zap.String("filepath", filepath),
		zap.Int("metric_count", len(metrics)))

	return nil
}

// GenerateReport creates a comprehensive performance report
func (pm *PerformanceMonitor) GenerateReport() *PerformanceReport {
	metrics := pm.GetMetrics()
	snapshot := pm.GetSnapshot()

	report := &PerformanceReport{
		GeneratedAt:     time.Now(),
		MonitoringStart: pm.startTime,
		Duration:        time.Since(pm.startTime),
		SystemSnapshot:  snapshot,
		Metrics:         metrics,
		Summary:         pm.generateSummary(metrics),
	}

	return report
}

// ValidatePerformanceTargets checks if current performance meets Phase 2 targets
func (pm *PerformanceMonitor) ValidatePerformanceTargets() *TargetValidationResult {
	metrics := pm.GetMetrics()

	result := &TargetValidationResult{
		ValidationTime: time.Now(),
		Targets:        make(map[string]*TargetValidation),
	}

	// Define Phase 2 performance targets
	targets := map[string]TargetConfig{
		"latency_medium_repo": {
			MaxValue:    150.0, // 150ms target
			Unit:        "ms",
			Description: "Medium repository response time (74% improvement target)",
		},
		"latency_large_repo": {
			MaxValue:    300.0, // 300ms target
			Unit:        "ms",
			Description: "Large repository response time (87% improvement target)",
		},
		"latency_concurrent_users": {
			MaxValue:    300.0, // 300ms target
			Unit:        "ms",
			Description: "Concurrent user response time (64% improvement target)",
		},
		"cache_hit_rate": {
			MinValue:    98.0, // 98% target
			Unit:        "%",
			Description: "Multi-tier cache hit rate improvement target",
		},
	}

	// Validate each target
	for targetName, config := range targets {
		validation := pm.validateTarget(metrics, targetName, config)
		result.Targets[targetName] = validation

		if !validation.Passed {
			result.OverallPassed = false
		}
	}

	// If no failures detected, overall validation passes
	if result.OverallPassed {
		result.OverallPassed = true
	}

	pm.logger.Info("Performance target validation completed",
		zap.Bool("overall_passed", result.OverallPassed),
		zap.Int("targets_validated", len(result.Targets)))

	return result
}

// Stop stops the performance monitoring
func (pm *PerformanceMonitor) Stop() {
	if pm.cancel != nil {
		pm.cancel()
	}

	if pm.samplingTicker != nil {
		pm.samplingTicker.Stop()
	}

	pm.logger.Info("Performance monitoring stopped",
		zap.Duration("total_duration", time.Since(pm.startTime)),
		zap.Int("total_metrics", len(pm.measurements)))
}

// Internal methods

func (pm *PerformanceMonitor) startBackgroundMonitoring() {
	pm.samplingTicker = time.NewTicker(1 * time.Second)

	go func() {
		for {
			select {
			case <-pm.ctx.Done():
				return
			case <-pm.samplingTicker.C:
				pm.sampleSystemMetrics()
			}
		}
	}()
}

func (pm *PerformanceMonitor) sampleSystemMetrics() {
	snapshot := pm.GetSnapshot()

	pm.Record("system_cpu_usage", snapshot.CPUUsagePercent, "%")
	pm.Record("system_memory_usage", snapshot.MemoryUsageMB, "MB")
	pm.Record("system_goroutines", float64(snapshot.GoroutineCount), "count")
	pm.Record("system_gc_pause", snapshot.GCPauseTimeMS, "ms")
	pm.Record("system_heap_size", snapshot.HeapSizeMB, "MB")
}

func (pm *PerformanceMonitor) getCPUUsage() float64 {
	// Simplified CPU usage calculation
	// In a production system, this would use more sophisticated methods
	return float64(runtime.NumGoroutine()) / 100.0 * runtime.GOMAXPROCS(0)
}

func (pm *PerformanceMonitor) updateStatistics(series *MetricSeries) {
	if len(series.Values) == 0 {
		return
	}

	// Create sorted copy for percentile calculations
	sortedValues := make([]float64, len(series.Values))
	copy(sortedValues, series.Values)

	// Simple sort (sufficient for test data sizes)
	for i := 0; i < len(sortedValues); i++ {
		for j := i + 1; j < len(sortedValues); j++ {
			if sortedValues[i] > sortedValues[j] {
				sortedValues[i], sortedValues[j] = sortedValues[j], sortedValues[i]
			}
		}
	}

	// Calculate statistics
	stats := series.Statistics
	stats.Count = int64(len(series.Values))
	stats.Min = sortedValues[0]
	stats.Max = sortedValues[len(sortedValues)-1]
	stats.LastUpdated = time.Now()

	// Calculate mean
	sum := 0.0
	for _, v := range series.Values {
		sum += v
	}
	stats.Mean = sum / float64(len(series.Values))

	// Calculate median
	if len(sortedValues)%2 == 0 {
		stats.Median = (sortedValues[len(sortedValues)/2-1] + sortedValues[len(sortedValues)/2]) / 2
	} else {
		stats.Median = sortedValues[len(sortedValues)/2]
	}

	// Calculate percentiles
	stats.P95 = sortedValues[int(float64(len(sortedValues))*0.95)]
	stats.P99 = sortedValues[int(float64(len(sortedValues))*0.99)]

	// Calculate standard deviation
	sumSquaredDiffs := 0.0
	for _, v := range series.Values {
		diff := v - stats.Mean
		sumSquaredDiffs += diff * diff
	}
	stats.StdDev = sumSquaredDiffs / float64(len(series.Values))
	if stats.StdDev > 0 {
		stats.StdDev = float64(int(stats.StdDev*100)) / 100 // Round to 2 decimal places
	}
}

func (pm *PerformanceMonitor) validateTarget(metrics map[string]*MetricSeries, targetName string, config TargetConfig) *TargetValidation {
	validation := &TargetValidation{
		TargetName:  targetName,
		Description: config.Description,
		Unit:        config.Unit,
	}

	series, exists := metrics[targetName]
	if !exists {
		validation.Passed = false
		validation.Message = "Metric not found"
		return validation
	}

	if series.Statistics == nil || series.Statistics.Count == 0 {
		validation.Passed = false
		validation.Message = "No data available"
		return validation
	}

	stats := series.Statistics
	validation.ActualValue = stats.Mean

	// Check against maximum value constraint
	if config.MaxValue > 0 && stats.Mean > config.MaxValue {
		validation.Passed = false
		validation.Message = fmt.Sprintf("Mean %.2f %s exceeds maximum %.2f %s",
			stats.Mean, config.Unit, config.MaxValue, config.Unit)
		return validation
	}

	// Check against minimum value constraint
	if config.MinValue > 0 && stats.Mean < config.MinValue {
		validation.Passed = false
		validation.Message = fmt.Sprintf("Mean %.2f %s below minimum %.2f %s",
			stats.Mean, config.Unit, config.MinValue, config.Unit)
		return validation
	}

	// Target validation passed
	validation.Passed = true
	validation.Message = "Target achieved"

	return validation
}

func (pm *PerformanceMonitor) generateSummary(metrics map[string]*MetricSeries) map[string]interface{} {
	summary := make(map[string]interface{})

	// Count metrics by category
	latencyCount := 0
	systemCount := 0
	cacheCount := 0

	for name := range metrics {
		if name[:7] == "latency" {
			latencyCount++
		} else if name[:6] == "system" {
			systemCount++
		} else if name[:5] == "cache" {
			cacheCount++
		}
	}

	summary["total_metrics"] = len(metrics)
	summary["latency_metrics"] = latencyCount
	summary["system_metrics"] = systemCount
	summary["cache_metrics"] = cacheCount
	summary["monitoring_duration"] = time.Since(pm.startTime).String()

	return summary
}

// Supporting types

type PerformanceReport struct {
	GeneratedAt     time.Time                `json:"generated_at"`
	MonitoringStart time.Time                `json:"monitoring_start"`
	Duration        time.Duration            `json:"duration"`
	SystemSnapshot  *PerformanceSnapshot     `json:"system_snapshot"`
	Metrics         map[string]*MetricSeries `json:"metrics"`
	Summary         map[string]interface{}   `json:"summary"`
}

type TargetValidationResult struct {
	ValidationTime time.Time                    `json:"validation_time"`
	OverallPassed  bool                         `json:"overall_passed"`
	Targets        map[string]*TargetValidation `json:"targets"`
}

type TargetValidation struct {
	TargetName  string  `json:"target_name"`
	Description string  `json:"description"`
	Unit        string  `json:"unit"`
	ActualValue float64 `json:"actual_value"`
	Passed      bool    `json:"passed"`
	Message     string  `json:"message"`
}

type TargetConfig struct {
	MaxValue    float64
	MinValue    float64
	Unit        string
	Description string
}
