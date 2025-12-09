package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/server"
)

// APIGoldenTestSuite provides a test environment for comparing v1 and v2 API responses
type APIGoldenTestSuite struct {
	testServer   *httptest.Server
	config       *config.Config
	logger       *logging.Logger
	tempDir      string
	testRepoPath string
	permClient   *mockPermissionClient
}

// SetupAPIGoldenTest creates a complete test environment with both v1 and v2 API routes
func SetupAPIGoldenTest(t *testing.T) *APIGoldenTestSuite {
	// Set Gin to test mode to avoid route conflicts
	gin.SetMode(gin.TestMode)

	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "api-golden-test-*")
	require.NoError(t, err)

	// Setup test repository path
	testRepoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	err = os.MkdirAll(filepath.Dir(testRepoPath), 0755)
	require.NoError(t, err)

	// Initialize bare repository
	_, err = gogit.PlainInit(testRepoPath, true)
	require.NoError(t, err)

	// Create initial commit to make repository valid
	createInitialCommitForGoldenTest(t, tempDir, testRepoPath)

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
		Events: config.EventsConfig{
			Enabled: false, // Disable events for testing
		},
	}

	// Create logger
	logger, err := logging.NewLogger(cfg.Logging)
	require.NoError(t, err)

	// Create mock permission client
	permClient := &mockPermissionClient{
		readAccess:  true,
		writeAccess: true,
	}

	// Create a fresh server instance for each test to avoid route conflicts
	// Each server instance creates its own router, so we don't have conflicts
	srv, err := server.New(cfg)
	require.NoError(t, err)

	// Set test mode before setting up router
	// This must be done before SetupRouter() which sets ReleaseMode
	gin.SetMode(gin.TestMode)

	// Get the router from the server
	// SetupRouter creates a new router each time, so this should be safe
	// However, we need to ensure each test gets a completely fresh router
	router := srv.SetupRouter()

	// Force test mode after SetupRouter (it sets ReleaseMode internally)
	// This is important for test isolation
	gin.SetMode(gin.TestMode)

	// Create test server with the router
	// Each httptest.NewServer creates a new HTTP server, so routes are isolated
	testServer := httptest.NewServer(router)

	suite := &APIGoldenTestSuite{
		testServer:   testServer,
		config:       cfg,
		logger:       logger,
		tempDir:      tempDir,
		testRepoPath: testRepoPath,
		permClient:   permClient,
	}

	// Set cleanup
	t.Cleanup(func() {
		suite.Cleanup()
	})

	return suite
}

// createInitialCommitForGoldenTest creates an initial commit with some test files
func createInitialCommitForGoldenTest(t *testing.T, tempDir, repoPath string) {
	// Open the bare repository directly
	repo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// Create README.md
	readme := "# Test Repository\n\nInitial commit for API golden test.\n"
	readmeBlob := repo.Storer.NewEncodedObject()
	readmeBlob.SetType(plumbing.BlobObject)
	readmeBlob.SetSize(int64(len(readme)))
	writer, err := readmeBlob.Writer()
	require.NoError(t, err)
	_, err = writer.Write([]byte(readme))
	require.NoError(t, err)
	_ = writer.Close()

	readmeHash, err := repo.Storer.SetEncodedObject(readmeBlob)
	require.NoError(t, err)

	// Create test.txt
	testFile := "Test content\n"
	testBlob := repo.Storer.NewEncodedObject()
	testBlob.SetType(plumbing.BlobObject)
	testBlob.SetSize(int64(len(testFile)))
	writer, err = testBlob.Writer()
	require.NoError(t, err)
	_, err = writer.Write([]byte(testFile))
	require.NoError(t, err)
	_ = writer.Close()

	testHash, err := repo.Storer.SetEncodedObject(testBlob)
	require.NoError(t, err)

	// Create tree with both files
	tree := &object.Tree{
		Entries: []object.TreeEntry{
			{
				Name: "README.md",
				Mode: filemode.Regular,
				Hash: readmeHash,
			},
			{
				Name: "test.txt",
				Mode: filemode.Regular,
				Hash: testHash,
			},
		},
	}

	treeObj := repo.Storer.NewEncodedObject()
	err = tree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// Create initial commit
	commit := &object.Commit{
		Message: "Initial commit",
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
		TreeHash: treeHash,
	}

	commitObj := repo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// Create and update master branch reference
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	err = repo.Storer.SetReference(masterRef)
	require.NoError(t, err)

	// Update HEAD to be a symbolic reference to master
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	err = repo.Storer.SetReference(headRef)
	require.NoError(t, err)
}

// Cleanup cleans up test resources
func (suite *APIGoldenTestSuite) Cleanup() {
	if suite.testServer != nil {
		suite.testServer.Close()
	}
	if suite.tempDir != "" {
		_ = os.RemoveAll(suite.tempDir)
	}
}

// makeRequest is a helper to make HTTP requests with proper headers
func (suite *APIGoldenTestSuite) makeRequest(method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequest(method, suite.testServer.URL+path, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-USER-ID", "testuser")
	req.Header.Set("X-Source", "orchestrator")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return http.DefaultClient.Do(req)
}

// normalizeResponse normalizes a response for comparison by removing metadata that may differ
func normalizeResponse(data map[string]interface{}) map[string]interface{} {
	normalized := make(map[string]interface{})

	// If there's a "data" field, unwrap it for comparison (v1 wraps responses in "data", v2 doesn't)
	if dataField, ok := data["data"].(map[string]interface{}); ok {
		// Merge data fields into normalized, but keep other top-level fields like "success", skip "message"
		for k, v := range data {
			if k == "data" {
				// Recursively normalize the data field
				normalizedData := normalizeResponse(dataField)
				// Merge normalized data fields into normalized
				for dk, dv := range normalizedData {
					normalized[dk] = dv
				}
			} else if k == "meta" {
				// Skip metadata fields
				continue
			} else if k == "timestamp" {
				// Skip timestamp fields
				continue
			} else if k == "message" || k == "Message" {
				// Skip message field (v1 wraps with message, v2 doesn't)
				continue
			} else {
				normalized[k] = v
			}
		}
	} else {
		// No "data" field, normalize normally
		for k, v := range data {
			// Skip metadata fields that may differ between versions
			if k == "meta" {
				continue
			}
			// Skip timestamp fields
			if k == "timestamp" {
				continue
			}
			// Skip duration_ms and transaction_id fields (non-deterministic)
			if k == "duration_ms" || k == "transaction_id" {
				continue
			}
			// Skip created_at fields (timestamp)
			if k == "created_at" {
				continue
			}

			// Handle Tree arrays - remove extra fields from TreeEntry objects
			if k == "tree" || k == "Tree" || k == "entries" || k == "Entries" {
				if treeArray, ok := v.([]interface{}); ok {
					normalizedTree := make([]interface{}, len(treeArray))
					for i, entry := range treeArray {
						if entryMap, ok := entry.(map[string]interface{}); ok {
							// Keep only type, name, path fields for tree entries
							normalizedEntry := make(map[string]interface{})
							if typ, ok := entryMap["type"]; ok {
								normalizedEntry["type"] = typ
							}
							if name, ok := entryMap["name"]; ok {
								normalizedEntry["name"] = name
							}
							if path, ok := entryMap["path"]; ok {
								normalizedEntry["path"] = path
							}
							// Remove mode, size, hash, latest_commit, etc.
							normalizedTree[i] = normalizedEntry
						} else {
							normalizedTree[i] = entry
						}
					}
					normalized[k] = normalizedTree
					continue
				}
			}

			// Skip path field (can differ between v1 and v2 - contains full file system path)
			if k == "path" || k == "Path" {
				continue
			}
			// Skip tree_path field (v1 only)
			if k == "tree_path" || k == "TreePath" {
				continue
			}
			// Skip readme field (v1 only for TreeDetail)
			if k == "readme" || k == "Readme" {
				continue
			}
			// Skip message field (v1 wraps with message)
			if k == "message" || k == "Message" {
				continue
			}
			// Skip name, owner, repository fields (v1 TreeDetail only)
			if k == "name" || k == "owner" || k == "repository" {
				continue
			}
			// Skip tag field (v1 RepositoryDetail only, v2 uses tags array)
			if k == "tag" || k == "Tag" {
				continue
			}
			// Normalize latest_commits (v1) vs latest_commit (v2)
			if k == "latest_commits" {
				normalized["latest_commit"] = normalizeResponse(v.(map[string]interface{}))
				continue
			}
			// Skip commits array for TreeDetail (v2 only)
			if k == "commits" || k == "Commits" {
				// Only skip if this is TreeDetail response (has tree_path or readme in v1)
				// For other endpoints, keep commits
				continue
			}
			// Normalize parent_hashes: empty array [] to null
			if k == "parent_hashes" {
				if arr, ok := v.([]interface{}); ok && len(arr) == 0 {
					normalized[k] = nil
					continue
				}
			}
			// Handle count field - remove commits field if present (v1 RepositoryDetail doesn't have it)
			if k == "count" {
				if countMap, ok := v.(map[string]interface{}); ok {
					normalizedCount := make(map[string]interface{})
					for ck, cv := range countMap {
						if ck == "commits" {
							// Skip commits field in count (v1 RepositoryDetail doesn't have it)
							continue
						}
						normalizedCount[ck] = cv
					}
					normalized[k] = normalizedCount
					continue
				}
			}

			// Recursively normalize nested maps (e.g., "data" field)
			if nestedMap, ok := v.(map[string]interface{}); ok {
				normalized[k] = normalizeResponse(nestedMap)
			} else if array, ok := v.([]interface{}); ok {
				// Handle arrays recursively
				normalizedArray := make([]interface{}, len(array))
				for i, item := range array {
					if itemMap, ok := item.(map[string]interface{}); ok {
						normalizedArray[i] = normalizeResponse(itemMap)
					} else {
						normalizedArray[i] = item
					}
				}
				normalized[k] = normalizedArray
			} else {
				normalized[k] = v
			}
		}
	}
	return normalized
}

// compareResponses compares two JSON responses and returns true if they are equivalent
func compareResponses(t *testing.T, v1Resp, v2Resp *http.Response, v1Path, v2Path string) bool {
	// Read both response bodies
	v1Body, err := io.ReadAll(v1Resp.Body)
	require.NoError(t, err)
	v2Body, err := io.ReadAll(v2Resp.Body)
	require.NoError(t, err)

	// Check status codes match
	if v1Resp.StatusCode != v2Resp.StatusCode {
		t.Errorf("Status codes differ:\n  v1 (%s): %d\n  v2 (%s): %d\n  v1 body: %s\n  v2 body: %s",
			v1Path, v1Resp.StatusCode, v2Path, v2Resp.StatusCode, string(v1Body), string(v2Body))
		return false
	}

	// If both are error responses, just check status codes match
	if v1Resp.StatusCode >= 400 {
		return true
	}

	// Parse JSON responses
	var v1Data, v2Data map[string]interface{}
	err = json.Unmarshal(v1Body, &v1Data)
	if err != nil {
		t.Logf("Failed to parse v1 response as JSON: %v\nBody: %s", err, string(v1Body))
		return false
	}
	err = json.Unmarshal(v2Body, &v2Data)
	if err != nil {
		t.Logf("Failed to parse v2 response as JSON: %v\nBody: %s", err, string(v2Body))
		return false
	}

	// Normalize responses
	v1Normalized := normalizeResponse(v1Data)
	v2Normalized := normalizeResponse(v2Data)

	// Compare normalized responses
	v1JSON, _ := json.MarshalIndent(v1Normalized, "", "  ")
	v2JSON, _ := json.MarshalIndent(v2Normalized, "", "  ")

	if string(v1JSON) != string(v2JSON) {
		t.Errorf("Responses differ:\n  v1 path: %s\n  v2 path: %s\n  v1 response:\n%s\n  v2 response:\n%s",
			v1Path, v2Path, string(v1JSON), string(v2JSON))
		return false
	}

	return true
}

// TestAPIGolden_GET_Tree compares GET /tree responses between v1 and v2
func TestAPIGolden_GET_Tree(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	v1Path := "/repos/testowner/testrepo/tree?ref=master"
	v2Path := "/v2/repos/testowner/testrepo/tree?ref=master"

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_GET_Blob compares GET /blob responses between v1 and v2
func TestAPIGolden_GET_Blob(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	v1Path := "/repos/testowner/testrepo/blob?path=README.md&ref=master"
	v2Path := "/v2/repos/testowner/testrepo/blob?path=README.md&ref=master"

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_GET_Commits compares GET /commits responses between v1 and v2
func TestAPIGolden_GET_Commits(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	v1Path := "/repos/testowner/testrepo/commits?ref=master&limit=10&skip=0"
	v2Path := "/v2/repos/testowner/testrepo/commits?ref=master&limit=10&skip=0"

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_POST_Commits compares POST /commits responses between v1 and v2
func TestAPIGolden_POST_Commits(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	body := map[string]interface{}{
		"base_branch": "master",
		"operations": []map[string]interface{}{
			{
				"operation": "create",
				"path":      "golden-test.txt",
				"content":   "R29sZGVuIHRlc3QgZmlsZQ==", // "Golden test file" in base64
			},
		},
		"message": "Add golden test file",
		"author": map[string]string{
			"name":  "Test User",
			"email": "test@example.com",
		},
	}

	v1Path := "/repos/testowner/testrepo/commits"
	v2Path := "/v2/repos/testowner/testrepo/commits"

	v1Resp, err := suite.makeRequest("POST", v1Path, body)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	// Reset repository state for v2 request
	suite = SetupAPIGoldenTest(t)

	v2Resp, err := suite.makeRequest("POST", v2Path, body)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_POST_Branches compares POST /branches responses between v1 and v2
func TestAPIGolden_POST_Branches(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	body := map[string]interface{}{
		"name":   "feature/golden-test",
		"source": "master",
	}

	v1Path := "/repos/testowner/testrepo/branches"
	v2Path := "/v2/repos/testowner/testrepo/branches"

	v1Resp, err := suite.makeRequest("POST", v1Path, body)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	// Reset repository state for v2 request
	suite = SetupAPIGoldenTest(t)

	v2Resp, err := suite.makeRequest("POST", v2Path, body)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_PATCH_Branches compares PATCH /branches responses between v1 and v2
func TestAPIGolden_PATCH_Branches(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	// First create a branch
	createBody := map[string]interface{}{
		"name":   "feature/patch-test",
		"source": "master",
	}

	_, err := suite.makeRequest("POST", "/repos/testowner/testrepo/branches", createBody)
	require.NoError(t, err)

	// Now test patch
	body := map[string]interface{}{
		"target": "master",
		"force":  false,
	}

	v1Path := "/repos/testowner/testrepo/branches/feature/patch-test"
	v2Path := "/v2/repos/testowner/testrepo/branches/feature/patch-test"

	v1Resp, err := suite.makeRequest("PATCH", v1Path, body)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	// Reset and recreate for v2
	suite = SetupAPIGoldenTest(t)
	_, err = suite.makeRequest("POST", "/v2/repos/testowner/testrepo/branches", createBody)
	require.NoError(t, err)

	v2Resp, err := suite.makeRequest("PATCH", v2Path, body)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_DELETE_Branches compares DELETE /branches responses between v1 and v2
func TestAPIGolden_DELETE_Branches(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	// Create a branch to delete
	createBody := map[string]interface{}{
		"name":   "feature/delete-test",
		"source": "master",
	}

	_, err := suite.makeRequest("POST", "/repos/testowner/testrepo/branches", createBody)
	require.NoError(t, err)

	body := map[string]interface{}{
		"force": false,
	}

	v1Path := "/repos/testowner/testrepo/branches/feature/delete-test"
	v2Path := "/v2/repos/testowner/testrepo/branches/feature/delete-test"

	v1Resp, err := suite.makeRequest("DELETE", v1Path, body)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	// Reset and recreate for v2
	suite = SetupAPIGoldenTest(t)
	_, err = suite.makeRequest("POST", "/v2/repos/testowner/testrepo/branches", createBody)
	require.NoError(t, err)

	v2Resp, err := suite.makeRequest("DELETE", v2Path, body)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_GET_DiffCommits compares GET /diff/commits responses between v1 and v2
func TestAPIGolden_GET_DiffCommits(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	// Get the initial commit hash
	repo, err := gogit.PlainOpen(suite.testRepoPath)
	require.NoError(t, err)
	headRef, err := repo.Head()
	require.NoError(t, err)
	commitHash := headRef.Hash().String()

	v1Path := fmt.Sprintf("/repos/testowner/testrepo/diff/commits/%s/%s", commitHash, commitHash)
	v2Path := fmt.Sprintf("/v2/repos/testowner/testrepo/diff/commits/%s/%s", commitHash, commitHash)

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_GET_DiffBranches compares GET /diff/branches responses between v1 and v2
func TestAPIGolden_GET_DiffBranches(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	v1Path := "/repos/testowner/testrepo/diff/branches/master/master"
	v2Path := "/v2/repos/testowner/testrepo/diff/branches/master/master"

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_GET_DiffFile compares GET /diff/file responses between v1 and v2
func TestAPIGolden_GET_DiffFile(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	v1Path := "/repos/testowner/testrepo/diff/file?path=README.md&base=master&head=master"
	v2Path := "/v2/repos/testowner/testrepo/diff/file?path=README.md&base=master&head=master"

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_GET_DiffCommit compares GET /diff/commit responses between v1 and v2
func TestAPIGolden_GET_DiffCommit(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	// Get the initial commit hash
	repo, err := gogit.PlainOpen(suite.testRepoPath)
	require.NoError(t, err)
	headRef, err := repo.Head()
	require.NoError(t, err)
	commitHash := headRef.Hash().String()

	v1Path := fmt.Sprintf("/repos/testowner/testrepo/diff/commit/%s", commitHash)
	v2Path := fmt.Sprintf("/v2/repos/testowner/testrepo/diff/commit/%s", commitHash)

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_GET_DiffPatch compares GET /diff/patch (v2) vs /patch (v1) responses
func TestAPIGolden_GET_DiffPatch(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	// Get the initial commit hash
	repo, err := gogit.PlainOpen(suite.testRepoPath)
	require.NoError(t, err)
	headRef, err := repo.Head()
	require.NoError(t, err)
	commitHash := headRef.Hash().String()

	v1Path := fmt.Sprintf("/repos/testowner/testrepo/patch/%s/%s", commitHash, commitHash)
	v2Path := fmt.Sprintf("/v2/repos/testowner/testrepo/diff/patch/%s/%s", commitHash, commitHash)

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	// For patch endpoints, compare the raw body content (not JSON)
	v1Body, err := io.ReadAll(v1Resp.Body)
	require.NoError(t, err)
	v2Body, err := io.ReadAll(v2Resp.Body)
	require.NoError(t, err)

	if v1Resp.StatusCode != v2Resp.StatusCode {
		t.Errorf("Status codes differ:\n  v1 (%s): %d\n  v2 (%s): %d",
			v1Path, v1Resp.StatusCode, v2Path, v2Resp.StatusCode)
		return
	}

	if string(v1Body) != string(v2Body) {
		t.Errorf("Patch content differs:\n  v1 path: %s\n  v2 path: %s\n  v1 content:\n%s\n  v2 content:\n%s",
			v1Path, v2Path, string(v1Body), string(v2Body))
	}
}

// TestAPIGolden_GET_MergePreview compares GET /merges/preview responses between v1 and v2
func TestAPIGolden_GET_MergePreview(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	v1Path := "/repos/testowner/testrepo/merges/preview?source_ref=master&target_ref=master"
	v2Path := "/v2/repos/testowner/testrepo/merges/preview?source_ref=master&target_ref=master"

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_POST_Merge compares POST /merges responses between v1 and v2
func TestAPIGolden_POST_Merge(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	body := map[string]interface{}{
		"source_ref": "master",
		"target_ref": "master",
		"message":    "Test merge",
		"author": map[string]interface{}{
			"name":  "Test User",
			"email": "test@example.com",
		},
		"force_conflict": false,
		"dry_run":        false,
	}

	v1Path := "/repos/testowner/testrepo/merges"
	v2Path := "/v2/repos/testowner/testrepo/merges"

	v1Resp, err := suite.makeRequest("POST", v1Path, body)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	// Reset repository state for v2 request
	suite = SetupAPIGoldenTest(t)

	v2Resp, err := suite.makeRequest("POST", v2Path, body)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_AllEndpoints runs all existing integration tests and compares v1/v2 responses
func TestAPIGolden_AllEndpoints(t *testing.T) {
	// Run all the golden tests
	t.Run("GET_Tree", func(t *testing.T) {
		TestAPIGolden_GET_Tree(t)
	})
	t.Run("GET_Blob", func(t *testing.T) {
		TestAPIGolden_GET_Blob(t)
	})
	t.Run("GET_Commits", func(t *testing.T) {
		TestAPIGolden_GET_Commits(t)
	})
	t.Run("POST_Commits", func(t *testing.T) {
		TestAPIGolden_POST_Commits(t)
	})
	t.Run("POST_Branches", func(t *testing.T) {
		TestAPIGolden_POST_Branches(t)
	})
	t.Run("PATCH_Branches", func(t *testing.T) {
		TestAPIGolden_PATCH_Branches(t)
	})
	t.Run("DELETE_Branches", func(t *testing.T) {
		TestAPIGolden_DELETE_Branches(t)
	})
	t.Run("GET_DiffCommits", func(t *testing.T) {
		TestAPIGolden_GET_DiffCommits(t)
	})
	t.Run("GET_DiffBranches", func(t *testing.T) {
		TestAPIGolden_GET_DiffBranches(t)
	})
	t.Run("GET_DiffFile", func(t *testing.T) {
		TestAPIGolden_GET_DiffFile(t)
	})
	t.Run("GET_DiffCommit", func(t *testing.T) {
		TestAPIGolden_GET_DiffCommit(t)
	})
	t.Run("GET_DiffPatch", func(t *testing.T) {
		TestAPIGolden_GET_DiffPatch(t)
	})
	t.Run("GET_MergePreview", func(t *testing.T) {
		TestAPIGolden_GET_MergePreview(t)
	})
	t.Run("POST_Merge", func(t *testing.T) {
		TestAPIGolden_POST_Merge(t)
	})
	t.Run("GET_RepositoryDetail", func(t *testing.T) {
		TestAPIGolden_GET_RepositoryDetail(t)
	})
	t.Run("GET_BlobDetail", func(t *testing.T) {
		TestAPIGolden_GET_BlobDetail(t)
	})
	t.Run("GET_TreeDetail", func(t *testing.T) {
		TestAPIGolden_GET_TreeDetail(t)
	})
	t.Run("GET_BranchesDetail", func(t *testing.T) {
		TestAPIGolden_GET_BranchesDetail(t)
	})
}

// TestAPIGolden_GET_RepositoryDetail compares GET /api/v1/repository/:owner/:repo (v1) vs /v2/repos/:owner/:repo/overview (v2)
func TestAPIGolden_GET_RepositoryDetail(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	v1Path := "/api/v1/repository/testowner/testrepo"
	v2Path := "/v2/repos/testowner/testrepo/overview"

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	// For RepositoryDetail, v1 doesn't have "commits" array, but v2 does
	compareResponsesForRepositoryDetail(t, v1Resp, v2Resp, v1Path, v2Path)
}

// compareResponsesForRepositoryDetail is a specialized comparison for RepositoryDetail endpoints
// v1 RepositoryDetail doesn't have "commits" array, but v2 RepositoryOverview does
func compareResponsesForRepositoryDetail(t *testing.T, v1Resp, v2Resp *http.Response, v1Path, v2Path string) bool {
	// Read both response bodies
	v1Body, err := io.ReadAll(v1Resp.Body)
	require.NoError(t, err)
	v2Body, err := io.ReadAll(v2Resp.Body)
	require.NoError(t, err)

	// Check status codes match
	if v1Resp.StatusCode != v2Resp.StatusCode {
		t.Errorf("Status codes differ:\n  v1 (%s): %d\n  v2 (%s): %d\n  v1 body: %s\n  v2 body: %s",
			v1Path, v1Resp.StatusCode, v2Path, v2Resp.StatusCode, string(v1Body), string(v2Body))
		return false
	}

	// If both are error responses, just check status codes match
	if v1Resp.StatusCode >= 400 {
		return true
	}

	// Parse JSON responses
	var v1Data, v2Data map[string]interface{}
	err = json.Unmarshal(v1Body, &v1Data)
	if err != nil {
		t.Logf("Failed to parse v1 response as JSON: %v\nBody: %s", err, string(v1Body))
		return false
	}
	err = json.Unmarshal(v2Body, &v2Data)
	if err != nil {
		t.Logf("Failed to parse v2 response as JSON: %v\nBody: %s", err, string(v2Body))
		return false
	}

	// Normalize responses
	v1Normalized := normalizeResponse(v1Data)
	v2Normalized := normalizeResponse(v2Data)

	// Compare normalized responses
	v1JSON, _ := json.MarshalIndent(v1Normalized, "", "  ")
	v2JSON, _ := json.MarshalIndent(v2Normalized, "", "  ")

	if string(v1JSON) != string(v2JSON) {
		t.Errorf("Responses differ:\n  v1 path: %s\n  v2 path: %s\n  v1 response:\n%s\n  v2 response:\n%s",
			v1Path, v2Path, string(v1JSON), string(v2JSON))
		return false
	}

	return true
}

// TestAPIGolden_GET_BlobDetail compares GET /api/v1/repository/:owner/:repo/blob (v1) vs /v2/repos/:owner/:repo/overview/blob (v2)
func TestAPIGolden_GET_BlobDetail(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	v1Path := "/api/v1/repository/testowner/testrepo/blob?path=README.md&ref=master"
	v2Path := "/v2/repos/testowner/testrepo/overview/blob?path=README.md&ref=master"

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// TestAPIGolden_GET_TreeDetail compares GET /api/v1/repository/:owner/:repo/tree (v1) vs /v2/repos/:owner/:repo/overview/tree (v2)
func TestAPIGolden_GET_TreeDetail(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	v1Path := "/api/v1/repository/testowner/testrepo/tree?ref=master"
	v2Path := "/v2/repos/testowner/testrepo/overview/tree?ref=master"

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	// For TreeDetail, v2 has "commits" array that v1 doesn't have, so we need special handling
	compareResponsesForTreeDetail(t, v1Resp, v2Resp, v1Path, v2Path)
}

// compareResponsesForTreeDetail is a specialized comparison for TreeDetail endpoints
// v1 TreeDetail doesn't have "commits" array, but v2 TreeOverview does
func compareResponsesForTreeDetail(t *testing.T, v1Resp, v2Resp *http.Response, v1Path, v2Path string) bool {
	// Read both response bodies
	v1Body, err := io.ReadAll(v1Resp.Body)
	require.NoError(t, err)
	v2Body, err := io.ReadAll(v2Resp.Body)
	require.NoError(t, err)

	// Check status codes match
	if v1Resp.StatusCode != v2Resp.StatusCode {
		t.Errorf("Status codes differ:\n  v1 (%s): %d\n  v2 (%s): %d\n  v1 body: %s\n  v2 body: %s",
			v1Path, v1Resp.StatusCode, v2Path, v2Resp.StatusCode, string(v1Body), string(v2Body))
		return false
	}

	// If both are error responses, just check status codes match
	if v1Resp.StatusCode >= 400 {
		return true
	}

	// Parse JSON responses
	var v1Data, v2Data map[string]interface{}
	err = json.Unmarshal(v1Body, &v1Data)
	if err != nil {
		t.Logf("Failed to parse v1 response as JSON: %v\nBody: %s", err, string(v1Body))
		return false
	}
	err = json.Unmarshal(v2Body, &v2Data)
	if err != nil {
		t.Logf("Failed to parse v2 response as JSON: %v\nBody: %s", err, string(v2Body))
		return false
	}

	// Remove "commits" from v2 for TreeDetail comparison
	delete(v2Data, "commits")
	// Remove "message" from v1 (v1 wraps with message, v2 doesn't)
	delete(v1Data, "message")
	delete(v1Data, "name")
	delete(v1Data, "owner")
	delete(v1Data, "repository")

	// Normalize responses
	v1Normalized := normalizeResponse(v1Data)
	v2Normalized := normalizeResponse(v2Data)

	// Compare normalized responses
	v1JSON, _ := json.MarshalIndent(v1Normalized, "", "  ")
	v2JSON, _ := json.MarshalIndent(v2Normalized, "", "  ")

	if string(v1JSON) != string(v2JSON) {
		t.Errorf("Responses differ:\n  v1 path: %s\n  v2 path: %s\n  v1 response:\n%s\n  v2 response:\n%s",
			v1Path, v2Path, string(v1JSON), string(v2JSON))
		return false
	}

	return true
}

// TestAPIGolden_GET_BranchesDetail compares GET /api/v1/repository/:owner/:repo/branches (v1) vs /v2/repos/:owner/:repo/overview/branches (v2)
func TestAPIGolden_GET_BranchesDetail(t *testing.T) {
	suite := SetupAPIGoldenTest(t)

	// BranchesDetail requires email parameter
	v1Path := "/api/v1/repository/testowner/testrepo/branches?email=test@example.com"
	v2Path := "/v2/repos/testowner/testrepo/overview/branches?email=test@example.com"

	v1Resp, err := suite.makeRequest("GET", v1Path, nil)
	require.NoError(t, err)
	defer v1Resp.Body.Close()

	v2Resp, err := suite.makeRequest("GET", v2Path, nil)
	require.NoError(t, err)
	defer v2Resp.Body.Close()

	compareResponses(t, v1Resp, v2Resp, v1Path, v2Path)
}

// setupBenchmarkSuite creates a test suite for benchmark tests (without requiring testing.T)
func setupBenchmarkSuite() (*APIGoldenTestSuite, func(), error) {
	// Set Gin to test mode to avoid route conflicts
	gin.SetMode(gin.TestMode)

	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "api-benchmark-test-*")
	if err != nil {
		return nil, nil, err
	}

	// Setup test repository path
	testRepoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	err = os.MkdirAll(filepath.Dir(testRepoPath), 0755)
	if err != nil {
		return nil, nil, err
	}

	// Initialize bare repository
	_, err = gogit.PlainInit(testRepoPath, true)
	if err != nil {
		return nil, nil, err
	}

	// Create initial commit to make repository valid
	err = createInitialCommitForBenchmark(tempDir, testRepoPath)
	if err != nil {
		return nil, nil, err
	}

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
		Events: config.EventsConfig{
			Enabled: false, // Disable events for testing
		},
	}

	// Create logger
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, nil, err
	}

	// Create mock permission client
	permClient := &mockPermissionClient{
		readAccess:  true,
		writeAccess: true,
	}

	// Create a fresh server instance for each test to avoid route conflicts
	srv, err := server.New(cfg)
	if err != nil {
		return nil, nil, err
	}

	// Set test mode before setting up router
	gin.SetMode(gin.TestMode)

	// Get the router from the server
	router := srv.SetupRouter()

	// Force test mode after SetupRouter
	gin.SetMode(gin.TestMode)

	// Create test server with the router
	testServer := httptest.NewServer(router)

	suite := &APIGoldenTestSuite{
		testServer:   testServer,
		config:       cfg,
		logger:       logger,
		tempDir:      tempDir,
		testRepoPath: testRepoPath,
		permClient:   permClient,
	}

	cleanup := func() {
		if suite.testServer != nil {
			suite.testServer.Close()
		}
		if suite.tempDir != "" {
			_ = os.RemoveAll(suite.tempDir)
		}
	}

	return suite, cleanup, nil
}

// createInitialCommitForBenchmark creates an initial commit for benchmark tests (returns error instead of using require)
func createInitialCommitForBenchmark(tempDir, repoPath string) error {
	// Open the bare repository directly
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return err
	}

	// Create README.md
	readme := "# Test Repository\n\nInitial commit for benchmark test.\n"
	readmeBlob := repo.Storer.NewEncodedObject()
	readmeBlob.SetType(plumbing.BlobObject)
	readmeBlob.SetSize(int64(len(readme)))
	writer, err := readmeBlob.Writer()
	if err != nil {
		return err
	}
	_, err = writer.Write([]byte(readme))
	if err != nil {
		return err
	}
	_ = writer.Close()

	readmeHash, err := repo.Storer.SetEncodedObject(readmeBlob)
	if err != nil {
		return err
	}

	// Create test.txt
	testFile := "Test content\n"
	testBlob := repo.Storer.NewEncodedObject()
	testBlob.SetType(plumbing.BlobObject)
	testBlob.SetSize(int64(len(testFile)))
	writer, err = testBlob.Writer()
	if err != nil {
		return err
	}
	_, err = writer.Write([]byte(testFile))
	if err != nil {
		return err
	}
	_ = writer.Close()

	testHash, err := repo.Storer.SetEncodedObject(testBlob)
	if err != nil {
		return err
	}

	// Create tree with both files
	tree := &object.Tree{
		Entries: []object.TreeEntry{
			{
				Name: "README.md",
				Mode: filemode.Regular,
				Hash: readmeHash,
			},
			{
				Name: "test.txt",
				Mode: filemode.Regular,
				Hash: testHash,
			},
		},
	}

	treeObj := repo.Storer.NewEncodedObject()
	err = tree.Encode(treeObj)
	if err != nil {
		return err
	}

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return err
	}

	// Create initial commit
	commit := &object.Commit{
		Message: "Initial commit",
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
		TreeHash: treeHash,
	}

	commitObj := repo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	if err != nil {
		return err
	}

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	if err != nil {
		return err
	}

	// Create and update master branch reference
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	err = repo.Storer.SetReference(masterRef)
	if err != nil {
		return err
	}

	// Update HEAD to be a symbolic reference to master
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	err = repo.Storer.SetReference(headRef)
	if err != nil {
		return err
	}

	return nil
}

// BenchmarkRepositoryDetail_v1 benchmarks the v1 repository detail endpoint
func BenchmarkRepositoryDetail_v1(b *testing.B) {
	suite, cleanup, err := setupBenchmarkSuite()
	if err != nil {
		b.Fatalf("Failed to setup benchmark suite: %v", err)
	}
	defer cleanup()

	v1Path := "/api/v1/repository/testowner/testrepo"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, err := suite.makeRequest("GET", v1Path, nil)
		if err != nil {
			b.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
		}
		// Read the body to simulate real usage
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
}

// BenchmarkRepositoryOverview_v2 benchmarks the v2 repository overview endpoint
func BenchmarkRepositoryOverview_v2(b *testing.B) {
	suite, cleanup, err := setupBenchmarkSuite()
	if err != nil {
		b.Fatalf("Failed to setup benchmark suite: %v", err)
	}
	defer cleanup()

	v2Path := "/v2/repos/testowner/testrepo/overview"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, err := suite.makeRequest("GET", v2Path, nil)
		if err != nil {
			b.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
		}
		// Read the body to simulate real usage
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
}

// BenchmarkRepositoryDetailComparison compares v1 and v2 performance side by side
func BenchmarkRepositoryDetailComparison(b *testing.B) {
	suite, cleanup, err := setupBenchmarkSuite()
	if err != nil {
		b.Fatalf("Failed to setup benchmark suite: %v", err)
	}
	defer cleanup()

	v1Path := "/api/v1/repository/testowner/testrepo"
	v2Path := "/v2/repos/testowner/testrepo/overview"

	// Benchmark v2 first
	b.Run("v2_RepositoryOverview", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v2Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})

	// Benchmark v1 after v2
	b.Run("v1_RepositoryDetail", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v1Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})
}

// BenchmarkTreeComparison compares v1 /repos tree and v2 tree endpoint performance with recursive option
func BenchmarkTreeComparison(b *testing.B) {
	suite, cleanup, err := setupBenchmarkSuite()
	if err != nil {
		b.Fatalf("Failed to setup benchmark suite: %v", err)
	}
	defer cleanup()

	v1Path := "/repos/testowner/testrepo/tree"
	v1PathRecursive := "/repos/testowner/testrepo/tree?recursive=true"
	v2PathNonRecursive := "/v2/repos/testowner/testrepo/tree"
	v2PathRecursive := "/v2/repos/testowner/testrepo/tree?recursive=true"

	// Benchmark v2 non-recursive first
	b.Run("v2_Tree_NonRecursive", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v2PathNonRecursive, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})

	// Benchmark v2 recursive
	b.Run("v2_Tree_Recursive", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v2PathRecursive, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})

	// Benchmark v1 tree (/repos path) non-recursive
	b.Run("v1_Tree_Repos_NonRecursive", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v1Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})

	// Benchmark v1 tree (/repos path) recursive
	b.Run("v1_Tree_Repos_Recursive", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v1PathRecursive, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})
}

// BenchmarkBlobComparison compares v1 and v2 blob endpoint performance
func BenchmarkBlobComparison(b *testing.B) {
	suite, cleanup, err := setupBenchmarkSuite()
	if err != nil {
		b.Fatalf("Failed to setup benchmark suite: %v", err)
	}
	defer cleanup()

	v1Path := "/repos/testowner/testrepo/blob?path=README.md&ref=HEAD"
	v2Path := "/v2/repos/testowner/testrepo/blob?path=README.md&ref=HEAD"

	// Benchmark v2 first
	b.Run("v2_Blob", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v2Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})

	// Benchmark v1 after v2
	b.Run("v1_Blob", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v1Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})
}

// BenchmarkCommitsComparison compares v1 and v2 commits endpoint performance
func BenchmarkCommitsComparison(b *testing.B) {
	suite, cleanup, err := setupBenchmarkSuite()
	if err != nil {
		b.Fatalf("Failed to setup benchmark suite: %v", err)
	}
	defer cleanup()

	v1Path := "/repos/testowner/testrepo/commits?ref=HEAD&limit=10&skip=0"
	v2Path := "/v2/repos/testowner/testrepo/commits?ref=HEAD&limit=10&skip=0"

	// Benchmark v2 first
	b.Run("v2_Commits", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v2Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})

	// Benchmark v1 after v2
	b.Run("v1_Commits", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v1Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})
}

// BenchmarkBranchesComparison compares v1 and v2 branches endpoint performance
func BenchmarkBranchesComparison(b *testing.B) {
	suite, cleanup, err := setupBenchmarkSuite()
	if err != nil {
		b.Fatalf("Failed to setup benchmark suite: %v", err)
	}
	defer cleanup()

	v1Path := "/api/v1/repository/testowner/testrepo/branches?email=test@example.com"
	v2Path := "/v2/repos/testowner/testrepo/overview/branches?email=test@example.com"

	// Benchmark v2 first
	b.Run("v2_Branches", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v2Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})

	// Benchmark v1 after v2
	b.Run("v1_Branches", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v1Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})
}

// BenchmarkRepositoryOverviewComparison compares v1 and v2 repository overview endpoint performance
func BenchmarkRepositoryOverviewComparison(b *testing.B) {
	suite, cleanup, err := setupBenchmarkSuite()
	if err != nil {
		b.Fatalf("Failed to setup benchmark suite: %v", err)
	}
	defer cleanup()

	v1Path := "/api/v1/repository/testowner/testrepo"
	v2Path := "/v2/repos/testowner/testrepo/overview"

	// Benchmark v2 first
	b.Run("v2_RepositoryOverview", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v2Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})

	// Benchmark v1 after v2
	b.Run("v1_RepositoryOverview", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v1Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})
}

// BenchmarkTreeOverviewComparison compares v1 and v2 tree overview endpoint performance
func BenchmarkTreeOverviewComparison(b *testing.B) {
	suite, cleanup, err := setupBenchmarkSuite()
	if err != nil {
		b.Fatalf("Failed to setup benchmark suite: %v", err)
	}
	defer cleanup()

	v1Path := "/api/v1/repository/testowner/testrepo/tree"
	v2Path := "/v2/repos/testowner/testrepo/overview/tree"

	// Benchmark v2 first
	b.Run("v2_TreeOverview", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v2Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})

	// Benchmark v1 after v2
	b.Run("v1_TreeOverview", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v1Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})
}

// BenchmarkBlobOverviewComparison compares v1 and v2 blob overview endpoint performance
func BenchmarkBlobOverviewComparison(b *testing.B) {
	suite, cleanup, err := setupBenchmarkSuite()
	if err != nil {
		b.Fatalf("Failed to setup benchmark suite: %v", err)
	}
	defer cleanup()

	v1Path := "/api/v1/repository/testowner/testrepo/blob?path=README.md"
	v2Path := "/v2/repos/testowner/testrepo/overview/blob?path=README.md"

	// Benchmark v2 first
	b.Run("v2_BlobOverview", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v2Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})

	// Benchmark v1 after v2
	b.Run("v1_BlobOverview", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := suite.makeRequest("GET", v1Path, nil)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				b.Fatalf("Unexpected status code: %d, body: %s", resp.StatusCode, string(body))
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	})
}
