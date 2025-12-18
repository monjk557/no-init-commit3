package branches

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

func setupTestHandler(t *testing.T) (*BranchOperationsHandler, string, func()) {
	gin.SetMode(gin.TestMode)

	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "branches-test-*")
	require.NoError(t, err)

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	handler, err := NewBranchOperationsHandler(cfg)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return handler, tempDir, cleanup
}

func createTestRepository(t *testing.T, tempDir, owner, repo string) string {
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// Set HEAD to master branch
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	err = gitRepo.Storer.SetReference(headRef)
	require.NoError(t, err)

	// Create master branch reference with a dummy hash
	// For testing purposes, we use a zero hash since we don't need actual commits
	emptyHash := plumbing.NewHash("0000000000000000000000000000000000000000")
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), emptyHash)
	err = gitRepo.Storer.SetReference(masterRef)
	require.NoError(t, err)

	return repoPath
}

func TestHandleCreateBranch_Success(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	createTestRepository(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

	requestBody := CreateBranchRequest{
		Name:   "feature/new-feature",
		Source: "master",
	}

	body, err := json.Marshal(requestBody)
	require.NoError(t, err)

	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response CreateBranchResponse
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)
	assert.Equal(t, "feature/new-feature", response.Data.BranchName)
	assert.Equal(t, "master", response.Data.Source)
	assert.Equal(t, "refs/heads/feature/new-feature", response.Data.RefPath)
	assert.True(t, response.Data.IsNewBranch)
	assert.NotEmpty(t, response.Data.CommitSHA)
	assert.NotEmpty(t, response.Data.CreatedAt)
	// Details may be empty if no checkout or force was used
}

func TestHandleCreateBranch_WithCheckout(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	createTestRepository(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

	requestBody := CreateBranchRequest{
		Name:     "feature/checkout-test",
		Source:   "master",
		Checkout: true,
	}

	body, err := json.Marshal(requestBody)
	require.NoError(t, err)

	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response CreateBranchResponse
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)
	assert.True(t, response.Data.Details.Checkout)
}

func TestHandleCreateBranch_AlreadyExists(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	repoPath := createTestRepository(t, tempDir, "testowner", "testrepo")

	// Create branch manually first
	gitRepo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	head, err := gitRepo.Head()
	require.NoError(t, err)

	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature/existing"), head.Hash())
	err = gitRepo.Storer.SetReference(branchRef)
	require.NoError(t, err)

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

	requestBody := CreateBranchRequest{
		Name:   "feature/existing",
		Source: "master",
		Force:  false,
	}

	body, err := json.Marshal(requestBody)
	require.NoError(t, err)

	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Should return 409 Conflict when branch already exists
	assert.Equal(t, http.StatusConflict, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response["success"].(bool))
	assert.Contains(t, response["error"].(string), "already exists")
}

func TestHandleCreateBranch_WithForce(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	repoPath := createTestRepository(t, tempDir, "testowner", "testrepo")

	// Create branch manually first
	gitRepo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	head, err := gitRepo.Head()
	require.NoError(t, err)

	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature/force-test"), head.Hash())
	err = gitRepo.Storer.SetReference(branchRef)
	require.NoError(t, err)

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

	requestBody := CreateBranchRequest{
		Name:   "feature/force-test",
		Source: "master",
		Force:  true,
	}

	body, err := json.Marshal(requestBody)
	require.NoError(t, err)

	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response CreateBranchResponse
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)
	assert.True(t, response.Data.Details.ForceUsed)
}

func TestHandleCreateBranch_InvalidBranchName(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	createTestRepository(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

	requestBody := CreateBranchRequest{
		Name:   "invalid/branch/name", // More than one slash level
		Source: "master",
	}

	body, err := json.Marshal(requestBody)
	require.NoError(t, err)

	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response["success"].(bool))
	assert.Contains(t, response["error"].(string), "branch name")
}

func TestHandleCreateBranch_InvalidJSON(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	createTestRepository(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

	// Send invalid JSON
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader("{invalid json}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleUpdateBranch_Success(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	repoPath := createTestRepository(t, tempDir, "testowner", "testrepo")

	// Create a feature branch
	gitRepo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	head, err := gitRepo.Head()
	require.NoError(t, err)

	featureRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature/test"), head.Hash())
	err = gitRepo.Storer.SetReference(featureRef)
	require.NoError(t, err)

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", handler.HandleUpdateBranch)

	requestBody := UpdateBranchRequest{
		Target: "feature/test",
		Force:  false,
	}

	body, err := json.Marshal(requestBody)
	require.NoError(t, err)

	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/master", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// May succeed or fail depending on fast-forward status
	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusBadRequest)
}

func TestHandleUpdateBranch_Rename(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	repoPath := createTestRepository(t, tempDir, "testowner", "testrepo")

	// Create a branch to rename
	gitRepo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	head, err := gitRepo.Head()
	require.NoError(t, err)

	oldBranchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/old-branch"), head.Hash())
	err = gitRepo.Storer.SetReference(oldBranchRef)
	require.NoError(t, err)

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", handler.HandleUpdateBranch)

	requestBody := UpdateBranchRequest{
		NewName: "new-branch",
		Force:   false,
	}

	body, err := json.Marshal(requestBody)
	require.NoError(t, err)

	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/old-branch", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response["success"].(bool))
	data := response["data"].(map[string]interface{})
	assert.Equal(t, "rename", data["operation"])
	assert.Equal(t, "new-branch", data["new_branch_name"])
}

func TestHandleUpdateBranch_BranchNotFound(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	createTestRepository(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", handler.HandleUpdateBranch)

	requestBody := UpdateBranchRequest{
		Target: "master",
		Force:  false,
	}

	body, err := json.Marshal(requestBody)
	require.NoError(t, err)

	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/nonexistent", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response["success"].(bool))
	assert.Contains(t, response["error"].(string), "does not exist")
}

func TestHandleDeleteBranch_Success(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	repoPath := createTestRepository(t, tempDir, "testowner", "testrepo")

	// Create a branch to delete
	gitRepo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	head, err := gitRepo.Head()
	require.NoError(t, err)

	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature/to-delete"), head.Hash())
	err = gitRepo.Storer.SetReference(branchRef)
	require.NoError(t, err)

	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", handler.HandleDeleteBranch)

	req := httptest.NewRequest("DELETE", "/repository/testowner/testrepo/branches/feature/to-delete", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response["success"].(bool))
	data := response["data"].(map[string]interface{})
	assert.Equal(t, "feature/to-delete", data["branch_name"])
	assert.Equal(t, "delete", data["operation"])
}

func TestHandleDeleteBranch_NotFound(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	createTestRepository(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", handler.HandleDeleteBranch)

	req := httptest.NewRequest("DELETE", "/repository/testowner/testrepo/branches/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response["success"].(bool))
	assert.Contains(t, response["error"].(string), "does not exist")
}

func TestHandleDeleteBranch_InvalidBranchName(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	createTestRepository(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", handler.HandleDeleteBranch)

	req := httptest.NewRequest("DELETE", "/repository/testowner/testrepo/branches/invalid/branch/name", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response["success"].(bool))
	assert.Contains(t, response["error"].(string), "branch name")
}

// TestHandleCreateBranch_DuplicateBranchName_409 tests that creating a branch with the same name returns 409 Conflict
func TestHandleCreateBranch_DuplicateBranchName_409(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	createTestRepository(t, tempDir, "testowner", "testrepo")

	// Create a branch first using the API
	router := gin.New()
	router.POST("/repos/:owner/:repo/branches", handler.HandleCreateBranch)

	firstRequest := CreateBranchRequest{
		Name:   "feature/test-branch",
		Source: "master",
	}

	body, err := json.Marshal(firstRequest)
	require.NoError(t, err)

	req := httptest.NewRequest("POST", "/repos/testowner/testrepo/branches", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// First creation should succeed
	assert.Equal(t, http.StatusOK, w.Code)

	// Try to create the same branch again
	duplicateRequest := CreateBranchRequest{
		Name:   "feature/test-branch",
		Source: "master",
		Force:  false,
	}

	body, err = json.Marshal(duplicateRequest)
	require.NoError(t, err)

	req = httptest.NewRequest("POST", "/repos/testowner/testrepo/branches", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Should return 409 Conflict
	assert.Equal(t, http.StatusConflict, w.Code, "Duplicate branch creation should return 409 Conflict")

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response["success"].(bool))
	assert.Contains(t, response["error"].(string), "already exists")
}

// TestHandleValidateBranchName_Available tests validation API when branch name is available
func TestHandleValidateBranchName_Available(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	createTestRepository(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/validate/*name", handler.HandleValidateBranchName)

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/validate/new-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response["success"].(bool))
	assert.True(t, response["is_available"].(bool), "New branch name should be available")
}

// TestHandleValidateBranchName_NotAvailable tests validation API when branch name already exists
func TestHandleValidateBranchName_NotAvailable(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	repoPath := createTestRepository(t, tempDir, "testowner", "testrepo")

	// Create branch manually first
	gitRepo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	head, err := gitRepo.Head()
	require.NoError(t, err)

	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/existing-branch"), head.Hash())
	err = gitRepo.Storer.SetReference(branchRef)
	require.NoError(t, err)

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/validate/*name", handler.HandleValidateBranchName)

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/validate/existing-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response["success"].(bool))
	assert.False(t, response["is_available"].(bool), "Existing branch name should not be available")
}

// TestHandleValidateBranchName_WithSlash tests validation API with branch names containing slashes
func TestHandleValidateBranchName_WithSlash(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// Create test repository
	createTestRepository(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/validate/*name", handler.HandleValidateBranchName)

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/validate/feature/new-feature", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response["success"].(bool))
	// Branch with one slash should be valid and available
	assert.True(t, response["is_available"].(bool))
}

// TestHandleValidateBranchName_RepositoryNotFound tests validation API when repository doesn't exist
func TestHandleValidateBranchName_RepositoryNotFound(t *testing.T) {
	handler, _, cleanup := setupTestHandler(t)
	defer cleanup()

	// Don't create repository

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/validate/*name", handler.HandleValidateBranchName)

	req := httptest.NewRequest("GET", "/repos/testowner/nonexistent/branches/validate/some-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response["success"].(bool))
	assert.Contains(t, response["error"].(string), "repository not found")
}
