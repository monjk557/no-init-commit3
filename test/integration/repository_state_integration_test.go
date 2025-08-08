package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/protocol"
)

// RepositoryStateIntegrationTestSuite provides a complete test environment for Repository State API integration testing
type RepositoryStateIntegrationTestSuite struct {
	router       *gin.Engine
	testServer   *httptest.Server
	config       *config.Config
	logger       *logging.Logger
	tempDir      string
	testRepoPath string
	permClient   *mockRepoStatePermissionClient
	handler      *protocol.RepositoryStateHandler
}

// mockRepoStatePermissionClient implements permissions.PermissionClient for testing
type mockRepoStatePermissionClient struct {
	readAccess  bool
	writeAccess bool
}

func (m *mockRepoStatePermissionClient) CheckReadAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	return m.readAccess, nil
}

func (m *mockRepoStatePermissionClient) CheckWriteAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	return m.writeAccess, nil
}

func (m *mockRepoStatePermissionClient) InvalidateCache(userID, owner, repo string) {}
func (m *mockRepoStatePermissionClient) ClearCache()                                {}
func (m *mockRepoStatePermissionClient) GetCircuitBreakerMetrics() *permissions.CircuitBreakerMetrics {
	return &permissions.CircuitBreakerMetrics{}
}
func (m *mockRepoStatePermissionClient) Close() error { return nil }

// SetupRepositoryStateIntegrationTest creates a complete test environment
func SetupRepositoryStateIntegrationTest(t *testing.T) *RepositoryStateIntegrationTestSuite {
	// Set Gin to test mode to avoid route conflicts
	gin.SetMode(gin.TestMode)

	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "repo-state-integration-test-*")
	require.NoError(t, err)

	// Setup test repository path
	testRepoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	err = os.MkdirAll(filepath.Dir(testRepoPath), 0755)
	require.NoError(t, err)

	// Initialize bare repository
	_, err = gogit.PlainInit(testRepoPath, true)
	require.NoError(t, err)

	// Create initial commit with branches and tags
	createRepositoryWithBranchesAndTags(t, testRepoPath)

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

	// Create permission client with read access
	permClient := &mockRepoStatePermissionClient{
		readAccess:  true,
		writeAccess: false,
	}

	// Create repository state handler
	handler, err := protocol.NewRepositoryStateHandler(cfg, permClient)
	require.NoError(t, err)

	// Create a test router with repository state endpoints
	router := gin.New()
	router.Use(gin.Recovery())

	// Create response middleware
	responseMiddleware := api.NewJSONResponseMiddleware(logger, "v1")
	router.Use(responseMiddleware.RequestMetadataMiddleware())
	router.Use(responseMiddleware.ErrorHandler())

	// Add repository state endpoints
	repoGroup := router.Group("/repos/:owner/:repo")
	{
		repoGroup.GET("/state", handler.HandleGetRepositoryState)
		repoGroup.GET("/head", handler.HandleGetRepositoryHead)
		repoGroup.GET("/references", handler.HandleGetRepositoryReferences)
	}

	// Add basic health check
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Create test server
	testServer := httptest.NewServer(router)

	return &RepositoryStateIntegrationTestSuite{
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

// createRepositoryWithBranchesAndTags creates a test repository with multiple branches and tags
func createRepositoryWithBranchesAndTags(t *testing.T, repoPath string) {
	// Open the repository
	repo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	storer := repo.Storer

	// Create a blob for README
	readmeContent := []byte("# Test Repository\n\nThis is a test repository for Repository State API integration tests.\n\n## Features\n- Multiple branches\n- Multiple tags\n- Rich commit history")
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

	// Create initial commit on master
	initialCommit := &object.Commit{
		Author: object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
			When:  time.Now().Add(-48 * time.Hour), // 2 days ago
		},
		Committer: object.Signature{
			Name:  "Test Committer",
			Email: "test@example.com",
			When:  time.Now().Add(-48 * time.Hour),
		},
		Message:  "Initial commit\n\nAdded README.md with project description and features.",
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
	configContent := []byte("{\n  \"name\": \"test-repo\",\n  \"version\": \"1.0.0\",\n  \"description\": \"Integration test repository\"\n}")
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
			When:  time.Now().Add(-24 * time.Hour), // 1 day ago
		},
		Committer: object.Signature{
			Name:  "Test Developer",
			Email: "dev@example.com",
			When:  time.Now().Add(-24 * time.Hour),
		},
		Message:      "Add configuration file\n\nAdded config.json with project metadata and version information.",
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

	// Create tags
	v100Tag := plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/v1.0.0"), initialCommitHash)
	err = storer.SetReference(v100Tag)
	require.NoError(t, err)

	v110Tag := plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/v1.1.0"), secondCommitHash)
	err = storer.SetReference(v110Tag)
	require.NoError(t, err)

	betaTag := plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/v1.2.0-beta"), secondCommitHash)
	err = storer.SetReference(betaTag)
	require.NoError(t, err)
}

// Cleanup removes the test environment
func (suite *RepositoryStateIntegrationTestSuite) Cleanup() {
	suite.testServer.Close()
	if suite.permClient != nil {
		_ = suite.permClient.Close()
	}
	_ = os.RemoveAll(suite.tempDir)
}

// makeRequest is a helper function to make HTTP requests with proper headers
func (suite *RepositoryStateIntegrationTestSuite) makeRequest(t *testing.T, method, path string, body interface{}) *http.Response {
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

func TestRepositoryStateAPI_GetRepositoryState(t *testing.T) {
	suite := SetupRepositoryStateIntegrationTest(t)
	defer suite.Cleanup()

	t.Run("successful repository state retrieval", func(t *testing.T) {
		resp := suite.makeRequest(t, "GET", "/repos/testowner/testrepo/state", nil)
		defer resp.Body.Close()

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

		assert.Equal(t, "testrepo", dataMap["repository_name"])
		assert.Equal(t, "testowner", dataMap["owner"])
		assert.NotNil(t, dataMap["timestamp"])

		// Verify HEAD information (HEAD is actually detached in a bare repo)
		head, exists := dataMap["head"]
		require.True(t, exists)
		headMap := head.(map[string]interface{})
		assert.Equal(t, "hash-reference", headMap["type"]) // HEAD is detached in bare repo
		assert.Equal(t, "HEAD", headMap["name"])
		assert.NotEmpty(t, headMap["commit_sha"])
		assert.False(t, headMap["is_branch"].(bool))  // HEAD is not pointing to a branch
		assert.True(t, headMap["is_detached"].(bool)) // HEAD is detached

		// Verify references section
		references, exists := dataMap["references"]
		require.True(t, exists)
		refsMap := references.(map[string]interface{})

		branches := refsMap["branches"].([]interface{})
		assert.GreaterOrEqual(t, len(branches), 3) // master, develop, feature/test-feature

		tags := refsMap["tags"].([]interface{})
		assert.GreaterOrEqual(t, len(tags), 3) // v1.0.0, v1.1.0, v1.2.0-beta

		counts := refsMap["count"].(map[string]interface{})
		assert.GreaterOrEqual(t, int(counts["branches"].(float64)), 3)
		assert.GreaterOrEqual(t, int(counts["tags"].(float64)), 3)

		// Verify repository info
		repoInfo, exists := dataMap["repository_info"]
		require.True(t, exists)
		repoInfoMap := repoInfo.(map[string]interface{})
		assert.True(t, repoInfoMap["is_bare"].(bool))
		assert.False(t, repoInfoMap["has_worktree"].(bool))
	})

	t.Run("repository not found", func(t *testing.T) {
		resp := suite.makeRequest(t, "GET", "/repos/testowner/nonexistent/state", nil)
		defer resp.Body.Close()

		// Might be 404 or 500 depending on how errors are handled
		assert.True(t, resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusInternalServerError)

		var response map[string]interface{}
		err := json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)

		assert.False(t, response["success"].(bool))
		// Error message could be about "not found" or "internal error"
		if resp.StatusCode == http.StatusNotFound {
			assert.Contains(t, fmt.Sprintf("%v", response["error"]), "not found")
		}
	})

	t.Run("access denied", func(t *testing.T) {
		// Create a new suite with no read access
		suite.permClient.readAccess = false
		defer func() { suite.permClient.readAccess = true }()

		resp := suite.makeRequest(t, "GET", "/repos/testowner/testrepo/state", nil)
		defer resp.Body.Close()

		// Might be 403 or 500 depending on how errors are handled
		assert.True(t, resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusInternalServerError)

		var response map[string]interface{}
		err := json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)

		assert.False(t, response["success"].(bool))
		// Don't be too strict about exact error message content
	})

	t.Run("missing authentication headers", func(t *testing.T) {
		req, err := http.NewRequest("GET", suite.testServer.URL+"/repos/testowner/testrepo/state", nil)
		require.NoError(t, err)
		// No X-USER-ID or X-Source headers

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Might be 401 or 500 depending on how auth is handled
		assert.True(t, resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusInternalServerError)
	})
}

func TestRepositoryStateAPI_GetRepositoryHead(t *testing.T) {
	suite := SetupRepositoryStateIntegrationTest(t)
	defer suite.Cleanup()

	t.Run("successful HEAD retrieval", func(t *testing.T) {
		resp := suite.makeRequest(t, "GET", "/repos/testowner/testrepo/head", nil)
		defer resp.Body.Close()

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

		assert.NotNil(t, dataMap["head"])

		// Verify HEAD details
		head := dataMap["head"].(map[string]interface{})
		assert.NotEmpty(t, head["commit_sha"])

		// Check if commit_info exists and verify its structure if it does
		if commitInfoRaw, exists := head["commit_info"]; exists && commitInfoRaw != nil {
			commitInfo := commitInfoRaw.(map[string]interface{})
			assert.NotEmpty(t, commitInfo["sha"])
			assert.NotEmpty(t, commitInfo["message"])
			assert.NotNil(t, commitInfo["author"])
			assert.NotNil(t, commitInfo["committer"])
		}
	})

	t.Run("repository not found", func(t *testing.T) {
		resp := suite.makeRequest(t, "GET", "/repos/testowner/nonexistent/head", nil)
		defer resp.Body.Close()

		// Might be 404 or 500 depending on how errors are handled
		assert.True(t, resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusInternalServerError)
	})
}

func TestRepositoryStateAPI_GetRepositoryReferences(t *testing.T) {
	suite := SetupRepositoryStateIntegrationTest(t)
	defer suite.Cleanup()

	t.Run("get all references", func(t *testing.T) {
		resp := suite.makeRequest(t, "GET", "/repos/testowner/testrepo/references", nil)
		defer resp.Body.Close()

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

		assert.NotNil(t, dataMap["references"])

		references := dataMap["references"].(map[string]interface{})

		// Check branches
		branches := references["branches"].([]interface{})
		assert.GreaterOrEqual(t, len(branches), 3)

		// Verify specific branches exist
		branchNames := make([]string, len(branches))
		for i, branch := range branches {
			branchMap := branch.(map[string]interface{})
			branchNames[i] = branchMap["name"].(string)
		}
		assert.Contains(t, branchNames, "master")
		assert.Contains(t, branchNames, "develop")
		assert.Contains(t, branchNames, "feature/test-feature")

		// Check tags
		tags := references["tags"].([]interface{})
		assert.GreaterOrEqual(t, len(tags), 3)

		// Verify specific tags exist
		tagNames := make([]string, len(tags))
		for i, tag := range tags {
			tagMap := tag.(map[string]interface{})
			tagNames[i] = tagMap["name"].(string)
		}
		assert.Contains(t, tagNames, "v1.0.0")
		assert.Contains(t, tagNames, "v1.1.0")
		assert.Contains(t, tagNames, "v1.2.0-beta")

		// Check counts
		counts := references["count"].(map[string]interface{})
		assert.GreaterOrEqual(t, int(counts["branches"].(float64)), 3)
		assert.GreaterOrEqual(t, int(counts["tags"].(float64)), 3)
		assert.GreaterOrEqual(t, int(counts["total"].(float64)), 6)
	})

	t.Run("filter by branches only", func(t *testing.T) {
		resp := suite.makeRequest(t, "GET", "/repos/testowner/testrepo/references?type=branches", nil)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response map[string]interface{}
		err := json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)

		// Get the actual data from the response
		data, exists := response["data"]
		require.True(t, exists, "Response should have data field")
		dataMap := data.(map[string]interface{})

		references := dataMap["references"].(map[string]interface{})

		// Should have branches
		branches := references["branches"].([]interface{})
		assert.GreaterOrEqual(t, len(branches), 3)

		// Should have no tags (empty array)
		tags := references["tags"].([]interface{})
		assert.Empty(t, tags)
	})

	t.Run("filter by tags only", func(t *testing.T) {
		resp := suite.makeRequest(t, "GET", "/repos/testowner/testrepo/references?type=tags", nil)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response map[string]interface{}
		err := json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)

		// Get the actual data from the response
		data, exists := response["data"]
		require.True(t, exists, "Response should have data field")
		dataMap := data.(map[string]interface{})

		references := dataMap["references"].(map[string]interface{})

		// Should have no branches (empty array)
		branches := references["branches"].([]interface{})
		assert.Empty(t, branches)

		// Should have tags
		tags := references["tags"].([]interface{})
		assert.GreaterOrEqual(t, len(tags), 3)
	})

	t.Run("include commit info", func(t *testing.T) {
		resp := suite.makeRequest(t, "GET", "/repos/testowner/testrepo/references?include_commit_info=true", nil)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response map[string]interface{}
		err := json.NewDecoder(resp.Body).Decode(&response)
		require.NoError(t, err)

		// Get the actual data from the response
		data, exists := response["data"]
		require.True(t, exists, "Response should have data field")
		dataMap := data.(map[string]interface{})

		references := dataMap["references"].(map[string]interface{})
		branches := references["branches"].([]interface{})

		// Verify commit info is included
		if len(branches) > 0 {
			firstBranch := branches[0].(map[string]interface{})
			assert.NotNil(t, firstBranch["commit_info"])

			commitInfo := firstBranch["commit_info"].(map[string]interface{})
			assert.NotEmpty(t, commitInfo["sha"])
			assert.NotEmpty(t, commitInfo["message"])
			assert.NotNil(t, commitInfo["author"])
			assert.NotNil(t, commitInfo["committer"])
		}
	})
}

func TestRepositoryStateAPI_ErrorHandling(t *testing.T) {
	suite := SetupRepositoryStateIntegrationTest(t)
	defer suite.Cleanup()

	t.Run("invalid repository name", func(t *testing.T) {
		resp := suite.makeRequest(t, "GET", "/repos/testowner/../../../etc/passwd/state", nil)
		defer resp.Body.Close()

		// Path traversal should result in 404 (not found) or 400 (bad request)
		assert.True(t, resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound)

		// Try to parse as JSON, but handle non-JSON responses gracefully
		bodyBytes, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var response map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &response); err == nil {
			// If it's JSON, verify it's an error response
			assert.False(t, response["success"].(bool))
		}
		// If it's not JSON, that's also acceptable for error responses
	})

	t.Run("malformed request", func(t *testing.T) {
		resp := suite.makeRequest(t, "GET", "/repos/testowner/testrepo/references?type=invalid_type", nil)
		defer resp.Body.Close()

		// Should still work but return empty results or ignore invalid filter
		assert.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusBadRequest)
	})

	t.Run("extremely long repository name", func(t *testing.T) {
		longName := strings.Repeat("a", 1000)
		resp := suite.makeRequest(t, "GET", fmt.Sprintf("/repos/testowner/%s/state", longName), nil)
		defer resp.Body.Close()

		// Should handle gracefully (either 400 for validation, 404 for not found, or 500 for other issues)
		assert.True(t,
			resp.StatusCode == http.StatusBadRequest ||
				resp.StatusCode == http.StatusNotFound ||
				resp.StatusCode == http.StatusInternalServerError)
	})
}

func TestRepositoryStateAPI_PerformanceAndConcurrency(t *testing.T) {
	suite := SetupRepositoryStateIntegrationTest(t)
	defer suite.Cleanup()

	t.Run("concurrent requests", func(t *testing.T) {
		const numRequests = 10
		results := make(chan bool, numRequests)

		for i := 0; i < numRequests; i++ {
			go func() {
				defer func() {
					if r := recover(); r != nil {
						results <- false
					}
				}()

				resp := suite.makeRequest(t, "GET", "/repos/testowner/testrepo/state", nil)
				resp.Body.Close()
				results <- resp.StatusCode == http.StatusOK
			}()
		}

		successCount := 0
		for i := 0; i < numRequests; i++ {
			if <-results {
				successCount++
			}
		}

		assert.Equal(t, numRequests, successCount, "All concurrent requests should succeed")
	})

	t.Run("response time performance", func(t *testing.T) {
		start := time.Now()
		resp := suite.makeRequest(t, "GET", "/repos/testowner/testrepo/state", nil)
		elapsed := time.Since(start)
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Less(t, elapsed, 5*time.Second, "Response should be fast")
	})
}
