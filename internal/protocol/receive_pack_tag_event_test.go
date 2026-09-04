package protocol

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const tagEventZeroHash = "0000000000000000000000000000000000000000"

// gitX runs a git command in dir and fails the test on error.
func gitX(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=tester", "GIT_COMMITTER_EMAIL=tester@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s\n%s", strings.Join(args, " "), string(out))
	return strings.TrimSpace(string(out))
}

// tagEventFixture builds a bare repo at <root>/<owner>/<repo>.git that already
// contains a commit and an annotated tag *object* (but no tag ref). It returns
// the repo path plus the commit SHA and annotated tag object SHA.
type tagEventFixture struct {
	repoRoot     string
	owner        string
	repo         string
	repoPath     string
	commitSHA    string
	tagObjectSHA string
}

func newTagEventFixture(t *testing.T) tagEventFixture {
	t.Helper()
	root := t.TempDir()
	owner, repo := "acme", "templates"

	// Working repo to author objects.
	work := t.TempDir()
	gitX(t, work, "init", "-q")
	gitX(t, work, "config", "user.name", "tester")
	gitX(t, work, "config", "user.email", "tester@example.com")
	gitX(t, work, "commit", "--allow-empty", "-q", "-m", "initial commit")
	commitSHA := gitX(t, work, "rev-parse", "HEAD")
	gitX(t, work, "tag", "-a", "v1", "-m", "release v1")
	tagObjectSHA := gitX(t, work, "rev-parse", "v1")
	require.NotEqual(t, commitSHA, tagObjectSHA, "annotated tag object must differ from commit")

	// Bare repo at the path the handler expects. Clone brings objects + the tag
	// ref; delete the ref so the object remains but the tag can be (re)created.
	repoPath := filepath.Join(root, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))
	gitX(t, root, "clone", "--bare", "-q", work, repoPath)
	gitX(t, repoPath, "tag", "-d", "v1")

	return tagEventFixture{
		repoRoot:     root,
		owner:        owner,
		repo:         repo,
		repoPath:     repoPath,
		commitSHA:    commitSHA,
		tagObjectSHA: tagObjectSHA,
	}
}

// newEventsHandler builds a ReceivePackHandler wired to a miniredis-backed events
// client, returning the handler and a raw redis client for reading the stream.
func newEventsHandler(t *testing.T, repoRoot string) (*ReceivePackHandler, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	url := "redis://" + mr.Addr() + "/0"

	client, err := events.NewRedisStreamClient(url, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: repoRoot},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	h.eventsClient = client

	raw := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = raw.Close() })
	return h, raw
}

// awaitEvent polls the git:events stream until exactly one entry is present and
// returns its decoded data fields. Publishing happens in a background goroutine.
func awaitEvent(t *testing.T, raw *redis.Client) map[string]string {
	t.Helper()
	ctx := context.Background()
	var entries []redis.XMessage
	require.Eventually(t, func() bool {
		res, err := raw.XRange(ctx, "git:events", "-", "+").Result()
		if err != nil || len(res) == 0 {
			return false
		}
		entries = res
		return true
	}, 3*time.Second, 10*time.Millisecond, "expected an event on git:events stream")

	require.Len(t, entries, 1, "expected exactly one published event")
	dataField, ok := entries[0].Values["data"].(string)
	require.True(t, ok, "event payload missing 'data' field")

	var decoded map[string]string
	require.NoError(t, json.Unmarshal([]byte(dataField), &decoded))
	return decoded
}

func TestProcessRefUpdate_TagPush_PublishesTagPushEvent(t *testing.T) {
	fx := newTagEventFixture(t)
	h, raw := newEventsHandler(t, fx.repoRoot)

	cmd := RefUpdateCommand{
		RefName: "refs/tags/v1",
		OldHash: tagEventZeroHash,
		NewHash: fx.tagObjectSHA, // pushing the annotated tag object
	}
	require.NoError(t, h.processRefUpdate(context.Background(), fx.repoPath, cmd, fx.owner, fx.repo, "user-1", ""))

	ev := awaitEvent(t, raw)
	require.Equal(t, string(events.EventTypeTagPush), ev["event_type"])
	require.Equal(t, "refs/tags/v1", ev["ref"])
	// Annotated tag must be peeled to the commit SHA — the tag object SHA must
	// never be exposed downstream (Issue-2829 decision #4).
	require.Equal(t, fx.commitSHA, ev["new_rev"])
	require.NotEqual(t, fx.tagObjectSHA, ev["new_rev"])
}

func TestProcessRefUpdate_TagDelete_PublishesTagDeleteEvent(t *testing.T) {
	fx := newTagEventFixture(t)
	h, raw := newEventsHandler(t, fx.repoRoot)

	// Establish the tag ref first so the delete's CAS check passes.
	gitX(t, fx.repoPath, "update-ref", "refs/tags/v1", fx.tagObjectSHA)

	cmd := RefUpdateCommand{
		RefName: "refs/tags/v1",
		OldHash: fx.tagObjectSHA,
		NewHash: tagEventZeroHash, // delete
	}
	require.NoError(t, h.processRefUpdate(context.Background(), fx.repoPath, cmd, fx.owner, fx.repo, "user-1", ""))

	ev := awaitEvent(t, raw)
	require.Equal(t, string(events.EventTypeTagDelete), ev["event_type"])
	require.Equal(t, "refs/tags/v1", ev["ref"])
	// Delete events carry old_rev (peeled commit) and no new_rev.
	require.Equal(t, fx.commitSHA, ev["old_rev"])
	require.Empty(t, ev["new_rev"])
}

func TestProcessRefUpdate_BranchPush_StillPublishesPushEvent(t *testing.T) {
	// Regression: branch push behavior is unchanged by the tag work.
	fx := newTagEventFixture(t)
	h, raw := newEventsHandler(t, fx.repoRoot)

	cmd := RefUpdateCommand{
		RefName: "refs/heads/feature",
		OldHash: tagEventZeroHash,
		NewHash: fx.commitSHA,
	}
	require.NoError(t, h.processRefUpdate(context.Background(), fx.repoPath, cmd, fx.owner, fx.repo, "user-1", ""))

	ev := awaitEvent(t, raw)
	require.Equal(t, string(events.EventTypePush), ev["event_type"])
	require.Equal(t, "refs/heads/feature", ev["ref"])
	require.Equal(t, fx.commitSHA, ev["new_rev"])
}

func TestProcessRefUpdate_TagNoOp_SkipsEvent(t *testing.T) {
	// OldHash == NewHash must not publish anything.
	fx := newTagEventFixture(t)
	h, raw := newEventsHandler(t, fx.repoRoot)
	gitX(t, fx.repoPath, "update-ref", "refs/tags/v1", fx.tagObjectSHA)

	cmd := RefUpdateCommand{
		RefName: "refs/tags/v1",
		OldHash: fx.tagObjectSHA,
		NewHash: fx.tagObjectSHA,
	}
	require.NoError(t, h.processRefUpdate(context.Background(), fx.repoPath, cmd, fx.owner, fx.repo, "user-1", ""))

	// Give any erroneous goroutine a chance to publish, then assert emptiness.
	time.Sleep(200 * time.Millisecond)
	n, err := raw.XLen(context.Background(), "git:events").Result()
	require.NoError(t, err)
	require.Equal(t, int64(0), n, "no-op tag update must not publish an event")
}
