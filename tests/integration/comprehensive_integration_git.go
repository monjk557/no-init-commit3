package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/git"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// testGitRepositoryFormatIntegration tests integration with different Git repository formats
func (suite *ComprehensiveIntegrationTestSuite) testGitRepositoryFormatIntegration(t *testing.T) {
	ctx := context.Background()

	// Test each repository type
	for repoName, testRepo := range suite.testRepositories {
		t.Run(fmt.Sprintf("Repository_%s", repoName), func(t *testing.T) {
			suite.testSpecificRepositoryFormat(t, ctx, testRepo)
		})
	}

	// Test Git version compatibility
	t.Run("Git_Version_Compatibility", func(t *testing.T) {
		suite.testGitVersionCompatibility(t)
	})

	// Test packfile operations
	t.Run("Packfile_Operations", func(t *testing.T) {
		suite.testPackfileOperations(t)
	})

	// Test large repository handling
	t.Run("Large_Repository_Handling", func(t *testing.T) {
		suite.testLargeRepositoryHandling(t, ctx)
	})
}

// testSpecificRepositoryFormat tests operations on a specific repository format
func (suite *ComprehensiveIntegrationTestSuite) testSpecificRepositoryFormat(t *testing.T,
	ctx context.Context, testRepo *TestRepository) {

	// Test basic repository operations
	t.Run("Basic_Operations", func(t *testing.T) {
		suite.testBasicRepositoryOperations(t, testRepo)
	})

	// Test branch operations
	t.Run("Branch_Operations", func(t *testing.T) {
		suite.testBranchOperations(t, testRepo)
	})

	// Test commit operations
	t.Run("Commit_Operations", func(t *testing.T) {
		suite.testCommitOperations(t, testRepo)
	})

	// Test submodule operations (if applicable)
	if testRepo.HasSubmodules {
		t.Run("Submodule_Operations", func(t *testing.T) {
			suite.testSubmoduleOperations(t, testRepo)
		})
	}

	// Test bare repository operations (if applicable)
	if testRepo.RepoType == RepoTypeBare {
		t.Run("Bare_Repository_Operations", func(t *testing.T) {
			suite.testBareRepositoryOperations(t, testRepo)
		})
	}
}

// testBasicRepositoryOperations tests basic Git operations
func (suite *ComprehensiveIntegrationTestSuite) testBasicRepositoryOperations(t *testing.T,
	testRepo *TestRepository) {

	// Test repository opening
	repo, err := gogit.PlainOpen(testRepo.Path)
	require.NoError(t, err, "Failed to open repository: %s", testRepo.Name)

	// Test HEAD reference
	head, err := repo.Head()
	if testRepo.RepoType != RepoTypeBare || err == nil {
		// For normal repos, HEAD should exist; for bare repos, it might not if no commits
		if err == nil {
			assert.NotNil(t, head, "HEAD reference should not be nil")
			assert.True(t, head.Hash().IsZero() == false, "HEAD hash should not be zero")
		}
	}

	// Test references listing
	refs, err := repo.References()
	require.NoError(t, err, "Failed to get repository references")

	refCount := 0
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		refCount++
		assert.NotEmpty(t, ref.Name().String(), "Reference name should not be empty")
		return nil
	})
	require.NoError(t, err, "Failed to iterate through references")

	if testRepo.RepoType != RepoTypeBare {
		assert.Greater(t, refCount, 0, "Repository should have at least one reference")
	}

	// Test configuration
	cfg, err := repo.Config()
	require.NoError(t, err, "Failed to get repository configuration")
	assert.NotNil(t, cfg, "Repository configuration should not be nil")

	// Test repository statistics
	suite.gatherRepositoryStatistics(t, testRepo, repo)

	suite.logger.Info("Basic repository operations completed",
		zap.String("repo_name", testRepo.Name),
		zap.String("repo_type", string(testRepo.RepoType)),
		zap.Int("references", refCount))
}

// testBranchOperations tests branch-related operations
func (suite *ComprehensiveIntegrationTestSuite) testBranchOperations(t *testing.T,
	testRepo *TestRepository) {

	if testRepo.RepoType == RepoTypeBare {
		t.Skip("Skipping branch operations for bare repository")
		return
	}

	repo, err := gogit.PlainOpen(testRepo.Path)
	require.NoError(t, err, "Failed to open repository for branch operations")

	// List all branches
	branches, err := repo.Branches()
	require.NoError(t, err, "Failed to get branches")

	branchCount := 0
	branchNames := make([]string, 0)

	err = branches.ForEach(func(ref *plumbing.Reference) error {
		branchCount++
		branchName := ref.Name().Short()
		branchNames = append(branchNames, branchName)

		// Verify branch reference is valid
		assert.True(t, ref.Name().IsBranch(), "Reference should be a branch: %s", ref.Name())
		assert.False(t, ref.Hash().IsZero(), "Branch hash should not be zero: %s", branchName)

		return nil
	})
	require.NoError(t, err, "Failed to iterate through branches")

	assert.Equal(t, testRepo.BranchCount, branchCount,
		"Branch count should match expected: expected %d, got %d", testRepo.BranchCount, branchCount)

	// Test branch checkout (if worktree available)
	if worktree, err := repo.Worktree(); err == nil && branchCount > 1 {
		// Try to checkout a different branch
		for _, branchName := range branchNames {
			if branchName != "main" {
				checkoutOptions := &gogit.CheckoutOptions{
					Branch: plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", branchName)),
				}

				err := worktree.Checkout(checkoutOptions)
				if err == nil {
					// Verify checkout was successful
					head, err := repo.Head()
					require.NoError(t, err, "Failed to get HEAD after checkout")

					expectedRef := fmt.Sprintf("refs/heads/%s", branchName)
					assert.Equal(t, expectedRef, head.Name().String(),
						"HEAD should point to checked out branch")

					break // Successfully tested one checkout
				}
			}
		}
	}

	suite.logger.Info("Branch operations completed",
		zap.String("repo_name", testRepo.Name),
		zap.Int("branches", branchCount),
		zap.Strings("branch_names", branchNames))
}

// testCommitOperations tests commit-related operations
func (suite *ComprehensiveIntegrationTestSuite) testCommitOperations(t *testing.T,
	testRepo *TestRepository) {

	repo, err := gogit.PlainOpen(testRepo.Path)
	require.NoError(t, err, "Failed to open repository for commit operations")

	// Get HEAD commit (if exists)
	head, err := repo.Head()
	if err != nil {
		// No commits yet, create one for testing
		if testRepo.RepoType != RepoTypeBare {
			suite.createTestCommit(t, repo, testRepo.Path)
			head, err = repo.Head()
		}
	}

	if err != nil && testRepo.RepoType == RepoTypeBare {
		t.Skip("Skipping commit operations for empty bare repository")
		return
	}

	require.NoError(t, err, "Failed to get HEAD reference")

	// Get commit object
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err, "Failed to get commit object")

	// Verify commit properties
	assert.NotEmpty(t, commit.Hash.String(), "Commit hash should not be empty")
	assert.NotEmpty(t, commit.Message, "Commit message should not be empty")
	assert.NotEmpty(t, commit.Author.Name, "Commit author should not be empty")
	assert.False(t, commit.Author.When.IsZero(), "Commit timestamp should not be zero")

	// Test commit iteration
	commitIter, err := repo.Log(&gogit.LogOptions{
		From: head.Hash(),
	})
	require.NoError(t, err, "Failed to get commit log")

	commitCount := 0
	err = commitIter.ForEach(func(c *object.Commit) error {
		commitCount++
		assert.NotNil(t, c, "Commit should not be nil")
		assert.NotEmpty(t, c.Hash.String(), "Commit hash should not be empty")

		// Limit iteration to avoid excessive processing
		if commitCount >= 10 {
			return fmt.Errorf("stop iteration") // Use error to stop iteration
		}
		return nil
	})

	// Error is expected when we stop iteration
	if commitCount >= 10 {
		assert.Error(t, err, "Expected error when stopping iteration")
	} else {
		assert.NoError(t, err, "Unexpected error during commit iteration")
	}

	assert.Greater(t, commitCount, 0, "Should have at least one commit")

	// Test commit tree access
	tree, err := commit.Tree()
	require.NoError(t, err, "Failed to get commit tree")
	assert.NotNil(t, tree, "Commit tree should not be nil")

	// Test tree file enumeration
	files := tree.Files()
	fileCount := 0
	err = files.ForEach(func(f *object.File) error {
		fileCount++
		assert.NotEmpty(t, f.Name, "File name should not be empty")
		assert.NotNil(t, f.Hash, "File hash should not be nil")
		return nil
	})
	require.NoError(t, err, "Failed to iterate through tree files")

	suite.logger.Info("Commit operations completed",
		zap.String("repo_name", testRepo.Name),
		zap.Int("commits_checked", commitCount),
		zap.Int("tree_files", fileCount),
		zap.String("head_commit", commit.Hash.String()))
}

// createTestCommit creates a test commit for repositories that need one
func (suite *ComprehensiveIntegrationTestSuite) createTestCommit(t *testing.T,
	repo *gogit.Repository, repoPath string) {

	worktree, err := repo.Worktree()
	require.NoError(t, err, "Failed to get worktree for test commit")

	// Create a test file
	testFile := filepath.Join(repoPath, "test_commit_file.txt")
	testContent := fmt.Sprintf("Test commit created at %s", time.Now().Format(time.RFC3339))

	err = os.WriteFile(testFile, []byte(testContent), 0644)
	require.NoError(t, err, "Failed to create test file")

	// Add file to staging
	_, err = worktree.Add("test_commit_file.txt")
	require.NoError(t, err, "Failed to stage test file")

	// Create commit
	_, err = worktree.Commit("Test commit for integration testing", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Integration Test",
			Email: "test@integration.test",
			When:  time.Now(),
		},
	})
	require.NoError(t, err, "Failed to create test commit")
}

// testSubmoduleOperations tests submodule-related operations
func (suite *ComprehensiveIntegrationTestSuite) testSubmoduleOperations(t *testing.T,
	testRepo *TestRepository) {

	repo, err := gogit.PlainOpen(testRepo.Path)
	require.NoError(t, err, "Failed to open repository for submodule operations")

	// Check for .gitmodules file
	gitmodulesPath := filepath.Join(testRepo.Path, ".gitmodules")
	_, err = os.Stat(gitmodulesPath)
	assert.NoError(t, err, "Should have .gitmodules file for submodule repository")

	// Read .gitmodules content
	if err == nil {
		content, err := os.ReadFile(gitmodulesPath)
		require.NoError(t, err, "Failed to read .gitmodules file")

		gitmodulesContent := string(content)
		assert.Contains(t, gitmodulesContent, "[submodule", ".gitmodules should contain submodule definition")
		assert.Contains(t, gitmodulesContent, "path =", ".gitmodules should contain submodule path")
		assert.Contains(t, gitmodulesContent, "url =", ".gitmodules should contain submodule URL")
	}

	// Test submodule directory structure
	submoduleDir := filepath.Join(testRepo.Path, "submodule")
	_, err = os.Stat(submoduleDir)
	assert.NoError(t, err, "Submodule directory should exist")

	// Note: Full submodule functionality requires actual git commands
	// This test focuses on structure validation
	suite.logger.Info("Submodule operations completed",
		zap.String("repo_name", testRepo.Name),
		zap.String("gitmodules_path", gitmodulesPath),
		zap.String("submodule_dir", submoduleDir))
}

// testBareRepositoryOperations tests bare repository specific operations
func (suite *ComprehensiveIntegrationTestSuite) testBareRepositoryOperations(t *testing.T,
	testRepo *TestRepository) {

	// Test bare repository characteristics
	assert.True(t, suite.isBareRepository(testRepo.Path),
		"Repository should be identified as bare")

	// Test opening bare repository
	repo, err := gogit.PlainOpen(testRepo.Path)
	require.NoError(t, err, "Failed to open bare repository")

	// Bare repositories should not have worktree
	_, err = repo.Worktree()
	assert.Error(t, err, "Bare repository should not have worktree")

	// Test storage access
	storer := repo.Storer
	assert.NotNil(t, storer, "Bare repository should have storer")

	// Test configuration
	cfg, err := repo.Config()
	require.NoError(t, err, "Failed to get bare repository configuration")

	if cfg.Core.IsBare != nil {
		assert.True(t, *cfg.Core.IsBare, "Repository configuration should indicate bare repository")
	}

	suite.logger.Info("Bare repository operations completed",
		zap.String("repo_name", testRepo.Name),
		zap.String("repo_path", testRepo.Path))
}

// testGitVersionCompatibility tests compatibility with different Git versions
func (suite *ComprehensiveIntegrationTestSuite) testGitVersionCompatibility(t *testing.T) {
	// Check Git version
	gitVersion, err := suite.getGitVersion()
	if err != nil {
		t.Skip("Git not available, skipping Git version compatibility test")
		return
	}

	suite.logger.Info("Testing Git version compatibility",
		zap.String("git_version", gitVersion))

	// Test basic git operations with system Git
	for repoName, testRepo := range suite.testRepositories {
		if testRepo.RepoType == RepoTypeBare {
			continue // Skip bare repositories for git command tests
		}

		t.Run(fmt.Sprintf("GitVersion_%s", repoName), func(t *testing.T) {
			// Test git status
			cmd := exec.Command("git", "status", "--porcelain")
			cmd.Dir = testRepo.Path

			output, err := cmd.Output()
			assert.NoError(t, err, "Git status should work on repository: %s", repoName)

			// Status output should be valid (empty or with modifications)
			statusOutput := string(output)
			suite.logger.Debug("Git status output",
				zap.String("repo", repoName),
				zap.String("status", statusOutput))

			// Test git log
			cmd = exec.Command("git", "log", "--oneline", "-n", "5")
			cmd.Dir = testRepo.Path

			output, err = cmd.Output()
			if err == nil {
				logOutput := string(output)
				assert.NotEmpty(t, strings.TrimSpace(logOutput),
					"Git log should have output for repository with commits: %s", repoName)
			}
		})
	}
}

// testPackfileOperations tests packfile-related operations
func (suite *ComprehensiveIntegrationTestSuite) testPackfileOperations(t *testing.T) {
	// Find a repository with commits for packfile testing
	var testRepo *TestRepository
	for _, repo := range suite.testRepositories {
		if repo.RepoType != RepoTypeBare && repo.CommitCount > 0 {
			testRepo = repo
			break
		}
	}

	if testRepo == nil {
		t.Skip("No suitable repository found for packfile testing")
		return
	}

	repo, err := gogit.PlainOpen(testRepo.Path)
	require.NoError(t, err, "Failed to open repository for packfile testing")

	// Test packfile access through storage
	storer := repo.Storer
	assert.NotNil(t, storer, "Repository storer should not be nil")

	// Test object storage
	head, err := repo.Head()
	if err != nil {
		t.Skip("No HEAD reference available for packfile testing")
		return
	}

	// Try to get object from storage
	obj, err := storer.EncodedObject(plumbing.CommitObject, head.Hash())
	require.NoError(t, err, "Failed to get encoded object from storage")
	assert.NotNil(t, obj, "Encoded object should not be nil")

	// Test object properties
	assert.Equal(t, plumbing.CommitObject, obj.Type(), "Object should be commit type")
	assert.Greater(t, obj.Size(), int64(0), "Object should have non-zero size")

	suite.logger.Info("Packfile operations completed",
		zap.String("repo_name", testRepo.Name),
		zap.String("commit_hash", head.Hash().String()),
		zap.Int64("object_size", obj.Size()))
}

// testLargeRepositoryHandling tests handling of repositories with many files/commits
func (suite *ComprehensiveIntegrationTestSuite) testLargeRepositoryHandling(t *testing.T,
	ctx context.Context) {

	// Create a repository with many files for stress testing
	largeRepoPath := filepath.Join(suite.testDataDir, "large_repo_test")
	err := os.MkdirAll(largeRepoPath, 0755)
	require.NoError(t, err, "Failed to create large repository directory")

	repo, err := gogit.PlainInit(largeRepoPath, false)
	require.NoError(t, err, "Failed to initialize large repository")

	worktree, err := repo.Worktree()
	require.NoError(t, err, "Failed to get worktree for large repository")

	// Create many files (but not too many for test performance)
	fileCount := 100
	for i := 0; i < fileCount; i++ {
		fileName := fmt.Sprintf("large_repo_file_%04d.txt", i)
		filePath := filepath.Join(largeRepoPath, fileName)
		content := fmt.Sprintf("Content for large repository file %d\n%s",
			i, strings.Repeat("Large file content padding ", 10))

		err = os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err, "Failed to create large repo file: %s", fileName)
	}

	// Add and commit files
	_, err = worktree.Add(".")
	require.NoError(t, err, "Failed to stage large repo files")

	commitStart := time.Now()
	_, err = worktree.Commit("Large repository test commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Large Repo Test",
			Email: "test@largerepo.test",
			When:  time.Now(),
		},
	})
	require.NoError(t, err, "Failed to commit large repo files")
	commitDuration := time.Since(commitStart)

	// Test operations on the large repository
	operationStart := time.Now()

	// Test tree traversal
	head, err := repo.Head()
	require.NoError(t, err, "Failed to get HEAD of large repository")

	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err, "Failed to get commit object of large repository")

	tree, err := commit.Tree()
	require.NoError(t, err, "Failed to get tree of large repository")

	// Count files in tree
	files := tree.Files()
	actualFileCount := 0
	err = files.ForEach(func(f *object.File) error {
		actualFileCount++
		return nil
	})
	require.NoError(t, err, "Failed to iterate through large repository files")

	operationDuration := time.Since(operationStart)

	assert.Equal(t, fileCount, actualFileCount,
		"Large repository should contain all created files")

	// Performance assertions (reasonable limits for test environment)
	assert.Less(t, commitDuration, 30*time.Second,
		"Large repository commit should complete within reasonable time")
	assert.Less(t, operationDuration, 10*time.Second,
		"Large repository operations should complete within reasonable time")

	suite.logger.Info("Large repository handling completed",
		zap.Int("file_count", fileCount),
		zap.Int("actual_files", actualFileCount),
		zap.Duration("commit_duration", commitDuration),
		zap.Duration("operation_duration", operationDuration))

	// Cleanup
	os.RemoveAll(largeRepoPath)
}

// Helper methods

// isBareRepository checks if a repository is bare
func (suite *ComprehensiveIntegrationTestSuite) isBareRepository(repoPath string) bool {
	// Check for .git directory (present in normal repos, absent in bare repos)
	gitDir := filepath.Join(repoPath, ".git")
	_, err := os.Stat(gitDir)
	return os.IsNotExist(err)
}

// getGitVersion gets the system Git version
func (suite *ComprehensiveIntegrationTestSuite) getGitVersion() (string, error) {
	cmd := exec.Command("git", "version")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// gatherRepositoryStatistics collects statistics about a repository
func (suite *ComprehensiveIntegrationTestSuite) gatherRepositoryStatistics(t *testing.T,
	testRepo *TestRepository, repo *gogit.Repository) {

	// Count references
	refs, err := repo.References()
	if err != nil {
		return
	}

	refCount := 0
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		refCount++
		return nil
	})

	if err == nil {
		suite.logger.Debug("Repository statistics",
			zap.String("repo_name", testRepo.Name),
			zap.String("repo_type", string(testRepo.RepoType)),
			zap.Int("references", refCount),
			zap.Int("expected_files", testRepo.FileCount),
			zap.Int("expected_branches", testRepo.BranchCount))
	}
}
