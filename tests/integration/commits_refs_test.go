package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// setupTestCommitsHandler creates a test handler with a temporary repository
func setupTestCommitsHandler(t *testing.T) (*protocol.CommitsHandler, string, func()) {
	// Set Gin to test mode
	gin.SetMode(gin.TestMode)

	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "commits-test-*")
	require.NoError(t, err)

	// Setup test repository path
	testRepoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	err = os.MkdirAll(filepath.Dir(testRepoPath), 0755)
	require.NoError(t, err)

	// Initialize bare repository
	repo, err := gogit.PlainInit(testRepoPath, true)
	require.NoError(t, err)

	// Create test commits with branches
	createTestCommitsWithBranches(t, repo)

	// Setup configuration
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	// Create mock permission client
	permClient := &mockPermissionClient{
		readAccess:  true,
		writeAccess: true,
	}

	// Create handler
	handler, err := protocol.NewCommitsHandler(cfg, permClient)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return handler, testRepoPath, cleanup
}

// createTestCommitsWithBranches creates a test repository with multiple branches
// Structure:
//   - main: commit1 -> commit2 -> commit3
//   - feature: commit1 -> commit2 -> commit4 -> commit5
//     (commit1 and commit2 are shared, then branches diverge)
func createTestCommitsWithBranches(t *testing.T, repo *gogit.Repository) {
	now := time.Now()

	// Create initial blob
	blob1 := createBlob(t, repo, "file1.txt", "content1")
	blob2 := createBlob(t, repo, "file2.txt", "content2")
	blob3 := createBlob(t, repo, "file3.txt", "content3")
	blob4 := createBlob(t, repo, "file4.txt", "content4")
	blob5 := createBlob(t, repo, "file5.txt", "content5")

	// Create tree1 (initial)
	tree1 := createTree(t, repo, []treeEntry{
		{Name: "file1.txt", Mode: 0100644, Hash: blob1},
	})

	// Create commit1 (shared base)
	commit1 := createCommit(t, repo, "Initial commit", tree1, nil, now)
	commit1Hash := commit1

	// Create tree2
	tree2 := createTree(t, repo, []treeEntry{
		{Name: "file1.txt", Mode: 0100644, Hash: blob1},
		{Name: "file2.txt", Mode: 0100644, Hash: blob2},
	})

	// Create commit2 (shared, on both branches)
	commit2 := createCommit(t, repo, "Add file2", tree2, []plumbing.Hash{commit1Hash}, now.Add(time.Minute))
	commit2Hash := commit2

	// Create tree3 (for main branch)
	tree3 := createTree(t, repo, []treeEntry{
		{Name: "file1.txt", Mode: 0100644, Hash: blob1},
		{Name: "file2.txt", Mode: 0100644, Hash: blob2},
		{Name: "file3.txt", Mode: 0100644, Hash: blob3},
	})

	// Create commit3 (main branch only)
	commit3 := createCommit(t, repo, "Add file3 on main", tree3, []plumbing.Hash{commit2Hash}, now.Add(2*time.Minute))
	commit3Hash := commit3

	// Create tree4 (for feature branch)
	tree4 := createTree(t, repo, []treeEntry{
		{Name: "file1.txt", Mode: 0100644, Hash: blob1},
		{Name: "file2.txt", Mode: 0100644, Hash: blob2},
		{Name: "file4.txt", Mode: 0100644, Hash: blob4},
	})

	// Create commit4 (feature branch only)
	commit4 := createCommit(t, repo, "Add file4 on feature", tree4, []plumbing.Hash{commit2Hash}, now.Add(3*time.Minute))
	commit4Hash := commit4

	// Create tree5 (for feature branch)
	tree5 := createTree(t, repo, []treeEntry{
		{Name: "file1.txt", Mode: 0100644, Hash: blob1},
		{Name: "file2.txt", Mode: 0100644, Hash: blob2},
		{Name: "file4.txt", Mode: 0100644, Hash: blob4},
		{Name: "file5.txt", Mode: 0100644, Hash: blob5},
	})

	// Create commit5 (feature branch only)
	commit5 := createCommit(t, repo, "Add file5 on feature", tree5, []plumbing.Hash{commit4Hash}, now.Add(4*time.Minute))
	commit5Hash := commit5

	// Set up main branch reference
	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commit3Hash)
	err := repo.Storer.SetReference(mainRef)
	require.NoError(t, err)

	// Set up feature branch reference
	featureRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature"), commit5Hash)
	err = repo.Storer.SetReference(featureRef)
	require.NoError(t, err)

	// Set HEAD to main
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	err = repo.Storer.SetReference(headRef)
	require.NoError(t, err)
}

type treeEntry struct {
	Name string
	Mode filemode.FileMode
	Hash plumbing.Hash
}

func createBlob(t *testing.T, repo *gogit.Repository, filename, content string) plumbing.Hash {
	blob := repo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(content)))
	writer, err := blob.Writer()
	require.NoError(t, err)
	_, err = writer.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	blobHash, err := repo.Storer.SetEncodedObject(blob)
	require.NoError(t, err)
	return blobHash
}

func createTree(t *testing.T, repo *gogit.Repository, entries []treeEntry) plumbing.Hash {
	tree := &object.Tree{
		Entries: make([]object.TreeEntry, len(entries)),
	}

	for i, entry := range entries {
		tree.Entries[i] = object.TreeEntry{
			Name: entry.Name,
			Mode: entry.Mode,
			Hash: entry.Hash,
		}
	}

	treeObj := repo.Storer.NewEncodedObject()
	err := tree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)
	return treeHash
}

func createCommit(t *testing.T, repo *gogit.Repository, message string, treeHash plumbing.Hash, parents []plumbing.Hash, when time.Time) plumbing.Hash {
	commit := &object.Commit{
		Message:      message,
		TreeHash:     treeHash,
		ParentHashes: parents,
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  when,
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  when,
		},
	}

	commitObj := repo.Storer.NewEncodedObject()
	err := commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)
	return commitHash
}

type mockPermissionClient struct {
	readAccess  bool
	writeAccess bool
}

func (m *mockPermissionClient) CheckReadAccess(ctx context.Context, userID, repoOwner, repoName string) (bool, error) {
	return m.readAccess, nil
}

func (m *mockPermissionClient) CheckWriteAccess(ctx context.Context, userID, repoOwner, repoName string) (bool, error) {
	return m.writeAccess, nil
}

func (m *mockPermissionClient) InvalidateCache(userID, repoOwner, repoName string) {
	// No-op for tests
}

func (m *mockPermissionClient) ClearCache() {
	// No-op for tests
}

func (m *mockPermissionClient) GetCircuitBreakerMetrics() *permissions.CircuitBreakerMetrics {
	return nil
}

func (m *mockPermissionClient) Close() error {
	return nil
}

// TestHandleCommitsAPI_WithSourceAndTargetRef tests that commits between merge base and source branch are returned correctly
func TestHandleCommitsAPI_WithSourceAndTargetRef(t *testing.T) {
	handler, _, cleanup := setupTestCommitsHandler(t)
	defer cleanup()

	// Create Gin context
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	})

	router.GET("/repos/:owner/:repo/commits", handler.HandleCommitsAPI)

	// Test: Get commits from merge base (commit2) to feature branch (commit5)
	// Expected: commit4 and commit5 (commits unique to feature branch)
	req, _ := http.NewRequest("GET", "/repos/testowner/testrepo/commits?target_ref=main&source_ref=feature&limit=10&skip=0", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response protocol.CommitsResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)
	assert.Equal(t, "Commits fetched successfully", response.Message)

	// Verify that we got commits from feature branch (after merge base)
	commits, ok := response.Results.([]interface{})
	require.True(t, ok, "Results should be an array")

	// Should have 2 commits: commit4 and commit5 (commits unique to feature branch)
	assert.GreaterOrEqual(t, len(commits), 2, "Should have at least 2 commits from feature branch")

	// Verify commit messages
	commitMap := make(map[string]interface{})
	for _, commit := range commits {
		commitObj := commit.(map[string]interface{})
		message := commitObj["message"].(string)
		commitMap[message] = commitObj
	}

	// Should contain commits from feature branch
	assert.Contains(t, commitMap, "Add file4 on feature", "Should contain commit4")
	assert.Contains(t, commitMap, "Add file5 on feature", "Should contain commit5")

	// Should NOT contain commits from main branch
	assert.NotContains(t, commitMap, "Add file3 on main", "Should not contain main branch commits")

	// Verify pagination
	assert.Equal(t, 2, response.Pagination.Count, "Total count should be 2")
	assert.Equal(t, 10, response.Pagination.PageSize, "Page size should be 10")
}

// TestHandleCommitsAPI_WithSourceAndTargetRef_Reverse tests the reverse direction
func TestHandleCommitsAPI_WithSourceAndTargetRef_Reverse(t *testing.T) {
	handler, _, cleanup := setupTestCommitsHandler(t)
	defer cleanup()

	// Create Gin context
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	})

	router.GET("/repos/:owner/:repo/commits", handler.HandleCommitsAPI)

	// Test: Get commits from merge base (commit2) to main branch (commit3)
	// Expected: commit3 (commits unique to main branch)
	req, _ := http.NewRequest("GET", "/repos/testowner/testrepo/commits?target_ref=feature&source_ref=main&limit=10&skip=0", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response protocol.CommitsResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)

	// Verify that we got commits from main branch (after merge base)
	commits, ok := response.Results.([]interface{})
	require.True(t, ok, "Results should be an array")

	// Should have 1 commit: commit3 (commits unique to main branch)
	assert.Equal(t, 1, len(commits), "Should have 1 commit from main branch")

	// Verify commit message
	commitObj := commits[0].(map[string]interface{})
	message := commitObj["message"].(string)
	assert.Equal(t, "Add file3 on main", message, "Should contain commit3 from main branch")
}

// TestHandleCommitsAPI_WithSourceAndTargetRef_SameBranch tests when source and target are the same
func TestHandleCommitsAPI_WithSourceAndTargetRef_SameBranch(t *testing.T) {
	handler, _, cleanup := setupTestCommitsHandler(t)
	defer cleanup()

	// Create Gin context
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	})

	router.GET("/repos/:owner/:repo/commits", handler.HandleCommitsAPI)

	// Test: Get commits when source and target are the same
	// Expected: empty array (no commits between merge base and source when they're the same)
	req, _ := http.NewRequest("GET", "/repos/testowner/testrepo/commits?target_ref=main&source_ref=main&limit=10&skip=0", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response protocol.CommitsResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)

	// Verify that we got empty commits
	commits, ok := response.Results.([]interface{})
	require.True(t, ok, "Results should be an array")
	assert.Equal(t, 0, len(commits), "Should have no commits when source and target are the same")
	assert.Equal(t, 0, response.Pagination.Count, "Total count should be 0")
}

// TestHandleCommitsAPI_WithoutSourceAndTargetRef tests normal commit history when refs are not provided
func TestHandleCommitsAPI_WithoutSourceAndTargetRef(t *testing.T) {
	handler, _, cleanup := setupTestCommitsHandler(t)
	defer cleanup()

	// Create Gin context
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	})

	router.GET("/repos/:owner/:repo/commits", handler.HandleCommitsAPI)

	// Test: Get normal commit history without source/target refs
	req, _ := http.NewRequest("GET", "/repos/testowner/testrepo/commits?ref=main&limit=10&skip=0", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response protocol.CommitsResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)

	// Verify that we got commits
	commits, ok := response.Results.([]interface{})
	require.True(t, ok, "Results should be an array")
	assert.Greater(t, len(commits), 0, "Should have commits")
}
