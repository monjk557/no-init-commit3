package performance

import (
	"encoding/json"
	"io"
	"time"
)

// StreamingJSONOptimizer provides the core zero-copy JSON optimization
// Research target: Eliminate 3x marshal/unmarshal cycles in response middleware
type StreamingJSONOptimizer struct{}

// NewStreamingJSONOptimizer creates a new streaming JSON optimizer
func NewStreamingJSONOptimizer() *StreamingJSONOptimizer {
	return &StreamingJSONOptimizer{}
}

// WriteOptimizedSuccessResponse eliminates the double marshaling bottleneck
// TARGET: Replace the 3-step marshal/unmarshal/marshal cycle in api/response.go
func (sjo *StreamingJSONOptimizer) WriteOptimizedSuccessResponse(w io.Writer, payload interface{}, message string, meta interface{}) error {
	// Use a single direct JSON writer to eliminate intermediate allocations
	encoder := json.NewEncoder(w)

	// Build the response structure directly without intermediate marshaling
	response := map[string]interface{}{
		"success":   true,
		"message":   message,
		"timestamp": time.Now().Format(time.RFC3339),
		"meta":      meta,
	}

	// Handle payload efficiently
	if payload != nil {
		// Check if payload is a map that needs flattening (existing behavior)
		if payloadMap, ok := payload.(map[string]interface{}); ok {
			// Flatten fields directly - ZERO COPY
			for k, v := range payloadMap {
				response[k] = v
			}
		} else {
			// Wrap in data field
			response["data"] = payload
		}
	}

	// Single marshal operation - NO DOUBLE MARSHALING
	return encoder.Encode(response)
}

// WriteOptimizedPaginatedResponse provides efficient paginated response writing
func (sjo *StreamingJSONOptimizer) WriteOptimizedPaginatedResponse(w io.Writer, payload interface{}, pagination interface{}, message string) error {
	encoder := json.NewEncoder(w)

	response := map[string]interface{}{
		"success":    true,
		"message":    message,
		"timestamp":  time.Now().Format(time.RFC3339),
		"data":       payload,
		"pagination": pagination,
	}

	// Single marshal - eliminates intermediate buffering
	return encoder.Encode(response)
}

// StreamingResponseComparison provides side-by-side comparison utilities
type StreamingResponseComparison struct {
	optimizer *StreamingJSONOptimizer
}

// NewStreamingResponseComparison creates a new comparison utility
func NewStreamingResponseComparison() *StreamingResponseComparison {
	return &StreamingResponseComparison{
		optimizer: NewStreamingJSONOptimizer(),
	}
}

// BenchmarkTraditionalResponse simulates the existing 3x marshaling approach
func (src *StreamingResponseComparison) BenchmarkTraditionalResponse(payload interface{}, message string, iterations int) time.Duration {
	start := time.Now()

	for i := 0; i < iterations; i++ {
		// Simulate existing api/response.go approach
		base := map[string]interface{}{
			"success":   true,
			"message":   message,
			"timestamp": time.Now(),
			"meta": map[string]interface{}{
				"request_id": "test-123",
				"version":    "v1.0",
			},
		}

		// CRITICAL BOTTLENECK: Double marshal/unmarshal cycle
		if payload != nil {
			b, _ := json.Marshal(payload) // MARSHAL #1
			var response map[string]interface{}
			if json.Unmarshal(b, &response) == nil { // UNMARSHAL #1
				for k, v := range response {
					base[k] = v
				}
			}
		}

		// Final marshal (simulating c.JSON)
		_, _ = json.Marshal(base) // MARSHAL #2
	}

	return time.Since(start)
}

// BenchmarkOptimizedResponse tests the new single-marshal approach
func (src *StreamingResponseComparison) BenchmarkOptimizedResponse(payload interface{}, message string, iterations int) time.Duration {
	start := time.Now()

	for i := 0; i < iterations; i++ {
		// Create a temporary buffer for testing
		var buf discardWriter

		meta := map[string]interface{}{
			"request_id": "test-123",
			"version":    "v1.0",
		}

		// Single marshal operation - NO INTERMEDIATE STEPS
		_ = src.optimizer.WriteOptimizedSuccessResponse(&buf, payload, message, meta)
	}

	return time.Since(start)
}

// discardWriter implements io.Writer for benchmarking without actual I/O
type discardWriter struct{}

func (dw discardWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}

// PerformanceAnalytics provides detailed performance analysis
type PerformanceAnalytics struct {
	comparison *StreamingResponseComparison
}

// NewPerformanceAnalytics creates a new performance analytics utility
func NewPerformanceAnalytics() *PerformanceAnalytics {
	return &PerformanceAnalytics{
		comparison: NewStreamingResponseComparison(),
	}
}

// AnalyzeResponseOptimization performs comprehensive response optimization analysis
func (pa *PerformanceAnalytics) AnalyzeResponseOptimization(payload interface{}, iterations int) ResponseOptimizationResult {
	message := "Success"

	// Benchmark traditional approach
	traditionalTime := pa.comparison.BenchmarkTraditionalResponse(payload, message, iterations)

	// Benchmark optimized approach
	optimizedTime := pa.comparison.BenchmarkOptimizedResponse(payload, message, iterations)

	// Calculate metrics
	improvement := float64(traditionalTime-optimizedTime) / float64(traditionalTime) * 100

	return ResponseOptimizationResult{
		TraditionalTime:    traditionalTime,
		OptimizedTime:      optimizedTime,
		ImprovementPercent: improvement,
		Iterations:         iterations,
		PayloadSize:        estimatePayloadSize(payload),
	}
}

// ResponseOptimizationResult contains the analysis results
type ResponseOptimizationResult struct {
	TraditionalTime    time.Duration
	OptimizedTime      time.Duration
	ImprovementPercent float64
	Iterations         int
	PayloadSize        int
}

// estimatePayloadSize provides rough payload size estimation
func estimatePayloadSize(payload interface{}) int {
	if payload == nil {
		return 0
	}

	// Rough estimation by marshaling once
	data, err := json.Marshal(payload)
	if err != nil {
		return 0
	}

	return len(data)
}

// GetOptimizationImpact returns human-readable impact analysis
func (ror *ResponseOptimizationResult) GetOptimizationImpact() string {
	if ror.ImprovementPercent > 60 {
		return "Excellent - Major performance gain"
	} else if ror.ImprovementPercent > 30 {
		return "Good - Significant improvement"
	} else if ror.ImprovementPercent > 10 {
		return "Moderate - Noticeable improvement"
	} else if ror.ImprovementPercent > 0 {
		return "Minor - Small improvement"
	} else {
		return "No improvement - Needs optimization"
	}
}
