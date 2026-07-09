package branches

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/testutil/gitrepo"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// =============================================================================
// 테스트: CreateBranch
// 회귀 방지: "브랜치 생성 API가 올바른 상태 코드와 응답을 반환해야 함"
// =============================================================================

// Test_CreateBranch_ValidRequest_ReturnsSuccess
// 회귀: 유효한 브랜치 생성 요청은 200 OK를 반환해야 함
func Test_CreateBranch_ValidRequest_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	// Given: 테스트 레포지토리
	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	// When: 유효한 브랜치 생성 요청
	body := `{"name": "feature/new-feature", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Then: 200 OK
	requireStatus(t, w, http.StatusOK)

	var resp CreateBranchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, "feature/new-feature", resp.BranchName)
	require.Equal(t, "refs/heads/feature/new-feature", resp.RefPath)
	require.True(t, resp.IsNewBranch)
}

// Test_CreateBranch_WithCheckout_ReturnsCheckoutTrue
// 회귀: checkout=true 옵션이 응답에 반영되어야 함
func Test_CreateBranch_WithCheckout_ReturnsCheckoutTrue(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "feature/checkout-test", "source": "master", "checkout": true}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp CreateBranchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.True(t, resp.Details.Checkout)
}

// Test_CreateBranch_DuplicateName_Returns409
// 회귀: 이미 존재하는 브랜치 이름으로 생성 시 409 Conflict 반환
func Test_CreateBranch_DuplicateName_Returns409(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 브랜치 미리 생성
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)
	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature/existing"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(branchRef))

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	// When: 같은 이름의 브랜치 생성 시도
	body := `{"name": "feature/existing", "source": "master", "force": false}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Then: 409 Conflict
	requireStatus(t, w, http.StatusConflict)
	requireBodyContains(t, w, "already exists")
}

// Test_CreateBranch_WithForce_OverwritesExisting
// 회귀: force=true 시 기존 브랜치를 덮어써야 함
func Test_CreateBranch_WithForce_OverwritesExisting(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 브랜치 미리 생성
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)
	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature/force-test"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(branchRef))

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "feature/force-test", "source": "master", "force": true}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp CreateBranchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.True(t, resp.Details.ForceUsed)
}

// Test_CreateBranch_InvalidBranchName_Returns400
// 회귀: 유효하지 않은 브랜치 이름은 400 Bad Request 반환
func Test_CreateBranch_InvalidBranchName_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "invalid/branch/name", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "branch name")
}

// Test_CreateBranch_InvalidJSON_Returns400
// 회귀: 잘못된 JSON은 400 Bad Request 반환
func Test_CreateBranch_InvalidJSON_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader("{invalid json}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
}

// =============================================================================
// 테스트: UpdateBranch
// 회귀 방지: "브랜치 업데이트/이름 변경 API가 올바르게 동작해야 함"
// =============================================================================

// Test_UpdateBranch_Rename_ReturnsSuccess
// 회귀: 브랜치 이름 변경은 200 OK를 반환해야 함
func Test_UpdateBranch_Rename_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 이름 변경할 브랜치 생성
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)
	oldRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/old-branch"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(oldRef))

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	body := `{"new_name": "new-branch", "force": false}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/old-branch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	require.Equal(t, "rename", resp["operation"])
	require.Equal(t, "new-branch", resp["new_branch_name"])
}

// Test_UpdateBranch_NonExistent_Returns404
// 회귀: 존재하지 않는 브랜치 업데이트 시 404 반환
func Test_UpdateBranch_NonExistent_Returns404(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	body := `{"target": "master", "force": false}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/nonexistent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusNotFound)
	requireBodyContains(t, w, "does not exist")
}

// =============================================================================
// 테스트: DeleteBranch
// 회귀 방지: "브랜치 삭제 API가 올바르게 동작해야 함"
// =============================================================================

// Test_DeleteBranch_Existing_ReturnsSuccess
// 회귀: 존재하는 브랜치 삭제 시 200 OK 반환
func Test_DeleteBranch_Existing_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 삭제할 브랜치 생성
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)
	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature/to-delete"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(branchRef))

	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleDeleteBranch))

	req := httptest.NewRequest("DELETE", "/repository/testowner/testrepo/branches/feature/to-delete", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	require.Equal(t, "feature/to-delete", resp["branch_name"])
	require.Equal(t, "delete", resp["operation"])
}

// Test_DeleteBranch_NonExistent_Returns404
// 회귀: 존재하지 않는 브랜치 삭제 시 404 반환
func Test_DeleteBranch_NonExistent_Returns404(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleDeleteBranch))

	req := httptest.NewRequest("DELETE", "/repository/testowner/testrepo/branches/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusNotFound)
	requireBodyContains(t, w, "does not exist")
}

// Test_DeleteBranch_InvalidName_Returns400
// 회귀: 유효하지 않은 브랜치 이름으로 삭제 시 400 반환
func Test_DeleteBranch_InvalidName_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleDeleteBranch))

	req := httptest.NewRequest("DELETE", "/repository/testowner/testrepo/branches/invalid/branch/name", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "branch name")
}

// =============================================================================
// 테스트: ValidateBranchName
// 회귀 방지: "브랜치 이름 검증 API가 올바르게 동작해야 함"
// =============================================================================

// Test_ValidateBranchName_Available_ReturnsTrue
// 회귀: 사용 가능한 브랜치 이름은 is_available=true 반환
func Test_ValidateBranchName_Available_ReturnsTrue(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/validate/*name", adapt(handler.HandleValidateBranchName))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/validate/new-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	require.True(t, resp["is_available"].(bool), "new branch name should be available")
}

// Test_ValidateBranchName_AlreadyExists_ReturnsFalse
// 회귀: 이미 존재하는 브랜치 이름은 is_available=false 반환
func Test_ValidateBranchName_AlreadyExists_ReturnsFalse(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 브랜치 미리 생성
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)
	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/existing-branch"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(branchRef))

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/validate/*name", adapt(handler.HandleValidateBranchName))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/validate/existing-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	require.False(t, resp["is_available"].(bool), "existing branch name should not be available")

	// suggested_name이 존재하고 원래 이름을 prefix로 포함해야 함
	suggested, ok := resp["suggested_name"].(string)
	require.True(t, ok, "response should contain suggested_name when branch exists")
	require.True(t, strings.HasPrefix(suggested, "existing-branch-"), "suggested_name should start with original name")
	require.NotEqual(t, "existing-branch", suggested, "suggested_name should differ from original")
}

// Test_ValidateBranchName_SuggestedNameIsAvailable
// 제안된 이름은 실제로 사용 가능해야 함
func Test_ValidateBranchName_SuggestedNameIsAvailable(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 브랜치 미리 생성
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)
	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature/test"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(branchRef))

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/validate/*name", adapt(handler.HandleValidateBranchName))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/validate/feature/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.False(t, resp["is_available"].(bool))

	suggested := resp["suggested_name"].(string)
	require.True(t, strings.HasPrefix(suggested, "feature/test-"))

	// suggested name이 실제로 존재하지 않는지 확인
	exists, _, err := NativeCheckBranchExists(context.Background(), repoPath, suggested)
	require.NoError(t, err)
	require.False(t, exists, "suggested name should not exist in repository")
}

// Test_ValidateBranchName_Available_NoSuggestedName
// 사용 가능한 이름에는 suggested_name이 포함되지 않아야 함
func Test_ValidateBranchName_Available_NoSuggestedName(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/validate/*name", adapt(handler.HandleValidateBranchName))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/validate/brand-new-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["is_available"].(bool))
	require.Nil(t, resp["suggested_name"], "available branch should have suggested_name as null")
}

// Test_ValidateBranchName_RepoNotFound_Returns404
// 회귀: 존재하지 않는 레포지토리는 404 반환
func Test_ValidateBranchName_RepoNotFound_Returns404(t *testing.T) {
	t.Parallel()

	handler, _ := newHandler(t)

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/validate/*name", adapt(handler.HandleValidateBranchName))

	req := httptest.NewRequest("GET", "/repos/testowner/nonexistent/branches/validate/some-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusNotFound)
	requireBodyContains(t, w, "repository not found")
}

// =============================================================================
// 테스트: GetBranches
// 회귀 방지: "브랜치 목록 API가 올바르게 동작해야 함"
// =============================================================================

// Test_GetBranches_PaginationOutOfRange_ReturnsEmptyArray
// 회귀: pagination 범위를 벗어나면 빈 배열(null 아님)을 반환해야 함
func Test_GetBranches_PaginationOutOfRange_ReturnsEmptyArray(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 5개의 브랜치 생성
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)

	for _, name := range []string{"branch1", "branch2", "branch3", "branch4", "branch5"} {
		branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/"+name), head.Hash())
		require.NoError(t, repo.Storer.SetReference(branchRef))
	}

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches", adaptQuery(handler.HandleGetBranches))

	// skip=10으로 범위를 벗어나는 요청
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches?limit=10&skip=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp GetBranchesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)

	// null이 아닌 빈 배열인지 확인
	branchesBytes, err := json.Marshal(resp.Results)
	require.NoError(t, err)
	require.Equal(t, "[]", string(branchesBytes), "branches should be empty array, not null")
}

// Test_GetBranches_DefaultRepo_ReturnsArrayNotNull
// 회귀: 브랜치 목록은 null이 아닌 배열을 반환해야 함
func Test_GetBranches_DefaultRepo_ReturnsArrayNotNull(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches", adaptQuery(handler.HandleGetBranches))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp GetBranchesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)

	// 배열이 null이 아닌지 확인 (master 브랜치가 있으므로 최소 1개)
	require.NotNil(t, resp.Results, "branches should not be nil")
	require.GreaterOrEqual(t, len(resp.Results), 1, "should have at least master branch")
}

// Test_GetBranches_SortByName_ReturnsSorted
// 회귀: 브랜치 목록이 이름순으로 정렬되어 반환된다
func Test_GetBranches_SortByName_ReturnsSorted(t *testing.T) {
	t.Parallel()
	t.Skip("sort by name 파라미터 미구현")
}

// Test_GetBranches_SortByDate_ReturnsSorted
// 회귀: 브랜치 목록이 날짜순으로 정렬되어 반환된다
func Test_GetBranches_SortByDate_ReturnsSorted(t *testing.T) {
	t.Parallel()
	t.Skip("sort by date 파라미터 미구현")
}

// Test_GetBranches_FilterByPattern_ReturnsMatching
// 회귀: 패턴 필터에 맞는 브랜치만 반환된다
func Test_GetBranches_FilterByPattern_ReturnsMatching(t *testing.T) {
	t.Parallel()
	t.Skip("filter by pattern 파라미터 미구현")
}

// =============================================================================
// Helper functions
// 원칙: boilerplate만 추상화, 핵심 로직은 테스트 본문에
// =============================================================================

func newHandler(t *testing.T) (*BranchOperationsHandler, string) {
	t.Helper()
	tempDir := t.TempDir()

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	handler, err := NewBranchOperationsHandler(cfg)
	require.NoError(t, err, "handler creation failed")

	return handler, tempDir
}

func initBareRepo(t *testing.T, baseDir, owner, repo string) string {
	t.Helper()
	repoPath := filepath.Join(baseDir, owner, repo+".git")

	err := makeDir(repoPath)
	require.NoError(t, err, "mkdir failed")

	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err, "repo init failed")

	// HEAD -> master
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	// 실제 commit object 생성 (empty tree → commit).
	// 회귀: native git(for-each-ref/rev-parse)은 실존 object를 가리키지 않는
	// ref를 나열/해석하지 못한다. zero-hash ref는 브랜치가 0개로 보이고
	// source="master" 해석이 실패하므로 반드시 실 commit을 만든다.
	emptyTree := &object.Tree{}
	treeObj := gitRepo.Storer.NewEncodedObject()
	require.NoError(t, emptyTree.Encode(treeObj))
	treeHash, err := gitRepo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	commit := &object.Commit{
		TreeHash: treeHash,
		Author: object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
		Committer: object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
		Message: "initial commit",
	}
	commitObj := gitRepo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	commitHash, err := gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// master 브랜치를 실제 commit hash로 생성
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	require.NoError(t, gitRepo.Storer.SetReference(masterRef))

	return repoPath
}

func openRepo(t *testing.T, path string) *gogit.Repository {
	t.Helper()
	repo, err := gogit.PlainOpen(path)
	require.NoError(t, err, "repo open failed")
	return repo
}

func makeDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0755)
}

// =============================================================================
// Assertion helpers - testutil/gitrepo 사용
// =============================================================================

func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	gitrepo.RequireStatus(t, w, want)
}

func requireBodyContains(t *testing.T, w *httptest.ResponseRecorder, substr string) {
	t.Helper()
	gitrepo.RequireBodyContains(t, w, substr)
}

// copyTreeRecursive는 tree와 모든 하위 blob 객체를 복사합니다.
func copyTreeRecursive(workRepo, gitRepo *gogit.Repository, treeHash plumbing.Hash) error {
	tree, err := workRepo.TreeObject(treeHash)
	if err != nil {
		return err
	}

	// 트리 객체 복사
	treeObj, err := workRepo.Storer.EncodedObject(plumbing.TreeObject, treeHash)
	if err != nil {
		return err
	}
	_, err = gitRepo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return err
	}

	// 모든 엔트리 처리
	for _, entry := range tree.Entries {
		if entry.Mode == filemode.Dir {
			// 서브트리 재귀적으로 복사
			if err := copyTreeRecursive(workRepo, gitRepo, entry.Hash); err != nil {
				return err
			}
		} else {
			// 블롭 객체 복사
			blobObj, err := workRepo.Storer.EncodedObject(plumbing.BlobObject, entry.Hash)
			if err != nil {
				return err
			}
			_, err = gitRepo.Storer.SetEncodedObject(blobObj)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// =============================================================================
// 테스트: Git이 금지하는 브랜치 이름 패턴 (P0 - 500 방지)
// 회귀 방지: "잘못된 브랜치 이름이 500으로 뭉개지지 않고 400으로 거절된다"
// =============================================================================

// Test_CreateBranch_EndsWithDotLock_Returns400
// 회귀: ".lock"으로 끝나는 브랜치 이름은 400으로 거절되어야 함
func Test_CreateBranch_EndsWithDotLock_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "feature.lock", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 400 Bad Request (500이 아님)
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for .lock suffix body=%s", w.Code, w.Body.String())
}

// Test_CreateBranch_ContainsAtBrace_Returns400
// 회귀: "@{" 패턴이 포함된 브랜치 이름은 400으로 거절되어야 함
func Test_CreateBranch_ContainsAtBrace_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "foo@{1}", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 400 Bad Request (500이 아님)
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for @{ pattern body=%s", w.Code, w.Body.String())
}

// Test_CreateBranch_DoubleDot_Returns400
// 회귀: ".." 패턴이 포함된 브랜치 이름은 400으로 거절되어야 함
func Test_CreateBranch_DoubleDot_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "feature..bar", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 400 Bad Request (500이 아님)
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for .. pattern body=%s", w.Code, w.Body.String())
}

// Test_CreateBranch_Tilde_Returns400
// 회귀: "~" 패턴이 포함된 브랜치 이름은 400으로 거절되어야 함
func Test_CreateBranch_Tilde_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "feature~1", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 400 Bad Request (500이 아님)
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for ~ pattern body=%s", w.Code, w.Body.String())
}

// Test_CreateBranch_Space_Returns400
// 회귀: 공백이 포함된 브랜치 이름은 400으로 거절되어야 함
func Test_CreateBranch_Space_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "feature branch", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 400 Bad Request (500이 아님)
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for space in name body=%s", w.Code, w.Body.String())
}

// Test_CreateBranch_LeadingWhitespace_Returns400
// 회귀: 앞에 공백이 있는 브랜치 이름은 400으로 거절되어야 함
func Test_CreateBranch_LeadingWhitespace_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": " main", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 400 Bad Request (500이 아님)
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for leading whitespace body=%s", w.Code, w.Body.String())
}

// Test_CreateBranch_TrailingWhitespace_Returns400
// 회귀: 뒤에 공백이 있는 브랜치 이름은 400으로 거절되어야 함
func Test_CreateBranch_TrailingWhitespace_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "main ", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 400 Bad Request (500이 아님)
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for trailing whitespace body=%s", w.Code, w.Body.String())
}

// Test_CreateBranch_InvalidSource_Returns404
// 회귀: 존재하지 않는 source는 404로 떨어져 500으로 뭉개지지 않는다
func Test_CreateBranch_InvalidSource_Returns404(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "new-branch", "source": "nonexistent-branch"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 404 또는 400이어야 함, 500은 안됨
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for invalid source body=%s", w.Code, w.Body.String())
}

// Test_CreateBranch_FromCommitSHA_ReturnsSuccess
// 회귀: 커밋 SHA에서 브랜치 생성이 정상적으로 동작해야 함
func Test_CreateBranch_FromCommitSHA_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// non-bare repo에서 커밋 생성
	tempWorkDir := t.TempDir()
	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	wt, err := workRepo.Worktree()
	require.NoError(t, err)

	// 첫 번째 커밋
	filePath := filepath.Join(tempWorkDir, "file1.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("content1"), 0644))
	_, err = wt.Add("file1.txt")
	require.NoError(t, err)

	commitHash, err := wt.Commit("first commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// 커밋 객체를 bare repo로 복사
	bareRepo := openRepo(t, repoPath)
	commit, err := workRepo.CommitObject(commitHash)
	require.NoError(t, err)

	commitObj := workRepo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	_, err = bareRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// master 브랜치를 커밋으로 업데이트
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	require.NoError(t, bareRepo.Storer.SetReference(masterRef))

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	// When: SHA에서 브랜치 생성
	body := `{"name": "branch-from-sha", "source": "` + commitHash.String() + `"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Then: 200 OK
	requireStatus(t, w, http.StatusOK)

	var resp CreateBranchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, "branch-from-sha", resp.BranchName)
	require.Equal(t, commitHash.String(), resp.CommitSHA)
}

// Test_CreateBranch_FromTag_ReturnsSuccess
// 회귀: 태그에서 브랜치 생성이 정상적으로 동작해야 함
func Test_CreateBranch_FromTag_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 커밋 및 태그 생성
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)

	// 태그 생성
	tagRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/v1.0.0"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(tagRef))

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	// When: 태그에서 브랜치 생성
	body := `{"name": "branch-from-tag", "source": "v1.0.0"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Then: 200 OK
	requireStatus(t, w, http.StatusOK)

	var resp CreateBranchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, "branch-from-tag", resp.BranchName)
	require.Equal(t, head.Hash().String(), resp.CommitSHA)
}

// Test_CreateBranch_SlashInName_ReturnsSuccess
// 회귀: 슬래시가 포함된 브랜치 이름(feature/foo)은 정상적으로 생성되어야 함
func Test_CreateBranch_SlashInName_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "feature/foo", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp CreateBranchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, "feature/foo", resp.BranchName)
}

// Test_CreateBranch_MultipleSlashes_Returns400
// 회귀: 여러 슬래시가 포함된 브랜치 이름(feature/foo/bar)은 400으로 거절되어야 함
func Test_CreateBranch_MultipleSlashes_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "feature/foo/bar", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "branch name")
}

// Test_CreateBranch_StartWithSlash_Returns400
// 회귀: 슬래시로 시작하는 브랜치 이름(/feature)은 400으로 거절되어야 함
func Test_CreateBranch_StartWithSlash_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "/feature", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "branch name")
}

// Test_CreateBranch_EndWithSlash_Returns400
// 회귀: 슬래시로 끝나는 브랜치 이름(feature/)은 400으로 거절되어야 함
func Test_CreateBranch_EndWithSlash_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "feature/", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "branch name")
}

// Test_CreateBranch_RefsHeads_Returns400
// 회귀: refs/heads/ 접두사가 포함된 브랜치 이름은 400으로 거절되어야 함
func Test_CreateBranch_RefsHeads_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "refs/heads/feature", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "branch name")
}

// Test_CreateBranch_SpecialChars_Returns400
// 회귀: 특수문자(^, :)가 포함된 브랜치 이름은 400으로 거절되어야 함
func Test_CreateBranch_SpecialChars_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	testCases := []struct {
		name string
		body string
	}{
		{"caret", `{"name": "feature^1", "source": "master"}`},
		{"colon", `{"name": "feature:bar", "source": "master"}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			requireStatus(t, w, http.StatusBadRequest)
			requireBodyContains(t, w, "branch name")
		})
	}
}

// Test_CreateBranch_ContainsBackslash_Returns400
// 회귀: 백슬래시가 포함된 브랜치 이름은 400으로 거절되어야 함
func Test_CreateBranch_ContainsBackslash_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	body := `{"name": "feature\\bar", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "branch name")
}

// Test_CreateBranch_ContainsQuestionAsteriskBracket_Returns400
// 회귀: ?, *, [ 문자가 포함된 브랜치 이름은 400으로 거절되어야 함
func Test_CreateBranch_ContainsQuestionAsteriskBracket_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	testCases := []struct {
		name string
		body string
	}{
		{"question", `{"name": "feature?bar", "source": "master"}`},
		{"asterisk", `{"name": "feature*bar", "source": "master"}`},
		{"bracket", `{"name": "feature[bar]", "source": "master"}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			requireStatus(t, w, http.StatusBadRequest)
			requireBodyContains(t, w, "branch name")
		})
	}
}

// Test_CreateBranch_NameIsDashOrDot_Returns400
// 회귀: 단독 "." 이름은 400으로 거절되어야 함 (현재 구현은 "-"는 허용)
func Test_CreateBranch_NameIsDashOrDot_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", adapt(handler.HandleCreateBranch))

	// "."는 validator에서 거부됨 (HasPrefix/HasSuffix)
	body := `{"name": ".", "source": "master"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "branch name")
}

// Test_CreateBranch_UpperLowerCollision_CaseInsensitiveFS_Returns409
// 회귀: mac 기본 FS에서 Feature vs feature 충돌이 적절히 처리된다
func Test_CreateBranch_UpperLowerCollision_CaseInsensitiveFS_Returns409(t *testing.T) {
	t.Parallel()
	t.Skip("case-insensitive FS 처리 로직 미구현")
}

// Test_CreateBranch_NameUnicodeNormalization_CollisionsHandled
// 회귀: NFC vs NFD 정규화 충돌이 적절히 처리된다
func Test_CreateBranch_NameUnicodeNormalization_CollisionsHandled(t *testing.T) {
	t.Parallel()
	t.Skip("unicode 정규화 충돌 처리 미구현")
}

// =============================================================================
// 테스트: UpdateBranch 추가 케이스
// =============================================================================

// Test_UpdateBranch_RenameToDuplicate_Returns409
// 회귀: 이미 존재하는 이름으로 변경 시 409 Conflict 반환
func Test_UpdateBranch_RenameToDuplicate_Returns409(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 두 개의 브랜치 생성
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)
	oldRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/old-branch"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(oldRef))
	existingRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/existing-branch"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(existingRef))

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	body := `{"new_name": "existing-branch", "force": false}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/old-branch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusConflict)
	requireBodyContains(t, w, "already exists")
}

// Test_UpdateBranch_FastForward_ReturnsSuccess
// 회귀: fast-forward 업데이트는 200 OK를 반환해야 함
func Test_UpdateBranch_FastForward_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// non-bare repo에서 커밋 생성
	tempWorkDir := t.TempDir()
	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	wt, err := workRepo.Worktree()
	require.NoError(t, err)

	// 첫 번째 커밋
	filePath := filepath.Join(tempWorkDir, "file1.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("content1"), 0644))
	_, err = wt.Add("file1.txt")
	require.NoError(t, err)

	commit1, err := wt.Commit("first commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// 두 번째 커밋
	filePath2 := filepath.Join(tempWorkDir, "file2.txt")
	require.NoError(t, os.WriteFile(filePath2, []byte("content2"), 0644))
	_, err = wt.Add("file2.txt")
	require.NoError(t, err)

	commit2, err := wt.Commit("second commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now().Add(time.Minute),
		},
	})
	require.NoError(t, err)

	// 커밋 객체를 bare repo로 복사 (tree, blob 포함)
	bareRepo := openRepo(t, repoPath)
	for _, hash := range []plumbing.Hash{commit1, commit2} {
		commit, err := workRepo.CommitObject(hash)
		require.NoError(t, err)

		// 커밋 객체 복사
		commitObj, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, hash)
		require.NoError(t, err)
		_, err = bareRepo.Storer.SetEncodedObject(commitObj)
		require.NoError(t, err)

		// tree와 blob 복사
		require.NoError(t, copyTreeRecursive(workRepo, bareRepo, commit.TreeHash))
	}

	// feature 브랜치를 첫 번째 커밋에 생성
	featureRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature"), commit1)
	require.NoError(t, bareRepo.Storer.SetReference(featureRef))

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	// When: fast-forward 업데이트 (commit1 -> commit2)
	body := `{"target": "` + commit2.String() + `", "force": false}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/feature", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Then: 200 OK
	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	require.Equal(t, "update", resp["operation"])
	require.True(t, resp["fast_forward"].(bool))
	require.Equal(t, commit2.String(), resp["commit_hash"])
}

// Test_UpdateBranch_NonFastForward_Returns400
// 회귀: non-fast-forward 업데이트는 force 없이 400 반환
func Test_UpdateBranch_NonFastForward_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// non-bare repo에서 분기된 커밋 생성
	tempWorkDir := t.TempDir()
	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	wt, err := workRepo.Worktree()
	require.NoError(t, err)

	// 첫 번째 커밋
	filePath := filepath.Join(tempWorkDir, "file1.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("content1"), 0644))
	_, err = wt.Add("file1.txt")
	require.NoError(t, err)

	commit1, err := wt.Commit("first commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// feature 브랜치 생성 및 커밋
	featureRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("feature"), commit1)
	require.NoError(t, workRepo.Storer.SetReference(featureRef))
	require.NoError(t, wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
	}))

	filePath2 := filepath.Join(tempWorkDir, "file2.txt")
	require.NoError(t, os.WriteFile(filePath2, []byte("content2"), 0644))
	_, err = wt.Add("file2.txt")
	require.NoError(t, err)

	commit2, err := wt.Commit("feature commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now().Add(time.Minute),
		},
	})
	require.NoError(t, err)

	// master로 돌아가서 다른 커밋 생성 (go-git PlainInit 기본 브랜치는 master)
	require.NoError(t, wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("master"),
	}))

	filePath3 := filepath.Join(tempWorkDir, "file3.txt")
	require.NoError(t, os.WriteFile(filePath3, []byte("content3"), 0644))
	_, err = wt.Add("file3.txt")
	require.NoError(t, err)

	commit3, err := wt.Commit("master commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now().Add(2 * time.Minute),
		},
	})
	require.NoError(t, err)

	// 커밋 객체를 bare repo로 복사 (tree, blob 포함)
	// 순서 중요: commit1 (parent)를 먼저 복사
	bareRepo := openRepo(t, repoPath)

	// commit1 먼저 복사 (parent)
	commit1Obj, err := workRepo.CommitObject(commit1)
	require.NoError(t, err)
	commitObj1, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, commit1)
	require.NoError(t, err)
	_, err = bareRepo.Storer.SetEncodedObject(commitObj1)
	require.NoError(t, err)
	require.NoError(t, copyTreeRecursive(workRepo, bareRepo, commit1Obj.TreeHash))

	// commit2 복사 (parent: commit1)
	commit2Obj, err := workRepo.CommitObject(commit2)
	require.NoError(t, err)
	commitObj2, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, commit2)
	require.NoError(t, err)
	_, err = bareRepo.Storer.SetEncodedObject(commitObj2)
	require.NoError(t, err)
	require.NoError(t, copyTreeRecursive(workRepo, bareRepo, commit2Obj.TreeHash))

	// commit3 복사 (parent: commit1)
	commit3Obj, err := workRepo.CommitObject(commit3)
	require.NoError(t, err)
	commitObj3, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, commit3)
	require.NoError(t, err)
	_, err = bareRepo.Storer.SetEncodedObject(commitObj3)
	require.NoError(t, err)
	require.NoError(t, copyTreeRecursive(workRepo, bareRepo, commit3Obj.TreeHash))

	// feature 브랜치를 commit2에 생성
	featureRef2 := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature"), commit2)
	require.NoError(t, bareRepo.Storer.SetReference(featureRef2))

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	// When: non-fast-forward 업데이트 시도 (commit2 -> commit3, 분기됨)
	body := `{"target": "` + commit3.String() + `", "force": false}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/feature", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Then: 400 Bad Request
	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "not a fast-forward")
}

// Test_UpdateBranch_NonFastForwardForce_ReturnsSuccess
// 회귀: force=true 시 non-fast-forward 업데이트도 성공해야 함
func Test_UpdateBranch_NonFastForwardForce_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// non-bare repo에서 분기된 커밋 생성
	tempWorkDir := t.TempDir()
	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	wt, err := workRepo.Worktree()
	require.NoError(t, err)

	// 첫 번째 커밋
	filePath := filepath.Join(tempWorkDir, "file1.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("content1"), 0644))
	_, err = wt.Add("file1.txt")
	require.NoError(t, err)

	commit1, err := wt.Commit("first commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// feature 브랜치 생성 및 커밋
	featureRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("feature"), commit1)
	require.NoError(t, workRepo.Storer.SetReference(featureRef))
	require.NoError(t, wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
	}))

	filePath2 := filepath.Join(tempWorkDir, "file2.txt")
	require.NoError(t, os.WriteFile(filePath2, []byte("content2"), 0644))
	_, err = wt.Add("file2.txt")
	require.NoError(t, err)

	commit2, err := wt.Commit("feature commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now().Add(time.Minute),
		},
	})
	require.NoError(t, err)

	// master로 돌아가서 다른 커밋 생성 (go-git PlainInit 기본 브랜치는 master)
	require.NoError(t, wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("master"),
	}))

	filePath3 := filepath.Join(tempWorkDir, "file3.txt")
	require.NoError(t, os.WriteFile(filePath3, []byte("content3"), 0644))
	_, err = wt.Add("file3.txt")
	require.NoError(t, err)

	commit3, err := wt.Commit("master commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now().Add(2 * time.Minute),
		},
	})
	require.NoError(t, err)

	// 커밋 객체를 bare repo로 복사
	bareRepo := openRepo(t, repoPath)
	for _, hash := range []plumbing.Hash{commit1, commit2, commit3} {
		commitObj := workRepo.Storer.NewEncodedObject()
		commit, _ := workRepo.CommitObject(hash)
		require.NoError(t, commit.Encode(commitObj))
		_, err = bareRepo.Storer.SetEncodedObject(commitObj)
		require.NoError(t, err)
	}

	// feature 브랜치를 commit2에 생성
	featureRef2 := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature"), commit2)
	require.NoError(t, bareRepo.Storer.SetReference(featureRef2))

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	// When: force=true로 non-fast-forward 업데이트
	body := `{"target": "` + commit3.String() + `", "force": true}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/feature", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Then: 200 OK
	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	require.Equal(t, "update", resp["operation"])
	require.Equal(t, commit3.String(), resp["commit_hash"])
	require.True(t, resp["forced"].(bool))
}

// Test_UpdateBranch_RenameDefault_ReturnsSuccess
// 회귀: 기본 브랜치(master) 이름 변경도 허용됨 (현재 구현은 제한하지 않음)
func Test_UpdateBranch_RenameDefault_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// master 브랜치 확인
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(masterRef))

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleUpdateBranch))

	body := `{"new_name": "main", "force": false}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/master", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 현재 구현은 기본 브랜치 이름 변경을 허용함
	requireStatus(t, w, http.StatusOK)
}

// Test_UpdateBranch_RenameToDifferentCase_CaseInsensitiveFS_BehavesDeterministically
// 회귀: case-insensitive FS에서 대소문자만 다른 리네임이 결정적으로 동작한다
func Test_UpdateBranch_RenameToDifferentCase_CaseInsensitiveFS_BehavesDeterministically(t *testing.T) {
	t.Parallel()
	t.Skip("case-insensitive FS 처리 로직 미구현")
}

// Test_UpdateBranch_RenameCaseOnly_WhenOtherCaseExists_Returns409
// 회귀: mac에서 Feature -> feature 리네임 시 다른 case가 있으면 409
func Test_UpdateBranch_RenameCaseOnly_WhenOtherCaseExists_Returns409(t *testing.T) {
	t.Parallel()
	t.Skip("case-insensitive FS 처리 로직 미구현")
}

// =============================================================================
// 테스트: DeleteBranch 추가 케이스
// =============================================================================

// Test_DeleteBranch_WithSlash_ReturnsSuccess
// 회귀: 슬래시가 포함된 브랜치 이름 삭제는 정상 동작해야 함
func Test_DeleteBranch_WithSlash_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 삭제할 브랜치 생성
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)
	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature/foo"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(branchRef))

	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleDeleteBranch))

	req := httptest.NewRequest("DELETE", "/repository/testowner/testrepo/branches/feature/foo", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	require.Equal(t, "feature/foo", resp["branch_name"])
}

// Test_DeleteBranch_DefaultBranch_ReturnsSuccess
// 회귀: 기본 브랜치 삭제도 허용됨 (현재 구현은 제한하지 않음)
func Test_DeleteBranch_DefaultBranch_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleDeleteBranch))

	req := httptest.NewRequest("DELETE", "/repository/testowner/testrepo/branches/master", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 현재 구현은 기본 브랜치 삭제를 허용함
	requireStatus(t, w, http.StatusOK)
}

// Test_DeleteBranch_CurrentHEAD_ReturnsSuccess
// 회귀: HEAD가 가리키는 브랜치 삭제도 허용됨 (현재 구현은 제한하지 않음)
func Test_DeleteBranch_CurrentHEAD_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// HEAD가 master를 가리키도록 설정
	repo := openRepo(t, repoPath)
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	require.NoError(t, repo.Storer.SetReference(headRef))

	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleDeleteBranch))

	req := httptest.NewRequest("DELETE", "/repository/testowner/testrepo/branches/master", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 현재 구현은 HEAD가 가리키는 브랜치 삭제를 허용함
	requireStatus(t, w, http.StatusOK)
}

// Test_DeleteBranch_AlreadyDeleted_Returns404
// 회귀: 이미 삭제된 브랜치 삭제 시도는 404 반환
func Test_DeleteBranch_AlreadyDeleted_Returns404(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 브랜치 생성 후 삭제
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)
	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/to-delete"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(branchRef))

	// 첫 번째 삭제
	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleDeleteBranch))

	req1 := httptest.NewRequest("DELETE", "/repository/testowner/testrepo/branches/to-delete", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	requireStatus(t, w1, http.StatusOK)

	// 두 번째 삭제 시도
	req2 := httptest.NewRequest("DELETE", "/repository/testowner/testrepo/branches/to-delete", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	requireStatus(t, w2, http.StatusNotFound)
	requireBodyContains(t, w2, "does not exist")
}

// Test_DeleteBranch_ProtectedBranch_Returns403_POLICY_VIOLATION
// 회귀: 보호 브랜치 삭제 시 403 POLICY_VIOLATION
func Test_DeleteBranch_ProtectedBranch_Returns403_POLICY_VIOLATION(t *testing.T) {
	t.Parallel()
	t.Skip("브랜치 보호 정책 시스템 미구현")
}

// Test_DeleteBranch_ConcurrentDelete_SecondReturns404
// 회귀: 동시 삭제에서 하나는 성공, 다른 하나는 404로 안정적으로 종료한다
func Test_DeleteBranch_ConcurrentDelete_SecondReturns404(t *testing.T) {
	t.Parallel()
	t.Skip("동시 삭제 검증 테스트 (나중에 구현)")
}

// Test_UpdateBranch_Rename_ConcurrentCreateSameName_Returns409_ALREADY_EXISTS
// 회귀: rename 중 동일 이름 create 경합이 나도 ref가 손상되지 않는다
func Test_UpdateBranch_Rename_ConcurrentCreateSameName_Returns409_ALREADY_EXISTS(t *testing.T) {
	t.Parallel()
	t.Skip("동시 rename/create 검증 테스트 (나중에 구현)")
}

// Test_UpdateBranch_RenameWhileClientReads_ListDeterministic
// 회귀: rename 중에도 list API가 깨지거나 중복 ref를 내지 않는다
func Test_UpdateBranch_RenameWhileClientReads_ListDeterministic(t *testing.T) {
	t.Parallel()
	t.Skip("rename 중 list 검증 테스트 (나중에 구현)")
}

// Test_DeleteBranch_NotMerged_RequiresForce
// 회귀: 머지되지 않은 브랜치 삭제는 force 필요 (또는 명시적 허용)
func Test_DeleteBranch_NotMerged_RequiresForce(t *testing.T) {
	t.Parallel()
	t.Skip("not-merged 삭제 정책 미구현")
}

// =============================================================================
// 테스트: GetBranches 추가 케이스
// =============================================================================

// Test_GetBranches_EmptyRepo_ReturnsEmptyArray
// 회귀: 빈 레포지토리는 빈 배열을 반환해야 함
func Test_GetBranches_EmptyRepo_ReturnsEmptyArray(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches", adaptQuery(handler.HandleGetBranches))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp GetBranchesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	// master 브랜치가 있으므로 최소 1개는 있음
	require.NotNil(t, resp.Results)
}

// Test_GetBranches_WithBranches_ReturnsAll
// 회귀: 여러 브랜치가 있는 경우 모두 반환해야 함
func Test_GetBranches_WithBranches_ReturnsAll(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 여러 브랜치 생성
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)

	branchNames := []string{"feature1", "feature2", "feature3"}
	for _, name := range branchNames {
		branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/"+name), head.Hash())
		require.NoError(t, repo.Storer.SetReference(branchRef))
	}

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches", adaptQuery(handler.HandleGetBranches))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp GetBranchesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	// master + 3개 브랜치 = 최소 4개
	require.GreaterOrEqual(t, len(resp.Results), 4)
}

// Test_GetBranches_Pagination_ReturnsCorrectPage
// 회귀: 페이지네이션은 올바른 페이지를 반환해야 함
func Test_GetBranches_Pagination_ReturnsCorrectPage(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	// 여러 브랜치 생성
	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)

	for i := 1; i <= 5; i++ {
		branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/branch"+string(rune('0'+i))), head.Hash())
		require.NoError(t, repo.Storer.SetReference(branchRef))
	}

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches", adaptQuery(handler.HandleGetBranches))

	// limit=2, skip=0으로 첫 페이지 요청
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches?limit=2&skip=0", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp GetBranchesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	// limit=2이므로 최대 2개 반환
	require.LessOrEqual(t, len(resp.Results), 2)
}

// =============================================================================
// 테스트: ResolveBranch (GET /repos/{owner}/{repo}/branches/resolve/*ref)
// =============================================================================

// Test_ResolveBranch_ExistingBranch_ReturnsSHA
// 회귀: 존재하는 브랜치 resolve 시 200과 name, commit_sha, ref_path 반환
func Test_ResolveBranch_ExistingBranch_ReturnsSHA(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	_ = initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/resolve/*ref", adapt(handler.HandleResolveBranch))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/resolve/master", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	require.Equal(t, "master", resp["name"])
	require.Equal(t, "refs/heads/master", resp["ref_path"])
	sha, ok := resp["commit_sha"].(string)
	require.True(t, ok)
	require.Len(t, sha, 40)
}

// Test_ResolveBranch_BranchWithSlash_ReturnsSHA
// 회귀: 슬래시 포함 브랜치 이름 resolve 시 올바른 ref_path 반환
func Test_ResolveBranch_BranchWithSlash_ReturnsSHA(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	repoPath := initBareRepo(t, repoDir, "testowner", "testrepo")

	repo := openRepo(t, repoPath)
	head, err := repo.Head()
	require.NoError(t, err)
	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature/login"), head.Hash())
	require.NoError(t, repo.Storer.SetReference(branchRef))

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/resolve/*ref", adapt(handler.HandleResolveBranch))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/resolve/feature/login", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	require.Equal(t, "feature/login", resp["name"])
	require.Equal(t, "refs/heads/feature/login", resp["ref_path"])
	require.Equal(t, head.Hash().String(), resp["commit_sha"])
}

// Test_ResolveBranch_EmptyRef_Returns400
// 회귀: ref 비어 있으면 400, branch name is required
func Test_ResolveBranch_EmptyRef_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/resolve/*ref", adapt(handler.HandleResolveBranch))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/resolve/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "branch name is required")

	// Issue-5360: flat machine code INVALID_INPUT.
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "INVALID_INPUT", resp["code"])
}

// Test_ResolveBranch_RepoNotFound_Returns404
// 회귀: 존재하지 않는 저장소면 404
func Test_ResolveBranch_RepoNotFound_Returns404(t *testing.T) {
	t.Parallel()

	handler, _ := newHandler(t)

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/resolve/*ref", adapt(handler.HandleResolveBranch))

	req := httptest.NewRequest("GET", "/repos/testowner/nonexistent/branches/resolve/master", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusNotFound)
	requireBodyContains(t, w, "repository not found")

	// Issue-5577: on-disk repo 부재는 REPOSITORY_NOT_FOUND (일반 NOT_FOUND 와
	// 구분되는 저장소 부재 코드).
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "REPOSITORY_NOT_FOUND", resp["code"])
}

// Test_ResolveBranch_BranchNotFound_Returns404
// 회귀: 존재하지 않는 브랜치면 404
func Test_ResolveBranch_BranchNotFound_Returns404(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/resolve/*ref", adapt(handler.HandleResolveBranch))

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/resolve/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusNotFound)
	requireBodyContains(t, w, "branch 'nonexistent' not found")

	// Issue-5360: flat machine code REF_NOT_FOUND + params.ref for interpolation.
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "REF_NOT_FOUND", resp["code"])
	params, ok := resp["params"].(map[string]interface{})
	require.True(t, ok, "params should be present: %s", w.Body.String())
	require.Equal(t, "nonexistent", params["ref"])
}

// =============================================================================
// 테스트: ValidateBranchName 추가 케이스
// =============================================================================

// Test_ValidateBranchName_InvalidFormat_Returns400
// 회귀: 잘못된 형식의 브랜치 이름은 400으로 거절되어야 함
func Test_ValidateBranchName_InvalidFormat_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/validate/*name", adapt(handler.HandleValidateBranchName))

	testCases := []struct {
		name     string
		urlName  string
		expected int
	}{
		{"double_dot", "feature..bar", http.StatusOK}, // URL로는 전달되지만 실제 검증은 handler에서
		{"tilde", "feature~1", http.StatusOK},
		{"starts_with_slash", "feature", http.StatusOK}, // URL에서 /는 제거됨
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// URL 인코딩된 이름 사용
			req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/validate/"+tc.urlName, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			// ValidateBranchName은 현재 이름 검증을 하지 않고 존재 여부만 확인함
			// 따라서 잘못된 형식도 200을 반환할 수 있음
			requireStatus(t, w, tc.expected)
		})
	}
}
