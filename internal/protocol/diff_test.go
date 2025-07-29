package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test helper to create a test repository with commits
func createTestRepositoryWithCommits(t *testing.T, repoPath string) (string, string, string) {
	// Create repository directory
	err := os.MkdirAll(repoPath, 0755)
	require.NoError(t, err)

	// Initialize Git repository
	repo, err := gogit.PlainInit(repoPath, false)
	require.NoError(t, err)

	// Get worktree
	worktree, err := repo.Worktree()
	require.NoError(t, err)

	// Create first commit
	file1Path := filepath.Join(repoPath, "file1.txt")
	err = os.WriteFile(file1Path, []byte("Initial content\nLine 2\n"), 0644)
	require.NoError(t, err)

	_, err = worktree.Add("file1.txt")
	require.NoError(t, err)

	commit1, err := worktree.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Create second commit (modify file1 and add file2)
	err = os.WriteFile(file1Path, []byte("Modified content\nLine 2\nLine 3\n"), 0644)
	require.NoError(t, err)

	file2Path := filepath.Join(repoPath, "file2.txt")
	err = os.WriteFile(file2Path, []byte("New file content\n"), 0644)
	require.NoError(t, err)

	_, err = worktree.Add("file1.txt")
	require.NoError(t, err)
	_, err = worktree.Add("file2.txt")
	require.NoError(t, err)

	commit2, err := worktree.Commit("Second commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Create third commit (delete file1, modify file2)
	err = os.Remove(file1Path)
	require.NoError(t, err)

	err = os.WriteFile(file2Path, []byte("Updated file content\nLine 2\n"), 0644)
	require.NoError(t, err)

	_, err = worktree.Remove("file1.txt")
	require.NoError(t, err)
	_, err = worktree.Add("file2.txt")
	require.NoError(t, err)

	commit3, err := worktree.Commit("Third commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	return commit1.String(), commit2.String(), commit3.String()
}

// Test helper to create test router with diff routes
func createTestRouter(t *testing.T, handler *DiffHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Add the Gin context middleware that our handlers expect
	router.Use(func(c *gin.Context) {
		// Store Gin context in request context for our URLParam function
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), GinContextKey{}, c))
		c.Next()
	})

	// Set up routes similar to the real server
	repoGroup := router.Group("/repos/:owner/:repo")
	{
		diffGroup := repoGroup.Group("/diff")
		{
			diffGroup.GET("/commits/:base/:head", gin.WrapH(http.HandlerFunc(handler.HandleCommitDiff)))
			diffGroup.GET("/branches/:base/:head", gin.WrapH(http.HandlerFunc(handler.HandleBranchDiff)))
		}
	}

	return router
}

func TestNewDiffHandler(t *testing.T) {
	tests := []struct {
		name           string
		config         *config.Config
		permClient     permissions.PermissionClient
		expectError    bool
		expectedErrMsg string
	}{
		{
			name:           "Nil config",
			config:         nil,
			permClient:     &MockPermissionClient{},
			expectError:    true,
			expectedErrMsg: "config cannot be nil",
		},
		{
			name: "Valid config",
			config: &config.Config{
				Git: config.GitConfig{RepoRoot: "/tmp"},
			},
			permClient:  &MockPermissionClient{},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := NewDiffHandler(tt.config, tt.permClient)

			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErrMsg)
				assert.Nil(t, handler)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, handler)
			}
		})
	}
}

func TestHandleCommitDiff(t *testing.T) {
	// Create test repository
	repoRoot := t.TempDir()
	repoPath := filepath.Join(repoRoot, "testowner", "testrepo.git")
	commit1, commit2, _ := createTestRepositoryWithCommits(t, repoPath)

	tests := []struct {
		name           string
		owner          string
		repo           string
		base           string
		head           string
		userID         string
		permClient     *MockPermissionClient
		context        string
		expectedStatus int
		checkResponse  func(t *testing.T, resp *DiffResponse)
	}{
		{
			name:   "Valid commit diff",
			owner:  "testowner",
			repo:   "testrepo",
			base:   commit1,
			head:   commit2,
			userID: "test-user",
			permClient: &MockPermissionClient{
				shouldAllow: true,
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, resp *DiffResponse) {
				assert.Equal(t, commit1, resp.Base)
				assert.Equal(t, commit2, resp.Head)
				assert.NotEmpty(t, resp.Files)
				assert.True(t, resp.Stats.FilesChanged > 0)

				// Should have file1.txt modified and file2.txt added
				fileNames := make(map[string]string)
				for _, file := range resp.Files {
					fileNames[file.Path] = file.Status
				}
				assert.Contains(t, fileNames, "file1.txt")
				assert.Contains(t, fileNames, "file2.txt")
			},
		},
		{
			name:   "Missing owner parameter",
			owner:  "",
			repo:   "testrepo",
			base:   commit1,
			head:   commit2,
			userID: "test-user",
			permClient: &MockPermissionClient{
				shouldAllow: true,
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "Missing user authentication",
			owner:  "testowner",
			repo:   "testrepo",
			base:   commit1,
			head:   commit2,
			userID: "",
			permClient: &MockPermissionClient{
				shouldAllow: true,
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:   "Permission denied",
			owner:  "testowner",
			repo:   "testrepo",
			base:   commit1,
			head:   commit2,
			userID: "test-user",
			permClient: &MockPermissionClient{
				shouldAllow: false,
			},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:   "Permission service error",
			owner:  "testowner",
			repo:   "testrepo",
			base:   commit1,
			head:   commit2,
			userID: "test-user",
			permClient: &MockPermissionClient{
				shouldError: true,
			},
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name:   "Non-existent commit",
			owner:  "testowner",
			repo:   "testrepo",
			base:   "1234567890123456789012345678901234567890",
			head:   commit2,
			userID: "test-user",
			permClient: &MockPermissionClient{
				shouldAllow: true,
			},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:    "With context parameter",
			owner:   "testowner",
			repo:    "testrepo",
			base:    commit1,
			head:    commit2,
			userID:  "test-user",
			context: "5",
			permClient: &MockPermissionClient{
				shouldAllow: true,
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, resp *DiffResponse) {
				assert.Equal(t, commit1, resp.Base)
				assert.Equal(t, commit2, resp.Head)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			cfg := &config.Config{
				Git: config.GitConfig{RepoRoot: repoRoot},
			}
			handler, err := NewDiffHandler(cfg, tt.permClient)
			require.NoError(t, err)

			router := createTestRouter(t, handler)

			// Create request
			url := fmt.Sprintf("/repos/%s/%s/diff/commits/%s/%s", tt.owner, tt.repo, tt.base, tt.head)
			if tt.context != "" {
				url += "?context=" + tt.context
			}

			req := httptest.NewRequest("GET", url, nil)
			if tt.userID != "" {
				req.Header.Set("X-USER-ID", tt.userID)
			}

			// Execute
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			// Assert
			assert.Equal(t, tt.expectedStatus, rr.Code)

			if tt.expectedStatus == http.StatusOK && tt.checkResponse != nil {
				var resp DiffResponse
				err := json.Unmarshal(rr.Body.Bytes(), &resp)
				require.NoError(t, err)
				tt.checkResponse(t, &resp)
			}
		})
	}
}

func TestHandleBranchDiff(t *testing.T) {
	// Create test repository with branches
	repoRoot := t.TempDir()
	repoPath := filepath.Join(repoRoot, "testowner", "testrepo.git")
	commit1, commit2, _ := createTestRepositoryWithCommits(t, repoPath)

	// Create branches in the repository
	repo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// Create a feature branch from commit2
	_, err = repo.Reference("refs/heads/feature", true)
	if err != nil {
		// Branch doesn't exist, create it
		head, err := repo.Head()
		require.NoError(t, err)

		featureRef := plumbing.NewBranchReferenceName("feature")
		newRef := plumbing.NewHashReference(featureRef, head.Hash())
		err = repo.Storer.SetReference(newRef)
		require.NoError(t, err)
	}

	tests := []struct {
		name           string
		owner          string
		repo           string
		base           string
		head           string
		userID         string
		permClient     *MockPermissionClient
		expectedStatus int
		checkResponse  func(t *testing.T, resp *DiffResponse)
	}{
		{
			name:   "Valid branch diff using commit hashes",
			owner:  "testowner",
			repo:   "testrepo",
			base:   commit1,
			head:   commit2,
			userID: "test-user",
			permClient: &MockPermissionClient{
				shouldAllow: true,
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, resp *DiffResponse) {
				assert.Equal(t, commit1, resp.Base)
				assert.Equal(t, commit2, resp.Head)
				assert.NotEmpty(t, resp.Files)
				assert.NotNil(t, resp.Commits)
				assert.NotEmpty(t, resp.MergeBase)
			},
		},
		{
			name:   "Missing branch parameter",
			owner:  "testowner",
			repo:   "testrepo",
			base:   "",
			head:   "main",
			userID: "test-user",
			permClient: &MockPermissionClient{
				shouldAllow: true,
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "Non-existent branch",
			owner:  "testowner",
			repo:   "testrepo",
			base:   "nonexistent",
			head:   "main",
			userID: "test-user",
			permClient: &MockPermissionClient{
				shouldAllow: true,
			},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:   "Permission denied for branch diff",
			owner:  "testowner",
			repo:   "testrepo",
			base:   commit1,
			head:   commit2,
			userID: "test-user",
			permClient: &MockPermissionClient{
				shouldAllow: false,
			},
			expectedStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			cfg := &config.Config{
				Git: config.GitConfig{RepoRoot: repoRoot},
			}
			handler, err := NewDiffHandler(cfg, tt.permClient)
			require.NoError(t, err)

			router := createTestRouter(t, handler)

			// Create request
			url := fmt.Sprintf("/repos/%s/%s/diff/branches/%s/%s", tt.owner, tt.repo, tt.base, tt.head)
			req := httptest.NewRequest("GET", url, nil)
			if tt.userID != "" {
				req.Header.Set("X-USER-ID", tt.userID)
			}

			// Execute
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			// Assert
			assert.Equal(t, tt.expectedStatus, rr.Code)

			if tt.expectedStatus == http.StatusOK && tt.checkResponse != nil {
				var resp DiffResponse
				err := json.Unmarshal(rr.Body.Bytes(), &resp)
				require.NoError(t, err)
				tt.checkResponse(t, &resp)
			}
		})
	}
}

func TestDiffResponseStructure(t *testing.T) {
	// Test that the response structure is properly formatted
	repoRoot := t.TempDir()
	repoPath := filepath.Join(repoRoot, "testowner", "testrepo.git")
	commit1, commit2, _ := createTestRepositoryWithCommits(t, repoPath)

	cfg := &config.Config{
		Git: config.GitConfig{RepoRoot: repoRoot},
	}
	handler, err := NewDiffHandler(cfg, &MockPermissionClient{shouldAllow: true})
	require.NoError(t, err)

	router := createTestRouter(t, handler)

	// Test commit diff response structure
	req := httptest.NewRequest("GET", fmt.Sprintf("/repos/testowner/testrepo/diff/commits/%s/%s", commit1, commit2), nil)
	req.Header.Set("X-USER-ID", "test-user")

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp DiffResponse
	err = json.Unmarshal(rr.Body.Bytes(), &resp)
	require.NoError(t, err)

	// Verify response structure
	assert.Equal(t, commit1, resp.Base)
	assert.Equal(t, commit2, resp.Head)
	assert.NotNil(t, resp.Files)
	assert.NotNil(t, resp.Stats)

	// Verify file diff structure
	if len(resp.Files) > 0 {
		file := resp.Files[0]
		assert.NotEmpty(t, file.Path)
		assert.NotEmpty(t, file.Status)
		assert.Contains(t, []string{"added", "deleted", "modified", "renamed"}, file.Status)
	}

	// Verify stats
	assert.GreaterOrEqual(t, resp.Stats.FilesChanged, 0)
	assert.GreaterOrEqual(t, resp.Stats.TotalAdditions, 0)
	assert.GreaterOrEqual(t, resp.Stats.TotalDeletions, 0)
}

func TestHelperFunctions(t *testing.T) {
	t.Run("getFileName", func(t *testing.T) {
		// This tests the helper function logic
		repoRoot := t.TempDir()
		repoPath := filepath.Join(repoRoot, "testowner", "testrepo.git")
		createTestRepositoryWithCommits(t, repoPath)

		repo, err := gogit.PlainOpen(repoPath)
		require.NoError(t, err)

		commits, err := repo.Log(&gogit.LogOptions{})
		require.NoError(t, err)

		var commitList []*object.Commit
		err = commits.ForEach(func(c *object.Commit) error {
			commitList = append(commitList, c)
			return nil
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(commitList), 2)

		// Get changes between commits
		tree1, err := commitList[1].Tree()
		require.NoError(t, err)
		tree2, err := commitList[0].Tree()
		require.NoError(t, err)

		changes, err := object.DiffTree(tree1, tree2)
		require.NoError(t, err)

		if len(changes) > 0 {
			fileName := getFileName(changes[0])
			assert.NotEmpty(t, fileName)
		}
	})

	t.Run("isBinaryPatch", func(t *testing.T) {
		tests := []struct {
			name     string
			patch    string
			expected bool
		}{
			{
				name:     "Text patch",
				patch:    "+++ a/file.txt\n--- b/file.txt\n@@ -1,3 +1,3 @@\n line1\n-line2\n+modified line2\n line3",
				expected: false,
			},
			{
				name:     "Binary patch marker",
				patch:    "Binary files a/image.png and b/image.png differ",
				expected: true,
			},
			{
				name:     "Git binary patch",
				patch:    "GIT binary patch\ndelta 123\n",
				expected: true,
			},
			{
				name:     "Contains null bytes",
				patch:    "some content\x00with null bytes",
				expected: true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := isBinaryPatch(tt.patch)
				assert.Equal(t, tt.expected, result)
			})
		}
	})

	t.Run("isCommitNotFoundError", func(t *testing.T) {
		tests := []struct {
			name     string
			err      error
			expected bool
		}{
			{
				name:     "Nil error",
				err:      nil,
				expected: false,
			},
			{
				name:     "Object not found",
				err:      fmt.Errorf("object not found"),
				expected: true,
			},
			{
				name:     "Reference not found",
				err:      fmt.Errorf("reference not found"),
				expected: true,
			},
			{
				name:     "Other error",
				err:      fmt.Errorf("some other error"),
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := isCommitNotFoundError(tt.err)
				assert.Equal(t, tt.expected, result)
			})
		}
	})

	t.Run("isBranchNotFoundError", func(t *testing.T) {
		tests := []struct {
			name     string
			err      error
			expected bool
		}{
			{
				name:     "Nil error",
				err:      nil,
				expected: false,
			},
			{
				name:     "Branch not found",
				err:      fmt.Errorf("branch main not found"),
				expected: true,
			},
			{
				name:     "Reference not found",
				err:      fmt.Errorf("reference not found"),
				expected: true,
			},
			{
				name:     "Other error",
				err:      fmt.Errorf("some other error"),
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := isBranchNotFoundError(tt.err)
				assert.Equal(t, tt.expected, result)
			})
		}
	})
}

// ========================================================================
// COMPREHENSIVE UNIT TESTS FOR NEW UTILITIES (Task 14.5)
// ========================================================================

func TestValidateAndExtractParams(t *testing.T) {
	handler := &DiffHandler{}

	tests := []struct {
		name           string
		setupRequest   func() *http.Request
		expectedParams *RequestParams
		expectedError  string
	}{
		{
			name: "Valid parameters",
			setupRequest: func() *http.Request {
				params := map[string]string{
					"owner": "testowner",
					"repo":  "testrepo",
					"base":  "abc",
					"head":  "def",
				}
				req := SetupTestRequestWithGinParams("GET", "/repos/testowner/testrepo/diff/commits/abc/def", params)
				req.Header.Set("X-USER-ID", "test-user")
				return req
			},
			expectedParams: &RequestParams{
				Owner:  "testowner",
				Repo:   "testrepo",
				Base:   "abc",
				Head:   "def",
				UserID: "test-user",
			},
			expectedError: "",
		},
		{
			name: "Missing owner parameter",
			setupRequest: func() *http.Request {
				params := map[string]string{
					"owner": "",
					"repo":  "testrepo",
					"base":  "abc",
					"head":  "def",
				}
				req := SetupTestRequestWithGinParams("GET", "/repos//testrepo/diff/commits/abc/def", params)
				req.Header.Set("X-USER-ID", "test-user")
				return req
			},
			expectedParams: nil,
			expectedError:  "missing required parameters: owner or repo",
		},
		{
			name: "Missing user authentication",
			setupRequest: func() *http.Request {
				params := map[string]string{
					"owner": "testowner",
					"repo":  "testrepo",
					"base":  "abc",
					"head":  "def",
				}
				req := SetupTestRequestWithGinParams("GET", "/repos/testowner/testrepo/diff/commits/abc/def", params)
				// No X-USER-ID header
				return req
			},
			expectedParams: nil,
			expectedError:  "missing user authentication",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.setupRequest()
			params, err := handler.validateAndExtractParams(req)

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				assert.Nil(t, params)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedParams, params)
			}
		})
	}
}

func TestExtractPaginationParams(t *testing.T) {
	tests := []struct {
		name     string
		queryStr string
		expected PaginationParams
	}{
		{
			name:     "Default parameters",
			queryStr: "",
			expected: PaginationParams{
				Page:     1,
				PageSize: 50,
			},
		},
		{
			name:     "Valid page and page_size",
			queryStr: "page=2&page_size=25",
			expected: PaginationParams{
				Page:     2,
				PageSize: 25,
			},
		},
		{
			name:     "Invalid page (negative)",
			queryStr: "page=-1&page_size=25",
			expected: PaginationParams{
				Page:     1, // defaults to 1
				PageSize: 25,
			},
		},
		{
			name:     "Invalid page_size (too large)",
			queryStr: "page=1&page_size=300",
			expected: PaginationParams{
				Page:     1,
				PageSize: 50, // defaults to 50
			},
		},
		{
			name:     "Invalid page_size (zero)",
			queryStr: "page=1&page_size=0",
			expected: PaginationParams{
				Page:     1,
				PageSize: 50, // defaults to 50
			},
		},
		{
			name:     "Non-numeric parameters",
			queryStr: "page=abc&page_size=def",
			expected: PaginationParams{
				Page:     1,  // defaults to 1
				PageSize: 50, // defaults to 50
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/?"+tt.queryStr, nil)
			result := extractPaginationParams(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPaginateFiles(t *testing.T) {
	// Create test files
	files := []FileDiff{
		{Path: "file1.txt", Status: "modified"},
		{Path: "file2.txt", Status: "added"},
		{Path: "file3.txt", Status: "deleted"},
		{Path: "file4.txt", Status: "modified"},
		{Path: "file5.txt", Status: "added"},
	}

	tests := []struct {
		name               string
		files              []FileDiff
		pagination         PaginationParams
		expectedFiles      []FileDiff
		expectedPagination PaginationParams
	}{
		{
			name:  "First page with page_size 2",
			files: files,
			pagination: PaginationParams{
				Page:     1,
				PageSize: 2,
			},
			expectedFiles: files[0:2],
			expectedPagination: PaginationParams{
				Page:     1,
				PageSize: 2,
				Total:    5,
				HasMore:  true,
			},
		},
		{
			name:  "Last page",
			files: files,
			pagination: PaginationParams{
				Page:     3,
				PageSize: 2,
			},
			expectedFiles: files[4:5],
			expectedPagination: PaginationParams{
				Page:     3,
				PageSize: 2,
				Total:    5,
				HasMore:  false,
			},
		},
		{
			name:  "Page beyond available data",
			files: files,
			pagination: PaginationParams{
				Page:     10,
				PageSize: 2,
			},
			expectedFiles: []FileDiff{},
			expectedPagination: PaginationParams{
				Page:     10,
				PageSize: 2,
				Total:    5,
				HasMore:  false,
			},
		},
		{
			name:  "Empty files list",
			files: []FileDiff{},
			pagination: PaginationParams{
				Page:     1,
				PageSize: 10,
			},
			expectedFiles: []FileDiff{},
			expectedPagination: PaginationParams{
				Page:     1,
				PageSize: 10,
				Total:    0,
				HasMore:  false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultFiles, resultPagination := paginateFiles(tt.files, tt.pagination)
			assert.Equal(t, tt.expectedFiles, resultFiles)
			assert.Equal(t, tt.expectedPagination, resultPagination)
		})
	}
}

func TestIsBinaryFile(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		filename string
		expected bool
	}{
		{
			name:     "Text file with normal content",
			content:  "This is normal text content\nwith multiple lines\n",
			filename: "test.txt",
			expected: false,
		},
		{
			name:     "Binary content with null bytes",
			content:  "Binary content\x00with null bytes",
			filename: "test.txt",
			expected: true,
		},
		{
			name:     "Image file by extension",
			content:  "Normal text content",
			filename: "image.jpg",
			expected: true,
		},
		{
			name:     "Executable file by extension",
			content:  "Normal text content",
			filename: "program.exe",
			expected: true,
		},
		{
			name:     "Go source file",
			content:  "package main\n\nfunc main() {\n\tprintln(\"Hello\")\n}",
			filename: "main.go",
			expected: false,
		},
		{
			name:     "Archive file by extension",
			content:  "Text content",
			filename: "archive.zip",
			expected: true,
		},
		{
			name:     "Mixed case extension",
			content:  "Text content",
			filename: "IMAGE.JPG",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isBinaryFile(tt.content, tt.filename)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSendJSONResponse(t *testing.T) {
	tests := []struct {
		name           string
		data           interface{}
		statusCode     int
		expectedStatus int
		expectedHeader string
	}{
		{
			name: "Valid JSON response",
			data: map[string]string{
				"message": "success",
				"status":  "ok",
			},
			statusCode:     http.StatusOK,
			expectedStatus: http.StatusOK,
			expectedHeader: "application/json",
		},
		{
			name: "Error response",
			data: ErrorResponse{
				Error: "Something went wrong",
				Code:  "ERR001",
			},
			statusCode:     http.StatusBadRequest,
			expectedStatus: http.StatusBadRequest,
			expectedHeader: "application/json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			sendJSONResponse(recorder, tt.data, tt.statusCode)

			assert.Equal(t, tt.expectedStatus, recorder.Code)
			assert.Equal(t, tt.expectedHeader, recorder.Header().Get("Content-Type"))

			// Verify JSON can be unmarshaled
			var result map[string]interface{}
			err := json.Unmarshal(recorder.Body.Bytes(), &result)
			assert.NoError(t, err)
		})
	}
}

func TestSendErrorResponse(t *testing.T) {
	tests := []struct {
		name           string
		message        string
		statusCode     int
		expectedStatus int
	}{
		{
			name:           "Bad request error",
			message:        "Invalid parameters",
			statusCode:     http.StatusBadRequest,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "Not found error",
			message:        "Resource not found",
			statusCode:     http.StatusNotFound,
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "Internal server error",
			message:        "Internal error occurred",
			statusCode:     http.StatusInternalServerError,
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			sendErrorResponse(recorder, tt.message, tt.statusCode)

			assert.Equal(t, tt.expectedStatus, recorder.Code)
			assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))

			var errorResp ErrorResponse
			err := json.Unmarshal(recorder.Body.Bytes(), &errorResp)
			assert.NoError(t, err)
			assert.Equal(t, tt.message, errorResp.Error)
		})
	}
}

func TestHandleDiffError(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		operation      string
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "Commit not found error",
			err:            fmt.Errorf("object not found"),
			operation:      "calculate diff",
			expectedStatus: http.StatusNotFound,
			expectedError:  "Commit not found:",
		},
		{
			name:           "Branch not found error",
			err:            fmt.Errorf("branch xyz not found"),
			operation:      "calculate diff",
			expectedStatus: http.StatusNotFound,
			expectedError:  "Branch not found:",
		},
		{
			name:           "Authentication error",
			err:            fmt.Errorf("missing user authentication"),
			operation:      "validate request",
			expectedStatus: http.StatusUnauthorized,
			expectedError:  "missing user authentication",
		},
		{
			name:           "Permission error",
			err:            fmt.Errorf("permission check failed"),
			operation:      "validate access",
			expectedStatus: http.StatusInternalServerError,
			expectedError:  "Permission check failed:",
		},
		{
			name:           "Access denied error",
			err:            fmt.Errorf("access denied to repository"),
			operation:      "validate access",
			expectedStatus: http.StatusForbidden,
			expectedError:  "Access denied to repository",
		},
		{
			name:           "Missing parameter error",
			err:            fmt.Errorf("missing required parameter: base"),
			operation:      "validate params",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "missing required parameter: base",
		},
		{
			name:           "Invalid parameter error",
			err:            fmt.Errorf("invalid repository path"),
			operation:      "validate params",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "invalid repository path",
		},
		{
			name:           "Generic error",
			err:            fmt.Errorf("unexpected error occurred"),
			operation:      "process request",
			expectedStatus: http.StatusInternalServerError,
			expectedError:  "Failed to process request:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handleDiffError(recorder, tt.err, tt.operation)

			assert.Equal(t, tt.expectedStatus, recorder.Code)
			assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))

			var errorResp ErrorResponse
			err := json.Unmarshal(recorder.Body.Bytes(), &errorResp)
			assert.NoError(t, err)
			assert.Contains(t, errorResp.Error, tt.expectedError)
		})
	}
}

func TestGetFileMetadata(t *testing.T) {
	tests := []struct {
		name     string
		change   *object.Change
		expected map[string]interface{}
	}{
		{
			name: "File modification with all metadata",
			change: &object.Change{
				From: object.ChangeEntry{
					Name: "test.txt",
					TreeEntry: object.TreeEntry{
						Name: "test.txt",
						Mode: 0100644,
						Hash: plumbing.NewHash("1234567890abcdef1234567890abcdef12345678"),
					},
				},
				To: object.ChangeEntry{
					Name: "test.txt",
					TreeEntry: object.TreeEntry{
						Name: "test.txt",
						Mode: 0100755,
						Hash: plumbing.NewHash("abcdef1234567890abcdef1234567890abcdef12"),
					},
				},
			},
			expected: map[string]interface{}{
				"old_mode": "0100644",
				"new_mode": "0100755",
				"old_hash": "1234567890abcdef1234567890abcdef12345678",
				"new_hash": "abcdef1234567890abcdef1234567890abcdef12",
			},
		},
		{
			name: "File addition (no old metadata)",
			change: &object.Change{
				From: object.ChangeEntry{},
				To: object.ChangeEntry{
					Name: "new.txt",
					TreeEntry: object.TreeEntry{
						Name: "new.txt",
						Mode: 0100644,
						Hash: plumbing.NewHash("abcdef1234567890abcdef1234567890abcdef12"),
					},
				},
			},
			expected: map[string]interface{}{
				"new_mode": "0100644",
				"new_hash": "abcdef1234567890abcdef1234567890abcdef12",
			},
		},
		{
			name: "File deletion (no new metadata)",
			change: &object.Change{
				From: object.ChangeEntry{
					Name: "deleted.txt",
					TreeEntry: object.TreeEntry{
						Name: "deleted.txt",
						Mode: 0100644,
						Hash: plumbing.NewHash("1234567890abcdef1234567890abcdef12345678"),
					},
				},
				To: object.ChangeEntry{},
			},
			expected: map[string]interface{}{
				"old_mode": "0100644",
				"old_hash": "1234567890abcdef1234567890abcdef12345678",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getFileMetadata(tt.change)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPaginatedDiffResponse(t *testing.T) {
	// Test that PaginatedDiffResponse properly embeds DiffResponse
	diffResp := &DiffResponse{
		Base: "abc123",
		Head: "def456",
		Files: []FileDiff{
			{Path: "test.txt", Status: "modified"},
		},
		Stats: DiffStats{
			TotalAdditions: 5,
			TotalDeletions: 3,
			FilesChanged:   1,
		},
	}

	pagination := &PaginationParams{
		Page:     1,
		PageSize: 10,
		Total:    1,
		HasMore:  false,
	}

	paginatedResp := &PaginatedDiffResponse{
		DiffResponse: diffResp,
		Pagination:   pagination,
	}

	// Test JSON serialization
	jsonData, err := json.Marshal(paginatedResp)
	assert.NoError(t, err)

	// Test JSON deserialization
	var unmarshaled PaginatedDiffResponse
	err = json.Unmarshal(jsonData, &unmarshaled)
	assert.NoError(t, err)

	// Verify all fields are preserved
	assert.Equal(t, diffResp.Base, unmarshaled.Base)
	assert.Equal(t, diffResp.Head, unmarshaled.Head)
	assert.Equal(t, len(diffResp.Files), len(unmarshaled.Files))
	assert.Equal(t, diffResp.Stats, unmarshaled.Stats)
	assert.Equal(t, pagination.Page, unmarshaled.Pagination.Page)
	assert.Equal(t, pagination.Total, unmarshaled.Pagination.Total)
}

func TestErrorResponseStructure(t *testing.T) {
	tests := []struct {
		name      string
		errorResp ErrorResponse
	}{
		{
			name: "Simple error",
			errorResp: ErrorResponse{
				Error: "Simple error message",
			},
		},
		{
			name: "Error with code",
			errorResp: ErrorResponse{
				Error: "Error with code",
				Code:  "ERR001",
			},
		},
		{
			name: "Error with code and details",
			errorResp: ErrorResponse{
				Error:   "Complex error",
				Code:    "ERR002",
				Details: "Additional error details",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test JSON serialization
			jsonData, err := json.Marshal(tt.errorResp)
			assert.NoError(t, err)

			// Test JSON deserialization
			var unmarshaled ErrorResponse
			err = json.Unmarshal(jsonData, &unmarshaled)
			assert.NoError(t, err)

			// Verify all fields are preserved
			assert.Equal(t, tt.errorResp.Error, unmarshaled.Error)
			assert.Equal(t, tt.errorResp.Code, unmarshaled.Code)
			assert.Equal(t, tt.errorResp.Details, unmarshaled.Details)
		})
	}
}
