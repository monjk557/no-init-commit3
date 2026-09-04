package repositories

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/gitutil"
)

// setupRepoWithBranches 는 초기 커밋이 있는 bare 저장소를 만들고 추가 브랜치를
// 붙인 뒤 (router, repoPath) 를 돌려준다.
func setupRepoWithBranches(t *testing.T, owner, repo string, extraBranches ...string) (*gin.Engine, string) {
	t.Helper()

	handler, _, cleanup := setupTestHandler(t)
	t.Cleanup(cleanup)

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/repos/"+owner+"/"+repo, nil))
	require.Equal(t, http.StatusCreated, w.Code, "repo 생성 실패: %s", w.Body.String())

	repoPath := handler.pathConfig.FormatRepoPath(owner, repo)
	for _, branch := range extraBranches {
		out, err := exec.Command("git", "--git-dir", repoPath, "update-ref",
			gitutil.BranchRefPrefix+branch, "HEAD").CombinedOutput()
		require.NoError(t, err, "branch %s 생성 실패: %s", branch, string(out))
	}
	return router, repoPath
}

func setDefaultBranch(t *testing.T, router *gin.Engine, owner, repo, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PUT", "/repos/"+owner+"/"+repo+"/head", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestHandleSetDefaultBranch_Success(t *testing.T) {
	router, repoPath := setupRepoWithBranches(t, "o", "r", "develop")

	w := setDefaultBranch(t, router, "o", "r", `{"branch":"develop"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp SetDefaultBranchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, "develop", resp.DefaultBranch)
	assert.Equal(t, "main", resp.PreviousDefaultBranch)

	// git 쪽 HEAD 가 실제로 옮겨졌는지 — 응답만 믿지 않는다.
	head, err := gitutil.HeadRefName(context.Background(), repoPath)
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/develop", head)
}

func TestHandleSetDefaultBranch_AcceptsFullRefPrefix(t *testing.T) {
	router, repoPath := setupRepoWithBranches(t, "o", "r", "develop")

	w := setDefaultBranch(t, router, "o", "r", `{"branch":"refs/heads/develop"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	head, err := gitutil.HeadRefName(context.Background(), repoPath)
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/develop", head)
}

func TestHandleSetDefaultBranch_BranchNotFound(t *testing.T) {
	router, repoPath := setupRepoWithBranches(t, "o", "r")

	w := setDefaultBranch(t, router, "o", "r", `{"branch":"nope"}`)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// 실패 시 HEAD 는 그대로여야 한다.
	head, err := gitutil.HeadRefName(context.Background(), repoPath)
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/main", head)
}

func TestHandleSetDefaultBranch_RepositoryNotFound(t *testing.T) {
	router, _ := setupRepoWithBranches(t, "o", "r")

	w := setDefaultBranch(t, router, "o", "missing", `{"branch":"main"}`)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleSetDefaultBranch_InvalidBody(t *testing.T) {
	router, _ := setupRepoWithBranches(t, "o", "r")

	for name, body := range map[string]string{
		"malformed JSON": `{"branch":`,
		"empty branch":   `{"branch":"  "}`,
		"missing field":  `{}`,
		"dash prefix":    `{"branch":"-delete"}`,
		"ref path":       `{"branch":"refs/tags/v1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := setDefaultBranch(t, router, "o", "r", body)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		})
	}
}

func TestNormalizeBranchName(t *testing.T) {
	ok := map[string]string{
		"develop":              "develop",
		"  develop  ":          "develop",
		"refs/heads/develop":   "develop",
		"feature/a-b.c":        "feature/a-b.c",
		"refs/heads/feat/x":    "feat/x",
		"release-1.2":          "release-1.2",
		"refs/heads/refs-like": "refs-like",
	}
	for in, want := range ok {
		got, err := normalizeBranchName(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}

	for _, in := range []string{"", "   ", "-f", "refs/tags/v1", "refs/heads/-f"} {
		_, err := normalizeBranchName(in)
		assert.Error(t, err, in)
	}
}
