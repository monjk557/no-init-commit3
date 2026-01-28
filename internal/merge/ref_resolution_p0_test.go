package merge

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/testutil/gitrepo"
)

// =============================================================================
// Ref Resolution P0 테스트 (최우선)
// 회귀: "404/409/400 혼선을 제거하여 클라이언트가 정확한 에러 처리 가능"
// ⚠️ Error Mapper 없이 핸들러 직접 분기 결과를 검증
// =============================================================================

// testMergePreviewP0Setup wraps test setup for P0 ref resolution tests
type testMergePreviewP0Setup struct {
	repo    *gitrepo.TestRepo
	handler *MergePreviewHandler
}

func setupMergePreviewP0Test(t *testing.T) *testMergePreviewP0Setup {
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

	return &testMergePreviewP0Setup{
		repo:    repo,
		handler: handler,
	}
}

// resolveRefsWithStatus calls resolveAndValidateRefs and returns status code
// ⚠️ 직접 분기 - Error Mapper 없음
func (s *testMergePreviewP0Setup) resolveRefsWithStatus(sourceRef, targetRef string) int {
	ctx := context.Background()
	status, _ := s.handler.resolveAndValidateRefs(ctx, s.repo.Repo, sourceRef, targetRef)
	return status
}

// =============================================================================
// P0: Malformed Ref → 400 INVALID_REF
// 회귀: "형식이 명확히 잘못된 ref는 400으로 거절된다"
// =============================================================================

func Test_MergePreview_RefWithWhitespace_Returns400_INVALID_REF(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
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
			status := setup.resolveRefsWithStatus(tc.ref, "main")
			assert.Equal(t, http.StatusBadRequest, status,
				"ref '%s' should return 400, got %d", tc.ref, status)
		})
	}
}

func Test_MergePreview_RefIsEmptyOrDot_Returns400_INVALID_REF(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	testCases := []struct {
		name string
		ref  string
	}{
		{"single_dot", "."},
		{"double_dot", ".."},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status := setup.resolveRefsWithStatus(tc.ref, "main")
			assert.Equal(t, http.StatusBadRequest, status,
				"ref '%s' should return 400, got %d", tc.ref, status)
		})
	}
}

// =============================================================================
// P0: Unsupported Ref → 400 UNSUPPORTED_REF
// 회귀: "지원하지 않는 ref 형식은 400으로 명확히 거절"
// =============================================================================

func Test_MergePreview_RevParseExpression_Returns400_UNSUPPORTED_REF(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
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
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status := setup.resolveRefsWithStatus(tc.ref, "main")
			assert.Equal(t, http.StatusBadRequest, status,
				"rev-parse '%s' should return 400, got %d", tc.ref, status)
		})
	}
}

func Test_MergePreview_TreeishPath_Returns400_UNSUPPORTED_REF(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
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
			status := setup.resolveRefsWithStatus(tc.ref, "main")
			assert.Equal(t, http.StatusBadRequest, status,
				"tree-ish:path '%s' should return 400, got %d", tc.ref, status)
		})
	}
}

func Test_MergePreview_LocalStateRef_Returns400_P0(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
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
			status := setup.resolveRefsWithStatus(tc.ref, "main")
			assert.Equal(t, http.StatusBadRequest, status,
				"local state ref '%s' should return 400, got %d", tc.ref, status)
		})
	}
}

func Test_MergePreview_FullRefPath_Returns400_UNSUPPORTED_REF(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
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
			status := setup.resolveRefsWithStatus(tc.ref, "main")
			assert.Equal(t, http.StatusBadRequest, status,
				"full ref path '%s' should return 400, got %d", tc.ref, status)
		})
	}
}

func Test_MergePreview_HEAD_Returns400_UNSUPPORTED_REF(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	status := setup.resolveRefsWithStatus("HEAD", "main")
	assert.Equal(t, http.StatusBadRequest, status,
		"HEAD should return 400, got %d", status)
}

// =============================================================================
// P0: Non-existent SHA → 404 OBJECT_NOT_FOUND
// 회귀: "존재하지 않는 SHA는 404로 떨어져 409/400과 섞이지 않는다"
// =============================================================================

func Test_MergePreview_NonExistentSHA_Returns404_P0(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	// 유효한 형식이지만 존재하지 않는 SHA
	nonExistentSHA := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	status := setup.resolveRefsWithStatus(nonExistentSHA, "main")

	// ⚠️ 핵심: 404 NOT 409 OR 400
	assert.Equal(t, http.StatusNotFound, status,
		"non-existent SHA should return 404, got %d", status)
}

func Test_MergePreview_NonExistentBranch_Returns404_REF_NOT_FOUND(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	status := setup.resolveRefsWithStatus("nonexistent-branch", "main")

	assert.Equal(t, http.StatusNotFound, status,
		"non-existent branch should return 404, got %d", status)
}

func Test_MergePreview_DeletedBranch_Returns404(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	// Create and delete branch
	setup.repo.BranchCreate("to-delete")
	setup.repo.Commit("delete.txt", "delete", "To delete commit")
	setup.repo.BranchCheckout("main")
	setup.repo.BranchDelete("to-delete")

	status := setup.resolveRefsWithStatus("to-delete", "main")

	assert.Equal(t, http.StatusNotFound, status,
		"deleted branch should return 404, got %d", status)
}

// =============================================================================
// P0: Branch-First Policy
// 회귀: "sha 모양 branch가 있으면 sha로 해석 안 하고 branch로 해석한다"
// =============================================================================

func Test_MergePreview_SHALikeBranchName_PrefersBranch_P0(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	// Create branch with SHA-like name
	shaLikeName := "deadbeef"
	setup.repo.BranchCreate(shaLikeName)
	setup.repo.Commit("sha-branch.txt", "sha branch", "SHA-like branch commit")
	setup.repo.BranchCheckout("main")

	status := setup.resolveRefsWithStatus(shaLikeName, "main")

	// Should succeed because it's a valid branch
	assert.Equal(t, http.StatusOK, status,
		"SHA-like branch name should be resolved as branch, got %d", status)
}

func Test_MergePreview_AmbiguousLikeRef_Returns404_P0(t *testing.T) {
	// 회귀: "xyz123, deadbeef 같은 애매한 입력은 malformed(400)가 아니라 ref로 해석 시도 후 404"
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	testCases := []struct {
		name string
		ref  string
	}{
		{"short_hex_like", "xyz123"},
		{"non_hex_40", "ghijklmnopqrstuvwxyzghijklmnopqrstuvwxyz"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status := setup.resolveRefsWithStatus(tc.ref, "main")

			// ⚠️ 핵심: 400 아님! branch로 해석 시도 후 404
			assert.Equal(t, http.StatusNotFound, status,
				"ambiguous-like ref '%s' should return 404 (not 400), got %d",
				tc.ref, status)
		})
	}
}

// =============================================================================
// P0: Tag Resolution
// 회귀: "annotated tag를 입력해도 commit으로 안정적으로 역참조된다"
// =============================================================================

func Test_MergePreview_AnnotatedTag_DereferencesToCommit_P0(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	baseCommit := setup.repo.Commit("file.txt", "content", "Initial")

	// Create feature branch
	setup.repo.BranchCreate("feature")
	setup.repo.Commit("feature.txt", "feature", "Feature commit")
	setup.repo.BranchCheckout("main")

	// Create annotated tag on base commit
	setup.repo.TagAnnotated("v1.0.0", "Release v1.0.0", baseCommit)

	status := setup.resolveRefsWithStatus("feature", "v1.0.0")

	// Annotated tag should dereference to commit
	assert.Equal(t, http.StatusOK, status,
		"annotated tag should work, got %d", status)
}

func Test_MergePreview_LightweightTag_PointsToCommit_P0(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	baseCommit := setup.repo.Commit("file.txt", "content", "Initial")

	// Create feature branch
	setup.repo.BranchCreate("feature")
	setup.repo.Commit("feature.txt", "feature", "Feature commit")
	setup.repo.BranchCheckout("main")

	// Create lightweight tag
	setup.repo.TagLightweight("v1.0.0-light", baseCommit)

	status := setup.resolveRefsWithStatus("feature", "v1.0.0-light")

	assert.Equal(t, http.StatusOK, status,
		"lightweight tag should work, got %d", status)
}

// =============================================================================
// Branch/Tag Name Conflict
// =============================================================================

func Test_MergePreview_BranchTagConflict_ReturnsSuccessWithBranchPriority_P0(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	mainCommit := setup.repo.Commit("file.txt", "content", "Initial")

	// Create branch "release"
	setup.repo.BranchCreate("release")
	setup.repo.Commit("release.txt", "release branch", "Release branch commit")
	setup.repo.BranchCheckout("main")

	// Create tag "release" pointing to main
	setup.repo.TagAnnotated("release", "Release tag", mainCommit)

	status := setup.resolveRefsWithStatus("release", "main")

	// With branch priority policy, should succeed
	assert.Equal(t, http.StatusOK, status,
		"branch priority policy should succeed, got %d", status)
}

// =============================================================================
// Valid Refs → Success
// =============================================================================

func Test_MergePreview_ValidBranchNames_ReturnsSuccess_P0(t *testing.T) {
	t.Parallel()

	// Each branch test needs its own repo to avoid directory conflicts
	// (e.g., feature/foo prevents feature/foo/bar)
	validBranches := []string{
		"feature/foo",
		"feature/sub/bar",
		"release-1.0.0",
		"hotfix_123",
		"UPPERCASE",
		"MixedCase",
		"123numeric",
	}

	for _, branch := range validBranches {
		t.Run(branch, func(t *testing.T) {
			t.Parallel()

			// Each subtest gets its own repo
			setup := setupMergePreviewP0Test(t)
			setup.repo.Commit("file.txt", "content", "Initial")

			// Create the branch
			setup.repo.BranchCreate(branch)
			setup.repo.Commit(branch+".txt", branch, branch+" commit")
			setup.repo.BranchCheckout("main")

			status := setup.resolveRefsWithStatus(branch, "main")

			assert.Equal(t, http.StatusOK, status,
				"valid branch '%s' should succeed, got %d", branch, status)
		})
	}
}

func Test_MergePreview_ShortSHA_ReturnsSuccess_P0(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	targetSHA := setup.repo.Commit("file.txt", "content", "Initial")

	setup.repo.BranchCreate("feature")
	sourceSHA := setup.repo.Commit("feature.txt", "feature", "Feature commit")
	setup.repo.BranchCheckout("main")

	// 7-character short SHA
	shortSource := sourceSHA.String()[:7]
	shortTarget := targetSHA.String()[:7]

	status := setup.resolveRefsWithStatus(shortSource, shortTarget)

	assert.Equal(t, http.StatusOK, status,
		"short SHA should succeed, got %d", status)
}

func Test_MergePreview_FullSHA_ReturnsSuccess_P0(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	targetSHA := setup.repo.Commit("file.txt", "content", "Initial")

	setup.repo.BranchCreate("feature")
	sourceSHA := setup.repo.Commit("feature.txt", "feature", "Feature commit")
	setup.repo.BranchCheckout("main")

	status := setup.resolveRefsWithStatus(sourceSHA.String(), targetSHA.String())

	assert.Equal(t, http.StatusOK, status,
		"full SHA should succeed, got %d", status)
}

// =============================================================================
// Test: No 500 guarantee
// 회귀: "어떤 경우에도 500 INTERNAL_ERROR가 발생하지 않음"
// =============================================================================

func Test_MergePreview_MaliciousInput_NoInternalError_P0(t *testing.T) {
	t.Parallel()

	setup := setupMergePreviewP0Test(t)
	setup.repo.Commit("file.txt", "content", "Initial")

	maliciousInputs := []string{
		"../../../etc/passwd",
		"..%2f..%2f..%2fetc%2fpasswd",
		"branch\x00injection",
		"'; DROP TABLE refs; --",
		"$(whoami)",
		"`whoami`",
		"${PATH}",
	}

	for i, input := range maliciousInputs {
		t.Run("input_"+string(rune('a'+i)), func(t *testing.T) {
			status := setup.resolveRefsWithStatus(input, "main")

			// Should NOT be 500
			assert.NotEqual(t, http.StatusInternalServerError, status,
				"malicious input should not return 500")

			// Should be 400 or 404
			assert.True(t, status == http.StatusBadRequest || status == http.StatusNotFound,
				"malicious input should return 400 or 404, got %d", status)
		})
	}
}
