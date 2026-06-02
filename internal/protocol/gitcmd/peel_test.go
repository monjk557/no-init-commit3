package gitcmd

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// gitInRepo runs a git command in repoPath and fails the test on error.
func gitInRepo(t *testing.T, repoPath string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	// Deterministic identity so commits/tags are reproducible.
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=tester", "GIT_COMMITTER_EMAIL=tester@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// setupPeelRepo creates a repo with one commit and returns its path + commit SHA.
func setupPeelRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	gitInRepo(t, repo, "init", "-q")
	gitInRepo(t, repo, "config", "user.name", "tester")
	gitInRepo(t, repo, "config", "user.email", "tester@example.com")
	gitInRepo(t, repo, "commit", "--allow-empty", "-q", "-m", "initial")
	commit := gitInRepo(t, repo, "rev-parse", "HEAD")
	return repo, commit
}

func TestPeelToCommit_Commit(t *testing.T) {
	repo, commit := setupPeelRepo(t)

	got, err := PeelToCommit(context.Background(), repo, commit)
	if err != nil {
		t.Fatalf("PeelToCommit(commit) error: %v", err)
	}
	if got != commit {
		t.Fatalf("PeelToCommit(commit) = %s, want %s (idempotent on commits)", got, commit)
	}
}

func TestPeelToCommit_LightweightTag(t *testing.T) {
	repo, commit := setupPeelRepo(t)
	gitInRepo(t, repo, "tag", "v1-light")

	got, err := PeelToCommit(context.Background(), repo, "v1-light")
	if err != nil {
		t.Fatalf("PeelToCommit(lightweight) error: %v", err)
	}
	if got != commit {
		t.Fatalf("PeelToCommit(lightweight) = %s, want commit %s", got, commit)
	}
}

func TestPeelToCommit_AnnotatedTag(t *testing.T) {
	repo, commit := setupPeelRepo(t)
	gitInRepo(t, repo, "tag", "-a", "v1-annot", "-m", "release v1")

	// The annotated tag's own object SHA must NOT be what PeelToCommit returns.
	tagObjectSHA := gitInRepo(t, repo, "rev-parse", "v1-annot")
	if tagObjectSHA == commit {
		t.Fatalf("test setup invalid: annotated tag object SHA equals commit SHA")
	}

	got, err := PeelToCommit(context.Background(), repo, "v1-annot")
	if err != nil {
		t.Fatalf("PeelToCommit(annotated) error: %v", err)
	}
	if got != commit {
		t.Fatalf("PeelToCommit(annotated) = %s, want peeled commit %s", got, commit)
	}
	if got == tagObjectSHA {
		t.Fatalf("PeelToCommit(annotated) leaked tag object SHA %s", tagObjectSHA)
	}

	// Peeling by the annotated tag object SHA directly must also resolve to commit.
	gotByOID, err := PeelToCommit(context.Background(), repo, tagObjectSHA)
	if err != nil {
		t.Fatalf("PeelToCommit(tag object SHA) error: %v", err)
	}
	if gotByOID != commit {
		t.Fatalf("PeelToCommit(tag object SHA) = %s, want commit %s", gotByOID, commit)
	}
}

func TestPeelToCommit_MissingObject(t *testing.T) {
	repo, _ := setupPeelRepo(t)

	_, err := PeelToCommit(context.Background(), repo, strings.Repeat("0", 40))
	if err == nil {
		t.Fatal("PeelToCommit(missing) expected error, got nil")
	}
}
