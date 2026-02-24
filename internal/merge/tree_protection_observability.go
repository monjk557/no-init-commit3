package merge

import (
	"time"

	"go.uber.org/zap"
)

// TreeProtectionObservability provides comprehensive monitoring and metrics for tree protection operations
type TreeProtectionObservability struct {
	logger             *zap.Logger
	protectionMetrics  *ProtectionMetrics
	alertThresholds    *AlertThresholds
	performanceTracker *PerformanceTracker
}

// ProtectionMetrics tracks protection system performance and effectiveness
type ProtectionMetrics struct {
	TotalProtectionAttempts     int64
	SuccessfulProtections       int64
	FailedProtections           int64
	EnvironmentFilesSaved       int64
	FallbacksTriggered          int64
	ManualInterventionsRequired int64
	AverageProtectionLatency    time.Duration
	LastProtectionTime          time.Time
}

// AlertThresholds defines when to trigger alerts
type AlertThresholds struct {
	MaxFailureRate          float64 // Alert if failure rate exceeds this
	MaxProtectionLatency    time.Duration
	MaxEnvironmentLossCount int
	AlertCooldown           time.Duration
	LastAlertTime           time.Time
}

// PerformanceTracker monitors system performance during protection operations
type PerformanceTracker struct {
	OperationLatencies []time.Duration
	MemoryUsage        []int64
	ErrorRates         []float64
	ThroughputMetrics  []int
}

// ProtectionEvent represents a single protection operation event
type ProtectionEvent struct {
	Timestamp      time.Time
	EventType      ProtectionEventType
	OperationID    string
	Duration       time.Duration
	FilesProtected []string
	Strategy       string
	Success        bool
	ErrorMessage   string
	Context        map[string]interface{}
}

// ProtectionEventType categorizes different types of protection events
type ProtectionEventType int

const (
	ProtectionEventFallbackTriggered ProtectionEventType = iota
	ProtectionEventFilesPreserved
	ProtectionEventManualRequired
	ProtectionEventSystemError
	ProtectionEventPerformanceAlert
	ProtectionEventConfigurationChange
)

// NewTreeProtectionObservability creates a new observability system
func NewTreeProtectionObservability(logger *zap.Logger) *TreeProtectionObservability {
	return &TreeProtectionObservability{
		logger: logger,
		protectionMetrics: &ProtectionMetrics{
			LastProtectionTime: time.Now(),
		},
		alertThresholds: &AlertThresholds{
			MaxFailureRate:          0.05,             // 5% failure rate triggers alert
			MaxProtectionLatency:    5 * time.Second,  // 5s latency triggers alert
			MaxEnvironmentLossCount: 1,                // Any environment loss triggers alert
			AlertCooldown:           15 * time.Minute, // 15min between alerts
		},
		performanceTracker: &PerformanceTracker{
			OperationLatencies: make([]time.Duration, 0),
			MemoryUsage:        make([]int64, 0),
			ErrorRates:         make([]float64, 0),
			ThroughputMetrics:  make([]int, 0),
		},
	}
}

// RecordFallback records a fallback operation for monitoring
func (o *TreeProtectionObservability) RecordFallback(
	result string,
	context FallbackContext,
) {
	start := time.Now()

	o.protectionMetrics.TotalProtectionAttempts++
	o.protectionMetrics.FallbacksTriggered++

	event := ProtectionEvent{
		Timestamp:      start,
		EventType:      ProtectionEventFallbackTriggered,
		OperationID:    generateOperationID(),
		FilesProtected: context.EnvironmentFiles,
		Strategy:       string(context.Strategy),
		Context: map[string]interface{}{
			"is_squash":       context.IsSquash,
			"conflict_count":  context.ConflictCount,
			"auto_resolvable": context.AutoResolvable,
			"result":          result,
		},
	}

	switch result {
	case "safe":
		o.protectionMetrics.SuccessfulProtections++
		event.Success = true
		o.logger.Info("Fallback completed safely - no protection needed",
			zap.String("operation_id", event.OperationID),
			zap.Bool("is_squash", context.IsSquash))

	case "protected":
		o.protectionMetrics.SuccessfulProtections++
		o.protectionMetrics.EnvironmentFilesSaved += int64(len(context.EnvironmentFiles))
		event.Success = true
		event.EventType = ProtectionEventFilesPreserved
		o.logger.Info("Successfully protected environment files during fallback",
			zap.String("operation_id", event.OperationID),
			zap.Strings("protected_files", context.EnvironmentFiles))

	case "failed_protection":
		o.protectionMetrics.FailedProtections++
		event.Success = false
		event.EventType = ProtectionEventSystemError
		event.ErrorMessage = "Protection system failed to preserve files"
		o.logger.Error("Failed to protect files during fallback",
			zap.String("operation_id", event.OperationID),
			zap.Strings("lost_files", context.EnvironmentFiles))

		// This is critical - trigger immediate alert
		o.triggerEnvironmentLossAlert(event, context.EnvironmentFiles)
	}

	// Update performance metrics
	latency := time.Since(start)
	o.updatePerformanceMetrics(latency, event.Success)

	// Check for alert conditions
	o.checkAlertConditions(event)

	o.protectionMetrics.LastProtectionTime = time.Now()
}

// RecordProtectionAttempt records any protection attempt with detailed context
func (o *TreeProtectionObservability) RecordProtectionAttempt(
	operationType string,
	filesInvolved []string,
	duration time.Duration,
	success bool,
	errorMsg string,
) {
	event := ProtectionEvent{
		Timestamp:      time.Now(),
		EventType:      ProtectionEventFilesPreserved,
		OperationID:    generateOperationID(),
		Duration:       duration,
		FilesProtected: filesInvolved,
		Success:        success,
		ErrorMessage:   errorMsg,
		Context: map[string]interface{}{
			"operation_type": operationType,
		},
	}

	o.protectionMetrics.TotalProtectionAttempts++
	if success {
		o.protectionMetrics.SuccessfulProtections++
	} else {
		o.protectionMetrics.FailedProtections++
		event.EventType = ProtectionEventSystemError
	}

	o.updatePerformanceMetrics(duration, success)
	o.checkAlertConditions(event)

	o.logger.Info("Protection attempt recorded",
		zap.String("operation_id", event.OperationID),
		zap.String("type", operationType),
		zap.Bool("success", success),
		zap.Duration("duration", duration))
}

// updatePerformanceMetrics updates performance tracking data
func (o *TreeProtectionObservability) updatePerformanceMetrics(
	latency time.Duration,
	success bool,
) {
	// Add latency to tracking
	o.performanceTracker.OperationLatencies = append(
		o.performanceTracker.OperationLatencies, latency)

	// Calculate current error rate
	totalOps := o.protectionMetrics.TotalProtectionAttempts
	if totalOps > 0 {
		errorRate := float64(o.protectionMetrics.FailedProtections) / float64(totalOps)
		o.performanceTracker.ErrorRates = append(
			o.performanceTracker.ErrorRates, errorRate)
	}

	// Update average latency
	if len(o.performanceTracker.OperationLatencies) > 0 {
		var total time.Duration
		for _, lat := range o.performanceTracker.OperationLatencies {
			total += lat
		}
		o.protectionMetrics.AverageProtectionLatency =
			total / time.Duration(len(o.performanceTracker.OperationLatencies))
	}

	// Limit tracking data size (keep last 1000 entries)
	maxEntries := 1000
	if len(o.performanceTracker.OperationLatencies) > maxEntries {
		start := len(o.performanceTracker.OperationLatencies) - maxEntries
		o.performanceTracker.OperationLatencies =
			o.performanceTracker.OperationLatencies[start:]
		o.performanceTracker.ErrorRates =
			o.performanceTracker.ErrorRates[start:]
	}
}

// checkAlertConditions checks if any alert thresholds have been exceeded
func (o *TreeProtectionObservability) checkAlertConditions(event ProtectionEvent) {
	now := time.Now()

	// Check if we're in cooldown period
	if now.Sub(o.alertThresholds.LastAlertTime) < o.alertThresholds.AlertCooldown {
		return
	}

	// Check failure rate threshold
	if o.protectionMetrics.TotalProtectionAttempts > 10 { // Need minimum sample size
		failureRate := float64(o.protectionMetrics.FailedProtections) /
			float64(o.protectionMetrics.TotalProtectionAttempts)

		if failureRate > o.alertThresholds.MaxFailureRate {
			o.triggerFailureRateAlert(failureRate)
		}
	}

	// Check latency threshold
	if event.Duration > o.alertThresholds.MaxProtectionLatency {
		o.triggerLatencyAlert(event.Duration)
	}

	// Environment loss is handled separately as it's critical
}

// triggerEnvironmentLossAlert triggers an immediate alert for environment file loss
func (o *TreeProtectionObservability) triggerEnvironmentLossAlert(
	event ProtectionEvent,
	lostFiles []string,
) {
	o.logger.Error("CRITICAL ALERT: Environment files lost during merge operation",
		zap.String("operation_id", event.OperationID),
		zap.Strings("lost_files", lostFiles),
		zap.Time("timestamp", event.Timestamp),
		zap.String("alert_type", "ENVIRONMENT_LOSS"))

	o.alertThresholds.LastAlertTime = time.Now()
}

// triggerFailureRateAlert triggers an alert when failure rate is too high
func (o *TreeProtectionObservability) triggerFailureRateAlert(failureRate float64) {
	o.logger.Warn("Protection system failure rate exceeds threshold",
		zap.Float64("current_failure_rate", failureRate),
		zap.Float64("threshold", o.alertThresholds.MaxFailureRate),
		zap.String("alert_type", "HIGH_FAILURE_RATE"))

	o.alertThresholds.LastAlertTime = time.Now()
}

// triggerLatencyAlert triggers an alert when operations are too slow
func (o *TreeProtectionObservability) triggerLatencyAlert(latency time.Duration) {
	o.logger.Warn("Protection operation latency exceeds threshold",
		zap.Duration("current_latency", latency),
		zap.Duration("threshold", o.alertThresholds.MaxProtectionLatency),
		zap.String("alert_type", "HIGH_LATENCY"))

	o.alertThresholds.LastAlertTime = time.Now()
}

// GetMetrics returns current protection system metrics
func (o *TreeProtectionObservability) GetMetrics() map[string]interface{} {
	totalAttempts := o.protectionMetrics.TotalProtectionAttempts
	successRate := float64(0)
	if totalAttempts > 0 {
		successRate = float64(o.protectionMetrics.SuccessfulProtections) / float64(totalAttempts)
	}

	return map[string]interface{}{
		"total_protection_attempts":     totalAttempts,
		"successful_protections":        o.protectionMetrics.SuccessfulProtections,
		"failed_protections":            o.protectionMetrics.FailedProtections,
		"success_rate":                  successRate,
		"environment_files_saved":       o.protectionMetrics.EnvironmentFilesSaved,
		"fallbacks_triggered":           o.protectionMetrics.FallbacksTriggered,
		"manual_interventions_required": o.protectionMetrics.ManualInterventionsRequired,
		"average_protection_latency_ms": o.protectionMetrics.AverageProtectionLatency.Milliseconds(),
		"last_protection_time":          o.protectionMetrics.LastProtectionTime,
		"alert_thresholds": map[string]interface{}{
			"max_failure_rate":    o.alertThresholds.MaxFailureRate,
			"max_latency_seconds": o.alertThresholds.MaxProtectionLatency.Seconds(),
		},
	}
}

// GetPerformanceStats returns detailed performance statistics
func (o *TreeProtectionObservability) GetPerformanceStats() map[string]interface{} {
	stats := map[string]interface{}{
		"operation_count": len(o.performanceTracker.OperationLatencies),
	}

	if len(o.performanceTracker.OperationLatencies) > 0 {
		// Calculate latency percentiles
		latencies := make([]time.Duration, len(o.performanceTracker.OperationLatencies))
		copy(latencies, o.performanceTracker.OperationLatencies)

		stats["latency_p50"] = calculatePercentile(latencies, 0.5).Milliseconds()
		stats["latency_p90"] = calculatePercentile(latencies, 0.9).Milliseconds()
		stats["latency_p99"] = calculatePercentile(latencies, 0.99).Milliseconds()

		// Current error rate
		if len(o.performanceTracker.ErrorRates) > 0 {
			stats["current_error_rate"] =
				o.performanceTracker.ErrorRates[len(o.performanceTracker.ErrorRates)-1]
		}
	}

	return stats
}

// ResetMetrics resets all metrics (useful for testing or periodic resets)
func (o *TreeProtectionObservability) ResetMetrics() {
	o.protectionMetrics = &ProtectionMetrics{
		LastProtectionTime: time.Now(),
	}
	o.performanceTracker = &PerformanceTracker{
		OperationLatencies: make([]time.Duration, 0),
		MemoryUsage:        make([]int64, 0),
		ErrorRates:         make([]float64, 0),
		ThroughputMetrics:  make([]int, 0),
	}

	o.logger.Info("Protection system metrics reset")
}

// Helper functions

// generateOperationID creates a unique ID for tracking operations
func generateOperationID() string {
	return time.Now().Format("20060102-150405") + "-" + generateRandomString(6)
}

// generateRandomString creates a random string of specified length
func generateRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[time.Now().UnixNano()%int64(len(charset))]
	}
	return string(result)
}

// calculatePercentile calculates the specified percentile of a duration slice
func calculatePercentile(durations []time.Duration, percentile float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}

	// Simple percentile calculation (would use sort in production)
	index := int(float64(len(durations)) * percentile)
	if index >= len(durations) {
		index = len(durations) - 1
	}
	return durations[index]
}
