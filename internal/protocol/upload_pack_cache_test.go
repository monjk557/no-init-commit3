package protocol

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/packcache"
)

// cloneRequestBody builds a minimal protocol-v0 upload-pack (clone) request:
// (pktLine helper is shared with capabilities_extra_test.go)
// a single want with capabilities, a flush, then done.
func cloneRequestBody(sha string) []byte {
	caps := "multi_ack_detailed side-band-64k thin-pack ofs-delta agent=git/2.39.5"
	return []byte(pktLine("want "+sha+" "+caps+"\n") + "0000" + pktLine("done\n"))
}

func newCachingHandler(t *testing.T, cacheDir string) *UploadPackHandler {
	t.Helper()
	t.Setenv(packcache.EnvEnabled, "true")
	t.Setenv(packcache.EnvDir, cacheDir)
	h, err := NewUploadPackHandler(&config.Config{
		Git:    config.GitConfig{RepoRoot: t.TempDir()},
		Server: config.ServerConfig{GitProtocolIdleTimeout: 0},
	})
	require.NoError(t, err)
	require.NotNil(t, h.cache)
	require.True(t, h.cache.Enabled())
	return h
}

// TestServeFromCache_HitReusesSingleGeneration verifies that a second identical
// upload-pack request is served from cache (identical bytes, no regeneration):
// exactly one cached pack file exists after two serves.
func TestServeFromCache_HitReusesSingleGeneration(t *testing.T) {
	bare, sha := buildBareRepoForPack(t)
	cacheDir := t.TempDir()
	h := newCachingHandler(t, cacheDir)
	body := cloneRequestBody(sha)

	serve := func() []byte {
		req := SetupTestRequestWithGinParams("POST", "/o/r/git-upload-pack",
			map[string]string{"owner": "o", "repo": "r"})
		rec := httptest.NewRecorder()
		sd := NewStreamDeadliner(rec, 0)
		ok := h.serveFromCache(req, "o", "r", bare, body, resolveGitProtocol(req, body), rec, sd)
		require.True(t, ok, "serveFromCache should fully handle the request")
		return append([]byte(nil), rec.Body.Bytes()...)
	}

	first := serve()
	require.NotEmpty(t, first, "generated pack response should be non-empty")
	second := serve()
	require.Equal(t, first, second, "cache hit must return byte-identical response")

	packs, err := filepath.Glob(filepath.Join(cacheDir, "*.pack"))
	require.NoError(t, err)
	require.Len(t, packs, 1, "identical requests must share one cached generation")
}

// TestServeFromCache_GenerationErrorFallsBack verifies the fallback-safety
// invariant: when generation fails, serveFromCache returns false WITHOUT writing
// anything to the response, so the caller can safely take the direct path.
func TestServeFromCache_GenerationErrorFallsBack(t *testing.T) {
	cacheDir := t.TempDir()
	h := newCachingHandler(t, cacheDir)

	req := SetupTestRequestWithGinParams("POST", "/o/r/git-upload-pack",
		map[string]string{"owner": "o", "repo": "r"})
	rec := httptest.NewRecorder()
	sd := NewStreamDeadliner(rec, 0)

	// Non-existent repo + garbage body → git upload-pack fails → generation error.
	badRepo := filepath.Join(t.TempDir(), "missing.git")
	ok := h.serveFromCache(req, "o", "r", badRepo, []byte("garbage"), "", rec, sd)

	require.False(t, ok, "generation failure must report not-served (fall back)")
	require.Zero(t, rec.Body.Len(), "no bytes may be written to the client on fallback")

	packs, _ := filepath.Glob(filepath.Join(cacheDir, "*.pack"))
	require.Empty(t, packs, "failed generation must not leave a cached entry")
}
