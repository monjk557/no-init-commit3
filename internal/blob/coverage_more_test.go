package blob

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getErrorStatusCode 는 에러 메시지 -> HTTP 상태 코드 매핑 (순수 함수).
func TestGetErrorStatusCode(t *testing.T) {
	h := &BlobHandler{}
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"directory", errors.New("path is a directory"), http.StatusBadRequest},
		{"file not found", errors.New("file not found: x.go"), http.StatusNotFound},
		{"generic not found", errors.New("ref not found"), http.StatusNotFound},
		{"missing param", errors.New("missing owner parameter"), http.StatusBadRequest},
		{"invalid input", errors.New("invalid ref"), http.StatusBadRequest},
		{"permission", errors.New("permission denied"), http.StatusForbidden},
		{"access denied", errors.New("access denied for repo"), http.StatusForbidden},
		{"authentication", errors.New("authentication required"), http.StatusUnauthorized},
		{"unknown -> 500", errors.New("something exploded"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, h.getErrorStatusCode(tt.err))
		})
	}
}

// parseRangeValid 는 HTTP Range 헤더 파싱 (순수 함수).
func TestParseRangeValid(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty is valid", "", true},
		{"closed range", "bytes=0-499", true},
		{"open ended range", "bytes=100-", true},
		{"suffix range (empty start)", "bytes=-500", true},
		{"missing bytes prefix", "invalid", false},
		{"bytes prefix only", "bytes=", false},
		{"non-numeric start", "abc-def", false},
		{"non-numeric start with prefix", "bytes=abc-1", false},
		{"non-numeric end", "bytes=0-abc", false},
		{"too many dashes", "bytes=0-499-999", false},
		{"end before start", "bytes=500-100", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseRangeValid(tt.input))
		})
	}
}

// normalizeMIMEType 은 enry vendor MIME -> IANA 정규화 (순수 함수).
func TestNormalizeMIMEType(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"gfm normalized to markdown", "text/x-gfm", "text/markdown"},
		{"unmapped passthrough", "text/plain", "text/plain"},
		{"empty passthrough", "", ""},
		{"binary passthrough", "application/octet-stream", "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeMIMEType(tt.in))
		})
	}
}

// setupNativeBareRepo 는 native git 으로 읽을 수 있는 bare repo 를 만들고
// refs/heads/main + HEAD 를 세팅한 뒤 repoPath 와 commit SHA 를 반환한다.
func setupNativeBareRepo(t *testing.T) (repoPath, commitSHA string) {
	t.Helper()
	repoPath = filepath.Join(t.TempDir(), "owner", "repo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))

	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.com",
			"GIT_AUTHOR_DATE=2024-01-01T00:00:00+00:00",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.com",
			"GIT_COMMITTER_DATE=2024-01-01T00:00:00+00:00",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimRight(string(out), "\r\n")
	}

	run("init", "--bare", repoPath)

	blobCmd := exec.Command("git", "--git-dir", repoPath, "hash-object", "-w", "--stdin")
	blobCmd.Stdin = strings.NewReader("hello\n")
	blobOut, err := blobCmd.Output()
	require.NoError(t, err)
	blob := strings.TrimRight(string(blobOut), "\r\n")

	treeCmd := exec.Command("git", "--git-dir", repoPath, "mktree")
	treeCmd.Stdin = strings.NewReader("100644 blob " + blob + "\tREADME.md\n")
	treeOut, err := treeCmd.Output()
	require.NoError(t, err)
	tree := strings.TrimRight(string(treeOut), "\r\n")

	commitSHA = run("--git-dir", repoPath, "commit-tree", tree, "-m", "init")
	run("--git-dir", repoPath, "update-ref", "refs/heads/main", commitSHA)
	run("--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main")
	return repoPath, commitSHA
}

// NativeResolveRef 는 native git 으로 ref -> commit SHA resolve.
func TestNativeResolveRef(t *testing.T) {
	repoPath, commitSHA := setupNativeBareRepo(t)
	ctx := context.Background()

	t.Run("branch name resolves", func(t *testing.T) {
		got, err := NativeResolveRef(ctx, repoPath, "main")
		require.NoError(t, err)
		assert.Equal(t, commitSHA, got)
	})

	t.Run("empty ref defaults to HEAD", func(t *testing.T) {
		got, err := NativeResolveRef(ctx, repoPath, "")
		require.NoError(t, err)
		assert.Equal(t, commitSHA, got)
	})

	t.Run("full sha resolves to itself", func(t *testing.T) {
		got, err := NativeResolveRef(ctx, repoPath, commitSHA)
		require.NoError(t, err)
		assert.Equal(t, commitSHA, got)
	})

	t.Run("unknown ref errors", func(t *testing.T) {
		_, err := NativeResolveRef(ctx, repoPath, "no-such-ref")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to resolve reference")
	})
}
