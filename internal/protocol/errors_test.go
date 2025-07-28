package protocol

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockLogger implements the Logger interface for testing
type MockLogger struct {
	errorCalls    []LogCall
	protocolCalls []LogCall
}

type LogCall struct {
	Operation string
	ErrorType string
	Message   string
	Details   map[string]interface{}
	Event     string
	Data      map[string]interface{}
}

func (ml *MockLogger) LogError(operation, errorType, message string, details map[string]interface{}) {
	ml.errorCalls = append(ml.errorCalls, LogCall{
		Operation: operation,
		ErrorType: errorType,
		Message:   message,
		Details:   details,
	})
}

func (ml *MockLogger) LogProtocol(event string, data map[string]interface{}) {
	ml.protocolCalls = append(ml.protocolCalls, LogCall{
		Event: event,
		Data:  data,
	})
}

func TestGitProtocolError(t *testing.T) {
	t.Run("Error_Interface", func(t *testing.T) {
		err := &GitProtocolError{
			Type:        "test_error",
			Message:     "Test error message",
			HTTPStatus:  400,
			ProtocolErr: false,
		}

		assert.Equal(t, "Test error message", err.Error())
	})

	t.Run("NewGitProtocolError", func(t *testing.T) {
		err := NewGitProtocolError("custom_error", "Custom message", 422, true)

		assert.Equal(t, "custom_error", err.Type)
		assert.Equal(t, "Custom message", err.Message)
		assert.Equal(t, 422, err.HTTPStatus)
		assert.True(t, err.ProtocolErr)
	})
}

func TestErrorHandler(t *testing.T) {
	t.Run("HandleError_GitProtocolError", func(t *testing.T) {
		mockLogger := &MockLogger{}
		handler := NewErrorHandler(mockLogger)

		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("X-USER-ID", "test-user")
		req.Header.Set("X-REQUEST-ID", "test-123")

		gitErr := ErrPermissionDenied
		handler.HandleError(w, req, gitErr, "test_operation")

		// Check response
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, w.Body.String(), "Access denied to repository")

		// Check logging
		require.Len(t, mockLogger.errorCalls, 1)
		logCall := mockLogger.errorCalls[0]
		assert.Equal(t, "test_operation", logCall.Operation)
		assert.Equal(t, "permission_denied", logCall.ErrorType)
		assert.Equal(t, "Access denied to repository", logCall.Message)
	})

	t.Run("HandleError_UnknownError", func(t *testing.T) {
		mockLogger := &MockLogger{}
		handler := NewErrorHandler(mockLogger)

		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/test", nil)

		unknownErr := errors.New("unknown error")
		handler.HandleError(w, req, unknownErr, "test_operation")

		// Check response
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Contains(t, w.Body.String(), "unknown error")

		// Check logging
		require.Len(t, mockLogger.errorCalls, 1)
		logCall := mockLogger.errorCalls[0]
		assert.Equal(t, "test_operation", logCall.Operation)
		assert.Equal(t, "unknown_error", logCall.ErrorType)
	})

	t.Run("HandleError_ProtocolError", func(t *testing.T) {
		mockLogger := &MockLogger{}
		handler := NewErrorHandler(mockLogger)

		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/test", nil)

		protocolErr := &GitProtocolError{
			Type:        "protocol_test",
			Message:     "Protocol error",
			HTTPStatus:  http.StatusOK,
			ProtocolErr: true,
		}

		handler.HandleError(w, req, protocolErr, "upload_pack")

		// Check response
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/x-git-upload-pack-result", w.Header().Get("Content-Type"))

		// Check pkt-line format in response
		body := w.Body.Bytes()
		assert.True(t, len(body) > 4)

		// Should start with length header
		lengthHeader := string(body[:4])
		assert.Regexp(t, "^[0-9a-f]{4}$", lengthHeader)
	})
}

func TestPktLineWriter(t *testing.T) {
	t.Run("WriteLine_ValidData", func(t *testing.T) {
		var buf bytes.Buffer
		writer := newPktLineWriter(&buf)

		err := writer.writeLine("test data\n")
		require.NoError(t, err)

		result := buf.Bytes()
		expected := "000etest data\n"
		assert.Equal(t, expected, string(result))
	})

	t.Run("WriteLine_EmptyData", func(t *testing.T) {
		var buf bytes.Buffer
		writer := newPktLineWriter(&buf)

		err := writer.writeLine("")
		require.NoError(t, err)

		result := buf.Bytes()
		// Empty string is treated as flush packet (Git protocol compliance)
		expected := "0000"
		assert.Equal(t, expected, string(result))
	})

	t.Run("Flush", func(t *testing.T) {
		var buf bytes.Buffer
		writer := newPktLineWriter(&buf)

		err := writer.flush()
		require.NoError(t, err)

		result := buf.Bytes()
		expected := "0000"
		assert.Equal(t, expected, string(result))
	})

	t.Run("WriteLine_TooLong", func(t *testing.T) {
		var buf bytes.Buffer
		writer := newPktLineWriter(&buf)

		longData := make([]byte, 65520)
		err := writer.writeLine(string(longData))

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "packet line too long")
	})
}

func TestValidateContentType(t *testing.T) {
	testCases := []struct {
		name          string
		method        string
		contentType   string
		expectedTypes []string
		expectError   bool
	}{
		{
			name:          "GET_Request_NoValidation",
			method:        "GET",
			contentType:   "",
			expectedTypes: []string{"application/x-git-upload-pack-request"},
			expectError:   false,
		},
		{
			name:          "POST_ValidContentType",
			method:        "POST",
			contentType:   "application/x-git-upload-pack-request",
			expectedTypes: []string{"application/x-git-upload-pack-request"},
			expectError:   false,
		},
		{
			name:          "POST_InvalidContentType",
			method:        "POST",
			contentType:   "application/json",
			expectedTypes: []string{"application/x-git-upload-pack-request"},
			expectError:   true,
		},
		{
			name:          "POST_MissingContentType",
			method:        "POST",
			contentType:   "",
			expectedTypes: []string{"application/x-git-upload-pack-request"},
			expectError:   true,
		},
		{
			name:          "POST_ContentTypeWithCharset",
			method:        "POST",
			contentType:   "application/x-git-upload-pack-request; charset=utf-8",
			expectedTypes: []string{"application/x-git-upload-pack-request"},
			expectError:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/test", nil)
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}

			err := ValidateContentType(req, tc.expectedTypes)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateGatewayRequest(t *testing.T) {
	t.Run("ValidRequest", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("X-USER-ID", "test-user")

		err := ValidateGatewayRequest(req)
		assert.NoError(t, err)
	})

	t.Run("MissingUserID", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)

		err := ValidateGatewayRequest(req)
		assert.Error(t, err)
		assert.Equal(t, ErrMissingUserContext, err)
	})
}

func TestValidateServiceParameter(t *testing.T) {
	testCases := []struct {
		name            string
		service         string
		allowedServices []string
		expectError     bool
	}{
		{
			name:            "ValidService",
			service:         "git-upload-pack",
			allowedServices: []string{"git-upload-pack", "git-receive-pack"},
			expectError:     false,
		},
		{
			name:            "InvalidService",
			service:         "invalid-service",
			allowedServices: []string{"git-upload-pack", "git-receive-pack"},
			expectError:     true,
		},
		{
			name:            "EmptyService",
			service:         "",
			allowedServices: []string{"git-upload-pack"},
			expectError:     true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateServiceParameter(tc.service, tc.allowedServices)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestWrapRepositoryError(t *testing.T) {
	testCases := []struct {
		name           string
		inputError     error
		repoPath       string
		expectedType   string
		expectedStatus int
	}{
		{
			name:           "RepositoryNotFound",
			inputError:     errors.New("repository does not exist"),
			repoPath:       "/path/to/repo",
			expectedType:   "repository_not_found",
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "PermissionDenied",
			inputError:     errors.New("permission denied"),
			repoPath:       "/path/to/repo",
			expectedType:   "repository_access_denied",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "InvalidRepository",
			inputError:     errors.New("not a git repository"),
			repoPath:       "/path/to/repo",
			expectedType:   "invalid_repository",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "UnknownError",
			inputError:     errors.New("some other error"),
			repoPath:       "/path/to/repo",
			expectedType:   "repository_error",
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name:           "NilError",
			inputError:     nil,
			repoPath:       "/path/to/repo",
			expectedType:   "",
			expectedStatus: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := WrapRepositoryError(tc.inputError, tc.repoPath)

			if tc.inputError == nil {
				assert.NoError(t, result)
				return
			}

			require.Error(t, result)

			var gitErr *GitProtocolError
			require.True(t, errors.As(result, &gitErr))
			assert.Equal(t, tc.expectedType, gitErr.Type)
			assert.Equal(t, tc.expectedStatus, gitErr.HTTPStatus)
		})
	}
}

func TestWrapPermissionError(t *testing.T) {
	testCases := []struct {
		name           string
		inputError     error
		operation      string
		expectedType   string
		expectedStatus int
	}{
		{
			name:           "PermissionDenied",
			inputError:     errors.New("access denied"),
			operation:      "read",
			expectedType:   "permission_denied",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "ServiceUnavailable",
			inputError:     errors.New("service unavailable"),
			operation:      "read",
			expectedType:   "permission_service_unavailable",
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name:           "UnknownError",
			inputError:     errors.New("some other error"),
			operation:      "read",
			expectedType:   "permission_check_failed",
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name:           "NilError",
			inputError:     nil,
			operation:      "read",
			expectedType:   "",
			expectedStatus: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := WrapPermissionError(tc.inputError, tc.operation)

			if tc.inputError == nil {
				assert.NoError(t, result)
				return
			}

			require.Error(t, result)

			var gitErr *GitProtocolError
			require.True(t, errors.As(result, &gitErr))
			assert.Equal(t, tc.expectedType, gitErr.Type)
			assert.Equal(t, tc.expectedStatus, gitErr.HTTPStatus)
		})
	}
}

func TestPredefinedErrors(t *testing.T) {
	testCases := []struct {
		name   string
		err    *GitProtocolError
		status int
	}{
		{"ErrInvalidService", ErrInvalidService, http.StatusBadRequest},
		{"ErrMissingUserContext", ErrMissingUserContext, http.StatusUnauthorized},
		{"ErrPermissionDenied", ErrPermissionDenied, http.StatusForbidden},
		{"ErrRepositoryNotFound", ErrRepositoryNotFound, http.StatusNotFound},
		{"ErrInvalidRequest", ErrInvalidRequest, http.StatusBadRequest},
		{"ErrInvalidContentType", ErrInvalidContentType, http.StatusBadRequest},
		{"ErrInternalServer", ErrInternalServer, http.StatusInternalServerError},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.status, tc.err.HTTPStatus)
			assert.NotEmpty(t, tc.err.Type)
			assert.NotEmpty(t, tc.err.Message)
			assert.NotEmpty(t, tc.err.Error())
		})
	}
}
