package state

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 회귀: "state typed handler + 라우트가 정상/에러 경로 모두 올바르게 동작해야 함"

// invokeTyped mirrors the typed wrapper + default *HTTPError mapping.
func invokeTyped[Resp any](c *gin.Context, fn func(*gin.Context) (Resp, error)) {
	resp, err := fn(c)
	if err != nil {
		var he *typed.HTTPError
		if errors.As(err, &he) {
			c.JSON(he.Status, gin.H{"success": false, "error": he.Message})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

func adapt[Resp any](fn func(*gin.Context) (Resp, error)) gin.HandlerFunc {
	return func(c *gin.Context) { invokeTyped(c, fn) }
}

// newHandlerWithRepo builds a StateHandler whose RepoRoot contains a bare repo
// at {root}/{owner}/{repo}.git. Returns handler + owner/repo identifiers.
func newHandlerWithRepo(t *testing.T) (*StateHandler, string, string) {
	t.Helper()
	root := t.TempDir()
	owner, repo := "octo", "demo"

	// Build the bare repo in a temp location, then move it to the expected path.
	src := newBareRepo(t)
	dstDir := filepath.Join(root, owner)
	require.NoError(t, os.MkdirAll(dstDir, 0o755))
	dst := filepath.Join(dstDir, repo+".git")
	require.NoError(t, os.Rename(src, dst))

	cfg := &config.Config{
		Git: config.GitConfig{RepoRoot: root},
		Logging: config.LoggingConfig{
			Level: "error", Format: "text", Output: "stdout", Development: true,
		},
	}
	h, err := NewStateHandler(cfg)
	require.NoError(t, err)
	return h, owner, repo
}

func TestNewStateHandler(t *testing.T) {
	cfg := &config.Config{
		Git: config.GitConfig{RepoRoot: t.TempDir()},
		Logging: config.LoggingConfig{
			Level: "error", Format: "text", Output: "stdout", Development: true,
		},
	}
	h, err := NewStateHandler(cfg)
	require.NoError(t, err)
	assert.NotNil(t, h)

	t.Run("empty repo root errors", func(t *testing.T) {
		bad := &config.Config{
			Git:     config.GitConfig{RepoRoot: ""},
			Logging: cfg.Logging,
		}
		_, err := NewStateHandler(bad)
		assert.Error(t, err)
	})
}

func TestHandleGetRepositoryState_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, owner, repo := newHandlerWithRepo(t)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Params = gin.Params{
		{Key: "owner", Value: owner},
		{Key: "repo", Value: repo},
	}

	resp, err := h.HandleGetRepositoryState(c)
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, repo, resp.RepositoryName)
	assert.Equal(t, owner, resp.Owner)
	assert.Equal(t, "main", resp.Head.Name)
	assert.GreaterOrEqual(t, resp.References.Count.Branches, 1)
	assert.GreaterOrEqual(t, resp.References.Count.Tags, 1)
}

func TestHandleGetRepositoryState_NotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _, _ := newHandlerWithRepo(t)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Params = gin.Params{
		{Key: "owner", Value: "missing"},
		{Key: "repo", Value: "nope"},
	}

	_, err := h.HandleGetRepositoryState(c)
	require.Error(t, err)
	var he *typed.HTTPError
	require.True(t, errors.As(err, &he))
	assert.Equal(t, http.StatusInternalServerError, he.Status)
}

func TestHandleGetRepositoryState_InvalidPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _, _ := newHandlerWithRepo(t)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Params = gin.Params{
		{Key: "owner", Value: "../etc"},
		{Key: "repo", Value: "passwd"},
	}

	_, err := h.HandleGetRepositoryState(c)
	require.Error(t, err)
	var he *typed.HTTPError
	require.True(t, errors.As(err, &he))
	assert.Equal(t, http.StatusBadRequest, he.Status)
}

func TestRegisterRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("spec-only (nil handler) does not panic", func(t *testing.T) {
		r := gin.New()
		assert.NotPanics(t, func() {
			RegisterRoutes(r, nil)
		})
	})

	t.Run("with handler serves real request", func(t *testing.T) {
		h, owner, repo := newHandlerWithRepo(t)

		r := gin.New()
		grp := r.Group("/repos/:owner/:repo")
		// Use the adapt helper so the typed handler result is written to the response.
		grp.GET("/state", adapt(h.HandleGetRepositoryState))

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/repos/"+owner+"/"+repo+"/state", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"success":true`)
		assert.Contains(t, w.Body.String(), `"repository_name":"`+repo+`"`)
	})
}
