package protocol

import (
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
	"github.com/go-chi/chi/v5"
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
func createTestRouter(t *testing.T, handler *DiffHandler) *chi.Mux {
	router := chi.NewRouter()
	router.Route("/repos", func(r chi.Router) {
		r.Route("/{owner}", func(r chi.Router) {
			r.Route("/{repo}", func(r chi.Router) {
				r.Route("/diff", func(r chi.Router) {
					r.Get("/commits/{base}/{head}", handler.HandleCommitDiff)
					r.Get("/branches/{base}/{head}", handler.HandleBranchDiff)
				})
			})
		})
	})
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
