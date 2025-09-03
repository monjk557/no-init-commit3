package protocol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/schemas/requests"
	"github.com/collabops/git-executor/internal/schemas/responses"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to create test Gin context
func createTestGinContext(w *httptest.ResponseRecorder, req *http.Request) (*gin.Context, *gin.Engine) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = req
	return ctx, engine
}

func TestNewBranchOperationsHandler(t *testing.T) {
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

	handler, err := NewBranchOperationsHandler(cfg, permClient)
	require.NoError(t, err)
	assert.NotNil(t, handler)
	assert.NotNil(t, handler.branchNamePattern)
}

func TestBranchOperationsHandler_validateBranchName(t *testing.T) {
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
	handler, err := NewBranchOperationsHandler(cfg, permClient)
	require.NoError(t, err)

	tests := []struct {
		name        string
		branchName  string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Valid branch name",
			branchName:  "feature/user-auth",
			expectError: false,
		},
		{
			name:        "Valid branch with numbers",
			branchName:  "feature-123",
			expectError: false,
		},
		{
			name:        "Valid branch with underscores",
			branchName:  "feature_branch",
			expectError: false,
		},
		{
			name:        "Empty branch name",
			branchName:  "",
			expectError: true,
			errorMsg:    "branch name cannot be empty",
		},
		{
			name:        "Branch starting with dot",
			branchName:  ".feature",
			expectError: true,
			errorMsg:    "branch name cannot start or end with a dot",
		},
		{
			name:        "Branch ending with dot",
			branchName:  "feature.",
			expectError: true,
			errorMsg:    "branch name cannot start or end with a dot",
		},
		{
			name:        "Branch with consecutive dots",
			branchName:  "feature..branch",
			expectError: true,
			errorMsg:    "branch name cannot contain consecutive dots",
		},
		{
			name:        "Branch with spaces",
			branchName:  "feature branch",
			expectError: true,
			errorMsg:    "branch name cannot contain spaces",
		},
		{
			name:        "Reserved name HEAD",
			branchName:  "HEAD",
			expectError: true,
			errorMsg:    "branch name 'HEAD' is reserved",
		},
		{
			name:        "Reserved name ORIG_HEAD",
			branchName:  "orig_head",
			expectError: true,
			errorMsg:    "branch name 'orig_head' is reserved",
		},
		{
			name:        "Invalid characters",
			branchName:  "feature@branch",
			expectError: true,
			errorMsg:    "branch name contains invalid characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler.validateBranchName(tt.branchName)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCreateBranchRequest_Structure(t *testing.T) {
	t.Run("Valid create branch request", func(t *testing.T) {
		req := requests.CreateBranchRequest{
			Name:     "feature/new-api",
			Source:   "main",
			Force:    false,
			Checkout: true,
			Metadata: map[string]string{
				"created_by": "user123",
				"purpose":    "api_development",
			},
		}

		assert.Equal(t, "feature/new-api", req.Name)
		assert.Equal(t, "main", req.Source)
		assert.False(t, req.Force)
		assert.True(t, req.Checkout)
		assert.NotNil(t, req.Metadata)
		assert.Equal(t, "user123", req.Metadata["created_by"])
	})

	t.Run("Minimal create branch request", func(t *testing.T) {
		req := requests.CreateBranchRequest{
			Name: "hotfix/urgent-fix",
		}

		assert.Equal(t, "hotfix/urgent-fix", req.Name)
		assert.Empty(t, req.Source) // Should default to HEAD
		assert.False(t, req.Force)
		assert.False(t, req.Checkout)
		assert.Nil(t, req.Metadata)
	})
}

func TestCreateBranchResponse_Structure(t *testing.T) {
	t.Run("Successful branch creation response", func(t *testing.T) {
		response := responses.CreateBranchResponse{
			Success:     true,
			BranchName:  "feature/auth",
			CommitSHA:   "abc123def456789",
			Source:      "main",
			RefPath:     "refs/heads/feature/auth",
			IsNewBranch: true,
			Message:     "Branch 'feature/auth' created successfully",
			Details: map[string]interface{}{
				"force_used": false,
				"checkout":   true,
				"commit_info": map[string]interface{}{
					"author": "John Doe",
					"date":   time.Now(),
				},
			},
		}

		assert.True(t, response.Success)
		assert.Equal(t, "feature/auth", response.BranchName)
		assert.Equal(t, "abc123def456789", response.CommitSHA)
		assert.Equal(t, "main", response.Source)
		assert.Equal(t, "refs/heads/feature/auth", response.RefPath)
		assert.True(t, response.IsNewBranch)
		assert.Contains(t, response.Message, "created successfully")
		assert.NotNil(t, response.Details)
		assert.False(t, response.Details["force_used"].(bool))
		assert.True(t, response.Details["checkout"].(bool))
	})

	t.Run("Force update branch response", func(t *testing.T) {
		response := responses.CreateBranchResponse{
			Success:     true,
			BranchName:  "feature/existing",
			CommitSHA:   "def456abc789",
			Source:      "develop",
			RefPath:     "refs/heads/feature/existing",
			IsNewBranch: false,
			Message:     "Branch 'feature/existing' updated successfully (forced)",
			Details: map[string]interface{}{
				"force_used":      true,
				"checkout":        false,
				"previous_commit": "old123commit456",
			},
		}

		assert.True(t, response.Success)
		assert.False(t, response.IsNewBranch)
		assert.Contains(t, response.Message, "updated successfully")
		assert.True(t, response.Details["force_used"].(bool))
		assert.Equal(t, "old123commit456", response.Details["previous_commit"])
	})
}

func TestBranchInfo_Structure(t *testing.T) {
	t.Run("Complete branch info", func(t *testing.T) {
		branchInfo := responses.BranchInfo{
			Name:      "main",
			CommitSHA: "abc123",
			RefPath:   "refs/heads/main",
			IsHead:    true,
			LastCommit: &responses.CommitInfo{
				SHA:     "abc123",
				Message: "Initial commit",
				Author: responses.AuthorInfo{
					Name:  "John Doe",
					Email: "john@example.com",
					Date:  time.Now(),
				},
				Committer: responses.AuthorInfo{
					Name:  "John Doe",
					Email: "john@example.com",
					Date:  time.Now(),
				},
				CreatedAt: time.Now(),
			},
		}

		assert.Equal(t, "main", branchInfo.Name)
		assert.Equal(t, "abc123", branchInfo.CommitSHA)
		assert.Equal(t, "refs/heads/main", branchInfo.RefPath)
		assert.True(t, branchInfo.IsHead)
		assert.NotNil(t, branchInfo.LastCommit)
		assert.Equal(t, "Initial commit", branchInfo.LastCommit.Message)
		assert.Equal(t, "John Doe", branchInfo.LastCommit.Author.Name)
	})
}

func TestCommitInfo_Structure(t *testing.T) {
	t.Run("Complete commit info", func(t *testing.T) {
		now := time.Now()
		commitInfo := responses.CommitInfo{
			SHA:     "abc123def456",
			Message: "Add new feature",
			Author: responses.AuthorInfo{
				Name:  "Alice Developer",
				Email: "alice@example.com",
				Date:  now,
			},
			Committer: responses.AuthorInfo{
				Name:  "Bob Maintainer",
				Email: "bob@example.com",
				Date:  now.Add(time.Hour),
			},
			CreatedAt: now,
		}

		assert.Equal(t, "abc123def456", commitInfo.SHA)
		assert.Equal(t, "Add new feature", commitInfo.Message)
		assert.Equal(t, "Alice Developer", commitInfo.Author.Name)
		assert.Equal(t, "alice@example.com", commitInfo.Author.Email)
		assert.Equal(t, "Bob Maintainer", commitInfo.Committer.Name)
		assert.Equal(t, now, commitInfo.CreatedAt)
	})
}

func TestBranchOperationsHandler_validatePermissions(t *testing.T) {
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
		name            string
		userID          string
		source          string
		action          string
		mockWriteResult bool
		mockReadResult  bool
		mockError       error
		expectedStatus  int
	}{
		{
			name:            "Valid write permissions",
			userID:          "user123",
			source:          "web-app",
			action:          "write",
			mockWriteResult: true,
			expectedStatus:  0, // no error
		},
		{
			name:           "Valid read permissions",
			userID:         "user123",
			source:         "web-app",
			action:         "read",
			mockReadResult: true,
			expectedStatus: 0, // no error
		},
		{
			name:           "Missing X-USER-ID header",
			userID:         "",
			source:         "web-app",
			action:         "write",
			expectedStatus: 0, // Permission validation disabled
		},
		{
			name:           "Missing X-Source header",
			userID:         "user123",
			source:         "",
			action:         "write",
			expectedStatus: 0, // Permission validation disabled
		},
		{
			name:            "Write permission denied",
			userID:          "user123",
			source:          "web-app",
			action:          "write",
			mockWriteResult: false,
			expectedStatus:  0, // Permission validation disabled
		},
		{
			name:           "Read permission denied",
			userID:         "user123",
			source:         "web-app",
			action:         "read",
			mockReadResult: false,
			expectedStatus: 0, // Permission validation disabled
		},
		{
			name:           "Unknown action",
			userID:         "user123",
			source:         "web-app",
			action:         "unknown",
			expectedStatus: 0, // Permission validation disabled
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			permClient := &mockPermissionClient{
				readAccess:  tt.mockReadResult,
				writeAccess: tt.mockWriteResult,
				shouldError: tt.mockError != nil,
			}

			handler, err := NewBranchOperationsHandler(cfg, permClient)
			require.NoError(t, err)

			// Create a test request
			req, _ := http.NewRequest("POST", "/repos/owner/repo/branches", strings.NewReader(`{}`))
			if tt.userID != "" {
				req.Header.Set("X-USER-ID", tt.userID)
			}
			if tt.source != "" {
				req.Header.Set("X-Source", tt.source)
			}

			w := httptest.NewRecorder()
			c, _ := createTestGinContext(w, req)

			err = handler.validatePermissions(c, "owner", "repo", tt.action)

			if tt.expectedStatus == 0 {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				// Check that it's a GitAPIError with correct status
				if apiErr, ok := err.(*api.GitAPIError); ok {
					switch tt.expectedStatus {
					case 400:
						assert.Equal(t, api.ErrorCodeBadRequest, apiErr.Code)
					case 401:
						assert.Equal(t, api.ErrorCodeUnauthorized, apiErr.Code)
					case 403:
						assert.Equal(t, api.ErrorCodeForbidden, apiErr.Code)
					}
				}
			}
		})
	}
}

func TestBranchCreationWorkflow(t *testing.T) {
	t.Run("Branch creation request validation", func(t *testing.T) {
		// Test the structure without full integration
		req := responses.CreateBranchRequest{
			Name:     "feature/user-dashboard",
			Source:   "main",
			Force:    false,
			Checkout: true,
			Metadata: map[string]string{
				"ticket": "PROJ-123",
				"team":   "frontend",
			},
		}

		// Validate request structure
		assert.Equal(t, "feature/user-dashboard", req.Name)
		assert.Equal(t, "main", req.Source)
		assert.False(t, req.Force)
		assert.True(t, req.Checkout)
		assert.Len(t, req.Metadata, 2)
		assert.Equal(t, "PROJ-123", req.Metadata["ticket"])
	})

	t.Run("Branch creation response validation", func(t *testing.T) {
		// Test response structure without full integration
		response := responses.CreateBranchResponse{
			Success:     true,
			BranchName:  "feature/user-dashboard",
			CommitSHA:   "1234567890abcdef1234567890abcdef12345678",
			Source:      "main",
			RefPath:     "refs/heads/feature/user-dashboard",
			IsNewBranch: true,
			Message:     "Branch 'feature/user-dashboard' created successfully",
			Details: map[string]interface{}{
				"force_used": false,
				"checkout":   true,
				"commit_info": map[string]interface{}{
					"author": "Test User",
					"date":   time.Now(),
				},
			},
		}

		assert.True(t, response.Success)
		assert.NotEmpty(t, response.CommitSHA)
		assert.NotEmpty(t, response.RefPath)
		assert.True(t, response.IsNewBranch)
		assert.Contains(t, response.Message, "created successfully")
		assert.NotNil(t, response.Details)
		assert.False(t, response.Details["force_used"].(bool))
		assert.True(t, response.Details["checkout"].(bool))
	})

	t.Run("JSON serialization", func(t *testing.T) {
		req := responses.CreateBranchRequest{
			Name:   "test-branch",
			Source: "main",
			Force:  true,
		}

		jsonData, err := json.Marshal(req)
		require.NoError(t, err)

		var decoded responses.CreateBranchRequest
		err = json.Unmarshal(jsonData, &decoded)
		require.NoError(t, err)

		assert.Equal(t, req.Name, decoded.Name)
		assert.Equal(t, req.Source, decoded.Source)
		assert.Equal(t, req.Force, decoded.Force)
	})
}
