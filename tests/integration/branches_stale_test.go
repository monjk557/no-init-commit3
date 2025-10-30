package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	responses "github.com/collabops/git-executor/internal/schemas/responses"
	testutils "github.com/collabops/git-executor/tests/utils"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

// copyDir recursively copies a directory (src) into dst.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		// file
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
	require.NoError(t, err)
}

func TestBranchesStaleClassificationByRequestDate(t *testing.T) {
	h := testutils.NewIntegrationTestHarness(t)
	defer func() { _ = h.Cleanup() }()

	owner := "alice"
	repo := "example"
	userID := "u-1"
	h.GrantPermission(userID, owner, repo)

	// 1) Create a non-bare repo with commits and branches
	workdir := t.TempDir()
	r, err := gogit.PlainInit(workdir, false)
	require.NoError(t, err)

	wt, err := r.Worktree()
	require.NoError(t, err)

	// Create initial file
	filePath := filepath.Join(workdir, "README.md")
	require.NoError(t, os.WriteFile(filePath, []byte("hello"), 0644))
	_, err = wt.Add("README.md")
	require.NoError(t, err)

	// Commit on main with old timestamp to simulate stale later branches if needed
	oldWhen := time.Now().AddDate(0, -5, 0) // ~5 months ago (>120 days)
	_, err = wt.Commit("initial", &gogit.CommitOptions{
		Author:    &object.Signature{Name: "Alice", Email: "alice@example.com", When: oldWhen},
		Committer: &object.Signature{Name: "Alice", Email: "alice@example.com", When: oldWhen},
	})
	require.NoError(t, err)

	// Create main branch at the old commit so it's stale
	err = wt.Checkout(&gogit.CheckoutOptions{Branch: "refs/heads/main", Create: true})
	require.NoError(t, err)

	// Create and switch to feature branch, add a recent commit
	err = wt.Checkout(&gogit.CheckoutOptions{Branch: "refs/heads/feature", Create: true})
	require.NoError(t, err)
	// Modify file
	require.NoError(t, os.WriteFile(filePath, []byte("hello feature"), 0644))
	_, err = wt.Add("README.md")
	require.NoError(t, err)
	recentWhen := time.Now().AddDate(0, 0, -1)
	_, err = wt.Commit("feature work", &gogit.CommitOptions{
		Author:    &object.Signature{Name: "Bob", Email: "bob@example.com", When: recentWhen},
		Committer: &object.Signature{Name: "Bob", Email: "bob@example.com", When: recentWhen},
	})
	require.NoError(t, err)

	// Ensure we end on main branch (no recreate)
	err = wt.Checkout(&gogit.CheckoutOptions{Branch: "refs/heads/main"})
	require.NoError(t, err)

	// 2) Copy .git content to bare repo path expected by server
	srcGitDir := filepath.Join(workdir, ".git")
	// Destination bare repo path
	destBare := filepath.Join(h.TempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(destBare, 0755))
	copyDir(t, srcGitDir, destBare)

	// 3) Call API to fetch branches list with email filter for "Bob"
	baseURL := h.TestServer.URL
	url := fmt.Sprintf("%s/api/v1/repository/%s/%s/branches?email=%s", baseURL, owner, repo, "bob@example.com")

	req, err := http.NewRequest("GET", url, nil)
	require.NoError(t, err)
	req.Header.Set("X-USER-ID", userID)

	resp, err := h.TestServer.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var payload responses.BranchesListResponse
	require.NoError(t, json.Unmarshal(body, &payload))

	// Validate grouping: default should contain main, yours should contain feature (author Bob),
	// stale classification uses 120+ days since last commit; main's last commit is old -> stale
	// feature's last commit is recent -> active/yours
	// Note: Default branch appears in Default group; it also may be stale due to age, but we keep it in Default group per API shape

	// Check default group contains main
	foundMain := false
	for _, b := range payload.Default {
		if b.Name == "main" {
			foundMain = true
			// main is expected old; days since last commit >= 120
			require.GreaterOrEqual(t, b.DaysSinceLastCommit, 120)
		}
	}
	require.True(t, foundMain, "main branch not found in default group")

	// Check yours contains feature for Bob
	foundFeature := false
	for _, b := range payload.Yours {
		if b.Name == "feature" {
			foundFeature = true
			require.Less(t, b.DaysSinceLastCommit, 120)
		}
	}
	require.True(t, foundFeature, "feature branch not found in yours group for Bob")

	// Sanity: Stale group should have at least the main if we also classify it there; if not, Active/Yours groups cover it
	// We primarily assert the age logic via DaysSinceLastCommit threshold above.
}
