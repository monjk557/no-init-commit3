package gitcmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupChainRepo 는 N 개의 선형 커밋 체인을 가진 repo 를 만들고
// 오래된 것부터 최신순으로 정렬된 commit SHA 슬라이스를 반환한다.
func setupChainRepo(t *testing.T, n int) (repo string, commits []string) {
	t.Helper()
	repo = t.TempDir()
	gitInRepo(t, repo, "init", "-q", "-b", "main")
	gitInRepo(t, repo, "config", "user.name", "tester")
	gitInRepo(t, repo, "config", "user.email", "tester@example.com")
	for i := 0; i < n; i++ {
		gitInRepo(t, repo, "commit", "--allow-empty", "-q", "-m", "c")
		commits = append(commits, gitInRepo(t, repo, "rev-parse", "HEAD"))
	}
	return repo, commits
}

func TestGetShallowBoundary_DepthZeroOrNoWants(t *testing.T) {
	repo, commits := setupChainRepo(t, 2)
	head := commits[len(commits)-1]

	// depth <= 0 → nil.
	got, err := GetShallowBoundary(context.Background(), repo, []string{head}, nil, 0)
	require.NoError(t, err)
	assert.Nil(t, got)

	// wants 없음 → nil.
	got, err = GetShallowBoundary(context.Background(), repo, nil, nil, 5)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestGetShallowBoundary_DepthOne(t *testing.T) {
	repo, commits := setupChainRepo(t, 3)
	head := commits[len(commits)-1]

	// depth=1 → want 커밋 자체가 shallow boundary (중복 제거 포함).
	got, err := GetShallowBoundary(context.Background(), repo, []string{head, head}, nil, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, head, got[0])
}

func TestGetShallowBoundary_DepthN(t *testing.T) {
	// 5 커밋 체인. depth=2 → 깊이 2 에 있는 커밋(= 끝에서 두번째)이 boundary.
	repo, commits := setupChainRepo(t, 5)
	head := commits[len(commits)-1]
	secondFromTop := commits[len(commits)-2]

	got, err := GetShallowBoundary(context.Background(), repo, []string{head}, nil, 2)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, secondFromTop, got[0],
		"depth=2 boundary should be the commit at depth 2 whose parent is cut off")
}

func TestGetShallowBoundary_BadRepo(t *testing.T) {
	notARepo := t.TempDir()
	_, err := GetShallowBoundary(context.Background(), notARepo,
		[]string{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}, nil, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rev-list")
}

func TestCountObjects_FullHistory(t *testing.T) {
	// 1 empty 커밋 = commit + tree (빈 트리) = 2 objects.
	repo, commits := setupChainRepo(t, 1)
	head := commits[0]

	n, err := CountObjects(context.Background(), repo, []string{head}, nil, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, 1, "should count at least the commit object")
}

func TestCountObjects_WithHave(t *testing.T) {
	repo, commits := setupChainRepo(t, 3)
	head := commits[len(commits)-1]
	parent := commits[len(commits)-2]

	full, err := CountObjects(context.Background(), repo, []string{head}, nil, 0)
	require.NoError(t, err)

	// parent 를 have 로 주면 보낼 객체 수가 줄어든다.
	incremental, err := CountObjects(context.Background(), repo, []string{head}, []string{parent}, 0)
	require.NoError(t, err)
	assert.Less(t, incremental, full,
		"have 가 주어지면 전체보다 적은 객체를 세야 한다")
}

func TestCountObjects_WithDepth(t *testing.T) {
	repo, commits := setupChainRepo(t, 5)
	head := commits[len(commits)-1]

	n, err := CountObjects(context.Background(), repo, []string{head}, nil, 1)
	require.NoError(t, err)
	assert.Greater(t, n, 0)
}

func TestCountObjects_BadRepo(t *testing.T) {
	notARepo := t.TempDir()
	_, err := CountObjects(context.Background(), notARepo,
		[]string{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}, nil, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rev-list")
}
