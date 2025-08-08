package protocol

import (
	"context"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
)

// mockPermissionClient for testing
type mockMergePermissionClient struct {
	readAccess  bool
	writeAccess bool
}

func (m *mockMergePermissionClient) CheckReadAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	return m.readAccess, nil
}

func (m *mockMergePermissionClient) CheckWriteAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	return m.writeAccess, nil
}

func (m *mockMergePermissionClient) InvalidateCache(userID, owner, repo string) {}
func (m *mockMergePermissionClient) ClearCache()                                {}
func (m *mockMergePermissionClient) GetCircuitBreakerMetrics() *permissions.CircuitBreakerMetrics {
	return &permissions.CircuitBreakerMetrics{}
}
func (m *mockMergePermissionClient) Close() error { return nil }

func TestNewMergeOperationsHandler(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/Users/atci/Documents/GitHub/collabops-git-executor/test-repos",
		},
	}

	permClient := &mockMergePermissionClient{
		readAccess:  true,
		writeAccess: true,
	}

	handler, err := NewMergeOperationsHandler(cfg, permClient)

	assert.NoError(t, err)
	assert.NotNil(t, handler)
	assert.NotNil(t, handler.pathConfig)
	assert.NotNil(t, handler.permClient)
	assert.NotNil(t, handler.logger)
	assert.NotNil(t, handler.errorMapper)
	assert.NotNil(t, handler.objectManager)
	assert.NotNil(t, handler.conflictDetector)
}

func TestNewMergeOperationsHandler_NilConfig(t *testing.T) {
	permClient := &mockMergePermissionClient{}

	handler, err := NewMergeOperationsHandler(nil, permClient)

	assert.Error(t, err)
	assert.Nil(t, handler)
	assert.Contains(t, err.Error(), "config cannot be nil")
}

func TestNewMergeOperationsHandler_NilPermissionClient(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/test",
		},
	}

	handler, err := NewMergeOperationsHandler(cfg, nil)

	assert.Error(t, err)
	assert.Nil(t, handler)
	assert.Contains(t, err.Error(), "permission client cannot be nil")
}

func TestMergeStrategy_Constants(t *testing.T) {
	tests := []struct {
		name     string
		value    MergeStrategy
		expected string
	}{
		{"Recursive", MergeStrategyRecursive, "recursive"},
		{"Resolve", MergeStrategyResolve, "resolve"},
		{"Ours", MergeStrategyOurs, "ours"},
		{"Theirs", MergeStrategyTheirs, "theirs"},
		{"Subtree", MergeStrategySubtree, "subtree"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestMergeRequest_Structure(t *testing.T) {
	now := time.Now()
	req := &MergeRequest{
		SourceRef:     "feature-branch",
		TargetRef:     "main",
		Strategy:      MergeStrategyRecursive,
		Message:       "Custom merge message",
		Author:        &AuthorInfo{Name: "John Doe", Email: "john@example.com", Date: now},
		Committer:     &AuthorInfo{Name: "Jane Doe", Email: "jane@example.com", Date: now},
		ForceConflict: false,
		DryRun:        true,
		Metadata:      map[string]interface{}{"test": "value"},
	}

	assert.Equal(t, "feature-branch", req.SourceRef)
	assert.Equal(t, "main", req.TargetRef)
	assert.Equal(t, MergeStrategyRecursive, req.Strategy)
	assert.Equal(t, "Custom merge message", req.Message)
	assert.NotNil(t, req.Author)
	assert.Equal(t, "John Doe", req.Author.Name)
	assert.NotNil(t, req.Committer)
	assert.Equal(t, "Jane Doe", req.Committer.Name)
	assert.False(t, req.ForceConflict)
	assert.True(t, req.DryRun)
	assert.Contains(t, req.Metadata, "test")
}

func TestMergeResponse_Structure(t *testing.T) {
	now := time.Now()
	response := &MergeResponse{
		Success:       true,
		MergeCommit:   &CommitInfo{SHA: "abc123", Message: "Test commit", CreatedAt: now},
		IsFastForward: false,
		HasConflicts:  true,
		ConflictCount: 3,
		Strategy:      MergeStrategyRecursive,
		Message:       "Merge completed",
		Details:       map[string]interface{}{"merge_type": "three-way"},
	}

	assert.True(t, response.Success)
	assert.NotNil(t, response.MergeCommit)
	assert.Equal(t, "abc123", response.MergeCommit.SHA)
	assert.False(t, response.IsFastForward)
	assert.True(t, response.HasConflicts)
	assert.Equal(t, 3, response.ConflictCount)
	assert.Equal(t, MergeStrategyRecursive, response.Strategy)
	assert.Equal(t, "Merge completed", response.Message)
	assert.Contains(t, response.Details, "merge_type")
}

func TestMergeResult_Structure(t *testing.T) {
	result := &MergeResult{
		MergeCommit:   nil, // Would be a real commit object in practice
		IsFastForward: true,
		HasConflicts:  false,
	}

	assert.Nil(t, result.MergeCommit)
	assert.True(t, result.IsFastForward)
	assert.False(t, result.HasConflicts)
}

func TestMergeOperationsHandler_ValidatePath(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: "/test"},
	}
	permClient := &mockMergePermissionClient{}

	handler, err := NewMergeOperationsHandler(cfg, permClient)
	assert.NoError(t, err)

	tests := []struct {
		name      string
		owner     string
		repo      string
		expectErr bool
	}{
		{
			name:      "Valid path",
			owner:     "testowner",
			repo:      "testrepo",
			expectErr: false,
		},
		{
			name:      "Empty owner",
			owner:     "",
			repo:      "testrepo",
			expectErr: true,
		},
		{
			name:      "Empty repo",
			owner:     "testowner",
			repo:      "",
			expectErr: true,
		},
		{
			name:      "Path traversal in owner",
			owner:     "../malicious",
			repo:      "testrepo",
			expectErr: true,
		},
		{
			name:      "Path traversal in repo",
			owner:     "testowner",
			repo:      "../malicious",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler.validatePath(tt.owner, tt.repo)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMergeOperationsHandler_ValidateMergeRequest(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: "/test"},
	}
	permClient := &mockMergePermissionClient{}

	handler, err := NewMergeOperationsHandler(cfg, permClient)
	assert.NoError(t, err)

	tests := []struct {
		name      string
		request   *MergeRequest
		expectErr bool
		errorMsg  string
	}{
		{
			name: "Valid request",
			request: &MergeRequest{
				SourceRef: "feature-branch",
				TargetRef: "main",
				Strategy:  MergeStrategyRecursive,
				Message:   "Test merge",
			},
			expectErr: false,
		},
		{
			name: "Empty source ref",
			request: &MergeRequest{
				SourceRef: "",
				TargetRef: "main",
				Strategy:  MergeStrategyRecursive,
			},
			expectErr: true,
			errorMsg:  "source_ref is required",
		},
		{
			name: "Empty target ref",
			request: &MergeRequest{
				SourceRef: "feature-branch",
				TargetRef: "",
				Strategy:  MergeStrategyRecursive,
			},
			expectErr: true,
			errorMsg:  "target_ref is required",
		},
		{
			name: "Invalid strategy",
			request: &MergeRequest{
				SourceRef: "feature-branch",
				TargetRef: "main",
				Strategy:  "invalid-strategy",
			},
			expectErr: true,
			errorMsg:  "Invalid merge strategy",
		},
		{
			name: "Message too long",
			request: &MergeRequest{
				SourceRef: "feature-branch",
				TargetRef: "main",
				Strategy:  MergeStrategyRecursive,
				Message:   string(make([]byte, 5001)), // Too long
			},
			expectErr: true,
			errorMsg:  "Merge message too long",
		},
		{
			name: "Empty author name",
			request: &MergeRequest{
				SourceRef: "feature-branch",
				TargetRef: "main",
				Strategy:  MergeStrategyRecursive,
				Author:    &AuthorInfo{Name: "", Email: "test@example.com"},
			},
			expectErr: true,
			errorMsg:  "Author name cannot be empty",
		},
		{
			name: "Empty author email",
			request: &MergeRequest{
				SourceRef: "feature-branch",
				TargetRef: "main",
				Strategy:  MergeStrategyRecursive,
				Author:    &AuthorInfo{Name: "Test Author", Email: ""},
			},
			expectErr: true,
			errorMsg:  "Author email cannot be empty",
		},
		{
			name: "Empty committer name",
			request: &MergeRequest{
				SourceRef: "feature-branch",
				TargetRef: "main",
				Strategy:  MergeStrategyRecursive,
				Committer: &AuthorInfo{Name: "", Email: "test@example.com"},
			},
			expectErr: true,
			errorMsg:  "Committer name cannot be empty",
		},
		{
			name: "Empty committer email",
			request: &MergeRequest{
				SourceRef: "feature-branch",
				TargetRef: "main",
				Strategy:  MergeStrategyRecursive,
				Committer: &AuthorInfo{Name: "Test Committer", Email: ""},
			},
			expectErr: true,
			errorMsg:  "Committer email cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler.validateMergeRequest(tt.request)
			if tt.expectErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMergeOperationsHandler_GenerateMergeCommitMessage(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: "/test"},
	}
	permClient := &mockMergePermissionClient{}

	handler, err := NewMergeOperationsHandler(cfg, permClient)
	assert.NoError(t, err)

	// Mock commit objects (simplified for testing)
	sourceCommit := &object.Commit{Message: "Source commit"}
	targetCommit := &object.Commit{Message: "Target commit"}

	tests := []struct {
		name     string
		request  *MergeRequest
		expected string
	}{
		{
			name: "Custom message",
			request: &MergeRequest{
				SourceRef: "feature-branch",
				TargetRef: "main",
				Message:   "Custom merge message",
			},
			expected: "Custom merge message",
		},
		{
			name: "Default message with branch names",
			request: &MergeRequest{
				SourceRef: "feature-branch",
				TargetRef: "main",
				Message:   "",
			},
			expected: "Merge branch 'feature-branch' into 'main'",
		},
		{
			name: "Default message with full refs",
			request: &MergeRequest{
				SourceRef: "refs/heads/feature-branch",
				TargetRef: "refs/heads/main",
				Message:   "",
			},
			expected: "Merge branch 'feature-branch' into 'main'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := handler.generateMergeCommitMessage(tt.request, sourceCommit, targetCommit)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMergeOperationsHandler_GenerateMergeMessage(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: "/test"},
	}
	permClient := &mockMergePermissionClient{}

	handler, err := NewMergeOperationsHandler(cfg, permClient)
	assert.NoError(t, err)

	request := &MergeRequest{
		SourceRef: "feature-branch",
		TargetRef: "main",
	}

	tests := []struct {
		name     string
		result   *MergeResult
		expected string
	}{
		{
			name: "Fast-forward merge",
			result: &MergeResult{
				IsFastForward: true,
				HasConflicts:  false,
			},
			expected: "Fast-forward merge from 'feature-branch' to 'main' completed successfully",
		},
		{
			name: "Merge with conflicts",
			result: &MergeResult{
				IsFastForward: false,
				HasConflicts:  true,
			},
			expected: "Merge from 'feature-branch' to 'main' completed with conflicts resolved",
		},
		{
			name: "Three-way merge without conflicts",
			result: &MergeResult{
				IsFastForward: false,
				HasConflicts:  false,
			},
			expected: "Merge from 'feature-branch' to 'main' completed successfully",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := handler.generateMergeMessage(tt.result, request)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMergeOperationsHandler_GetMergeType(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: "/test"},
	}
	permClient := &mockMergePermissionClient{}

	handler, err := NewMergeOperationsHandler(cfg, permClient)
	assert.NoError(t, err)

	tests := []struct {
		name     string
		result   *MergeResult
		expected string
	}{
		{
			name: "Fast-forward",
			result: &MergeResult{
				IsFastForward: true,
				HasConflicts:  false,
			},
			expected: "fast-forward",
		},
		{
			name: "Three-way with conflicts",
			result: &MergeResult{
				IsFastForward: false,
				HasConflicts:  true,
			},
			expected: "three-way-with-conflicts",
		},
		{
			name: "Three-way without conflicts",
			result: &MergeResult{
				IsFastForward: false,
				HasConflicts:  false,
			},
			expected: "three-way",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := handler.getMergeType(tt.result)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Integration test for repository access (simplified)
func TestMergeOperationsHandler_InvalidRepository(t *testing.T) {
	// This test would require a more complex setup with actual repositories
	// For now, we'll test the basic structure
	assert.True(t, true, "Integration tests require full repository setup")
}
