package gitcmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupRefsRepo 는 main 브랜치 + lightweight/annotated 태그를 가진 repo 를 만든다.
// 반환: repoPath, head commit SHA, annotated tag object SHA.
func setupRefsRepo(t *testing.T) (repo, head, annotTagOID string) {
	t.Helper()
	repo = t.TempDir()
	gitInRepo(t, repo, "init", "-q", "-b", "main")
	gitInRepo(t, repo, "config", "user.name", "tester")
	gitInRepo(t, repo, "config", "user.email", "tester@example.com")
	gitInRepo(t, repo, "commit", "--allow-empty", "-q", "-m", "initial")
	head = gitInRepo(t, repo, "rev-parse", "HEAD")
	// lightweight tag
	gitInRepo(t, repo, "tag", "v1-light")
	// annotated tag
	gitInRepo(t, repo, "tag", "-a", "v1-annot", "-m", "release v1")
	annotTagOID = gitInRepo(t, repo, "rev-parse", "v1-annot")
	return repo, head, annotTagOID
}

func TestListRefs_AllRefs(t *testing.T) {
	repo, head, annotTagOID := setupRefsRepo(t)

	refs, err := ListRefs(context.Background(), repo)
	require.NoError(t, err)
	require.NotEmpty(t, refs)

	byName := make(map[string]RefEntry, len(refs))
	for _, r := range refs {
		byName[r.RefName] = r
	}

	// main 브랜치가 head commit 을 가리켜야 한다.
	main, ok := byName["refs/heads/main"]
	require.True(t, ok, "refs/heads/main should appear")
	assert.Equal(t, head, main.ObjectName)
	assert.Equal(t, "commit", main.ObjectType)

	// lightweight 태그는 commit 을 직접 가리킨다 (PeeledOID 없음).
	light, ok := byName["refs/tags/v1-light"]
	require.True(t, ok, "refs/tags/v1-light should appear")
	assert.Equal(t, head, light.ObjectName)
	assert.Empty(t, light.PeeledOID)

	// annotated 태그는 tag object 이며 PeeledOID 가 commit 을 가리킨다.
	annot, ok := byName["refs/tags/v1-annot"]
	require.True(t, ok, "refs/tags/v1-annot should appear")
	assert.Equal(t, annotTagOID, annot.ObjectName)
	assert.Equal(t, "tag", annot.ObjectType)
	assert.Equal(t, head, annot.PeeledOID)
}

func TestListRefs_WithPattern(t *testing.T) {
	repo, head, _ := setupRefsRepo(t)

	// heads 만 필터링.
	heads, err := ListRefs(context.Background(), repo, "refs/heads/")
	require.NoError(t, err)
	require.Len(t, heads, 1)
	assert.Equal(t, "refs/heads/main", heads[0].RefName)
	assert.Equal(t, head, heads[0].ObjectName)

	// tags 만 필터링 — light + annot 두 개.
	tags, err := ListRefs(context.Background(), repo, "refs/tags/")
	require.NoError(t, err)
	assert.Len(t, tags, 2)
}

func TestListRefs_EmptyRepo(t *testing.T) {
	// 커밋이 없는 repo 는 ref 가 없으므로 빈 슬라이스를 반환해야 한다 (에러 아님).
	repo := t.TempDir()
	gitInRepo(t, repo, "init", "-q", "-b", "main")

	refs, err := ListRefs(context.Background(), repo)
	require.NoError(t, err)
	assert.Empty(t, refs)
}

func TestListRefs_BadRepo(t *testing.T) {
	notARepo := t.TempDir()
	_, err := ListRefs(context.Background(), notARepo)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "for-each-ref")
}

func TestResolveHEAD_Symbolic(t *testing.T) {
	repo, head, _ := setupRefsRepo(t)

	info, err := ResolveHEAD(context.Background(), repo)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.True(t, info.IsSymbolic, "fresh repo HEAD should be symbolic")
	assert.Equal(t, "refs/heads/main", info.Target)
	assert.Equal(t, head, info.OID)
}

func TestResolveHEAD_Detached(t *testing.T) {
	repo, head, _ := setupRefsRepo(t)
	// HEAD 를 detached 상태로 만든다 (commit SHA 로 직접 checkout).
	gitInRepo(t, repo, "checkout", "-q", head)

	info, err := ResolveHEAD(context.Background(), repo)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.False(t, info.IsSymbolic, "detached HEAD should not be symbolic")
	assert.Empty(t, info.Target)
	assert.Equal(t, head, info.OID)
}

func TestResolveHEAD_Unborn(t *testing.T) {
	// 커밋이 없는 repo 에서 HEAD 는 unborn → rev-parse HEAD 실패해야 한다.
	repo := t.TempDir()
	gitInRepo(t, repo, "init", "-q", "-b", "main")

	info, err := ResolveHEAD(context.Background(), repo)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rev-parse HEAD")
	// HEAD 가 symbolic 이긴 하므로 info 는 부분적으로 채워져 반환된다.
	require.NotNil(t, info)
	assert.True(t, info.IsSymbolic)
	assert.Empty(t, info.OID)
}
