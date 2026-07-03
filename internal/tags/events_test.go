package tags

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/repositories"
)

// captureStream is a fake RedisStreamClient that records emitted events.
type captureStream struct {
	mu  sync.Mutex
	got []*events.CommitEvent
}

func (c *captureStream) AddCommitEventToStream(_ context.Context, e *events.CommitEvent) error {
	c.mu.Lock()
	c.got = append(c.got, e)
	c.mu.Unlock()
	return nil
}
func (c *captureStream) Close() error { return nil }

// waitN polls until at least n events are captured (emit is async), or times out.
func (c *captureStream) waitN(t *testing.T, n int) []*events.CommitEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		l := len(c.got)
		c.mu.Unlock()
		if l >= n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*events.CommitEvent(nil), c.got...)
}

// buildBareTagRepo makes {root}/{owner}/{repo}.git with one commit + a
// lightweight tag v1 pointing at it. Returns root, the git-dir, and the commit.
func buildBareTagRepo(t *testing.T, owner, repo string) (root, gitDir, commitSHA string) {
	t.Helper()
	root = t.TempDir()
	gitDir = filepath.Join(root, owner, repo+".git")
	if out, err := exec.Command("git", "init", "--bare", gitDir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"--git-dir", gitDir}, args...)...)
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@t.io",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@t.io",
			"GIT_AUTHOR_DATE=2024-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2024-01-01T00:00:00Z",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	tree := run("hash-object", "-t", "tree", "-w", "--stdin")
	commitSHA = run("commit-tree", tree, "-m", "initial commit")
	run("update-ref", "refs/heads/main", commitSHA)
	run("update-ref", "refs/tags/v1", commitSHA)
	return root, gitDir, commitSHA
}

func newHandlerWithFake(t *testing.T, root string) (*TagsHandler, *captureStream) {
	t.Helper()
	pc, err := repositories.NewPathConfig(root)
	require.NoError(t, err)
	fake := &captureStream{}
	return &TagsHandler{pathConfig: pc, eventsClient: fake}, fake
}

func TestPublishTagPush_EmitsTagPushEvent(t *testing.T) {
	owner, repo := "acme", "proj"
	root, gitDir, commitSHA := buildBareTagRepo(t, owner, repo)
	h, fake := newHandlerWithFake(t, root)

	h.publishTagPush(context.Background(), gitDir, owner, repo, "v1", commitSHA, "user-1")

	got := fake.waitN(t, 1)
	require.Len(t, got, 1)
	e := got[0]
	assert.Equal(t, events.EventTypeTagPush, e.EventType)
	assert.Equal(t, "refs/tags/v1", e.RefName)
	assert.Equal(t, commitSHA, e.CommitHash, "tag hash must be peeled to the commit SHA")
	assert.Empty(t, e.OldRev, "new tag has no old rev")
	assert.Equal(t, "git", e.Pusher)
	assert.Equal(t, "user-1", e.UserID)
	assert.NotEmpty(t, e.CommitMessage, "should be enriched with commit info")
}

func TestPublishTagDelete_EmitsTagDeleteEvent(t *testing.T) {
	owner, repo := "acme", "proj"
	root, _, commitSHA := buildBareTagRepo(t, owner, repo)
	h, fake := newHandlerWithFake(t, root)

	h.publishTagDelete(owner, repo, "v1", commitSHA, "user-1")

	got := fake.waitN(t, 1)
	require.Len(t, got, 1)
	e := got[0]
	assert.Equal(t, events.EventTypeTagDelete, e.EventType)
	assert.Equal(t, "refs/tags/v1", e.RefName)
	assert.Equal(t, commitSHA, e.CommitHash)
	assert.Equal(t, commitSHA, e.OldRev)
	assert.Equal(t, "git", e.Pusher)
}

func TestPublishTag_NilClientAndEmptyHash_Noop(t *testing.T) {
	// nil events client → both are safe no-ops (no panic), matching the push path.
	h := &TagsHandler{}
	h.publishTagPush(context.Background(), "/nope", "o", "r", "v1", "deadbeef", "u")
	h.publishTagDelete("o", "r", "v1", "deadbeef", "u")

	// Empty old hash → delete is skipped even with a client.
	fake := &captureStream{}
	h2 := &TagsHandler{eventsClient: fake}
	h2.publishTagDelete("o", "r", "v1", "", "u")
	assert.Empty(t, fake.waitN(t, 0))
}
