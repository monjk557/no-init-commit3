package merge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

func setupRepoForMergeHandlers(t *testing.T) (repoRoot string, owner string, repoName string, repo *gogit.Repository, cleanup func()) {
	t.Helper()

	gin.SetMode(gin.TestMode)

	repoRoot = t.TempDir()
	owner = "testowner"
	repoName = "testrepo"

	repoPath := filepath.Join(repoRoot, owner, repoName+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	var err error
	repo, err = gogit.PlainInit(repoPath, false) // non-bare is fine for tests; path still ends with .git
	require.NoError(t, err)

	// Use "main" as default branch (match handler tests and common convention)
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))
	require.NoError(t, repo.Storer.SetReference(headRef))

	cleanup = func() {}
	return repoRoot, owner, repoName, repo, cleanup
}

func TestHandleMergePreview_Returns404_When_SourceRefNotFound(t *testing.T) {
	repoRoot, owner, repoName, repo, _ := setupRepoForMergeHandlers(t)

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: repoRoot,
		},
	}

	handler, err := NewMergePreviewHandler(cfg)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// main: base commit
	writeFile(t, wt, "file.txt", "base\n")
	_, err = commitFile(t, wt, "file.txt", "base")
	require.NoError(t, err)

	// feature/foo: one commit, then delete the ref (simulate branch deletion)
	featureBranch := "feature/foo"
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(featureBranch),
		Create: true,
	})
	require.NoError(t, err)

	writeFile(t, wt, "file.txt", "feature\n")
	_, err = commitFile(t, wt, "file.txt", "feature")
	require.NoError(t, err)

	// Delete branch ref only (commit object can remain)
	require.NoError(t, repo.Storer.RemoveReference(plumbing.NewBranchReferenceName(featureBranch)))

	router := gin.New()
	router.GET("/repos/:owner/:repo/merges/preview", handler.HandleMergePreview)

	req := httptest.NewRequest(
		http.MethodGet,
		"/repos/"+owner+"/"+repoName+"/merges/preview?source_ref="+featureBranch+"&target_ref=main",
		nil,
	)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), `"success":false`)
}

func TestHandleMerge_Returns409_When_ConflictDetected(t *testing.T) {
	repoRoot, owner, repoName, repo, _ := setupRepoForMergeHandlers(t)

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: repoRoot,
		},
	}

	handler, err := NewMergeOperationsHandler(cfg)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// main: base commit
	writeFile(t, wt, "conflict.txt", "base\n")
	_, err = commitFile(t, wt, "conflict.txt", "base")
	require.NoError(t, err)

	// feature: change conflict.txt differently
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)
	writeFile(t, wt, "conflict.txt", "feature\n")
	_, err = commitFile(t, wt, "conflict.txt", "feature change")
	require.NoError(t, err)

	// main: change same file differently
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	})
	require.NoError(t, err)
	writeFile(t, wt, "conflict.txt", "main\n")
	_, err = commitFile(t, wt, "conflict.txt", "main change")
	require.NoError(t, err)

	router := gin.New()
	router.POST("/repos/:owner/:repo/merges", handler.HandleMerge)

	bodyBytes, err := json.Marshal(&MergeRequest{
		SourceRef: "feature",
		TargetRef: "main",
		// ForceConflict=false -> should fail with 409 on conflicts
	})
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/repos/"+owner+"/"+repoName+"/merges",
		bytes.NewReader(bodyBytes),
	)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), `"success":false`)
}

