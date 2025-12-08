package merge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/collabops/git-executor/internal/config"
)

// TestNewMergePreviewHandler tests merge preview handler creation
func TestNewMergePreviewHandler(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/tmp/test-repos",
		},
	}

	handler, err := NewMergePreviewHandler(cfg)
	require.NoError(t, err)
	require.NotNil(t, handler)
}

// TestNewMergePreviewHandlerWithNilConfig tests error handling
func TestNewMergePreviewHandlerWithNilConfig(t *testing.T) {
	handler, err := NewMergePreviewHandler(nil)
	assert.Error(t, err)
	assert.Nil(t, handler)
	assert.Contains(t, err.Error(), "config cannot be nil")
}

// TestNewMergeOperationsHandler tests merge operations handler creation
func TestNewMergeOperationsHandler(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/tmp/test-repos",
		},
	}

	handler, err := NewMergeOperationsHandler(cfg)
	require.NoError(t, err)
	require.NotNil(t, handler)
}

// TestNewMergeOperationsHandlerWithNilConfig tests error handling
func TestNewMergeOperationsHandlerWithNilConfig(t *testing.T) {
	handler, err := NewMergeOperationsHandler(nil)
	assert.Error(t, err)
	assert.Nil(t, handler)
	assert.Contains(t, err.Error(), "config cannot be nil")
}

// TestMergePreviewResponseStructure tests merge preview response structure
func TestMergePreviewResponseStructure(t *testing.T) {
	repo, cleanup := setupTestRepository(t)
	defer cleanup()

	handler := setupTestHandler(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create simple history
	writeFile(t, wt, "file1.txt", "content1")
	commit1, err := commitFile(t, wt, "file1.txt", "Initial commit")
	require.NoError(t, err)

	// Create feature branch
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	writeFile(t, wt, "file2.txt", "content2")
	commit2, err := commitFile(t, wt, "file2.txt", "Add file2")
	require.NoError(t, err)

	// Test merge preview
	req := &MergePreviewRequest{
		SourceRef:       commit2.String(),
		TargetRef:       commit1.String(),
		Strategy:        MergeStrategyRecursive,
		IncludeContent:  false,
		MaxFileSize:     1024 * 1024,
		MaxFiles:        1000,
		CompareStrategy: "diff",
	}

	ctx := context.Background()
	startTime := time.Now()
	response, err := handler.performMergePreview(ctx, repo, commit2, commit1, req, startTime)
	require.NoError(t, err)
	require.NotNil(t, response)

	// Test response structure
	assert.NotNil(t, response)
	assert.IsType(t, true, response.CanMerge)
	assert.IsType(t, true, response.IsFastForward)
	assert.IsType(t, true, response.HasConflicts)
	assert.IsType(t, 0, response.ConflictCount)
	assert.NotEmpty(t, string(response.Strategy))
	assert.NotNil(t, response.ChangesSummary)
	assert.NotNil(t, response.CountSummary)

	// Test CountSummary
	countSummary := response.CountSummary
	require.NotNil(t, countSummary)
	assert.GreaterOrEqual(t, countSummary.Commits, 0)
	assert.GreaterOrEqual(t, countSummary.Files, 0)
	assert.GreaterOrEqual(t, countSummary.Contributors, 0)

	// Test ChangesSummary
	changesSummary := response.ChangesSummary
	require.NotNil(t, changesSummary)
	assert.GreaterOrEqual(t, changesSummary.TotalFiles, 0)
	assert.GreaterOrEqual(t, changesSummary.AddedFiles, 0)
	assert.GreaterOrEqual(t, changesSummary.ModifiedFiles, 0)
	assert.GreaterOrEqual(t, changesSummary.DeletedFiles, 0)
}

// TestValidateMergePreviewRequest tests merge preview request validation
func TestValidateMergePreviewRequest(t *testing.T) {
	handler := setupTestHandler(t)

	tests := []struct {
		name    string
		req     *MergePreviewRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "ValidRequest",
			req: &MergePreviewRequest{
				SourceRef:       "feature",
				TargetRef:       "main",
				Strategy:        MergeStrategyRecursive,
				MaxFileSize:     1024 * 1024,
				MaxFiles:        1000,
				CompareStrategy: "diff",
			},
			wantErr: false,
		},
		{
			name: "MissingSourceRef",
			req: &MergePreviewRequest{
				TargetRef:       "main",
				Strategy:        MergeStrategyRecursive,
				CompareStrategy: "diff",
			},
			wantErr: true,
			errMsg:  "source_ref is required",
		},
		{
			name: "MissingTargetRef",
			req: &MergePreviewRequest{
				SourceRef:       "feature",
				Strategy:        MergeStrategyRecursive,
				CompareStrategy: "diff",
			},
			wantErr: true,
			errMsg:  "target_ref is required",
		},
		{
			name: "InvalidStrategy",
			req: &MergePreviewRequest{
				SourceRef:       "feature",
				TargetRef:       "main",
				Strategy:        MergeStrategy("invalid"),
				CompareStrategy: "diff",
			},
			wantErr: true,
			errMsg:  "Invalid merge strategy",
		},
		{
			name: "InvalidMaxFileSize",
			req: &MergePreviewRequest{
				SourceRef:       "feature",
				TargetRef:       "main",
				Strategy:        MergeStrategyRecursive,
				MaxFileSize:     -1,
				CompareStrategy: "diff",
			},
			wantErr: true,
			errMsg:  "max_file_size cannot be negative",
		},
		{
			name: "InvalidMaxFiles",
			req: &MergePreviewRequest{
				SourceRef:       "feature",
				TargetRef:       "main",
				Strategy:        MergeStrategyRecursive,
				MaxFiles:        -1,
				CompareStrategy: "diff",
			},
			wantErr: true,
			errMsg:  "max_files cannot be negative",
		},
		{
			name: "InvalidCompareStrategy",
			req: &MergePreviewRequest{
				SourceRef:       "feature",
				TargetRef:       "main",
				Strategy:        MergeStrategyRecursive,
				CompareStrategy: "invalid",
			},
			wantErr: true,
			errMsg:  "Invalid compare strategy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, err := handler.validateMergePreviewRequest(tt.req)
			if tt.wantErr {
				assert.Error(t, err)
				assert.NotEqual(t, 200, status)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, 200, status)
			}
		})
	}
}

// Helper functions (shared with merge_test.go)

func setupTestRepository(t *testing.T) (*gogit.Repository, func()) {
	tempDir, err := os.MkdirTemp("", "merge-test-*")
	require.NoError(t, err)

	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	// Set default branch to main
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))
	err = repo.Storer.SetReference(headRef)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return repo, cleanup
}

func setupTestHandler(t *testing.T) *MergePreviewHandler {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/tmp/test-repos",
		},
	}

	handler, err := NewMergePreviewHandler(cfg)
	require.NoError(t, err)
	return handler
}

func writeFile(t *testing.T, wt *gogit.Worktree, filename, content string) {
	repoPath := wt.Filesystem.Root()
	filePath := filepath.Join(repoPath, filename)
	dirPath := filepath.Dir(filePath)
	err := os.MkdirAll(dirPath, 0755)
	require.NoError(t, err)
	err = os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err)
}

func commitFile(t *testing.T, wt *gogit.Worktree, filename, message string) (plumbing.Hash, error) {
	_, err := wt.Add(filename)
	require.NoError(t, err)

	hash, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	return hash, nil
}
