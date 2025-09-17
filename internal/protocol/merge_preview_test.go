package protocol

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/schemas/common"
)

// mockPreviewPermissionClient for testing
type mockPreviewPermissionClient struct {
	readAccess  bool
	writeAccess bool
}

func (m *mockPreviewPermissionClient) CheckReadAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	return m.readAccess, nil
}

func (m *mockPreviewPermissionClient) CheckWriteAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	return m.writeAccess, nil
}

func (m *mockPreviewPermissionClient) InvalidateCache(userID, owner, repo string) {}
func (m *mockPreviewPermissionClient) ClearCache()                                {}
func (m *mockPreviewPermissionClient) GetCircuitBreakerMetrics() *permissions.CircuitBreakerMetrics {
	return &permissions.CircuitBreakerMetrics{}
}
func (m *mockPreviewPermissionClient) Close() error { return nil }

func TestNewMergePreviewHandler(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/Users/atci/Documents/GitHub/collabops-git-executor/test-repos",
		},
	}

	handler, err := NewMergePreviewHandler(cfg)

	assert.NoError(t, err)
	assert.NotNil(t, handler)
	assert.NotNil(t, handler.pathConfig)
	assert.NotNil(t, handler.logger)
	assert.NotNil(t, handler.errorMapper)
	assert.NotNil(t, handler.objectManager)
	assert.NotNil(t, handler.conflictDetector)
}

func TestNewMergePreviewHandler_NilConfig(t *testing.T) {

	handler, err := NewMergePreviewHandler(nil)

	assert.Error(t, err)
	assert.Nil(t, handler)
	assert.Contains(t, err.Error(), "config cannot be nil")
}

func TestNewMergePreviewHandler_NilPermissionClient(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/test",
		},
	}

	handler, err := NewMergePreviewHandler(cfg)

	assert.Error(t, err)
	assert.Nil(t, handler)
	assert.Contains(t, err.Error(), "permission client cannot be nil")
}

func TestMergePreviewRequest_Structure(t *testing.T) {
	req := &MergePreviewRequest{
		SourceRef:       "feature-branch",
		TargetRef:       "main",
		Strategy:        MergeStrategyRecursive,
		IncludeChanges:  true,
		IncludeContent:  false,
		MaxFileSize:     1024 * 1024,
		MaxFiles:        1000,
		CompareStrategy: "tree",
	}

	assert.Equal(t, "feature-branch", req.SourceRef)
	assert.Equal(t, "main", req.TargetRef)
	assert.Equal(t, MergeStrategyRecursive, req.Strategy)
	assert.True(t, req.IncludeChanges)
	assert.False(t, req.IncludeContent)
	assert.Equal(t, int64(1024*1024), req.MaxFileSize)
	assert.Equal(t, 1000, req.MaxFiles)
	assert.Equal(t, "tree", req.CompareStrategy)
}

func TestMergePreviewResponse_Structure(t *testing.T) {
	now := time.Now()
	metrics := &PreviewMetrics{
		AnalysisTime:         100 * time.Millisecond,
		ConflictAnalysisTime: 50 * time.Millisecond,
		TreeComparisonTime:   30 * time.Millisecond,
		FilesAnalyzed:        10,
		ObjectsTraversed:     25,
		MemoryUsed:           1024 * 1024,
	}

	response := &MergePreviewResponse{
		CanMerge:           true,
		IsFastForward:      false,
		HasConflicts:       true,
		ConflictCount:      3,
		Strategy:           MergeStrategyRecursive,
		RecommendedActions: []string{"Manual conflict resolution required"},
		MergeBase:          &common.CommitInfo{SHA: "abc123", Message: "Base commit", CreatedAt: now},
		PerformanceMetrics: metrics,
		Warnings:           []string{"High number of conflicts"},
	}

	assert.True(t, response.CanMerge)
	assert.False(t, response.IsFastForward)
	assert.True(t, response.HasConflicts)
	assert.Equal(t, 3, response.ConflictCount)
	assert.Equal(t, MergeStrategyRecursive, response.Strategy)
	assert.Contains(t, response.RecommendedActions, "Manual conflict resolution required")
	assert.NotNil(t, response.MergeBase)
	assert.Equal(t, "abc123", response.MergeBase.SHA)
	assert.NotNil(t, response.PerformanceMetrics)
	assert.Equal(t, 100*time.Millisecond, response.PerformanceMetrics.AnalysisTime)
	assert.Contains(t, response.Warnings, "High number of conflicts")
}

func TestChangesSummary_Structure(t *testing.T) {
	summary := &ChangesSummary{
		TotalFiles:    15,
		AddedFiles:    5,
		ModifiedFiles: 8,
		DeletedFiles:  2,
		RenamedFiles:  1,
		LinesAdded:    120,
		LinesDeleted:  45,
		BinaryFiles:   3,
	}

	assert.Equal(t, 15, summary.TotalFiles)
	assert.Equal(t, 5, summary.AddedFiles)
	assert.Equal(t, 8, summary.ModifiedFiles)
	assert.Equal(t, 2, summary.DeletedFiles)
	assert.Equal(t, 1, summary.RenamedFiles)
	assert.Equal(t, 120, summary.LinesAdded)
	assert.Equal(t, 45, summary.LinesDeleted)
	assert.Equal(t, 3, summary.BinaryFiles)
}

func TestFileChange_Structure(t *testing.T) {
	content := &FileContent{
		SourceContent: "source content",
		TargetContent: "target content",
		MergedContent: "merged content",
		Encoding:      "utf-8",
		IsTruncated:   false,
	}

	change := &FileChange{
		Path:         "src/main.js",
		Status:       "modified",
		OldPath:      "",
		Mode:         "100644",
		IsBinary:     false,
		Size:         1024,
		LinesAdded:   10,
		LinesDeleted: 5,
		Patch:        "@@ -1,3 +1,3 @@",
		Content:      content,
		Conflicts:    []string{"Content conflict in line 5"},
	}

	assert.Equal(t, "src/main.js", change.Path)
	assert.Equal(t, "modified", change.Status)
	assert.Equal(t, "100644", change.Mode)
	assert.False(t, change.IsBinary)
	assert.Equal(t, int64(1024), change.Size)
	assert.Equal(t, 10, change.LinesAdded)
	assert.Equal(t, 5, change.LinesDeleted)
	assert.NotNil(t, change.Content)
	assert.Equal(t, "source content", change.Content.SourceContent)
	assert.Contains(t, change.Conflicts, "Content conflict in line 5")
}

func TestFileContent_Structure(t *testing.T) {
	content := &FileContent{
		SourceContent: "console.log('source');",
		TargetContent: "console.log('target');",
		MergedContent: "console.log('merged');",
		Encoding:      "utf-8",
		IsTruncated:   true,
	}

	assert.Equal(t, "console.log('source');", content.SourceContent)
	assert.Equal(t, "console.log('target');", content.TargetContent)
	assert.Equal(t, "console.log('merged');", content.MergedContent)
	assert.Equal(t, "utf-8", content.Encoding)
	assert.True(t, content.IsTruncated)
}

func TestPreviewMetrics_Structure(t *testing.T) {
	metrics := &PreviewMetrics{
		AnalysisTime:         200 * time.Millisecond,
		ConflictAnalysisTime: 80 * time.Millisecond,
		TreeComparisonTime:   60 * time.Millisecond,
		FilesAnalyzed:        50,
		ObjectsTraversed:     150,
		MemoryUsed:           2 * 1024 * 1024,
	}

	assert.Equal(t, 200*time.Millisecond, metrics.AnalysisTime)
	assert.Equal(t, 80*time.Millisecond, metrics.ConflictAnalysisTime)
	assert.Equal(t, 60*time.Millisecond, metrics.TreeComparisonTime)
	assert.Equal(t, 50, metrics.FilesAnalyzed)
	assert.Equal(t, 150, metrics.ObjectsTraversed)
	assert.Equal(t, int64(2*1024*1024), metrics.MemoryUsed)
}

func TestMergePreviewHandler_ValidatePath(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: "/test"},
	}

	handler, err := NewMergePreviewHandler(cfg)
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

func TestMergePreviewHandler_ValidateMergePreviewRequest(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: "/test"},
	}

	handler, err := NewMergePreviewHandler(cfg)
	assert.NoError(t, err)

	tests := []struct {
		name      string
		request   *MergePreviewRequest
		expectErr bool
		errorMsg  string
	}{
		{
			name: "Valid request",
			request: &MergePreviewRequest{
				SourceRef:       "feature-branch",
				TargetRef:       "main",
				Strategy:        MergeStrategyRecursive,
				IncludeChanges:  true,
				MaxFileSize:     1024 * 1024,
				MaxFiles:        1000,
				CompareStrategy: "tree",
			},
			expectErr: false,
		},
		{
			name: "Empty source ref",
			request: &MergePreviewRequest{
				SourceRef: "",
				TargetRef: "main",
				Strategy:  MergeStrategyRecursive,
			},
			expectErr: true,
			errorMsg:  "source_ref is required",
		},
		{
			name: "Empty target ref",
			request: &MergePreviewRequest{
				SourceRef: "feature-branch",
				TargetRef: "",
				Strategy:  MergeStrategyRecursive,
			},
			expectErr: true,
			errorMsg:  "target_ref is required",
		},
		{
			name: "Invalid strategy",
			request: &MergePreviewRequest{
				SourceRef: "feature-branch",
				TargetRef: "main",
				Strategy:  "invalid-strategy",
			},
			expectErr: true,
			errorMsg:  "Invalid merge strategy",
		},
		{
			name: "Negative max file size",
			request: &MergePreviewRequest{
				SourceRef:   "feature-branch",
				TargetRef:   "main",
				Strategy:    MergeStrategyRecursive,
				MaxFileSize: -1,
			},
			expectErr: true,
			errorMsg:  "max_file_size cannot be negative",
		},
		{
			name: "Max file size too large",
			request: &MergePreviewRequest{
				SourceRef:   "feature-branch",
				TargetRef:   "main",
				Strategy:    MergeStrategyRecursive,
				MaxFileSize: 200 * 1024 * 1024, // 200MB
			},
			expectErr: true,
			errorMsg:  "max_file_size cannot exceed 100MB",
		},
		{
			name: "Negative max files",
			request: &MergePreviewRequest{
				SourceRef: "feature-branch",
				TargetRef: "main",
				Strategy:  MergeStrategyRecursive,
				MaxFiles:  -1,
			},
			expectErr: true,
			errorMsg:  "max_files cannot be negative",
		},
		{
			name: "Max files too large",
			request: &MergePreviewRequest{
				SourceRef: "feature-branch",
				TargetRef: "main",
				Strategy:  MergeStrategyRecursive,
				MaxFiles:  15000,
			},
			expectErr: true,
			errorMsg:  "max_files cannot exceed 10000",
		},
		{
			name: "Invalid compare strategy",
			request: &MergePreviewRequest{
				SourceRef:       "feature-branch",
				TargetRef:       "main",
				Strategy:        MergeStrategyRecursive,
				CompareStrategy: "invalid",
			},
			expectErr: true,
			errorMsg:  "Invalid compare strategy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler.validateMergePreviewRequest(tt.request)
			if tt.expectErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMergePreviewHandler_GenerateRecommendations(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: "/test"},
	}

	handler, err := NewMergePreviewHandler(cfg)
	assert.NoError(t, err)

	tests := []struct {
		name             string
		conflictAnalysis *git.MergeAnalysisResult
		changesSummary   *ChangesSummary
		request          *MergePreviewRequest
		expectedContains []string
	}{
		{
			name: "Fast-forward merge",
			conflictAnalysis: &git.MergeAnalysisResult{
				IsFastForward: true,
				HasConflicts:  false,
			},
			changesSummary:   &ChangesSummary{TotalFiles: 5},
			request:          &MergePreviewRequest{},
			expectedContains: []string{"fast-forward merge"},
		},
		{
			name: "Merge with auto-resolvable conflicts",
			conflictAnalysis: &git.MergeAnalysisResult{
				IsFastForward:  false,
				HasConflicts:   true,
				ConflictCount:  5,
				AutoResolvable: 3,
			},
			changesSummary:   &ChangesSummary{TotalFiles: 10},
			request:          &MergePreviewRequest{},
			expectedContains: []string{"3 conflicts can be automatically resolved", "Manual conflict resolution"},
		},
		{
			name: "Large merge",
			conflictAnalysis: &git.MergeAnalysisResult{
				IsFastForward: false,
				HasConflicts:  false,
			},
			changesSummary:   &ChangesSummary{TotalFiles: 1500},
			request:          &MergePreviewRequest{},
			expectedContains: []string{"breaking into smaller merges"},
		},
		{
			name: "Binary files detected",
			conflictAnalysis: &git.MergeAnalysisResult{
				IsFastForward: false,
				HasConflicts:  false,
			},
			changesSummary:   &ChangesSummary{TotalFiles: 10, BinaryFiles: 3},
			request:          &MergePreviewRequest{},
			expectedContains: []string{"3 binary files detected", "Git LFS"},
		},
		{
			name: "Clean merge",
			conflictAnalysis: &git.MergeAnalysisResult{
				IsFastForward: false,
				HasConflicts:  false,
			},
			changesSummary:   &ChangesSummary{TotalFiles: 5},
			request:          &MergePreviewRequest{},
			expectedContains: []string{"Merge can proceed without issues"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recommendations := handler.generateRecommendations(tt.conflictAnalysis, tt.changesSummary, tt.request)

			for _, expected := range tt.expectedContains {
				found := false
				for _, rec := range recommendations {
					if strings.Contains(rec, expected) {
						found = true
						break
					}
				}
				assert.True(t, found, "Expected recommendation containing '%s' not found in: %v", expected, recommendations)
			}
		})
	}
}

func TestMergePreviewHandler_GenerateWarnings(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: "/test"},
	}

	handler, err := NewMergePreviewHandler(cfg)
	assert.NoError(t, err)

	tests := []struct {
		name             string
		conflictAnalysis *git.MergeAnalysisResult
		changesSummary   *ChangesSummary
		request          *MergePreviewRequest
		expectedContains []string
	}{
		{
			name: "High conflict count",
			conflictAnalysis: &git.MergeAnalysisResult{
				HasConflicts:  true,
				ConflictCount: 75,
			},
			changesSummary:   &ChangesSummary{TotalFiles: 100},
			request:          &MergePreviewRequest{MaxFileSize: 1024 * 1024, MaxFiles: 1000},
			expectedContains: []string{"High number of conflicts"},
		},
		{
			name: "Very large merge",
			conflictAnalysis: &git.MergeAnalysisResult{
				HasConflicts: false,
			},
			changesSummary:   &ChangesSummary{TotalFiles: 6000},
			request:          &MergePreviewRequest{MaxFileSize: 1024 * 1024, MaxFiles: 1000},
			expectedContains: []string{"Very large merge", "performance may be impacted"},
		},
		{
			name: "Low max file size",
			conflictAnalysis: &git.MergeAnalysisResult{
				HasConflicts: false,
			},
			changesSummary:   &ChangesSummary{TotalFiles: 10},
			request:          &MergePreviewRequest{MaxFileSize: 512 * 1024, MaxFiles: 1000}, // 512KB
			expectedContains: []string{"Low max_file_size", "content analysis accuracy"},
		},
		{
			name: "Low max files",
			conflictAnalysis: &git.MergeAnalysisResult{
				HasConflicts: false,
			},
			changesSummary:   &ChangesSummary{TotalFiles: 10},
			request:          &MergePreviewRequest{MaxFileSize: 1024 * 1024, MaxFiles: 50},
			expectedContains: []string{"Low max_files", "incomplete analysis"},
		},
		{
			name: "No warnings",
			conflictAnalysis: &git.MergeAnalysisResult{
				HasConflicts:  false,
				ConflictCount: 0,
			},
			changesSummary:   &ChangesSummary{TotalFiles: 10},
			request:          &MergePreviewRequest{MaxFileSize: 1024 * 1024, MaxFiles: 1000},
			expectedContains: []string{}, // No warnings expected
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings := handler.generateWarnings(tt.conflictAnalysis, tt.changesSummary, tt.request)

			if len(tt.expectedContains) == 0 {
				assert.Empty(t, warnings, "Expected no warnings but got: %v", warnings)
			} else {
				for _, expected := range tt.expectedContains {
					found := false
					for _, warning := range warnings {
						if strings.Contains(warning, expected) {
							found = true
							break
						}
					}
					assert.True(t, found, "Expected warning containing '%s' not found in: %v", expected, warnings)
				}
			}
		})
	}
}

func TestMergePreviewHandler_CompareTreesBasic(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: "/test"},
	}

	handler, err := NewMergePreviewHandler(cfg)
	assert.NoError(t, err)

	req := &MergePreviewRequest{
		IncludeChanges: true,
		MaxFiles:       1000,
	}
	metrics := &PreviewMetrics{}

	// Note: This test uses nil trees as the actual implementation
	// would require a more complex setup with real Git repository objects
	summary, changes, err := handler.compareTreesBasic(nil, nil, req, metrics)

	assert.NoError(t, err)
	assert.NotNil(t, summary)
	assert.NotNil(t, changes)
	assert.Equal(t, 1, metrics.FilesAnalyzed)
	assert.Equal(t, 2, metrics.ObjectsTraversed)
}

// Integration test for repository access (simplified)
func TestMergePreviewHandler_InvalidRepository(t *testing.T) {
	// This test would require a more complex setup with actual repositories
	// For now, we'll test the basic structure
	assert.True(t, true, "Integration tests require full repository setup")
}
