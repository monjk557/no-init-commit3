package maintenance

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/config"
)

// invokeTyped 는 typed handler 를 직접 호출하고 결과/에러를 gin.Context 에
// 쓴다. typed.RegisterX wrapper 의 default 매핑 mirror.
func invokeTyped[Resp any](c *gin.Context, fn func(*gin.Context) (Resp, error)) {
	resp, err := fn(c)
	if err != nil {
		var he *typed.HTTPError
		if errors.As(err, &he) {
			body := gin.H{"success": false, "error": he.Message}
			if he.Code != "" {
				body["code"] = he.Code
			}
			if len(he.Params) > 0 {
				body["params"] = he.Params
			}
			c.JSON(he.Status, body)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// adapt 는 typed handler 를 gin.HandlerFunc 로 변환한다.
func adapt[Resp any](fn func(*gin.Context) (Resp, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		invokeTyped(c, fn)
	}
}

// buildBareRepoAt creates a bare repo with one commit at the exact gitDir path
// (parents created), so the handler's owner/repo path resolution finds it.
func buildBareRepoAt(t *testing.T, gitDir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(gitDir), 0o755))

	work := filepath.Join(t.TempDir(), "work")
	cmds := [][]string{
		{"git", "init", work},
		{"git", "-C", work, "config", "user.email", "test@test.com"},
		{"git", "-C", work, "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		require.NoError(t, err, "%v: %s", args, out)
	}
	require.NoError(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("# Test"), 0o644))
	for _, args := range [][]string{
		{"git", "-C", work, "add", "."},
		{"git", "-C", work, "commit", "-m", "initial commit"},
		{"git", "clone", "--bare", work, gitDir},
	} {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		require.NoError(t, err, "%v: %s", args, out)
	}
}

// newTestHandler builds a maintenance Handler rooted at a fresh temp repo root
// and returns the handler plus that root.
func newTestHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "debug", Format: "json", Output: "stdout", Development: true},
		Git:     config.GitConfig{RepoRoot: root},
	}
	h, err := NewHandler(cfg)
	require.NoError(t, err)
	return h, root
}

// newRouter mounts the maintenance routes under /repos/:owner/:repo using adapt,
// mirroring server.go's ownerRepoGroup registration.
func newRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	grp := r.Group("/repos/:owner/:repo")
	grp.POST("/maintenance", adapt(h.HandleMaintainRepo))
	return r
}

func TestHandleMaintainRepo_Success(t *testing.T) {
	h, root := newTestHandler(t)
	buildBareRepoAt(t, filepath.Join(root, "acme", "widgets.git"))

	req := httptest.NewRequest(http.MethodPost, "/repos/acme/widgets/maintenance", nil)
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp MaintainRepoResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.Success, "tasks: %+v", resp.Tasks)
	require.Equal(t, "acme", resp.Owner)
	require.Equal(t, "widgets", resp.Repo)
	require.NotEmpty(t, resp.Tasks)

	tasks := map[string]bool{}
	for _, tr := range resp.Tasks {
		tasks[tr.Task] = tr.Success
	}
	// The forced plan must at least repack (writes the bitmap) and build the
	// commit-graph — the two things a fresh ingest needs for a fast first clone.
	require.Contains(t, tasks, "geometric-repack")
	require.Contains(t, tasks, "commit-graph")
	// Prune is intentionally NOT part of the on-demand plan.
	require.NotContains(t, tasks, "prune")
}

func TestHandleMaintainRepo_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/repos/acme/missing/maintenance", nil)
	rec := httptest.NewRecorder()
	newRouter(h).ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestHandleMaintainRepo_InvalidPath(t *testing.T) {
	h, _ := newTestHandler(t)

	// A path-traversal owner must be rejected at validation (400) before any
	// filesystem access. Set params directly so the check isn't masked by gin's
	// URL normalization of "..".
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Params = gin.Params{{Key: "owner", Value: ".."}, {Key: "repo", Value: "etc"}}

	_, err := h.HandleMaintainRepo(c)

	var he *typed.HTTPError
	require.ErrorAs(t, err, &he)
	require.Equal(t, http.StatusBadRequest, he.Status)
}
