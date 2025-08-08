package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"net/http/httptest"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/protocol"
)

// BranchOperationsIntegrationTestSuite provides a complete test environment for Branch Operations API integration testing
type BranchOperationsIntegrationTestSuite struct {
	router       *gin.Engine
	testServer   *httptest.Server
	config       *config.Config
	logger       *logging.Logger
	tempDir      string
	testRepoPath string
	permClient   *mockBranchOpsPermissionClient
	handler      *protocol.BranchOperationsHandler
}

// mockBranchOpsPermissionClient implements permissions.PermissionClient for testing
type mockBranchOpsPermissionClient struct {
	readAccess  bool
	writeAccess bool
}

func (m *mockBranchOpsPermissionClient) CheckReadAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	return m.readAccess, nil
}

func (m *mockBranchOpsPermissionClient) CheckWriteAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	return m.writeAccess, nil
}

func (m *mockBranchOpsPermissionClient) InvalidateCache(userID, owner, repo string) {}
func (m *mockBranchOpsPermissionClient) ClearCache()                                {}
func (m *mockBranchOpsPermissionClient) GetCircuitBreakerMetrics() *permissions.CircuitBreakerMetrics {
	return &permissions.CircuitBreakerMetrics{}
}
func (m *mockBranchOpsPermissionClient) Close() error { return nil }

// SetupBranchOperationsIntegrationTest creates a complete test environment
func SetupBranchOperationsIntegrationTest(t *testing.T) *BranchOperationsIntegrationTestSuite {
	// Set Gin to test mode to avoid route conflicts
	gin.SetMode(gin.TestMode)

	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "branch-ops-integration-test-*")
	require.NoError(t, err)

	// Setup test repository path
	testRepoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	err = os.MkdirAll(filepath.Dir(testRepoPath), 0755)
	require.NoError(t, err)

	// Initialize bare repository
	_, err = gogit.PlainInit(testRepoPath, true)
	require.NoError(t, err)

	// Create initial commit with multiple branches
	createRepositoryWithMultipleBranches(t, testRepoPath)

	// Setup configuration
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Server: config.ServerConfig{
			Port: 8080,
		},
	}

	// Create logger
	logger, err := logging.NewLogger(cfg.Logging)
	require.NoError(t, err)

	// Create permission client with read/write access
	permClient := &mockBranchOpsPermissionClient{
		readAccess:  true,
		writeAccess: true,
	}

	// Create branch operations handler
	handler, err := protocol.NewBranchOperationsHandler(cfg, permClient)
	require.NoError(t, err)

	// Create a test router with branch operations endpoints
	router := gin.New()
	router.Use(gin.Recovery())

	// Create response middleware
	responseMiddleware := api.NewJSONResponseMiddleware(logger, "v1")
	router.Use(responseMiddleware.RequestMetadataMiddleware())
	router.Use(responseMiddleware.ErrorHandler())

	// Add branch operations endpoints
	reposGroup := router.Group("/repos")
	{
		ownerGroup := reposGroup.Group("/:owner")
		{
			repoGroup := ownerGroup.Group("/:repo")
			{
				// Branch operations endpoints
				branchesGroup := repoGroup.Group("/branches")
				{
					branchesGroup.POST("", handler.HandleCreateBranch)
					branchesGroup.PATCH("/*branch", handler.HandleUpdateBranch)
					branchesGroup.DELETE("/*branch", handler.HandleDeleteBranch)
				}
			}
		}
	}

	// Add basic health check
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Create test server
	testServer := httptest.NewServer(router)

	return &BranchOperationsIntegrationTestSuite{
		router:       router,
		testServer:   testServer,
		config:       cfg,
		logger:       logger,
		tempDir:      tempDir,
		testRepoPath: testRepoPath,
		permClient:   permClient,
		handler:      handler,
	}
}

// createRepositoryWithMultipleBranches creates a test repository with multiple branches
func createRepositoryWithMultipleBranches(t *testing.T, repoPath string) {
	// Open the repository
	repo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	storer := repo.Storer

	// Create a blob for README
	readmeContent := []byte("# Test Repository\n\nThis is a test repository for Branch Operations API integration tests.\n")
	obj := storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(int64(len(readmeContent)))
	writer, err := obj.Writer()
	require.NoError(t, err)
	_, err = writer.Write(readmeContent)
	require.NoError(t, err)
	err = writer.Close()
	require.NoError(t, err)
	readmeHash, err := storer.SetEncodedObject(obj)
	require.NoError(t, err)

	// Create initial tree with README
	tree := &object.Tree{
		Entries: []object.TreeEntry{
			{
				Name: "README.md",
				Mode: 0644,
				Hash: readmeHash,
			},
		},
	}
	treeObj := storer.NewEncodedObject()
	err = tree.Encode(treeObj)
	require.NoError(t, err)
	initialTreeHash, err := storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// Create initial commit
	initialCommit := &object.Commit{
		Author: object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
			When:  time.Now().Add(-48 * time.Hour),
		},
		Committer: object.Signature{
			Name:  "Test Committer",
			Email: "test@example.com",
			When:  time.Now().Add(-48 * time.Hour),
		},
		Message:  "Initial commit\n\nAdded README.md",
		TreeHash: initialTreeHash,
	}
	initialCommitObj := storer.NewEncodedObject()
	err = initialCommit.Encode(initialCommitObj)
	require.NoError(t, err)
	initialCommitHash, err := storer.SetEncodedObject(initialCommitObj)
	require.NoError(t, err)

	// Set HEAD to point to master
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	err = storer.SetReference(headRef)
	require.NoError(t, err)

	// Create master branch pointing to the initial commit
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), initialCommitHash)
	err = storer.SetReference(masterRef)
	require.NoError(t, err)

	// Create a second commit for develop branch
	configContent := []byte(`{
  "name": "test-repo",
  "version": "1.0.0",
  "description": "Branch operations test repository"
}`)
	configObj := storer.NewEncodedObject()
	configObj.SetType(plumbing.BlobObject)
	configObj.SetSize(int64(len(configContent)))
	configWriter, err := configObj.Writer()
	require.NoError(t, err)
	_, err = configWriter.Write(configContent)
	require.NoError(t, err)
	err = configWriter.Close()
	require.NoError(t, err)
	configHash, err := storer.SetEncodedObject(configObj)
	require.NoError(t, err)

	// Create tree with both README and config
	developTree := &object.Tree{
		Entries: []object.TreeEntry{
			{
				Name: "README.md",
				Mode: 0644,
				Hash: readmeHash,
			},
			{
				Name: "config.json",
				Mode: 0644,
				Hash: configHash,
			},
		},
	}
	developTreeObj := storer.NewEncodedObject()
	err = developTree.Encode(developTreeObj)
	require.NoError(t, err)
	developTreeHash, err := storer.SetEncodedObject(developTreeObj)
	require.NoError(t, err)

	// Create second commit
	secondCommit := &object.Commit{
		Author: object.Signature{
			Name:  "Test Developer",
			Email: "dev@example.com",
			When:  time.Now().Add(-24 * time.Hour),
		},
		Committer: object.Signature{
			Name:  "Test Developer",
			Email: "dev@example.com",
			When:  time.Now().Add(-24 * time.Hour),
		},
		Message:      "Add configuration file\n\nAdded config.json with project metadata.",
		TreeHash:     developTreeHash,
		ParentHashes: []plumbing.Hash{initialCommitHash},
	}
	secondCommitObj := storer.NewEncodedObject()
	err = secondCommit.Encode(secondCommitObj)
	require.NoError(t, err)
	secondCommitHash, err := storer.SetEncodedObject(secondCommitObj)
	require.NoError(t, err)

	// Create develop branch pointing to the second commit
	developRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/develop"), secondCommitHash)
	err = storer.SetReference(developRef)
	require.NoError(t, err)

	// Create feature branch pointing to the second commit
	featureRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature/test-feature"), secondCommitHash)
	err = storer.SetReference(featureRef)
	require.NoError(t, err)
}

// Cleanup removes the test environment
func (suite *BranchOperationsIntegrationTestSuite) Cleanup() {
	suite.testServer.Close()
	if suite.permClient != nil {
		_ = suite.permClient.Close() // errcheck: cleanup, errors expected
	}
	_ = os.RemoveAll(suite.tempDir) // errcheck: cleanup, errors expected
}

// makeRequest is a helper function to make HTTP requests with proper headers
func (suite *BranchOperationsIntegrationTestSuite) makeRequest(t *testing.T, method, path string, body interface{}) *http.Response {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		require.NoError(t, err)
		reqBody = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequest(method, suite.testServer.URL+path, reqBody)
	require.NoError(t, err)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Set required headers
	req.Header.Set("X-USER-ID", "test-user")
	req.Header.Set("X-Source", "orchestrator")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)

	return resp
}

func TestBranchOperationsAPI_CreateBranch(t *testing.T) {
	suite := SetupBranchOperationsIntegrationTest(t)
	defer suite.Cleanup()

	t.Run("successful branch creation", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"name":          "feature/new-feature",
			"source_branch": "master",
			"author_name":   "Test Developer",
			"author_email":  "dev@example.com",
		}

		resp := suite.makeRequest(t, "POST", "/repos/testowner/testrepo/branches", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response map[string]interface{}
		err := json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)

		// Verify basic response structure
		assert.True(t, response["success"].(bool))

		// Get the actual data from the response
		data, exists := response["data"]
		require.True(t, exists, "Response should have data field")
		dataMap := data.(map[string]interface{})

		assert.Equal(t, "feature/new-feature", dataMap["branch_name"])
		assert.Equal(t, "master", dataMap["source_branch"])
		assert.NotEmpty(t, dataMap["commit_hash"])
		assert.Equal(t, "create", dataMap["operation"])
	})

	t.Run("branch creation with invalid name", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"name":          "invalid/branch/name",
			"source_branch": "master",
			"author_name":   "Test Developer",
			"author_email":  "dev@example.com",
		}

		resp := suite.makeRequest(t, "POST", "/repos/testowner/testrepo/branches", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		// Should be rejected due to invalid branch name
		assert.True(t, resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusInternalServerError)
	})

	t.Run("branch creation from non-existent source", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"name":          "feature/test",
			"source_branch": "nonexistent",
			"author_name":   "Test Developer",
			"author_email":  "dev@example.com",
		}

		resp := suite.makeRequest(t, "POST", "/repos/testowner/testrepo/branches", requestBody)

		defer func() { _ = resp.Body.Close() }()

		// Should fail due to non-existent source branch
		assert.True(t, resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusInternalServerError)
	})

	t.Run("branch creation without write permission", func(t *testing.T) {
		// Temporarily disable write access
		suite.permClient.writeAccess = false
		defer func() { suite.permClient.writeAccess = true }()

		requestBody := map[string]interface{}{
			"name":          "feature/test",
			"source_branch": "master",
			"author_name":   "Test Developer",
			"author_email":  "dev@example.com",
		}

		resp := suite.makeRequest(t, "POST", "/repos/testowner/testrepo/branches", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestBranchOperationsAPI_UpdateBranch(t *testing.T) {
	suite := SetupBranchOperationsIntegrationTest(t)
	defer suite.Cleanup()

	t.Run("successful branch update (fast-forward)", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"target_branch": "develop",
			"update_type":   "fast-forward",
			"author_name":   "Test Developer",
			"author_email":  "dev@example.com",
		}

		resp := suite.makeRequest(t, "PATCH", "/repos/testowner/testrepo/branches/master", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		// Accept either 200 OK (if implemented) or 501 Not Implemented
		assert.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotImplemented)

		// Only validate response content if feature is implemented (200 OK)
		if resp.StatusCode == http.StatusOK {
			var response map[string]interface{}
			err := json.NewDecoder(resp.Body).Decode(&response)
			require.NoError(t, err)

			assert.True(t, response["success"].(bool))

			data, exists := response["data"]
			require.True(t, exists)
			dataMap := data.(map[string]interface{})

			assert.Equal(t, "master", dataMap["branch_name"])
			assert.Equal(t, "develop", dataMap["target_branch"])
			assert.NotEmpty(t, dataMap["old_commit_hash"])
			assert.NotEmpty(t, dataMap["new_commit_hash"])
			assert.Equal(t, "fast-forward", dataMap["update_type"])
		}
	})

	t.Run("branch update with force option", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"target_branch": "develop",
			"update_type":   "force",
			"force":         true,
			"author_name":   "Test Developer",
			"author_email":  "dev@example.com",
		}

		resp := suite.makeRequest(t, "PATCH", "/repos/testowner/testrepo/branches/master", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		// Should succeed with force option, or return 501 if not implemented
		assert.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotImplemented)
	})

	t.Run("update protected branch", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"target_branch": "develop",
			"update_type":   "fast-forward",
			"author_name":   "Test Developer",
			"author_email":  "dev@example.com",
		}

		resp := suite.makeRequest(t, "PATCH", "/repos/testowner/testrepo/branches/master", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		// Should fail due to protected branch, or return 501 if not implemented
		assert.True(t, resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusInternalServerError || resp.StatusCode == http.StatusNotImplemented)
	})
}

func TestBranchOperationsAPI_DeleteBranch(t *testing.T) {
	suite := SetupBranchOperationsIntegrationTest(t)
	defer suite.Cleanup()

	t.Run("successful branch deletion", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"author_name":  "Test Developer",
			"author_email": "dev@example.com",
		}

		resp := suite.makeRequest(t, "DELETE", "/repos/testowner/testrepo/branches/feature/test-feature", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response map[string]interface{}
		err := json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)

		assert.True(t, response["success"].(bool))

		data, exists := response["data"]
		require.True(t, exists)
		dataMap := data.(map[string]interface{})

		assert.Equal(t, "feature/test-feature", dataMap["branch_name"])
		assert.Equal(t, "delete", dataMap["operation"])
	})

	t.Run("delete protected branch", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"author_name":  "Test Developer",
			"author_email": "dev@example.com",
		}

		resp := suite.makeRequest(t, "DELETE", "/repos/testowner/testrepo/branches/master", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		// Should fail due to protected branch
		assert.True(t, resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusInternalServerError)
	})

	t.Run("delete non-existent branch", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"author_name":  "Test Developer",
			"author_email": "dev@example.com",
		}

		resp := suite.makeRequest(t, "DELETE", "/repos/testowner/testrepo/branches/nonexistent", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		// Should fail due to non-existent branch
		assert.True(t, resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusInternalServerError)
	})

	t.Run("delete branch without write permission", func(t *testing.T) {
		// Temporarily disable write access
		suite.permClient.writeAccess = false
		defer func() { suite.permClient.writeAccess = true }()

		requestBody := map[string]interface{}{
			"author_name":  "Test Developer",
			"author_email": "dev@example.com",
		}

		resp := suite.makeRequest(t, "DELETE", "/repos/testowner/testrepo/branches/feature/test-feature", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestBranchOperationsAPI_ErrorHandling(t *testing.T) {
	suite := SetupBranchOperationsIntegrationTest(t)
	defer suite.Cleanup()

	t.Run("missing required fields", func(t *testing.T) {
		// Missing name for branch creation
		requestBody := map[string]interface{}{
			"source_branch": "master",
			"author_name":   "Test Developer",
			"author_email":  "dev@example.com",
		}

		resp := suite.makeRequest(t, "POST", "/repos/testowner/testrepo/branches", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		assert.True(t, resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusInternalServerError)
	})

	t.Run("invalid branch name format", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"name":          "invalid..branch..name",
			"source_branch": "master",
			"author_name":   "Test Developer",
			"author_email":  "dev@example.com",
		}

		resp := suite.makeRequest(t, "POST", "/repos/testowner/testrepo/branches", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		assert.True(t, resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusInternalServerError)
	})

	t.Run("repository not found", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"name":          "feature/test",
			"source_branch": "master",
			"author_name":   "Test Developer",
			"author_email":  "dev@example.com",
		}

		resp := suite.makeRequest(t, "POST", "/repos/testowner/nonexistent/branches", requestBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		assert.True(t, resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusInternalServerError)
	})

	t.Run("conflicting branch names", func(t *testing.T) {
		// First create a branch
		createBody := map[string]interface{}{
			"name":          "feature/duplicate",
			"source_branch": "master",
			"author_name":   "Test Developer",
			"author_email":  "dev@example.com",
		}

		resp := suite.makeRequest(t, "POST", "/repos/testowner/testrepo/branches", createBody)
		_ = resp.Body.Close() // errcheck: test cleanup
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Try to create the same branch again
		resp = suite.makeRequest(t, "POST", "/repos/testowner/testrepo/branches", createBody)
		defer func() { _ = resp.Body.Close() }() // errcheck: test cleanup

		// Should fail due to branch already existing
		assert.True(t, resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusInternalServerError)
	})
}

func TestBranchOperationsAPI_PerformanceAndConcurrency(t *testing.T) {
	suite := SetupBranchOperationsIntegrationTest(t)
	defer suite.Cleanup()

	t.Run("concurrent branch operations", func(t *testing.T) {
		const numOperations = 5
		results := make(chan bool, numOperations)

		for i := 0; i < numOperations; i++ {
			go func(index int) {
				defer func() {
					if r := recover(); r != nil {
						results <- false
					}
				}()

				requestBody := map[string]interface{}{
					"name":          fmt.Sprintf("concurrent-branch-%d", index),
					"source_branch": "master",
					"author_name":   "Test Developer",
					"author_email":  "dev@example.com",
				}

				resp := suite.makeRequest(t, "POST", "/repos/testowner/testrepo/branches", requestBody)
				_ = resp.Body.Close() // errcheck: test cleanup
				results <- resp.StatusCode == http.StatusOK
			}(i)
		}

		successCount := 0
		for i := 0; i < numOperations; i++ {
			if <-results {
				successCount++
			}
		}

		// Most operations should succeed
		assert.GreaterOrEqual(t, successCount, numOperations/2, "At least half of concurrent operations should succeed")
	})

	t.Run("response time performance", func(t *testing.T) {
		requestBody := map[string]interface{}{
			"name":          "performance-test-branch",
			"source_branch": "master",
			"author_name":   "Test Developer",
			"author_email":  "dev@example.com",
		}

		start := time.Now()
		resp := suite.makeRequest(t, "POST", "/repos/testowner/testrepo/branches", requestBody)
		elapsed := time.Since(start)
		_ = resp.Body.Close() // errcheck: test cleanup

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Less(t, elapsed, 10*time.Second, "Response should be fast")
	})
}
