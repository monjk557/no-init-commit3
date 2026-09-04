package protocol

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/gitutil"
)

// newUnbornHeadRepo 는 "초기 커밋 없이 만든 저장소" 상태를 재현한다: 객체는
// 있지만 브랜치 ref 는 하나도 없고 HEAD 는 실재하지 않는 main 을 가리킨다.
// 반환값은 (repoRoot, repoPath, commitSHA).
func newUnbornHeadRepo(t *testing.T, owner, repo string) (string, string, string) {
	t.Helper()
	root := t.TempDir()

	work := t.TempDir()
	gitX(t, work, "init", "-q", "-b", "seed")
	gitX(t, work, "config", "user.name", "tester")
	gitX(t, work, "config", "user.email", "tester@example.com")
	gitX(t, work, "commit", "--allow-empty", "-q", "-m", "initial commit")
	commitSHA := gitX(t, work, "rev-parse", "HEAD")

	repoPath := filepath.Join(root, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))
	gitX(t, root, "clone", "--bare", "-q", work, repoPath)
	// 브랜치를 모두 지우고 HEAD 를 unborn main 으로 되돌린다.
	gitX(t, repoPath, "symbolic-ref", "HEAD", "refs/heads/main")
	gitX(t, repoPath, "update-ref", "-d", "refs/heads/seed")

	head, err := gitutil.HeadRefName(context.Background(), repoPath)
	require.NoError(t, err)
	require.Equal(t, "refs/heads/main", head)
	require.False(t, gitutil.RefExists(context.Background(), repoPath, head), "fixture must start unborn")

	return root, repoPath, commitSHA
}

func TestProcessRefUpdate_FirstPushAdoptsNonDefaultBranch(t *testing.T) {
	root, repoPath, commitSHA := newUnbornHeadRepo(t, "acme", "fresh")
	h, _ := newEventsHandler(t, root)

	cmd := RefUpdateCommand{
		RefName: "refs/heads/develop",
		OldHash: tagEventZeroHash,
		NewHash: commitSHA,
	}
	target := gitutil.PreferAdoptionRef(createdBranchRefs([]RefUpdateCommand{cmd}))
	require.NoError(t, h.processRefUpdate(context.Background(), repoPath, cmd, "acme", "fresh", "user-1", target))

	head, err := gitutil.HeadRefName(context.Background(), repoPath)
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/develop", head, "첫 push 브랜치가 default branch 가 되어야 한다")
}

func TestProcessRefUpdate_LaterPushDoesNotMoveHead(t *testing.T) {
	root, repoPath, commitSHA := newUnbornHeadRepo(t, "acme", "fresh")
	h, _ := newEventsHandler(t, root)

	// 첫 push: main 이 생기며 HEAD 가 정상화된다.
	first := RefUpdateCommand{RefName: "refs/heads/main", OldHash: tagEventZeroHash, NewHash: commitSHA}
	require.NoError(t, h.processRefUpdate(context.Background(), repoPath, first, "acme", "fresh", "user-1", first.RefName))

	// 이후 feature push 는 default branch 를 건드리면 안 된다.
	second := RefUpdateCommand{RefName: "refs/heads/feature", OldHash: tagEventZeroHash, NewHash: commitSHA}
	require.NoError(t, h.processRefUpdate(context.Background(), repoPath, second, "acme", "fresh", "user-1", second.RefName))

	head, err := gitutil.HeadRefName(context.Background(), repoPath)
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/main", head)
}

func TestCreatedBranchRefs_AndAdoptionTarget(t *testing.T) {
	// 한 번의 push 에 여러 브랜치가 실려도 ref 처리 순서와 무관하게 같은
	// 브랜치가 default 로 뽑혀야 한다 (SSH 경로와 동일한 선택 규칙).
	cmds := []RefUpdateCommand{
		{RefName: "refs/heads/zebra", OldHash: tagEventZeroHash, NewHash: "a"},
		{RefName: "refs/heads/main", OldHash: tagEventZeroHash, NewHash: "b"},
		{RefName: "refs/tags/v1", OldHash: tagEventZeroHash, NewHash: "c"},
		{RefName: "refs/heads/existing", OldHash: "old", NewHash: "d"},
	}

	created := createdBranchRefs(cmds)
	assert.ElementsMatch(t, []string{"refs/heads/zebra", "refs/heads/main"}, created,
		"태그와 기존 브랜치 갱신은 승격 후보가 아니다")
	assert.Equal(t, "refs/heads/main", gitutil.PreferAdoptionRef(created))
}
