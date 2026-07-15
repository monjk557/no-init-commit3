package overview

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestNativeCountCommits_EmptyRepoErrorIncludesStderr verifies that a failing
// `git rev-list --count` (here: an empty/unborn-HEAD repo) surfaces git's
// stderr in the returned error, not just a bare "exit status 128". This is what
// lets callers distinguish a genuine failure (corrupt object, maintenance
// dead-zone) from a bad-revision on an empty repo (Issue-5952).
func TestNativeCountCommits_EmptyRepoErrorIncludesStderr(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare failed: %s: %v", out, err)
	}

	_, _, err := NativeCountCommits(context.Background(), dir, "HEAD")
	if err == nil {
		t.Fatal("expected error counting commits on an empty repo, got nil")
	}

	// The wrapped error must carry git's fatal reason from stderr, not only the
	// exit code. git prints "fatal: ..." to stderr for a bad/unborn revision.
	if !strings.Contains(err.Error(), "fatal") {
		t.Errorf("error should include git stderr (a 'fatal:' line), got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "rev-list --count") {
		t.Errorf("error should identify the failing command, got: %q", err.Error())
	}
}
