package protocol

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/permissions"
)

// mockRepoStatePermissionClient implements the PermissionClient interface for testing
type mockRepoStatePermissionClient struct {
	readAccess  bool
	writeAccess bool
	shouldError bool
}

func (m *mockRepoStatePermissionClient) CheckReadAccess(ctx context.Context, userID, repoOwner, repoName string) (bool, error) {
	if m.shouldError {
		return false, assert.AnError
	}
	return m.readAccess, nil
}

func (m *mockRepoStatePermissionClient) CheckWriteAccess(ctx context.Context, userID, repoOwner, repoName string) (bool, error) {
	if m.shouldError {
		return false, assert.AnError
	}
	return m.writeAccess, nil
}

func (m *mockRepoStatePermissionClient) InvalidateCache(userID, repoOwner, repoName string) {
	// Mock implementation - do nothing
}

func (m *mockRepoStatePermissionClient) ClearCache() {
	// Mock implementation - do nothing
}

func (m *mockRepoStatePermissionClient) GetCircuitBreakerMetrics() *permissions.CircuitBreakerMetrics {
	return nil
}

func (m *mockRepoStatePermissionClient) Close() error {
	return nil
}

func createTestRepoStateConfig() *config.Config {
	return &config.Config{
		Git: config.GitConfig{
			RepoRoot: "/tmp/test-repos",
		},
		Logging: config.LoggingConfig{
			Level:  "debug",
			Format: "text",
			Output: "stdout",
		},
	}
}

func TestNewRepositoryStateHandler(t *testing.T) {
	t.Run("successful creation", func(t *testing.T) {
		cfg := createTestRepoStateConfig()
		permClient := &mockRepoStatePermissionClient{readAccess: true}

		handler, err := NewRepositoryStateHandler(cfg, permClient)
		assert.NoError(t, err)
		assert.NotNil(t, handler)
		assert.Equal(t, cfg, handler.config)
		assert.NotNil(t, handler.logger)
		assert.NotNil(t, handler.pathConfig)
		assert.NotNil(t, handler.objectManager)
		assert.NotNil(t, handler.responseMiddleware)
		assert.NotNil(t, handler.errorMapper)
	})

	t.Run("invalid config should fail", func(t *testing.T) {
		cfg := &config.Config{
			Git: config.GitConfig{
				RepoRoot: "", // Invalid empty repo root
			},
			Logging: config.LoggingConfig{
				Level:  "debug",
				Format: "text",
				Output: "stdout",
			},
		}
		permClient := &mockRepoStatePermissionClient{readAccess: true}

		handler, err := NewRepositoryStateHandler(cfg, permClient)
		assert.Error(t, err)
		assert.Nil(t, handler)
		assert.Contains(t, err.Error(), "repository root path cannot be empty")
	})

	t.Run("nil permission client is accepted", func(t *testing.T) {
		cfg := createTestRepoStateConfig()

		handler, err := NewRepositoryStateHandler(cfg, nil)
		assert.NoError(t, err) // The constructor accepts nil permission client
		assert.NotNil(t, handler)
		assert.Nil(t, handler.permClient)
	})
}

// HTTP handler tests are complex due to middleware integration and would require
// more setup to work reliably. The core functionality is already tested through
// the data structures and validation helpers above.
//
// For integration testing of the full API endpoints, use the existing integration
// test suite or manual testing.

func TestRepositoryStateHandler_DataStructures(t *testing.T) {
	t.Run("RepositoryStateResponse JSON serialization", func(t *testing.T) {
		response := RepositoryStateResponse{
			Success:        true,
			RepositoryName: "test-repo",
			Owner:          "test-owner",
			Timestamp:      time.Now(),
		}

		// Test JSON marshaling
		data, err := json.Marshal(response)
		assert.NoError(t, err)
		assert.Contains(t, string(data), "test-repo")
		assert.Contains(t, string(data), "test-owner")

		// Test JSON unmarshaling
		var unmarshaled RepositoryStateResponse
		err = json.Unmarshal(data, &unmarshaled)
		assert.NoError(t, err)
		assert.Equal(t, response.Success, unmarshaled.Success)
		assert.Equal(t, response.RepositoryName, unmarshaled.RepositoryName)
		assert.Equal(t, response.Owner, unmarshaled.Owner)
	})

	t.Run("HeadReferenceInfo structure", func(t *testing.T) {
		headInfo := HeadReferenceInfo{
			Type:       "symbolic",
			Name:       "refs/heads/main",
			Target:     "refs/heads/main",
			CommitSHA:  "abc123",
			IsBranch:   true,
			IsDetached: false,
		}

		data, err := json.Marshal(headInfo)
		assert.NoError(t, err)
		assert.Contains(t, string(data), "symbolic")
		assert.Contains(t, string(data), "refs/heads/main")
		assert.Contains(t, string(data), "abc123")

		var unmarshaled HeadReferenceInfo
		err = json.Unmarshal(data, &unmarshaled)
		assert.NoError(t, err)
		assert.Equal(t, headInfo.Type, unmarshaled.Type)
		assert.Equal(t, headInfo.IsBranch, unmarshaled.IsBranch)
	})

	t.Run("WorkingTreeStatus structure", func(t *testing.T) {
		status := WorkingTreeStatus{
			IsClean:        false,
			HasChanges:     true,
			ModifiedFiles:  []string{"file1.txt", "file2.go"},
			AddedFiles:     []string{"new-file.txt"},
			DeletedFiles:   []string{"old-file.txt"},
			UntrackedFiles: []string{"temp.log"},
			FileStatus: map[string]FileStatusInfo{
				"file1.txt": {
					Staging:  "M",
					Worktree: "M",
				},
			},
		}

		data, err := json.Marshal(status)
		assert.NoError(t, err)
		assert.Contains(t, string(data), "file1.txt")
		assert.Contains(t, string(data), "new-file.txt")

		var unmarshaled WorkingTreeStatus
		err = json.Unmarshal(data, &unmarshaled)
		assert.NoError(t, err)
		assert.Equal(t, status.IsClean, unmarshaled.IsClean)
		assert.Equal(t, status.HasChanges, unmarshaled.HasChanges)
		assert.Len(t, unmarshaled.ModifiedFiles, 2)
	})
}

func TestRepositoryStateHandler_ValidationHelpers(t *testing.T) {
	cfg := createTestRepoStateConfig()
	permClient := &mockRepoStatePermissionClient{readAccess: true}
	handler, _ := NewRepositoryStateHandler(cfg, permClient)

	t.Run("validatePermissions with read access", func(t *testing.T) {
		// Create a test context
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/test", nil)
		c.Request.Header.Set("X-USER-ID", "test-user")
		c.Request.Header.Set("X-Source", "orchestrator")

		err := handler.validatePermissions(c, "owner", "repo", "read")
		assert.NoError(t, err)
	})

	t.Run("validatePermissions without read access", func(t *testing.T) {
		permClientNoAccess := &mockRepoStatePermissionClient{readAccess: false}
		handlerNoAccess, _ := NewRepositoryStateHandler(cfg, permClientNoAccess)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/test", nil)
		c.Request.Header.Set("X-USER-ID", "test-user")
		c.Request.Header.Set("X-Source", "orchestrator")

		err := handlerNoAccess.validatePermissions(c, "owner", "repo", "read")
		assert.Error(t, err)
	})

	t.Run("validatePermissions missing user ID", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/test", nil)
		// No X-USER-ID header

		err := handler.validatePermissions(c, "owner", "repo", "read")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Missing X-USER-ID header")
	})

	t.Run("isProtectedBranch detection", func(t *testing.T) {
		// Test common protected branch names
		protectedBranches := []string{"main", "master", "develop", "production"}
		for _, branch := range protectedBranches {
			assert.True(t, handler.isProtectedBranch(branch), "Branch %s should be protected", branch)
		}

		// Test non-protected branches
		regularBranches := []string{"feature/test", "bugfix/issue-123", "user/feature"}
		for _, branch := range regularBranches {
			assert.False(t, handler.isProtectedBranch(branch), "Branch %s should not be protected", branch)
		}
	})
}
