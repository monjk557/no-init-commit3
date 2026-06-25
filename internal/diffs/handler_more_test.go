package diffs

// handler.go 의 미커버 typed handler (ExpandContext, SingleCommitDiff,
// DiffFileSearch, BranchDiffQuery, ClassifyAnchors) 와 routes.RegisterRoutes /
// diffErrorMapper 를 httptest + adapt 패턴으로 검증한다.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goginNew 는 TestMode 의 gin 엔진을 만든다 (init() 에서 SetMode 됨).
func goginNew() *gin.Engine {
	return gin.New()
}

// handlerWithTwoCommits 는 repoRoot/testowner/testrepo.git 에 두 커밋을 가진
// bare 저장소를 만들고 (handler, initialSHA, secondSHA) 를 반환한다. handler 의
// RepoRoot 가 repoRoot 로 설정돼 GetAndValidateRepoPath 가 실제 경로를 찾는다.
func handlerWithTwoCommits(t *testing.T) (*DiffHandler, string, string) {
	t.Helper()
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b1 := makeBlob(t, repo, []byte("First line\n"))
	t1 := makeTree(t, repo, []object.TreeEntry{{Name: "existing.txt", Mode: 0100644, Hash: b1}})
	c1 := makeCommit(t, repo, "Initial commit", t1, nil, now)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/master", c1)))

	b2 := makeBlob(t, repo, []byte("First line\nSecond line\n"))
	t2 := makeTree(t, repo, []object.TreeEntry{{Name: "existing.txt", Mode: 0100644, Hash: b2}})
	c2 := makeCommit(t, repo, "Add second line", t2, []plumbing.Hash{c1}, now)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/master", c2)))

	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: tempDir},
	}
	h, err := NewDiffHandler(cfg)
	require.NoError(t, err)
	return h, c1.String(), c2.String()
}

func diffPath(suffix string) string {
	return "/api/v2/repository/:owner/:repo" + suffix
}

func diffURL(suffix string) string {
	return "/api/v2/repository/testowner/testrepo" + suffix
}

// ----------------------------------------------------------------------------
// HandleSingleCommitDiff
// ----------------------------------------------------------------------------

func TestHandleSingleCommitDiff_Success(t *testing.T) {
	h, _, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/commit/:sha"), adapt(h.HandleSingleCommitDiff))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", diffURL("/diff/commit/"+secondSHA), nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp SingleCommitDiffResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, secondSHA, resp.Commit.SHA)
}

func TestHandleSingleCommitDiff_MissingSha(t *testing.T) {
	h, _, _ := handlerWithTwoCommits(t)
	router := goginNew()
	// sha 를 빈 segment 로 둘 수 없으므로 별도 route 로 sha 미설정 시뮬레이션.
	router.GET(diffPath("/diff/commit-nosha"), adapt(h.HandleSingleCommitDiff))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", diffURL("/diff/commit-nosha"), nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleSingleCommitDiff_BadCommit(t *testing.T) {
	h, _, _ := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/commit/:sha"), adapt(h.HandleSingleCommitDiff))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", diffURL("/diff/commit/deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"), nil)
	router.ServeHTTP(w, req)
	// classifyDiffError → NOT_FOUND substring 매칭으로 404, 그 외 400.
	assert.GreaterOrEqual(t, w.Code, 400)
}

// ----------------------------------------------------------------------------
// HandleExpandContext
// ----------------------------------------------------------------------------

func TestHandleExpandContext_DirectRange(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/expand-context"), adaptQuery(h.HandleExpandContext))

	w := httptest.NewRecorder()
	url := diffURL(fmt.Sprintf(
		"/diff/expand-context?path=existing.txt&base=%s&head=%s&from_old=1&to_old=2&from_new=1&diff_mode=direct",
		initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp ExpandContextResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, 2, resp.Count)
}

func TestHandleExpandContext_StartOldDirectionDown(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/expand-context"), adaptQuery(h.HandleExpandContext))

	w := httptest.NewRecorder()
	url := diffURL(fmt.Sprintf(
		"/diff/expand-context?path=existing.txt&base=%s&head=%s&start_old=1&start_new=1&count=2&direction=down&diff_mode=direct",
		initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestHandleExpandContext_StartOldDirectionUp(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/expand-context"), adaptQuery(h.HandleExpandContext))

	w := httptest.NewRecorder()
	url := diffURL(fmt.Sprintf(
		"/diff/expand-context?path=existing.txt&base=%s&head=%s&start_old=2&start_new=2&count=1&direction=up&side=old&diff_mode=direct",
		initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestHandleExpandContext_InvalidStartOld(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/expand-context"), adaptQuery(h.HandleExpandContext))

	w := httptest.NewRecorder()
	url := diffURL(fmt.Sprintf(
		"/diff/expand-context?path=existing.txt&base=%s&head=%s&start_old=abc&start_new=1",
		initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleExpandContext_MissingRangeParams(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/expand-context"), adaptQuery(h.HandleExpandContext))

	w := httptest.NewRecorder()
	// start_old 도 from_old 도 없음 → 400.
	url := diffURL(fmt.Sprintf(
		"/diff/expand-context?path=existing.txt&base=%s&head=%s", initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleExpandContext_RangeTooLarge(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/expand-context"), adaptQuery(h.HandleExpandContext))

	w := httptest.NewRecorder()
	// to_old - from_old + 1 > 500 → 400.
	url := diffURL(fmt.Sprintf(
		"/diff/expand-context?path=existing.txt&base=%s&head=%s&from_old=1&to_old=600&from_new=1",
		initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleExpandContext_BadRef(t *testing.T) {
	h, _, _ := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/expand-context"), adaptQuery(h.HandleExpandContext))

	w := httptest.NewRecorder()
	url := diffURL("/diff/expand-context?path=existing.txt&base=nope1&head=nope2&from_old=1&to_old=2&from_new=1&diff_mode=direct")
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ----------------------------------------------------------------------------
// HandleDiffFileSearch
// ----------------------------------------------------------------------------

func TestHandleDiffFileSearch_Success(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/search/files"), adapt(h.HandleDiffFileSearch))

	w := httptest.NewRecorder()
	url := diffURL(fmt.Sprintf(
		"/diff/search/files?base=%s&head=%s&query=existing&diff_mode=direct", initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp DiffFileSearchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, "existing", resp.Query)
}

func TestHandleDiffFileSearch_MissingBaseHead(t *testing.T) {
	h, _, _ := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/search/files"), adapt(h.HandleDiffFileSearch))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", diffURL("/diff/search/files?query=x"), nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDiffFileSearch_MissingQuery(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/search/files"), adapt(h.HandleDiffFileSearch))

	w := httptest.NewRecorder()
	url := diffURL(fmt.Sprintf("/diff/search/files?base=%s&head=%s", initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDiffFileSearch_InvalidDiffMode(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/search/files"), adapt(h.HandleDiffFileSearch))

	w := httptest.NewRecorder()
	url := diffURL(fmt.Sprintf(
		"/diff/search/files?base=%s&head=%s&query=x&diff_mode=bogus", initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDiffFileSearch_LegacyTypeCommit(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/search/files"), adapt(h.HandleDiffFileSearch))

	w := httptest.NewRecorder()
	url := diffURL(fmt.Sprintf(
		"/diff/search/files?base=%s&head=%s&query=existing&type=commit", initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestHandleDiffFileSearch_InvalidLegacyType(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/search/files"), adapt(h.HandleDiffFileSearch))

	w := httptest.NewRecorder()
	url := diffURL(fmt.Sprintf(
		"/diff/search/files?base=%s&head=%s&query=x&type=bogus", initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDiffFileSearch_BadRef(t *testing.T) {
	h, _, _ := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/search/files"), adapt(h.HandleDiffFileSearch))

	w := httptest.NewRecorder()
	url := diffURL("/diff/search/files?base=nope1&head=nope2&query=x&diff_mode=direct")
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ----------------------------------------------------------------------------
// HandleBranchDiffQuery
// ----------------------------------------------------------------------------

func TestHandleBranchDiffQuery_Success(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/branches"), adaptQuery(h.HandleBranchDiffQuery))

	w := httptest.NewRecorder()
	url := diffURL(fmt.Sprintf("/diff/branches?base=%s&head=%s", initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp DiffResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
}

func TestHandleBranchDiffQuery_Paginated(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/branches"), adaptQuery(h.HandleBranchDiffQuery))

	w := httptest.NewRecorder()
	url := diffURL(fmt.Sprintf("/diff/branches?base=%s&head=%s&page=1&page_size=10", initialSHA, secondSHA))
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// paginated 응답 (responseOverride) — pagination 필드 존재.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	assert.Contains(t, raw, "pagination")
}

func TestHandleBranchDiffQuery_MissingBaseQueryBind(t *testing.T) {
	h, _, _ := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/branches"), adaptQuery(h.HandleBranchDiffQuery))

	w := httptest.NewRecorder()
	// base/head binding:required → adaptQuery 의 ShouldBindQuery 단계에서 400.
	req, _ := http.NewRequest("GET", diffURL("/diff/branches"), nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleBranchDiffQuery_BadRef(t *testing.T) {
	h, _, _ := handlerWithTwoCommits(t)
	router := goginNew()
	router.GET(diffPath("/diff/branches"), adaptQuery(h.HandleBranchDiffQuery))

	w := httptest.NewRecorder()
	url := diffURL("/diff/branches?base=nope1&head=nope2")
	req, _ := http.NewRequest("GET", url, nil)
	router.ServeHTTP(w, req)
	assert.GreaterOrEqual(t, w.Code, 400)
}

// ----------------------------------------------------------------------------
// HandleClassifyAnchors + classifyAnchors
// ----------------------------------------------------------------------------

func postJSON(t *testing.T, router http.Handler, url string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

func TestHandleClassifyAnchors_EmptyAnchors(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.POST(diffPath("/diff/classify-anchors"), adapt(h.HandleClassifyAnchors))

	w := postJSON(t, router, diffURL("/diff/classify-anchors"), ClassifyAnchorsRequest{
		BaseSHA: initialSHA, HeadSHA: secondSHA, Anchors: []AnchorRequest{},
	})
	require.Equal(t, http.StatusOK, w.Code)
	var resp ClassifyAnchorsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Empty(t, resp.Results)
}

func TestHandleClassifyAnchors_ResultsPreserveOrder(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	router := goginNew()
	router.POST(diffPath("/diff/classify-anchors"), adapt(h.HandleClassifyAnchors))

	// 두 anchor 모두 분류 결과를 입력 순서대로 받는다. missing.txt 는 변경 없음 → active.
	w := postJSON(t, router, diffURL("/diff/classify-anchors"), ClassifyAnchorsRequest{
		BaseSHA: initialSHA,
		HeadSHA: secondSHA,
		Anchors: []AnchorRequest{
			{ID: "a1", Path: "existing.txt", StartLine: 1, EndLine: 2},
			{ID: "a2", Path: "missing.txt", StartLine: 1, EndLine: 1},
		},
	})
	require.Equal(t, http.StatusOK, w.Code)
	var resp ClassifyAnchorsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Results, 2)
	assert.Equal(t, "a1", resp.Results[0].ID)
	assert.Equal(t, "a2", resp.Results[1].ID)
	assert.Equal(t, AnchorStatusActive, resp.Results[1].Status)
}

func TestHandleClassifyAnchors_InvalidJSON(t *testing.T) {
	h, _, _ := handlerWithTwoCommits(t)
	router := goginNew()
	router.POST(diffPath("/diff/classify-anchors"), adapt(h.HandleClassifyAnchors))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", diffURL("/diff/classify-anchors"), bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestClassifyAnchors_BaseEqualsHeadActive(t *testing.T) {
	repoPath, _, secondSHA := twoCommitRepo(t)
	// base == head → 변경 없음 → 모든 anchor active.
	results, err := classifyAnchors(context.Background(), repoPath, secondSHA, secondSHA,
		[]AnchorRequest{{ID: "x", Path: "existing.txt", StartLine: 1, EndLine: 1}})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, AnchorStatusActive, results[0].Status)
}

func TestClassifyAnchors_BadBaseOrphaned(t *testing.T) {
	repoPath, _, secondSHA := twoCommitRepo(t)
	// diff 계산 실패 (bad base) → 그 그룹 orphaned.
	results, err := classifyAnchors(context.Background(), repoPath, "badbase", secondSHA,
		[]AnchorRequest{{ID: "x", Path: "existing.txt", StartLine: 1, EndLine: 1}})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, AnchorStatusOrphaned, results[0].Status)
}

func TestClassifyAnchors_PerAnchorCommitSHAGrouping(t *testing.T) {
	// anchor.CommitSHA 가 effective base 로 사용되는 grouping 경로를 검증한다.
	// classifyAnchors 는 contextLines=0 으로 diff 를 계산하므로 hunk 가 비어
	// (production 동작) modified 파일이라도 active 로 분류된다 — overlap 기반 stale
	// 판정은 classifyOneAnchor 단위 테스트(classify_test.go)에서 직접 검증한다.
	repoPath, initialSHA, secondSHA := modifiedLineRepo(t)
	// anchor.CommitSHA = initial → diff(initial..second) 그룹으로 분류.
	results, err := classifyAnchors(context.Background(), repoPath, "ignored", secondSHA,
		[]AnchorRequest{{ID: "x", Path: "file.txt", StartLine: 1, EndLine: 1, CommitSHA: initialSHA}})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "x", results[0].ID)
	assert.Equal(t, AnchorStatusActive, results[0].Status)
}

// TestClassifyAnchors_DeletedFileOrphaned 는 파일이 삭제된 경우 orphaned 로
// 분류되는 실제 git 경로를 검증한다 (status 기반 → contextLines 무관).
func TestClassifyAnchors_DeletedFileOrphaned(t *testing.T) {
	repoPath, initialSHA, deleteSHA := deletedFileRepo(t)
	results, err := classifyAnchors(context.Background(), repoPath, initialSHA, deleteSHA,
		[]AnchorRequest{{ID: "d", Path: "gone.txt", StartLine: 1, EndLine: 1}})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, AnchorStatusOrphaned, results[0].Status)
}

// deletedFileRepo 는 gone.txt 를 두 번째 커밋에서 삭제하는 fixture 다.
func deletedFileRepo(t *testing.T) (string, string, string) {
	t.Helper()
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "owner", "repo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	bGone := makeBlob(t, repo, []byte("to be deleted\n"))
	bKeep := makeBlob(t, repo, []byte("kept\n"))
	t1 := makeTree(t, repo, []object.TreeEntry{
		{Name: "gone.txt", Mode: 0100644, Hash: bGone},
		{Name: "keep.txt", Mode: 0100644, Hash: bKeep},
	})
	c1 := makeCommit(t, repo, "init", t1, nil, now)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/master", c1)))

	// 두 번째 커밋: gone.txt 제거 (keep.txt 만 남김).
	t2 := makeTree(t, repo, []object.TreeEntry{{Name: "keep.txt", Mode: 0100644, Hash: bKeep}})
	c2 := makeCommit(t, repo, "delete gone.txt", t2, []plumbing.Hash{c1}, now)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/master", c2)))

	return repoPath, c1.String(), c2.String()
}

// modifiedLineRepo 는 file.txt 의 첫 줄 내용을 실제로 바꾸는(추가가 아닌 변경)
// 두 커밋 bare 저장소를 만든다.
func modifiedLineRepo(t *testing.T) (string, string, string) {
	t.Helper()
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "owner", "repo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	b1 := makeBlob(t, repo, []byte("alpha\nbeta\ngamma\n"))
	t1 := makeTree(t, repo, []object.TreeEntry{{Name: "file.txt", Mode: 0100644, Hash: b1}})
	c1 := makeCommit(t, repo, "init", t1, nil, now)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/master", c1)))

	b2 := makeBlob(t, repo, []byte("ALPHA-CHANGED\nbeta\ngamma\n"))
	t2 := makeTree(t, repo, []object.TreeEntry{{Name: "file.txt", Mode: 0100644, Hash: b2}})
	c2 := makeCommit(t, repo, "modify line 1", t2, []plumbing.Hash{c1}, now)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/master", c2)))

	return repoPath, c1.String(), c2.String()
}

// ----------------------------------------------------------------------------
// routes.RegisterRoutes (real handler) + diffErrorMapper
// ----------------------------------------------------------------------------

func TestRegisterRoutes_RealHandlerServesCommitDiff(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	e := goginNew()
	g := e.Group("/repos/:owner/:repo")
	RegisterRoutes(g, h)

	w := httptest.NewRecorder()
	url := fmt.Sprintf("/repos/testowner/testrepo/diff/commits/%s/%s", initialSHA, secondSHA)
	req, _ := http.NewRequest("GET", url, nil)
	e.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp DiffResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
}

func TestRegisterRoutes_RealHandlerServesPatch(t *testing.T) {
	h, initialSHA, secondSHA := handlerWithTwoCommits(t)
	e := goginNew()
	g := e.Group("/repos/:owner/:repo")
	RegisterRoutes(g, h)

	w := httptest.NewRecorder()
	url := fmt.Sprintf("/repos/testowner/testrepo/diff/patch/%s/%s", initialSHA, secondSHA)
	req, _ := http.NewRequest("GET", url, nil)
	e.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "existing.txt")
}

func TestDiffErrorMapper_AllBranches(t *testing.T) {
	t.Parallel()
	// responseOverride 우선.
	st, body := diffErrorMapper(&responseOverride{status: 413, body: gin.H{"k": "v"}})
	assert.Equal(t, 413, st)
	assert.NotNil(t, body)

	// typed.HTTPError.
	st, _ = diffErrorMapper(typed.NewHTTPError(http.StatusBadRequest, "bad"))
	assert.Equal(t, http.StatusBadRequest, st)

	// 일반 에러 → 500.
	st, _ = diffErrorMapper(errors.New("boom"))
	assert.Equal(t, http.StatusInternalServerError, st)
}
