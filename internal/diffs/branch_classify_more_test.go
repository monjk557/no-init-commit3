package diffs

// HandleBranchDiff / HandleClassifyAnchors 의 미커버 브랜치(파라미터 검증 실패,
// repo-path 검증 실패, branch diff pagination override)를 실제 bare 저장소로
// 검증한다. 기존 헬퍼(makeBlob/Tree/Commit, diffPath/diffURL, postJSON, adapt,
// goginNew)를 재사용한다.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoBranchHandler 는 master / feature 두 브랜치를 가진 저장소를 만든다.
// feature 는 master 에서 두 파일을 추가/수정해 branch diff 가 여러 파일을 낸다.
func twoBranchHandler(t *testing.T) *DiffHandler {
	t.Helper()
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	b1 := makeBlob(t, repo, []byte("initial\n"))
	t1 := makeTree(t, repo, []object.TreeEntry{{Name: "a.txt", Mode: 0100644, Hash: b1}})
	c1 := makeCommit(t, repo, "init", t1, nil, now)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/master", c1)))

	// feature: a.txt 수정 + b.txt 추가 → 파일 2개 변경 (pagination 검증용).
	b2 := makeBlob(t, repo, []byte("initial\nmore\n"))
	bNew := makeBlob(t, repo, []byte("brand new\n"))
	t2 := makeTree(t, repo, []object.TreeEntry{
		{Name: "a.txt", Mode: 0100644, Hash: b2},
		{Name: "b.txt", Mode: 0100644, Hash: bNew},
	})
	c2 := makeCommit(t, repo, "feature work", t2, []plumbing.Hash{c1}, now)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/feature", c2)))

	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: tempDir},
	}
	h, err := NewDiffHandler(cfg)
	require.NoError(t, err)
	return h
}

// ----------------------------------------------------------------------------
// HandleBranchDiff
// ----------------------------------------------------------------------------

func TestHandleBranchDiff_PaginationOverride(t *testing.T) {
	h := twoBranchHandler(t)
	router := goginNew()
	router.GET(diffPath("/diff/branches/:base/:head"), adapt(h.HandleBranchDiff))

	// page/page_size 지정 → responseOverride(PaginatedDiffResponse) 경로.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET",
		diffURL("/diff/branches/master/feature")+"?page=1&page_size=1", nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp PaginatedDiffResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	require.NotNil(t, resp.Pagination)
	assert.Equal(t, 1, resp.Pagination.PageSize)
	require.NotNil(t, resp.DiffResponse)
	assert.LessOrEqual(t, len(resp.DiffResponse.Files), 1, "page_size=1 이면 한 파일만")
}

func TestHandleBranchDiff_MissingBaseHeadParams(t *testing.T) {
	h := twoBranchHandler(t)
	router := goginNew()
	// :base/:head 없는 route → base/head 가 "" 로 400.
	router.GET(diffPath("/diff/branches-nobasehead"), adapt(h.HandleBranchDiff))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", diffURL("/diff/branches-nobasehead"), nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestHandleBranchDiff_MissingOwnerParams(t *testing.T) {
	h := twoBranchHandler(t)
	router := goginNew()
	// :owner/:repo 없는 route → validateAndExtractParams 가 400.
	router.GET("/noparams/diff/branches/:base/:head", adapt(h.HandleBranchDiff))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/noparams/diff/branches/master/feature", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestHandleBranchDiff_InvalidRepoPath(t *testing.T) {
	h := twoBranchHandler(t)
	router := goginNew()
	router.GET(diffPath("/diff/branches/:base/:head"), adapt(h.HandleBranchDiff))

	// owner 에 ".." → GetAndValidateRepoPath path-traversal 검증 실패 → 400.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET",
		"/api/v2/repository/bad..owner/testrepo/diff/branches/master/feature", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// ----------------------------------------------------------------------------
// HandleClassifyAnchors — 파라미터/경로 검증 실패 경로
// ----------------------------------------------------------------------------

func TestHandleClassifyAnchors_MissingOwnerParams(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	// :owner/:repo 없는 route → validateAndExtractParams 가 400 (bind 이전에 실패).
	router.POST("/noparams/diff/classify-anchors", adapt(h.HandleClassifyAnchors))

	w := postJSON(t, router, "/noparams/diff/classify-anchors", ClassifyAnchorsRequest{
		BaseSHA: initialSHA, HeadSHA: secondSHA,
		Anchors: []AnchorRequest{{ID: "a", Path: "existing.txt", StartLine: 1, EndLine: 1}},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestHandleClassifyAnchors_InvalidRepoPath(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.POST(diffPath("/diff/classify-anchors"), adapt(h.HandleClassifyAnchors))

	// owner 에 ".." → empty-anchor early return 을 지나 GetAndValidateRepoPath 실패 → 400.
	url := "/api/v2/repository/bad..owner/testrepo/diff/classify-anchors"
	w := postJSON(t, router, url, ClassifyAnchorsRequest{
		BaseSHA: initialSHA, HeadSHA: secondSHA,
		Anchors: []AnchorRequest{{ID: "a", Path: "existing.txt", StartLine: 1, EndLine: 1}},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}
