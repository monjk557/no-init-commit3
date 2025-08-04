package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Test repository setup helpers

func createTestRepo(t *testing.T) string {
	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Initialize git repository
	cmd := exec.Command("git", "init")
	cmd.Dir = tempDir
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(tempDir)
		t.Fatalf("Failed to init git repo: %v", err)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tempDir
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(tempDir)
		t.Fatalf("Failed to set git user name: %v", err)
	}

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = tempDir
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(tempDir)
		t.Fatalf("Failed to set git user email: %v", err)
	}

	return tempDir
}

func addFileToRepo(t *testing.T, repoPath, fileName, content string) {
	filePath := filepath.Join(repoPath, fileName)

	// Create directory if needed
	if dir := filepath.Dir(filePath); dir != repoPath {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create directory %s: %v", dir, err)
		}
	}

	// Write file
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write file %s: %v", fileName, err)
	}

	// Add to git
	cmd := exec.Command("git", "add", fileName)
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to add file %s: %v", fileName, err)
	}
}

func commitRepo(t *testing.T, repoPath, message string) string {
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Get the commit hash
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get commit hash: %v", err)
	}

	return strings.TrimSpace(string(output))
}

func createBranch(t *testing.T, repoPath, branchName string) {
	cmd := exec.Command("git", "checkout", "-b", branchName)
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to create branch %s: %v", branchName, err)
	}
}

func getCurrentBranch(t *testing.T, repoPath string) string {
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get current branch: %v", err)
	}
	return strings.TrimSpace(string(output))
}

// OpenRepository tests

func TestOpenRepository_Success(t *testing.T) {
	repoPath := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()

	// Add initial commit
	addFileToRepo(t, repoPath, "README.md", "# Test Repository")
	commitRepo(t, repoPath, "Initial commit")

	// Test opening the repository
	repo, err := OpenRepository(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository: %v", err)
	}
	defer func() { _ = repo.Close() }()

	// Verify the repository path
	if repo.Path() != repoPath {
		t.Errorf("Expected path %q, got %q", repoPath, repo.Path())
	}

	// Verify it implements the Repository interface
	var _ = repo
}

func TestOpenRepository_NotFound(t *testing.T) {
	nonExistentPath := "/path/that/does/not/exist"

	_, err := OpenRepository(nonExistentPath)
	if err == nil {
		t.Fatal("Expected error for non-existent path, got nil")
	}

	_, ok := err.(*RepositoryNotFoundError)
	if !ok {
		t.Errorf("Expected RepositoryNotFoundError, got %T: %v", err, err)
	}
}

func TestOpenRepository_InvalidRepository(t *testing.T) {
	// Create a regular directory (not a git repo)
	tempDir, err := os.MkdirTemp("", "not-git-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	_, err = OpenRepository(tempDir)
	if err == nil {
		t.Fatal("Expected error for invalid repository, got nil")
	}

	_, ok := err.(*InvalidRepositoryError)
	if !ok {
		t.Errorf("Expected InvalidRepositoryError, got %T: %v", err, err)
	}
}

// Repository interface tests

func TestGoGitRepository_GetTree(t *testing.T) {
	repoPath := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()

	// Add files and commit
	addFileToRepo(t, repoPath, "README.md", "# Test Repository")
	addFileToRepo(t, repoPath, "src/main.go", "package main\n\nfunc main() {}\n")
	addFileToRepo(t, repoPath, "docs/guide.md", "# Guide")
	commitHash := commitRepo(t, repoPath, "Initial commit")

	repo, err := OpenRepository(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository: %v", err)
	}
	defer func() { _ = repo.Close() }()

	ctx := context.Background()

	// Test getting tree from HEAD
	entries, err := repo.GetTree(ctx, "HEAD")
	if err != nil {
		t.Fatalf("Failed to get tree: %v", err)
	}

	// Should have 3 entries: README.md, src/, docs/
	if len(entries) != 3 {
		t.Errorf("Expected 3 entries, got %d", len(entries))
	}

	// Check for specific entries
	entryNames := make(map[string]TreeEntry)
	for _, entry := range entries {
		entryNames[entry.Path] = entry
	}

	// Check README.md
	if readme, exists := entryNames["README.md"]; exists {
		if readme.Type != EntryTypeBlob {
			t.Errorf("Expected README.md to be a blob, got %s", readme.Type)
		}
		if readme.Size <= 0 {
			t.Error("Expected README.md to have a size > 0")
		}
	} else {
		t.Error("Expected README.md in tree entries")
	}

	// Check src/ directory
	if src, exists := entryNames["src"]; exists {
		if src.Type != EntryTypeTree {
			t.Errorf("Expected src to be a tree, got %s", src.Type)
		}
	} else {
		t.Error("Expected src directory in tree entries")
	}

	// Test getting tree from commit hash
	entries2, err := repo.GetTree(ctx, commitHash)
	if err != nil {
		t.Fatalf("Failed to get tree from commit hash: %v", err)
	}

	if len(entries2) != len(entries) {
		t.Errorf("Expected same number of entries from commit hash, got %d vs %d", len(entries2), len(entries))
	}
}

func TestGoGitRepository_GetTree_InvalidRef(t *testing.T) {
	repoPath := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()

	addFileToRepo(t, repoPath, "README.md", "# Test")
	commitRepo(t, repoPath, "Initial commit")

	repo, err := OpenRepository(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository: %v", err)
	}
	defer func() { _ = repo.Close() }()

	ctx := context.Background()

	_, err = repo.GetTree(ctx, "non-existent-ref")
	if err == nil {
		t.Fatal("Expected error for invalid ref, got nil")
	}

	_, ok := err.(*ReferenceNotFoundError)
	if !ok {
		t.Errorf("Expected ReferenceNotFoundError, got %T: %v", err, err)
	}
}

func TestGoGitRepository_GetBlob(t *testing.T) {
	repoPath := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()

	fileContent := "package main\n\nfunc main() {\n\tfmt.Println(\"Hello, World!\")\n}\n"
	addFileToRepo(t, repoPath, "main.go", fileContent)
	commitRepo(t, repoPath, "Add main.go")

	repo, err := OpenRepository(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository: %v", err)
	}
	defer func() { _ = repo.Close() }()

	ctx := context.Background()

	// Test getting blob content
	content, err := repo.GetBlob(ctx, "main.go", "HEAD")
	if err != nil {
		t.Fatalf("Failed to get blob: %v", err)
	}

	if string(content) != fileContent {
		t.Errorf("Expected content %q, got %q", fileContent, string(content))
	}
}

func TestGoGitRepository_GetBlob_NotFound(t *testing.T) {
	repoPath := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()

	addFileToRepo(t, repoPath, "README.md", "# Test")
	commitRepo(t, repoPath, "Initial commit")

	repo, err := OpenRepository(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository: %v", err)
	}
	defer func() { _ = repo.Close() }()

	ctx := context.Background()

	_, err = repo.GetBlob(ctx, "non-existent-file.txt", "HEAD")
	if err == nil {
		t.Fatal("Expected error for non-existent file, got nil")
	}

	_, ok := err.(*PathNotFoundError)
	if !ok {
		t.Errorf("Expected PathNotFoundError, got %T: %v", err, err)
	}
}

func TestGoGitRepository_GetCommits(t *testing.T) {
	repoPath := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()

	// Create multiple commits
	addFileToRepo(t, repoPath, "README.md", "# Test")
	commitRepo(t, repoPath, "Initial commit")

	addFileToRepo(t, repoPath, "main.go", "package main")
	commitRepo(t, repoPath, "Add main.go")

	addFileToRepo(t, repoPath, "utils.go", "package main")
	commitRepo(t, repoPath, "Add utils.go")

	repo, err := OpenRepository(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository: %v", err)
	}
	defer func() { _ = repo.Close() }()

	ctx := context.Background()

	// Test getting all commits
	commits, err := repo.GetCommits(ctx, "HEAD", 10, 0)
	if err != nil {
		t.Fatalf("Failed to get commits: %v", err)
	}

	if len(commits) != 3 {
		t.Errorf("Expected 3 commits, got %d", len(commits))
	}

	// Check commit order (should be newest first)
	if commits[0].Message != "Add utils.go" {
		t.Errorf("Expected first commit to be 'Add utils.go', got %q", commits[0].Message)
	}

	if commits[2].Message != "Initial commit" {
		t.Errorf("Expected third commit to be 'Initial commit', got %q", commits[2].Message)
	}

	// Test pagination
	firstCommit, err := repo.GetCommits(ctx, "HEAD", 1, 0)
	if err != nil {
		t.Fatalf("Failed to get first commit: %v", err)
	}

	if len(firstCommit) != 1 {
		t.Errorf("Expected 1 commit, got %d", len(firstCommit))
	}

	secondCommit, err := repo.GetCommits(ctx, "HEAD", 1, 1)
	if err != nil {
		t.Fatalf("Failed to get second commit: %v", err)
	}

	if len(secondCommit) != 1 {
		t.Errorf("Expected 1 commit, got %d", len(secondCommit))
	}

	// Verify pagination works correctly
	if firstCommit[0].Hash == secondCommit[0].Hash {
		t.Error("First and second commits should be different")
	}

	// Test commit structure
	commit := commits[0]
	if commit.Hash == "" {
		t.Error("Commit hash should not be empty")
	}
	if commit.Author.Name == "" {
		t.Error("Author name should not be empty")
	}
	if commit.Author.Email == "" {
		t.Error("Author email should not be empty")
	}
	if commit.TreeHash == "" {
		t.Error("Tree hash should not be empty")
	}
}

func TestGoGitRepository_ContextCancellation(t *testing.T) {
	repoPath := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()

	addFileToRepo(t, repoPath, "README.md", "# Test")
	commitRepo(t, repoPath, "Initial commit")

	repo, err := OpenRepository(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository: %v", err)
	}
	defer func() { _ = repo.Close() }()

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Test that all methods respect context cancellation
	_, err = repo.GetTree(ctx, "HEAD")
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled for GetTree, got %v", err)
	}

	_, err = repo.GetBlob(ctx, "README.md", "HEAD")
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled for GetBlob, got %v", err)
	}

	_, err = repo.GetCommits(ctx, "HEAD", 10, 0)
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled for GetCommits, got %v", err)
	}
}

func TestGoGitRepository_BranchHandling(t *testing.T) {
	repoPath := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()

	// Create initial commit on default branch
	addFileToRepo(t, repoPath, "README.md", "# Default Branch")
	commitRepo(t, repoPath, "Initial commit on default branch")

	// Get the default branch name (could be "main" or "master" depending on Git version)
	defaultBranch := getCurrentBranch(t, repoPath)

	// Create and switch to feature branch
	createBranch(t, repoPath, "feature/test")
	addFileToRepo(t, repoPath, "feature.txt", "Feature content")
	commitRepo(t, repoPath, "Add feature")

	repo, err := OpenRepository(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository: %v", err)
	}
	defer func() { _ = repo.Close() }()

	ctx := context.Background()

	// Test accessing different branches using the detected default branch
	defaultTree, err := repo.GetTree(ctx, defaultBranch)
	if err != nil {
		t.Fatalf("Failed to get default branch (%s) tree: %v", defaultBranch, err)
	}

	featureTree, err := repo.GetTree(ctx, "feature/test")
	if err != nil {
		t.Fatalf("Failed to get feature tree: %v", err)
	}

	// Feature branch should have one more file
	if len(featureTree) != len(defaultTree)+1 {
		t.Errorf("Expected feature branch to have %d files, got %d", len(defaultTree)+1, len(featureTree))
	}

	// Test getting commits from different branches
	defaultCommits, err := repo.GetCommits(ctx, defaultBranch, 10, 0)
	if err != nil {
		t.Fatalf("Failed to get default branch (%s) commits: %v", defaultBranch, err)
	}

	featureCommits, err := repo.GetCommits(ctx, "feature/test", 10, 0)
	if err != nil {
		t.Fatalf("Failed to get feature commits: %v", err)
	}

	// Feature branch should have one more commit
	if len(featureCommits) != len(defaultCommits)+1 {
		t.Errorf("Expected feature branch to have %d commits, got %d", len(defaultCommits)+1, len(featureCommits))
	}
}

func TestGoGitRepository_Close(t *testing.T) {
	repoPath := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()

	addFileToRepo(t, repoPath, "README.md", "# Test")
	commitRepo(t, repoPath, "Initial commit")

	repo, err := OpenRepository(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository: %v", err)
	}

	// Test that Close doesn't return an error
	err = repo.Close()
	if err != nil {
		t.Errorf("Close should not return an error, got %v", err)
	}

	// Test that we can call Close multiple times
	err = repo.Close()
	if err != nil {
		t.Errorf("Multiple Close calls should not return an error, got %v", err)
	}
}
