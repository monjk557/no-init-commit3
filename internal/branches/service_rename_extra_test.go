package branches

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

// service_rename_extra_test.go — service.go 의 renameBranch / updateBranchCommit /
// CreateBranch 저수준 경로를 실제 bare 레포로 커버한다. 기존 service_test.go 의
// newBranchService / newBareRepoWithMainNative / trimOutput 헬퍼를 재사용한다.

// nativeSymbolicHead 는 bare 레포의 HEAD 가 가리키는 refname 을 반환한다.
func nativeSymbolicHead(t *testing.T, repoPath string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", repoPath, "symbolic-ref", "HEAD").Output()
	require.NoError(t, err)
	return trimOutput(out)
}

// createBranchAt 은 source 에서 분기한 새 브랜치를 만든다.
func createBranchAt(t *testing.T, svc *BranchService, repoPath, name, source string) {
	t.Helper()
	_, err := svc.CreateBranch(context.Background(), repoPath, CreateBranchRequest{
		Name:   name,
		Source: source,
	})
	require.NoError(t, err)
}

func Test_BranchService_RenameBranch(t *testing.T) {
	ctx := context.Background()

	t.Run("rename HEAD branch repoints HEAD", func(t *testing.T) {
		t.Parallel()
		repoPath, _ := newBareRepoWithMainNative(t) // HEAD -> refs/heads/main
		svc := newBranchService(t)

		res, err := svc.UpdateBranch(ctx, repoPath, "main", UpdateBranchRequest{NewName: "renamed"})

		require.NoError(t, err)
		require.Equal(t, "rename", res.Operation)
		require.Equal(t, "main", res.OldBranchName)
		require.Equal(t, "renamed", res.NewBranchName)
		require.True(t, res.HeadUpdated, "HEAD should be repointed to renamed branch")

		// old ref gone, new ref present
		oldExists, _, err := NativeCheckBranchExists(ctx, repoPath, "main")
		require.NoError(t, err)
		require.False(t, oldExists)
		newExists, _, err := NativeCheckBranchExists(ctx, repoPath, "renamed")
		require.NoError(t, err)
		require.True(t, newExists)
		// HEAD symbolic-ref now points at renamed
		require.Equal(t, "refs/heads/renamed", nativeSymbolicHead(t, repoPath))
	})

	t.Run("rename non-HEAD branch leaves HEAD untouched", func(t *testing.T) {
		t.Parallel()
		repoPath, _ := newBareRepoWithMainNative(t)
		svc := newBranchService(t)
		createBranchAt(t, svc, repoPath, "feature", "main")

		res, err := svc.UpdateBranch(ctx, repoPath, "feature", UpdateBranchRequest{NewName: "feature2"})

		require.NoError(t, err)
		require.False(t, res.HeadUpdated, "HEAD points to main, not the renamed branch")
		require.Equal(t, "refs/heads/main", nativeSymbolicHead(t, repoPath))
	})

	t.Run("force rename over existing branch deletes target first", func(t *testing.T) {
		t.Parallel()
		repoPath, _ := newBareRepoWithMainNative(t)
		svc := newBranchService(t)
		createBranchAt(t, svc, repoPath, "feature", "main")
		createBranchAt(t, svc, repoPath, "target", "main")

		res, err := svc.UpdateBranch(ctx, repoPath, "feature", UpdateBranchRequest{
			NewName: "target",
			Force:   true,
		})

		require.NoError(t, err)
		require.Equal(t, "target", res.NewBranchName)
		require.True(t, res.Forced)
		oldExists, _, err := NativeCheckBranchExists(ctx, repoPath, "feature")
		require.NoError(t, err)
		require.False(t, oldExists)
	})

	t.Run("rename to existing without force errors", func(t *testing.T) {
		t.Parallel()
		repoPath, _ := newBareRepoWithMainNative(t)
		svc := newBranchService(t)
		createBranchAt(t, svc, repoPath, "feature", "main")
		createBranchAt(t, svc, repoPath, "taken", "main")

		_, err := svc.UpdateBranch(ctx, repoPath, "feature", UpdateBranchRequest{NewName: "taken"})

		require.Error(t, err)
		require.Contains(t, err.Error(), "already exists")
	})

	t.Run("rename to same name errors", func(t *testing.T) {
		t.Parallel()
		repoPath, _ := newBareRepoWithMainNative(t)
		svc := newBranchService(t)

		_, err := svc.UpdateBranch(ctx, repoPath, "main", UpdateBranchRequest{NewName: "main"})

		require.Error(t, err)
		require.Contains(t, err.Error(), "cannot be the same")
	})

	t.Run("rename non-existent branch errors", func(t *testing.T) {
		t.Parallel()
		repoPath, _ := newBareRepoWithMainNative(t)
		svc := newBranchService(t)

		_, err := svc.UpdateBranch(ctx, repoPath, "ghost", UpdateBranchRequest{NewName: "whatever"})

		require.Error(t, err)
		require.Contains(t, err.Error(), "does not exist")
	})
}

func Test_BranchService_UpdateBranchCommit(t *testing.T) {
	ctx := context.Background()

	t.Run("already at target is a no-op fast-forward", func(t *testing.T) {
		t.Parallel()
		repoPath, commit := newBareRepoWithMainNative(t)
		svc := newBranchService(t)
		createBranchAt(t, svc, repoPath, "feature", "main")

		res, err := svc.UpdateBranch(ctx, repoPath, "feature", UpdateBranchRequest{Target: "main"})

		require.NoError(t, err)
		require.Equal(t, "update", res.Operation)
		require.True(t, res.FastForward)
		require.Equal(t, commit, res.CommitHash)
		require.Contains(t, res.Message, "already points")
	})

	t.Run("non-fast-forward without force errors", func(t *testing.T) {
		t.Parallel()
		repoPath, _ := newBareRepoWithMainNative(t)
		svc := newBranchService(t)
		// Make a second, unrelated commit reachable via a new branch "other".
		other := commitEmptyOrphan(t, repoPath, "other")

		_, err := svc.UpdateBranch(ctx, repoPath, "main", UpdateBranchRequest{Target: other})

		require.Error(t, err)
		require.Contains(t, err.Error(), "fast-forward")
	})

	t.Run("non-fast-forward with force succeeds", func(t *testing.T) {
		t.Parallel()
		repoPath, _ := newBareRepoWithMainNative(t)
		svc := newBranchService(t)
		other := commitEmptyOrphan(t, repoPath, "other")

		res, err := svc.UpdateBranch(ctx, repoPath, "main", UpdateBranchRequest{
			Target: other,
			Force:  true,
		})

		require.NoError(t, err)
		require.True(t, res.Forced)
		require.Equal(t, other, res.CommitHash)
	})

	t.Run("invalid target ref errors", func(t *testing.T) {
		t.Parallel()
		repoPath, _ := newBareRepoWithMainNative(t)
		svc := newBranchService(t)

		_, err := svc.UpdateBranch(ctx, repoPath, "main", UpdateBranchRequest{Target: "nope-nope"})

		require.Error(t, err)
		require.Contains(t, err.Error(), "resolve target")
	})
}

func Test_BranchService_CreateBranch_ExtraPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("HEAD is never repointed by branch creation", func(t *testing.T) {
		t.Parallel()
		repoPath, _ := newBareRepoWithMainNative(t)
		svc := newBranchService(t)
		headBefore := nativeSymbolicHead(t, repoPath)

		_, err := svc.CreateBranch(ctx, repoPath, CreateBranchRequest{
			Name:   "not-checked-out",
			Source: "main",
		})

		require.NoError(t, err)
		require.Equal(t, headBefore, nativeSymbolicHead(t, repoPath),
			"기본 브랜치 변경은 PUT /head 전용 endpoint 가 담당한다")
	})

	t.Run("source_branch field used when source empty", func(t *testing.T) {
		t.Parallel()
		repoPath, _ := newBareRepoWithMainNative(t)
		svc := newBranchService(t)

		res, err := svc.CreateBranch(ctx, repoPath, CreateBranchRequest{
			Name:         "from-source-branch",
			SourceBranch: "main",
		})

		require.NoError(t, err)
		require.Equal(t, "main", res.Source)
		require.True(t, res.IsNewBranch)
	})

	t.Run("invalid source ref errors", func(t *testing.T) {
		t.Parallel()
		repoPath, _ := newBareRepoWithMainNative(t)
		svc := newBranchService(t)

		_, err := svc.CreateBranch(ctx, repoPath, CreateBranchRequest{
			Name:   "bad",
			Source: "does-not-exist",
		})

		require.Error(t, err)
		require.Contains(t, err.Error(), "resolve source")
	})
}

// commitEmptyOrphan 은 부모 없는 (main 과 non-fast-forward 관계인) 새 커밋을
// 만들고 그 해시를 반환한다. 브랜치 "name" 도 함께 만들어 object 가 살아있게 한다.
func commitEmptyOrphan(t *testing.T, repoPath, name string) string {
	t.Helper()

	// empty tree
	out, err := exec.Command("git", "--git-dir", repoPath, "hash-object", "-w", "-t", "tree", "/dev/null").Output()
	require.NoError(t, err)
	treeHash := trimOutput(out)

	cmd := exec.Command("git", "--git-dir", repoPath, "commit-tree", treeHash, "-m", "orphan")
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.com",
		"GIT_AUTHOR_DATE=2025-01-01T00:00:00+00:00",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.com",
		"GIT_COMMITTER_DATE=2025-01-01T00:00:00+00:00",
	)
	out, err = cmd.Output()
	require.NoError(t, err)
	commit := trimOutput(out)

	require.NoError(t, exec.Command("git", "--git-dir", repoPath, "update-ref", "refs/heads/"+name, commit).Run())
	return commit
}
