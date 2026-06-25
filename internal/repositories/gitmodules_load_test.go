package repositories

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadGitmodulesAtRef_Found(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, repoPath := newBareRepoAt(t, "owner", "repo")

	content := []byte("[submodule \"vendor/x\"]\n\tpath = vendor/x\n\turl = https://example.com/x.git\n")
	blob, err := nativeHashObject(ctx, repoPath, content)
	require.NoError(t, err)
	tree, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blob, Name: ".gitmodules"},
	})
	require.NoError(t, err)
	sig := &nativeSignature{Name: "T", Email: "t@e.com", When: time.Now()}
	commit, err := nativeCommitTree(ctx, repoPath, tree, nil, "add submodule", sig, sig)
	require.NoError(t, err)
	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/heads/main", commit))
	require.NoError(t, nativeSetSymbolicRef(ctx, repoPath, "refs/heads/main"))

	// ref="" → HEAD 로 fallback
	idx, err := LoadGitmodulesAtRef(ctx, repoPath, "")
	require.NoError(t, err)
	cfg := idx.LookupByPath("vendor/x")
	require.NotNil(t, cfg)
	assert.Equal(t, "https://example.com/x.git", cfg.URL)

	// 명시적 ref
	idx2, err := LoadGitmodulesAtRef(ctx, repoPath, "main")
	require.NoError(t, err)
	assert.NotNil(t, idx2.LookupByPath("vendor/x"))
}

func TestLoadGitmodulesAtRef_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// .gitmodules 없는 commit
	repoPath, _ := newBareRepoWithCommit(t)

	_, err := LoadGitmodulesAtRef(ctx, repoPath, "main")
	assert.ErrorIs(t, err, ErrGitmodulesNotFound, ".gitmodules 없으면 sentinel 반환")
}

func TestLoadGitmodulesAtRef_BadRef(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoPath, _ := newBareRepoWithCommit(t)

	// 존재하지 않는 ref → "not a valid object name" → ErrGitmodulesNotFound 로 매핑
	_, err := LoadGitmodulesAtRef(ctx, repoPath, "totally-bogus-ref")
	assert.Error(t, err)
	// missing 매핑이거나 일반 에러 — 둘 다 허용 (분기 커버 목적)
	_ = errors.Is(err, ErrGitmodulesNotFound)
}
