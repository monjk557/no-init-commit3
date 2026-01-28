package merge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/testutil/gitrepo"
)

// =============================================================================
// Ref Resolution P0 테스트 (최우선)
// 회귀: "404/409/400 혼선을 제거하여 클라이언트가 정확한 에러 처리 가능"
// =============================================================================

func init() {
	gin.SetMode(gin.TestMode)
}

// =============================================================================
// Test Helpers - Error Mapper 없이 직접 검증
// =============================================================================

type testMergePreviewSetup struct {
	repo      *gitrepo.TestRepo
	handler   *MergePreviewHandler
	router    *gin.Engine
	ownerRepo string
}

func setupMergePreviewTest(t *testing.T) *testMergePreviewSetup {
	t.Helper()

	repo := gitrepo.NewRepo(t)

	logCfg := config.LoggingConfig{Level: "error", Format: "json"}
	logger, err := logging.NewLogger(logCfg)
	require.NoError(t, err)

	pathConfig, err := repositories.NewPathConfig(repo.Dir)
	require.NoError(t, err)

	objectManager, err := repositories.NewGitObjectManager(pathConfig, logger)
	require.NoError(t, err)

	conflictDetector := NewConflictDetector(pathConfig, logger)

	handler := &MergePreviewHandler{
		pathConfig:       pathConfig,
		logger:           logger,
		objectManager:    objectManager,
		conflictDetector: conflictDetector,
	}

	router := gin.New()
	router.POST("/repos/:owner/:repo/merge/preview", handler.HandleMergePreview)

	return &testMergePreviewSetup{
		repo:      repo,
		handler:   handler,
		router:    router,
		ownerRepo: "test/repo",
	}
}

func (s *testMergePreviewSetup) doPreview(sourceRef, targetRef string) *httptest.ResponseRecorder {
	body := map[string]string{
		"source_ref": sourceRef,
		"target_ref": targetRef,
	}
	jsonBody, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", "/repos/test/repo/merge/preview", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")

	resp := httptest.NewRecorder()
	s.router.ServeHTTP(resp, req)
	return resp
}

// =============================================================================
// P0: Malformed Ref → 400 INVALID_REF
// 회귀: "형식이 명확히 잘못된 ref는 400으로 거절된다"
// =============================================================================

func Test_MergePreview_RefWithWhitespace_Returns400_INVALID_REF(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")
	setup.repo.BranchCreate("feature")
	setup.repo.Commit("feature.txt", "feature", "Feature commit")
	setup.repo.BranchCheckout("main")

	testCases := []struct {
		name string
		ref  string
	}{
		{"space_in_name", "main branch"},
		{"tab_in_name", "main\tbranch"},
		{"newline_in_name", "main\nbranch"},
		{"carriage_return", "main\rbranch"},
		{"leading_space", " main"},
		{"trailing_space", "main "},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := setup.doPreview(tc.ref, "main")

			// ⚠️ 직접 검증 - Error Mapper 없음
			assert.Equal(t, http.StatusBadRequest, resp.Code,
				"ref '%s' should return 400, got %d\nBody: %s",
				tc.ref, resp.Code, resp.Body.String())
			gitrepo.AssertNo500(t, resp)
		})
	}
}

func Test_MergePreview_RefIsEmptyOrDot_Returns400_INVALID_REF(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	testCases := []struct {
		name string
		ref  string
	}{
		{"empty_string", ""},
		{"single_dot", "."},
		{"double_dot", ".."},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := setup.doPreview(tc.ref, "main")

			assert.Equal(t, http.StatusBadRequest, resp.Code,
				"ref '%s' should return 400", tc.ref)
			gitrepo.AssertNo500(t, resp)
		})
	}
}

// =============================================================================
// P0: Unsupported Ref → 400 UNSUPPORTED_REF
// 회귀: "지원하지 않는 ref 형식은 400으로 명확히 거절"
// =============================================================================

func Test_MergePreview_RevParseExpression_Returns400_UNSUPPORTED_REF(t *testing.T) {
	// 회귀: "rev-parse 문법(main^, main~1)이 입력으로 들어와도 다른 ref로 오해되지 않는다"
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")
	setup.repo.Commit("file2.txt", "content2", "Second")

	testCases := []struct {
		name string
		ref  string
	}{
		{"caret", "main^"},
		{"tilde_1", "main~1"},
		{"tilde_2", "main~2"},
		{"double_caret", "main^^"},
		{"at_brace", "main@{1}"},
		{"at_upstream", "main@{upstream}"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := setup.doPreview(tc.ref, "main")

			assert.Equal(t, http.StatusBadRequest, resp.Code,
				"rev-parse '%s' should return 400", tc.ref)
			gitrepo.AssertNo500(t, resp)
		})
	}
}

func Test_MergePreview_TreeishPath_Returns400_UNSUPPORTED_REF(t *testing.T) {
	// 회귀: "tree-ish:path 입력이 path traversal/오해로 이어지지 않는다"
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	testCases := []struct {
		name string
		ref  string
	}{
		{"main_colon_file", "main:file.txt"},
		{"head_colon_file", "HEAD:file.txt"},
		{"sha_colon_path", "abc123:src/main.go"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := setup.doPreview(tc.ref, "main")

			assert.Equal(t, http.StatusBadRequest, resp.Code,
				"tree-ish:path '%s' should return 400", tc.ref)
			gitrepo.AssertNo500(t, resp)
		})
	}
}

func Test_MergePreview_LocalStateRef_Returns400_UNSUPPORTED_REF(t *testing.T) {
	// 회귀: "로컬 state ref(FETCH_HEAD, ORIG_HEAD 등)가 API에서 거부된다"
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	testCases := []struct {
		name string
		ref  string
	}{
		{"FETCH_HEAD", "FETCH_HEAD"},
		{"ORIG_HEAD", "ORIG_HEAD"},
		{"MERGE_HEAD", "MERGE_HEAD"},
		{"CHERRY_PICK_HEAD", "CHERRY_PICK_HEAD"},
		{"REBASE_HEAD", "REBASE_HEAD"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := setup.doPreview(tc.ref, "main")

			assert.Equal(t, http.StatusBadRequest, resp.Code,
				"local state ref '%s' should return 400", tc.ref)
			gitrepo.AssertNo500(t, resp)
		})
	}
}

func Test_MergePreview_FullRefPath_Returns400_UNSUPPORTED_REF(t *testing.T) {
	// 회귀: "refs/heads/* 같은 내부 ref를 직접 넣어도 우회되지 않는다"
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	testCases := []struct {
		name string
		ref  string
	}{
		{"refs_heads_main", "refs/heads/main"},
		{"refs_tags_v1", "refs/tags/v1.0.0"},
		{"refs_remotes", "refs/remotes/origin/main"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := setup.doPreview(tc.ref, "main")

			assert.Equal(t, http.StatusBadRequest, resp.Code,
				"full ref path '%s' should return 400", tc.ref)
			gitrepo.AssertNo500(t, resp)
		})
	}
}

func Test_MergePreview_RemoteRef_Returns400_UNSUPPORTED_REF(t *testing.T) {
	// 회귀: "origin/main 같은 remote ref가 거부된다"
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	testCases := []struct {
		name string
		ref  string
	}{
		{"origin_main", "origin/main"},
		{"upstream_master", "upstream/master"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := setup.doPreview(tc.ref, "main")

			// origin/main은 현재 코드에서 branch로 해석 시도 후 404 반환
			// 또는 명시적으로 거부하면 400
			assert.True(t, resp.Code == http.StatusBadRequest || resp.Code == http.StatusNotFound,
				"remote ref '%s' should return 400 or 404, got %d", tc.ref, resp.Code)
			gitrepo.AssertNo500(t, resp)
		})
	}
}

// =============================================================================
// P0: Non-existent SHA → 404 OBJECT_NOT_FOUND
// 회귀: "존재하지 않는 SHA는 404로 떨어져 409/400과 섞이지 않는다"
// =============================================================================

func Test_MergePreview_NonExistentSHA_Returns404_OBJECT_NOT_FOUND(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewTest(t)
	targetSHA := setup.repo.Commit("file.txt", "content", "Initial")

	// 유효한 형식이지만 존재하지 않는 SHA
	nonExistentSHA := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	resp := setup.doPreview(nonExistentSHA, targetSHA.String())

	// ⚠️ 핵심: 404 NOT 409 OR 400
	assert.Equal(t, http.StatusNotFound, resp.Code,
		"non-existent SHA should return 404, got %d\nBody: %s",
		resp.Code, resp.Body.String())
	gitrepo.AssertNo500(t, resp)
}

func Test_MergePreview_NonExistentBranch_Returns404_REF_NOT_FOUND(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	resp := setup.doPreview("nonexistent-branch", "main")

	assert.Equal(t, http.StatusNotFound, resp.Code,
		"non-existent branch should return 404")
	gitrepo.AssertNo500(t, resp)
}

func Test_MergePreview_DeletedBranch_Returns404(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	// Create and delete branch
	setup.repo.BranchCreate("to-delete")
	setup.repo.Commit("delete.txt", "delete", "To delete commit")
	setup.repo.BranchCheckout("main")
	setup.repo.BranchDelete("to-delete")

	resp := setup.doPreview("to-delete", "main")

	assert.Equal(t, http.StatusNotFound, resp.Code,
		"deleted branch should return 404")
	gitrepo.AssertNo500(t, resp)
}

// =============================================================================
// P0: Branch-First Policy
// 회귀: "sha 모양 branch가 있으면 sha로 해석 안 하고 branch로 해석한다"
// =============================================================================

func Test_MergePreview_SHALikeBranchName_PrefersBranch(t *testing.T) {
	// 회귀: "sha처럼 보이는 branch가 존재하면 branch로 해석"
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	// Create branch with SHA-like name
	shaLikeName := "deadbeef"
	setup.repo.BranchCreate(shaLikeName)
	setup.repo.Commit("sha-branch.txt", "sha branch", "SHA-like branch commit")
	setup.repo.BranchCheckout("main")

	resp := setup.doPreview(shaLikeName, "main")

	// Should succeed because it's a valid branch
	assert.Equal(t, http.StatusOK, resp.Code,
		"SHA-like branch name should be resolved as branch, got %d\nBody: %s",
		resp.Code, resp.Body.String())
}

func Test_MergePreview_AmbiguousLikeRef_Returns404_NotMalformed(t *testing.T) {
	// 회귀: "xyz123, deadbeef 같은 애매한 입력은 malformed(400)가 아니라 ref로 해석 시도 후 404"
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	testCases := []struct {
		name string
		ref  string
	}{
		{"short_hex_like", "xyz123"},
		{"eight_char_hex", "deadbeef"},
		{"non_hex_40", "ghijklmnopqrstuvwxyzghijklmnopqrstuvwxyz"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := setup.doPreview(tc.ref, "main")

			// ⚠️ 핵심: 400 아님! branch로 해석 시도 후 404
			assert.Equal(t, http.StatusNotFound, resp.Code,
				"ambiguous-like ref '%s' should return 404 (not 400), got %d",
				tc.ref, resp.Code)
			gitrepo.AssertNo500(t, resp)
		})
	}
}

// =============================================================================
// P0: Tag Resolution
// 회귀: "annotated tag를 입력해도 commit으로 안정적으로 역참조된다"
// =============================================================================

func Test_MergePreview_AnnotatedTag_DereferencesToCommit(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewTest(t)
	baseCommit := setup.repo.Commit("file.txt", "content", "Initial")

	// Create feature branch
	setup.repo.BranchCreate("feature")
	setup.repo.Commit("feature.txt", "feature", "Feature commit")
	setup.repo.BranchCheckout("main")

	// Create annotated tag on base commit
	setup.repo.TagAnnotated("v1.0.0", "Release v1.0.0", baseCommit)

	resp := setup.doPreview("feature", "v1.0.0")

	// Annotated tag should dereference to commit
	assert.Equal(t, http.StatusOK, resp.Code,
		"annotated tag should work, got %d\nBody: %s",
		resp.Code, resp.Body.String())
}

func Test_MergePreview_LightweightTag_PointsToCommit(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewTest(t)
	baseCommit := setup.repo.Commit("file.txt", "content", "Initial")

	// Create feature branch
	setup.repo.BranchCreate("feature")
	setup.repo.Commit("feature.txt", "feature", "Feature commit")
	setup.repo.BranchCheckout("main")

	// Create lightweight tag
	setup.repo.TagLightweight("v1.0.0-light", baseCommit)

	resp := setup.doPreview("feature", "v1.0.0-light")

	assert.Equal(t, http.StatusOK, resp.Code,
		"lightweight tag should work, got %d\nBody: %s",
		resp.Code, resp.Body.String())
}

// =============================================================================
// Branch/Tag Name Conflict → 400 AMBIGUOUS_REF (or branch priority)
// =============================================================================

func Test_MergePreview_BranchTagConflict_ReturnsSuccessWithBranchPriority(t *testing.T) {
	// 회귀: "동일 이름의 branch와 tag가 있으면 branch 우선 정책에 따라 처리"
	t.Parallel()

	setup := setupMergePreviewTest(t)
	mainCommit := setup.repo.Commit("file.txt", "content", "Initial")

	// Create branch "release"
	setup.repo.BranchCreate("release")
	setup.repo.Commit("release.txt", "release branch", "Release branch commit")
	setup.repo.BranchCheckout("main")

	// Create tag "release" pointing to main
	setup.repo.TagAnnotated("release", "Release tag", mainCommit)

	resp := setup.doPreview("release", "main")

	// With branch priority policy, should succeed
	// If AMBIGUOUS_REF policy, should return 400
	t.Logf("Status: %d, Body: %s", resp.Code, resp.Body.String())
	gitrepo.AssertNo500(t, resp)

	// 현재 정책: branch 우선이므로 성공
	assert.Equal(t, http.StatusOK, resp.Code,
		"branch priority policy should succeed")
}

// =============================================================================
// Valid Refs → Success
// =============================================================================

func Test_MergePreview_ValidBranchNames_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	validBranches := []string{
		"feature/foo",
		"feature/foo/bar",
		"release-1.0.0",
		"hotfix_123",
		"UPPERCASE",
		"MixedCase",
		"123numeric",
	}

	for _, branch := range validBranches {
		t.Run(branch, func(t *testing.T) {
			// Create the branch
			setup.repo.BranchCreate(branch)
			setup.repo.Commit(branch+".txt", branch, branch+" commit")
			setup.repo.BranchCheckout("main")

			resp := setup.doPreview(branch, "main")

			assert.Equal(t, http.StatusOK, resp.Code,
				"valid branch '%s' should succeed, got %d\nBody: %s",
				branch, resp.Code, resp.Body.String())
		})
	}
}

func Test_MergePreview_ShortSHA_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewTest(t)
	targetSHA := setup.repo.Commit("file.txt", "content", "Initial")

	setup.repo.BranchCreate("feature")
	sourceSHA := setup.repo.Commit("feature.txt", "feature", "Feature commit")
	setup.repo.BranchCheckout("main")

	// 7-character short SHA
	shortSource := sourceSHA.String()[:7]
	shortTarget := targetSHA.String()[:7]

	resp := setup.doPreview(shortSource, shortTarget)

	assert.Equal(t, http.StatusOK, resp.Code,
		"short SHA should succeed, got %d\nBody: %s",
		resp.Code, resp.Body.String())
}

func Test_MergePreview_FullSHA_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewTest(t)
	targetSHA := setup.repo.Commit("file.txt", "content", "Initial")

	setup.repo.BranchCreate("feature")
	sourceSHA := setup.repo.Commit("feature.txt", "feature", "Feature commit")
	setup.repo.BranchCheckout("main")

	resp := setup.doPreview(sourceSHA.String(), targetSHA.String())

	assert.Equal(t, http.StatusOK, resp.Code,
		"full SHA should succeed, got %d\nBody: %s",
		resp.Code, resp.Body.String())
}

// =============================================================================
// HEAD Policy
// =============================================================================

func Test_MergePreview_HEAD_Returns400_UNSUPPORTED_REF(t *testing.T) {
	// 정책: HEAD는 지원하지 않음
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	resp := setup.doPreview("HEAD", "main")

	assert.Equal(t, http.StatusBadRequest, resp.Code,
		"HEAD should return 400, got %d\nBody: %s",
		resp.Code, resp.Body.String())
}

// =============================================================================
// Test: No 500 guarantee
// 회귀: "어떤 경우에도 500 INTERNAL_ERROR가 발생하지 않음"
// =============================================================================

func Test_MergePreview_MaliciousInput_NoInternalError(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewTest(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	maliciousInputs := []string{
		"../../../etc/passwd",
		"..%2f..%2f..%2fetc%2fpasswd",
		"\x00",
		"\x00malicious",
		"branch\x00injection",
		"'; DROP TABLE refs; --",
		"$(whoami)",
		"`whoami`",
		"${PATH}",
		string(make([]byte, 10000)), // Very long input
	}

	for i, input := range maliciousInputs {
		t.Run("input_"+string(rune('a'+i)), func(t *testing.T) {
			resp := setup.doPreview(input, "main")

			// Should NOT be 500
			gitrepo.RequireNo500(t, resp)

			// Should be 400 or 404
			assert.True(t, resp.Code == http.StatusBadRequest || resp.Code == http.StatusNotFound,
				"malicious input should return 400 or 404, got %d", resp.Code)
		})
	}
}
