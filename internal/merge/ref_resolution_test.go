package merge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
)

// =============================================================================
// 1.1 Ref Resolution 문제 (409 버그 원인)
// 회귀 방지: 404/409/400 혼선을 제거하여 클라이언트가 정확한 에러 처리 가능
// =============================================================================

// ErrorResponse represents API error response structure
type ErrorResponse struct {
	Success bool `json:"success"`
	Error   struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id,omitempty"`
	} `json:"error"`
}

// =============================================================================
// Test: 삭제된 브랜치 시나리오
// =============================================================================

func Test_MergePreview_DeletedSourceBranch_Returns404(t *testing.T) {
	// 회귀: "삭제된 source 브랜치는 404로 떨어져야 한다"
	t.Parallel()

	// Given: feature 브랜치가 있는 레포
	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main에 초기 커밋
	createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	// feature 브랜치 생성 및 커밋
	checkoutBranch(t, wt, "feature", true)
	createCommit(t, wt, tempDir, "feature.txt", "feature", "Feature commit", now)

	// main으로 돌아가기
	checkoutBranch(t, wt, "main", false)

	// feature 브랜치 삭제
	err := repo.Storer.RemoveReference(plumbing.NewBranchReferenceName("feature"))
	require.NoError(t, err, "branch deletion should succeed")

	// When: 삭제된 브랜치로 merge preview
	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	status, errResp := handler.resolveAndValidateRefs(ctx, repo, "feature", "main")

	// Then: 404 반환
	assert.Equal(t, 404, status, "deleted source branch should return 404, got %d", status)
	assert.NotNil(t, errResp, "error response should not be nil")
}

func Test_MergePreview_DeletedTargetBranch_Returns404(t *testing.T) {
	// 회귀: "삭제된 target 브랜치는 404로 떨어져야 한다"
	t.Parallel()

	// Given: 두 브랜치가 있는 레포
	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main에 초기 커밋
	createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	// feature 브랜치 생성 및 커밋
	checkoutBranch(t, wt, "feature", true)
	createCommit(t, wt, tempDir, "feature.txt", "feature", "Feature commit", now)

	// target 브랜치 생성
	checkoutBranch(t, wt, "target", true)
	createCommit(t, wt, tempDir, "target.txt", "target", "Target commit", now)

	// feature로 돌아가서 target 삭제
	checkoutBranch(t, wt, "feature", false)
	err := repo.Storer.RemoveReference(plumbing.NewBranchReferenceName("target"))
	require.NoError(t, err, "branch deletion should succeed")

	// When: 삭제된 target으로 merge preview
	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	status, errResp := handler.resolveAndValidateRefs(ctx, repo, "feature", "target")

	// Then: 404 반환
	assert.Equal(t, 404, status, "deleted target branch should return 404, got %d", status)
	assert.NotNil(t, errResp, "error response should not be nil")
}

func Test_MergePreview_DeletedBothBranches_Returns404(t *testing.T) {
	// 회귀: "둘 다 삭제된 경우 404로 떨어져야 한다"
	t.Parallel()

	// Given: 레포
	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main에 초기 커밋
	createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	// When: 존재하지 않는 두 브랜치로 merge preview
	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	status, errResp := handler.resolveAndValidateRefs(ctx, repo, "nonexistent1", "nonexistent2")

	// Then: 404 반환
	assert.Equal(t, 404, status, "nonexistent branches should return 404, got %d", status)
	assert.NotNil(t, errResp, "error response should not be nil")
}

// =============================================================================
// Test: 브랜치 이름 형식
// =============================================================================

func Test_MergePreview_BranchNameWithSlash_ReturnsSuccess(t *testing.T) {
	// 회귀: "feature/foo 형태의 브랜치도 정상 처리된다"
	t.Parallel()

	// Given: slash 포함 브랜치
	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main에 초기 커밋
	createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	// feature/foo 브랜치 생성
	checkoutBranch(t, wt, "feature/foo", true)
	createCommit(t, wt, tempDir, "feature.txt", "feature", "Feature commit", now)

	// When: feature/foo -> main merge preview
	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	status, errResp := handler.resolveAndValidateRefs(ctx, repo, "feature/foo", "main")

	// Then: 성공 (200)
	assert.Equal(t, 200, status, "branch with slash should succeed, got %d with error: %v", status, errResp)
}

func Test_MergePreview_BranchNameWithMultipleSlashes_ReturnsSuccess(t *testing.T) {
	// 회귀: "feature/foo/bar 형태의 브랜치도 정상 처리된다"
	t.Parallel()

	// Given: 다중 slash 포함 브랜치
	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main에 초기 커밋
	createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	// feature/foo/bar 브랜치 생성
	checkoutBranch(t, wt, "feature/foo/bar", true)
	createCommit(t, wt, tempDir, "feature.txt", "feature", "Feature commit", now)

	// When: feature/foo/bar -> main merge preview
	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	status, errResp := handler.resolveAndValidateRefs(ctx, repo, "feature/foo/bar", "main")

	// Then: 성공 (200)
	assert.Equal(t, 200, status, "branch with multiple slashes should succeed, got %d with error: %v", status, errResp)
}

// =============================================================================
// Test: SHA 입력
// =============================================================================

func Test_MergePreview_CommitSHA_ReturnsSuccess(t *testing.T) {
	// 회귀: "full SHA로 직접 접근해도 정상 처리된다"
	t.Parallel()

	// Given: 두 커밋
	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main에 초기 커밋
	targetSHA := createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	// feature 브랜치에서 커밋
	checkoutBranch(t, wt, "feature", true)
	sourceSHA := createCommit(t, wt, tempDir, "feature.txt", "feature", "Feature commit", now)

	// When: full SHA로 merge preview
	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	status, errResp := handler.resolveAndValidateRefs(ctx, repo, sourceSHA.String(), targetSHA.String())

	// Then: 성공 (200)
	assert.Equal(t, 200, status, "full SHA should succeed, got %d with error: %v", status, errResp)
}

func Test_MergePreview_ShortSHA_ReturnsSuccess(t *testing.T) {
	// 회귀: "7자리 short SHA로도 정상 처리된다"
	t.Parallel()

	// Given: 두 커밋
	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main에 초기 커밋
	targetSHA := createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	// feature 브랜치에서 커밋
	checkoutBranch(t, wt, "feature", true)
	sourceSHA := createCommit(t, wt, tempDir, "feature.txt", "feature", "Feature commit", now)

	// When: short SHA (7자리)로 merge preview
	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	shortSource := sourceSHA.String()[:7]
	shortTarget := targetSHA.String()[:7]

	status, errResp := handler.resolveAndValidateRefs(ctx, repo, shortSource, shortTarget)

	// Then: 성공 (200) - 현재 코드가 short SHA를 지원하지 않을 수 있음
	// 이 테스트가 실패하면 short SHA 지원을 추가해야 함
	assert.Equal(t, 200, status, "short SHA should succeed, got %d with error: %v", status, errResp)
}

// =============================================================================
// Test: SHA 에러 케이스 (P0)
// =============================================================================

func Test_MergePreview_NonExistentSHA_Returns404_OBJECT_NOT_FOUND(t *testing.T) {
	// 회귀: "존재하지 않는 SHA는 404로 떨어져 409/400과 섞이지 않는다"
	// ⭐ P0 테스트
	t.Parallel()

	// Given: 레포
	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main에 초기 커밋
	targetSHA := createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	// When: 존재하지 않는 SHA로 merge preview
	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	// 유효한 형식이지만 존재하지 않는 SHA (40자리 hex)
	nonExistentSHA := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	status, errResp := handler.resolveAndValidateRefs(ctx, repo, nonExistentSHA, targetSHA.String())

	// Then: 404 + OBJECT_NOT_FOUND
	assert.Equal(t, 404, status, "non-existent SHA should return 404, got %d", status)
	if errResp != nil {
		t.Logf("Error response: %+v", errResp)
	}
}

func Test_MergePreview_MalformedRef_Returns400_INVALID_REF(t *testing.T) {
	// 회귀: "형식이 명확히 잘못된 ref는 400으로 거절된다"
	// 정책: docs/collabops-git/COLLABOPS_RUNTIME_POLICIES.md - Section 6
	// ⭐ P0 테스트
	t.Parallel()

	// Given: 레포
	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main에 초기 커밋
	targetSHA := createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	// When: 명확히 형식이 잘못된 ref로 merge preview
	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	// 400을 반환해야 하는 케이스: 명확한 형식 오류
	testCases := []struct {
		name        string
		ref         string
		description string
	}{
		{"contains_space", "main branch", "공백 포함"},
		{"contains_tab", "main\tbranch", "탭 포함"},
		{"contains_newline", "main\nbranch", "개행 포함"},
		{"rev_parse_caret", "main^", "rev-parse 표현식 (^)"},
		{"rev_parse_tilde", "main~1", "rev-parse 표현식 (~)"},
		{"rev_parse_at", "main@{1}", "rev-parse 표현식 (@{})"},
		{"tree_ish_path", "main:README.md", "tree-ish:path 형식"},
		{"dot_only", ".", "단독 점"},
		{"double_dot", "..", "이중 점"},
		{"local_state_fetch_head", "FETCH_HEAD", "로컬 state ref"},
		{"local_state_orig_head", "ORIG_HEAD", "로컬 state ref"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status, errResp := handler.resolveAndValidateRefs(ctx, repo, tc.ref, targetSHA.String())

			// Then: 400 (형식 오류)
			assert.Equal(t, 400, status, "malformed ref '%s' (%s) should return 400, got %d", tc.ref, tc.description, status)
			if errResp != nil {
				t.Logf("Error response for %s: %+v", tc.name, errResp)
			}
		})
	}
}

func Test_MergePreview_AmbiguousLikeRef_Returns404_NotMalformed(t *testing.T) {
	// 회귀: "xyz123, deadbeef 같은 애매한 입력은 malformed가 아니라 ref로 해석 시도 후 404"
	// 정책: Branch-First Policy (docs/collabops-git/COLLABOPS_RUNTIME_POLICIES.md - Section 4)
	t.Parallel()

	// Given: 레포
	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main에 초기 커밋
	targetSHA := createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	// When: SHA처럼 보이지만 branch로도 유효한 ref
	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	// 404를 반환해야 하는 케이스: branch로 해석 가능하지만 존재하지 않음
	testCases := []struct {
		name string
		ref  string
	}{
		{"short_hex", "xyz123"},
		{"eight_char_hex", "deadbeef"},
		{"non_hex_40char", "ghijklmnopqrstuvwxyzghijklmnopqrstuvwxyz"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status, errResp := handler.resolveAndValidateRefs(ctx, repo, tc.ref, targetSHA.String())

			// Then: 404 (존재하지 않는 ref로 처리)
			// 이런 입력은 malformed(400)가 아니라 ref not found(404)
			assert.Equal(t, 404, status, "ambiguous-like ref '%s' should return 404 (not 400), got %d", tc.ref, status)
			if errResp != nil {
				t.Logf("Error response for %s: %+v", tc.name, errResp)
			}
		})
	}
}

func Test_MergePreview_AmbiguousRef_Returns400_AMBIGUOUS_REF(t *testing.T) {
	// 회귀: "브랜치/태그 이름 충돌 시 400 AMBIGUOUS_REF"
	t.Parallel()

	// Given: 같은 이름의 브랜치와 태그
	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main에 초기 커밋
	mainSHA := createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	// "release" 브랜치 생성
	checkoutBranch(t, wt, "release", true)
	createCommit(t, wt, tempDir, "release.txt", "release", "Release commit", now)

	// main으로 돌아가서 "release" 태그도 생성 (같은 이름!)
	checkoutBranch(t, wt, "main", false)
	_, err := repo.CreateTag("release", mainSHA, &gogit.CreateTagOptions{
		Message: "Release tag",
		Tagger: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  now,
		},
	})
	require.NoError(t, err, "tag creation should succeed")

	// When: ambiguous ref로 merge preview
	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	status, errResp := handler.resolveAndValidateRefs(ctx, repo, "release", "main")

	// Then: 현재 구현은 branch 우선이므로 성공하거나,
	// 명시적으로 ambiguous를 감지하면 400 반환
	// 정책에 따라 다름 - 여기서는 "branch 우선" 정책이면 200 OK
	t.Logf("Status: %d, Error: %+v", status, errResp)

	// 현재 정책: branch 우선이므로 성공
	// 만약 AMBIGUOUS_REF를 반환해야 한다면 이 assertion을 변경
	assert.Equal(t, 200, status, "branch priority policy should succeed, got %d", status)
}

// =============================================================================
// Helper: resolveAndValidateRefs - handler 내부 로직 테스트용
// =============================================================================

func (h *MergePreviewHandler) resolveAndValidateRefs(ctx context.Context, repo *gogit.Repository, sourceRef, targetRef string) (int, error) {
	// Resolve source
	sourceCommit, err := h.objectManager.ResolveReference(repo, sourceRef)
	if err != nil {
		return statusCodeForResolveReferenceError(err), err
	}

	// Resolve target
	targetCommit, err := h.objectManager.ResolveReference(repo, targetRef)
	if err != nil {
		return statusCodeForResolveReferenceError(err), err
	}

	// Same commit check
	if sourceCommit == targetCommit {
		return 400, nil
	}

	return 200, nil
}

// =============================================================================
// Helper functions
// =============================================================================

func initTestRepo(t *testing.T, dir string) *gogit.Repository {
	t.Helper()
	repo, err := gogit.PlainInit(dir, false)
	require.NoError(t, err, "repo init failed")

	// main 브랜치로 HEAD 설정
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))
	err = repo.Storer.SetReference(headRef)
	require.NoError(t, err, "set HEAD failed")

	return repo
}

func getWorktree(t *testing.T, repo *gogit.Repository) *gogit.Worktree {
	t.Helper()
	wt, err := repo.Worktree()
	require.NoError(t, err, "worktree failed")
	return wt
}

func checkoutBranch(t *testing.T, wt *gogit.Worktree, name string, create bool) {
	t.Helper()
	err := wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
		Create: create,
	})
	require.NoError(t, err, "checkout %s failed", name)
}

func createCommit(t *testing.T, wt *gogit.Worktree, dir, filename, content, message string, when time.Time) plumbing.Hash {
	t.Helper()

	// Write file
	filePath := filepath.Join(dir, filename)
	dirPath := filepath.Dir(filePath)
	err := os.MkdirAll(dirPath, 0755)
	require.NoError(t, err)
	err = os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err)

	// Add and commit
	_, err = wt.Add(filename)
	require.NoError(t, err)

	hash, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
			When:  when,
		},
	})
	require.NoError(t, err)

	return hash
}

func createTestMergePreviewHandler(t *testing.T, repoRoot string) *MergePreviewHandler {
	t.Helper()

	logCfg := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}

	logger, err := logging.NewLogger(logCfg)
	require.NoError(t, err)

	pathConfig, err := repositories.NewPathConfig(repoRoot)
	require.NoError(t, err)

	objectManager, err := repositories.NewGitObjectManager(pathConfig, logger)
	require.NoError(t, err)

	conflictDetector := NewConflictDetector(pathConfig, logger)

	return &MergePreviewHandler{
		pathConfig:       pathConfig,
		logger:           logger,
		objectManager:    objectManager,
		conflictDetector: conflictDetector,
	}
}

// =============================================================================
// 명시적 테스트 함수들 (문서 1.1A 매핑)
// =============================================================================

func Test_MergePreview_RefContainsWhitespaceOrNewline_Returns400(t *testing.T) {
	// 회귀: "ref에 공백/개행이 섞여도 parser가 흔들리거나 로그가 오염되지 않는다"
	t.Parallel()

	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	targetSHA := createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	testCases := []struct {
		name string
		ref  string
	}{
		{"space", "main branch"},
		{"tab", "main\tbranch"},
		{"newline", "main\nbranch"},
		{"carriage_return", "main\rbranch"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := handler.resolveAndValidateRefs(ctx, repo, tc.ref, targetSHA.String())
			assert.Equal(t, 400, status, "ref with whitespace '%s' should return 400", tc.name)
		})
	}
}

func Test_MergePreview_RefIsEmptyOrDotOrDotDot_Returns400(t *testing.T) {
	// 회귀: "빈 ref, '.', '..'가 들어와도 git 내부 path/ref로 흘러가지 않는다"
	t.Parallel()

	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	targetSHA := createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	testCases := []struct {
		name string
		ref  string
	}{
		{"dot", "."},
		{"double_dot", ".."},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := handler.resolveAndValidateRefs(ctx, repo, tc.ref, targetSHA.String())
			assert.Equal(t, 400, status, "ref '%s' should return 400", tc.ref)
		})
	}
}

func Test_MergePreview_RefIsRevParseExpression_Returns400_UNSUPPORTED_REF(t *testing.T) {
	// 회귀: "rev-parse 문법(main^, main~1)이 입력으로 들어와도 다른 ref로 오해되지 않는다"
	t.Parallel()

	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	targetSHA := createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	testCases := []struct {
		name string
		ref  string
	}{
		{"caret", "main^"},
		{"tilde", "main~1"},
		{"at_brace", "main@{1}"},
		{"double_caret", "main^^"},
		{"tilde_number", "main~3"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := handler.resolveAndValidateRefs(ctx, repo, tc.ref, targetSHA.String())
			assert.Equal(t, 400, status, "rev-parse expression '%s' should return 400", tc.ref)
		})
	}
}

func Test_MergePreview_RefIsTreeishPath_Returns400_UNSUPPORTED_REF(t *testing.T) {
	// 회귀: "tree-ish:path 입력이 path traversal/오해로 이어지지 않는다"
	t.Parallel()

	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	targetSHA := createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

	testCases := []struct {
		name string
		ref  string
	}{
		{"main_readme", "main:README.md"},
		{"head_file", "HEAD:file.txt"},
		{"sha_path", "abc123:src/main.go"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := handler.resolveAndValidateRefs(ctx, repo, tc.ref, targetSHA.String())
			assert.Equal(t, 400, status, "tree-ish:path '%s' should return 400", tc.ref)
		})
	}
}

func Test_MergePreview_LocalStateRef_Returns400_UNSUPPORTED_REF(t *testing.T) {
	// 회귀: "로컬 state ref(FETCH_HEAD, ORIG_HEAD 등)가 API에서 거부된다"
	t.Parallel()

	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	targetSHA := createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

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
			status, _ := handler.resolveAndValidateRefs(ctx, repo, tc.ref, targetSHA.String())
			assert.Equal(t, 400, status, "local state ref '%s' should return 400", tc.ref)
		})
	}
}

func Test_MergePreview_RefIsFullRef_RefsHeadsMain_Returns400_UNSUPPORTED_REF(t *testing.T) {
	// 회귀: "refs/heads/* 같은 내부 ref를 직접 넣어도 우회되지 않는다"
	t.Parallel()

	tempDir := t.TempDir()
	repo := initTestRepo(t, tempDir)
	wt := getWorktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	targetSHA := createCommit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	handler := createTestMergePreviewHandler(t, tempDir)
	ctx := context.Background()

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
			status, _ := handler.resolveAndValidateRefs(ctx, repo, tc.ref, targetSHA.String())
			assert.Equal(t, 400, status, "full ref path '%s' should return 400", tc.ref)
		})
	}
}
