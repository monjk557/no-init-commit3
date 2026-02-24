package performance

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// OptimizedJSONResponseMiddleware replaces the inefficient double marshaling in api/response.go
// Research target: Eliminate 3x marshal/unmarshal cycles per response
type OptimizedJSONResponseMiddleware struct {
	version        string
	responseWriter *OptimizedResponseWriter
}

// ResponseMeta matches the existing structure from api/response.go
type ResponseMeta struct {
	RequestID string        `json:"request_id"`
	Duration  time.Duration `json:"duration"`
	Version   string        `json:"version"`
}

// NewOptimizedJSONResponseMiddleware creates a new optimized response middleware
func NewOptimizedJSONResponseMiddleware(version string) *OptimizedJSONResponseMiddleware {
	return &OptimizedJSONResponseMiddleware{
		version:        version,
		responseWriter: NewOptimizedResponseWriter(),
	}
}

// SendSuccessResponseOptimized replaces the inefficient SendSuccessResponse
// TARGET: Eliminate double marshal + unmarshal + flatten cycle
func (m *OptimizedJSONResponseMiddleware) SendSuccessResponseOptimized(c *gin.Context, payload interface{}, message string) {
	// Calculate request duration
	if startTime, exists := c.Get("startTime"); exists {
		if start, ok := startTime.(time.Time); ok {
			duration := time.Since(start)
			c.Set("requestDuration", duration)
		}
	}

	// Get request metadata
	requestIDStr := "optimized-stream"
	if requestID, exists := c.Get("requestID"); exists {
		if id, ok := requestID.(string); ok {
			requestIDStr = id
		}
	}

	var duration time.Duration
	if reqDuration, exists := c.Get("requestDuration"); exists {
		if d, ok := reqDuration.(time.Duration); ok {
			duration = d
		}
	}

	// Set response headers for streaming
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Status(http.StatusOK)

	// Create streaming writer that writes directly to response
	writer := m.responseWriter.streamer.NewStreamingJSONWriter(c.Writer)
	defer writer.Close()

	// Start response object
	if err := writer.WriteObjectStart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Write standard envelope fields directly
	if err := writer.WriteField("success", true); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	if err := writer.WriteField("message", message); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	if err := writer.WriteField("timestamp", time.Now().Format(time.RFC3339)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Write metadata directly
	meta := &ResponseMeta{
		RequestID: requestIDStr,
		Duration:  duration,
		Version:   m.version,
	}
	if err := writer.WriteField("meta", meta); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Stream payload directly - NO DOUBLE MARSHALING
	if payload != nil {
		// Check if payload is a map that needs flattening (existing behavior)
		if payloadMap, ok := payload.(map[string]interface{}); ok {
			// Flatten fields directly into response (zero-copy)
			for k, v := range payloadMap {
				if err := writer.WriteField(k, v); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
					return
				}
			}
		} else {
			// Write as data field (wrapped behavior)
			if err := writer.WriteField("data", payload); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
				return
			}
		}
	}

	// End response object
	if err := writer.WriteObjectEnd(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}
}

// SendSuccessResponseWithPaginationOptimized provides optimized paginated response streaming
func (m *OptimizedJSONResponseMiddleware) SendSuccessResponseWithPaginationOptimized(c *gin.Context, payload interface{}, pagination interface{}, message string) {
	// Set response headers for streaming
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Status(http.StatusOK)

	// Create streaming writer
	writer := m.responseWriter.streamer.NewStreamingJSONWriter(c.Writer)
	defer writer.Close()

	// Start response object
	if err := writer.WriteObjectStart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Write standard fields
	if err := writer.WriteField("success", true); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	if err := writer.WriteField("message", message); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Stream payload directly
	if payload != nil {
		if err := writer.WriteField("data", payload); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
			return
		}
	}

	// Stream pagination
	if pagination != nil {
		if err := writer.WriteField("pagination", pagination); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
			return
		}
	}

	// End response object
	if err := writer.WriteObjectEnd(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}
}

// StreamingDiffResponseHandler provides optimized diff response streaming
// TARGET: Stream 5-15MB diff responses without full memory allocation
type StreamingDiffResponseHandler struct {
	middleware *OptimizedJSONResponseMiddleware
	diffWriter *StreamingDiffResponseWriter
}

// NewStreamingDiffResponseHandler creates a new streaming diff response handler
func NewStreamingDiffResponseHandler(version string) *StreamingDiffResponseHandler {
	return &StreamingDiffResponseHandler{
		middleware: NewOptimizedJSONResponseMiddleware(version),
		diffWriter: NewStreamingDiffResponseWriter(),
	}
}

// StreamDiffResponseOptimized streams large diff responses progressively
func (sdrh *StreamingDiffResponseHandler) StreamDiffResponseOptimized(c *gin.Context, base, head string, files []interface{}, stats interface{}, pagination interface{}) {
	// Set response headers for streaming
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Status(http.StatusOK)

	// Create streaming writer
	writer := sdrh.diffWriter.streamer.NewStreamingJSONWriter(c.Writer)
	defer writer.Close()

	// Start response envelope
	if err := writer.WriteObjectStart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Write success envelope
	if err := writer.WriteField("success", true); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Start diff data object
	if _, err := c.Writer.Write([]byte(`,"data":`)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	if err := writer.WriteObjectStart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Write diff metadata
	if err := writer.WriteField("base", base); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	if err := writer.WriteField("head", head); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	if err := writer.WriteField("stats", stats); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Stream files array progressively - CRITICAL OPTIMIZATION
	if _, err := c.Writer.Write([]byte(`,"files":`)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	if err := writer.WriteArrayStart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Write files one by one without loading all in memory
	// This is the key optimization for 5-15MB responses
	for _, file := range files {
		if err := writer.WriteValue(file); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
			return
		}
	}

	if err := writer.WriteArrayEnd(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// End diff data object
	if err := writer.WriteObjectEnd(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Add pagination if provided
	if pagination != nil {
		if err := writer.WriteField("pagination", pagination); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
			return
		}
	}

	// End response envelope
	if err := writer.WriteObjectEnd(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}
}

// StreamingCommitsResponseHandler provides optimized commits response streaming
type StreamingCommitsResponseHandler struct {
	middleware    *OptimizedJSONResponseMiddleware
	commitsWriter *StreamingCommitsResponseWriter
}

// NewStreamingCommitsResponseHandler creates a new streaming commits response handler
func NewStreamingCommitsResponseHandler(version string) *StreamingCommitsResponseHandler {
	return &StreamingCommitsResponseHandler{
		middleware:    NewOptimizedJSONResponseMiddleware(version),
		commitsWriter: NewStreamingCommitsResponseWriter(),
	}
}

// StreamCommitsResponseOptimized streams paginated commits response progressively
func (scrh *StreamingCommitsResponseHandler) StreamCommitsResponseOptimized(c *gin.Context, commitGroups []interface{}, pagination interface{}) {
	// Set response headers for streaming
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Status(http.StatusOK)

	// Create streaming writer
	writer := scrh.commitsWriter.streamer.NewStreamingJSONWriter(c.Writer)
	defer writer.Close()

	// Start response object
	if err := writer.WriteObjectStart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Write success envelope
	if err := writer.WriteField("success", true); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Stream results array progressively
	if _, err := c.Writer.Write([]byte(`,"results":`)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	if err := writer.WriteArrayStart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Write commit groups progressively
	for _, group := range commitGroups {
		if err := writer.WriteValue(group); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
			return
		}
	}

	if err := writer.WriteArrayEnd(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// Write pagination
	if err := writer.WriteField("pagination", pagination); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}

	// End response object
	if err := writer.WriteObjectEnd(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming error"})
		return
	}
}
