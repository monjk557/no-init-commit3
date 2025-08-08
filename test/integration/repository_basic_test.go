package integration

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRepositoryBasicOperations tests that the repository setup and basic Git operations work
func TestRepositoryBasicOperations(t *testing.T) {
	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "git-executor-repo-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Setup test repository path
	testRepoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	err = os.MkdirAll(filepath.Dir(testRepoPath), 0755)
	require.NoError(t, err)

	// Initialize bare repository
	_, err = gogit.PlainInit(testRepoPath, true)
	require.NoError(t, err)

	// Create initial commit to make repository valid
	createInitialCommit(t, tempDir, testRepoPath)

	// Test 1: Verify repository is valid and has commits
	repo, err := gogit.PlainOpen(testRepoPath)
	require.NoError(t, err)

	// Check that HEAD exists and points to a valid commit
	headRef, err := repo.Head()
	require.NoError(t, err)
	assert.Equal(t, plumbing.ReferenceName("refs/heads/master"), headRef.Name())

	// Verify the commit exists and has the expected content
	commit, err := repo.CommitObject(headRef.Hash())
	require.NoError(t, err)
	assert.Equal(t, "Initial commit", commit.Message)
	assert.Equal(t, "Test User", commit.Author.Name)
	assert.Equal(t, "test@example.com", commit.Author.Email)

	// Test 2: Verify the file content is correct
	tree, err := commit.Tree()
	require.NoError(t, err)

	file, err := tree.File("README.md")
	require.NoError(t, err)

	content, err := file.Contents()
	require.NoError(t, err)
	assert.Contains(t, content, "# Test Repository")
	assert.Contains(t, content, "Initial commit for integration testing")

	// Test 3: Verify we can add more content to the repository
	addSecondCommit(t, testRepoPath)

	// Reload repository to get latest state
	repo, err = gogit.PlainOpen(testRepoPath)
	require.NoError(t, err)

	// Verify we now have 2 commits
	commitIter, err := repo.Log(&gogit.LogOptions{})
	require.NoError(t, err)

	commitCount := 0
	err = commitIter.ForEach(func(c *object.Commit) error {
		commitCount++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, commitCount)

	t.Log("✅ Repository basic operations test passed - setup working correctly")
}

// addSecondCommit adds a second commit to test repository mutation
func addSecondCommit(t *testing.T, repoPath string) {
	// Open the bare repository directly
	repo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// Create a new blob for second file
	content := "# Second File\n\nThis is the second file added to test repository operations.\n"
	blob := repo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(content)))
	writer, err := blob.Writer()
	require.NoError(t, err)
	_, err = writer.Write([]byte(content))
	require.NoError(t, err)
	writer.Close()

	blobHash, err := repo.Storer.SetEncodedObject(blob)
	require.NoError(t, err)

	// Get the current tree from HEAD
	headRef, err := repo.Head()
	require.NoError(t, err)

	headCommit, err := repo.CommitObject(headRef.Hash())
	require.NoError(t, err)

	currentTree, err := headCommit.Tree()
	require.NoError(t, err)

	// Create new tree with existing files plus the new file
	entries := []object.TreeEntry{}

	// Add existing files
	err = currentTree.Files().ForEach(func(f *object.File) error {
		entries = append(entries, object.TreeEntry{
			Name: f.Name,
			Mode: f.Mode,
			Hash: f.Hash,
		})
		return nil
	})
	require.NoError(t, err)

	// Add new file
	entries = append(entries, object.TreeEntry{
		Name: "second.md",
		Mode: filemode.Regular,
		Hash: blobHash,
	})

	// Create the new tree
	tree := &object.Tree{Entries: entries}

	treeObj := repo.Storer.NewEncodedObject()
	err = tree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// Create second commit
	commit := &object.Commit{
		Message: "Add second file",
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{headRef.Hash()},
	}

	commitObj := repo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// Update HEAD and master branch to point to the new commit
	newHeadRef := plumbing.NewHashReference(plumbing.HEAD, commitHash)
	err = repo.Storer.SetReference(newHeadRef)
	require.NoError(t, err)

	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	err = repo.Storer.SetReference(masterRef)
	require.NoError(t, err)
}
