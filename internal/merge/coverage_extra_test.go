package merge

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 회귀: "rebase 실행 / resolveMergeTree fallback / rebase dry-run / merge diff
// pagination / preview tree 비교 경로는 --merge-base= 미지원 환경에서도
// 직접반환·fallback 분기를 통해 실행 가능해야 한다."

// ---------------------------------------------------------------------------
// performRebase (strategy_rebase.go)
// ---------------------------------------------------------------------------

// Test_performRebase_SingleCommit_Theirs exercises the full rebase loop once.
// source=B (child of base A), target=A. mergeBase(B,A)=A != B so the rebase
// proceeds. The single commit B is a fast-forward vs the current base A, so the
// inner analyzeMerge avoids the unsupported merge-tree base path; with the
// "theirs" strategy resolveMergeTree returns the source tree directly.
func Test_performRebase_SingleCommit_Theirs(t *testing.T) {
	t.Parallel()
	handler, repoPath, base := setupMergeRepo(t, "reb", "proj")
	b := addCommit(t, repoPath, base, "a.txt", "modified\n", "B modifies")

	req := &MergeRequest{
		SourceRef: b, TargetRef: "main", Method: MergeMethodRebase, Strategy: MergeStrategyTheirs,
	}
	res, err := handler.performRebase(context.Background(), repoPath, b, base, req, cleanAnalysis())
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotEqual(t, base, res.Hash, "rebase should produce a new commit")
	require.Len(t, res.ParentHashes, 1)
	require.Equal(t, base, res.ParentHashes[0])

	// main ref advanced to the rebased commit.
	head := trimGitOutput(t, "git", "--git-dir", repoPath, "rev-parse", "refs/heads/main")
	require.Equal(t, res.Hash, head)
}

// Test_performRebase_AnalyzeError drives the loop into the analyzeMerge error
// branch: source=B and target=T diverge, so analyzing B onto T needs the
// unsupported merge-tree base flag and returns an error.
func Test_performRebase_AnalyzeError(t *testing.T) {
	t.Parallel()
	handler, repoPath, base := setupMergeRepo(t, "reberr", "proj")
	b := addCommit(t, repoPath, base, "b.txt", "b\n", "source B")
	target := addCommit(t, repoPath, base, "t.txt", "t\n", "target T")

	req := &MergeRequest{
		SourceRef: b, TargetRef: "main", Method: MergeMethodRebase, Strategy: MergeStrategyRecursive,
	}
	_, err := handler.performRebase(context.Background(), repoPath, b, target, req, cleanAnalysis())
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// resolveMergeTree fallback + no-conflict paths (strategy_merge.go)
// ---------------------------------------------------------------------------

func Test_resolveMergeTree_FallbackAndClean(t *testing.T) {
	t.Parallel()
	handler, repoPath, source, target := divergedRepo(t)
	ctx := context.Background()

	sourceCommit, err := gitCatFileCommit(ctx, repoPath, source)
	require.NoError(t, err)
	targetCommit, err := gitCatFileCommit(ctx, repoPath, target)
	require.NoError(t, err)

	t.Run("conflict+force+recursive falls back to target tree", func(t *testing.T) {
		// recursive needs merge-tree base (unsupported) → graceful fallback.
		conflicted := &MergeAnalysisResult{HasConflicts: true, ConflictCount: 2}
		tree, err := handler.resolveMergeTree(ctx, repoPath, sourceCommit, targetCommit, conflicted, MergeStrategyRecursive, true, false)
		require.NoError(t, err)
		require.Equal(t, targetCommit.TreeHash, tree)
	})

	t.Run("conflict+force+recursive+squash falls back to source tree", func(t *testing.T) {
		conflicted := &MergeAnalysisResult{HasConflicts: true, ConflictCount: 2}
		tree, err := handler.resolveMergeTree(ctx, repoPath, sourceCommit, targetCommit, conflicted, MergeStrategyRecursive, true, true)
		require.NoError(t, err)
		require.Equal(t, sourceCommit.TreeHash, tree)
	})

	t.Run("conflict+force+unknown strategy uses fallback tree", func(t *testing.T) {
		conflicted := &MergeAnalysisResult{HasConflicts: true, ConflictCount: 1}
		tree, err := handler.resolveMergeTree(ctx, repoPath, sourceCommit, targetCommit, conflicted, MergeStrategy("bogus"), true, false)
		require.NoError(t, err)
		require.Equal(t, targetCommit.TreeHash, tree)
	})

	t.Run("no-conflict recursive falls back to target tree", func(t *testing.T) {
		tree, err := handler.resolveMergeTree(ctx, repoPath, sourceCommit, targetCommit, cleanAnalysis(), MergeStrategyRecursive, false, false)
		require.NoError(t, err)
		require.Equal(t, targetCommit.TreeHash, tree)
	})

	t.Run("no-conflict ours returns target tree directly", func(t *testing.T) {
		tree, err := handler.resolveMergeTree(ctx, repoPath, sourceCommit, targetCommit, cleanAnalysis(), MergeStrategyOurs, false, false)
		require.NoError(t, err)
		require.Equal(t, targetCommit.TreeHash, tree)
	})
}

// Test_createMergedTreeWithBase_RecursiveError covers the merge-tree invocation
// error return (recursive strategy with a real base on git 2.39.5).
func Test_createMergedTreeWithBase_RecursiveError(t *testing.T) {
	t.Parallel()
	handler, repoPath, source, target := divergedRepo(t)
	ctx := context.Background()

	sourceCommit, err := gitCatFileCommit(ctx, repoPath, source)
	require.NoError(t, err)
	targetCommit, err := gitCatFileCommit(ctx, repoPath, target)
	require.NoError(t, err)

	base := trimGitOutput(t, "git", "--git-dir", repoPath, "merge-base", source, target)

	_, err = handler.createMergedTreeWithBase(ctx, repoPath, sourceCommit, targetCommit, base, cleanAnalysis(), MergeStrategyRecursive)
	// git 2.39.5 rejects --merge-base=; on 2.40+ this succeeds. Accept both.
	if err != nil {
		require.Contains(t, err.Error(), "merge-tree")
	}
}

// ---------------------------------------------------------------------------
// analyzeRebaseDryRun (analysis.go)
// ---------------------------------------------------------------------------

func Test_analyzeRebaseDryRun_Direct(t *testing.T) {
	t.Parallel()
	handler, repoPath, base := setupMergeRepo(t, "dry", "proj")
	ctx := context.Background()

	t.Run("linear source onto base: no conflicts", func(t *testing.T) {
		// A(base) -> B -> C, target = A. Each commit is FF vs A → analyzeMerge
		// short-circuits without the unsupported merge-tree base path.
		b := addCommit(t, repoPath, base, "b.txt", "b\n", "B")
		c := addCommit(t, repoPath, b, "c.txt", "c\n", "C")

		req := &MergeRequest{SourceRef: c, TargetRef: base, Method: MergeMethodRebase, Strategy: MergeStrategyRecursive}
		res, err := handler.analyzeRebaseDryRun(ctx, repoPath, c, base, req)
		require.NoError(t, err)
		require.False(t, res.HasConflicts)
		require.True(t, res.CanMerge)
		require.Equal(t, 2, res.Metadata["commits_to_rebase"])
		require.Equal(t, "sequential", res.Metadata["rebase_method"])
		require.Contains(t, res.Recommendations[0], "without conflicts")
	})

	t.Run("nothing to rebase when source based on target", func(t *testing.T) {
		// source=base is an ancestor of tip → mergeBase(base,tip)=base=source.
		tip := addCommit(t, repoPath, base, "x.txt", "x\n", "advance")
		req := &MergeRequest{SourceRef: base, TargetRef: tip, Method: MergeMethodRebase, Strategy: MergeStrategyRecursive}
		res, err := handler.analyzeRebaseDryRun(ctx, repoPath, base, tip, req)
		require.NoError(t, err)
		require.False(t, res.HasConflicts)
		require.Contains(t, res.Recommendations[0], "already based")
	})

	t.Run("diverged commit is skipped via analyze error", func(t *testing.T) {
		// source=B, target=T diverge; analyzeMerge(B,T) errors → loop continues.
		b := addCommit(t, repoPath, base, "src.txt", "s\n", "src")
		target := addCommit(t, repoPath, base, "tgt.txt", "t\n", "tgt")
		req := &MergeRequest{SourceRef: b, TargetRef: target, Method: MergeMethodRebase, Strategy: MergeStrategyRecursive}
		res, err := handler.analyzeRebaseDryRun(ctx, repoPath, b, target, req)
		require.NoError(t, err)
		// analyzeMerge error → commit skipped → no conflicts recorded.
		require.False(t, res.HasConflicts)
	})

	t.Run("bad ref returns error", func(t *testing.T) {
		req := &MergeRequest{SourceRef: "nope", TargetRef: base, Method: MergeMethodRebase, Strategy: MergeStrategyRecursive}
		_, err := handler.analyzeRebaseDryRun(ctx, repoPath, "does-not-exist", base, req)
		require.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// HandleMerge real execution (fast-forward, no dry-run)
// ---------------------------------------------------------------------------

// Test_HandleMerge_FastForwardExecute drives HandleMerge through the full
// (non-dry-run) execution path: analyze → lock → performMerge → response build.
// A fast-forward merge avoids the unsupported merge-tree base flag.
func Test_HandleMerge_FastForwardExecute(t *testing.T) {
	t.Parallel()
	handler, repoPath, base := setupMergeRepo(t, "ff", "proj")
	tip := addCommit(t, repoPath, base, "x.txt", "x\n", "advance")
	// point a target branch at base so we can merge tip into it.
	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/target", base)
	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", tip)

	w := doMerge(t, handler, "ff", "proj", MergeRequest{
		SourceRef: tip, TargetRef: "target",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp MergeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotNil(t, resp.MergeCommit)
	require.Equal(t, "target", resp.Details["target_ref"])
}

// ---------------------------------------------------------------------------
// HandleMergeDiff pagination / context (handler.go)
// ---------------------------------------------------------------------------

func Test_HandleMergeDiff_Pagination(t *testing.T) {
	t.Parallel()
	h, repoPath, base := multiFileDiffRepo(t, "pg", "proj")

	t.Run("page 1 with small page size", func(t *testing.T) {
		w := doDiff(t, h, "pg", "proj", map[string]string{
			"source_ref": "main", "target_ref": base,
			"page": "1", "page_size": "2",
		})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var resp struct {
			Files []json.RawMessage `json:"files"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.LessOrEqual(t, len(resp.Files), 2)
	})

	t.Run("page beyond total yields empty files", func(t *testing.T) {
		w := doDiff(t, h, "pg", "proj", map[string]string{
			"source_ref": "main", "target_ref": base,
			"page": "999", "page_size": "10",
		})
		require.Equal(t, http.StatusOK, w.Code)
		var resp struct {
			Files []json.RawMessage `json:"files"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Empty(t, resp.Files)
	})

	t.Run("explicit zero context", func(t *testing.T) {
		w := doDiff(t, h, "pg", "proj", map[string]string{
			"source_ref": "main", "target_ref": base, "context": "0",
		})
		require.Equal(t, http.StatusOK, w.Code)
	})

	_ = repoPath
}

// multiFileDiffRepo builds a preview handler over a repo whose main tip adds
// several files on top of base (so a merge-base diff yields multiple files).
func multiFileDiffRepo(t *testing.T, owner, repo string) (*MergePreviewHandler, string, string) {
	t.Helper()
	h, repoPath, base, _ := setupPreviewRepo(t, owner, repo)
	tip := base
	for i := 0; i < 5; i++ {
		tip = addCommit(t, repoPath, tip, "f"+strconv.Itoa(i)+".txt", "content "+strconv.Itoa(i)+"\n", "add f"+strconv.Itoa(i))
	}
	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", tip)
	return h, repoPath, base
}

// ---------------------------------------------------------------------------
// compareRepositoryTrees + getFileContentNative (preview.go) — direct
// ---------------------------------------------------------------------------

func Test_compareRepositoryTrees_Direct(t *testing.T) {
	t.Parallel()
	h, repoPath, base := multiFileDiffRepo(t, "cmp", "proj")
	ctx := context.Background()
	tip := trimGitOutput(t, "git", "--git-dir", repoPath, "rev-parse", "refs/heads/main")

	t.Run("include content, added files", func(t *testing.T) {
		metrics := &PreviewMetrics{}
		req := &MergePreviewRequest{IncludeContent: true, MaxFiles: 1000}
		summary, changes, err := h.compareRepositoryTrees(ctx, repoPath, base, tip, req, metrics)
		require.NoError(t, err)
		require.NotNil(t, summary)
		require.NotEmpty(t, changes)
		require.Equal(t, len(changes), summary.TotalFiles)
		require.GreaterOrEqual(t, summary.AddedFiles, 1)
		// added text file has content fetched.
		var withContent bool
		for _, c := range changes {
			if c.Content != nil && c.Content.TargetContent != "" {
				withContent = true
			}
		}
		require.True(t, withContent)
	})

	t.Run("maxFiles caps the result", func(t *testing.T) {
		metrics := &PreviewMetrics{}
		req := &MergePreviewRequest{MaxFiles: 2}
		_, changes, err := h.compareRepositoryTrees(ctx, repoPath, base, tip, req, metrics)
		require.NoError(t, err)
		require.LessOrEqual(t, len(changes), 2)
	})

	t.Run("cancelled context aborts", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		metrics := &PreviewMetrics{}
		req := &MergePreviewRequest{MaxFiles: 1000}
		_, _, err := h.compareRepositoryTrees(cctx, repoPath, base, tip, req, metrics)
		require.Error(t, err)
	})

	t.Run("bad tree returns error", func(t *testing.T) {
		metrics := &PreviewMetrics{}
		req := &MergePreviewRequest{MaxFiles: 1000}
		_, _, err := h.compareRepositoryTrees(ctx, repoPath, "deadbeef", tip, req, metrics)
		require.Error(t, err)
	})
}

func Test_getFileContentNative_Modified(t *testing.T) {
	t.Parallel()
	h, repoPath, _, _ := setupPreviewRepo(t, "gfc", "proj")
	ctx := context.Background()

	// A modified-file diff entry carries both SrcHash and DstHash so both the
	// source-side and target-side blob reads are exercised.
	v1 := testHashObject(t, repoPath, "v1\n")
	v2 := testHashObject(t, repoPath, "v2\n")
	de := DiffEntry{
		SrcMode: "100644", DstMode: "100644",
		SrcHash: v1, DstHash: v2, Status: "M", Path: "doc.txt",
	}

	content := h.getFileContentNative(ctx, repoPath, &de)
	require.NotNil(t, content)
	require.Equal(t, "v1\n", content.SourceContent)
	require.Equal(t, "v2\n", content.TargetContent)
	require.False(t, content.IsTruncated)
}

// ---------------------------------------------------------------------------
// NewBatchCatFile error path (gitcmd.go)
// ---------------------------------------------------------------------------

func Test_NewBatchCatFile_CancelledContext(t *testing.T) {
	t.Parallel()
	repoPath, _ := newBareRepoForMerge(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewBatchCatFile(ctx, repoPath)
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "cat-file")
}
