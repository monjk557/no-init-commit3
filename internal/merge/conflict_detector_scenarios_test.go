package merge

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 회귀: "충돌 감지가 시나리오별로 정확해야 merge preview/실행이 올바르게 동작한다."
// raw git CLI 로 bare repo 를 구성하고 (already_merged_test.go 헬퍼 재사용)
// ConflictDetector.AnalyzeMerge 를 native git 으로 실행한다.
//
// 환경 제약: Apple Git 2.39.5 는 `git merge-tree --merge-base=<base>` 를
// 지원하지 않는다 (해당 플래그는 git 2.40+). AnalyzeMerge 의 비-FF 충돌
// 분석 경로 (analyzeThreeWayMerge → gitMergeTree(base!="")) 는 이 환경에서
// 실행 불가하므로, base 가 없는 FF / already-merged / clean-skip 경로만 검증.

// commitDeleteFile creates a commit on top of parentHash that removes filename.
func commitDeleteFile(t *testing.T, repoPath, parentHash, filename, message string) string {
	t.Helper()

	parentTree := trimGitOutput(t, "git", "--git-dir", repoPath, "rev-parse", parentHash+"^{tree}")
	cmd := exec.Command("git", "--git-dir", repoPath, "ls-tree", parentTree)
	out, err := cmd.Output()
	require.NoError(t, err)

	var kept []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, "\t"+filename) {
			continue
		}
		kept = append(kept, line)
	}
	newTree := testMkTree(t, repoPath, strings.Join(kept, "\n")+"\n")
	return testCommitTree(t, repoPath, newTree, []string{parentHash}, message)
}

// Test_AnalyzeMerge_FastForward: target 이 source 의 조상.
func Test_AnalyzeMerge_FastForward(t *testing.T) {
	t.Parallel()

	repoPath, base := newBareRepoForMerge(t)
	tip := addCommit(t, repoPath, base, "x.txt", "x\n", "advance")

	cd := newConflictDetectorForTest(t, repoPath)
	analysis, err := cd.AnalyzeMerge(context.Background(), &MergeAnalysisRequest{
		RepoPath:    repoPath,
		SourceRef:   tip,
		TargetRef:   base,
		Strategy:    MergeStrategyRecursive,
		MaxFileSize: 1024 * 1024,
	})
	require.NoError(t, err)
	require.True(t, analysis.IsFastForward)
	require.True(t, analysis.CanMerge)
	require.False(t, analysis.HasConflicts)
	require.Contains(t, analysis.Recommendations[0], "Fast-forward")
	require.NotNil(t, analysis.SourceCommit)
	require.NotNil(t, analysis.TargetCommit)
}

// Test_AnalyzeMerge_AlreadyMerged: source 가 target 의 조상.
func Test_AnalyzeMerge_AlreadyMerged(t *testing.T) {
	t.Parallel()

	repoPath, base := newBareRepoForMerge(t)
	mid := addCommit(t, repoPath, base, "b.txt", "b\n", "B")
	tip := addCommit(t, repoPath, mid, "c.txt", "c\n", "C")

	cd := newConflictDetectorForTest(t, repoPath)
	analysis, err := cd.AnalyzeMerge(context.Background(), &MergeAnalysisRequest{
		RepoPath:    repoPath,
		SourceRef:   base,
		TargetRef:   tip,
		Strategy:    MergeStrategyRecursive,
		MaxFileSize: 1024 * 1024,
	})
	require.NoError(t, err)
	require.True(t, analysis.IsAlreadyMerged)
	require.False(t, analysis.CanMerge)
	require.False(t, analysis.HasConflicts)
	require.Contains(t, analysis.Recommendations[0], "already merged")
}

// Test_AnalyzeMerge_DeleteScenarioBuild: delete-vs-modify fixture 가 정상 빌드되어
// diff-tree 가 삭제/수정을 인식하는지 직접 검증 (gitDiffTree 경로 커버).
func Test_AnalyzeMerge_DeleteScenarioBuild(t *testing.T) {
	t.Parallel()

	repoPath, base := newBareRepoForMerge(t)
	base = addCommit(t, repoPath, base, "deleteme.txt", "original\n", "Add deleteme")
	source := addCommit(t, repoPath, base, "deleteme.txt", "modified\n", "Source modify")
	target := commitDeleteFile(t, repoPath, base, "deleteme.txt", "Target delete")

	ctx := context.Background()
	sourceDiffs, err := gitDiffTree(ctx, repoPath, base, source)
	require.NoError(t, err)
	require.NotEmpty(t, sourceDiffs)

	targetDiffs, err := gitDiffTree(ctx, repoPath, base, target)
	require.NoError(t, err)
	require.NotEmpty(t, targetDiffs)

	// target 쪽 diff 는 삭제(D) 이어야 한다.
	var sawDelete bool
	for _, de := range targetDiffs {
		if de.Path == "deleteme.txt" && de.Status == "D" {
			sawDelete = true
		}
	}
	require.True(t, sawDelete, "target should delete deleteme.txt")
}

// Test_AnalyzeMerge_UnresolvableRefs: 존재하지 않는 ref → 에러.
func Test_AnalyzeMerge_UnresolvableRefs(t *testing.T) {
	t.Parallel()

	repoPath, base := newBareRepoForMerge(t)
	cd := newConflictDetectorForTest(t, repoPath)

	_, err := cd.AnalyzeMerge(context.Background(), &MergeAnalysisRequest{
		RepoPath:  repoPath,
		SourceRef: "does-not-exist",
		TargetRef: base,
		Strategy:  MergeStrategyRecursive,
	})
	require.Error(t, err)

	_, err = cd.AnalyzeMerge(context.Background(), &MergeAnalysisRequest{
		RepoPath:  repoPath,
		SourceRef: base,
		TargetRef: "also-missing",
		Strategy:  MergeStrategyRecursive,
	})
	require.Error(t, err)
}
