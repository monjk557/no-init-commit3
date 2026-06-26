package branches

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// 테스트 픽스처: 분기(diverged)된 브랜치를 가진 bare repo
// 회귀 방지: ahead/behind / merge-base 계산 경로가 실 git object 위에서 검증된다
// =============================================================================

// gitDir 는 주어진 bare repo 에 대해 git 서브커맨드를 실행하고 trim 된 stdout 을 반환한다.
func gitDir(t *testing.T, repoPath string, args ...string) string {
	t.Helper()
	full := append([]string{"--git-dir", repoPath}, args...)
	cmd := exec.Command("git", full...)
	out, err := cmd.Output()
	require.NoError(t, err, "git %v", args)
	return trimOutput(out)
}

// gitDirStdin 은 stdin 입력과 함께 git 서브커맨드를 실행한다.
func gitDirStdin(t *testing.T, repoPath, stdin string, args ...string) string {
	t.Helper()
	full := append([]string{"--git-dir", repoPath}, args...)
	cmd := exec.Command("git", full...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	require.NoError(t, err, "git %v", args)
	return trimOutput(out)
}

// gitCommitEnv 는 결정론적인 author/committer 환경변수를 반환한다.
func gitCommitEnv(base []string) []string {
	return append(base,
		"GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@t.com",
		"GIT_AUTHOR_DATE=2024-01-01T00:00:00+00:00",
		"GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t.com",
		"GIT_COMMITTER_DATE=2024-01-01T00:00:00+00:00",
	)
}

// commitTree 는 commit-tree 로 커밋을 만들고 hash 를 반환한다. parents 는 -p 로 연결.
func commitTree(t *testing.T, repoPath, treeHash, msg string, parents ...string) string {
	t.Helper()
	args := []string{"--git-dir", repoPath, "commit-tree", treeHash, "-m", msg}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = gitCommitEnv(cmd.Environ())
	out, err := cmd.Output()
	require.NoError(t, err)
	return trimOutput(out)
}

// makeTree 는 단일 blob 을 가진 tree 를 만들어 hash 를 반환한다.
func makeTree(t *testing.T, repoPath, content, fileName string) string {
	t.Helper()
	blob := gitDirStdin(t, repoPath, content, "hash-object", "-w", "--stdin")
	tree := gitDirStdin(t, repoPath, "100644 blob "+blob+"\t"+fileName+"\n", "mktree")
	return tree
}

// newDivergedBareRepo 는 main/feature 가 공통 조상 A 에서 각각 1 커밋씩
// 분기한 bare repo 를 생성한다.
//
//	A (commitA)
//	├── feature → B (1 ahead, 1 behind main)
//	└── main    → C (1 ahead, 1 behind feature)
//
// returns: repoPath, commitA (merge-base), commitB(feature tip), commitC(main tip)
func newDivergedBareRepo(t *testing.T) (repoPath, commitA, commitB, commitC string) {
	t.Helper()

	dir := t.TempDir()
	repoPath = filepath.Join(dir, "diverged.git")
	require.NoError(t, exec.Command("git", "init", "--bare", repoPath).Run())

	// 공통 조상 A
	treeA := makeTree(t, repoPath, "a\n", "f")
	commitA = commitTree(t, repoPath, treeA, "A")

	// feature: A → B
	treeB := makeTree(t, repoPath, "b\n", "f")
	commitB = commitTree(t, repoPath, treeB, "B", commitA)

	// main: A → C
	treeC := makeTree(t, repoPath, "c\n", "f")
	commitC = commitTree(t, repoPath, treeC, "C", commitA)

	gitDir(t, repoPath, "update-ref", "refs/heads/main", commitC)
	gitDir(t, repoPath, "update-ref", "refs/heads/feature", commitB)
	require.NoError(t, exec.Command("git", "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main").Run())

	return repoPath, commitA, commitB, commitC
}

// initDivergedRepoUnderRoot 는 newHandler 가 만든 RepoRoot 아래
// {root}/{owner}/{repo}.git 위치에 분기된 bare repo 를 만든다.
func initDivergedRepoUnderRoot(t *testing.T, root, owner, repo string) (repoPath, commitA, commitB, commitC string) {
	t.Helper()
	repoPath = filepath.Join(root, owner, repo+".git")
	require.NoError(t, makeDir(repoPath))
	require.NoError(t, exec.Command("git", "init", "--bare", repoPath).Run())

	treeA := makeTree(t, repoPath, "a\n", "f")
	commitA = commitTree(t, repoPath, treeA, "A")
	treeB := makeTree(t, repoPath, "b\n", "f")
	commitB = commitTree(t, repoPath, treeB, "B", commitA)
	treeC := makeTree(t, repoPath, "c\n", "f")
	commitC = commitTree(t, repoPath, treeC, "C", commitA)

	gitDir(t, repoPath, "update-ref", "refs/heads/main", commitC)
	gitDir(t, repoPath, "update-ref", "refs/heads/feature", commitB)
	require.NoError(t, exec.Command("git", "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main").Run())

	return repoPath, commitA, commitB, commitC
}

// =============================================================================
// 테스트: NativeCalculateAheadBehind / CalculateAheadBehind
// =============================================================================

func Test_NativeCalculateAheadBehind_Diverged_ReturnsOneOne(t *testing.T) {
	t.Parallel()

	repoPath, _, _, _ := newDivergedBareRepo(t)

	ab, err := NativeCalculateAheadBehind(context.Background(), repoPath, "feature", "main")

	require.NoError(t, err)
	require.Equal(t, 1, ab.Ahead, "feature 는 main 대비 1 커밋 ahead")
	require.Equal(t, 1, ab.Behind, "feature 는 main 대비 1 커밋 behind")
}

func Test_NativeCalculateAheadBehind_SameRef_ReturnsZeroZero(t *testing.T) {
	t.Parallel()

	repoPath, _, _, _ := newDivergedBareRepo(t)

	ab, err := NativeCalculateAheadBehind(context.Background(), repoPath, "main", "main")

	require.NoError(t, err)
	require.Equal(t, 0, ab.Ahead)
	require.Equal(t, 0, ab.Behind)
}

func Test_NativeCalculateAheadBehind_InvalidRef_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath, _, _, _ := newDivergedBareRepo(t)

	_, err := NativeCalculateAheadBehind(context.Background(), repoPath, "ghost-branch", "main")

	require.Error(t, err)
}

// CalculateAheadBehind 는 NativeCalculateAheadBehind 로 위임하는 package-level wrapper.
func Test_CalculateAheadBehind_Diverged_ReturnsOneOne(t *testing.T) {
	t.Parallel()

	repoPath, _, _, _ := newDivergedBareRepo(t)

	ab, err := CalculateAheadBehind(context.Background(), repoPath, "feature", "main")

	require.NoError(t, err)
	require.Equal(t, 1, ab.Ahead)
	require.Equal(t, 1, ab.Behind)
}

// =============================================================================
// 테스트: NativeMergeBase
// =============================================================================

func Test_NativeMergeBase_Diverged_ReturnsCommonAncestor(t *testing.T) {
	t.Parallel()

	repoPath, commitA, commitB, commitC := newDivergedBareRepo(t)

	mergeBase, err := NativeMergeBase(context.Background(), repoPath, commitB, commitC)

	require.NoError(t, err)
	require.Equal(t, commitA, mergeBase, "B 와 C 의 merge-base 는 공통 조상 A")
}

func Test_NativeMergeBase_NoCommonAncestor_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath, _, _, _ := newDivergedBareRepo(t)

	// 완전히 독립된(parent 없는) 커밋을 추가하여 공통 조상이 없도록 한다.
	orphanTree := makeTree(t, repoPath, "orphan\n", "g")
	orphan := commitTree(t, repoPath, orphanTree, "orphan")

	_, err := NativeMergeBase(context.Background(), repoPath, orphan, "main")

	require.Error(t, err)
	require.Contains(t, err.Error(), "no common ancestor")
}

// =============================================================================
// 테스트: ParseBranchSort (table-driven)
// =============================================================================

func Test_ParseBranchSort_AllCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  BranchSortField
	}{
		{"oldest", "oldest", BranchSortOldest},
		{"name", "name", BranchSortName},
		{"newest explicit", "newest", BranchSortNewest},
		{"empty defaults to newest", "", BranchSortNewest},
		{"unknown defaults to newest", "bogus-value", BranchSortNewest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ParseBranchSort(tc.input))
		})
	}
}

// =============================================================================
// 테스트: RegisterRoutes
// =============================================================================

// Test_RegisterRoutes_NilHandler_DoesNotPanic
// 회귀: spec-only 모드 (h == nil) 등록은 panic 하지 않아야 함
func Test_RegisterRoutes_NilHandler_DoesNotPanic(t *testing.T) {
	t.Parallel()

	r := gin.New()
	require.NotPanics(t, func() {
		RegisterRoutes(r, nil)
	})
}

// Test_RegisterRoutes_WithHandler_RoutesRequest
// 회귀: 실제 handler 를 등록하면 요청이 라우팅되어 핸들러가 실행되어야 함
func Test_RegisterRoutes_WithHandler_RoutesRequest(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	r := gin.New()
	grp := r.Group("/repository/:owner/:repo")
	require.NotPanics(t, func() {
		RegisterRoutes(grp, handler)
	})

	// GET /branches 는 등록된 listBranches 핸들러로 라우팅되어야 한다.
	req := httptest.NewRequest("GET", "/repository/testowner/testrepo/branches", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 404(라우트 미존재)가 아니라 핸들러가 실행되어 200 을 반환해야 함.
	require.Equal(t, http.StatusOK, w.Code, "routed handler should respond 200, body=%s", w.Body.String())

	var resp GetBranchesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
}

// =============================================================================
// 테스트: HandleBranchStats
// =============================================================================

func Test_HandleBranchStats_Success_ReturnsAheadBehind(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/stats", adapt(handler.HandleBranchStats))

	body := `{"branches": ["main", "feature"]}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches/stats", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp BranchStatsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)

	// main 은 default branch → 0/0
	require.Equal(t, BranchStatsItem{Ahead: 0, Behind: 0}, resp.Stats["main"])
	// feature 는 main 대비 1 ahead / 1 behind
	require.Equal(t, BranchStatsItem{Ahead: 1, Behind: 1}, resp.Stats["feature"])
}

func Test_HandleBranchStats_UnknownBranch_ReturnsZeroStats(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/stats", adapt(handler.HandleBranchStats))

	// 존재하지 않는 브랜치는 ahead/behind 계산이 실패하지만 0/0 으로 graceful 처리.
	body := `{"branches": ["ghost"]}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches/stats", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp BranchStatsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, BranchStatsItem{Ahead: 0, Behind: 0}, resp.Stats["ghost"])
}

func Test_HandleBranchStats_InvalidBody_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/stats", adapt(handler.HandleBranchStats))

	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches/stats", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "invalid request")
}

// HandleBranchStats 는 GetAndValidateRepoPath 만 호출하며 (validateAndGetRepoPath
// 와 달리 디스크 존재 검사를 하지 않음), 존재하지 않는 repo 의 ahead/behind 계산은
// 실패하지만 핸들러가 graceful 하게 0/0 으로 처리하여 200 을 반환한다.
func Test_HandleBranchStats_RepoMissing_GracefulZeroStats(t *testing.T) {
	t.Parallel()

	handler, _ := newHandler(t)

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/stats", adapt(handler.HandleBranchStats))

	body := `{"branches": ["main"]}`
	req := httptest.NewRequest("POST", "/repository/testowner/nonexistent/branches/stats", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp BranchStatsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
}

// =============================================================================
// 테스트: HandleValidateReopen
// =============================================================================

func Test_HandleValidateReopen_BothBranchesExist_ReturnsValid(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	_, _, commitB, commitC := initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/validate-reopen", adapt(handler.HandleValidateReopen))

	body := `{"source_branch_name": "feature", "target_branch_name": "main"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches/validate-reopen", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp ValidateReopenResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.True(t, resp.LineageValid)
	require.Equal(t, commitB, resp.SourceCommitSHA)
	require.Equal(t, commitC, resp.TargetCommitSHA)
}

func Test_HandleValidateReopen_LineageValid_WhenStoredSHAMatchesTip(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	_, _, commitB, _ := initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/validate-reopen", adapt(handler.HandleValidateReopen))

	// stored SHA == 현재 source tip → lineage 검사 skip, valid.
	body := `{"source_branch_name": "feature", "target_branch_name": "main", "stored_source_commit_sha": "` + commitB + `"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches/validate-reopen", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp ValidateReopenResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.LineageValid)
}

func Test_HandleValidateReopen_StoredSHADivergedFromTip_Returns409(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	// commitC(main tip) 는 commitB(feature tip) 의 조상이 아니다 → merge-base != storedSHA
	// → lineage invalid.
	_, _, _, commitC := initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/validate-reopen", adapt(handler.HandleValidateReopen))

	// stored SHA = main tip (feature tip 과 다르고, 그 조상도 아님) → lineage invalid 409.
	body := `{"source_branch_name": "feature", "target_branch_name": "main", "stored_source_commit_sha": "` + commitC + `"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches/validate-reopen", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusConflict)
	requireBodyContains(t, w, "different merge base")
}

func Test_HandleValidateReopen_SourceBranchMissing_Returns409(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/validate-reopen", adapt(handler.HandleValidateReopen))

	body := `{"source_branch_name": "ghost", "target_branch_name": "main"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches/validate-reopen", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusConflict)
	requireBodyContains(t, w, "source branch")
}

func Test_HandleValidateReopen_TargetBranchMissing_Returns409(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/validate-reopen", adapt(handler.HandleValidateReopen))

	body := `{"source_branch_name": "feature", "target_branch_name": "ghost"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches/validate-reopen", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusConflict)
	requireBodyContains(t, w, "target branch")
}

func Test_HandleValidateReopen_InvalidBody_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/validate-reopen", adapt(handler.HandleValidateReopen))

	// 필수 필드 누락 → binding:"required" 실패.
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches/validate-reopen", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "invalid request body")
}

// HandleValidateReopen 도 GetAndValidateRepoPath 만 호출하므로 존재하지 않는
// repo 에서는 브랜치 존재 검사가 false 로 떨어져 409 (source no longer exists).
func Test_HandleValidateReopen_RepoMissing_Returns409(t *testing.T) {
	t.Parallel()

	handler, _ := newHandler(t)

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/validate-reopen", adapt(handler.HandleValidateReopen))

	body := `{"source_branch_name": "feature", "target_branch_name": "main"}`
	req := httptest.NewRequest("POST", "/repository/testowner/nonexistent/branches/validate-reopen", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusConflict)
	requireBodyContains(t, w, "source branch")
}

// =============================================================================
// 테스트: HandleBulkDeleteBranches
// =============================================================================

func Test_HandleBulkDeleteBranches_AllSucceed_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/bulk-delete", adapt(handler.HandleBulkDeleteBranches))

	// feature 만 삭제 (main 은 HEAD 라 보존).
	body := `{"branches": ["feature"]}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches/bulk-delete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp BulkDeleteBranchesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, 1, resp.Total)
	require.Equal(t, 1, resp.Succeeded)
	require.Equal(t, 0, resp.Failed)
	require.Len(t, resp.Results, 1)
	require.True(t, resp.Results[0].Success)
	require.Equal(t, "feature", resp.Results[0].BranchName)
}

func Test_HandleBulkDeleteBranches_PartialFailure_ReturnsMixedResults(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/bulk-delete", adapt(handler.HandleBulkDeleteBranches))

	// feature 는 성공, ghost 는 존재하지 않아 실패, "bad name" 은 검증 실패.
	body := `{"branches": ["feature", "ghost", "bad name"]}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches/bulk-delete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp BulkDeleteBranchesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.False(t, resp.Success, "하나라도 실패하면 success=false")
	require.Equal(t, 3, resp.Total)
	require.Equal(t, 1, resp.Succeeded)
	require.Equal(t, 2, resp.Failed)

	byName := make(map[string]BulkDeleteBranchResult, len(resp.Results))
	for _, r := range resp.Results {
		byName[r.BranchName] = r
	}
	require.True(t, byName["feature"].Success)
	require.False(t, byName["ghost"].Success)
	require.Contains(t, byName["ghost"].Error, "does not exist")
	require.False(t, byName["bad name"].Success)
	require.Contains(t, byName["bad name"].Error, "invalid branch name")
}

func Test_HandleBulkDeleteBranches_InvalidBody_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/bulk-delete", adapt(handler.HandleBulkDeleteBranches))

	// 빈 배열 → binding:"required,min=1" 실패.
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches/bulk-delete", strings.NewReader(`{"branches": []}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "branches array is required")
}

func Test_HandleBulkDeleteBranches_RepoNotFound_Returns404(t *testing.T) {
	t.Parallel()

	handler, _ := newHandler(t)

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches/bulk-delete", adapt(handler.HandleBulkDeleteBranches))

	body := `{"branches": ["feature"]}`
	req := httptest.NewRequest("POST", "/repository/testowner/nonexistent/branches/bulk-delete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusNotFound)
	requireBodyContains(t, w, "repository not found")
}

// =============================================================================
// 테스트: HandleGetBranches 추가 커버리지 (sort/email/status 필터 경로)
// =============================================================================

func Test_HandleGetBranches_StatusFilterInvalid_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches", adaptQuery(handler.HandleGetBranches))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches?status=bogus", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "invalid status")
}

func Test_HandleGetBranches_InvalidLimit_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches", adaptQuery(handler.HandleGetBranches))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches?limit=abc", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "invalid limit")
}

func Test_HandleGetBranches_InvalidSkip_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches", adaptQuery(handler.HandleGetBranches))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches?skip=-1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "invalid skip")
}

func Test_HandleGetBranches_SearchAndSortFilters_ReturnMatching(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches", adaptQuery(handler.HandleGetBranches))

	// search=feat + sort=name → feature 만 매칭.
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches?search=feat&sort=name", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp GetBranchesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Len(t, resp.Results, 1)
	require.Equal(t, "feature", resp.Results[0].Name)
}

func Test_HandleGetBranches_EmailFilter_ReturnMatching(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches", adaptQuery(handler.HandleGetBranches))

	// 모든 커밋의 committer email 은 t@t.com → 매칭, status=active 도 함께 통과.
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches?email=t@t.com", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp GetBranchesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.GreaterOrEqual(t, len(resp.Results), 2, "main + feature 모두 매칭")
}

// HandleGetBranches 는 GetAndValidateRepoPath 만 호출 → 디스크 미존재 repo 는
// GetBranches(for-each-ref) 가 실패하여 422 "repository corrupted" 로 떨어진다.
func Test_HandleGetBranches_RepoMissing_Returns422(t *testing.T) {
	t.Parallel()

	handler, _ := newHandler(t)

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches", adaptQuery(handler.HandleGetBranches))

	req := httptest.NewRequest("GET", "/repos/testowner/nonexistent/branches", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusUnprocessableEntity)
	requireBodyContains(t, w, "repository corrupted")
}

// =============================================================================
// 테스트: HandleUpdateBranch / HandleDeleteBranch 추가 커버리지
// =============================================================================

// HandleUpdateBranch: target 으로 fast-forward 업데이트 (commit update 경로).
func Test_HandleUpdateBranch_FastForwardTarget_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath, commitA, _, commitC := initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	// commitA 에 머무는 ff-base 브랜치를 만들고, main(commitC, A 의 자손) 으로 ff 업데이트.
	gitDir(t, repoPath, "update-ref", "refs/heads/ff-base", commitA)

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	body := `{"target": "main"}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/ff-base", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp UpdateBranchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, "update", resp.Operation)
	require.Equal(t, commitC, resp.CommitHash)
	require.True(t, resp.FastForward)
}

// HandleUpdateBranch: non-fast-forward target → 400 (force 없이).
func Test_HandleUpdateBranch_NonFastForward_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initDivergedRepoUnderRoot(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	// feature → main 으로 업데이트는 분기되어 fast-forward 가 아님.
	body := `{"target": "main"}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/feature", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "fast-forward")
}

// HandleUpdateBranch: target 과 new_name 둘 다 없으면 400.
func Test_HandleUpdateBranch_NoTargetNoNewName_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/master", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "must be provided")
}

// HandleUpdateBranch: target 과 new_name 둘 다 있으면 400.
func Test_HandleUpdateBranch_BothTargetAndNewName_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	body := `{"target": "master", "new_name": "renamed"}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/master", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "Cannot provide both")
}

// HandleUpdateBranch: 잘못된 JSON 은 400.
func Test_HandleUpdateBranch_InvalidJSON_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/master", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "Invalid JSON")
}

// HandleUpdateBranch: 잘못된 브랜치 이름 → 400.
func Test_HandleUpdateBranch_InvalidBranchName_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	body := `{"new_name": "renamed"}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/bad~name", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "branch name")
}

// HandleUpdateBranch: repo 미존재 → 404.
func Test_HandleUpdateBranch_RepoNotFound_Returns404(t *testing.T) {
	t.Parallel()

	handler, _ := newHandler(t)

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	body := `{"new_name": "renamed"}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/nonexistent/branches/master", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusNotFound)
	requireBodyContains(t, w, "repository not found")
}

// HandleDeleteBranch: repo 미존재 → 404.
func Test_HandleDeleteBranch_RepoNotFound_Returns404(t *testing.T) {
	t.Parallel()

	handler, _ := newHandler(t)

	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleDeleteBranch))

	req := httptest.NewRequest("DELETE", "/repository/testowner/nonexistent/branches/feature", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusNotFound)
	requireBodyContains(t, w, "repository not found")
}
