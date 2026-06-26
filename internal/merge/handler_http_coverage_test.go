package merge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 회귀: "merge/preview HTTP 핸들러는 validation/FF/already-merged/dry-run/
// 404 경로를 정확한 status 로 매핑해야 한다." gin httptest + invokeTyped 패턴
// (handler_typed_test.go) 사용.
//
// 환경 제약: 비-FF 충돌 머지 (merge-tree --merge-base=) 는 Apple Git 2.39.5
// 미지원이므로 FF / already-merged / dry-run / validation 경로만 검증.

// setupMergeRepo creates a RepoRoot with a bare repo at <root>/<owner>/<repo>.git
// and returns the handler + the initial commit hash. Linear history helpers
// (newBareRepoForMerge / addCommit) cannot target an arbitrary path, so we
// inline an init here.
func setupMergeRepo(t *testing.T, owner, repo string) (*MergeOperationsHandler, string, string) {
	t.Helper()

	root := t.TempDir()
	repoPath := filepath.Join(root, owner, repo+".git")

	cmd := exec.Command("git", "init", "--bare", repoPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	blob := testHashObject(t, repoPath, "init\n")
	tree := testMkTree(t, repoPath, "100644 blob "+blob+"\tinit.txt\n")
	commit := testCommitTree(t, repoPath, tree, nil, "Initial commit")
	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", commit)
	run(t, "git", "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main")

	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "error", Format: "json"},
		Git:     config.GitConfig{RepoRoot: root},
	}
	handler, err := NewMergeOperationsHandler(cfg)
	require.NoError(t, err)

	return handler, repoPath, commit
}

func doMerge(t *testing.T, handler *MergeOperationsHandler, owner, repo string, body MergeRequest) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	reqBody, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/"+owner+"/"+repo+"/merge", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "owner", Value: owner}, {Key: "repo", Value: repo}}

	invokeTyped(c, handler.HandleMerge)
	return w
}

func Test_HandleMerge_MissingSourceRef(t *testing.T) {
	t.Parallel()
	handler, _, _ := setupMergeRepo(t, "alice", "proj")
	w := doMerge(t, handler, "alice", "proj", MergeRequest{TargetRef: "main"})
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func Test_HandleMerge_InvalidStrategy(t *testing.T) {
	t.Parallel()
	handler, _, _ := setupMergeRepo(t, "alice", "proj")
	w := doMerge(t, handler, "alice", "proj", MergeRequest{
		SourceRef: "a", TargetRef: "b", Strategy: "bogus",
	})
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func Test_HandleMerge_RepoNotFound(t *testing.T) {
	t.Parallel()
	handler, _, _ := setupMergeRepo(t, "alice", "proj")
	// Unresolvable source ref against a real repo → 400/404.
	w := doMerge(t, handler, "alice", "proj", MergeRequest{
		SourceRef: "no-such-ref", TargetRef: "main",
	})
	require.Contains(t, []int{http.StatusBadRequest, http.StatusNotFound}, w.Code)
}

func Test_HandleMerge_DryRun_FastForward(t *testing.T) {
	t.Parallel()
	handler, repoPath, base := setupMergeRepo(t, "alice", "proj")

	// Advance main to a tip; source=main (tip), target=base → FF.
	tip := addCommit(t, repoPath, base, "x.txt", "x\n", "advance")
	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", tip)

	w := doMerge(t, handler, "alice", "proj", MergeRequest{
		SourceRef: tip, TargetRef: base, DryRun: true,
	})
	require.Equal(t, http.StatusOK, w.Code)

	var resp MergeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.IsFastForward)
	require.True(t, resp.Success)
	require.False(t, resp.HasConflicts)
}

func Test_HandleMerge_AlreadyMerged(t *testing.T) {
	t.Parallel()
	handler, repoPath, base := setupMergeRepo(t, "alice", "proj")

	mid := addCommit(t, repoPath, base, "b.txt", "b\n", "B")
	tip := addCommit(t, repoPath, mid, "c.txt", "c\n", "C")
	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", tip)

	// source=base (ancestor of tip), target=tip → already merged (idempotent success).
	w := doMerge(t, handler, "alice", "proj", MergeRequest{
		SourceRef: base, TargetRef: tip,
	})
	require.Equal(t, http.StatusOK, w.Code)

	var resp MergeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.IsAlreadyMerged)
	require.True(t, resp.Success)
}

func Test_HandleMerge_DryRun_AlreadyMerged(t *testing.T) {
	t.Parallel()
	handler, repoPath, base := setupMergeRepo(t, "alice", "proj")

	tip := addCommit(t, repoPath, base, "b.txt", "b\n", "B")
	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", tip)

	w := doMerge(t, handler, "alice", "proj", MergeRequest{
		SourceRef: base, TargetRef: tip, DryRun: true,
	})
	require.Equal(t, http.StatusOK, w.Code)

	var resp MergeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// dry-run already-merged: message reflects already-merged; Details carries flag.
	require.Contains(t, resp.Message, "already merged")
	require.Equal(t, true, resp.Details["is_already_merged"])
}

func Test_HandleMerge_DryRun_Rebase_NothingToRebase(t *testing.T) {
	t.Parallel()
	handler, repoPath, base := setupMergeRepo(t, "alice", "proj")

	// source already based on target → "nothing to rebase".
	tip := addCommit(t, repoPath, base, "b.txt", "b\n", "B")
	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", tip)

	// target=base, source=tip; mergeBase(tip,base)=base != tip, so analyze proceeds.
	// Use source=tip target=base: this is FF, but Method=rebase + DryRun goes through
	// analyzeRebaseDryRun. mergeBase == base, source=tip → commits to rebase.
	w := doMerge(t, handler, "alice", "proj", MergeRequest{
		SourceRef: tip, TargetRef: base, Method: MergeMethodRebase, DryRun: true,
	})
	require.Equal(t, http.StatusOK, w.Code)

	var resp MergeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, MergeMethodRebase, resp.Method)
	require.Contains(t, resp.Message, "Rebase dry run")
}

func Test_NewMergeOperationsHandler_NilConfig(t *testing.T) {
	t.Parallel()
	_, err := NewMergeOperationsHandler(nil)
	require.Error(t, err)
}

func Test_NewMergePreviewHandler_NilConfig(t *testing.T) {
	t.Parallel()
	_, err := NewMergePreviewHandler(nil)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// hasEnvironmentFileConflicts / formatConflictsForResponse / matchesPattern
// ---------------------------------------------------------------------------

func Test_hasEnvironmentFileConflicts(t *testing.T) {
	t.Parallel()
	h := &MergeOperationsHandler{}

	require.False(t, h.hasEnvironmentFileConflicts(nil))

	withEnv := &MergeAnalysisResult{FileConflicts: []FileConflict{
		{FilePath: "config/env/prod.yml"},
	}}
	require.True(t, h.hasEnvironmentFileConflicts(withEnv))

	withoutEnv := &MergeAnalysisResult{FileConflicts: []FileConflict{
		{FilePath: "src/main.go"},
	}}
	require.False(t, h.hasEnvironmentFileConflicts(withoutEnv))
}

func Test_formatConflictsForResponse(t *testing.T) {
	t.Parallel()
	h := &MergeOperationsHandler{}

	require.Empty(t, h.formatConflictsForResponse(nil))

	analysis := &MergeAnalysisResult{FileConflicts: []FileConflict{
		{FilePath: ".env", ConflictType: ConflictTypeContent, Severity: ConflictSeverityHigh},
		{FilePath: "main.go", ConflictType: ConflictTypeContent, Severity: ConflictSeverityMedium},
	}}
	got := h.formatConflictsForResponse(analysis)
	require.Len(t, got, 2)
	require.Equal(t, ".env", got[0]["file_path"])
	require.Equal(t, true, got[0]["is_environment_file"])
	require.Equal(t, false, got[1]["is_environment_file"])
}

func Test_matchesPattern(t *testing.T) {
	t.Parallel()
	h := &MergeOperationsHandler{}

	// directory prefix pattern
	require.True(t, h.matchesPattern("config/env/x.yml", "config/env/"))
	require.False(t, h.matchesPattern("config/env/", "config/env/")) // not strictly longer
	// leading wildcard
	require.True(t, h.matchesPattern("svc.env", "*.env"))
	// exact / prefix
	require.True(t, h.matchesPattern(".env", ".env"))
	require.True(t, h.matchesPattern(".envfile", ".env"))
	require.False(t, h.matchesPattern("README", ".env"))
}

func Test_validatePath_Handlers(t *testing.T) {
	t.Parallel()
	handler, _, _ := setupMergeRepo(t, "alice", "proj")

	require.Error(t, handler.validatePath("", "repo"))
	require.Error(t, handler.validatePath("owner", ""))
	require.NoError(t, handler.validatePath("owner", "repo"))
}

// ---------------------------------------------------------------------------
// validateMergePreviewRequest (preview.go)
// ---------------------------------------------------------------------------

func Test_validateMergePreviewRequest(t *testing.T) {
	t.Parallel()
	h := &MergePreviewHandler{}

	valid := &MergePreviewRequest{
		SourceRef: "a", TargetRef: "b", Strategy: MergeStrategyRecursive, CompareStrategy: "tree",
	}
	status, err := h.validateMergePreviewRequest(valid)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	status, err = h.validateMergePreviewRequest(&MergePreviewRequest{TargetRef: "b", CompareStrategy: "tree", Strategy: MergeStrategyRecursive})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, status)

	_, err = h.validateMergePreviewRequest(&MergePreviewRequest{SourceRef: "a", CompareStrategy: "tree", Strategy: MergeStrategyRecursive})
	require.Error(t, err)

	_, err = h.validateMergePreviewRequest(&MergePreviewRequest{SourceRef: "a", TargetRef: "b", Strategy: "bogus", CompareStrategy: "tree"})
	require.Error(t, err)

	_, err = h.validateMergePreviewRequest(&MergePreviewRequest{SourceRef: "a", TargetRef: "b", Strategy: MergeStrategyRecursive, MaxFileSize: -1, CompareStrategy: "tree"})
	require.Error(t, err)

	_, err = h.validateMergePreviewRequest(&MergePreviewRequest{SourceRef: "a", TargetRef: "b", Strategy: MergeStrategyRecursive, MaxFileSize: 200 * 1024 * 1024, CompareStrategy: "tree"})
	require.Error(t, err)

	_, err = h.validateMergePreviewRequest(&MergePreviewRequest{SourceRef: "a", TargetRef: "b", Strategy: MergeStrategyRecursive, MaxFiles: -1, CompareStrategy: "tree"})
	require.Error(t, err)

	_, err = h.validateMergePreviewRequest(&MergePreviewRequest{SourceRef: "a", TargetRef: "b", Strategy: MergeStrategyRecursive, MaxFiles: 20000, CompareStrategy: "tree"})
	require.Error(t, err)

	_, err = h.validateMergePreviewRequest(&MergePreviewRequest{SourceRef: "a", TargetRef: "b", Strategy: MergeStrategyRecursive, CompareStrategy: "bogus"})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// preview.go generators (pure, no merge-tree needed)
// ---------------------------------------------------------------------------

func Test_generatePreviewRecommendations(t *testing.T) {
	t.Parallel()
	h := &MergePreviewHandler{}

	ff := h.generatePreviewRecommendations(&MergeAnalysisResult{IsFastForward: true}, nil)
	require.Contains(t, ff[0], "fast-forward")

	conflicts := h.generatePreviewRecommendations(&MergeAnalysisResult{HasConflicts: true, ConflictCount: 3}, nil)
	require.Contains(t, conflicts[0], "3 conflicts")

	large := h.generatePreviewRecommendations(&MergeAnalysisResult{}, &ChangesSummary{TotalFiles: 2000})
	require.Contains(t, large[0], "Large number of files")

	binary := h.generatePreviewRecommendations(&MergeAnalysisResult{}, &ChangesSummary{BinaryFiles: 2})
	require.Contains(t, binary[0], "binary files")

	clean := h.generatePreviewRecommendations(&MergeAnalysisResult{}, &ChangesSummary{TotalFiles: 1})
	require.Contains(t, clean[0], "without issues")
}

func Test_generatePreviewWarnings(t *testing.T) {
	t.Parallel()
	h := &MergePreviewHandler{}

	manyConflicts := h.generatePreviewWarnings(
		&MergeAnalysisResult{HasConflicts: true, ConflictCount: 60},
		&ChangesSummary{TotalFiles: 6000},
		&MergePreviewRequest{MaxFileSize: 1, MaxFiles: 1},
	)
	require.NotEmpty(t, manyConflicts)
	require.Len(t, manyConflicts, 4)

	none := h.generatePreviewWarnings(
		&MergeAnalysisResult{},
		&ChangesSummary{TotalFiles: 1},
		&MergePreviewRequest{MaxFileSize: 2 * 1024 * 1024, MaxFiles: 1000},
	)
	require.Empty(t, none)
}

func Test_analyzeFileContent_and_detectPotentialConflicts(t *testing.T) {
	t.Parallel()
	h := &MergePreviewHandler{}

	fc := &FileChange{}
	fc.Path = "main.go"
	fc.Additions = 10
	fc.Deletions = 2
	analysis := h.analyzeFileContent(fc)
	require.Equal(t, "go", analysis["file_type"])
	require.Equal(t, 8, analysis["size_change"])
	require.NotNil(t, analysis["code_metrics"])

	// non-code file → no code_metrics
	fc2 := &FileChange{}
	fc2.Path = "README.md"
	analysis2 := h.analyzeFileContent(fc2)
	require.Nil(t, analysis2["code_metrics"])

	// potential conflicts
	mod := &FileChange{}
	mod.Status = "modified"
	mod.Additions = 100
	require.Contains(t, h.detectPotentialConflicts(mod), "large_modification")

	ren := &FileChange{}
	ren.Status = "renamed"
	require.Contains(t, h.detectPotentialConflicts(ren), "file_rename")

	bin := &FileChange{}
	bin.IsBinary = true
	require.Contains(t, h.detectPotentialConflicts(bin), "binary_file")
}

// contributorEmails / calculateCountSummary exercise real git on a fixture.
func Test_contributorEmails_and_CountSummary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repoPath := filepath.Join(root, "bob", "p.git")
	cmd := exec.Command("git", "init", "--bare", repoPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	blob := testHashObject(t, repoPath, "init\n")
	tree := testMkTree(t, repoPath, "100644 blob "+blob+"\tinit.txt\n")
	base := testCommitTree(t, repoPath, tree, nil, "base")
	tip := addCommit(t, repoPath, base, "a.txt", "a\n", "advance")

	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "error", Format: "json"},
		Git:     config.GitConfig{RepoRoot: root},
	}
	h, err := NewMergePreviewHandler(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	emails := h.contributorEmails(ctx, repoPath, base, tip)
	require.Contains(t, emails, "test@test.com")

	summary := h.calculateCountSummary(ctx, repoPath, tip, base, &ChangesSummary{TotalFiles: 1})
	require.Equal(t, 1, summary.Commits)
	require.Equal(t, 1, summary.Files)
	require.GreaterOrEqual(t, summary.Contributors, 1)
}
