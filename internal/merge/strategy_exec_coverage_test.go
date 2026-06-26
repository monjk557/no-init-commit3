package merge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 회귀: "merge 실행 경로 (performMerge/three-way/squash/rebase + resolveMergeTree
// + createMergedTree) 는 ours/theirs/subtree 전략에서 git merge-tree 를 우회해
// 트리를 직접 반환하므로, --merge-base= 미지원 환경 (Apple Git 2.39.5) 에서도
// 실행 가능하다." 핸들러의 analysis 단계를 건너뛰고 strategy 메서드를 직접
// 호출하여 실제 commit 생성/ref 갱신까지 검증한다.
//
// recursive/ORT 전략의 실제 3-way merge (gitMergeTree base!="") 는 환경 제약
// 으로 직접 실행 불가 — fallback/직접반환 분기만 커버.

func divergedRepo(t *testing.T) (*MergeOperationsHandler, string, string, string) {
	t.Helper()
	handler, repoPath, base := setupMergeRepo(t, "exec", "proj")
	// diverged: source adds src.txt, target adds tgt.txt (different files, clean).
	source := addCommit(t, repoPath, base, "src.txt", "src\n", "source change")
	target := addCommit(t, repoPath, base, "tgt.txt", "tgt\n", "target change")
	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", target)
	return handler, repoPath, source, target
}

func cleanAnalysis() *MergeAnalysisResult {
	return &MergeAnalysisResult{HasConflicts: false, CanMerge: true}
}

func Test_performMerge_OursStrategy(t *testing.T) {
	t.Parallel()
	handler, repoPath, source, target := divergedRepo(t)

	req := &MergeRequest{
		SourceRef: source, TargetRef: "main", Method: MergeMethodMerge, Strategy: MergeStrategyOurs,
		Message: "ours merge",
	}
	res, err := handler.performMerge(context.Background(), repoPath, source, target, req, cleanAnalysis())
	require.NoError(t, err)
	require.NotNil(t, res.MergeCommit)
	// ours → result tree == target tree (tgt.txt present, src.txt absent).
	data, err := gitShowFile(context.Background(), repoPath, res.MergeCommit.Hash, "tgt.txt")
	require.NoError(t, err)
	require.Equal(t, "tgt\n", string(data))
	_, err = gitShowFile(context.Background(), repoPath, res.MergeCommit.Hash, "src.txt")
	require.Error(t, err) // src.txt not in ours tree
	// merge commit has two parents (target, source).
	require.Len(t, res.MergeCommit.ParentHashes, 2)
}

func Test_performMerge_TheirsStrategy(t *testing.T) {
	t.Parallel()
	handler, repoPath, source, target := divergedRepo(t)

	req := &MergeRequest{
		SourceRef: source, TargetRef: "main", Method: MergeMethodMerge, Strategy: MergeStrategyTheirs,
	}
	res, err := handler.performMerge(context.Background(), repoPath, source, target, req, cleanAnalysis())
	require.NoError(t, err)
	require.NotNil(t, res.MergeCommit)
	// theirs → result tree == source tree (src.txt present).
	data, err := gitShowFile(context.Background(), repoPath, res.MergeCommit.Hash, "src.txt")
	require.NoError(t, err)
	require.Equal(t, "src\n", string(data))
}

func Test_performMerge_Squash_Ours(t *testing.T) {
	t.Parallel()
	handler, repoPath, source, target := divergedRepo(t)

	req := &MergeRequest{
		SourceRef: source, TargetRef: "main", Method: MergeMethodSquash, Strategy: MergeStrategyOurs,
	}
	res, err := handler.performMerge(context.Background(), repoPath, source, target, req, cleanAnalysis())
	require.NoError(t, err)
	require.NotNil(t, res.MergeCommit)
	// squash commit has single parent (target only).
	require.Len(t, res.MergeCommit.ParentHashes, 1)
}

func Test_performMerge_Rebase_NothingToRebase(t *testing.T) {
	t.Parallel()
	// source already based on target (linear) → rebase short-circuits to target.
	handler, repoPath, base := setupMergeRepo(t, "exec", "proj")
	tip := addCommit(t, repoPath, base, "x.txt", "x\n", "advance")
	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", tip)

	// source=base, target=tip: mergeBase(base,tip)=base==source → nothing to rebase.
	req := &MergeRequest{SourceRef: base, TargetRef: "main", Method: MergeMethodRebase, Strategy: MergeStrategyRecursive}
	res, err := handler.performMerge(context.Background(), repoPath, base, tip, req, cleanAnalysis())
	require.NoError(t, err)
	require.NotNil(t, res.MergeCommit)
	require.Equal(t, tip, res.MergeCommit.Hash)
}

func Test_resolveMergeTree_DirectBranches(t *testing.T) {
	t.Parallel()
	handler, repoPath, source, target := divergedRepo(t)
	ctx := context.Background()

	sourceCommit, err := gitCatFileCommit(ctx, repoPath, source)
	require.NoError(t, err)
	targetCommit, err := gitCatFileCommit(ctx, repoPath, target)
	require.NoError(t, err)

	// conflict + force + ours → target tree
	conflicted := &MergeAnalysisResult{HasConflicts: true, ConflictCount: 1}
	tree, err := handler.resolveMergeTree(ctx, repoPath, sourceCommit, targetCommit, conflicted, MergeStrategyOurs, true, false)
	require.NoError(t, err)
	require.Equal(t, targetCommit.TreeHash, tree)

	// conflict + force + theirs → source tree
	tree, err = handler.resolveMergeTree(ctx, repoPath, sourceCommit, targetCommit, conflicted, MergeStrategyTheirs, true, false)
	require.NoError(t, err)
	require.Equal(t, sourceCommit.TreeHash, tree)

	// conflict + no force (merge) → target tree
	tree, err = handler.resolveMergeTree(ctx, repoPath, sourceCommit, targetCommit, conflicted, MergeStrategyRecursive, false, false)
	require.NoError(t, err)
	require.Equal(t, targetCommit.TreeHash, tree)

	// conflict + no force (squash) → source tree
	tree, err = handler.resolveMergeTree(ctx, repoPath, sourceCommit, targetCommit, conflicted, MergeStrategyRecursive, false, true)
	require.NoError(t, err)
	require.Equal(t, sourceCommit.TreeHash, tree)

	// conflict + force + subtree → source tree
	tree, err = handler.resolveMergeTree(ctx, repoPath, sourceCommit, targetCommit, conflicted, MergeStrategySubtree, true, false)
	require.NoError(t, err)
	require.Equal(t, sourceCommit.TreeHash, tree)
}

func Test_createMergedTree_DirectStrategies(t *testing.T) {
	t.Parallel()
	handler, repoPath, source, target := divergedRepo(t)
	ctx := context.Background()

	sourceCommit, err := gitCatFileCommit(ctx, repoPath, source)
	require.NoError(t, err)
	targetCommit, err := gitCatFileCommit(ctx, repoPath, target)
	require.NoError(t, err)

	// ours → target tree (createMergedTree → createMergedTreeWithBase ours branch).
	tree, err := handler.createMergedTree(ctx, repoPath, sourceCommit, targetCommit, cleanAnalysis(), MergeStrategyOurs)
	require.NoError(t, err)
	require.Equal(t, targetCommit.TreeHash, tree)

	// theirs → source tree.
	tree, err = handler.createMergedTree(ctx, repoPath, sourceCommit, targetCommit, cleanAnalysis(), MergeStrategyTheirs)
	require.NoError(t, err)
	require.Equal(t, sourceCommit.TreeHash, tree)

	// subtree → source tree.
	tree, err = handler.createMergedTree(ctx, repoPath, sourceCommit, targetCommit, cleanAnalysis(), MergeStrategySubtree)
	require.NoError(t, err)
	require.Equal(t, sourceCommit.TreeHash, tree)

	// nil commits → error.
	_, err = handler.createMergedTree(ctx, repoPath, nil, targetCommit, cleanAnalysis(), MergeStrategyOurs)
	require.Error(t, err)
}

func Test_createMergedTreeWithBase_EmptyBase(t *testing.T) {
	t.Parallel()
	handler, repoPath, source, target := divergedRepo(t)
	ctx := context.Background()

	sourceCommit, _ := gitCatFileCommit(ctx, repoPath, source)
	targetCommit, _ := gitCatFileCommit(ctx, repoPath, target)

	_, err := handler.createMergedTreeWithBase(ctx, repoPath, sourceCommit, targetCommit, "", cleanAnalysis(), MergeStrategyRecursive)
	require.Error(t, err) // empty base hash rejected
}

func Test_getFileContentNative(t *testing.T) {
	t.Parallel()
	root, repoPath, base := setupPreviewRepoBare(t, "fc", "proj")
	_ = root
	commit := addCommit(t, repoPath, base, "file.txt", "file content\n", "add file")
	ctx := context.Background()

	diffs, err := gitDiffTree(ctx, repoPath, base, commit)
	require.NoError(t, err)
	require.NotEmpty(t, diffs)

	h := &MergePreviewHandler{}
	var de DiffEntry
	for _, d := range diffs {
		if d.Path == "file.txt" {
			de = d
		}
	}
	content := h.getFileContentNative(ctx, repoPath, &de)
	require.NotNil(t, content)
	// Added file → DstHash present → TargetContent populated.
	require.Equal(t, "file content\n", content.TargetContent)
}

// setupPreviewRepoBare creates a bare repo without a handler (for direct fn tests).
func setupPreviewRepoBare(t *testing.T, owner, repo string) (string, string, string) {
	t.Helper()
	repoPath, base := newBareRepoForMerge(t)
	return owner + "/" + repo, repoPath, base
}
