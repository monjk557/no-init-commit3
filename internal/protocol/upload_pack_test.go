package protocol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockPermissionClient for testing
type MockPermissionClient struct {
	shouldAllow bool
	shouldError bool
}

func (m *MockPermissionClient) CheckReadAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	if m.shouldError {
		return false, assert.AnError
	}
	return m.shouldAllow, nil
}

func (m *MockPermissionClient) CheckWriteAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	if m.shouldError {
		return false, assert.AnError
	}
	return m.shouldAllow, nil
}

// InvalidateCache removes cached permission results for a specific user and repository
func (m *MockPermissionClient) InvalidateCache(userID, repoOwner, repoName string) {
	// Mock implementation - do nothing
}

// ClearCache removes all cached permission results
func (m *MockPermissionClient) ClearCache() {
	// Mock implementation - do nothing
}

// GetCircuitBreakerMetrics returns metrics about the circuit breaker state
func (m *MockPermissionClient) GetCircuitBreakerMetrics() *permissions.CircuitBreakerMetrics {
	return nil
}

// Close gracefully shuts down the client
func (m *MockPermissionClient) Close() error {
	return nil
}

// createTestRepository creates a test Git repository
func createTestRepository(t *testing.T, repoPath string) {
	// Create the directory structure
	err := os.MkdirAll(repoPath, 0755)
	require.NoError(t, err)

	// Create basic Git repository structure
	dirs := []string{
		filepath.Join(repoPath, "objects"),
		filepath.Join(repoPath, "refs", "heads"),
		filepath.Join(repoPath, "refs", "tags"),
	}

	for _, dir := range dirs {
		err := os.MkdirAll(dir, 0755)
		require.NoError(t, err)
	}

	// Create HEAD file
	headContent := "ref: refs/heads/main\n"
	err = os.WriteFile(filepath.Join(repoPath, "HEAD"), []byte(headContent), 0644)
	require.NoError(t, err)

	// Create a basic config file
	configContent := `[core]
	repositoryformatversion = 0
	filemode = true
	bare = true
`
	err = os.WriteFile(filepath.Join(repoPath, "config"), []byte(configContent), 0644)
	require.NoError(t, err)
}

func TestUploadPackHandler_HandleInfoRefs(t *testing.T) {
	tests := []struct {
		name                string
		setupRepo           bool
		userID              string
		owner               string
		repo                string
		service             string
		permissionShouldOK  bool
		permissionError     bool
		expectedStatus      int
		expectedContentType string
		expectedContains    []string
	}{
		{
			name:                "Valid request with permissions",
			setupRepo:           true,
			userID:              "user123",
			owner:               "testowner",
			repo:                "testrepo",
			service:             "git-upload-pack",
			permissionShouldOK:  true,
			permissionError:     false,
			expectedStatus:      http.StatusOK,
			expectedContentType: "application/x-git-upload-pack-advertisement",
			expectedContains:    []string{"# service=git-upload-pack", "0000"},
		},
		{
			name:               "Missing user ID",
			setupRepo:          true,
			userID:             "",
			owner:              "testowner",
			repo:               "testrepo",
			service:            "git-upload-pack",
			permissionShouldOK: true,
			permissionError:    false,
			expectedStatus:     http.StatusUnauthorized,
		},
		{
			name:               "Invalid service parameter",
			setupRepo:          true,
			userID:             "user123",
			owner:              "testowner",
			repo:               "testrepo",
			service:            "invalid-service",
			permissionShouldOK: true,
			permissionError:    false,
			expectedStatus:     http.StatusBadRequest,
		},
		{
			name:               "Permission denied",
			setupRepo:          true,
			userID:             "user123",
			owner:              "testowner",
			repo:               "testrepo",
			service:            "git-upload-pack",
			permissionShouldOK: false,
			permissionError:    false,
			expectedStatus:     http.StatusForbidden,
		},
		{
			name:               "Permission check error",
			setupRepo:          true,
			userID:             "user123",
			owner:              "testowner",
			repo:               "testrepo",
			service:            "git-upload-pack",
			permissionShouldOK: false,
			permissionError:    true,
			expectedStatus:     http.StatusInternalServerError,
		},
		{
			name:               "Repository not found",
			setupRepo:          false,
			userID:             "user123",
			owner:              "testowner",
			repo:               "nonexistent",
			service:            "git-upload-pack",
			permissionShouldOK: true,
			permissionError:    false,
			expectedStatus:     http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary directory for test
			tempDir, err := os.MkdirTemp("", "git-protocol-test-")
			require.NoError(t, err)
			defer os.RemoveAll(tempDir)

			// Create test configuration
			cfg := &config.Config{
				Git: config.GitConfig{
					RepoRoot: tempDir,
				},
			}

			// Setup repository if needed
			if tt.setupRepo {
				repoPath := filepath.Join(tempDir, tt.owner, tt.repo+".git")
				createTestRepository(t, repoPath)
			}

			// Create mock permission client
			mockPermClient := &MockPermissionClient{
				shouldAllow: tt.permissionShouldOK,
				shouldError: tt.permissionError,
			}

			// Create handler
			handler, err := NewUploadPackHandler(cfg, mockPermClient)
			require.NoError(t, err)

			// Create request
			url := "/info/refs?service=" + tt.service
			req := httptest.NewRequest(http.MethodGet, url, nil)

			// Set headers
			if tt.userID != "" {
				req.Header.Set("X-USER-ID", tt.userID)
			}

			// Add URL parameters using chi context
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("owner", tt.owner)
			rctx.URLParams.Add("repo", tt.repo)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			// Create response recorder
			w := httptest.NewRecorder()

			// Call handler
			handler.HandleInfoRefs(w, req)

			// Check status code
			assert.Equal(t, tt.expectedStatus, w.Code)

			// Check content type for successful requests
			if tt.expectedContentType != "" {
				assert.Equal(t, tt.expectedContentType, w.Header().Get("Content-Type"))
			}

			// Check response contains expected strings
			if len(tt.expectedContains) > 0 {
				body := w.Body.String()
				for _, expected := range tt.expectedContains {
					assert.Contains(t, body, expected)
				}
			}
		})
	}
}

func TestUploadPackHandler_HandleUploadPack(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := os.MkdirTemp("", "git-protocol-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create test configuration
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	// Create mock permission client
	mockPermClient := &MockPermissionClient{
		shouldAllow: true,
		shouldError: false,
	}

	// Create handler
	handler, err := NewUploadPackHandler(cfg, mockPermClient)
	require.NoError(t, err)

	// Create request
	req := httptest.NewRequest(http.MethodPost, "/git-upload-pack", nil)
	req.Header.Set("X-USER-ID", "user123")

	// Add URL parameters
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("owner", "testowner")
	rctx.URLParams.Add("repo", "testrepo")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	// Create response recorder
	w := httptest.NewRecorder()

	// Call handler (now implemented - should fail with repository not found)
	handler.HandleUploadPack(w, req)

	// Check that it processes the request (fails due to missing repo, but that's expected)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "Repository not found")
}

func TestUploadPackHandler_writeReferences(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := os.MkdirTemp("", "git-protocol-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create test configuration
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	// Create handler
	handler, err := NewUploadPackHandler(cfg, &MockPermissionClient{})
	require.NoError(t, err)

	// Create a mock repository
	mockRepo := git.NewMockRepository("/test/repo")

	// Test with empty repository (no commits)
	t.Run("Empty repository", func(t *testing.T) {
		// Set up mock to return error for GetCommits (simulating empty repo)
		mockRepo.GetCommitsError = assert.AnError

		w := httptest.NewRecorder()
		err := handler.writeReferences(context.Background(), w, mockRepo)
		assert.NoError(t, err)

		body := w.Body.String()
		// Should contain capabilities advertisement for empty repo
		assert.Contains(t, body, "capabilities^{}")
		assert.Contains(t, body, "multi_ack")
	})

	// Test with repository that has commits
	t.Run("Repository with commits", func(t *testing.T) {
		// Clear previous error
		mockRepo.GetCommitsError = nil

		// Set up mock to return a commit
		commits := []git.Commit{
			{
				Hash:    "abc123456789012345678901234567890123456",
				Message: "Initial commit",
			},
		}
		mockRepo.SetCommits("HEAD", commits)

		w := httptest.NewRecorder()
		err := handler.writeReferences(context.Background(), w, mockRepo)
		assert.NoError(t, err)

		body := w.Body.String()
		// Should contain HEAD reference and capabilities
		assert.Contains(t, body, "abc123456789012345678901234567890123456")
		assert.Contains(t, body, "HEAD")
		assert.Contains(t, body, "refs/heads/main")
		assert.Contains(t, body, "multi_ack")
	})
}

func TestNewUploadPackHandler(t *testing.T) {
	tests := []struct {
		name        string
		repoRoot    string
		expectError bool
	}{
		{
			name:        "Valid configuration",
			repoRoot:    "/tmp/valid/path",
			expectError: false,
		},
		{
			name:        "Invalid repository root",
			repoRoot:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Git: config.GitConfig{
					RepoRoot: tt.repoRoot,
				},
			}

			mockPermClient := &MockPermissionClient{}

			handler, err := NewUploadPackHandler(cfg, mockPermClient)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, handler)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, handler)
				assert.NotNil(t, handler.pathConfig)
				assert.NotNil(t, handler.permissionClient)
			}
		})
	}
}
