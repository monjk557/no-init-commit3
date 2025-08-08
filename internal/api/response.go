// Package api provides HTTP response handling utilities and middleware for standardized API responses.
// This file contains response formatting, JSON error middleware, and helper functions for consistent output.
package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// APIResponse represents a standardized API response structure
type APIResponse struct {
	// Success indicates if the operation was successful
	Success bool `json:"success"`
	// Message provides a human-readable description
	Message string `json:"message,omitempty"`
	// Data contains the response payload (only for successful responses)
	Data interface{} `json:"data,omitempty"`
	// Error contains error information (only for error responses)
	Error *ErrorResponse `json:"error,omitempty"`
	// Meta contains metadata about the response
	Meta *ResponseMeta `json:"meta,omitempty"`
	// Timestamp when the response was generated
	Timestamp time.Time `json:"timestamp"`
}

// ErrorResponse represents error information in API responses
type ErrorResponse struct {
	// Code is the standardized error code
	Code string `json:"code"`
	// Message is the human-readable error message
	Message string `json:"message"`
	// Details provides additional context about the error
	Details map[string]interface{} `json:"details,omitempty"`
	// Severity indicates the severity level of the error
	Severity string `json:"severity,omitempty"`
	// TraceID for correlation across services
	TraceID string `json:"trace_id,omitempty"`
	// RequestID for tracking specific requests
	RequestID string `json:"request_id,omitempty"`
	// Operation that caused the error
	Operation string `json:"operation,omitempty"`
	// Resource that was being accessed
	Resource string `json:"resource,omitempty"`
}

// ResponseMeta contains metadata about the API response
type ResponseMeta struct {
	// RequestID for correlation
	RequestID string `json:"request_id,omitempty"`
	// Duration of the operation in milliseconds
	Duration int64 `json:"duration_ms,omitempty"`
	// Version of the API
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

// JSONResponseMiddleware provides standardized JSON response handling
type JSONResponseMiddleware struct {
	logger  *logging.Logger
	version string
}

// NewJSONResponseMiddleware creates a new JSON response middleware instance
func NewJSONResponseMiddleware(logger *logging.Logger, version string) *JSONResponseMiddleware {
	if logger == nil {
		// Use a default logger if none provided
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

// ErrorHandler is the main middleware function for handling errors
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
	if apiErr, ok := ginErr.Err.(*GitAPIError); ok {
		m.SendErrorResponse(c, apiErr, requestID, duration)
		return
	}

	// For other public errors, create a generic API error
	apiErr := NewBadRequestError(ginErr.Error())
	apiErr.RequestID = requestID
	m.SendErrorResponse(c, apiErr, requestID, duration)
}

// handleBindError handles request binding errors
func (m *JSONResponseMiddleware) handleBindError(c *gin.Context, ginErr *gin.Error, requestID string, duration int64) {
	apiErr := NewValidationError("Invalid request format or missing required fields")
	apiErr.RequestID = requestID
	apiErr.Details = map[string]interface{}{
		"bind_error": ginErr.Error(),
	}
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
	apiErr := NewInternalError("An internal server error occurred")
	apiErr.RequestID = requestID
	m.SendErrorResponse(c, apiErr, requestID, duration)
}

// SendSuccessResponse sends a standardized success response
func (m *JSONResponseMiddleware) SendSuccessResponse(c *gin.Context, data interface{}, message string) {
	// Get request metadata
	requestID, _ := c.Get("request_id")
	requestIDStr, _ := requestID.(string)

	startTime, exists := c.Get("start_time")
	var duration int64
	if exists {
		if start, ok := startTime.(time.Time); ok {
			duration = time.Since(start).Milliseconds()
		}
	}

	response := APIResponse{
		Success:   true,
		Message:   message,
		Data:      data,
		Timestamp: time.Now(),
		Meta: &ResponseMeta{
			RequestID: requestIDStr,
			Duration:  duration,
			Version:   m.version,
		},
	}

	c.JSON(http.StatusOK, response)
}

// SendSuccessResponseWithPagination sends a paginated success response
func (m *JSONResponseMiddleware) SendSuccessResponseWithPagination(c *gin.Context, data interface{}, pagination *PaginationMeta, message string) {
	// Get request metadata
	requestID, _ := c.Get("request_id")
	requestIDStr, _ := requestID.(string)

	startTime, exists := c.Get("start_time")
	var duration int64
	if exists {
		if start, ok := startTime.(time.Time); ok {
			duration = time.Since(start).Milliseconds()
		}
	}

	response := APIResponse{
		Success:   true,
		Message:   message,
		Data:      data,
		Timestamp: time.Now(),
		Meta: &ResponseMeta{
			RequestID:  requestIDStr,
			Duration:   duration,
			Version:    m.version,
			Pagination: pagination,
		},
	}

	c.JSON(http.StatusOK, response)
}

// SendErrorResponse sends a standardized error response
func (m *JSONResponseMiddleware) SendErrorResponse(c *gin.Context, apiErr *GitAPIError, requestID string, duration int64) {
	errorResponse := &ErrorResponse{
		Code:      string(apiErr.Code),
		Message:   apiErr.Message,
		Details:   apiErr.Details,
		Severity:  string(apiErr.Severity),
		TraceID:   apiErr.TraceID,
		RequestID: apiErr.RequestID,
		Operation: apiErr.Operation,
		Resource:  apiErr.Resource,
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
		zap.String("severity", string(apiErr.Severity)),
		zap.String("operation", apiErr.Operation),
		zap.String("resource", apiErr.Resource),
		zap.String("request_id", requestID),
		zap.Int("status_code", apiErr.HTTPStatusCode()),
	)

	c.JSON(apiErr.HTTPStatusCode(), response)
}

// RequestMetadataMiddleware adds request metadata to the context
func (m *JSONResponseMiddleware) RequestMetadataMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Set start time for duration calculation
		c.Set("start_time", time.Now())

		// Generate or extract request ID
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			// Generate a simple request ID (in production, use UUID or similar)
			requestID = generateRequestID()
		}
		c.Set("request_id", requestID)

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
func SendError(c *gin.Context, apiErr *GitAPIError) {
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

// SendSuccess is a utility function to send success responses
func SendSuccess(c *gin.Context, data interface{}, message string) {
	// Create a basic response middleware for this utility
	middleware := NewJSONResponseMiddleware(nil, "")
	middleware.SendSuccessResponse(c, data, message)
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

// ErrorMapper converts various error types to GitAPIError
func ErrorMapper(err error, operation string) *GitAPIError {
	mapper := GetDefaultMapper()
	return mapper.MapError(err, operation)
}

// ErrorMapperWithContext converts errors with additional context
func ErrorMapperWithContext(err error, operation, resource string) *GitAPIError {
	mapper := GetDefaultMapper()
	return mapper.MapErrorWithContext(err, operation, resource)
}

// ErrorMapperWithDetails converts errors with additional details
func ErrorMapperWithDetails(err error, operation string, details map[string]interface{}) *GitAPIError {
	mapper := GetDefaultMapper()
	return mapper.MapErrorWithDetails(err, operation, details)
}
