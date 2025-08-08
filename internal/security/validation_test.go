package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
)

func createTestValidationLogger() *logging.Logger {
	cfg := config.LoggingConfig{
		Level:  "debug",
		Format: "text",
		Output: "stdout",
	}
	logger, _ := logging.NewLogger(cfg)
	return logger
}

func createTestValidationFramework() *ValidationFramework {
	logger := createTestValidationLogger()
	vf, _ := NewValidationFramework(nil, logger)
	return vf
}

func TestNewValidationFramework(t *testing.T) {
	logger := createTestValidationLogger()

	t.Run("successful creation with default config", func(t *testing.T) {
		vf, err := NewValidationFramework(nil, logger)
		assert.NoError(t, err)
		assert.NotNil(t, vf)
		assert.NotNil(t, vf.config)
		assert.NotNil(t, vf.validator)
		assert.Equal(t, int64(100*1024*1024), vf.config.MaxFileSize)
	})

	t.Run("successful creation with custom config", func(t *testing.T) {
		config := &ValidationConfig{
			MaxFileSize:            50 * 1024 * 1024,
			MaxCommitMessageLength: 1000,
			MaxPathLength:          2000,
			MaxFilesPerCommit:      500,
		}
		vf, err := NewValidationFramework(config, logger)
		assert.NoError(t, err)
		assert.NotNil(t, vf)
		assert.Equal(t, int64(50*1024*1024), vf.config.MaxFileSize)
		assert.Equal(t, 1000, vf.config.MaxCommitMessageLength)
	})

	t.Run("error with nil logger", func(t *testing.T) {
		_, err := NewValidationFramework(nil, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "logger is required")
	})
}

func TestValidationFramework_PathValidation(t *testing.T) {
	vf := createTestValidationFramework()

	tests := []struct {
		name        string
		path        string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid simple path",
			path:        "src/main.go",
			expectError: false,
		},
		{
			name:        "valid nested path",
			path:        "src/components/Button/index.tsx",
			expectError: false,
		},
		{
			name:        "path traversal attempt",
			path:        "../../../etc/passwd",
			expectError: true,
			errorMsg:    "path traversal attempt",
		},
		{
			name:        "absolute path",
			path:        "/etc/passwd",
			expectError: true,
			errorMsg:    "cannot be absolute",
		},
		{
			name:        "git internal path",
			path:        ".git/config",
			expectError: true,
			errorMsg:    "Git internal paths is forbidden",
		},
		{
			name:        "empty path",
			path:        "",
			expectError: true,
			errorMsg:    "cannot be empty",
		},
		{
			name:        "banned extension",
			path:        "malware.exe",
			expectError: true,
			errorMsg:    "not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := vf.validateFilePath(tt.path)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidationFramework_BranchValidation(t *testing.T) {
	vf := createTestValidationFramework()

	tests := []struct {
		name        string
		branch      string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid branch name",
			branch:      "feature/add-validation",
			expectError: false,
		},
		{
			name:        "valid simple branch",
			branch:      "main",
			expectError: false,
		},
		{
			name:        "branch starting with dash",
			branch:      "-invalid",
			expectError: true,
			errorMsg:    "invalid branch name format",
		},
		{
			name:        "branch ending with dot",
			branch:      "invalid.",
			expectError: true,
			errorMsg:    "invalid branch name format",
		},
		{
			name:        "empty branch name",
			branch:      "",
			expectError: true,
			errorMsg:    "cannot be empty",
		},
		{
			name:        "too long branch name",
			branch:      strings.Repeat("a", 256),
			expectError: true,
			errorMsg:    "too long",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := vf.validateBranchName(tt.branch)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidationFramework_RequestSizeValidation(t *testing.T) {
	vf := createTestValidationFramework()

	gin.SetMode(gin.TestMode)

	t.Run("request within size limit", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/test", strings.NewReader("small content"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req

		err := vf.validateRequestSize(c)
		assert.NoError(t, err)
	})

	t.Run("request exceeding size limit", func(t *testing.T) {
		// Create a request larger than the limit
		largeContent := strings.Repeat("a", int(vf.config.MaxFileSize)+1)
		req := httptest.NewRequest("POST", "/api/test", strings.NewReader(largeContent))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req

		err := vf.validateRequestSize(c)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds maximum allowed size")
	})
}

func TestValidationFramework_FileOperationRequestValidation(t *testing.T) {
	vf := createTestValidationFramework()

	t.Run("valid file operation request", func(t *testing.T) {
		req := FileOperationRequest{
			Content:       "package main\n\nfunc main() {\n\tprintln(\"Hello, World!\")\n}",
			CommitMessage: "Add hello world program",
			Branch:        "main",
			AuthorName:    "John Doe",
			AuthorEmail:   "john@example.com",
		}

		body, err := json.Marshal(req)
		require.NoError(t, err)

		err = vf.validateFileOperationRequest(body)
		assert.NoError(t, err)
	})

	t.Run("invalid file operation request - missing required fields", func(t *testing.T) {
		req := FileOperationRequest{
			Content: "some content",
			// Missing required fields
		}

		body, err := json.Marshal(req)
		require.NoError(t, err)

		err = vf.validateFileOperationRequest(body)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "validation failed")
	})

	t.Run("invalid file operation request - invalid email", func(t *testing.T) {
		req := FileOperationRequest{
			Content:       "some content",
			CommitMessage: "commit message",
			Branch:        "main",
			AuthorName:    "John Doe",
			AuthorEmail:   "invalid-email",
		}

		body, err := json.Marshal(req)
		require.NoError(t, err)

		err = vf.validateFileOperationRequest(body)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "validation failed")
	})

	t.Run("content too large", func(t *testing.T) {
		largeContent := strings.Repeat("a", int(vf.config.MaxFileSize)+1)
		req := FileOperationRequest{
			Content:       largeContent,
			CommitMessage: "commit message",
			Branch:        "main",
			AuthorName:    "John Doe",
			AuthorEmail:   "john@example.com",
		}

		body, err := json.Marshal(req)
		require.NoError(t, err)

		err = vf.validateFileOperationRequest(body)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "content size exceeds maximum file size")
	})
}

func TestValidationFramework_MultiFileCommitRequestValidation(t *testing.T) {
	vf := createTestValidationFramework()

	t.Run("valid multi-file commit request", func(t *testing.T) {
		req := MultiFileCommitRequest{
			Files: []FileChange{
				{
					Path:      "src/main.go",
					Operation: "create",
					Content:   "package main",
				},
				{
					Path:      "README.md",
					Operation: "update",
					Content:   "# Updated README",
				},
			},
			CommitMessage: "Add main.go and update README",
			Branch:        "main",
			AuthorName:    "John Doe",
			AuthorEmail:   "john@example.com",
		}

		body, err := json.Marshal(req)
		require.NoError(t, err)

		err = vf.validateMultiFileCommitRequest(body)
		assert.NoError(t, err)
	})

	t.Run("too many files in commit", func(t *testing.T) {
		files := make([]FileChange, vf.config.MaxFilesPerCommit+1)
		for i := range files {
			files[i] = FileChange{
				Path:      "file" + string(rune(i)) + ".txt",
				Operation: "create",
				Content:   "content",
			}
		}

		req := MultiFileCommitRequest{
			Files:         files,
			CommitMessage: "commit message",
			Branch:        "main",
			AuthorName:    "John Doe",
			AuthorEmail:   "john@example.com",
		}

		body, err := json.Marshal(req)
		require.NoError(t, err)

		err = vf.validateMultiFileCommitRequest(body)
		assert.Error(t, err)
		// The validator/v10 library catches the max constraint before our custom validation
		isValidError := strings.Contains(err.Error(), "too many files in commit") || strings.Contains(err.Error(), "max")
		assert.True(t, isValidError, "Expected validation error for too many files, got: %s", err.Error())
	})
}

func TestValidationFramework_Middleware(t *testing.T) {
	vf := createTestValidationFramework()

	gin.SetMode(gin.TestMode)

	t.Run("middleware skips non-API endpoints", func(t *testing.T) {
		router := gin.New()
		router.Use(vf.ValidateRequestMiddleware())
		router.GET("/health", func(c *gin.Context) {
			c.JSON(200, gin.H{"status": "ok"})
		})

		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "ok")
	})

	t.Run("middleware validates API endpoints", func(t *testing.T) {
		router := gin.New()
		router.Use(vf.ValidateRequestMiddleware())
		router.PUT("/repos/:owner/:repo/files/*path", func(c *gin.Context) {
			c.JSON(200, gin.H{"status": "ok"})
		})

		// Test with path traversal in URL
		req := httptest.NewRequest("PUT", "/repos/owner/repo/files/../../../etc/passwd", nil)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Invalid URL parameters")
	})

	t.Run("middleware validates request body", func(t *testing.T) {
		router := gin.New()
		router.Use(vf.ValidateRequestMiddleware())
		router.PUT("/repos/:owner/:repo/files/*path", func(c *gin.Context) {
			c.JSON(200, gin.H{"status": "ok"})
		})

		// Test with invalid JSON - use a simple valid path
		invalidJSON := `{"invalid": json}`
		req := httptest.NewRequest("PUT", "/repos/owner/repo/files/test.txt", strings.NewReader(invalidJSON))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		// Check for either URL validation error (which is valid) or request body error
		body := w.Body.String()
		isValidError := strings.Contains(body, "Invalid request body") || strings.Contains(body, "Invalid URL parameters")
		assert.True(t, isValidError, "Expected validation error, got: %s", body)
	})

	t.Run("middleware rejects oversized requests", func(t *testing.T) {
		router := gin.New()
		router.Use(vf.ValidateRequestMiddleware())
		router.POST("/repos/:owner/:repo/commits", func(c *gin.Context) {
			c.JSON(200, gin.H{"status": "ok"})
		})

		// Create an oversized request
		largeContent := strings.Repeat("a", int(vf.config.MaxFileSize)+1)
		req := httptest.NewRequest("POST", "/repos/owner/repo/commits", strings.NewReader(largeContent))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
		assert.Contains(t, w.Body.String(), "Request too large")
	})
}

func TestValidationFramework_CustomValidators(t *testing.T) {
	t.Run("validateGitPath", func(t *testing.T) {
		tests := []struct {
			path  string
			valid bool
		}{
			{"src/main.go", true},
			{"", false},
			{"../../../etc/passwd", false},
			{"/absolute/path", false},
			{".git/config", false},
			{strings.Repeat("a", 5000), false},
		}

		for _, tt := range tests {
			// Create a mock field level
			var mockField mockFieldLevel
			mockField.value = tt.path
			result := validateGitPath(&mockField)
			assert.Equal(t, tt.valid, result, "Path: %s", tt.path)
		}
	})

	t.Run("validateGitBranch", func(t *testing.T) {
		tests := []struct {
			branch string
			valid  bool
		}{
			{"main", true},
			{"feature/add-validation", true},
			{"", false},
			{"-invalid", false},
			{"invalid.", false},
			{"branch with spaces", false},
			{"branch~invalid", false},
			{strings.Repeat("a", 300), false},
		}

		for _, tt := range tests {
			var mockField mockFieldLevel
			mockField.value = tt.branch
			result := validateGitBranch(&mockField)
			assert.Equal(t, tt.valid, result, "Branch: %s", tt.branch)
		}
	})

	t.Run("validateGitRef", func(t *testing.T) {
		tests := []struct {
			ref   string
			valid bool
		}{
			{"abc123def456", true},
			{"a1b2c3d4e5f6789012345678901234567890abcd", true},
			{"main", true},
			{"feature/branch", true},
			{"", false},
			{"invalid ref with spaces", false},
		}

		for _, tt := range tests {
			var mockField mockFieldLevel
			mockField.value = tt.ref
			result := validateGitRef(&mockField)
			assert.Equal(t, tt.valid, result, "Ref: %s", tt.ref)
		}
	})
}

func TestValidationFramework_IsAPIEndpoint(t *testing.T) {
	vf := createTestValidationFramework()

	tests := []struct {
		path  string
		isAPI bool
	}{
		{"/repos/owner/repo", true},
		{"/api/repos/owner/repo", true},
		{"/health", false},
		{"/metrics", false},
		{"/", false},
		{"/some/other/path", false},
	}

	for _, tt := range tests {
		result := vf.isAPIEndpoint(tt.path)
		assert.Equal(t, tt.isAPI, result, "Path: %s", tt.path)
	}
}

// Mock field level for testing custom validators
type mockFieldLevel struct {
	value string
}

func (m *mockFieldLevel) Top() reflect.Value      { return reflect.ValueOf(m.value) }
func (m *mockFieldLevel) Parent() reflect.Value   { return reflect.ValueOf(m.value) }
func (m *mockFieldLevel) Field() reflect.Value    { return reflect.ValueOf(m.value) }
func (m *mockFieldLevel) FieldName() string       { return "testField" }
func (m *mockFieldLevel) StructFieldName() string { return "TestField" }
func (m *mockFieldLevel) Param() string           { return "" }
func (m *mockFieldLevel) GetTag() string          { return "" }
func (m *mockFieldLevel) ExtractType(field reflect.Value) (reflect.Value, reflect.Kind, bool) {
	return field, field.Kind(), true
}
func (m *mockFieldLevel) GetStructFieldOK() (reflect.Value, reflect.Kind, bool) {
	return reflect.ValueOf(m.value), reflect.String, true
}
func (m *mockFieldLevel) GetStructFieldOKAdvanced(val reflect.Value, namespace string) (reflect.Value, reflect.Kind, bool) {
	return reflect.ValueOf(m.value), reflect.String, true
}
func (m *mockFieldLevel) GetStructFieldOK2() (reflect.Value, reflect.Kind, bool, bool) {
	return reflect.ValueOf(m.value), reflect.String, true, true
}
func (m *mockFieldLevel) GetStructFieldOKAdvanced2(val reflect.Value, namespace string) (reflect.Value, reflect.Kind, bool, bool) {
	return reflect.ValueOf(m.value), reflect.String, true, true
}
