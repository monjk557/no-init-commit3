// Package api는 표준화된 API 응답을 위한 HTTP 응답 처리 유틸리티와 미들웨어를 제공합니다.
// 이 파일은 일관된 출력을 위한 응답 포매팅, JSON 에러 미들웨어, 도우미 함수들을 포함합니다.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// APIResponse는 표준화된 API 응답 구조를 나타냅니다.
// 규약: 성공 응답은 data 래퍼 없이 flat (payload 필드는 최상위에 둠).
type APIResponse struct {
	// Success는 작업이 성공했는지를 나타냅니다
	Success bool `json:"success"`
	// Message는 사람이 읽을 수 있는 설명을 제공합니다
	Message string `json:"message,omitempty"`
	// Error는 에러 정보를 포함합니다 (에러 응답에만)
	Error *logging.AppError `json:"error,omitempty"`
	// Meta는 응답에 대한 메타데이터를 포함합니다
	Meta *ResponseMeta `json:"meta,omitempty"`
	// Timestamp는 응답이 생성된 시간입니다
	Timestamp time.Time `json:"timestamp"`
}

// ResponseMeta는 API 응답에 대한 메타데이터를 포함합니다
type ResponseMeta struct {
	// RequestID는 상관관계를 위한 ID입니다
	RequestID string `json:"request_id,omitempty"`
	// Duration은 작업 소요 시간(밀리초)입니다
	Duration int64 `json:"duration_ms,omitempty"`
	// Version은 API 버전입니다
	Version string `json:"version,omitempty"`
	// Pagination information for paginated responses
	Pagination *PaginationMeta `json:"pagination,omitempty"`
}

// PaginationMeta contains pagination information
type PaginationMeta struct {
	// Page is the current page number (1-based)
	Page int `json:"page"`
	// PageSize is the number of items per page
	PageSize int `json:"page_size"`
	// Total is the total number of items
	Total int `json:"total"`
	// TotalPages is the total number of pages
	TotalPages int `json:"total_pages"`
	// HasNext indicates if there are more pages
	HasNext bool `json:"has_next"`
	// HasPrev indicates if there are previous pages
	HasPrev bool `json:"has_prev"`
}

// JSONResponseMiddleware는 표준화된 JSON 응답 처리를 제공합니다
type JSONResponseMiddleware struct {
	logger  *logging.Logger
	version string
}

// NewJSONResponseMiddleware는 새로운 JSON 응답 미들웨어 인스턴스를 생성합니다
func NewJSONResponseMiddleware(logger *logging.Logger, version string) *JSONResponseMiddleware {
	if logger == nil {
		// 제공되지 않은 경우 기본 로거 사용
		cfg := config.LoggingConfig{Level: "info", Format: "text", Output: "stderr"}
		logger, _ = logging.NewLogger(cfg)
	}
	if version == "" {
		version = "1.0"
	}

	return &JSONResponseMiddleware{
		logger:  logger,
		version: version,
	}
}

// ErrorHandler는 에러 처리를 위한 메인 미들웨어 함수입니다
func (m *JSONResponseMiddleware) ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		// Process any errors that occurred during the request
		if len(c.Errors) > 0 {
			err := c.Errors.Last()

			// Get request start time for duration calculation
			startTime, exists := c.Get("start_time")
			var duration int64
			if exists {
				if start, ok := startTime.(time.Time); ok {
					duration = time.Since(start).Milliseconds()
				}
			}

			// Extract request ID if available
			requestID, _ := c.Get("request_id")
			requestIDStr, _ := requestID.(string)

			// Handle different error types
			switch err.Type {
			case gin.ErrorTypePublic:
				m.handlePublicError(c, err, requestIDStr, duration)
			case gin.ErrorTypeBind:
				m.handleBindError(c, err, requestIDStr, duration)
			default:
				m.handleInternalError(c, err, requestIDStr, duration)
			}
		}
	}
}

// handlePublicError handles publicly safe errors
func (m *JSONResponseMiddleware) handlePublicError(c *gin.Context, ginErr *gin.Error, requestID string, duration int64) {
	// Check if the error is a GitAPIError
	if apiErr, ok := ginErr.Err.(*logging.AppError); ok {
		m.SendErrorResponse(c, apiErr, requestID, duration)
		return
	}

	// For other public errors, create a generic API error
	apiErr := logging.NewValidationError("UNKNOWN_ERROR", ginErr.Error())
	apiErr.RequestID = requestID
	m.SendErrorResponse(c, apiErr, requestID, duration)
}

// handleBindError handles request binding errors
func (m *JSONResponseMiddleware) handleBindError(c *gin.Context, ginErr *gin.Error, requestID string, duration int64) {
	apiErr := logging.NewValidationError("UNKNOWN_ERROR", "Invalid request format or missing required fields")
	apiErr.RequestID = requestID
	_ = apiErr.WithDetails(ginErr.Error())
	m.SendErrorResponse(c, apiErr, requestID, duration)
}

// handleInternalError handles internal server errors
func (m *JSONResponseMiddleware) handleInternalError(c *gin.Context, ginErr *gin.Error, requestID string, duration int64) {
	// Log the full error for internal tracking
	m.logger.Error("Internal server error",
		zap.String("error", ginErr.Error()),
		zap.String("request_id", requestID),
		zap.String("path", c.Request.URL.Path),
		zap.String("method", c.Request.Method),
	)

	// Send a generic internal error response (don't expose internal details)
	apiErr := logging.NewInternalError("UNKNOWN_ERROR", "An internal server error occurred")
	apiErr.RequestID = requestID
	m.SendErrorResponse(c, apiErr, requestID, duration)
}

// SendSuccessResponse는 flat 성공 응답을 전송합니다 (data 래퍼 없이 payload를 최상위에 병합).
func (m *JSONResponseMiddleware) SendSuccessResponse(c *gin.Context, payload interface{}, message string) {
	requestID, _ := c.Get("request_id")
	requestIDStr, _ := requestID.(string)
	var duration int64
	if start, exists := c.Get("start_time"); exists {
		if t, ok := start.(time.Time); ok {
			duration = time.Since(t).Milliseconds()
		}
	}
	base := map[string]interface{}{
		"success":   true,
		"message":   message,
		"timestamp": time.Now(),
		"meta": &ResponseMeta{
			RequestID: requestIDStr,
			Duration:  duration,
			Version:   m.version,
		},
	}
	if payload != nil {
		b, _ := json.Marshal(payload)
		var response map[string]interface{}
		if json.Unmarshal(b, &response) == nil {
			for k, v := range response {
				base[k] = v
			}
		}
	}
	c.JSON(http.StatusOK, base)
}

// SendSuccessResponseWithPagination sends a flat paginated success response (data 래퍼 없음).
func (m *JSONResponseMiddleware) SendSuccessResponseWithPagination(c *gin.Context, payload interface{}, pagination *PaginationMeta, message string) {
	requestID, _ := c.Get("request_id")
	requestIDStr, _ := requestID.(string)
	var duration int64
	if start, exists := c.Get("start_time"); exists {
		if t, ok := start.(time.Time); ok {
			duration = time.Since(t).Milliseconds()
		}
	}
	base := map[string]interface{}{
		"success":   true,
		"message":   message,
		"timestamp": time.Now(),
		"meta": &ResponseMeta{
			RequestID:  requestIDStr,
			Duration:   duration,
			Version:    m.version,
			Pagination: pagination,
		},
	}
	if payload != nil {
		b, _ := json.Marshal(payload)
		var response map[string]interface{}
		if json.Unmarshal(b, &response) == nil {
			for k, v := range response {
				base[k] = v
			}
		}
	}
	c.JSON(http.StatusOK, base)
}

// SendErrorResponse는 표준화된 에러 응답을 전송합니다
func (m *JSONResponseMiddleware) SendErrorResponse(c *gin.Context, apiErr *logging.AppError, requestID string, duration int64) {
	errorResponse := &logging.AppError{
		Type:        apiErr.Type,
		Code:        apiErr.Code,
		Message:     apiErr.Message,
		Details:     apiErr.Details,
		Cause:       apiErr.Cause,
		Context:     apiErr.Context,
		StatusCode:  apiErr.StatusCode,
		RequestID:   apiErr.RequestID,
		Retryable:   apiErr.Retryable,
		UserMessage: apiErr.UserMessage,
	}

	// Use provided request ID if the error doesn't have one
	if errorResponse.RequestID == "" {
		errorResponse.RequestID = requestID
	}

	response := APIResponse{
		Success:   false,
		Error:     errorResponse,
		Timestamp: time.Now(),
		Meta: &ResponseMeta{
			RequestID: requestID,
			Duration:  duration,
			Version:   m.version,
		},
	}

	// Log the error for monitoring
	m.logger.Warn("API error response",
		zap.String("code", string(apiErr.Code)),
		zap.String("message", apiErr.Message),
		zap.String("severity", string(apiErr.Type)),
		zap.String("request_id", requestID),
		zap.Int("status_code", apiErr.StatusCode),
	)

	c.JSON(apiErr.StatusCode, response)
}

// RequestMetadataMiddleware adds request metadata to the context
func (m *JSONResponseMiddleware) RequestMetadataMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Set start time for duration calculation
		c.Set("start_time", time.Now())

		// Use request ID from context (e.g. set by GinRequestLogger) or header, otherwise generate
		var requestID string
		if id, ok := c.Get("request_id"); ok {
			if s, ok := id.(string); ok && s != "" {
				requestID = s
			}
		}
		if requestID == "" {
			requestID = c.GetHeader("X-Request-ID")
			if requestID == "" {
				requestID = generateRequestID()
			}
			c.Set("request_id", requestID)
		}

		// Add request ID to response headers
		c.Header("X-Request-ID", requestID)

		c.Next()
	}
}

// Helper Functions

// SendJSON is a utility function to send JSON responses with proper error handling
func SendJSON(c *gin.Context, statusCode int, data interface{}) {
	c.Header("Content-Type", "application/json")

	// Handle potential JSON marshaling errors
	c.JSON(statusCode, data)
}

// SendError is a utility function to send error responses
func SendError(c *gin.Context, apiErr *logging.AppError) {
	// Create a basic response middleware for this utility
	middleware := NewJSONResponseMiddleware(nil, "")

	// Extract request metadata if available
	requestID, _ := c.Get("request_id")
	requestIDStr, _ := requestID.(string)

	startTime, exists := c.Get("start_time")
	var duration int64
	if exists {
		if start, ok := startTime.(time.Time); ok {
			duration = time.Since(start).Milliseconds()
		}
	}

	middleware.SendErrorResponse(c, apiErr, requestIDStr, duration)
}

// SendSuccess is a utility function to send flat success responses
func SendSuccess(c *gin.Context, payload interface{}, message string) {
	middleware := NewJSONResponseMiddleware(nil, "")
	middleware.SendSuccessResponse(c, payload, message)
}

// CreatePagination creates pagination metadata
func CreatePagination(page, pageSize, total int) *PaginationMeta {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}

	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}

	return &PaginationMeta{
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages,
		HasNext:    page < totalPages,
		HasPrev:    page > 1,
	}
}

// Helper function to generate a simple request ID
func generateRequestID() string {
	// In production, use UUID or a more robust ID generation
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}
