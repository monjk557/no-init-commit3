package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"go.uber.org/zap"
)

// ErrorType represents different types of errors
type ErrorType string

const (
	ErrorTypeValidation   ErrorType = "validation"
	ErrorTypeNotFound     ErrorType = "not_found"
	ErrorTypeUnauthorized ErrorType = "unauthorized"
	ErrorTypeForbidden    ErrorType = "forbidden"
	ErrorTypeConflict     ErrorType = "conflict"
	ErrorTypeInternal     ErrorType = "internal"
	ErrorTypeExternal     ErrorType = "external"
	ErrorTypeTimeout      ErrorType = "timeout"
	ErrorTypeRateLimit    ErrorType = "rate_limit"
)

// AppError represents a structured application error
type AppError struct {
	Type        ErrorType              `json:"type"`
	Code        string                 `json:"code"`
	Message     string                 `json:"message"`
	Details     string                 `json:"details,omitempty"`
	Cause       error                  `json:"-"`
	Context     map[string]interface{} `json:"context,omitempty"`
	StatusCode  int                    `json:"-"`
	RequestID   string                 `json:"request_id,omitempty"`
	Retryable   bool                   `json:"retryable"`
	UserMessage string                 `json:"user_message,omitempty"`
}

// Error implements the error interface
func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (caused by: %v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap implements the errors.Unwrap interface
func (e *AppError) Unwrap() error {
	return e.Cause
}

// WithContext adds context to the error
func (e *AppError) WithContext(key string, value interface{}) *AppError {
	if e.Context == nil {
		e.Context = make(map[string]interface{})
	}
	e.Context[key] = value
	return e
}

// WithCause adds a cause to the error
func (e *AppError) WithCause(cause error) *AppError {
	e.Cause = cause
	return e
}

// WithRequestID adds a request ID to the error
func (e *AppError) WithRequestID(requestID string) *AppError {
	e.RequestID = requestID
	return e
}

// WithDetails adds details to the error
func (e *AppError) WithDetails(details string) *AppError {
	e.Details = details
	return e
}

// ErrorBuilder helps build AppError instances
type ErrorBuilder struct {
	errorType   ErrorType
	code        string
	message     string
	details     string
	cause       error
	context     map[string]interface{}
	statusCode  int
	retryable   bool
	userMessage string
}

// NewErrorBuilder creates a new error builder
func NewErrorBuilder(errorType ErrorType, code, message string) *ErrorBuilder {
	return &ErrorBuilder{
		errorType: errorType,
		code:      code,
		message:   message,
		context:   make(map[string]interface{}),
	}
}

// WithDetails adds details to the error
func (b *ErrorBuilder) WithDetails(details string) *ErrorBuilder {
	b.details = details
	return b
}

// WithCause adds a cause to the error
func (b *ErrorBuilder) WithCause(cause error) *ErrorBuilder {
	b.cause = cause
	return b
}

// WithContext adds context to the error
func (b *ErrorBuilder) WithContext(key string, value interface{}) *ErrorBuilder {
	b.context[key] = value
	return b
}

// WithStatusCode sets the HTTP status code
func (b *ErrorBuilder) WithStatusCode(statusCode int) *ErrorBuilder {
	b.statusCode = statusCode
	return b
}

// WithRetryable sets whether the error is retryable
func (b *ErrorBuilder) WithRetryable(retryable bool) *ErrorBuilder {
	b.retryable = retryable
	return b
}

// WithUserMessage sets a user-friendly message
func (b *ErrorBuilder) WithUserMessage(userMessage string) *ErrorBuilder {
	b.userMessage = userMessage
	return b
}

// Build creates the AppError
func (b *ErrorBuilder) Build() *AppError {
	statusCode := b.statusCode
	if statusCode == 0 {
		statusCode = getDefaultStatusCode(b.errorType)
	}

	return &AppError{
		Type:        b.errorType,
		Code:        b.code,
		Message:     b.message,
		Details:     b.details,
		Cause:       b.cause,
		Context:     b.context,
		StatusCode:  statusCode,
		Retryable:   b.retryable,
		UserMessage: b.userMessage,
	}
}

// getDefaultStatusCode returns default HTTP status codes for error types
func getDefaultStatusCode(errorType ErrorType) int {
	switch errorType {
	case ErrorTypeValidation:
		return http.StatusBadRequest
	case ErrorTypeNotFound:
		return http.StatusNotFound
	case ErrorTypeUnauthorized:
		return http.StatusUnauthorized
	case ErrorTypeForbidden:
		return http.StatusForbidden
	case ErrorTypeConflict:
		return http.StatusConflict
	case ErrorTypeTimeout:
		return http.StatusRequestTimeout
	case ErrorTypeRateLimit:
		return http.StatusTooManyRequests
	case ErrorTypeExternal:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// Common error constructors
func NewValidationError(code, message string) *AppError {
	return NewErrorBuilder(ErrorTypeValidation, code, message).
		WithUserMessage("Invalid input provided").
		Build()
}

func NewNotFoundError(code, message string) *AppError {
	return NewErrorBuilder(ErrorTypeNotFound, code, message).
		WithUserMessage("The requested resource was not found").
		Build()
}

func NewUnauthorizedError(code, message string) *AppError {
	return NewErrorBuilder(ErrorTypeUnauthorized, code, message).
		WithUserMessage("Authentication required").
		Build()
}

func NewForbiddenError(code, message string) *AppError {
	return NewErrorBuilder(ErrorTypeForbidden, code, message).
		WithUserMessage("Access denied").
		Build()
}

func NewInternalError(code, message string) *AppError {
	return NewErrorBuilder(ErrorTypeInternal, code, message).
		WithUserMessage("An internal error occurred").
		WithRetryable(true).
		Build()
}

func NewExternalError(code, message string) *AppError {
	return NewErrorBuilder(ErrorTypeExternal, code, message).
		WithUserMessage("External service error").
		WithRetryable(true).
		Build()
}

// ErrorHandler provides centralized error handling and logging
type ErrorHandler struct {
	logger *Logger
}

// NewErrorHandler creates a new error handler
func NewErrorHandler(logger *Logger) *ErrorHandler {
	return &ErrorHandler{
		logger: logger,
	}
}

// Handle processes an error and returns appropriate HTTP response
func (h *ErrorHandler) Handle(ctx context.Context, w http.ResponseWriter, err error) {
	// Extract request ID from context
	requestID := ""
	if id, ok := ctx.Value(RequestIDKey).(string); ok {
		requestID = id
	}

	var appErr *AppError

	// Convert to AppError if it's not already
	if ae, ok := err.(*AppError); ok {
		appErr = ae
		if appErr.RequestID == "" {
			appErr.RequestID = requestID
		}
	} else {
		// Wrap unknown errors as internal errors
		appErr = NewInternalError("UNKNOWN_ERROR", "An unexpected error occurred").
			WithCause(err).
			WithRequestID(requestID)
	}

	// Log the error
	h.logError(ctx, appErr)

	// Send HTTP response
	h.sendErrorResponse(w, appErr)
}

// logError logs the error with appropriate level and context
func (h *ErrorHandler) logError(ctx context.Context, appErr *AppError) {
	fields := []zap.Field{
		zap.String("error_type", string(appErr.Type)),
		zap.String("error_code", appErr.Code),
		zap.String("error_message", appErr.Message),
		zap.Bool("retryable", appErr.Retryable),
		zap.Int("status_code", appErr.StatusCode),
	}

	if appErr.RequestID != "" {
		fields = append(fields, zap.String("request_id", appErr.RequestID))
	}

	if appErr.Details != "" {
		fields = append(fields, zap.String("details", appErr.Details))
	}

	// Add context fields
	for key, value := range appErr.Context {
		fields = append(fields, zap.Any(fmt.Sprintf("ctx_%s", key), value))
	}

	// Log at appropriate level
	switch appErr.Type {
	case ErrorTypeValidation, ErrorTypeNotFound, ErrorTypeUnauthorized, ErrorTypeForbidden:
		h.logger.LogWarning(ctx, "Client error", fields...)
	case ErrorTypeInternal, ErrorTypeExternal:
		h.logger.LogError(ctx, appErr.Cause, "Server error", fields...)
	default:
		h.logger.LogError(ctx, appErr.Cause, "Application error", fields...)
	}
}

// sendErrorResponse sends an HTTP error response
func (h *ErrorHandler) sendErrorResponse(w http.ResponseWriter, appErr *AppError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(appErr.StatusCode)

	// Create response body
	response := map[string]interface{}{
		"error": map[string]interface{}{
			"type":    appErr.Type,
			"code":    appErr.Code,
			"message": appErr.UserMessage,
		},
	}

	if appErr.RequestID != "" {
		response["request_id"] = appErr.RequestID
	}

	// Include details in non-production environments or for client errors
	if appErr.Type != ErrorTypeInternal || appErr.Details != "" {
		response["error"].(map[string]interface{})["details"] = appErr.Details
	}

	// Encode and send response
	if err := json.NewEncoder(w).Encode(response); err != nil {
		// Fallback to plain text if JSON encoding fails
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// HandlePanic recovers from panics and converts them to errors
func (h *ErrorHandler) HandlePanic(ctx context.Context, w http.ResponseWriter) {
	if r := recover(); r != nil {
		var err error
		if e, ok := r.(error); ok {
			err = e
		} else {
			err = fmt.Errorf("panic: %v", r)
		}

		appErr := NewInternalError("PANIC", "Request processing failed").
			WithCause(err).
			WithDetails(fmt.Sprintf("Panic recovered: %v", r))

		h.Handle(ctx, w, appErr)
	}
}

// Global error handler instance
var globalErrorHandler *ErrorHandler

// InitGlobalErrorHandler initializes the global error handler
func InitGlobalErrorHandler(logger *Logger) {
	globalErrorHandler = NewErrorHandler(logger)
}

// GetGlobalErrorHandler returns the global error handler
func GetGlobalErrorHandler() *ErrorHandler {
	if globalErrorHandler == nil {
		globalErrorHandler = NewErrorHandler(GetGlobalLogger())
	}
	return globalErrorHandler
}

// HandleError is a convenience function using the global error handler
func HandleError(ctx context.Context, w http.ResponseWriter, err error) {
	GetGlobalErrorHandler().Handle(ctx, w, err)
}

// HandlePanic is a convenience function using the global error handler
func HandlePanic(ctx context.Context, w http.ResponseWriter) {
	GetGlobalErrorHandler().HandlePanic(ctx, w)
}
