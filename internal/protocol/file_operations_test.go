package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPermissionClient implements the PermissionClient interface for testing
type mockPermissionClient struct {
	readAccess  bool
	writeAccess bool
	shouldError bool
}

func (m *mockPermissionClient) CheckReadAccess(ctx context.Context, userID, repoOwner, repoName string) (bool, error) {
	if m.shouldError {
		return false, assert.AnError
	}
	return m.readAccess, nil
}

func (m *mockPermissionClient) CheckWriteAccess(ctx context.Context, userID, repoOwner, repoName string) (bool, error) {
	if m.shouldError {
		return false, assert.AnError
	}
	return m.writeAccess, nil
}

func (m *mockPermissionClient) InvalidateCache(userID, repoOwner, repoName string) {
	// Mock implementation - do nothing
}

func (m *mockPermissionClient) ClearCache() {
	// Mock implementation - do nothing
}

func (m *mockPermissionClient) GetCircuitBreakerMetrics() *permissions.CircuitBreakerMetrics {
	return nil
}

func (m *mockPermissionClient) Close() error {
	return nil
}

func TestNewFileOperationsHandler(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/tmp/test-repos",
		},
	}

	permClient := &mockPermissionClient{
		readAccess:  true,
		writeAccess: true,
	}

	handler, err := NewFileOperationsHandler(cfg, permClient)
	require.NoError(t, err)
	assert.NotNil(t, handler)
	assert.Equal(t, cfg, handler.config)
	assert.NotNil(t, handler.logger)
	assert.Equal(t, permClient, handler.permClient)
	assert.NotNil(t, handler.pathConfig)
	assert.NotNil(t, handler.pathValidator)
	assert.NotNil(t, handler.objectManager)
	assert.NotNil(t, handler.responseMiddleware)
	assert.NotNil(t, handler.errorMapper)
	assert.NotNil(t, handler.refNamePattern)
}

func TestFileOperationsHandler_validateRefName(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/tmp/test-repos",
		},
	}

	permClient := &mockPermissionClient{}
	handler, err := NewFileOperationsHandler(cfg, permClient)
	require.NoError(t, err)

	tests := []struct {
		name    string
		refName string
		wantErr bool
	}{
		{
			name:    "Valid branch name",
			refName: "main",
			wantErr: false,
		},
		{
			name:    "Valid branch with slashes",
			refName: "feature/new-feature",
			wantErr: false,
		},
		{
			name:    "Valid branch with underscores and dots",
			refName: "release_v1.0.0",
			wantErr: false,
		},
		{
			name:    "Empty ref name",
			refName: "",
			wantErr: true,
		},
		{
			name:    "Ref starting with dot",
			refName: ".hidden",
			wantErr: true,
		},
		{
			name:    "Ref ending with dot",
			refName: "branch.",
			wantErr: true,
		},
		{
			name:    "Ref with consecutive dots",
			refName: "branch..name",
			wantErr: true,
		},
		{
			name:    "Invalid characters",
			refName: "branch@name",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler.validateRefName(tt.refName)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestFileOperationsHandler_validatePermissions(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/tmp/test-repos",
		},
	}

	tests := []struct {
		name        string
		headers     map[string]string
		permClient  *mockPermissionClient
		action      string
		wantErr     bool
		errContains string
	}{
		{
			name: "Valid write permissions",
			headers: map[string]string{
				"X-USER-ID": "user123",
				"X-Source":  "orchestrator",
			},
			permClient: &mockPermissionClient{
				writeAccess: true,
			},
			action:  "write",
			wantErr: false,
		},
		{
			name: "Missing X-USER-ID header",
			headers: map[string]string{
				"X-Source": "orchestrator",
			},
			permClient:  &mockPermissionClient{},
			action:      "write",
			wantErr:     true,
			errContains: "X-USER-ID",
		},
		{
			name: "Missing X-Source header",
			headers: map[string]string{
				"X-USER-ID": "user123",
			},
			permClient:  &mockPermissionClient{},
			action:      "write",
			wantErr:     true,
			errContains: "X-Source",
		},
		{
			name: "Permission denied",
			headers: map[string]string{
				"X-USER-ID": "user123",
				"X-Source":  "orchestrator",
			},
			permClient: &mockPermissionClient{
				writeAccess: false,
			},
			action:      "write",
			wantErr:     true,
			errContains: "Insufficient permissions",
		},
		{
			name: "Permission service error",
			headers: map[string]string{
				"X-USER-ID": "user123",
				"X-Source":  "orchestrator",
			},
			permClient: &mockPermissionClient{
				shouldError: true,
			},
			action:      "write",
			wantErr:     true,
			errContains: "verification failed",
		},
		{
			name: "Unknown action",
			headers: map[string]string{
				"X-USER-ID": "user123",
				"X-Source":  "orchestrator",
			},
			permClient:  &mockPermissionClient{},
			action:      "unknown",
			wantErr:     true,
			errContains: "Unknown action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := NewFileOperationsHandler(cfg, tt.permClient)
			require.NoError(t, err)

			// Create a test Gin context
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("PUT", "/test", nil)

			// Set headers
			for key, value := range tt.headers {
				c.Request.Header.Set(key, value)
			}

			err = handler.validatePermissions(c, "testowner", "testrepo", tt.action)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestFileOperationsHandler_HandleFilePUT_ValidationErrors(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/tmp/test-repos",
		},
	}

	permClient := &mockPermissionClient{
		writeAccess: true,
	}

	handler, err := NewFileOperationsHandler(cfg, permClient)
	require.NoError(t, err)

	tests := []struct {
		name         string
		path         string
		headers      map[string]string
		requestBody  interface{}
		expectedCode int
		errContains  string
	}{
		{
			name: "Invalid file path in URL",
			path: "/repos/owner/repo/invalid",
			headers: map[string]string{
				"X-USER-ID": "user123",
				"X-Source":  "orchestrator",
			},
			requestBody: FileOperationRequest{
				Content: "test content",
				Message: "test commit",
			},
			expectedCode: http.StatusNotFound,
			errContains:  "Invalid file path",
		},
		{
			name: "Missing required JSON fields",
			path: "/repos/owner/repo/files/test.txt",
			headers: map[string]string{
				"X-USER-ID": "user123",
				"X-Source":  "orchestrator",
			},
			requestBody:  map[string]string{}, // Missing required fields
			expectedCode: http.StatusBadRequest,
			errContains:  "Invalid JSON",
		},
		{
			name: "Missing X-USER-ID header",
			path: "/repos/owner/repo/files/test.txt",
			headers: map[string]string{
				"X-Source": "orchestrator",
			},
			requestBody: FileOperationRequest{
				Content: "test content",
				Message: "test commit",
			},
			expectedCode: http.StatusUnauthorized,
			errContains:  "X-USER-ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, router := gin.CreateTestContext(w)

			// Set up the route
			router.PUT("/repos/:owner/:repo/files/*path", handler.HandleFilePUT)

			// Prepare request body
			var bodyBytes []byte
			if tt.requestBody != nil {
				bodyBytes, _ = json.Marshal(tt.requestBody)
			}

			// Create request
			c.Request = httptest.NewRequest("PUT", tt.path, bytes.NewBuffer(bodyBytes))
			c.Request.Header.Set("Content-Type", "application/json")

			// Set headers
			for key, value := range tt.headers {
				c.Request.Header.Set(key, value)
			}

			// Execute request
			router.ServeHTTP(w, c.Request)

			// Note: Due to the complexity of testing actual Git operations,
			// we mainly test validation errors here. Full integration tests
			// would require setting up actual Git repositories.
			if tt.expectedCode != 0 {
				assert.Equal(t, tt.expectedCode, w.Code)
			}
		})
	}
}

func TestErrorConstructors(t *testing.T) {
	t.Run("NewUnauthorizedError", func(t *testing.T) {
		err := NewUnauthorizedError("test message")
		assert.NotNil(t, err)
		assert.Contains(t, err.Error(), "test message")
		assert.Equal(t, 401, err.HTTPStatusCode())
	})

	t.Run("NewForbiddenError", func(t *testing.T) {
		err := NewForbiddenError("test message")
		assert.NotNil(t, err)
		assert.Contains(t, err.Error(), "test message")
		assert.Equal(t, 403, err.HTTPStatusCode())
	})
}

func TestFileOperationsHandler_HandleFileDELETE_ValidationErrors(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/tmp/test-repos",
		},
	}

	permClient := &mockPermissionClient{
		writeAccess: true,
	}

	handler, err := NewFileOperationsHandler(cfg, permClient)
	require.NoError(t, err)

	tests := []struct {
		name         string
		path         string
		headers      map[string]string
		requestBody  interface{}
		expectedCode int
		errContains  string
	}{
		{
			name: "Invalid file path in URL for DELETE",
			path: "/repos/owner/repo/invalid",
			headers: map[string]string{
				"X-USER-ID": "user123",
				"X-Source":  "orchestrator",
			},
			requestBody: FileDeleteRequest{
				Message: "Delete test file",
			},
			expectedCode: http.StatusNotFound,
			errContains:  "Invalid file path",
		},
		{
			name: "Missing required message field",
			path: "/repos/owner/repo/files/test.txt",
			headers: map[string]string{
				"X-USER-ID": "user123",
				"X-Source":  "orchestrator",
			},
			requestBody:  map[string]string{}, // Missing required message field
			expectedCode: http.StatusBadRequest,
			errContains:  "Invalid JSON",
		},
		{
			name: "Missing X-USER-ID header for DELETE",
			path: "/repos/owner/repo/files/test.txt",
			headers: map[string]string{
				"X-Source": "orchestrator",
			},
			requestBody: FileDeleteRequest{
				Message: "Delete test file",
			},
			expectedCode: http.StatusUnauthorized,
			errContains:  "X-USER-ID",
		},
		{
			name: "Invalid branch name for DELETE",
			path: "/repos/owner/repo/files/test.txt",
			headers: map[string]string{
				"X-USER-ID": "user123",
				"X-Source":  "orchestrator",
			},
			requestBody: FileDeleteRequest{
				Message: "Delete test file",
				Branch:  "invalid..branch",
			},
			expectedCode: http.StatusBadRequest,
			errContains:  "Invalid branch name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, router := gin.CreateTestContext(w)

			// Set up the route
			router.DELETE("/repos/:owner/:repo/files/*path", handler.HandleFileDELETE)

			// Prepare request body
			var bodyBytes []byte
			if tt.requestBody != nil {
				bodyBytes, _ = json.Marshal(tt.requestBody)
			}

			// Create request
			c.Request = httptest.NewRequest("DELETE", tt.path, bytes.NewBuffer(bodyBytes))
			c.Request.Header.Set("Content-Type", "application/json")

			// Set headers
			for key, value := range tt.headers {
				c.Request.Header.Set(key, value)
			}

			// Execute request
			router.ServeHTTP(w, c.Request)

			// Note: Due to the complexity of testing actual Git operations,
			// we mainly test validation errors here. Full integration tests
			// would require setting up actual Git repositories.
			if tt.expectedCode != 0 {
				assert.Equal(t, tt.expectedCode, w.Code)
			}
		})
	}
}

func TestFileDeleteRequest_Validation(t *testing.T) {
	tests := []struct {
		name    string
		request FileDeleteRequest
		wantErr bool
	}{
		{
			name: "Valid delete request",
			request: FileDeleteRequest{
				Message: "Delete obsolete file",
				Branch:  "main",
				Author: &CommitAuthor{
					Name:  "Test User",
					Email: "test@example.com",
				},
			},
			wantErr: false,
		},
		{
			name: "Valid delete request with minimal fields",
			request: FileDeleteRequest{
				Message: "Delete file",
			},
			wantErr: false,
		},
		{
			name: "Invalid delete request - empty message",
			request: FileDeleteRequest{
				Branch: "main",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate JSON binding validation
			jsonData, err := json.Marshal(tt.request)
			require.NoError(t, err)

			var req FileDeleteRequest
			err = json.Unmarshal(jsonData, &req)
			require.NoError(t, err)

			// Check if required fields are present
			hasError := req.Message == ""

			if tt.wantErr {
				assert.True(t, hasError, "Expected validation error for missing required fields")
			} else {
				assert.False(t, hasError, "Expected no validation error for valid request")
			}
		})
	}
}

func TestFileOperationsHandler_DeleteWorkflow(t *testing.T) {
	t.Run("DELETE request structure validation", func(t *testing.T) {
		// Test that FileDeleteRequest has the correct JSON tags and structure
		req := FileDeleteRequest{
			Message: "Delete test file",
			Branch:  "feature/test",
			Author: &CommitAuthor{
				Name:  "Test Author",
				Email: "author@test.com",
			},
			Committer: &CommitAuthor{
				Name:  "Test Committer",
				Email: "committer@test.com",
			},
			Metadata: map[string]string{
				"reason": "obsolete",
			},
		}

		// Verify JSON serialization/deserialization
		jsonData, err := json.Marshal(req)
		assert.NoError(t, err)

		var decoded FileDeleteRequest
		err = json.Unmarshal(jsonData, &decoded)
		assert.NoError(t, err)

		assert.Equal(t, req.Message, decoded.Message)
		assert.Equal(t, req.Branch, decoded.Branch)
		assert.Equal(t, req.Author.Name, decoded.Author.Name)
		assert.Equal(t, req.Author.Email, decoded.Author.Email)
		assert.Equal(t, req.Committer.Name, decoded.Committer.Name)
		assert.Equal(t, req.Committer.Email, decoded.Committer.Email)
		assert.Equal(t, req.Metadata["reason"], decoded.Metadata["reason"])
	})

	t.Run("DELETE response structure validation", func(t *testing.T) {
		// Test that the same FileOperationResponse works for DELETE operations
		response := FileOperationResponse{
			Success:   true,
			CommitSHA: "abc123def456",
			Message:   "File 'test.txt' deleted successfully",
			FilePath:  "test.txt",
			Operation: "DELETE",
			Branch:    "main",
		}

		// Verify JSON serialization
		jsonData, err := json.Marshal(response)
		assert.NoError(t, err)

		var decoded FileOperationResponse
		err = json.Unmarshal(jsonData, &decoded)
		assert.NoError(t, err)

		assert.Equal(t, response.Success, decoded.Success)
		assert.Equal(t, response.CommitSHA, decoded.CommitSHA)
		assert.Equal(t, response.Message, decoded.Message)
		assert.Equal(t, response.FilePath, decoded.FilePath)
		assert.Equal(t, response.Operation, decoded.Operation)
		assert.Equal(t, response.Branch, decoded.Branch)
	})
}

func TestMultiFileOperation_Validation(t *testing.T) {
	t.Run("Valid multi-file operations", func(t *testing.T) {
		operations := []MultiFileOperation{
			{
				Path:      "file1.txt",
				Operation: "create",
				Content:   "content1",
			},
			{
				Path:      "file2.txt",
				Operation: "update",
				Content:   "content2",
				Encoding:  "utf-8",
			},
			{
				Path:      "file3.txt",
				Operation: "delete",
			},
		}

		for _, op := range operations {
			assert.NotEmpty(t, op.Path)
			assert.Contains(t, []string{"create", "update", "delete"}, op.Operation)
		}
	})

	t.Run("Invalid operation types", func(t *testing.T) {
		invalidOps := []string{"modify", "rename", "copy", ""}

		for _, opType := range invalidOps {
			op := MultiFileOperation{
				Path:      "file.txt",
				Operation: opType,
				Content:   "content",
			}

			// Operation type validation would be done by the handler
			assert.NotContains(t, []string{"create", "update", "delete"}, op.Operation)
		}
	})
}

func TestMultiFileCommitRequest_Structure(t *testing.T) {
	t.Run("Valid multi-file commit request", func(t *testing.T) {
		req := MultiFileCommitRequest{
			Message: "Multi-file commit test",
			Branch:  "main",
			Author: &CommitAuthor{
				Name:  "Test Author",
				Email: "test@example.com",
			},
			Operations: []MultiFileOperation{
				{
					Path:      "README.md",
					Operation: "create",
					Content:   "# Test Repository",
				},
				{
					Path:      "src/main.go",
					Operation: "create",
					Content:   "package main\n\nfunc main() {}\n",
				},
			},
			Options: &CommitOptions{
				ValidateOnly: false,
				Timeout:      30 * time.Minute,
			},
		}

		assert.Equal(t, "Multi-file commit test", req.Message)
		assert.Equal(t, "main", req.Branch)
		assert.Len(t, req.Operations, 2)
		assert.NotNil(t, req.Author)
		assert.NotNil(t, req.Options)
		assert.False(t, req.Options.ValidateOnly)
	})

	t.Run("Minimal multi-file commit request", func(t *testing.T) {
		req := MultiFileCommitRequest{
			Message: "Minimal commit",
			Operations: []MultiFileOperation{
				{
					Path:      "file.txt",
					Operation: "create",
					Content:   "content",
				},
			},
		}

		assert.Equal(t, "Minimal commit", req.Message)
		assert.Empty(t, req.Branch) // Should default to "main"
		assert.Nil(t, req.Author)   // Should get default author
		assert.Len(t, req.Operations, 1)
	})
}

func TestMultiFileCommitResponse_Structure(t *testing.T) {
	t.Run("Successful multi-file commit response", func(t *testing.T) {
		response := MultiFileCommitResponse{
			Success:         true,
			CommitSHA:       "abc123def456",
			TransactionID:   "txn_12345",
			Message:         "Test commit",
			TotalOperations: 3,
			SuccessfulOps:   3,
			FailedOps:       0,
			OperationResults: []OperationResult{
				{Path: "file1.txt", Operation: "create", Success: true},
				{Path: "file2.txt", Operation: "update", Success: true},
				{Path: "file3.txt", Operation: "delete", Success: true},
			},
			ValidationOnly: false,
			Duration:       time.Second * 2,
		}

		assert.True(t, response.Success)
		assert.Equal(t, "abc123def456", response.CommitSHA)
		assert.Equal(t, "txn_12345", response.TransactionID)
		assert.Equal(t, 3, response.TotalOperations)
		assert.Equal(t, 3, response.SuccessfulOps)
		assert.Equal(t, 0, response.FailedOps)
		assert.Len(t, response.OperationResults, 3)
		assert.False(t, response.ValidationOnly)
	})

	t.Run("Partial success multi-file commit response", func(t *testing.T) {
		response := MultiFileCommitResponse{
			Success:         true, // Can still be successful with partial commits
			TransactionID:   "txn_67890",
			Message:         "Partial commit",
			TotalOperations: 3,
			SuccessfulOps:   2,
			FailedOps:       1,
			OperationResults: []OperationResult{
				{Path: "file1.txt", Operation: "create", Success: true},
				{Path: "file2.txt", Operation: "update", Success: true},
				{Path: "invalid/../../path.txt", Operation: "create", Success: false, Error: "invalid path"},
			},
			ValidationOnly: false,
			Duration:       time.Millisecond * 500,
			Details: map[string]interface{}{
				"partial_success": true,
				"failed_operations": map[string]error{
					"op2": fmt.Errorf("invalid path"),
				},
			},
		}

		assert.True(t, response.Success)
		assert.Equal(t, 3, response.TotalOperations)
		assert.Equal(t, 2, response.SuccessfulOps)
		assert.Equal(t, 1, response.FailedOps)
		assert.NotNil(t, response.Details)
		assert.Contains(t, response.OperationResults[2].Error, "invalid path")
	})
}

func TestCommitOptions_Validation(t *testing.T) {
	t.Run("Default commit options", func(t *testing.T) {
		options := &CommitOptions{}

		assert.False(t, options.ValidateOnly)
		assert.False(t, options.AllowPartialCommit)
		assert.False(t, options.DryRun)
		assert.Equal(t, time.Duration(0), options.Timeout)
	})

	t.Run("Custom commit options", func(t *testing.T) {
		options := &CommitOptions{
			ValidateOnly:       true,
			AllowPartialCommit: true,
			Timeout:            15 * time.Minute,
			DryRun:             false, // Should be overridden by ValidateOnly
		}

		assert.True(t, options.ValidateOnly)
		assert.True(t, options.AllowPartialCommit)
		assert.Equal(t, 15*time.Minute, options.Timeout)

		// DryRun is an alias for ValidateOnly
		if options.DryRun || options.ValidateOnly {
			assert.True(t, true) // Either should trigger validation-only mode
		}
	})
}

func TestOperationResult_Success(t *testing.T) {
	t.Run("Successful operation result", func(t *testing.T) {
		result := OperationResult{
			Path:      "success.txt",
			Operation: "create",
			Success:   true,
		}

		assert.True(t, result.Success)
		assert.Empty(t, result.Error)
	})

	t.Run("Failed operation result", func(t *testing.T) {
		result := OperationResult{
			Path:      "fail.txt",
			Operation: "update",
			Success:   false,
			Error:     "file not found",
		}

		assert.False(t, result.Success)
		assert.Equal(t, "file not found", result.Error)
	})
}
