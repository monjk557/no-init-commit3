package blob

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBlobRoutes_TypedRegistration_RegistersAsRaw 는 RegisterRoutes 가 nil
// handler 로 호출될 때 (spec-only 모드) /blob endpoint 를 RegisterRaw 형태로
// 등록하는지 — registry 에 LocationRawBytes + application/octet-stream 으로
// capture 되는지 검증한다.
//
// orchestrator auto-gen 클라이언트는 spec 의 ContentType 을 보고 raw bytes
// API 를 emit 한다. 이 contract 가 깨지면 dead method 회귀가 발생한다
// (Issue-2327 origin).
func TestBlobRoutes_TypedRegistration_RegistersAsRaw(t *testing.T) {
	reg := typed.NewRegistry()
	restore := typed.SetDefaultRegistry(reg)
	defer restore()

	gin.SetMode(gin.TestMode)
	e := gin.New()
	g := e.Group("/repos/:owner/:repo")

	RegisterRoutes(g, nil)

	snap := reg.Snapshot()
	require.Len(t, snap, 1, "expected exactly 1 endpoint registered")

	ep := snap[0]
	assert.Equal(t, http.MethodGet, ep.Method)
	assert.Equal(t, "/repos/{owner}/{repo}/blob", ep.Path)
	assert.Equal(t, "Blob", ep.Tag)
	assert.Equal(t, "HandleBlobAPI", ep.Summary)
	assert.Equal(t, typed.LocationRawBytes, ep.ReqLocation)
	assert.Equal(t, "application/octet-stream", ep.ContentType)

	require.Len(t, ep.Responses, 1)
	assert.Equal(t, http.StatusOK, ep.Responses[0].Status)
	assert.Equal(t, "application/octet-stream", ep.Responses[0].ContentType)
}

// TestBlobAPI_LFSPointer_StreamsAsBinary 는 LFS pointer 콘텐츠를 가진 blob 이
// /blob 으로 요청됐을 때 binary streaming 으로 응답되어 raw pointer 텍스트가
// 그대로 body 에 흘러나오는지 검증한다 — Issue-2327 에서 polymorphic LFS JSON
// 분기를 제거했음을 보장. LFS 메타데이터는 /overview/blob 에서 읽어야 한다.
func TestBlobAPI_LFSPointer_StreamsAsBinary(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "lfsowner", "lfsrepo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	lfsContent := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n" +
		"size 1024\n"

	commitHash := createTestCommit(t, gitRepo, map[string]string{
		"asset.bin": lfsContent,
	})
	require.NoError(t, gitRepo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)))

	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	handler, err := NewBlobHandler(cfg)
	require.NoError(t, err)

	c, w := createGinContext("GET", "/repos/lfsowner/lfsrepo/blob?path=asset.bin&ref=main")
	c.Params = gin.Params{
		{Key: "owner", Value: "lfsowner"},
		{Key: "repo", Value: "lfsrepo"},
	}

	handler.HandleBlobAPI(c)

	require.Equal(t, http.StatusOK, w.Code)
	// JSON LFS 응답 분기는 제거됐다 — `is_lfs_pointer` 필드가 응답 body 에
	// 노출되면 안 된다.
	assert.NotContains(t, w.Body.String(), "is_lfs_pointer")
	// 대신 raw pointer 텍스트가 그대로 streaming 된다.
	assert.Contains(t, w.Body.String(), "git-lfs.github.com")
	assert.Contains(t, w.Body.String(), "oid sha256:")
	// JSON 응답이 아니므로 application/json content-type 이 set 되지 않는다.
	assert.NotEqual(t, "application/json", w.Header().Get("Content-Type"))
}

// TestBlobRoutes_RuntimeRegistration_StreamsBinary 는 RegisterRoutes 가 실제
// gin engine 에 binary handler 를 wire 하는지 — HTTP 요청 경로가 실제 blob 을
// 반환하는지 e2e 검증.
func TestBlobRoutes_RuntimeRegistration_StreamsBinary(t *testing.T) {
	reg := typed.NewRegistry()
	restore := typed.SetDefaultRegistry(reg)
	defer restore()

	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "owner", "repo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)
	commitHash := createTestCommit(t, gitRepo, map[string]string{"README.md": "# Hello"})
	require.NoError(t, gitRepo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)))

	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	handler, err := NewBlobHandler(cfg)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	e := gin.New()
	g := e.Group("/repos/:owner/:repo")
	RegisterRoutes(g, handler)

	req := httptest.NewRequest(http.MethodGet,
		"/repos/owner/repo/blob?path=README.md&ref=main", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	assert.Contains(t, w.Body.String(), "# Hello")
}
