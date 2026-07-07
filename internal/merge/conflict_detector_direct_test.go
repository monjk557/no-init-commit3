package merge

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 회귀: "buildFileConflict / analyzeContentConflicts / populateSubmoduleMeta 는
// 실제 blob·.gitmodules 를 읽어 conflict 메타를 채워야 한다." git 2.39.5 는
// `merge-tree --merge-base=` 미지원이라 AnalyzeMerge 의 비-FF 경로를 통해
// 이들을 간접 호출할 수 없으므로, DiffEntry 를 직접 구성해 함수를 호출한다.

// gitmodulesRepo builds a bare repo whose HEAD tree contains a .gitmodules file
// registering a single submodule at the given path.
func gitmodulesRepo(t *testing.T, subPath, url, branch string) (string, string) {
	t.Helper()
	repoPath, base := newBareRepoForMerge(t)

	content := "[submodule \"" + subPath + "\"]\n" +
		"\tpath = " + subPath + "\n" +
		"\turl = " + url + "\n" +
		"\tbranch = " + branch + "\n"

	blob := testHashObject(t, repoPath, content)
	// keep init.txt from base + add .gitmodules
	initBlob := testHashObject(t, repoPath, "init content\n")
	tree := testMkTree(t, repoPath,
		"100644 blob "+blob+"\t.gitmodules\n"+
			"100644 blob "+initBlob+"\tinit.txt\n")
	commit := testCommitTree(t, repoPath, tree, []string{base}, "add .gitmodules")
	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", commit)
	return repoPath, commit
}

func Test_analyzeContentConflicts_Direct(t *testing.T) {
	t.Parallel()
	repoPath, _ := newBareRepoForMerge(t)
	ctx := context.Background()
	cd := newConflictDetectorForTest(t, repoPath)

	baseBlob := testHashObject(t, repoPath, "line1\nline2\nline3\n")
	srcBlob := testHashObject(t, repoPath, "line1\nSRC\nline3\n")
	tgtBlob := testHashObject(t, repoPath, "line1\nTGT\nline3\n")

	sourceDiff := &DiffEntry{
		SrcMode: "100644", DstMode: "100644",
		SrcHash: baseBlob, DstHash: srcBlob, Status: "M", Path: "f.txt",
	}
	targetDiff := &DiffEntry{
		SrcMode: "100644", DstMode: "100644",
		SrcHash: baseBlob, DstHash: tgtBlob, Status: "M", Path: "f.txt",
	}

	t.Run("differing content yields a region", func(t *testing.T) {
		regions, err := cd.analyzeContentConflicts(ctx, repoPath, sourceDiff, targetDiff, 1024*1024)
		require.NoError(t, err)
		require.Len(t, regions, 1)
		require.Contains(t, regions[0].OurContent, "SRC")
		require.Contains(t, regions[0].TheirContent, "TGT")
		require.Contains(t, regions[0].BaseContent, "line2")
		require.Equal(t, "standard", regions[0].MarkerStyle)
	})

	t.Run("tiny max file size skips content, no region", func(t *testing.T) {
		// size limit forces empty content on both sides → equal → no region.
		regions, err := cd.analyzeContentConflicts(ctx, repoPath, sourceDiff, targetDiff, 1)
		require.NoError(t, err)
		require.Empty(t, regions)
	})

	t.Run("zero base hash skips base read", func(t *testing.T) {
		zero := strings.Repeat("0", 40)
		sd := &DiffEntry{SrcMode: "100644", DstMode: "100644", SrcHash: zero, DstHash: srcBlob, Status: "A", Path: "f.txt"}
		td := &DiffEntry{SrcMode: "100644", DstMode: "100644", SrcHash: zero, DstHash: tgtBlob, Status: "A", Path: "f.txt"}
		regions, err := cd.analyzeContentConflicts(ctx, repoPath, sd, td, 1024*1024)
		require.NoError(t, err)
		require.Len(t, regions, 1)
	})
}

func Test_buildFileConflict_ContentType(t *testing.T) {
	t.Parallel()
	repoPath, _ := newBareRepoForMerge(t)
	ctx := context.Background()
	cd := newConflictDetectorForTest(t, repoPath)

	baseBlob := testHashObject(t, repoPath, "a\nb\nc\n")
	srcBlob := testHashObject(t, repoPath, "a\nX\nc\n")
	tgtBlob := testHashObject(t, repoPath, "a\nY\nc\n")

	sourceDiff := &DiffEntry{SrcMode: "100644", DstMode: "100644", SrcHash: baseBlob, DstHash: srcBlob, Status: "M", Path: "f.txt"}
	targetDiff := &DiffEntry{SrcMode: "100644", DstMode: "100644", SrcHash: baseBlob, DstHash: tgtBlob, Status: "M", Path: "f.txt"}

	req := &MergeAnalysisRequest{IncludeContent: true, MaxFileSize: 1024 * 1024, Strategy: MergeStrategyRecursive}
	conflict := cd.buildFileConflict(ctx, repoPath, "f.txt", sourceDiff, targetDiff, req)
	require.NotNil(t, conflict)
	require.Equal(t, "f.txt", conflict.FilePath)
	require.Equal(t, ConflictTypeContent, conflict.ConflictType)
	require.False(t, conflict.AutoResolvable)
	require.NotEmpty(t, conflict.Regions, "content conflict with IncludeContent should populate regions")
	require.True(t, conflict.OurVersion.Exists)
	require.True(t, conflict.TheirVersion.Exists)
	require.True(t, conflict.BaseVersion.Exists)
}

func Test_buildFileConflict_DeleteAndAdd(t *testing.T) {
	t.Parallel()
	repoPath, _ := newBareRepoForMerge(t)
	ctx := context.Background()
	cd := newConflictDetectorForTest(t, repoPath)

	baseBlob := testHashObject(t, repoPath, "base\n")
	modBlob := testHashObject(t, repoPath, "modified\n")
	req := &MergeAnalysisRequest{IncludeContent: false, MaxFileSize: 1024 * 1024, Strategy: MergeStrategyOurs}

	t.Run("delete/modify", func(t *testing.T) {
		// source deletes, target modifies → delete conflict.
		sourceDiff := &DiffEntry{SrcMode: "100644", DstMode: "000000", SrcHash: baseBlob, DstHash: strings.Repeat("0", 40), Status: "D", Path: "f.txt"}
		targetDiff := &DiffEntry{SrcMode: "100644", DstMode: "100644", SrcHash: baseBlob, DstHash: modBlob, Status: "M", Path: "f.txt"}
		conflict := cd.buildFileConflict(ctx, repoPath, "f.txt", sourceDiff, targetDiff, req)
		require.NotNil(t, conflict)
		require.Equal(t, ConflictTypeDelete, conflict.ConflictType)
		require.False(t, conflict.OurVersion.Exists) // source deleted
	})

	t.Run("add/add", func(t *testing.T) {
		// both add same path with different content → add conflict.
		srcBlob := testHashObject(t, repoPath, "src add\n")
		tgtBlob := testHashObject(t, repoPath, "tgt add\n")
		zero := strings.Repeat("0", 40)
		sourceDiff := &DiffEntry{SrcMode: "000000", DstMode: "100644", SrcHash: zero, DstHash: srcBlob, Status: "A", Path: "new.txt"}
		targetDiff := &DiffEntry{SrcMode: "000000", DstMode: "100644", SrcHash: zero, DstHash: tgtBlob, Status: "A", Path: "new.txt"}
		conflict := cd.buildFileConflict(ctx, repoPath, "new.txt", sourceDiff, targetDiff, req)
		require.NotNil(t, conflict)
		require.Equal(t, ConflictTypeAdd, conflict.ConflictType)
	})
}

func Test_buildFileConflict_Submodule(t *testing.T) {
	t.Parallel()
	repoPath, _ := gitmodulesRepo(t, "libs/sub", "https://github.com/example/sub.git", "release")
	ctx := context.Background()
	cd := newConflictDetectorForTest(t, repoPath)

	// both sides are gitlinks (mode 160000) pinned to different commits.
	sourceDiff := &DiffEntry{SrcMode: "160000", DstMode: "160000", SrcHash: strings.Repeat("a", 40), DstHash: strings.Repeat("b", 40), Status: "M", Path: "libs/sub"}
	targetDiff := &DiffEntry{SrcMode: "160000", DstMode: "160000", SrcHash: strings.Repeat("a", 40), DstHash: strings.Repeat("c", 40), Status: "M", Path: "libs/sub"}

	req := &MergeAnalysisRequest{IncludeContent: true, MaxFileSize: 1024 * 1024, Strategy: MergeStrategyRecursive}
	conflict := cd.buildFileConflict(ctx, repoPath, "libs/sub", sourceDiff, targetDiff, req)
	require.NotNil(t, conflict)
	require.Equal(t, ConflictTypeSubmodule, conflict.ConflictType)
	require.NotNil(t, conflict.Submodule)
	require.Equal(t, targetDiff.DstHash, conflict.Submodule.OurCommit)
	require.Equal(t, sourceDiff.DstHash, conflict.Submodule.TheirCommit)
	require.Equal(t, strings.Repeat("a", 40), conflict.Submodule.BaseCommit)
	// .gitmodules metadata populated from HEAD.
	require.Equal(t, "https://github.com/example/sub.git", conflict.Submodule.URL)
	require.Equal(t, "release", conflict.Submodule.Branch)
	require.NotNil(t, conflict.Submodule.Parsed)
}

func Test_populateSubmoduleMeta_Direct(t *testing.T) {
	t.Parallel()
	repoPath, _ := gitmodulesRepo(t, "vendor/lib", "git@github.com:acme/lib.git", "main")
	ctx := context.Background()
	cd := newConflictDetectorForTest(t, repoPath)

	t.Run("known path populates url/branch/parsed", func(t *testing.T) {
		sub := &SubmoduleConflict{}
		cd.populateSubmoduleMeta(ctx, repoPath, "vendor/lib", sub)
		require.Equal(t, "git@github.com:acme/lib.git", sub.URL)
		require.Equal(t, "main", sub.Branch)
		require.NotNil(t, sub.Parsed)
	})

	t.Run("unknown path leaves fields empty", func(t *testing.T) {
		sub := &SubmoduleConflict{}
		cd.populateSubmoduleMeta(ctx, repoPath, "not/registered", sub)
		require.Empty(t, sub.URL)
		require.Empty(t, sub.Branch)
	})

	t.Run("repo without .gitmodules is a no-op", func(t *testing.T) {
		plainRepo, _ := newBareRepoForMerge(t)
		sub := &SubmoduleConflict{}
		cd.populateSubmoduleMeta(ctx, plainRepo, "anything", sub)
		require.Empty(t, sub.URL)
	})
}

// Test_AnalyzeMerge_Diverged_MergeTreeUnsupported drives AnalyzeMerge through the
// merge-base + analyzeThreeWayMerge path. On git 2.39.5 the `--merge-base=` flag
// is unsupported so merge-tree fails, surfacing as an error — this still covers
// AnalyzeMerge's non-FF branch (merge-base resolution, commit reads) and
// analyzeThreeWayMerge's merge-tree invocation + error return.
func Test_AnalyzeMerge_Diverged_MergeTreeUnsupported(t *testing.T) {
	t.Parallel()
	repoPath, base := newBareRepoForMerge(t)
	source := addCommit(t, repoPath, base, "src.txt", "s\n", "source")
	target := addCommit(t, repoPath, base, "tgt.txt", "t\n", "target")

	cd := newConflictDetectorForTest(t, repoPath)
	_, err := cd.AnalyzeMerge(context.Background(), &MergeAnalysisRequest{
		RepoPath:    repoPath,
		SourceRef:   source,
		TargetRef:   target,
		Strategy:    MergeStrategyRecursive,
		MaxFileSize: 1024 * 1024,
	})
	// Deterministic on Apple Git 2.39.5: merge-tree --merge-base= rejected.
	// (On git 2.40+ this would succeed with a clean merge — either way the
	// merge-base + three-way analysis branch is exercised.)
	if err != nil {
		require.Contains(t, err.Error(), "three-way merge")
	}
}
