package branches

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildRepoWithTwoBranches makes a bare repo with main and feature branches,
// each tipped by a commit with a DISTINCT committer email, so we can assert the
// light scan + enrich reproduces the full scan's per-branch emails.
func buildRepoWithTwoBranches(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	run := func(extraEnv []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), extraEnv...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(nil, "init", "-q", "-b", "main")
	run(nil, "config", "user.name", "T")
	run([]string{"GIT_COMMITTER_NAME=T", "GIT_AUTHOR_NAME=T", "GIT_COMMITTER_EMAIL=main@x.io", "GIT_AUTHOR_EMAIL=main@x.io"},
		"commit", "--allow-empty", "-q", "-m", "c1")
	run(nil, "checkout", "-q", "-b", "feature")
	run([]string{"GIT_COMMITTER_NAME=T", "GIT_AUTHOR_NAME=T", "GIT_COMMITTER_EMAIL=feat@y.io", "GIT_AUTHOR_EMAIL=feat@y.io"},
		"commit", "--allow-empty", "-q", "-m", "c2")

	bare := filepath.Join(t.TempDir(), "r.git")
	if out, err := exec.Command("git", "clone", "--bare", "-q", work, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}
	return bare
}

func TestNativeGetBranchesLight_OmitsEmail_EnrichMatchesFull(t *testing.T) {
	bare := buildRepoWithTwoBranches(t)
	ctx := context.Background()

	full, err := NativeGetBranches(ctx, bare, BranchSortNewest)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(full), 2)
	fullByRef := make(map[string]BranchInfo, len(full))
	for _, b := range full {
		require.NotEmpty(t, b.CommitterEmail, "sanity: full scan has emails")
		fullByRef[b.RefPath] = b
	}

	light, err := NativeGetBranchesLight(ctx, bare, BranchSortNewest, true)
	require.NoError(t, err)
	require.Equal(t, len(full), len(light))

	// Light scan: same identity, but NO email yet.
	for _, l := range light {
		f, ok := fullByRef[l.RefPath]
		require.True(t, ok, "light ref %s missing from full", l.RefPath)
		require.Empty(t, l.CommitterEmail, "light scan must omit committer email")
		require.Equal(t, f.CommitSHA, l.CommitSHA)
		require.Equal(t, f.Name, l.Name)
	}

	// Enrich the page → emails must match the full scan exactly (incl. distinct
	// per-branch emails, verifying we don't cross-wire refs).
	require.NoError(t, NativeEnrichCommitterEmails(ctx, bare, light))
	for _, l := range light {
		require.NotEmpty(t, l.CommitterEmail, "enrich must fill email for %s", l.RefPath)
		require.Equal(t, fullByRef[l.RefPath].CommitterEmail, l.CommitterEmail,
			"enriched email must match full scan for %s", l.RefPath)
	}
}

func TestNativeEnrichCommitterEmails_EmptyPageIsNoop(t *testing.T) {
	require.NoError(t, NativeEnrichCommitterEmails(context.Background(), t.TempDir(), nil))
}
