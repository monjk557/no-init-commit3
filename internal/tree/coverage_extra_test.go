package tree

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coverage_extra_test.go — routes.go 의 pure error mapper, NewTreeHandler 의
// 에러 경로, service.go 의 getTreeStructureRecursive 를 커버한다.

func TestTreeSearchErrorMapper(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "typed HTTPError preserved",
			err:        typed.NewHTTPError(http.StatusBadRequest, "bad query"),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "bad query",
		},
		{
			name:       "resolve reference maps to 404",
			err:        errors.New("failed to resolve reference: nope"),
			wantStatus: http.StatusNotFound,
			wantMsg:    "failed to resolve reference: nope",
		},
		{
			name:       "unknown error maps to 500",
			err:        errors.New("boom"),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "boom",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status, body := treeSearchErrorMapper(tc.err)
			assert.Equal(t, tc.wantStatus, status)
			m, ok := body.(gin.H)
			require.True(t, ok)
			assert.Equal(t, false, m["success"])
			assert.Equal(t, tc.wantMsg, m["error"])
		})
	}
}

func TestNewTreeHandler_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("nil config errors", func(t *testing.T) {
		t.Parallel()
		h, err := NewTreeHandler(nil)
		assert.Error(t, err)
		assert.Nil(t, h)
	})

	t.Run("empty repo root errors (path config)", func(t *testing.T) {
		t.Parallel()
		h, err := NewTreeHandler(&config.Config{})
		assert.Error(t, err)
		assert.Nil(t, h)
	})

	t.Run("valid config succeeds", func(t *testing.T) {
		t.Parallel()
		h, err := NewTreeHandler(&config.Config{
			Logging: config.LoggingConfig{Level: "error", Format: "json"},
			Git:     config.GitConfig{RepoRoot: t.TempDir()},
		})
		require.NoError(t, err)
		assert.NotNil(t, h)
	})
}

// buildBareRepoWithSubdir clones a work repo (containing src/main.go) into a bare
// repo under {root}/{owner}/{repo}.git and returns the bare repo path.
func buildBareRepoWithSubdir(t *testing.T, root, owner, repo string) string {
	t.Helper()
	work := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "T")

	require.NoError(t, os.MkdirAll(filepath.Join(work, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, "src", "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("# hi\n"), 0o644))
	run("add", "-A")
	run("commit", "-q", "-m", "init")

	bare := filepath.Join(root, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(bare), 0o755))
	cmd := exec.Command("git", "clone", "--bare", "-q", work, bare)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}
	return bare
}

func TestGetTreeStructureRecursive(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	h, err := NewTreeHandler(&config.Config{
		Logging: config.LoggingConfig{Level: "error", Format: "json"},
		Git:     config.GitConfig{RepoRoot: root},
	})
	require.NoError(t, err)

	repoPath := buildBareRepoWithSubdir(t, root, "acme", "proj")

	t.Run("root path returns tree with root key", func(t *testing.T) {
		res, err := h.getTreeStructureRecursive(repoPath, "HEAD", "")
		require.NoError(t, err)
		require.Contains(t, res, "/")
		// root should include the src directory and README.md
		names := map[string]bool{}
		for _, e := range res["/"] {
			names[e.Name] = true
		}
		assert.True(t, names["src"], "root should list src dir")
		assert.True(t, names["README.md"], "root should list README.md")
	})

	t.Run("nested path expands intermediate levels", func(t *testing.T) {
		res, err := h.getTreeStructureRecursive(repoPath, "HEAD", "src")
		require.NoError(t, err)
		assert.Contains(t, res, "/")
		require.Contains(t, res, "src")
		found := false
		for _, e := range res["src"] {
			if e.Name == "main.go" {
				found = true
			}
		}
		assert.True(t, found, "src should list main.go")
	})

	t.Run("nonexistent target path errors", func(t *testing.T) {
		_, err := h.getTreeStructureRecursive(repoPath, "HEAD", "no/such/dir")
		assert.Error(t, err)
	})
}
