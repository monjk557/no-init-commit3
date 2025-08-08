package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLogger creates a logger for testing
func testLogger() *logging.Logger {
	cfg := config.LoggingConfig{
		Level:       "info",
		Format:      "text",
		Output:      "stderr",
		Development: true,
	}
	logger, _ := logging.NewLogger(cfg)
	return logger
}

func TestNewGitAPIValidationMiddleware(t *testing.T) {
	logger := testLogger()

	tests := []struct {
		name      string
		config    *GitAPIValidationConfig
		logger    *logging.Logger
		wantError bool
	}{
		{
			name:      "Valid configuration",
			config:    DefaultGitAPIValidationConfig(),
			logger:    logger,
			wantError: false,
		},
		{
			name:      "Nil config uses default",
			config:    nil,
			logger:    logger,
			wantError: false,
		},
		{
			name:      "Nil logger fails",
			config:    DefaultGitAPIValidationConfig(),
			logger:    nil,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			middleware, err := NewGitAPIValidationMiddleware(tt.config, tt.logger)
			if tt.wantError {
				assert.Error(t, err)
				assert.Nil(t, middleware)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, middleware)
			}
		})
	}
}

func TestGitAPIValidationMiddleware_ValidateGitAPIRequest(t *testing.T) {
	logger := testLogger()
	middleware, err := NewGitAPIValidationMiddleware(DefaultGitAPIValidationConfig(), logger)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		method         string
		path           string
		headers        map[string]string
		body           interface{}
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "GET request passes through",
			method:         "GET",
			path:           "/repos/owner/repo/tree",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Non-Git API endpoint passes through",
			method:         "POST",
			path:           "/health",
			expectedStatus: http.StatusOK,
		},
		{
			name:    "Valid file operation request",
			method:  "PUT",
			path:    "/repos/owner/repo/files/test.txt",
			headers: map[string]string{"Content-Type": "application/json"},
			body: FileOperationRequest{
				Content: "test content",
				Message: "Test commit",
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:   "Missing Content-Type header",
			method: "PUT",
			path:   "/repos/owner/repo/files/test.txt",
			body: FileOperationRequest{
				Content: "test content",
				Message: "Test commit",
			},
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Content-Type header is required",
		},
		{
			name:           "Invalid Content-Type",
			method:         "PUT",
			path:           "/repos/owner/repo/files/test.txt",
			headers:        map[string]string{"Content-Type": "text/plain"},
			expectedStatus: http.StatusBadRequest,
			expectedError:  "unsupported Content-Type",
		},
		{
			name:    "Path traversal in file path",
			method:  "PUT",
			path:    "/repos/owner/repo/files/../../../etc/passwd",
			headers: map[string]string{"Content-Type": "application/json"},
			body: FileOperationRequest{
				Content: "malicious content",
				Message: "Test commit",
			},
			expectedStatus: http.StatusBadRequest,
			expectedError:  "path traversal attempt",
		},
		{
			name:    "Empty commit message",
			method:  "PUT",
			path:    "/repos/owner/repo/files/test.txt",
			headers: map[string]string{"Content-Type": "application/json"},
			body: FileOperationRequest{
				Content: "test content",
				Message: "",
			},
			expectedStatus: http.StatusBadRequest,
			expectedError:  "commit message cannot be empty",
		},
		{
			name:    "Banned file extension",
			method:  "PUT",
			path:    "/repos/owner/repo/files/malware.exe",
			headers: map[string]string{"Content-Type": "application/json"},
			body: FileOperationRequest{
				Content: "malicious content",
				Message: "Test commit",
			},
			expectedStatus: http.StatusBadRequest,
			expectedError:  "file extension .exe is not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup router with middleware
			router := gin.New()
			router.Use(middleware.ValidateGitAPIRequest())
			router.Any("/*path", func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"success": true})
			})

			// Prepare request body
			var body []byte
			if tt.body != nil {
				body, _ = json.Marshal(tt.body)
			}

			// Create request
			req := httptest.NewRequest(tt.method, tt.path, bytes.NewBuffer(body))

			// Set headers
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			// Create response recorder
			w := httptest.NewRecorder()

			// Execute request
			router.ServeHTTP(w, req)

			// Check status code
			assert.Equal(t, tt.expectedStatus, w.Code)

			// Check error message if expected
			if tt.expectedError != "" {
				var response map[string]interface{}
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(t, err)

				errorMsg, exists := response["error"]
				assert.True(t, exists)
				assert.Contains(t, strings.ToLower(errorMsg.(string)), strings.ToLower(tt.expectedError))
			}
		})
	}
}

func TestValidateFileOperationRequest(t *testing.T) {
	logger := testLogger()
	middleware, err := NewGitAPIValidationMiddleware(DefaultGitAPIValidationConfig(), logger)
	require.NoError(t, err)

	tests := []struct {
		name      string
		request   FileOperationRequest
		wantError bool
		errorMsg  string
	}{
		{
			name: "Valid request",
			request: FileOperationRequest{
				Content:        "test content",
				Message:        "Test commit message",
				Branch:         "main",
				CommitterName:  "Test User",
				CommitterEmail: "test@example.com",
			},
			wantError: false,
		},
		{
			name: "Empty commit message",
			request: FileOperationRequest{
				Content: "test content",
				Message: "",
			},
			wantError: true,
			errorMsg:  "commit message cannot be empty",
		},
		{
			name: "Long commit message",
			request: FileOperationRequest{
				Content: "test content",
				Message: strings.Repeat("a", 3000),
			},
			wantError: true,
			errorMsg:  "commit message too long",
		},
		{
			name: "Invalid branch name",
			request: FileOperationRequest{
				Content: "test content",
				Message: "Test commit",
				Branch:  "invalid branch name!",
			},
			wantError: true,
			errorMsg:  "branch name contains invalid characters",
		},
		{
			name: "Invalid email format",
			request: FileOperationRequest{
				Content:        "test content",
				Message:        "Test commit",
				CommitterEmail: "invalid-email",
			},
			wantError: true,
			errorMsg:  "invalid email format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			err := middleware.validateFileOperationRequest(body)

			if tt.wantError {
				assert.Error(t, err)
				assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tt.errorMsg))
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateMultiFileOperationRequest(t *testing.T) {
	logger := testLogger()
	config := DefaultGitAPIValidationConfig()
	config.MaxFilesPerCommit = 2 // Set low limit for testing
	middleware, err := NewGitAPIValidationMiddleware(config, logger)
	require.NoError(t, err)

	tests := []struct {
		name      string
		request   MultiFileOperationRequest
		wantError bool
		errorMsg  string
	}{
		{
			name: "Valid multi-file request",
			request: MultiFileOperationRequest{
				Files: []struct {
					Path      string `json:"path"`
					Content   string `json:"content"`
					Operation string `json:"operation"`
				}{
					{Path: "file1.txt", Content: "content1", Operation: "create"},
					{Path: "file2.txt", Content: "content2", Operation: "update"},
				},
				Message: "Multi-file commit",
			},
			wantError: false,
		},
		{
			name: "Too many files",
			request: MultiFileOperationRequest{
				Files: []struct {
					Path      string `json:"path"`
					Content   string `json:"content"`
					Operation string `json:"operation"`
				}{
					{Path: "file1.txt", Content: "content1", Operation: "create"},
					{Path: "file2.txt", Content: "content2", Operation: "update"},
					{Path: "file3.txt", Content: "content3", Operation: "create"},
				},
				Message: "Multi-file commit",
			},
			wantError: true,
			errorMsg:  "exceeds maximum",
		},
		{
			name: "Invalid file operation",
			request: MultiFileOperationRequest{
				Files: []struct {
					Path      string `json:"path"`
					Content   string `json:"content"`
					Operation string `json:"operation"`
				}{
					{Path: "file1.txt", Content: "content1", Operation: "invalid_op"},
				},
				Message: "Multi-file commit",
			},
			wantError: true,
			errorMsg:  "invalid file operation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			err := middleware.validateMultiFileOperationRequest(body)

			if tt.wantError {
				assert.Error(t, err)
				assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tt.errorMsg))
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateBranchOperationRequest(t *testing.T) {
	logger := testLogger()
	middleware, err := NewGitAPIValidationMiddleware(DefaultGitAPIValidationConfig(), logger)
	require.NoError(t, err)

	tests := []struct {
		name      string
		request   BranchOperationRequest
		wantError bool
		errorMsg  string
	}{
		{
			name: "Valid branch creation",
			request: BranchOperationRequest{
				Name:      "feature-branch",
				SourceRef: "main",
			},
			wantError: false,
		},
		{
			name: "Invalid branch name",
			request: BranchOperationRequest{
				Name: "invalid branch!",
			},
			wantError: true,
			errorMsg:  "invalid branch name",
		},
		{
			name: "Invalid source ref",
			request: BranchOperationRequest{
				Name:      "feature-branch",
				SourceRef: "invalid ref!",
			},
			wantError: true,
			errorMsg:  "invalid source ref",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			err := middleware.validateBranchOperationRequest(body)

			if tt.wantError {
				assert.Error(t, err)
				assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tt.errorMsg))
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateMergeOperationRequest(t *testing.T) {
	logger := testLogger()
	middleware, err := NewGitAPIValidationMiddleware(DefaultGitAPIValidationConfig(), logger)
	require.NoError(t, err)

	tests := []struct {
		name      string
		request   MergeOperationRequest
		wantError bool
		errorMsg  string
	}{
		{
			name: "Valid merge request",
			request: MergeOperationRequest{
				Base:          "main",
				Head:          "feature-branch",
				CommitMessage: "Merge feature branch",
				Strategy:      "merge",
			},
			wantError: false,
		},
		{
			name: "Valid merge with commit hash",
			request: MergeOperationRequest{
				Base: "a1b2c3d4e5f6789012345678901234567890abcd",
				Head: "feature-branch",
			},
			wantError: false,
		},
		{
			name: "Invalid base ref",
			request: MergeOperationRequest{
				Base: "invalid ref!",
				Head: "feature-branch",
			},
			wantError: true,
			errorMsg:  "invalid base ref",
		},
		{
			name: "Invalid merge strategy",
			request: MergeOperationRequest{
				Base:     "main",
				Head:     "feature-branch",
				Strategy: "invalid_strategy",
			},
			wantError: true,
			errorMsg:  "invalid merge strategy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			err := middleware.validateMergeOperationRequest(body)

			if tt.wantError {
				assert.Error(t, err)
				assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tt.errorMsg))
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidationHelperFunctions(t *testing.T) {
	logger := testLogger()
	middleware, err := NewGitAPIValidationMiddleware(DefaultGitAPIValidationConfig(), logger)
	require.NoError(t, err)

	t.Run("validatePathComponent", func(t *testing.T) {
		tests := []struct {
			name      string
			component string
			wantError bool
		}{
			{"Valid component", "valid-component", false},
			{"Empty component", "", true},
			{"Too long component", strings.Repeat("a", 300), true},
			{"Path traversal", "../evil", true},
			{"Invalid characters", "comp:onent", true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := middleware.validatePathComponent(tt.component, "test")
				if tt.wantError {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
			})
		}
	})

	t.Run("validateFilePath", func(t *testing.T) {
		tests := []struct {
			name      string
			path      string
			wantError bool
		}{
			{"Valid path", "src/main.go", false},
			{"Empty path", "", true},
			{"Path traversal", "../../../etc/passwd", true},
			{"Absolute path", "/etc/passwd", true},
			{"Banned extension", "malware.exe", true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := middleware.validateFilePath(tt.path)
				if tt.wantError {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
			})
		}
	})

	t.Run("validateBranchName", func(t *testing.T) {
		tests := []struct {
			name      string
			branch    string
			wantError bool
		}{
			{"Valid branch", "feature-branch", false},
			{"Valid branch with slash", "feature/new-feature", false},
			{"Empty branch", "", true},
			{"Too long branch", strings.Repeat("a", 300), true},
			{"Invalid characters", "feature branch!", true},
			{"Starts with dash", "-feature", true},
			{"Ends with dot", "feature.", true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := middleware.validateBranchName(tt.branch)
				if tt.wantError {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
			})
		}
	})

	t.Run("validateCommitMessage", func(t *testing.T) {
		tests := []struct {
			name      string
			message   string
			wantError bool
		}{
			{"Valid message", "Fix bug in authentication", false},
			{"Empty message", "", true},
			{"Too long message", strings.Repeat("a", 3000), true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := middleware.validateCommitMessage(tt.message)
				if tt.wantError {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
			})
		}
	})
}

func TestIsGitAPIEndpoint(t *testing.T) {
	logger := testLogger()
	middleware, err := NewGitAPIValidationMiddleware(DefaultGitAPIValidationConfig(), logger)
	require.NoError(t, err)

	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{"Git API endpoint", "/repos/owner/repo/files/test.txt", true},
		{"Health endpoint", "/health", false},
		{"Metrics endpoint", "/metrics", false},
		{"Git protocol endpoint", "/owner/repo/info/refs", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := middleware.isGitAPIEndpoint(tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDefaultGitAPIValidationConfig(t *testing.T) {
	config := DefaultGitAPIValidationConfig()

	assert.NotNil(t, config)
	assert.Equal(t, int64(50*1024*1024), config.MaxFileSize)
	assert.Equal(t, 2048, config.MaxCommitMessageLength)
	assert.Equal(t, 4096, config.MaxPathLength)
	assert.Equal(t, 1000, config.MaxFilesPerCommit)
	assert.True(t, config.EnableContentValidation)
	assert.NotEmpty(t, config.BannedFileExtensions)

	// Check that common dangerous extensions are banned
	assert.Contains(t, config.BannedFileExtensions, ".exe")
	assert.Contains(t, config.BannedFileExtensions, ".dll")
	assert.Contains(t, config.BannedFileExtensions, ".jar")
}
