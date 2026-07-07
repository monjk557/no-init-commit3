package protocol

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
)

// headerOrderRecorder is an http.ResponseWriter + http.Flusher that records the
// order in which the response header, the flush, and the first body write
// happen. It lets a test assert the header is flushed before the (potentially
// slow) packfile body begins.
type headerOrderRecorder struct {
	header    http.Header
	status    int
	events    []string // first "header", "flush", "body" in call order
	sawHeader bool
	sawFlush  bool
	sawBody   bool
	body      bytes.Buffer
}

func (r *headerOrderRecorder) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}

func (r *headerOrderRecorder) WriteHeader(code int) {
	if !r.sawHeader {
		r.sawHeader = true
		r.status = code
		r.events = append(r.events, "header")
	}
}

func (r *headerOrderRecorder) Write(p []byte) (int, error) {
	if !r.sawHeader {
		r.WriteHeader(http.StatusOK)
	}
	if !r.sawBody && len(p) > 0 {
		r.sawBody = true
		r.events = append(r.events, "body")
	}
	return r.body.Write(p)
}

func (r *headerOrderRecorder) Flush() {
	if !r.sawFlush {
		r.sawFlush = true
		r.events = append(r.events, "flush")
	}
}

func indexOfEvent(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}
	return -1
}

// buildBareRepoAt creates a bare repo with one commit at repoPath and returns
// its HEAD sha (a valid "want" for a clone request).
func buildBareRepoAt(t *testing.T, repoPath string) string {
	t.Helper()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	work := t.TempDir()
	run(work, "init", "-q", "-b", "main")
	run(work, "config", "user.email", "t@example.com")
	run(work, "config", "user.name", "T")
	run(work, "commit", "--allow-empty", "-q", "-m", "c1")
	sha := run(work, "rev-parse", "HEAD")
	run("", "clone", "--bare", "-q", work, repoPath)
	return sha
}

// TestHandleUploadPack_FlushesHeaderBeforePackBody verifies the response header
// is written and flushed before the packfile body streams. git upload-pack
// produces no bytes until it finishes counting + delta-compressing, so without
// an early header flush a slow first byte (stale/missing bitmap) trips the
// fronting proxy's ResponseHeaderTimeout and turns a healthy clone into an
// HTTP 502. See Issue-5273.
func TestHandleUploadPack_FlushesHeaderBeforePackBody(t *testing.T) {
	tempDir := t.TempDir()
	owner, repoParam := "test", "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))
	sha := buildBareRepoAt(t, repoPath)

	h, err := NewUploadPackHandler(&config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Server:  config.ServerConfig{GitProtocolIdleTimeout: 0},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	})
	require.NoError(t, err)

	req := SetupTestRequestWithOwnerRepo("POST", "/test/repo.git/git-upload-pack", owner, repoParam)
	req.Body = io.NopCloser(bytes.NewReader(cloneRequestBody(sha)))

	rec := &headerOrderRecorder{}
	h.HandleUploadPack(rec, req)

	require.True(t, rec.sawHeader, "response header must be written")
	require.Equal(t, http.StatusOK, rec.status)
	require.True(t, rec.sawFlush, "response header must be flushed before pack generation")
	require.NotEmpty(t, rec.body.Bytes(), "a real pack body should have streamed")

	headerIdx := indexOfEvent(rec.events, "header")
	flushIdx := indexOfEvent(rec.events, "flush")
	bodyIdx := indexOfEvent(rec.events, "body")
	require.Less(t, headerIdx, bodyIdx, "header must precede body")
	require.Less(t, flushIdx, bodyIdx, "flush must precede body (header latency decoupled from pack compute)")
}
