package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
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

// createTestRepository creates a temporary test repository
func createTestRepository(t *testing.T) (string, *git.Repository) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "test.git")

	// Initialize bare repository
	repo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	return repoPath, repo
}

func TestNewGitObjectManager(t *testing.T) {
	logger := testLogger()
	pathConfig, err := repository.NewPathConfig("/tmp/test")
	require.NoError(t, err)

	tests := []struct {
		name        string
		pathConfig  *repository.PathConfig
		logger      *logging.Logger
		expectError bool
	}{
		{
			name:        "Valid configuration",
			pathConfig:  pathConfig,
			logger:      logger,
			expectError: false,
		},
		{
			name:        "Nil path config",
			pathConfig:  nil,
			logger:      logger,
			expectError: true,
		},
		{
			name:        "Nil logger",
			pathConfig:  pathConfig,
			logger:      nil,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gom, err := NewGitObjectManager(tt.pathConfig, tt.logger)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, gom)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, gom)
				assert.Equal(t, tt.pathConfig, gom.pathConfig)
				assert.Equal(t, tt.logger, gom.logger)
			}
		})
	}
}

func TestGitObjectManager_ValidateFileChanges(t *testing.T) {
	logger := testLogger()
	pathConfig, err := repository.NewPathConfig("/tmp/test")
	require.NoError(t, err)

	gom, err := NewGitObjectManager(pathConfig, logger)
	require.NoError(t, err)

	ctx := context.Background()

	tests := []struct {
		name        string
		changes     []FileChange
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Empty changes",
			changes:     []FileChange{},
			expectError: true,
			errorMsg:    "no file changes provided",
		},
		{
			name: "Valid create operation",
			changes: []FileChange{
				{
					Path:      "src/main.go",
					Operation: FileOperationCreate,
					Content:   []byte("package main"),
				},
			},
			expectError: false,
		},
		{
			name: "Valid update operation",
			changes: []FileChange{
				{
					Path:      "src/main.go",
					Operation: FileOperationUpdate,
					Content:   []byte("package main\n\nfunc main() {}"),
				},
			},
			expectError: false,
		},
		{
			name: "Valid delete operation",
			changes: []FileChange{
				{
					Path:      "src/old.go",
					Operation: FileOperationDelete,
				},
			},
			expectError: false,
		},
		{
			name: "Invalid operation",
			changes: []FileChange{
				{
					Path:      "src/main.go",
					Operation: "invalid",
					Content:   []byte("content"),
				},
			},
			expectError: true,
			errorMsg:    "invalid operation",
		},
		{
			name: "Create without content",
			changes: []FileChange{
				{
					Path:      "src/main.go",
					Operation: FileOperationCreate,
				},
			},
			expectError: true,
			errorMsg:    "content is required",
		},
		{
			name: "Delete with content",
			changes: []FileChange{
				{
					Path:      "src/main.go",
					Operation: FileOperationDelete,
					Content:   []byte("should not have content"),
				},
			},
			expectError: true,
			errorMsg:    "content should not be provided",
		},
		{
			name: "Duplicate paths",
			changes: []FileChange{
				{
					Path:      "src/main.go",
					Operation: FileOperationCreate,
					Content:   []byte("content1"),
				},
				{
					Path:      "src/main.go",
					Operation: FileOperationUpdate,
					Content:   []byte("content2"),
				},
			},
			expectError: true,
			errorMsg:    "duplicate path",
		},
		{
			name: "Empty path",
			changes: []FileChange{
				{
					Path:      "",
					Operation: FileOperationCreate,
					Content:   []byte("content"),
				},
			},
			expectError: true,
			errorMsg:    "path is required",
		},
		{
			name: "Invalid file mode",
			changes: []FileChange{
				{
					Path:      "src/main.go",
					Operation: FileOperationCreate,
					Content:   []byte("content"),
					Mode:      func() *filemode.FileMode { mode := filemode.FileMode(0777); return &mode }(), // Invalid mode
				},
			},
			expectError: true,
			errorMsg:    "invalid file mode",
		},
		{
			name: "Valid executable mode",
			changes: []FileChange{
				{
					Path:      "bin/script.sh",
					Operation: FileOperationCreate,
					Content:   []byte("#!/bin/bash\necho hello"),
					Mode:      func() *filemode.FileMode { mode := filemode.Executable; return &mode }(),
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := gom.ValidateFileChanges(ctx, tt.changes)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGitObjectManager_OpenRepository(t *testing.T) {
	logger := testLogger()

	// Create a temporary directory for our test repositories
	tempDir := t.TempDir()
	pathConfig, err := repository.NewPathConfig(tempDir)
	require.NoError(t, err)

	gom, err := NewGitObjectManager(pathConfig, logger)
	require.NoError(t, err)

	// Create a test repository
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	err = os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	_, err = git.PlainInit(repoPath, true)
	require.NoError(t, err)

	ctx := context.Background()

	tests := []struct {
		name        string
		owner       string
		repo        string
		expectError bool
	}{
		{
			name:        "Valid repository",
			owner:       "testowner",
			repo:        "testrepo", // Don't include .git here - it's added by GetRepoPath
			expectError: false,
		},
		{
			name:        "Non-existent repository",
			owner:       "testowner",
			repo:        "nonexistent",
			expectError: true,
		},
		{
			name:        "Invalid owner",
			owner:       "../invalid",
			repo:        "testrepo",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, err := gom.OpenRepository(ctx, tt.owner, tt.repo)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, repo)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, repo)
			}
		})
	}
}

func TestGitObjectManager_CreateTreeFromChanges(t *testing.T) {
	logger := testLogger()

	// Create a temporary repository
	repoPath, repo := createTestRepository(t)
	pathConfig, err := repository.NewPathConfig(filepath.Dir(repoPath))
	require.NoError(t, err)

	gom, err := NewGitObjectManager(pathConfig, logger)
	require.NoError(t, err)

	ctx := context.Background()

	// Test creating a tree from changes
	changes := []FileChange{
		{
			Path:      "README.md",
			Operation: FileOperationCreate,
			Content:   []byte("# Test Repository\n\nThis is a test."),
		},
		{
			Path:      "src/main.go",
			Operation: FileOperationCreate,
			Content:   []byte("package main\n\nfunc main() {\n\tprintln(\"Hello, World!\")\n}"),
		},
		{
			Path:      "bin/script.sh",
			Operation: FileOperationCreate,
			Content:   []byte("#!/bin/bash\necho 'Hello from script'"),
			Mode:      func() *filemode.FileMode { mode := filemode.Executable; return &mode }(),
		},
	}

	result, err := gom.CreateTreeFromChanges(ctx, repo, nil, changes)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.False(t, result.TreeHash.IsZero())
	assert.Len(t, result.CreatedFiles, 3)
	assert.Len(t, result.ChangedFiles, 0)
	assert.Len(t, result.DeletedFiles, 0)
	assert.Contains(t, result.CreatedFiles, "README.md")
	assert.Contains(t, result.CreatedFiles, "src/main.go")
	assert.Contains(t, result.CreatedFiles, "bin/script.sh")
}

func TestGitObjectManager_CreateCommit(t *testing.T) {
	logger := testLogger()

	// Create a temporary repository
	repoPath, repo := createTestRepository(t)
	pathConfig, err := repository.NewPathConfig(filepath.Dir(repoPath))
	require.NoError(t, err)

	gom, err := NewGitObjectManager(pathConfig, logger)
	require.NoError(t, err)

	ctx := context.Background()

	// First create a tree
	changes := []FileChange{
		{
			Path:      "README.md",
			Operation: FileOperationCreate,
			Content:   []byte("# Test Repository"),
		},
	}

	treeResult, err := gom.CreateTreeFromChanges(ctx, repo, nil, changes)
	require.NoError(t, err)

	// Create commit info
	author := object.Signature{
		Name:  "Test Author",
		Email: "test@example.com",
		When:  time.Now(),
	}

	commitInfo := CommitInfo{
		Message: "Initial commit",
		Author:  author,
		Parents: []plumbing.Hash{}, // No parents for initial commit
	}

	// Create the commit
	commitResult, err := gom.CreateCommit(ctx, repo, treeResult.TreeHash, commitInfo)
	require.NoError(t, err)
	assert.NotNil(t, commitResult)
	assert.False(t, commitResult.Hash.IsZero())
	assert.Equal(t, treeResult.TreeHash, commitResult.TreeHash)
	assert.Equal(t, "Initial commit", commitResult.Message)
	assert.Equal(t, "Test Author", commitResult.Author.Name)
	assert.Equal(t, "test@example.com", commitResult.Author.Email)
}

func TestGitObjectManager_ResolveReference(t *testing.T) {
	logger := testLogger()

	// Use existing test repository
	tempDir := t.TempDir()
	pathConfig, err := repository.NewPathConfig(tempDir)
	require.NoError(t, err)

	gom, err := NewGitObjectManager(pathConfig, logger)
	require.NoError(t, err)

	// Create a repository with some commits for testing
	repoPath := filepath.Join(tempDir, "test.git")
	repo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	ctx := context.Background()

	// Test resolving non-existent reference
	_, err = gom.ResolveReference(ctx, repo, "refs/heads/main")
	assert.Error(t, err) // Should fail since repository is empty

	// Test with invalid hash
	_, err = gom.ResolveReference(ctx, repo, "invalid-hash")
	assert.Error(t, err)
}
