package state

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildheadinfo_test.go — buildHeadInfo 의 detached-HEAD 분기(및 short-hash
// 경계)를 커버한다. branch 경로는 GetRepositoryState_Success 에서 이미 커버됨.

func TestBuildHeadInfo(t *testing.T) {
	repoPath := newBareRepo(t)
	svc, err := NewStateService(testLogger(t))
	require.NoError(t, err)

	// 실제 HEAD commit hash 확보 (nativeGetCommitInfo 가 이 hash 로 git log 실행).
	out, err := exec.Command("git", "--git-dir", repoPath, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	headHash := trim(out)
	require.NotEmpty(t, headHash)

	ctx := context.Background()

	t.Run("branch head", func(t *testing.T) {
		info := svc.buildHeadInfo(ctx, repoPath, "refs/heads/main", headHash)
		require.NotNil(t, info)
		assert.False(t, info.IsDetached)
		assert.True(t, info.IsBranch)
		assert.Equal(t, "branch", info.Type)
		assert.Equal(t, "main", info.Name)
		assert.Equal(t, "refs/heads/main", info.Target)
		assert.Equal(t, headHash, info.CommitSHA)
		assert.Equal(t, "init", info.Commit.Message)
	})

	t.Run("detached head uses short hash as name", func(t *testing.T) {
		info := svc.buildHeadInfo(ctx, repoPath, "", headHash)
		require.NotNil(t, info)
		assert.True(t, info.IsDetached)
		assert.False(t, info.IsBranch)
		assert.Equal(t, "commit", info.Type)
		assert.Equal(t, headHash[:7], info.Name)
		assert.Equal(t, headHash, info.Target)
		assert.Equal(t, headHash, info.CommitSHA)
	})

	t.Run("detached head with short hash leaves name empty", func(t *testing.T) {
		// len < 7 → Name 미설정 경계. commit info 는 조회 실패해도 best-effort.
		info := svc.buildHeadInfo(ctx, repoPath, "", "abc")
		require.NotNil(t, info)
		assert.True(t, info.IsDetached)
		assert.Equal(t, "commit", info.Type)
		assert.Empty(t, info.Name)
		assert.Equal(t, "abc", info.Target)
	})
}
