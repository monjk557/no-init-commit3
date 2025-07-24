package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestAppError_Comprehensive(t *testing.T) {
	t.Run("BasicErrorCreation", func(t *testing.T) {
		err := &AppError{
			Type:        ErrorTypeValidation,
			Code:        "INVALID_INPUT",
			Message:     "Invalid input provided",
			StatusCode:  400,
			Retryable:   false,
			UserMessage: "Please check your input",
		}

		assert.Equal(t, ErrorTypeValidation, err.Type)
		assert.Equal(t, "INVALID_INPUT", err.Code)
		assert.Equal(t, "Invalid input provided", err.Message)
		assert.Equal(t, 400, err.StatusCode)
		assert.False(t, err.Retryable)
		assert.Equal(t, "Please check your input", err.UserMessage)

		// Test Error() method
		assert.Equal(t, "INVALID_INPUT: Invalid input provided", err.Error())
	})

	t.Run("ErrorWithCause", func(t *testing.T) {
		cause := errors.New("database connection failed")
		err := &AppError{
			Type:    ErrorTypeInternal,
			Code:    "DB_ERROR",
			Message: "Database operation failed",
			Cause:   cause,
		}

		assert.Equal(t, "DB_ERROR: Database operation failed (caused by: database connection failed)", err.Error())
		assert.Equal(t, cause, err.Unwrap())
	})

	t.Run("ErrorBuilderPattern", func(t *testing.T) {
		cause := errors.New("network timeout")
		err := NewErrorBuilder(ErrorTypeExternal, "NETWORK_ERROR", "External service unavailable").
			WithDetails("Connection timeout after 30 seconds").
			WithCause(cause).
			WithContext("service", "api-service").
			WithContext("timeout", "30s").
			WithStatusCode(502).
			WithRetryable(true).
			WithUserMessage("Service temporarily unavailable").
			Build()

		assert.Equal(t, ErrorTypeExternal, err.Type)
		assert.Equal(t, "NETWORK_ERROR", err.Code)
		assert.Equal(t, "External service unavailable", err.Message)
		assert.Equal(t, "Connection timeout after 30 seconds", err.Details)
		assert.Equal(t, cause, err.Cause)
		assert.Equal(t, 502, err.StatusCode)
		assert.True(t, err.Retryable)
		assert.Equal(t, "Service temporarily unavailable", err.UserMessage)
		assert.Equal(t, "api-service", err.Context["service"])
		assert.Equal(t, "30s", err.Context["timeout"])
	})

	t.Run("FluentInterface", func(t *testing.T) {
		err := &AppError{Type: ErrorTypeNotFound, Code: "NOT_FOUND", Message: "Resource not found"}

		modified := err.
			WithContext("resource_id", "123").
			WithContext("resource_type", "repository").
			WithCause(errors.New("file not found")).
			WithRequestID("req-456").
			WithDetails("Repository does not exist in filesystem")

		// Should return the same instance (fluent interface)
		assert.Equal(t, err, modified)
		assert.Equal(t, "123", err.Context["resource_id"])
		assert.Equal(t, "repository", err.Context["resource_type"])
		assert.NotNil(t, err.Cause)
		assert.Equal(t, "req-456", err.RequestID)
		assert.Equal(t, "Repository does not exist in filesystem", err.Details)
	})
}

func TestErrorConstructors_PRDScenarios(t *testing.T) {
	// Test error constructors for PRD-specific scenarios
	testCases := []struct {
		name              string
		constructor       func() *AppError
		expectedType      ErrorType
		expectedStatus    int
		expectedRetryable bool
	}{
		{
			name: "ValidationError - Invalid Repository Path",
			constructor: func() *AppError {
				return NewValidationError("INVALID_REPO_PATH", "Repository path contains invalid characters")
			},
			expectedType:      ErrorTypeValidation,
			expectedStatus:    400,
			expectedRetryable: false,
		},
		{
			name: "NotFoundError - Repository Not Found",
			constructor: func() *AppError {
				return NewNotFoundError("REPO_NOT_FOUND", "Repository does not exist")
			},
			expectedType:      ErrorTypeNotFound,
			expectedStatus:    404,
			expectedRetryable: false,
		},
		{
			name: "UnauthorizedError - JWT Validation Failed",
			constructor: func() *AppError {
				return NewUnauthorizedError("JWT_INVALID", "JWT token validation failed")
			},
			expectedType:      ErrorTypeUnauthorized,
			expectedStatus:    401,
			expectedRetryable: false,
		},
		{
			name: "ForbiddenError - Access Control Violation",
			constructor: func() *AppError {
				return NewForbiddenError("ACCESS_DENIED", "Component not authorized for this endpoint")
			},
			expectedType:      ErrorTypeForbidden,
			expectedStatus:    403,
			expectedRetryable: false,
		},
		{
			name: "InternalError - Git Operation Failed",
			constructor: func() *AppError {
				return NewInternalError("GIT_ERROR", "Git operation failed")
			},
			expectedType:      ErrorTypeInternal,
			expectedStatus:    500,
			expectedRetryable: true,
		},
		{
			name: "ExternalError - Permission API Unavailable",
			constructor: func() *AppError {
				return NewExternalError("PERMISSION_API_ERROR", "Permission service unavailable")
			},
			expectedType:      ErrorTypeExternal,
			expectedStatus:    502,
			expectedRetryable: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.constructor()

			assert.Equal(t, tc.expectedType, err.Type)
			assert.Equal(t, tc.expectedStatus, err.StatusCode)
			assert.Equal(t, tc.expectedRetryable, err.Retryable)
			assert.NotEmpty(t, err.UserMessage)
		})
	}
}

func TestDefaultStatusCodes(t *testing.T) {
	testCases := []struct {
		errorType      ErrorType
		expectedStatus int
	}{
		{ErrorTypeValidation, 400},
		{ErrorTypeNotFound, 404},
		{ErrorTypeUnauthorized, 401},
		{ErrorTypeForbidden, 403},
		{ErrorTypeConflict, 409},
		{ErrorTypeTimeout, 408},
		{ErrorTypeRateLimit, 429},
		{ErrorTypeExternal, 502},
		{ErrorTypeInternal, 500},
		{ErrorType("unknown"), 500}, // Default case
	}

	for _, tc := range testCases {
		t.Run(string(tc.errorType), func(t *testing.T) {
			status := getDefaultStatusCode(tc.errorType)
			assert.Equal(t, tc.expectedStatus, status)
		})
	}
}

func TestErrorHandler_Comprehensive(t *testing.T) {
	var logBuf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			MessageKey:  "message",
			LevelKey:    "level",
			EncodeLevel: zapcore.LowercaseLevelEncoder,
		}),
		zapcore.AddSync(&logBuf),
		zapcore.DebugLevel,
	)
	logger := &Logger{Logger: zap.New(core)}

	handler := NewErrorHandler(logger)

	t.Run("HandleAppError", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), RequestIDKey, "test-req-123")
		w := httptest.NewRecorder()

		appErr := NewValidationError("INVALID_PATH", "Invalid repository path").
			WithDetails("Path contains '..' which is not allowed").
			WithContext("path", "../etc/passwd").
			WithRequestID("test-req-123")

		handler.Handle(ctx, w, appErr)

		// Verify HTTP response
		assert.Equal(t, 400, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

		// Parse response body
		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)

		errorData := response["error"].(map[string]interface{})
		assert.Equal(t, "validation", errorData["type"])
		assert.Equal(t, "INVALID_PATH", errorData["code"])
		assert.Equal(t, "Invalid input provided", errorData["message"]) // User message
		assert.Equal(t, "test-req-123", response["request_id"])

		// Verify logging
		logOutput := logBuf.String()
		assert.Contains(t, logOutput, "Client error")
		assert.Contains(t, logOutput, "warn")
		assert.Contains(t, logOutput, "INVALID_PATH")
	})

	t.Run("HandleUnknownError", func(t *testing.T) {
		logBuf.Reset()
		ctx := context.WithValue(context.Background(), RequestIDKey, "unknown-req-456")
		w := httptest.NewRecorder()

		unknownErr := errors.New("some unexpected error")
		handler.Handle(ctx, w, unknownErr)

		// Should wrap as internal error
		assert.Equal(t, 500, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)

		errorData := response["error"].(map[string]interface{})
		assert.Equal(t, "internal", errorData["type"])
		assert.Equal(t, "UNKNOWN_ERROR", errorData["code"])
		assert.Equal(t, "unknown-req-456", response["request_id"])

		// Verify logging
		logOutput := logBuf.String()
		assert.Contains(t, logOutput, "Server error")
		assert.Contains(t, logOutput, "error")
	})

	t.Run("LoggingByErrorType", func(t *testing.T) {
		testCases := []struct {
			name          string
			errorType     ErrorType
			expectedLevel string
		}{
			{"ValidationError", ErrorTypeValidation, "warn"},
			{"NotFoundError", ErrorTypeNotFound, "warn"},
			{"UnauthorizedError", ErrorTypeUnauthorized, "warn"},
			{"ForbiddenError", ErrorTypeForbidden, "warn"},
			{"InternalError", ErrorTypeInternal, "error"},
			{"ExternalError", ErrorTypeExternal, "error"},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				logBuf.Reset()
				ctx := context.Background()
				w := httptest.NewRecorder()

				err := &AppError{
					Type:       tc.errorType,
					Code:       "TEST_ERROR",
					Message:    "Test error message",
					StatusCode: getDefaultStatusCode(tc.errorType),
				}

				handler.Handle(ctx, w, err)

				logOutput := logBuf.String()
				assert.Contains(t, logOutput, tc.expectedLevel)
			})
		}
	})
}

func TestErrorHandler_PanicHandling(t *testing.T) {
	var logBuf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			MessageKey:  "message",
			LevelKey:    "level",
			EncodeLevel: zapcore.LowercaseLevelEncoder,
		}),
		zapcore.AddSync(&logBuf),
		zapcore.ErrorLevel,
	)
	logger := &Logger{Logger: zap.New(core)}
	handler := NewErrorHandler(logger)

	t.Run("HandlePanicWithError", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), RequestIDKey, "panic-req-789")
		w := httptest.NewRecorder()

		// Simulate panic recovery
		func() {
			defer handler.HandlePanic(ctx, w)
			panic(errors.New("test panic error"))
		}()

		// Verify response
		assert.Equal(t, 500, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)

		errorData := response["error"].(map[string]interface{})
		assert.Equal(t, "internal", errorData["type"])
		assert.Equal(t, "PANIC", errorData["code"])

		// Verify logging
		logOutput := logBuf.String()
		assert.Contains(t, logOutput, "Server error")
		assert.Contains(t, logOutput, "test panic error")
	})

	t.Run("HandlePanicWithString", func(t *testing.T) {
		logBuf.Reset()
		ctx := context.Background()
		w := httptest.NewRecorder()

		func() {
			defer handler.HandlePanic(ctx, w)
			panic("string panic message")
		}()

		assert.Equal(t, 500, w.Code)

		logOutput := logBuf.String()
		assert.Contains(t, logOutput, "string panic message")
	})
}

func TestGlobalErrorHandler(t *testing.T) {
	// Save original global error handler
	originalHandler := globalErrorHandler
	defer func() { globalErrorHandler = originalHandler }()

	t.Run("InitGlobalErrorHandler", func(t *testing.T) {
		logger := &Logger{Logger: zap.NewNop()}
		InitGlobalErrorHandler(logger)

		handler := GetGlobalErrorHandler()
		assert.NotNil(t, handler)
		assert.Equal(t, globalErrorHandler, handler)
	})

	t.Run("GetGlobalErrorHandler_Fallback", func(t *testing.T) {
		globalErrorHandler = nil

		handler := GetGlobalErrorHandler()
		assert.NotNil(t, handler)
		assert.NotNil(t, globalErrorHandler)
	})

	t.Run("GlobalHelperFunctions", func(t *testing.T) {
		var logBuf bytes.Buffer
		core := zapcore.NewCore(
			zapcore.NewJSONEncoder(zapcore.EncoderConfig{
				MessageKey:  "message",
				LevelKey:    "level",
				EncodeLevel: zapcore.LowercaseLevelEncoder,
			}),
			zapcore.AddSync(&logBuf),
			zapcore.ErrorLevel,
		)
		logger := &Logger{Logger: zap.New(core)}
		InitGlobalErrorHandler(logger)

		ctx := context.Background()
		w := httptest.NewRecorder()

		// Test HandleError global function
		err := NewInternalError("TEST_ERROR", "Test error")
		HandleError(ctx, w, err)

		assert.Equal(t, 500, w.Code)
		assert.Contains(t, logBuf.String(), "Server error")

		// Note: HandlePanic global function is tested via ErrorHandler.HandlePanic
		// since it requires proper global error handler initialization in production
	})
}

func TestErrorHandler_JSONEncodingFailure(t *testing.T) {
	// Test fallback when JSON encoding fails
	logger := &Logger{Logger: zap.NewNop()}
	handler := NewErrorHandler(logger)

	// Create a response writer that fails JSON encoding
	w := &failingResponseWriter{}
	ctx := context.Background()
	err := NewInternalError("TEST_ERROR", "Test error")

	handler.Handle(ctx, w, err)

	// Should fall back to plain text
	assert.Equal(t, 500, w.statusCode)
	assert.Contains(t, w.body, "Internal Server Error")
}

// Mock response writer that fails JSON encoding
type failingResponseWriter struct {
	header     http.Header
	body       string
	statusCode int
}

func (w *failingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failingResponseWriter) Write(data []byte) (int, error) {
	w.body += string(data)
	return 0, errors.New("write error") // Simulate encoding failure
}

func (w *failingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func TestErrorType_String(t *testing.T) {
	// Test string representation of error types
	types := []ErrorType{
		ErrorTypeValidation,
		ErrorTypeNotFound,
		ErrorTypeUnauthorized,
		ErrorTypeForbidden,
		ErrorTypeConflict,
		ErrorTypeInternal,
		ErrorTypeExternal,
		ErrorTypeTimeout,
		ErrorTypeRateLimit,
	}

	for _, errorType := range types {
		assert.NotEmpty(t, string(errorType))
	}
}

func TestAppError_ContextManagement(t *testing.T) {
	err := &AppError{
		Type:    ErrorTypeInternal,
		Code:    "TEST_ERROR",
		Message: "Test error",
	}

	// Test context initialization
	_ = err.WithContext("key1", "value1")
	assert.NotNil(t, err.Context)
	assert.Equal(t, "value1", err.Context["key1"])

	// Test multiple context values
	_ = err.WithContext("key2", 123).WithContext("key3", true)
	assert.Equal(t, "value1", err.Context["key1"])
	assert.Equal(t, 123, err.Context["key2"])
	assert.Equal(t, true, err.Context["key3"])
}

// PRD-specific error scenarios
func TestPRDErrorScenarios(t *testing.T) {
	t.Run("JWTValidationFailure", func(t *testing.T) {
		err := NewUnauthorizedError("JWT_INVALID", "JWT token validation failed").
			WithDetails("Token signature verification failed").
			WithContext("jwt_source", "Authorization header").
			WithContext("component", "git-executor")

		assert.Equal(t, ErrorTypeUnauthorized, err.Type)
		assert.Equal(t, 401, err.StatusCode)
		assert.Equal(t, "JWT_INVALID", err.Code)
	})

	t.Run("EndpointAccessViolation", func(t *testing.T) {
		err := NewForbiddenError("ENDPOINT_ACCESS_DENIED", "Component not authorized for this endpoint").
			WithDetails("HTTP Gateway attempted to access REST API endpoint").
			WithContext("source", "http-gateway").
			WithContext("endpoint", "/repos/owner/repo/tree").
			WithContext("allowed_endpoints", []string{"/info/refs", "/git-upload-pack", "/git-receive-pack"})

		assert.Equal(t, ErrorTypeForbidden, err.Type)
		assert.Equal(t, 403, err.StatusCode)
		assert.Equal(t, "http-gateway", err.Context["source"])
	})

	t.Run("RepositoryPathTraversal", func(t *testing.T) {
		err := NewValidationError("PATH_TRAVERSAL", "Repository path contains path traversal attempt").
			WithDetails("Path contains '../' sequence which is forbidden").
			WithContext("requested_path", "/var/lib/git/repos/../etc/passwd").
			WithContext("sanitized_path", "/var/lib/git/repos/")

		assert.Equal(t, ErrorTypeValidation, err.Type)
		assert.Equal(t, 400, err.StatusCode)
	})

	t.Run("PermissionAPITimeout", func(t *testing.T) {
		err := NewExternalError("PERMISSION_API_TIMEOUT", "Permission service timeout").
			WithDetails("API call to permission service exceeded 30 second timeout").
			WithContext("api_endpoint", "http://api-service.local/permissions/user123/owner/repo").
			WithContext("timeout_duration", "30s")

		assert.Equal(t, ErrorTypeExternal, err.Type)
		assert.Equal(t, 502, err.StatusCode)
		assert.True(t, err.Retryable)
	})

	t.Run("GitOperationFailure", func(t *testing.T) {
		gitErr := errors.New("failed to read object")
		err := NewInternalError("GIT_OPERATION_FAILED", "Git operation failed").
			WithCause(gitErr).
			WithDetails("Unable to read git object from repository").
			WithContext("operation", "upload-pack").
			WithContext("repository", "/var/lib/git/repos/owner/repo.git").
			WithContext("object_hash", "abc123def456")

		assert.Equal(t, ErrorTypeInternal, err.Type)
		assert.Equal(t, 500, err.StatusCode)
		assert.Equal(t, gitErr, err.Cause)
		assert.True(t, err.Retryable)
	})
}
