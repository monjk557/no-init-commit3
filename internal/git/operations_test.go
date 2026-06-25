package git

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"

	"github.com/collabops/git-executor/internal/testutil/gitrepo"
)

// 회귀: "PerformOperation 이 git 상태/refs/기본 분기를 정확히 보고해야 함"

func openRepo(t *testing.T) *gogit.Repository {
	t.Helper()
	tr := gitrepo.NewRepo(t)
	tr.Commit("file1.txt", "content", "initial commit")
	return tr.Repo
}

func TestPerformOperation_Status(t *testing.T) {
	repo := openRepo(t)
	op := PerformOperation(context.Background(), repo, "status")
	assert.True(t, op.Success)
	require.NoError(t, op.Error)
	assert.GreaterOrEqual(t, op.Duration.Nanoseconds(), int64(0))
}

func TestPerformOperation_StatusNoHead(t *testing.T) {
	// 커밋 없는 빈 repo 는 HEAD 가 없어 status 가 실패해야 함
	tr := gitrepo.NewRepo(t)
	op := PerformOperation(context.Background(), tr.Repo, "status")
	assert.False(t, op.Success)
	assert.Error(t, op.Error)
}

func TestPerformOperation_Refs(t *testing.T) {
	repo := openRepo(t)
	op := PerformOperation(context.Background(), repo, "refs")
	assert.True(t, op.Success)
	require.NoError(t, op.Error)
}

func TestPerformOperation_Default(t *testing.T) {
	repo := openRepo(t)
	op := PerformOperation(context.Background(), repo, "unknown-op")
	assert.True(t, op.Success)
	assert.NoError(t, op.Error)
}
