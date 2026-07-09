package overview

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
)

// TestHandlePostLatestCommitsBatch_EmptyRepo_Returns200 pins the Issue-5577
// unification: an empty repository (exists, unborn HEAD, zero commits) must
// return 200 with empty CommitsByPath — matching the main overview and
// latest-commit empty handling — instead of the 404 "reference not found" the
// batch endpoint used to emit.
func TestHandlePostLatestCommitsBatch_EmptyRepo_Returns200(t *testing.T) {
	h, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner, repo := "o", "emptyrepo"
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	r := gin.New()
	r.POST("/repos/:owner/:repo/overview/latest-commits", adapt(h.HandlePostLatestCommitsBatch))

	payload, err := json.Marshal(LatestCommitsBatchRequest{Ref: "main", Paths: []string{"README.md"}})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost,
		"/repos/"+owner+"/"+repo+"/overview/latest-commits", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "empty repo batch should be 200, body=%s", w.Body.String())
	var resp LatestCommitsBatchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Empty(t, resp.CommitsByPath, "empty repo has no commits for any path")
}
