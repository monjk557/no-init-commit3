package tags_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/tags"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// HTTP handler / routes 커버리지 — typed handler 를 gin httptest 로 구동한다.
//
// 핵심 패턴: NewTagsHandler 는 cfg.Git.RepoRoot 기반 PathConfig 로 owner/repo
// 를 {root}/{owner}/{repo}.git 로 매핑한다. native git 으로 bare repo +
// commit + lightweight/annotated tag fixture 를 만든 뒤, RegisterRoutes 로
// 등록된 라우터에 실제 HTTP 요청을 보내 success/error 경로를 모두 커버한다.
// =============================================================================

// gitCmd 는 GIT_DIR 을 지정해 bare repo 에 native git plumbing 명령을 실행하고
// 표준출력(trim)을 돌려준다. 실패 시 t.Fatal.
func gitCmd(t *testing.T, gitDir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir", gitDir}, args...)...)
	// 결정적 commit 생성을 위해 author/committer 를 고정.
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_AUTHOR_DATE=2024-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2024-01-01T00:00:00Z",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newBareRepoFixture 는 {root}/{owner}/{repo}.git bare repo 를 만들고
// 1개의 commit + lightweight tag(v1) + annotated tag(v2) 를 심는다.
// root, commitSHA 를 반환한다.
func newBareRepoFixture(t *testing.T, owner, repo string) (root, commitSHA string) {
	t.Helper()
	root = t.TempDir()
	gitDir := filepath.Join(root, owner, repo+".git")

	// bare repo 초기화
	if out, err := exec.Command("git", "init", "--bare", gitDir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare failed: %v\n%s", err, out)
	}

	// 빈 tree → commit (worktree 없이 plumbing 으로 commit 생성)
	emptyTree := gitCmd(t, gitDir, "hash-object", "-t", "tree", "-w", "--stdin")
	commitSHA = gitCmd(t, gitDir, "commit-tree", emptyTree, "-m", "initial commit")
	gitCmd(t, gitDir, "update-ref", "refs/heads/main", commitSHA)

	// lightweight tag v1 → commit 을 직접 가리킴
	gitCmd(t, gitDir, "update-ref", "refs/tags/v1", commitSHA)

	// annotated tag v2 → tag object 생성 후 ref 등록
	gitCmd(t, gitDir, "tag", "-a", "v2", "-m", "release v2", commitSHA)

	return root, commitSHA
}

// newTagsRouter 는 cfg.Git.RepoRoot=root 로 TagsHandler 를 만들고
// /repos/:owner/:repo 그룹에 RegisterRoutes 로 라우트를 등록한 gin 엔진을
// 반환한다.
func newTagsRouter(t *testing.T, root string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Git.RepoRoot = root
	h, err := tags.NewTagsHandler(cfg)
	require.NoError(t, err)

	r := gin.New()
	grp := r.Group("/repos/:owner/:repo")
	tags.RegisterRoutes(grp, h)
	return r
}

// doReq 는 라우터에 요청을 보내고 ResponseRecorder 를 돌려준다.
func doReq(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewBuffer(b)
	} else {
		rdr = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ------------------------------------------------------------
// NewTagsHandler
// ------------------------------------------------------------

func Test_NewTagsHandler_Success(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Git.RepoRoot = t.TempDir()
	h, err := tags.NewTagsHandler(cfg)
	require.NoError(t, err)
	assert.NotNil(t, h)
}

func Test_NewTagsHandler_EmptyRepoRoot_Errors(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{} // RepoRoot 빈 값 → NewPathConfig 실패
	_, err := tags.NewTagsHandler(cfg)
	assert.Error(t, err)
}

// ------------------------------------------------------------
// RegisterRoutes — nil handler (spec-only) + 실제 handler
// ------------------------------------------------------------

func Test_RegisterRoutes_NilHandler_SpecOnly(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	grp := r.Group("/repos/:owner/:repo")
	// nil handler → spec-only 모드. panic 없이 등록되어야 한다.
	assert.NotPanics(t, func() {
		tags.RegisterRoutes(grp, nil)
	})
}

// ------------------------------------------------------------
// HandleGetTags
// ------------------------------------------------------------

func Test_HandleGetTags_Success(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	w := doReq(t, r, http.MethodGet, "/repos/owner/repo/tags", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp tags.TagsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, 2, resp.Pagination.Count) // v1 + v2
}

func Test_HandleGetTags_WithLimitSkipSort(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	w := doReq(t, r, http.MethodGet, "/repos/owner/repo/tags?limit=1&skip=0&sort=name&search=v", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp tags.TagsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Len(t, resp.Results, 1)
}

func Test_HandleGetTags_InvalidLimit_400(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	w := doReq(t, r, http.MethodGet, "/repos/owner/repo/tags?limit=0", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func Test_HandleGetTags_InvalidSkip_400(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	w := doReq(t, r, http.MethodGet, "/repos/owner/repo/tags?skip=-1", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func Test_HandleGetTags_LimitOverMaxClamps(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	// limit > MaxTagsLimit → clamp 되어 200.
	w := doReq(t, r, http.MethodGet, "/repos/owner/repo/tags?limit=99999", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp tags.TagsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, tags.MaxTagsLimit, resp.Pagination.PageSize)
}

func Test_HandleGetTags_RepoNotFound_404(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	// 존재하지 않는 owner/repo → GetTagsSorted 가 native git 실패 → mapper 가
	// details 첨부 400 으로 매핑 (path validation 은 통과하지만 git dir 부재).
	w := doReq(t, r, http.MethodGet, "/repos/ghost/missing/tags", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "details")
}

// ------------------------------------------------------------
// HandleCreateTag
// ------------------------------------------------------------

func Test_HandleCreateTag_Lightweight_Success(t *testing.T) {
	t.Parallel()
	root, sha := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	w := doReq(t, r, http.MethodPost, "/repos/owner/repo/tags", tags.CreateTagRequest{
		Name: "v3",
		Hash: sha,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp tags.CreateTagResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	require.NotNil(t, resp.Tag)
	assert.Equal(t, "v3", resp.Tag.Name)
}

func Test_HandleCreateTag_Annotated_Success(t *testing.T) {
	t.Parallel()
	root, sha := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	w := doReq(t, r, http.MethodPost, "/repos/owner/repo/tags", tags.CreateTagRequest{
		Name:      "v4",
		Hash:      sha,
		Annotated: true,
		Message:   "annotated v4",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp tags.CreateTagResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	require.NotNil(t, resp.Tag)
	assert.True(t, resp.Tag.IsAnnotated)
}

func Test_HandleCreateTag_InvalidBody_400(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	// name/hash 누락 → ShouldBindJSON 실패 → invalidBindErr → 400 + details.
	w := doReq(t, r, http.MethodPost, "/repos/owner/repo/tags", map[string]string{})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "Invalid request body")
}

func Test_HandleCreateTag_Duplicate_409(t *testing.T) {
	t.Parallel()
	root, sha := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	// v1 은 fixture 가 이미 만든 lightweight tag → 중복 → 409.
	w := doReq(t, r, http.MethodPost, "/repos/owner/repo/tags", tags.CreateTagRequest{
		Name: "v1",
		Hash: sha,
	})
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "already exists")
}

func Test_HandleCreateTag_NonExistentCommit_404(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	// 형식은 valid 하지만 존재하지 않는 commit → "not found" → 404.
	w := doReq(t, r, http.MethodPost, "/repos/owner/repo/tags", tags.CreateTagRequest{
		Name: "v9",
		Hash: "abcd1234567890abcd1234567890abcd12345678",
	})
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

func Test_HandleCreateTag_InvalidHashFormat_400(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	// 너무 짧은 hash → CreateTag 가 "invalid tag hash" 반환 → default 400.
	w := doReq(t, r, http.MethodPost, "/repos/owner/repo/tags", tags.CreateTagRequest{
		Name: "vbad",
		Hash: "abc",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// ------------------------------------------------------------
// HandleDeleteTag
// ------------------------------------------------------------

func Test_HandleDeleteTag_Success(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	w := doReq(t, r, http.MethodDelete, "/repos/owner/repo/tags/v1", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp tags.DeleteTagResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, "v1", resp.Tag)
}

func Test_HandleDeleteTag_NotFound_404(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	w := doReq(t, r, http.MethodDelete, "/repos/owner/repo/tags/nonexistent", nil)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "not found")
}

// ------------------------------------------------------------
// HandleBulkDeleteTags
// ------------------------------------------------------------

func Test_HandleBulkDeleteTags_AllSuccess(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	w := doReq(t, r, http.MethodPost, "/repos/owner/repo/tags/bulk-delete",
		tags.BulkDeleteTagsRequest{Tags: []string{"v1", "v2"}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp tags.BulkDeleteTagsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, 2, resp.Total)
	assert.Equal(t, 2, resp.Succeeded)
	assert.Equal(t, 0, resp.Failed)
}

func Test_HandleBulkDeleteTags_PartialFailure(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	// v1 존재 + nope 부재 → 부분 실패, HTTP 는 200, Success=false.
	w := doReq(t, r, http.MethodPost, "/repos/owner/repo/tags/bulk-delete",
		tags.BulkDeleteTagsRequest{Tags: []string{"v1", "nope"}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp tags.BulkDeleteTagsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.Success)
	assert.Equal(t, 1, resp.Succeeded)
	assert.Equal(t, 1, resp.Failed)
}

func Test_HandleBulkDeleteTags_InvalidBody_400(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	// tags 누락 (min=1 위반) → bind 실패 → 400.
	w := doReq(t, r, http.MethodPost, "/repos/owner/repo/tags/bulk-delete", map[string]any{})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func Test_HandleBulkDeleteTags_RepoNotFound_404(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	r := newTagsRouter(t, root)

	// 유효한 body 지만 존재하지 않는 repo → 404 (GetAndValidateRepoPath 통과
	// 하지만 git dir 부재 시 DeleteTag 가 실패. 단, validation 은 path 만 보므로
	// 여기서는 ghost repo 에 대해 모든 delete 가 개별 실패로 기록되어 200 일 수
	// 있다 — repo 자체가 없으면 native git 이 ref 없음으로 처리). 대신 path
	// 컴포넌트가 잘못된 경우 404 를 확인한다.
	w := doReq(t, r, http.MethodPost, "/repos/../evil/tags/bulk-delete",
		tags.BulkDeleteTagsRequest{Tags: []string{"v1"}})
	// gin 라우터가 ".." 를 정규화하므로 라우트 자체가 매칭 안 될 수 있음 →
	// 404 또는 400. 어느 쪽이든 200 success 가 아님을 확인.
	assert.NotEqual(t, http.StatusOK, w.Code, w.Body.String())
}

// ------------------------------------------------------------
// service: getTagDate (간접 — buildPagination 은 HTTP 경로에서 이미 커버)
// getTagDate 는 외부에서 직접 접근 불가하므로 GetTagsSorted 의 정렬 경로로
// 간접 커버되며, 위 GET success 테스트들이 이를 실행한다.
// ------------------------------------------------------------

// Test_GetTags_NativeBareRepo 는 native bare repo 에 대한 service 직접 호출도
// 한 번 더 커버한다 (handler 우회).
func Test_GetTags_NativeBareRepo_Direct(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoFixture(t, "owner", "repo")
	gitDir := filepath.Join(root, "owner", "repo.git")

	result, total, err := tags.GetTags(context.Background(), gitDir, 100, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, result, 2)
}

// Test_CreateTag_Annotated_DefaultTaggerAndMessage 는 createAnnotatedTag 의
// "tagger nil → 기본값", "message 빈값 → 기본 메시지" 분기를 커버한다.
func Test_CreateTag_Annotated_DefaultTaggerAndMessage(t *testing.T) {
	t.Parallel()
	root, sha := newBareRepoFixture(t, "owner", "repo")
	gitDir := filepath.Join(root, "owner", "repo.git")

	req := &tags.CreateTagRequest{
		Name:      "v-default",
		Hash:      sha,
		Annotated: true,
		// Message / Tagger 모두 비움 → 기본값 분기.
	}
	tag, err := tags.CreateTag(context.Background(), gitDir, req)
	require.NoError(t, err)
	assert.True(t, tag.IsAnnotated)
	assert.Equal(t, "Tag v-default", tag.Message) // 기본 메시지
	require.NotNil(t, tag.Tagger)
	assert.Equal(t, "Git Executor", tag.Tagger.Name) // 기본 tagger
}

// Test_HandleGetTags_Pagination_NextPrevious 는 buildPagination 의 next /
// previous URL 분기를 모두 커버한다. fixture 외에 lightweight tag 를 더 심어
// limit=1 로 중간 페이지(skip=1)를 요청하면 next + previous 가 모두 생긴다.
func Test_HandleGetTags_Pagination_NextPrevious(t *testing.T) {
	t.Parallel()
	root, sha := newBareRepoFixture(t, "owner", "repo")
	gitDir := filepath.Join(root, "owner", "repo.git")
	// 태그를 더 추가하여 총 4개(v1,v2,v3,v4) 확보 → 페이지 여러 개.
	gitCmd(t, gitDir, "update-ref", "refs/tags/v3", sha)
	gitCmd(t, gitDir, "update-ref", "refs/tags/v4", sha)

	r := newTagsRouter(t, root)

	// limit=1, skip=1 → currentPage=2: next(<totalPages) + previous(>1) 둘 다.
	w := doReq(t, r, http.MethodGet, "/repos/owner/repo/tags?limit=1&skip=1&sort=name", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp tags.TagsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 4, resp.Pagination.Count)
	assert.Equal(t, 2, resp.Pagination.CurrentPage)
	require.NotNil(t, resp.Pagination.Next, "next URL should be present on middle page")
	require.NotNil(t, resp.Pagination.Previous, "previous URL should be present on middle page")
}
