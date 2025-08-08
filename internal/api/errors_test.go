package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGitAPIError(t *testing.T) {
	tests := []struct {
		name     string
		code     ErrorCode
		message  string
		severity ErrorSeverity
	}{
		{
			name:     "Create basic error",
			code:     ErrorCodeBadRequest,
			message:  "Invalid request",
			severity: ErrorSeverityLow,
		},
		{
			name:     "Create repository error",
			code:     ErrorCodeRepositoryNotFound,
			message:  "Repository does not exist",
			severity: ErrorSeverityMedium,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewGitAPIError(tt.code, tt.message, tt.severity)

			assert.Equal(t, tt.code, err.Code)
			assert.Equal(t, tt.message, err.Message)
			assert.Equal(t, tt.severity, err.Severity)
			assert.NotZero(t, err.Timestamp)
			assert.NotNil(t, err.Details)
			assert.Empty(t, err.Details)
		})
	}
}

func TestGitAPIError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *GitAPIError
		expected string
	}{
		{
			name: "Basic error",
			err: &GitAPIError{
				Code:    ErrorCodeBadRequest,
				Message: "Invalid input",
			},
			expected: "bad_request: Invalid input",
		},
		{
			name: "Error with operation",
			err: &GitAPIError{
				Code:      ErrorCodeNotFound,
				Message:   "File not found",
				Operation: "get_file",
			},
			expected: "not_found: File not found (operation: get_file)",
		},
		{
			name: "Error with operation and resource",
			err: &GitAPIError{
				Code:      ErrorCodePermissionDenied,
				Message:   "Access denied",
				Operation: "write_file",
				Resource:  "src/main.go",
			},
			expected: "permission_denied: Access denied (operation: write_file, resource: src/main.go)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

func TestGitAPIError_HTTPStatusCode(t *testing.T) {
	tests := []struct {
		name     string
		code     ErrorCode
		expected int
	}{
		{ErrorCodeBadRequest.String(), ErrorCodeBadRequest, http.StatusBadRequest},
		{ErrorCodeUnauthorized.String(), ErrorCodeUnauthorized, http.StatusUnauthorized},
		{ErrorCodeForbidden.String(), ErrorCodeForbidden, http.StatusForbidden},
		{ErrorCodeNotFound.String(), ErrorCodeNotFound, http.StatusNotFound},
		{ErrorCodeConflict.String(), ErrorCodeConflict, http.StatusConflict},
		{ErrorCodeFileTooBig.String(), ErrorCodeFileTooBig, http.StatusRequestEntityTooLarge},
		{ErrorCodeRateLimited.String(), ErrorCodeRateLimited, http.StatusTooManyRequests},
		{ErrorCodeOperationNotSupported.String(), ErrorCodeOperationNotSupported, http.StatusNotImplemented},
		{ErrorCodeInternalError.String(), ErrorCodeInternalError, http.StatusInternalServerError},
		{"unknown_error", ErrorCode("unknown_error"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &GitAPIError{Code: tt.code}
			assert.Equal(t, tt.expected, err.HTTPStatusCode())
		})
	}
}

// Helper function since ErrorCode doesn't have a String() method by default
func (e ErrorCode) String() string {
	return string(e)
}

func TestGitAPIError_WithDetails(t *testing.T) {
	err := NewGitAPIError(ErrorCodeBadRequest, "Test error", ErrorSeverityLow)

	// Add details using interface method
	result := err.WithDetails("key1", "value1")
	assert.Equal(t, err, result) // Should return the same instance

	// Check that details were added
	assert.Equal(t, "value1", err.Details["key1"])

	// Add more details
	_ = err.WithDetails("key2", 42)
	assert.Equal(t, 42, err.Details["key2"])
}

func TestGitAPIError_WithTrace(t *testing.T) {
	err := NewGitAPIError(ErrorCodeInternalError, "Test error", ErrorSeverityHigh)

	result := err.WithTrace("trace-123", "req-456")
	assert.Equal(t, err, result) // Should return the same instance

	assert.Equal(t, "trace-123", err.TraceID)
	assert.Equal(t, "req-456", err.RequestID)
}

func TestGitAPIError_WithOperation(t *testing.T) {
	err := NewGitAPIError(ErrorCodeNotFound, "Test error", ErrorSeverityLow)

	result := err.WithOperation("test_operation")
	assert.Equal(t, err, result) // Should return the same instance

	assert.Equal(t, "test_operation", err.Operation)
}

func TestGitAPIError_WithResource(t *testing.T) {
	err := NewGitAPIError(ErrorCodePermissionDenied, "Test error", ErrorSeverityMedium)

	result := err.WithResource("test_resource")
	assert.Equal(t, err, result) // Should return the same instance

	assert.Equal(t, "test_resource", err.Resource)
}

func TestGitAPIError_WithCause(t *testing.T) {
	originalErr := errors.New("original error")
	err := NewGitAPIError(ErrorCodeInternalError, "Wrapped error", ErrorSeverityHigh)

	result := err.WithCause(originalErr)
	assert.Equal(t, err, result) // Should return the same instance

	assert.Equal(t, originalErr, err.Cause)
}

func TestGitAPIError_Unwrap(t *testing.T) {
	originalErr := errors.New("original error")
	err := NewGitAPIError(ErrorCodeInternalError, "Wrapped error", ErrorSeverityHigh)
	err.Cause = originalErr

	assert.Equal(t, originalErr, err.Unwrap())

	// Test with no cause
	err2 := NewGitAPIError(ErrorCodeBadRequest, "No cause", ErrorSeverityLow)
	assert.Nil(t, err2.Unwrap())
}

func TestGitAPIError_Is(t *testing.T) {
	err1 := NewGitAPIError(ErrorCodeNotFound, "Error 1", ErrorSeverityLow)
	err2 := NewGitAPIError(ErrorCodeNotFound, "Error 2", ErrorSeverityMedium)
	err3 := NewGitAPIError(ErrorCodeBadRequest, "Error 3", ErrorSeverityLow)
	otherErr := errors.New("different error")

	// Same error code should match
	assert.True(t, err1.Is(err2))

	// Different error code should not match
	assert.False(t, err1.Is(err3))

	// Non-GitAPIError should not match
	assert.False(t, err1.Is(otherErr))
}

func TestGitAPIError_InterfaceImplementation(t *testing.T) {
	err := NewGitAPIError(ErrorCodeBadRequest, "Test error", ErrorSeverityLow)

	// Test that it implements GitAPIErrorInterface
	var apiErr GitAPIErrorInterface = err

	assert.Equal(t, ErrorCodeBadRequest, apiErr.GetCode())
	assert.Equal(t, "Test error", apiErr.GetMessage())
	assert.Equal(t, ErrorSeverityLow, apiErr.GetSeverity())
	assert.NotNil(t, apiErr.GetDetails())
	assert.Equal(t, http.StatusBadRequest, apiErr.HTTPStatusCode())
}

func TestPredefinedErrorConstructors(t *testing.T) {
	t.Run("NewInternalError", func(t *testing.T) {
		err := NewInternalError("Something went wrong")
		assert.Equal(t, ErrorCodeInternalError, err.Code)
		assert.Equal(t, "Something went wrong", err.Message)
		assert.Equal(t, ErrorSeverityHigh, err.Severity)
	})

	t.Run("NewBadRequestError", func(t *testing.T) {
		err := NewBadRequestError("Invalid input")
		assert.Equal(t, ErrorCodeBadRequest, err.Code)
		assert.Equal(t, "Invalid input", err.Message)
		assert.Equal(t, ErrorSeverityLow, err.Severity)
	})

	t.Run("NewNotFoundError", func(t *testing.T) {
		err := NewNotFoundError("file", "File not found")
		assert.Equal(t, ErrorCodeNotFound, err.Code)
		assert.Equal(t, "File not found", err.Message)
		assert.Equal(t, "file", err.Resource)
		assert.Equal(t, ErrorSeverityLow, err.Severity)
	})

	t.Run("NewValidationError", func(t *testing.T) {
		err := NewValidationError("Validation failed")
		assert.Equal(t, ErrorCodeValidationFailed, err.Code)
		assert.Equal(t, "Validation failed", err.Message)
		assert.Equal(t, ErrorSeverityLow, err.Severity)
	})

	t.Run("NewPermissionError", func(t *testing.T) {
		err := NewPermissionError("write", "repo/file.txt")
		assert.Equal(t, ErrorCodePermissionDenied, err.Code)
		assert.Contains(t, err.Message, "Permission denied")
		assert.Contains(t, err.Message, "write")
		assert.Contains(t, err.Message, "repo/file.txt")
		assert.Equal(t, "write", err.Operation)
		assert.Equal(t, "repo/file.txt", err.Resource)
		assert.Equal(t, ErrorSeverityMedium, err.Severity)
	})

	t.Run("NewRepositoryNotFoundError", func(t *testing.T) {
		err := NewRepositoryNotFoundError("owner", "repo")
		assert.Equal(t, ErrorCodeRepositoryNotFound, err.Code)
		assert.Contains(t, err.Message, "owner/repo")
		assert.Equal(t, "owner/repo", err.Resource)
		assert.Equal(t, ErrorSeverityLow, err.Severity)
	})

	t.Run("NewBranchNotFoundError", func(t *testing.T) {
		err := NewBranchNotFoundError("main")
		assert.Equal(t, ErrorCodeBranchNotFound, err.Code)
		assert.Contains(t, err.Message, "main")
		assert.Equal(t, "main", err.Resource)
		assert.Equal(t, ErrorSeverityLow, err.Severity)
	})

	t.Run("NewCommitNotFoundError", func(t *testing.T) {
		err := NewCommitNotFoundError("abc123")
		assert.Equal(t, ErrorCodeCommitNotFound, err.Code)
		assert.Contains(t, err.Message, "abc123")
		assert.Equal(t, "abc123", err.Resource)
		assert.Equal(t, ErrorSeverityLow, err.Severity)
	})

	t.Run("NewFileNotFoundError", func(t *testing.T) {
		err := NewFileNotFoundError("src/main.go")
		assert.Equal(t, ErrorCodeFileNotFound, err.Code)
		assert.Contains(t, err.Message, "src/main.go")
		assert.Equal(t, "src/main.go", err.Resource)
		assert.Equal(t, ErrorSeverityLow, err.Severity)
	})

	t.Run("NewMergeConflictError", func(t *testing.T) {
		err := NewMergeConflictError("main", "feature")
		assert.Equal(t, ErrorCodeMergeConflict, err.Code)
		assert.Contains(t, err.Message, "main")
		assert.Contains(t, err.Message, "feature")
		assert.Equal(t, "main", err.Details["source"])
		assert.Equal(t, "feature", err.Details["target"])
		assert.Equal(t, ErrorSeverityMedium, err.Severity)
	})

	t.Run("NewPathTraversalError", func(t *testing.T) {
		err := NewPathTraversalError("../../../etc/passwd")
		assert.Equal(t, ErrorCodePathTraversal, err.Code)
		assert.Contains(t, err.Message, "../../../etc/passwd")
		assert.Equal(t, "../../../etc/passwd", err.Resource)
		assert.Equal(t, ErrorSeverityCritical, err.Severity)
	})

	t.Run("NewFileTooBigError", func(t *testing.T) {
		err := NewFileTooBigError("large.bin", 1000000, 500000)
		assert.Equal(t, ErrorCodeFileTooBig, err.Code)
		assert.Contains(t, err.Message, "large.bin")
		assert.Contains(t, err.Message, "1000000")
		assert.Contains(t, err.Message, "500000")
		assert.Equal(t, "large.bin", err.Resource)
		assert.Equal(t, int64(1000000), err.Details["size"])
		assert.Equal(t, int64(500000), err.Details["max_size"])
		assert.Equal(t, ErrorSeverityLow, err.Severity)
	})
}

func TestGitAPIError_JSONSerialization(t *testing.T) {
	err := NewGitAPIError(ErrorCodeBadRequest, "Test error", ErrorSeverityLow)
	err.TraceID = "trace-123"
	err.RequestID = "req-456"
	err.Operation = "test_op"
	err.Resource = "test_resource"
	err.Details["key"] = "value"

	// Test JSON marshaling
	data, jsonErr := json.Marshal(err)
	require.NoError(t, jsonErr)

	// Test JSON unmarshaling
	var unmarshaled GitAPIError
	jsonErr = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, jsonErr)

	// Verify fields (note: Cause is not serialized)
	assert.Equal(t, err.Code, unmarshaled.Code)
	assert.Equal(t, err.Message, unmarshaled.Message)
	assert.Equal(t, err.Severity, unmarshaled.Severity)
	assert.Equal(t, err.TraceID, unmarshaled.TraceID)
	assert.Equal(t, err.RequestID, unmarshaled.RequestID)
	assert.Equal(t, err.Operation, unmarshaled.Operation)
	assert.Equal(t, err.Resource, unmarshaled.Resource)
	assert.Equal(t, err.Details, unmarshaled.Details)

	// Cause should not be serialized
	assert.Nil(t, unmarshaled.Cause)
}

func TestErrorCode_Coverage(t *testing.T) {
	// Test that all error codes have reasonable HTTP status mappings
	codes := []ErrorCode{
		ErrorCodeInternalError,
		ErrorCodeBadRequest,
		ErrorCodeUnauthorized,
		ErrorCodeForbidden,
		ErrorCodeNotFound,
		ErrorCodeConflict,
		ErrorCodeValidationFailed,
		ErrorCodeRateLimited,
		ErrorCodeRepositoryNotFound,
		ErrorCodeBranchNotFound,
		ErrorCodeCommitNotFound,
		ErrorCodeFileNotFound,
		ErrorCodeReferenceNotFound,
		ErrorCodeInvalidReference,
		ErrorCodeInvalidCommit,
		ErrorCodeMergeConflict,
		ErrorCodeBranchExists,
		ErrorCodeFileExists,
		ErrorCodeInvalidPath,
		ErrorCodePathTraversal,
		ErrorCodeFileTooBig,
		ErrorCodeInvalidFileContent,
		ErrorCodeRepositoryCorrupted,
		ErrorCodeOperationNotSupported,
		ErrorCodePermissionDenied,
		ErrorCodeInsufficientAccess,
		ErrorCodeSecurityViolation,
	}

	for _, code := range codes {
		t.Run(string(code), func(t *testing.T) {
			err := &GitAPIError{Code: code}
			statusCode := err.HTTPStatusCode()

			// Status code should be a valid HTTP status
			assert.True(t, statusCode >= 400 && statusCode < 600,
				"Error code %s should map to a valid HTTP error status (4xx or 5xx), got %d", code, statusCode)
		})
	}
}

func TestErrorSeverity_Coverage(t *testing.T) {
	severities := []ErrorSeverity{
		ErrorSeverityLow,
		ErrorSeverityMedium,
		ErrorSeverityHigh,
		ErrorSeverityCritical,
	}

	for _, severity := range severities {
		t.Run(string(severity), func(t *testing.T) {
			err := NewGitAPIError(ErrorCodeInternalError, "Test", severity)
			assert.Equal(t, severity, err.Severity)
			assert.Equal(t, severity, err.GetSeverity())
		})
	}
}
