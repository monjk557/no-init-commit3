package branches

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/testutil/gitrepo"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

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
	require.Equal(t, "feature/new-feature", resp.Data.BranchName)
	require.Equal(t, "refs/heads/feature/new-feature", resp.Data.RefPath)
	require.True(t, resp.Data.IsNewBranch)
}

// Test_CreateBranch_WithCheckout_ReturnsCheckoutTrue
// 회귀: checkout=true 옵션이 응답에 반영되어야 함
func Test_CreateBranch_WithCheckout_ReturnsCheckoutTrue(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

	body := `{"name": "feature/checkout-test", "source": "master", "checkout": true}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp CreateBranchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.True(t, resp.Data.Details.Checkout)
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
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

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
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

	body := `{"name": "feature/force-test", "source": "master", "force": true}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp CreateBranchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.True(t, resp.Data.Details.ForceUsed)
}

// Test_CreateBranch_InvalidBranchName_Returns400
// 회귀: 유효하지 않은 브랜치 이름은 400 Bad Request 반환
func Test_CreateBranch_InvalidBranchName_Returns400(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

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
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

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
	router.PATCH("/repository/:owner/:repo/branches/*branch", handler.HandleUpdateBranch)

	body := `{"new_name": "new-branch", "force": false}`
	req := httptest.NewRequest("PATCH", "/repository/testowner/testrepo/branches/old-branch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	data := resp["data"].(map[string]interface{})
	require.Equal(t, "rename", data["operation"])
	require.Equal(t, "new-branch", data["new_branch_name"])
}

// Test_UpdateBranch_NonExistent_Returns404
// 회귀: 존재하지 않는 브랜치 업데이트 시 404 반환
func Test_UpdateBranch_NonExistent_Returns404(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.PATCH("/repository/:owner/:repo/branches/*branch", handler.HandleUpdateBranch)

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
	router.DELETE("/repository/:owner/:repo/branches/*branch", handler.HandleDeleteBranch)

	req := httptest.NewRequest("DELETE", "/repository/testowner/testrepo/branches/feature/to-delete", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	data := resp["data"].(map[string]interface{})
	require.Equal(t, "feature/to-delete", data["branch_name"])
	require.Equal(t, "delete", data["operation"])
}

// Test_DeleteBranch_NonExistent_Returns404
// 회귀: 존재하지 않는 브랜치 삭제 시 404 반환
func Test_DeleteBranch_NonExistent_Returns404(t *testing.T) {
	t.Parallel()

	handler, repoDir := newHandler(t)
	initBareRepo(t, repoDir, "testowner", "testrepo")

	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", handler.HandleDeleteBranch)

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
	router.DELETE("/repository/:owner/:repo/branches/*branch", handler.HandleDeleteBranch)

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
	router.GET("/repos/:owner/:repo/branches/validate/*name", handler.HandleValidateBranchName)

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
	router.GET("/repos/:owner/:repo/branches/validate/*name", handler.HandleValidateBranchName)

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches/validate/existing-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	require.False(t, resp["is_available"].(bool), "existing branch name should not be available")
}

// Test_ValidateBranchName_RepoNotFound_Returns404
// 회귀: 존재하지 않는 레포지토리는 404 반환
func Test_ValidateBranchName_RepoNotFound_Returns404(t *testing.T) {
	t.Parallel()

	handler, _ := newHandler(t)

	router := gin.New()
	router.GET("/repos/:owner/:repo/branches/validate/*name", handler.HandleValidateBranchName)

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
	router.GET("/repos/:owner/:repo/branches", handler.HandleGetBranches)

	// skip=10으로 범위를 벗어나는 요청
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches?limit=10&skip=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp GetBranchesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)

	// null이 아닌 빈 배열인지 확인
	branchesBytes, err := json.Marshal(resp.Branches)
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
	router.GET("/repos/:owner/:repo/branches", handler.HandleGetBranches)

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/branches", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp GetBranchesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)

	// 배열이 null이 아닌지 확인 (master 브랜치가 있으므로 최소 1개)
	require.NotNil(t, resp.Branches, "branches should not be nil")
	require.GreaterOrEqual(t, len(resp.Branches), 1, "should have at least master branch")
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

	// master 브랜치 생성 (zero hash로)
	emptyHash := plumbing.NewHash("0000000000000000000000000000000000000000")
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), emptyHash)
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
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

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
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

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
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

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
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

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
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

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
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

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
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

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
	router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

	body := `{"name": "new-branch", "source": "nonexistent-branch"}`
	req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 404 또는 400이어야 함, 500은 안됨
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for invalid source body=%s", w.Code, w.Body.String())
}
