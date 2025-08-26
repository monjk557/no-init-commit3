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
	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRepositoryManagerTest(t *testing.T) (*DefaultRepositoryManager, string, func()) {
	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "repo-manager-test-*")
	require.NoError(t, err)

	// Create path config
	pathConfig, err := repository.NewPathConfig(tempDir)
	require.NoError(t, err)

	// Create logger
	logger, err := logging.NewLogger(config.LoggingConfig{
		Level:  "info",
		Format: "json",
	})
	require.NoError(t, err)

	// Create repository manager
	repoManager, err := NewRepositoryManager(pathConfig, logger)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return repoManager, tempDir, cleanup
}

func TestDefaultRepositoryManager_CreateRepository(t *testing.T) {
	repoManager, tempDir, cleanup := setupRepositoryManagerTest(t)
	defer cleanup()

	ctx := context.Background()

	tests := []struct {
		name           string
		options        *CreateRepositoryOptions
		expectError    bool
		expectInitial  bool
		expectedBranch string
	}{
		{
			name: "create basic bare repository",
			options: &CreateRepositoryOptions{
				Path:          filepath.Join(tempDir, "owner", "basic-repo.git"),
				Bare:          true,
				Description:   "Basic test repository",
				DefaultBranch: "main",
				InitialCommit: false,
			},
			expectError:    false,
			expectInitial:  false,
			expectedBranch: "main",
		},
		{
			name: "create repository with initial commit",
			options: &CreateRepositoryOptions{
				Path:          filepath.Join(tempDir, "owner", "initial-repo.git"),
				Bare:          true,
				Description:   "Repository with initial commit",
				DefaultBranch: "main",
				InitialCommit: true,
				Author: &CommitAuthor{
					Name:  "Test User",
					Email: "test@example.com",
					Date:  time.Now(),
				},
			},
			expectError:    false,
			expectInitial:  true,
			expectedBranch: "main",
		},
		{
			name: "create repository with custom branch",
			options: &CreateRepositoryOptions{
				Path:          filepath.Join(tempDir, "owner", "custom-branch.git"),
				Bare:          true,
				Description:   "Repository with custom branch",
				DefaultBranch: "develop",
				InitialCommit: true,
			},
			expectError:    false,
			expectInitial:  true,
			expectedBranch: "develop",
		},
		{
			name: "create repository with initial files",
			options: &CreateRepositoryOptions{
				Path:          filepath.Join(tempDir, "owner", "with-files.git"),
				Bare:          true,
				Description:   "Repository with initial files",
				DefaultBranch: "main",
				InitialCommit: true,
				InitialFiles: map[string]string{
					"README.md":     "# Test Repository\n\nThis is a test.",
					"src/main.go":   "package main\n\nfunc main() {\n\tprintln(\"Hello World\")\n}",
					"docs/guide.md": "# Guide\n\nThis is a guide.",
				},
			},
			expectError:    false,
			expectInitial:  true,
			expectedBranch: "main",
		},
		{
			name: "empty path should fail",
			options: &CreateRepositoryOptions{
				Path: "",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoInfo, err := repoManager.CreateRepository(ctx, tt.options)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, repoInfo)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, repoInfo)

			// Check repository info
			assert.Equal(t, tt.options.Path, repoInfo.Path)
			assert.Equal(t, tt.options.Description, repoInfo.Description)
			assert.Equal(t, tt.expectedBranch, repoInfo.DefaultBranch)
			assert.True(t, repoInfo.IsBare)
			assert.NotZero(t, repoInfo.CreatedAt)

			// Check that repository was actually created
			_, err = os.Stat(tt.options.Path)
			assert.NoError(t, err, "Repository directory should exist")

			// Check that it's a valid Git repository
			gitRepo, err := gogit.PlainOpen(tt.options.Path)
			assert.NoError(t, err, "Should be a valid Git repository")

			if tt.expectInitial {
				// Check that initial commit exists
				head, err := gitRepo.Head()
				assert.NoError(t, err, "Should have HEAD reference")
				assert.Equal(t, tt.expectedBranch, head.Name().Short(), "Should point to correct branch")

				// Check that commit exists
				_, err = gitRepo.CommitObject(head.Hash())
				assert.NoError(t, err, "Initial commit should exist")

				// If initial files were specified, check them
				if tt.options.InitialFiles != nil {
					commit, err := gitRepo.CommitObject(head.Hash())
					require.NoError(t, err)

					tree, err := gitRepo.TreeObject(commit.TreeHash)
					require.NoError(t, err)

					// Check that all specified files exist in the tree
					for filename := range tt.options.InitialFiles {
						_, err := tree.FindEntry(filename)
						assert.NoError(t, err, "File %s should exist in initial commit", filename)
					}
				}
			}
		})
	}
}

func TestDefaultRepositoryManager_RepositoryExists(t *testing.T) {
	repoManager, tempDir, cleanup := setupRepositoryManagerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Test non-existent repository
	exists, err := repoManager.RepositoryExists(ctx, filepath.Join(tempDir, "nonexistent"))
	require.NoError(t, err)
	assert.False(t, exists)

	// Create a repository
	repoPath := filepath.Join(tempDir, "owner", "test-repo.git")
	options := &CreateRepositoryOptions{
		Path:          repoPath,
		Bare:          true,
		InitialCommit: true,
	}

	_, err = repoManager.CreateRepository(ctx, options)
	require.NoError(t, err)

	// Test existing repository
	exists, err = repoManager.RepositoryExists(ctx, repoPath)
	require.NoError(t, err)
	assert.True(t, exists)

	// Test directory that exists but is not a git repository
	nonGitDir := filepath.Join(tempDir, "not-git")
	err = os.MkdirAll(nonGitDir, 0755)
	require.NoError(t, err)

	exists, err = repoManager.RepositoryExists(ctx, nonGitDir)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestDefaultRepositoryManager_DeleteRepository(t *testing.T) {
	repoManager, tempDir, cleanup := setupRepositoryManagerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create a repository first
	repoPath := filepath.Join(tempDir, "owner", "delete-test.git")
	options := &CreateRepositoryOptions{
		Path:          repoPath,
		Bare:          true,
		InitialCommit: true,
	}

	_, err := repoManager.CreateRepository(ctx, options)
	require.NoError(t, err)

	// Verify it exists
	exists, err := repoManager.RepositoryExists(ctx, repoPath)
	require.NoError(t, err)
	assert.True(t, exists)

	// Delete the repository
	err = repoManager.DeleteRepository(ctx, repoPath)
	require.NoError(t, err)

	// Verify it no longer exists
	exists, err = repoManager.RepositoryExists(ctx, repoPath)
	require.NoError(t, err)
	assert.False(t, exists)

	_, err = os.Stat(repoPath)
	assert.True(t, os.IsNotExist(err), "Repository directory should be deleted")
}

func TestDefaultRepositoryManager_ListRepositories(t *testing.T) {
	repoManager, tempDir, cleanup := setupRepositoryManagerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Initially should be empty
	repos, err := repoManager.ListRepositories(ctx, tempDir)
	require.NoError(t, err)
	assert.Empty(t, repos)

	// Create several repositories
	testRepos := []struct {
		owner string
		name  string
		desc  string
	}{
		{"owner1", "repo1", "First repository"},
		{"owner1", "repo2", "Second repository"},
		{"owner2", "repo1", "Third repository"},
	}

	for _, tr := range testRepos {
		repoPath := filepath.Join(tempDir, tr.owner, tr.name+".git")
		options := &CreateRepositoryOptions{
			Path:          repoPath,
			Bare:          true,
			Description:   tr.desc,
			InitialCommit: true,
		}

		_, err := repoManager.CreateRepository(ctx, options)
		require.NoError(t, err)
	}

	// List repositories
	repos, err = repoManager.ListRepositories(ctx, tempDir)
	require.NoError(t, err)
	assert.Len(t, repos, len(testRepos))

	// Check that all repositories are found
	repoNames := make(map[string]bool)
	for _, repo := range repos {
		repoNames[repo.Name] = true
		assert.True(t, repo.IsBare)
		assert.NotZero(t, repo.CreatedAt)
		assert.Greater(t, repo.CommitCount, 0) // Should have at least one commit
	}

	for _, tr := range testRepos {
		assert.True(t, repoNames[tr.name], "Repository %s should be found", tr.name)
	}
}

func TestDefaultRepositoryManager_OpenRepository(t *testing.T) {
	repoManager, tempDir, cleanup := setupRepositoryManagerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create a repository first
	repoPath := filepath.Join(tempDir, "owner", "open-test.git")
	options := &CreateRepositoryOptions{
		Path:          repoPath,
		Bare:          true,
		InitialCommit: true,
	}

	_, err := repoManager.CreateRepository(ctx, options)
	require.NoError(t, err)

	// Open the repository
	repo, err := repoManager.OpenRepository(ctx, repoPath)
	require.NoError(t, err)
	require.NotNil(t, repo)

	// Check that it implements our interface
	assert.Equal(t, repoPath, repo.Path())

	// Close should not error
	err = repo.Close()
	assert.NoError(t, err)

	// Test opening non-existent repository
	_, err = repoManager.OpenRepository(ctx, filepath.Join(tempDir, "nonexistent"))
	assert.Error(t, err)
}

func TestCreateRepositoryOptions_Validation(t *testing.T) {
	repoManager, tempDir, cleanup := setupRepositoryManagerTest(t)
	defer cleanup()

	ctx := context.Background()

	tests := []struct {
		name    string
		options *CreateRepositoryOptions
		wantErr bool
	}{
		{
			name:    "nil options",
			options: nil,
			wantErr: true,
		},
		{
			name: "empty path",
			options: &CreateRepositoryOptions{
				Path: "",
			},
			wantErr: true,
		},
		{
			name: "path outside repo root",
			options: &CreateRepositoryOptions{
				Path: "/tmp/outside-repo",
			},
			wantErr: true,
		},
		{
			name: "valid path",
			options: &CreateRepositoryOptions{
				Path: filepath.Join(tempDir, "owner", "valid.git"),
				Bare: true,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := repoManager.CreateRepository(ctx, tt.options)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDefaultRepositoryManager_CreateRepository_Duplicate(t *testing.T) {
	repoManager, tempDir, cleanup := setupRepositoryManagerTest(t)
	defer cleanup()

	ctx := context.Background()

	repoPath := filepath.Join(tempDir, "owner", "duplicate-test.git")
	options := &CreateRepositoryOptions{
		Path:          repoPath,
		Bare:          true,
		InitialCommit: true,
	}

	// Create repository first time
	_, err := repoManager.CreateRepository(ctx, options)
	require.NoError(t, err)

	// Try to create the same repository again
	_, err = repoManager.CreateRepository(ctx, options)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}
