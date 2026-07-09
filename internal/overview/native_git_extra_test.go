package overview

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newNativeBareRepo 는 native git 으로 main 브랜치에 단일 커밋을 가진 bare repo
// 를 만든다. (repoPath, commitHash) 반환. branches 패키지의
// newBareRepoWithMainNative 패턴을 그대로 따른다.
func newNativeBareRepo(t *testing.T) (string, string) {
	t.Helper()

	repoPath := filepath.Join(t.TempDir(), "repo.git")

	run := func(stdin string, args ...string) string {
		cmd := exec.Command("git", args...)
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, string(out))
		return strings.TrimSpace(string(out))
	}

	run("", "init", "--bare", repoPath)

	blobHash := run("hello\n", "--git-dir", repoPath, "hash-object", "-w", "--stdin")
	treeHash := run("100644 blob "+blobHash+"\tREADME.md\n", "--git-dir", repoPath, "mktree")

	cmd := exec.Command("git", "--git-dir", repoPath, "commit-tree", treeHash, "-m", "init")
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=Alice",
		"GIT_AUTHOR_EMAIL=alice@example.com",
		"GIT_AUTHOR_DATE=2024-01-01T00:00:00+00:00",
		"GIT_COMMITTER_NAME=Alice",
		"GIT_COMMITTER_EMAIL=alice@example.com",
		"GIT_COMMITTER_DATE=2024-01-01T00:00:00+00:00",
	)
	out, err := cmd.Output()
	require.NoError(t, err)
	commitHash := strings.TrimSpace(string(out))

	run("", "--git-dir", repoPath, "update-ref", "refs/heads/main", commitHash)
	run("", "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main")

	return repoPath, commitHash
}

// ---------------------------------------------------------------------------
// NativeGetCommits
// ---------------------------------------------------------------------------

func Test_NativeGetCommits_ReturnsCommits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, commitHash := newNativeBareRepo(t)

	commits, err := NativeGetCommits(ctx, repoPath, "main", 10)
	require.NoError(t, err)
	require.Len(t, commits, 1)
	assert.Equal(t, commitHash, commits[0].SHA)
	assert.Equal(t, "init", commits[0].Message)
	assert.Equal(t, "Alice", commits[0].Author.Name)
	assert.Equal(t, "alice@example.com", commits[0].Author.Email)
}

func Test_NativeGetCommits_DefaultRef_UsesHEAD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	commits, err := NativeGetCommits(ctx, repoPath, "", 10)
	require.NoError(t, err)
	require.Len(t, commits, 1)
}

func Test_NativeGetCommits_InvalidRepo_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	_, err := NativeGetCommits(ctx, filepath.Join(t.TempDir(), "nope.git"), "main", 10)
	require.Error(t, err)
}

func Test_NativeGetCommits_LimitedToOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "many.git")
	createTestRepositoryWith500CommitsAtPath(t, repoPath)

	commits, err := NativeGetCommits(ctx, repoPath, "main", 3)
	require.NoError(t, err)
	assert.Len(t, commits, 3)
}

// ---------------------------------------------------------------------------
// NativeGetRemotes
// ---------------------------------------------------------------------------

func Test_NativeGetRemotes_NoRemotes_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	remotes, err := NativeGetRemotes(ctx, repoPath)
	require.NoError(t, err)
	assert.Empty(t, remotes)
}

func Test_NativeGetRemotes_WithRemotes_ReturnsFetchURLs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	require.NoError(t, exec.Command("git", "--git-dir", repoPath,
		"remote", "add", "origin", "https://github.com/example/origin.git").Run())
	require.NoError(t, exec.Command("git", "--git-dir", repoPath,
		"remote", "add", "upstream", "https://github.com/example/upstream.git").Run())

	remotes, err := NativeGetRemotes(ctx, repoPath)
	require.NoError(t, err)
	require.Len(t, remotes, 2)

	byName := map[string]RemoteInfo{}
	for _, r := range remotes {
		byName[r.Name] = r
	}
	assert.Equal(t, "https://github.com/example/origin.git", byName["origin"].URL)
	assert.Equal(t, "https://github.com/example/upstream.git", byName["upstream"].URL)

	// 정확히 하나만 IsDefault.
	defaults := 0
	for _, r := range remotes {
		if r.IsDefault {
			defaults++
		}
	}
	assert.Equal(t, 1, defaults)
}

func Test_NativeGetRemotes_InvalidRepo_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	_, err := NativeGetRemotes(ctx, filepath.Join(t.TempDir(), "nope.git"))
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// NativeGetTags
// ---------------------------------------------------------------------------

func Test_NativeGetTags_Empty_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	tags, err := NativeGetTags(ctx, repoPath, 0)
	require.NoError(t, err)
	assert.Empty(t, tags)
}

func Test_NativeGetTags_LightweightAndAnnotated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, commitHash := newNativeBareRepo(t)

	// lightweight tag.
	require.NoError(t, exec.Command("git", "--git-dir", repoPath,
		"tag", "v1.0", commitHash).Run())

	// annotated tag.
	annotate := exec.Command("git", "--git-dir", repoPath,
		"tag", "-a", "v2.0", commitHash, "-m", "release 2.0")
	annotate.Env = append(annotate.Environ(),
		"GIT_COMMITTER_NAME=Alice",
		"GIT_COMMITTER_EMAIL=alice@example.com",
		"GIT_COMMITTER_DATE=2024-02-01T00:00:00+00:00",
	)
	require.NoError(t, annotate.Run())

	tags, err := NativeGetTags(ctx, repoPath, 0)
	require.NoError(t, err)
	require.Len(t, tags, 2)

	byName := map[string]GitTag{}
	for _, tag := range tags {
		byName[tag.Name] = tag
	}
	require.Contains(t, byName, "v1.0")
	require.Contains(t, byName, "v2.0")
	assert.False(t, byName["v1.0"].IsAnnotated, "lightweight tag")
	assert.True(t, byName["v2.0"].IsAnnotated, "annotated tag")
	assert.NotEmpty(t, byName["v1.0"].Hash)
}

func Test_NativeGetTags_RespectsLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, commitHash := newNativeBareRepo(t)
	for _, name := range []string{"a", "b", "c"} {
		require.NoError(t, exec.Command("git", "--git-dir", repoPath,
			"tag", name, commitHash).Run())
	}

	tags, err := NativeGetTags(ctx, repoPath, 2)
	require.NoError(t, err)
	assert.Len(t, tags, 2)
}

func Test_NativeGetTags_InvalidRepo_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	_, err := NativeGetTags(ctx, filepath.Join(t.TempDir(), "nope.git"), 0)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// NativeGetLatestCommit / NativeCountCommits / NativeCountBranches
// ---------------------------------------------------------------------------

func Test_NativeGetLatestCommit_ReturnsHead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, commitHash := newNativeBareRepo(t)

	commit, err := NativeGetLatestCommit(ctx, repoPath, "main")
	require.NoError(t, err)
	require.NotNil(t, commit)
	assert.Equal(t, commitHash, commit.SHA)
	assert.Equal(t, "init", commit.Message)
}

func Test_NativeGetLatestCommit_DefaultRef(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	commit, err := NativeGetLatestCommit(ctx, repoPath, "")
	require.NoError(t, err)
	require.NotNil(t, commit)
}

func Test_NativeGetLatestCommit_InvalidRepo_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	_, err := NativeGetLatestCommit(ctx, filepath.Join(t.TempDir(), "nope.git"), "main")
	require.Error(t, err)
}

func Test_NativeCountCommits_Single(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	count, approximate, err := NativeCountCommits(ctx, repoPath, "main")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.False(t, approximate)
}

func Test_NativeCountCommits_500(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "many.git")
	createTestRepositoryWith500CommitsAtPath(t, repoPath)

	count, approximate, err := NativeCountCommits(ctx, repoPath, "main")
	require.NoError(t, err)
	assert.Equal(t, 500, count)
	assert.False(t, approximate, "500 < cap, not approximate")
}

func Test_NativeCountCommits_InvalidRepo_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	_, _, err := NativeCountCommits(ctx, filepath.Join(t.TempDir(), "nope.git"), "main")
	require.Error(t, err)
}

// createTestRepositoryWithMultiplePathCommits는 root/src/docs/src 순으로 커밋을 만든다:
//   - README.md (root)  → 1 commit
//   - src/main.go, src/util.go → src 경로 2 commits
//   - docs/README.md → docs 경로 1 commit
//   - ref 전체 4 commits
func Test_NativeCountCommitsForPath_ScopesToPathspec(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tempDir := t.TempDir()
	owner, repo := "o", "r"
	createTestRepositoryWithMultiplePathCommits(t, tempDir, owner, repo)
	repoPath := filepath.Join(tempDir, owner, repo+".git")

	srcCount, approx, err := NativeCountCommitsForPath(ctx, repoPath, "main", "src")
	require.NoError(t, err)
	assert.Equal(t, 2, srcCount, "src/ 는 2개 커밋이 건드림")
	assert.False(t, approx)

	docsCount, _, err := NativeCountCommitsForPath(ctx, repoPath, "main", "docs")
	require.NoError(t, err)
	assert.Equal(t, 1, docsCount, "docs/ 는 1개 커밋이 건드림")
}

func Test_NativeCountCommitsForPath_EmptyPath_EqualsRefTotal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tempDir := t.TempDir()
	owner, repo := "o", "r"
	createTestRepositoryWithMultiplePathCommits(t, tempDir, owner, repo)
	repoPath := filepath.Join(tempDir, owner, repo+".git")

	total, _, err := NativeCountCommitsForPath(ctx, repoPath, "main", "")
	require.NoError(t, err)
	assert.Equal(t, 4, total, "path 없으면 ref 전체 커밋 수")

	refTotal, _, err := NativeCountCommits(ctx, repoPath, "main")
	require.NoError(t, err)
	assert.Equal(t, total, refTotal, "path=='' 는 NativeCountCommits 와 동일해야 함")
}

func Test_NativeCountCommitsForPath_InvalidRepo_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	_, _, err := NativeCountCommitsForPath(ctx, filepath.Join(t.TempDir(), "nope.git"), "main", "src")
	require.Error(t, err)
}

func Test_NativeCountBranches_SingleMain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, commitHash := newNativeBareRepo(t)

	n, err := NativeCountBranches(ctx, repoPath)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// 추가 브랜치 → 2.
	require.NoError(t, exec.Command("git", "--git-dir", repoPath,
		"update-ref", "refs/heads/feature", commitHash).Run())
	n, err = NativeCountBranches(ctx, repoPath)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}

// ---------------------------------------------------------------------------
// GetLatestCommitsNative (concurrent per-path)
// ---------------------------------------------------------------------------

func Test_GetLatestCommitsNative_EmptyPaths_Complete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	result, meta := GetLatestCommitsNative(ctx, repoPath, "main", nil)
	assert.Empty(t, result)
	assert.Equal(t, "complete", meta.CutReason)
	assert.False(t, meta.Incomplete)
}

func Test_GetLatestCommitsNative_KnownPath_Complete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, commitHash := newNativeBareRepo(t)

	result, meta := GetLatestCommitsNative(ctx, repoPath, "main", []string{"README.md"})
	require.Contains(t, result, "README.md")
	assert.Equal(t, commitHash, result["README.md"].SHA)
	assert.Equal(t, "complete", meta.CutReason)
	assert.NotEmpty(t, meta.ResolvedRefHash)
}

func Test_GetLatestCommitsNative_UnresolvableRef_Error(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	result, meta := GetLatestCommitsNative(ctx, repoPath, "no-such-ref", []string{"README.md"})
	assert.Empty(t, result)
	assert.True(t, meta.Incomplete)
	assert.Equal(t, "error", meta.CutReason)
}

func Test_GetLatestCommitsNative_MissingPath_StoredAsNil(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	// README.md 는 존재, ghost.txt 는 history 에 없음.
	// nativeGitLogForPath 는 history 에 없는 path 에 대해 (nil, nil) 을 반환하므로
	// 두 path 모두 result 에 들어가고 (ghost.txt 는 nil 값) → complete 로 분류된다.
	result, meta := GetLatestCommitsNative(ctx, repoPath, "main",
		[]string{"README.md", "ghost.txt"})
	require.Contains(t, result, "README.md")
	require.NotNil(t, result["README.md"])
	assert.Contains(t, result, "ghost.txt")
	assert.Nil(t, result["ghost.txt"])
	assert.Equal(t, "complete", meta.CutReason)
	assert.False(t, meta.Incomplete)
}

// ---------------------------------------------------------------------------
// NativeGetBlobContent / NativeGetTreeEntries (error paths)
// ---------------------------------------------------------------------------

func Test_NativeGetBlobContent_ReturnsContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	content, err := NativeGetBlobContent(ctx, repoPath, "main", "README.md")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(content))
}

func Test_NativeGetBlobContent_MissingPath_Error(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	_, err := NativeGetBlobContent(ctx, repoPath, "main", "nope.txt")
	require.Error(t, err)
}

func Test_NativeGetTreeEntries_Root(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	entries, err := NativeGetTreeEntries(ctx, repoPath, "main", "")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "README.md", entries[0].Name)
	assert.Equal(t, "blob", entries[0].Type)
	require.NotNil(t, entries[0].Size)
	assert.Equal(t, int64(len("hello\n")), *entries[0].Size)
}

func Test_NativeGetTreeEntries_BadTreeSpec_Error(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	_, err := NativeGetTreeEntries(ctx, repoPath, "main", "no-such-dir")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// parseLsTreeLine / parseGitLogLine (pure parsing edge cases)
// ---------------------------------------------------------------------------

func Test_ParseLsTreeLine_Blob(t *testing.T) {
	t.Parallel()

	line := "100644 blob abc123 42\tfile.go"
	entry, err := parseLsTreeLine(line, "src")
	require.NoError(t, err)
	assert.Equal(t, "blob", entry.Type)
	assert.Equal(t, "file.go", entry.Name)
	assert.Equal(t, "src/file.go", entry.Path)
	require.NotNil(t, entry.Size)
	assert.Equal(t, int64(42), *entry.Size)
}

func Test_ParseLsTreeLine_TreeNoSize(t *testing.T) {
	t.Parallel()

	line := "040000 tree def456 -\tsubdir"
	entry, err := parseLsTreeLine(line, "")
	require.NoError(t, err)
	assert.Equal(t, "tree", entry.Type)
	assert.Equal(t, "subdir", entry.Name)
	assert.Nil(t, entry.Size, "tree 는 size 없음")
}

func Test_ParseLsTreeLine_SubmoduleNormalized(t *testing.T) {
	t.Parallel()

	line := "160000 commit 0123456789abcdef -\tvendor/lib"
	entry, err := parseLsTreeLine(line, "")
	require.NoError(t, err)
	assert.Equal(t, "submodule", entry.Type, "commit → submodule 정규화")
}

func Test_ParseLsTreeLine_InvalidNoTab(t *testing.T) {
	t.Parallel()

	_, err := parseLsTreeLine("100644 blob abc123 42 noTab", "")
	require.Error(t, err)
}

func Test_ParseLsTreeLine_InvalidMetadata(t *testing.T) {
	t.Parallel()

	_, err := parseLsTreeLine("100644 blob\tfile.go", "")
	require.Error(t, err)
}

func Test_ParseGitLogLine_TooFewFields(t *testing.T) {
	t.Parallel()

	_, err := parseGitLogLine("only\x00three\x00fields")
	require.Error(t, err)
}

func Test_ParseGitLogLine_WithParents(t *testing.T) {
	t.Parallel()

	line := strings.Join([]string{
		"sha1",                 // H
		"tree1",                // T
		"parent1 parent2",      // P
		"Author",               // an
		"author@x.com",         // ae
		"2024-01-01T00:00:00Z", // aI
		"Committer",            // cn
		"committer@x.com",      // ce
		"2024-01-02T00:00:00Z", // cI
		"the subject",          // s
	}, "\x00")

	commit, err := parseGitLogLine(line)
	require.NoError(t, err)
	assert.Equal(t, "sha1", commit.SHA)
	assert.Equal(t, "the subject", commit.Message)
	assert.Equal(t, []string{"parent1", "parent2"}, commit.ParentHashes)
	assert.Equal(t, "Author", commit.Author.Name)
	assert.Equal(t, "Committer", commit.Committer.Name)
}

// ---------------------------------------------------------------------------
// nativeResolveRef / NativeGetLatestCommitForPath
// ---------------------------------------------------------------------------

func Test_NativeResolveRef_Default_HEAD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, commitHash := newNativeBareRepo(t)

	hash, err := nativeResolveRef(ctx, repoPath, "")
	require.NoError(t, err)
	assert.Equal(t, commitHash, hash)
}

func Test_NativeResolveRef_Invalid_Error(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	_, err := nativeResolveRef(ctx, repoPath, "does-not-exist")
	require.Error(t, err)
}

func Test_NativeGetLatestCommitForPath_Known(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, commitHash := newNativeBareRepo(t)

	commit, err := NativeGetLatestCommitForPath(ctx, repoPath, "main", "README.md")
	require.NoError(t, err)
	require.NotNil(t, commit)
	assert.Equal(t, commitHash, commit.SHA)
}

func Test_NativeGetLatestCommitForPath_Missing_NilNoError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	commit, err := NativeGetLatestCommitForPath(ctx, repoPath, "main", "ghost.txt")
	require.NoError(t, err)
	assert.Nil(t, commit, "history 에 없는 path 는 nil, nil")
}

func Test_NativeLoadGitmodules_Absent_ReturnsNil(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath, _ := newNativeBareRepo(t)

	idx := nativeLoadGitmodules(ctx, repoPath, "main")
	assert.Nil(t, idx, ".gitmodules 부재 → nil")
}
