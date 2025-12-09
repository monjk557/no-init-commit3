package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

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

// setupTestFileOperationsHandler creates a test handler with a temporary repository
func setupTestFileOperationsHandler(t *testing.T) (*protocol.FileOperationsHandler, string, func()) {
	// Set Gin to test mode
	gin.SetMode(gin.TestMode)

	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "file-ops-test-*")
	require.NoError(t, err)

	// Setup test repository path
	testRepoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	err = os.MkdirAll(filepath.Dir(testRepoPath), 0755)
	require.NoError(t, err)

	// Initialize bare repository
	repo, err := gogit.PlainInit(testRepoPath, true)
	require.NoError(t, err)

	// Create initial commit with test files
	createInitialCommitWithFiles(t, repo)

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
	permClient := &mockPermissionClientForFileOps{
		readAccess:  true,
		writeAccess: true,
	}

	// Create handler
	handler, err := protocol.NewFileOperationsHandler(cfg, permClient)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return handler, testRepoPath, cleanup
}

// createInitialCommitWithFiles creates an initial commit with test files
func createInitialCommitWithFiles(t *testing.T, repo *gogit.Repository) {
	// Create blobs
	blob1 := createBlobForFileOps(t, repo, "old.txt", "old content")
	blob2 := createBlobForFileOps(t, repo, "existing.txt", "existing content")
	blob3 := createBlobForFileOps(t, repo, "another.txt", "another content")

	// Create tree
	tree := createTreeForFileOps(t, repo, []treeEntryForFileOps{
		{Name: "old.txt", Mode: 0100644, Hash: blob1},
		{Name: "existing.txt", Mode: 0100644, Hash: blob2},
		{Name: "another.txt", Mode: 0100644, Hash: blob3},
	})

	// Create commit
	commit := createCommitForFileOps(t, repo, "Initial commit", tree, nil)

	// Set up main branch reference
	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commit)
	err := repo.Storer.SetReference(mainRef)
	require.NoError(t, err)

	// Set HEAD to main
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	err = repo.Storer.SetReference(headRef)
	require.NoError(t, err)
}

type treeEntryForFileOps struct {
	Name string
	Mode filemode.FileMode
	Hash plumbing.Hash
}

func createBlobForFileOps(t *testing.T, repo *gogit.Repository, filename, content string) plumbing.Hash {
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

func createTreeForFileOps(t *testing.T, repo *gogit.Repository, entries []treeEntryForFileOps) plumbing.Hash {
	treeEntries := make([]object.TreeEntry, len(entries))

	for i, entry := range entries {
		treeEntries[i] = object.TreeEntry{
			Name: entry.Name,
			Mode: entry.Mode,
			Hash: entry.Hash,
		}
	}

	// Sort tree entries (Git requirement)
	// Git tree entries must be sorted using the same logic as go-git's TreeEntrySorter:
	// - Directories have "/" appended to their name for comparison
	// - This ensures directories come before files with the same prefix
	// - Then entries are sorted alphabetically by the modified name
	sort.Slice(treeEntries, func(i, j int) bool {
		name1 := treeEntries[i].Name
		name2 := treeEntries[j].Name

		// Append "/" to directory names for comparison (same as go-git's TreeEntrySorter)
		if treeEntries[i].Mode == filemode.Dir {
			name1 += "/"
		}
		if treeEntries[j].Mode == filemode.Dir {
			name2 += "/"
		}

		return name1 < name2
	})

	tree := &object.Tree{
		Entries: treeEntries,
	}

	treeObj := repo.Storer.NewEncodedObject()
	err := tree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)
	return treeHash
}

func createCommitForFileOps(t *testing.T, repo *gogit.Repository, message string, treeHash plumbing.Hash, parents []plumbing.Hash) plumbing.Hash {
	commit := &object.Commit{
		Message:      message,
		TreeHash:     treeHash,
		ParentHashes: parents,
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
	}

	commitObj := repo.Storer.NewEncodedObject()
	err := commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)
	return commitHash
}

type mockPermissionClientForFileOps struct {
	readAccess  bool
	writeAccess bool
}

func (m *mockPermissionClientForFileOps) CheckReadAccess(ctx context.Context, userID, repoOwner, repoName string) (bool, error) {
	return m.readAccess, nil
}

func (m *mockPermissionClientForFileOps) CheckWriteAccess(ctx context.Context, userID, repoOwner, repoName string) (bool, error) {
	return m.writeAccess, nil
}

func (m *mockPermissionClientForFileOps) InvalidateCache(userID, repoOwner, repoName string) {
	// No-op for tests
}

func (m *mockPermissionClientForFileOps) ClearCache() {
	// No-op for tests
}

func (m *mockPermissionClientForFileOps) GetCircuitBreakerMetrics() *permissions.CircuitBreakerMetrics {
	return nil
}

func (m *mockPermissionClientForFileOps) Close() error {
	return nil
}

// TestMultiFileCommit_MoveThenUpdateOriginalPath tests that updating the original path after move fails
func TestMultiFileCommit_MoveThenUpdateOriginalPath(t *testing.T) {
	handler, _, cleanup := setupTestFileOperationsHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	})

	router.POST("/repos/:owner/:repo/commits", handler.HandleMultiFileCommit)

	// Try to move old.txt -> new.txt and then update old.txt (should fail)
	reqBody := protocol.MultiFileCommitRequest{
		Message: "Move and update original",
		Branch:  "main",
		Operations: []protocol.MultiFileOperation{
			{
				Operation:    protocol.OperationMove,
				Path:         "new.txt",
				OriginalPath: "old.txt",
			},
			{
				Operation: protocol.OperationUpdate,
				Path:      "old.txt",
				Content:   "updated content",
			},
		},
		Author: &protocol.CommitAuthor{
			Name:  "Test User",
			Email: "test@example.com",
		},
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/repos/testowner/testrepo/commits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Should fail because old.txt is moved and then we try to update it
	assert.NotEqual(t, http.StatusOK, w.Code, "Should fail when trying to update original path after move")
}

// TestMultiFileCommit_MoveThenCreateTargetPath tests that creating a file at target path after move fails
func TestMultiFileCommit_MoveThenCreateTargetPath(t *testing.T) {
	handler, _, cleanup := setupTestFileOperationsHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	})

	router.POST("/repos/:owner/:repo/commits", handler.HandleMultiFileCommit)

	// Try to move old.txt -> new.txt and then create new.txt (should fail)
	reqBody := protocol.MultiFileCommitRequest{
		Message: "Move and create target",
		Branch:  "main",
		Operations: []protocol.MultiFileOperation{
			{
				Operation:    protocol.OperationMove,
				Path:         "new.txt",
				OriginalPath: "old.txt",
			},
			{
				Operation: protocol.OperationCreate,
				Path:      "new.txt",
				Content:   "new content",
			},
		},
		Author: &protocol.CommitAuthor{
			Name:  "Test User",
			Email: "test@example.com",
		},
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/repos/testowner/testrepo/commits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Should fail because new.txt is created by move and then we try to create it again
	assert.NotEqual(t, http.StatusOK, w.Code, "Should fail when trying to create target path after move")
}

// TestMultiFileCommit_MoveThenDeleteOriginalPath tests that deleting the original path after move fails
func TestMultiFileCommit_MoveThenDeleteOriginalPath(t *testing.T) {
	handler, _, cleanup := setupTestFileOperationsHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	})

	router.POST("/repos/:owner/:repo/commits", handler.HandleMultiFileCommit)

	// Try to move old.txt -> new.txt and then delete old.txt (should fail)
	reqBody := protocol.MultiFileCommitRequest{
		Message: "Move and delete original",
		Branch:  "main",
		Operations: []protocol.MultiFileOperation{
			{
				Operation:    protocol.OperationMove,
				Path:         "new.txt",
				OriginalPath: "old.txt",
			},
			{
				Operation: protocol.OperationDelete,
				Path:      "old.txt",
			},
		},
		Author: &protocol.CommitAuthor{
			Name:  "Test User",
			Email: "test@example.com",
		},
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/repos/testowner/testrepo/commits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Should fail because old.txt is moved and then we try to delete it
	assert.NotEqual(t, http.StatusOK, w.Code, "Should fail when trying to delete original path after move")
}

// TestMultiFileCommit_MoveThenUpdateTargetPath tests that updating the target path after move fails
func TestMultiFileCommit_MoveThenUpdateTargetPath(t *testing.T) {
	handler, _, cleanup := setupTestFileOperationsHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	})

	router.POST("/repos/:owner/:repo/commits", handler.HandleMultiFileCommit)

	// Try to move old.txt -> new.txt and then update new.txt (should fail)
	reqBody := protocol.MultiFileCommitRequest{
		Message: "Move and update target",
		Branch:  "main",
		Operations: []protocol.MultiFileOperation{
			{
				Operation:    protocol.OperationMove,
				Path:         "new.txt",
				OriginalPath: "old.txt",
			},
			{
				Operation: protocol.OperationUpdate,
				Path:      "new.txt",
				Content:   "updated content",
			},
		},
		Author: &protocol.CommitAuthor{
			Name:  "Test User",
			Email: "test@example.com",
		},
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/repos/testowner/testrepo/commits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Should fail because new.txt is created by move and then we try to update it (file doesn't exist in base tree)
	assert.NotEqual(t, http.StatusOK, w.Code, "Should fail when trying to update target path after move")
}

// TestMultiFileCommit_UpdateThenMoveOriginalPath tests that moving a file after updating it should work
func TestMultiFileCommit_UpdateThenMoveOriginalPath(t *testing.T) {
	handler, _, cleanup := setupTestFileOperationsHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	})

	router.POST("/repos/:owner/:repo/commits", handler.HandleMultiFileCommit)

	// Update old.txt and then move it (should work - update happens first, then move)
	reqBody := protocol.MultiFileCommitRequest{
		Message: "Update and move",
		Branch:  "main",
		Operations: []protocol.MultiFileOperation{
			{
				Operation: protocol.OperationUpdate,
				Path:      "old.txt",
				Content:   "updated content",
			},
			{
				Operation:    protocol.OperationMove,
				Path:         "new.txt",
				OriginalPath: "old.txt",
			},
		},
		Author: &protocol.CommitAuthor{
			Name:  "Test User",
			Email: "test@example.com",
		},
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/repos/testowner/testrepo/commits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Should succeed: update happens first, then move with updated content
	assert.Equal(t, http.StatusOK, w.Code, "Should succeed when updating a file and then moving it in the same transaction")

	// Verify the response
	// api.SendSuccess wraps the response in APIResponse with Data field
	var apiResponse struct {
		Success bool                             `json:"success"`
		Data    protocol.MultiFileCommitResponse `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &apiResponse)
	require.NoError(t, err)
	assert.True(t, apiResponse.Success, "Transaction should succeed")
	assert.Equal(t, 2, apiResponse.Data.TotalOperations)
	assert.Equal(t, 2, apiResponse.Data.SuccessfulOps)

	// Note: Content verification would require accessing handler's objectManager
	// which is not exposed. The test verifies that the operation succeeds.
	// Content verification can be done via the file content API endpoint.
}

// TestMultiFileCommit_MoveThenMoveAgain tests that moving a file twice in the same commit fails
func TestMultiFileCommit_MoveThenMoveAgain(t *testing.T) {
	handler, _, cleanup := setupTestFileOperationsHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	})

	router.POST("/repos/:owner/:repo/commits", handler.HandleMultiFileCommit)

	// Try to move old.txt -> new.txt and then move new.txt -> final.txt (should fail)
	reqBody := protocol.MultiFileCommitRequest{
		Message: "Move twice",
		Branch:  "main",
		Operations: []protocol.MultiFileOperation{
			{
				Operation:    protocol.OperationMove,
				Path:         "new.txt",
				OriginalPath: "old.txt",
			},
			{
				Operation:    protocol.OperationMove,
				Path:         "final.txt",
				OriginalPath: "new.txt",
			},
		},
		Author: &protocol.CommitAuthor{
			Name:  "Test User",
			Email: "test@example.com",
		},
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/repos/testowner/testrepo/commits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Should fail because new.txt doesn't exist in base tree (it's created by first move)
	assert.NotEqual(t, http.StatusOK, w.Code, "Should fail when trying to move a file that was moved in the same transaction")
}

// TestMultiFileCommit_MoveOriginalPathUsedByOtherOperation tests various conflicts
func TestMultiFileCommit_MoveOriginalPathUsedByOtherOperation(t *testing.T) {
	handler, _, cleanup := setupTestFileOperationsHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	})

	router.POST("/repos/:owner/:repo/commits", handler.HandleMultiFileCommit)

	testCases := []struct {
		name        string
		operations  []protocol.MultiFileOperation
		shouldFail  bool
		description string
	}{
		{
			name: "Move then create at original path",
			operations: []protocol.MultiFileOperation{
				{
					Operation:    protocol.OperationMove,
					Path:         "new.txt",
					OriginalPath: "old.txt",
				},
				{
					Operation: protocol.OperationCreate,
					Path:      "old.txt",
					Content:   "new content",
				},
			},
			shouldFail:  true,
			description: "Should fail: trying to create at original path after move",
		},
		{
			name: "Move then delete at original path",
			operations: []protocol.MultiFileOperation{
				{
					Operation:    protocol.OperationMove,
					Path:         "new.txt",
					OriginalPath: "old.txt",
				},
				{
					Operation: protocol.OperationDelete,
					Path:      "old.txt",
				},
			},
			shouldFail:  true,
			description: "Should fail: trying to delete original path after move",
		},
		{
			name: "Move then update at original path",
			operations: []protocol.MultiFileOperation{
				{
					Operation:    protocol.OperationMove,
					Path:         "new.txt",
					OriginalPath: "old.txt",
				},
				{
					Operation: protocol.OperationUpdate,
					Path:      "old.txt",
					Content:   "updated content",
				},
			},
			shouldFail:  true,
			description: "Should fail: trying to update original path after move",
		},
		{
			name: "Move then create at target path",
			operations: []protocol.MultiFileOperation{
				{
					Operation:    protocol.OperationMove,
					Path:         "new.txt",
					OriginalPath: "old.txt",
				},
				{
					Operation: protocol.OperationCreate,
					Path:      "new.txt",
					Content:   "new content",
				},
			},
			shouldFail:  true,
			description: "Should fail: trying to create at target path after move",
		},
		{
			name: "Move then update at target path",
			operations: []protocol.MultiFileOperation{
				{
					Operation:    protocol.OperationMove,
					Path:         "new.txt",
					OriginalPath: "old.txt",
				},
				{
					Operation: protocol.OperationUpdate,
					Path:      "new.txt",
					Content:   "updated content",
				},
			},
			shouldFail:  true,
			description: "Should fail: trying to update target path after move (file doesn't exist in base)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reqBody := protocol.MultiFileCommitRequest{
				Message:    tc.description,
				Branch:     "main",
				Operations: tc.operations,
				Author: &protocol.CommitAuthor{
					Name:  "Test User",
					Email: "test@example.com",
				},
			}

			body, _ := json.Marshal(reqBody)
			req, _ := http.NewRequest("POST", "/repos/testowner/testrepo/commits", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			router.ServeHTTP(w, req)

			if tc.shouldFail {
				assert.NotEqual(t, http.StatusOK, w.Code, tc.description)
			} else {
				assert.Equal(t, http.StatusOK, w.Code, tc.description)
			}
		})
	}
}

// TestMultiFileCommit_UpdateThenMoveWithContentVerification tests that update followed by move
// succeeds and the moved file contains the updated content
func TestMultiFileCommit_UpdateThenMoveWithContentVerification(t *testing.T) {
	handler, _, cleanup := setupTestFileOperationsHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), protocol.GinContextKey{}, c))
		c.Next()
	})

	router.POST("/repos/:owner/:repo/commits", handler.HandleMultiFileCommit)
	router.GET("/repos/:owner/:repo/files/*path", handler.HandleFileContent)

	// Update old.txt with new content and then move it to new.txt
	updatedContent := "test test test 123"
	reqBody := protocol.MultiFileCommitRequest{
		Message: "Update and move",
		Branch:  "main",
		Operations: []protocol.MultiFileOperation{
			{
				Operation: protocol.OperationUpdate,
				Path:      "old.txt",
				Content:   updatedContent,
			},
			{
				Operation:    protocol.OperationMove,
				Path:         "DONTREADME.md",
				OriginalPath: "old.txt",
			},
		},
		Author: &protocol.CommitAuthor{
			Name:  "Test User",
			Email: "test@example.com",
		},
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/repos/testowner/testrepo/commits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Should succeed: update happens first, then move with updated content
	assert.Equal(t, http.StatusOK, w.Code, "Should succeed when updating a file and then moving it in the same transaction")

	// Verify the response
	// api.SendSuccess wraps the response in APIResponse with Data field
	var apiResponse struct {
		Success bool                             `json:"success"`
		Data    protocol.MultiFileCommitResponse `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &apiResponse)
	require.NoError(t, err)
	assert.True(t, apiResponse.Success, "Transaction should succeed")
	assert.Equal(t, 2, apiResponse.Data.TotalOperations)
	assert.Equal(t, 2, apiResponse.Data.SuccessfulOps)

	// Verify that old.txt no longer exists
	getReq, _ := http.NewRequest("GET", "/repos/testowner/testrepo/files/old.txt?ref=main", nil)
	getW := httptest.NewRecorder()
	router.ServeHTTP(getW, getReq)
	assert.NotEqual(t, http.StatusOK, getW.Code, "old.txt should not exist after move")

	// Verify that DONTREADME.md exists with updated content
	getReq2, _ := http.NewRequest("GET", "/repos/testowner/testrepo/files/DONTREADME.md?ref=main", nil)
	getW2 := httptest.NewRecorder()
	router.ServeHTTP(getW2, getReq2)
	assert.Equal(t, http.StatusOK, getW2.Code, "DONTREADME.md should exist after move")

	var fileResponse protocol.FileContentResponse
	err = json.Unmarshal(getW2.Body.Bytes(), &fileResponse)
	require.NoError(t, err)
	assert.Equal(t, updatedContent, fileResponse.Content, "Moved file should contain updated content from update operation")
}
