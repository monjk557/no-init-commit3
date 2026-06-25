package context

import (
	stdctx "context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
)

func testLogger(t *testing.T) *logging.Logger {
	t.Helper()
	l, err := logging.NewLogger(config.LoggingConfig{
		Level: "error", Format: "text", Output: "stdout", Development: true,
	})
	require.NoError(t, err)
	return l
}

func TestService_GetContext(t *testing.T) {
	repoPath, oldRev, newRev := newRepoWithWorkflow(t)
	svc := NewService(testLogger(t))

	t.Run("returns diff files and workflows", func(t *testing.T) {
		resp, err := svc.GetContext(stdctx.Background(), repoPath, oldRev, newRev)
		require.NoError(t, err)
		assert.True(t, resp.Success)
		assert.Contains(t, resp.DiffFiles, "readme.txt")
		assert.Equal(t, "name: ci\n", resp.Workflows["ci.yaml"])
	})

	t.Run("null oldRev is treated as first push", func(t *testing.T) {
		resp, err := svc.GetContext(stdctx.Background(), repoPath, nullSHA, newRev)
		require.NoError(t, err)
		assert.True(t, resp.Success)
		assert.NotEmpty(t, resp.DiffFiles)
	})

	t.Run("invalid old_rev errors", func(t *testing.T) {
		bad := "deadbeef" + strings.Repeat("0", 32)
		_, err := svc.GetContext(stdctx.Background(), repoPath, bad, newRev)
		assert.Error(t, err)
	})

	t.Run("invalid new_rev errors", func(t *testing.T) {
		bad := "deadbeef" + strings.Repeat("0", 32)
		_, err := svc.GetContext(stdctx.Background(), repoPath, oldRev, bad)
		assert.Error(t, err)
	})
}

// setupHandlerRepo places a bare repo at {root}/{owner}/{repo}.git and returns
// the handler + revs.
func setupHandlerRepo(t *testing.T) (*ContextHandler, string, string) {
	t.Helper()
	repoPath, oldRev, newRev := newRepoWithWorkflow(t)

	root := t.TempDir()
	ownerDir := filepath.Join(root, "acme")
	require.NoError(t, os.MkdirAll(ownerDir, 0o755))
	dest := filepath.Join(ownerDir, "proj.git")
	require.NoError(t, os.Rename(repoPath, dest))

	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: root},
		Logging: config.LoggingConfig{Level: "error", Format: "text", Output: "stdout", Development: true},
	}
	h, err := NewContextHandler(cfg)
	require.NoError(t, err)
	return h, oldRev, newRev
}

func TestNewContextHandler_NilConfig(t *testing.T) {
	_, err := NewContextHandler(nil)
	assert.Error(t, err)
}

func TestHandleContext(t *testing.T) {
	h, oldRev, newRev := setupHandlerRepo(t)

	t.Run("success", func(t *testing.T) {
		resp, err := h.HandleContext(newCtx(t, "acme", "proj", `{"old_rev":"`+oldRev+`","new_rev":"`+newRev+`"}`))
		require.NoError(t, err)
		assert.True(t, resp.Success)
		assert.Contains(t, resp.DiffFiles, "readme.txt")
	})

	t.Run("missing owner/repo", func(t *testing.T) {
		_, err := h.HandleContext(newCtx(t, "", "", `{"old_rev":"`+oldRev+`","new_rev":"`+newRev+`"}`))
		assert.Error(t, err)
	})

	t.Run("invalid json body", func(t *testing.T) {
		_, err := h.HandleContext(newCtx(t, "acme", "proj", `not-json`))
		assert.Error(t, err)
	})

	t.Run("invalid old_rev format", func(t *testing.T) {
		_, err := h.HandleContext(newCtx(t, "acme", "proj", `{"old_rev":"xyz","new_rev":"`+newRev+`"}`))
		assert.Error(t, err)
	})

	t.Run("invalid new_rev format", func(t *testing.T) {
		_, err := h.HandleContext(newCtx(t, "acme", "proj", `{"old_rev":"`+oldRev+`","new_rev":"xyz"}`))
		assert.Error(t, err)
	})

	t.Run("repository not found", func(t *testing.T) {
		_, err := h.HandleContext(newCtx(t, "acme", "missing", `{"old_rev":"`+oldRev+`","new_rev":"`+newRev+`"}`))
		assert.Error(t, err)
	})
}

func TestRegisterRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("spec-only mode does not panic", func(t *testing.T) {
		r := gin.New()
		assert.NotPanics(t, func() { RegisterRoutes(r, nil) })
	})

	t.Run("with handler serves endpoint", func(t *testing.T) {
		h, oldRev, newRev := setupHandlerRepo(t)
		r := gin.New()
		grp := r.Group("/repos/:owner/:repo")
		RegisterRoutes(grp, h)

		w := httptest.NewRecorder()
		body := `{"old_rev":"` + oldRev + `","new_rev":"` + newRev + `"}`
		req := httptest.NewRequest(http.MethodPost, "/repos/acme/proj/context", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "readme.txt")
	})
}

func newCtx(t *testing.T, owner, repo, body string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "owner", Value: owner}, {Key: "repo", Value: repo}}
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}
