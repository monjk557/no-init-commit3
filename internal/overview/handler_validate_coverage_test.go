package overview

import (
	"context"
	"errors"
	"testing"

	"github.com/collabops/git-executor/internal/repositories"
	"github.com/stretchr/testify/require"
)

// handler_validate_coverage_test.go — validateAndOpenRepository 의 성공/실패
// branch 와 isObjectNotFoundForPack 를 커버한다.

func releaseRepoCtx(rc *repositoryContext) {
	if rc == nil {
		return
	}
	repositories.GetRepoRWLock(rc.repoPath).RUnlock()
	if rc.repoWrapper != nil {
		_ = rc.repoWrapper.Close()
	}
}

func TestValidateAndOpenRepository(t *testing.T) {
	h, tempDir, cleanup := setupTestHandler(t)
	t.Cleanup(cleanup)

	owner, repo := "o", "r"
	createTestRepositoryForHandler(t, tempDir, owner, repo)
	ctx := context.Background()

	t.Run("empty ref defaults to HEAD and opens repo", func(t *testing.T) {
		rc, err := h.validateAndOpenRepository(ctx, owner, repo, "")
		require.NoError(t, err)
		require.NotNil(t, rc)
		require.Equal(t, "HEAD", rc.ref)
		require.NotNil(t, rc.repoWrapper)
		releaseRepoCtx(rc)
	})

	t.Run("valid non-HEAD ref resolves", func(t *testing.T) {
		rc, err := h.validateAndOpenRepository(ctx, owner, repo, "main")
		require.NoError(t, err)
		require.NotNil(t, rc)
		releaseRepoCtx(rc)
	})

	t.Run("invalid ref returns error", func(t *testing.T) {
		rc, err := h.validateAndOpenRepository(ctx, owner, repo, "no-such-ref")
		require.Error(t, err)
		require.Nil(t, rc)
	})

	t.Run("missing repository returns error", func(t *testing.T) {
		rc, err := h.validateAndOpenRepository(ctx, owner, "does-not-exist", "")
		require.Error(t, err)
		require.Nil(t, rc)
	})

	t.Run("invalid path returns error", func(t *testing.T) {
		rc, err := h.validateAndOpenRepository(ctx, "../evil", repo, "")
		require.Error(t, err)
		require.Nil(t, rc)
	})
}

func TestIsObjectNotFoundForPack(t *testing.T) {
	t.Parallel()

	t.Run("object not found error returns true", func(t *testing.T) {
		res := overviewResult{treeErr: errors.New("git: Object Not Found in pack")}
		require.True(t, isObjectNotFoundForPack(res))
	})

	t.Run("other error returns false", func(t *testing.T) {
		res := overviewResult{treeErr: errors.New("some other failure")}
		require.False(t, isObjectNotFoundForPack(res))
	})

	t.Run("no error returns false", func(t *testing.T) {
		res := overviewResult{}
		require.False(t, isObjectNotFoundForPack(res))
	})
}
