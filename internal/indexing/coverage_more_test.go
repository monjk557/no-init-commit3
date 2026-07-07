package indexing

import (
	"context"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getAllCommitsFromRepo 는 HEAD 에서부터 커밋을 순회해 oldest-first 로 반환한다.
func TestGetAllCommitsFromRepo(t *testing.T) {
	db := newTestDB(t)

	t.Run("returns all commits oldest first", func(t *testing.T) {
		b := NewIndexBuilder(db, &fakeGitService{}, nil)
		repo := buildMemoryRepo(t)
		commits, err := b.getAllCommitsFromRepo(context.Background(), repo)
		require.NoError(t, err)
		require.Len(t, commits, 2)
		// oldest-first: 첫 커밋 메시지가 "initial".
		assert.Equal(t, "initial", commits[0].Message)
		assert.Equal(t, "second", commits[1].Message)
	})

	t.Run("respects MaxCommitsPerRun limit", func(t *testing.T) {
		cfg := &IndexConfig{MaxCommitsPerRun: 1}
		b := NewIndexBuilder(db, &fakeGitService{}, cfg)
		repo := buildMemoryRepo(t)
		commits, err := b.getAllCommitsFromRepo(context.Background(), repo)
		require.NoError(t, err)
		assert.Len(t, commits, 1)
	})

	t.Run("empty repo without HEAD errors", func(t *testing.T) {
		b := NewIndexBuilder(db, &fakeGitService{}, nil)
		emptyRepo, err := gogit.Init(memory.NewStorage(), nil)
		require.NoError(t, err)
		_, err = b.getAllCommitsFromRepo(context.Background(), emptyRepo)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get HEAD")
	})

	t.Run("cancelled context aborts iteration", func(t *testing.T) {
		b := NewIndexBuilder(db, &fakeGitService{}, nil)
		repo := buildMemoryRepo(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := b.getAllCommitsFromRepo(ctx, repo)
		require.Error(t, err)
	})
}
