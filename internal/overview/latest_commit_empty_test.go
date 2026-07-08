package overview

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleGetLatestCommitOverview_EmptyRepo_Returns200Null pins the Issue-5560
// fix: a repository that exists on disk but has no commits (created, never
// pushed → unborn HEAD) must return 200 with a null commit, not the 422
// "failed to resolve latest commit" it used to fall through to when `git log
// HEAD` failed exit 128. An empty repo is a valid state, not an error.
func TestHandleGetLatestCommitOverview_EmptyRepo_Returns200Null(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "emptyrepo"
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	// Bare repo with an unborn HEAD (refs/heads/main) and zero commits.
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview/latest-commit", adaptQuery(handler.HandleGetLatestCommitOverview))

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview/latest-commit", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "empty repo should be 200, body=%s", w.Body.String())

	var resp LatestCommitOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Nil(t, resp.Commit, "empty repo has no latest commit")
	assert.Equal(t, owner+"/"+repo, resp.Repository)
}
