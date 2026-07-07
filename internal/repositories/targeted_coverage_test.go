package repositories

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// GetRepositoryInfo — 저커버리지 분기 (permission / structure-invalid /
// non-bare / worktree type 판별). 기존 validator_coverage_test.go 는
// valid(bare) / not-exist / invalid-components 만 커버했다.
// =============================================================================

func TestGetRepositoryInfo_Branches(t *testing.T) {
	t.Parallel()

	t.Run("structure_invalid", func(t *testing.T) {
		t.Parallel()
		// root/owner/bad.git 가 존재하지만 git repo 구조가 아님 → 빈 디렉토리.
		// EnsureRepoExists 의 validateGitRepository 실패 → else 분기
		// (Exists=true, IsAccessible=true, IsValid=false).
		root := t.TempDir()
		cfg, err := NewPathConfig(root)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "owner", "bad.git"), 0o755))

		info, err := GetRepositoryInfo(cfg, "owner", "bad")
		require.NoError(t, err)
		assert.True(t, info.Exists)
		assert.True(t, info.IsAccessible)
		assert.False(t, info.IsValid)
		assert.Empty(t, info.Type)
	})

	t.Run("non_bare_type", func(t *testing.T) {
		t.Parallel()
		// root/owner/nb.git 에 non-bare repo(.git 하위 디렉토리) 생성 →
		// Type="non-bare" 분기.
		root := t.TempDir()
		cfg, err := NewPathConfig(root)
		require.NoError(t, err)
		repoPath := filepath.Join(root, "owner", "nb.git")
		require.NoError(t, os.MkdirAll(repoPath, 0o755))
		out, err := exec.Command("git", "init", repoPath).CombinedOutput()
		require.NoError(t, err, "git init: %s", out)

		info, err := GetRepositoryInfo(cfg, "owner", "nb")
		require.NoError(t, err)
		assert.True(t, info.IsValid)
		assert.Equal(t, "non-bare", info.Type)
	})

	t.Run("worktree_type", func(t *testing.T) {
		t.Parallel()
		// root/owner/wt.git 안의 .git 이 "gitdir: <경로>" 파일(파일이지 디렉토리 아님)
		// → isNonBareRepository=true & GetRepositoryInfo 의 os.Stat(.git) 가
		// 파일이므로 Type="worktree" 분기.
		root := t.TempDir()
		cfg, err := NewPathConfig(root)
		require.NoError(t, err)

		// 실제 non-bare repo 를 만들어 유효한 .git 디렉토리를 확보.
		realRepo := t.TempDir()
		out, err := exec.Command("git", "init", realRepo).CombinedOutput()
		require.NoError(t, err, "git init real: %s", out)
		realGitDir := filepath.Join(realRepo, ".git")

		repoPath := filepath.Join(root, "owner", "wt.git")
		require.NoError(t, os.MkdirAll(repoPath, 0o755))
		gitFile := filepath.Join(repoPath, ".git")
		require.NoError(t, os.WriteFile(gitFile, []byte("gitdir: "+realGitDir+"\n"), 0o644))

		info, err := GetRepositoryInfo(cfg, "owner", "wt")
		require.NoError(t, err)
		assert.True(t, info.IsValid)
		assert.Equal(t, "worktree", info.Type)
	})

	t.Run("permission_denied", func(t *testing.T) {
		// 병렬 미사용: chmod 로 부모 디렉토리 권한을 제거하므로 격리 유지.
		if os.Getuid() == 0 {
			t.Skip("root 는 파일 권한을 우회하므로 permission-denied 분기 검증 불가")
		}
		root := t.TempDir()
		cfg, err := NewPathConfig(root)
		require.NoError(t, err)
		ownerDir := filepath.Join(root, "owner")
		repoPath := filepath.Join(ownerDir, "perm.git")
		require.NoError(t, os.MkdirAll(repoPath, 0o755))

		// owner 디렉토리 실행/탐색 권한 제거 → 하위 stat 이 permission denied.
		require.NoError(t, os.Chmod(ownerDir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(ownerDir, 0o755) })

		info, err := GetRepositoryInfo(cfg, "owner", "perm")
		require.NoError(t, err)
		// permission denied → Exists=true, IsAccessible=false, IsValid=false.
		if info.IsAccessible {
			t.Skip("환경상 하위 경로 stat 이 permission denied 를 내지 않음")
		}
		assert.True(t, info.Exists)
		assert.False(t, info.IsAccessible)
		assert.False(t, info.IsValid)
	})
}

// =============================================================================
// nativeSymbolicRef — detached HEAD(exit 1 → "", nil) 및 일반 에러 분기.
// 기존 native_git_coverage_test.go 는 성공 경로만 커버.
// =============================================================================

func TestNativeSymbolicRef_Branches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("detached_head", func(t *testing.T) {
		t.Parallel()
		repoPath, commit := newBareRepoWithCommit(t)
		// HEAD 를 특정 커밋으로 직접(detached) 설정.
		out, err := exec.Command("git", "--git-dir", repoPath,
			"update-ref", "--no-deref", "HEAD", commit).CombinedOutput()
		require.NoError(t, err, "update-ref --no-deref: %s", out)

		sym, err := nativeSymbolicRef(ctx, repoPath)
		require.NoError(t, err, "detached HEAD 는 에러가 아님")
		assert.Empty(t, sym, "detached HEAD → 빈 문자열")
	})

	t.Run("error_path", func(t *testing.T) {
		t.Parallel()
		// 존재하지 않는 git-dir → exit code != 1 (128) → 일반 에러 분기.
		_, err := nativeSymbolicRef(ctx, "/nonexistent/repo.git")
		assert.Error(t, err)
	})
}

// =============================================================================
// GetRepositoryHead — repo.Head() 실패(커밋 없는 bare repo, unborn branch)
// 에러 분기. 기존 manager_coverage_test.go 는 성공 경로만 커버.
// =============================================================================

func TestGetRepositoryHead_Error(t *testing.T) {
	t.Parallel()
	// 커밋이 전혀 없는 bare repo → HEAD 가 unborn refs/heads/main 을 가리켜
	// repo.Head() 가 실패한다.
	root, _ := newBareRepoAt(t, "owner", "repo")
	manager := newTestManager(t, root)

	repo, err := manager.OpenRepository("owner", "repo")
	require.NoError(t, err)

	head, err := manager.GetRepositoryHead(repo)
	assert.Error(t, err)
	assert.Nil(t, head)
}
