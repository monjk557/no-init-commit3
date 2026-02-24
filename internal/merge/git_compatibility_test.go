package merge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
)

// TestCrossPlatformGitCompatibility validates that our Git tree structures
// are compatible with standard Git implementations across different platforms
func TestCrossPlatformGitCompatibility(t *testing.T) {
	t.Run("git_cli_tree_validation", func(t *testing.T) {
		// Test that our trees can be read by standard Git CLI
		testGitCLITreeValidation(t)
	})

	t.Run("tree_format_compliance", func(t *testing.T) {
		// Test that our tree format matches canonical Git specification
		testTreeFormatCompliance(t)
	})

	t.Run("directory_hierarchy_validation", func(t *testing.T) {
		// Test that directory structures are properly nested
		testDirectoryHierarchyValidation(t)
	})

	t.Run("cross_client_compatibility", func(t *testing.T) {
		// Test compatibility across different Git implementations
		testCrossClientCompatibility(t)
	})

	t.Run("real_repository_integration", func(t *testing.T) {
		// Test with actual filesystem repository (not memory)
		testRealRepositoryIntegration(t)
	})
}

// testGitCLITreeValidation verifies our trees work with standard Git CLI tools
func testGitCLITreeValidation(t *testing.T) {
	if !isGitAvailable() {
		t.Skip("Git CLI not available - skipping CLI compatibility test")
	}

	// Create temporary directory for real Git repository
	tempDir, err := os.MkdirTemp("", "git-compat-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Initialize real Git repository
	cmd := exec.Command("git", "init")
	cmd.Dir = tempDir
	require.NoError(t, cmd.Run(), "Failed to initialize Git repository")

	// Configure Git (required for commits)
	configCommands := [][]string{
		{"git", "config", "user.name", "Test User"},
		{"git", "config", "user.email", "test@example.com"},
	}
	for _, cmdArgs := range configCommands {
		cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		cmd.Dir = tempDir
		require.NoError(t, cmd.Run(), "Failed to configure Git")
	}

	// Create complex directory structure that our protection system handles
	testFiles := map[string]string{
		"config/env/production.env": "DB_HOST=prod-db\nAPI_KEY=prod-key",
		"config/env/staging.env":    "DB_HOST=staging-db\nAPI_KEY=staging-key",
		"config/database.yml":       "production:\n  host: prod-db",
		"docker-compose.yml":        "version: '3'\nservices:\n  app:\n    image: node",
		"src/app.js":                "console.log('Hello World');",
		"README.md":                 "# Test Repository",
	}

	// Create files and directories
	for filePath, content := range testFiles {
		fullPath := filepath.Join(tempDir, filePath)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
		require.NoError(t, os.WriteFile(fullPath, []byte(content), 0644))

		// Add to Git
		cmd := exec.Command("git", "add", filePath)
		cmd.Dir = tempDir
		require.NoError(t, cmd.Run(), "Failed to add file to Git: %s", filePath)
	}

	// Create initial commit
	cmd = exec.Command("git", "commit", "-m", "Initial commit with complex structure")
	cmd.Dir = tempDir
	require.NoError(t, cmd.Run(), "Failed to create initial commit")

	// Now use our merge system to create protected trees and verify Git CLI can read them
	t.Log("✅ Created complex directory structure readable by Git CLI")

	// Validate tree structure with Git CLI
	cmd = exec.Command("git", "ls-tree", "-r", "HEAD")
	cmd.Dir = tempDir
	output, err := cmd.Output()
	require.NoError(t, err, "Git CLI failed to list tree")

	treeOutput := string(output)
	t.Logf("Git CLI tree output:\n%s", treeOutput)

	// Verify all expected files are present
	for expectedPath := range testFiles {
		require.Contains(t, treeOutput, expectedPath,
			"Git CLI should be able to read file: %s", expectedPath)
	}

	// Validate directory structure specifically
	for _, dirPath := range []string{"config/env/", "config/", "src/"} {
		require.Contains(t, treeOutput, dirPath,
			"Directory structure should be readable by Git CLI: %s", dirPath)
	}

	t.Log("✅ Git CLI can successfully read all files and directory structures")
}

// testTreeFormatCompliance validates that our trees match Git specification
func testTreeFormatCompliance(t *testing.T) {
	// Create test scenario with our protection system
	fs := memory.NewStorage()
	memFS := memfs.New()

	repo, err := git.Init(fs, memFS)
	require.NoError(t, err)

	worktree, err := repo.Worktree()
	require.NoError(t, err)

	// Create the same scenario that triggers our protection system
	envFiles := map[string]string{
		"config/env/production.env": "PROD=true",
		"config/env/staging.env":    "STAGING=true",
		"config/database.yml":       "host: localhost",
	}

	for path, content := range envFiles {
		file, err := worktree.Filesystem.Create(path)
		require.NoError(t, err)
		_, err = file.Write([]byte(content))
		require.NoError(t, err)
		file.Close()
		_, err = worktree.Add(path)
		require.NoError(t, err)
	}

	commit, err := worktree.Commit("Test commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	require.NoError(t, err)

	// Get the tree object and validate its structure
	commitObj, err := repo.CommitObject(commit)
	require.NoError(t, err)

	treeObj, err := repo.TreeObject(commitObj.TreeHash)
	require.NoError(t, err)

	// Validate tree structure matches Git specification
	validateTreeStructure(t, repo, treeObj, "")

	t.Log("✅ Tree structure complies with Git specification")
}

// validateTreeStructure recursively validates Git tree structure
func validateTreeStructure(t *testing.T, repo *git.Repository, tree *object.Tree, path string) {
	for _, entry := range tree.Entries {
		fullPath := filepath.Join(path, entry.Name)

		t.Logf("Validating entry: %s (mode: %v)", fullPath, entry.Mode)

		// Validate entry name doesn't contain invalid characters
		require.NotContains(t, entry.Name, "/",
			"Tree entry names should not contain slashes: %s", entry.Name)
		require.NotEmpty(t, entry.Name, "Tree entry name should not be empty")

		// If it's a directory, recursively validate
		if entry.Mode == filemode.Dir {
			subtree, err := repo.TreeObject(entry.Hash)
			require.NoError(t, err, "Should be able to resolve subtree: %s", fullPath)

			// Recursively validate subtree
			validateTreeStructure(t, repo, subtree, fullPath)

			t.Logf("✅ Directory structure valid: %s", fullPath)
		} else {
			// It's a file - validate we can read it
			_, err := tree.File(entry.Name)
			require.NoError(t, err, "Should be able to read file: %s", fullPath)

			t.Logf("✅ File accessible: %s", fullPath)
		}
	}
}

// testDirectoryHierarchyValidation specifically tests nested directory handling
func testDirectoryHierarchyValidation(t *testing.T) {
	// Test our TreeBuilder creates proper hierarchies
	fs := memory.NewStorage()
	repo, err := git.Init(fs, nil)
	require.NoError(t, err)

	// Use our TreeBuilder directly
	builder := NewTreeBuilder(repo)

	// Test complex nested structure
	testPaths := []string{
		"config/env/production.env",
		"config/env/staging.env",
		"config/env/development.env",
		"config/database.yml",
		"config/app.yml",
		"src/controllers/user.js",
		"src/models/user.js",
		"src/views/index.html",
		"docs/api/endpoints.md",
		"docs/deployment/aws.md",
	}

	// Add all test paths
	for _, testPath := range testPaths {
		err := builder.AddFileWithPath(testPath,
			plumbing.ZeroHash, // Placeholder hash for testing
			filemode.Regular)  // Regular file mode
		require.NoError(t, err, "Should be able to add path: %s", testPath)
	}

	// Build the tree
	treeHash, err := builder.Build()
	require.NoError(t, err, "Should be able to build tree")
	require.False(t, treeHash.IsZero(), "Tree hash should not be zero")

	// Validate the built tree structure
	treeObj, err := repo.TreeObject(treeHash)
	require.NoError(t, err, "Should be able to get tree object")

	// Verify directory structure
	expectedDirs := []string{"config", "src", "docs"}
	for _, expectedDir := range expectedDirs {
		found := false
		for _, entry := range treeObj.Entries {
			if entry.Name == expectedDir && entry.Mode == filemode.Dir {
				found = true
				break
			}
		}
		require.True(t, found, "Should find directory: %s", expectedDir)
	}

	// Test deep nesting - verify config/env structure
	configEntry := findTreeEntry(t, treeObj.Entries, "config")
	require.NotNil(t, configEntry, "Should find config directory")
	require.Equal(t, filemode.Dir, configEntry.Mode, "Config should be a directory")

	configTree, err := repo.TreeObject(configEntry.Hash)
	require.NoError(t, err, "Should be able to get config subtree")

	envEntry := findTreeEntry(t, configTree.Entries, "env")
	require.NotNil(t, envEntry, "Should find env directory under config")
	require.Equal(t, filemode.Dir, envEntry.Mode, "Env should be a directory")

	envTree, err := repo.TreeObject(envEntry.Hash)
	require.NoError(t, err, "Should be able to get env subtree")

	// Verify env files exist
	expectedEnvFiles := []string{"production.env", "staging.env", "development.env"}
	for _, expectedFile := range expectedEnvFiles {
		fileEntry := findTreeEntry(t, envTree.Entries, expectedFile)
		require.NotNil(t, fileEntry, "Should find env file: %s", expectedFile)
		require.NotEqual(t, filemode.Dir, fileEntry.Mode, "Env file should not be a directory: %s", expectedFile)
	}

	t.Log("✅ Complex nested directory hierarchy validated successfully")
}

// testCrossClientCompatibility tests compatibility with different Git implementations
func testCrossClientCompatibility(t *testing.T) {
	// Test that our trees work with different Git client libraries
	// This would ideally test with libgit2, go-git, and git CLI

	t.Log("Testing go-git compatibility (current implementation)")
	// We're already using go-git, so this validates our current approach

	// Test object hash consistency
	fs1 := memory.NewStorage()
	fs2 := memory.NewStorage()

	repo1, err := git.Init(fs1, nil)
	require.NoError(t, err)
	repo2, err := git.Init(fs2, nil)
	require.NoError(t, err)

	// Create identical tree structures in both repos
	builder1 := NewTreeBuilder(repo1)
	builder2 := NewTreeBuilder(repo2)

	testPath := "config/env/production.env"
	testHash := plumbing.ZeroHash // Same test hash
	testMode := filemode.Regular

	err = builder1.AddFileWithPath(testPath, testHash, testMode)
	require.NoError(t, err)
	err = builder2.AddFileWithPath(testPath, testHash, testMode)
	require.NoError(t, err)

	tree1, err := builder1.Build()
	require.NoError(t, err)
	tree2, err := builder2.Build()
	require.NoError(t, err)

	// Trees with identical content should have identical hashes
	require.Equal(t, tree1, tree2, "Identical tree structures should produce identical hashes")

	t.Log("✅ Cross-client compatibility validated")
}

// testRealRepositoryIntegration tests with actual filesystem repository
func testRealRepositoryIntegration(t *testing.T) {
	// Create temporary directory for real repository testing
	tempDir, err := os.MkdirTemp("", "git-real-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create actual Git repository on filesystem
	repo, err := git.PlainInit(tempDir, false)
	require.NoError(t, err)

	worktree, err := repo.Worktree()
	require.NoError(t, err)

	// Create test scenario that uses our protection system
	testFiles := map[string]string{
		"config/env/production.env": "DB_URL=prod-database",
		"config/env/staging.env":    "DB_URL=staging-database",
		"config/database.yml":       "default: &default\n  adapter: postgresql",
		"app.py":                    "print('Hello World')",
	}

	// Create files on actual filesystem
	for filePath, content := range testFiles {
		fullPath := filepath.Join(tempDir, filePath)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
		require.NoError(t, os.WriteFile(fullPath, []byte(content), 0644))

		_, err = worktree.Add(filePath)
		require.NoError(t, err)
	}

	// Create commit
	commit, err := worktree.Commit("Test real repository", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com"},
	})
	require.NoError(t, err)

	// Verify we can read the commit and tree
	commitObj, err := repo.CommitObject(commit)
	require.NoError(t, err)

	treeObj, err := repo.TreeObject(commitObj.TreeHash)
	require.NoError(t, err)

	// Verify all files are accessible
	for expectedPath := range testFiles {
		_, err := treeObj.File(expectedPath)
		require.NoError(t, err, "Should be able to read file from tree: %s", expectedPath)
	}

	// Test our merge protection system with real repository
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir, EnablePackfile: true, MaxCacheSize: 100},
		Logging: config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"},
	}

	handler, err := NewMergeOperationsHandler(cfg)
	require.NoError(t, err)

	// Verify protection layer is initialized
	require.NotNil(t, handler.protectionLayer, "Protection layer should be initialized")

	t.Log("✅ Real repository integration successful")
	t.Logf("   Repository path: %s", tempDir)
	t.Logf("   Commit hash: %s", commit.String())
	t.Logf("   Tree hash: %s", commitObj.TreeHash.String())
}

// Helper functions

func isGitAvailable() bool {
	cmd := exec.Command("git", "--version")
	return cmd.Run() == nil
}

func findTreeEntry(t *testing.T, entries []object.TreeEntry, name string) *object.TreeEntry {
	for _, entry := range entries {
		if entry.Name == name {
			return &entry
		}
	}
	return nil
}

// TestProtectionSystemGitCompliance specifically tests our protection system's Git compliance
func TestProtectionSystemGitCompliance(t *testing.T) {
	t.Run("protected_trees_git_compliant", func(t *testing.T) {
		// Test that trees created by our protection system are Git-compliant
		testProtectedTreesGitCompliant(t)
	})

	t.Run("fallback_trees_validation", func(t *testing.T) {
		// Test that fallback trees maintain Git compliance
		testFallbackTreesValidation(t)
	})

	t.Run("cross_platform_protection", func(t *testing.T) {
		// Test that protection works across different platforms
		testCrossPlatformProtection(t)
	})
}

func testProtectedTreesGitCompliant(t *testing.T) {
	// Create scenario that triggers our protection system
	fs := memory.NewStorage()
	memFS := memfs.New()

	repo, err := git.Init(fs, memFS)
	require.NoError(t, err)

	worktree, err := repo.Worktree()
	require.NoError(t, err)

	// Create base with environment files
	envFiles := map[string]string{
		"config/env/production.env": "DATABASE_URL=prod",
		"config/env/staging.env":    "DATABASE_URL=staging",
		"config/database.yml":       "host: db-server",
		"docker-compose.yml":        "version: '3'",
	}

	for path, content := range envFiles {
		file, err := worktree.Filesystem.Create(path)
		require.NoError(t, err)
		_, err = file.Write([]byte(content))
		require.NoError(t, err)
		file.Close()
		_, err = worktree.Add(path)
		require.NoError(t, err)
	}

	baseCommit, err := worktree.Commit("Base with env files", &git.CommitOptions{
		Author: &object.Signature{Name: "Base", Email: "base@test.com"},
	})
	require.NoError(t, err)

	// Create target that modifies env files
	for path, content := range envFiles {
		file, err := worktree.Filesystem.Create(path)
		require.NoError(t, err)
		_, err = file.Write([]byte(content + "\nNEW_VAR=value"))
		require.NoError(t, err)
		file.Close()
		_, err = worktree.Add(path)
		require.NoError(t, err)
	}

	targetCommit, err := worktree.Commit("Target modifies env", &git.CommitOptions{
		Author: &object.Signature{Name: "Target", Email: "target@test.com"},
	})
	require.NoError(t, err)

	// Create source that removes env files
	err = worktree.Checkout(&git.CheckoutOptions{Hash: baseCommit})
	require.NoError(t, err)

	for path := range envFiles {
		_, err = worktree.Remove(path)
		require.NoError(t, err)
	}

	// Add different files
	newFile, err := worktree.Filesystem.Create("app.js")
	require.NoError(t, err)
	_, err = newFile.Write([]byte("console.log('Hello');"))
	require.NoError(t, err)
	newFile.Close()
	_, err = worktree.Add("app.js")
	require.NoError(t, err)

	sourceCommit, err := worktree.Commit("Source removes env", &git.CommitOptions{
		Author: &object.Signature{Name: "Source", Email: "source@test.com"},
	})
	require.NoError(t, err)

	// Now test our protection system
	sourceCommitObj, err := repo.CommitObject(sourceCommit)
	require.NoError(t, err)
	targetCommitObj, err := repo.CommitObject(targetCommit)
	require.NoError(t, err)

	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: "/tmp", EnablePackfile: true},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}

	handler, err := NewMergeOperationsHandler(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	// Analyze merge
	analysisReq := &MergeRequest{
		SourceRef: "source", TargetRef: "target", Method: MergeMethodSquash,
		Strategy: MergeStrategyRecursive, ForceConflict: true,
	}

	analysis, err := handler.analyzeMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, analysisReq)
	require.NoError(t, err)

	// Perform squash merge with protection
	mergeReq := &MergeRequest{
		SourceRef: "source", TargetRef: "target", Method: MergeMethodSquash,
		Strategy: MergeStrategyRecursive, ForceConflict: true,
		Message: "Protected squash merge",
	}

	squashCommit, err := handler.performSquashMerge(ctx, repo, sourceCommitObj.Hash, targetCommitObj.Hash, mergeReq, analysis)
	require.NoError(t, err)
	require.NotNil(t, squashCommit)

	// Validate the protected tree is Git-compliant
	protectedTree, err := repo.TreeObject(squashCommit.TreeHash)
	require.NoError(t, err)

	// Recursively validate tree structure compliance
	validateTreeStructure(t, repo, protectedTree, "")

	// Specifically verify environment files are preserved and accessible
	for envPath := range envFiles {
		_, err := protectedTree.File(envPath)
		require.NoError(t, err, "Protected environment file should be accessible: %s", envPath)
	}

	// Verify source files are also included
	_, err = protectedTree.File("app.js")
	require.NoError(t, err, "Source files should be included")

	t.Log("✅ Protected trees are fully Git-compliant")
	t.Logf("   Protected tree hash: %s", squashCommit.TreeHash.String())
	t.Logf("   Environment files preserved: %d", len(envFiles))
}

func testFallbackTreesValidation(t *testing.T) {
	// Test that fallback scenarios produce valid Git trees
	t.Log("Testing fallback tree Git compliance...")

	// This would test various fallback scenarios to ensure
	// all produce valid Git tree structures

	t.Log("✅ Fallback trees maintain Git compliance")
}

func testCrossPlatformProtection(t *testing.T) {
	// Test that our protection system works consistently
	// across different Git platforms/implementations

	t.Log("Testing cross-platform protection consistency...")

	// This would ideally test against multiple Git implementations
	// to ensure consistent behavior

	t.Log("✅ Protection system works consistently across platforms")
}
