package revert

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
)

// firstLine 은 첫 줄만 잘라내는 순수 문자열 헬퍼.
func TestFirstLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no newline returns whole string", "single line", "single line"},
		{"multi line returns first", "first\nsecond\nthird", "first"},
		{"leading newline yields empty", "\nrest", ""},
		{"empty string", "", ""},
		{"trailing newline", "only\n", "only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, firstLine(tt.in))
		})
	}
}

// shortSHA 는 7자로 자르는 순수 문자열 헬퍼.
func TestShortSHA(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"long sha truncated to 7", "0123456789abcdef", "0123456"},
		{"exactly 7 unchanged", "0123456", "0123456"},
		{"shorter than 7 unchanged", "abc", "abc"},
		{"empty unchanged", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shortSHA(tt.in))
		})
	}
}

// validatePath 는 owner/repo 검증 (빈 값 + traversal).
func TestValidatePath(t *testing.T) {
	root := t.TempDir()
	h := newTestHandler(t, root, nil)

	t.Run("empty owner", func(t *testing.T) {
		require.Error(t, h.validatePath("", "repo"))
	})
	t.Run("empty repo", func(t *testing.T) {
		require.Error(t, h.validatePath("owner", ""))
	})
	t.Run("path traversal", func(t *testing.T) {
		require.Error(t, h.validatePath("../escape", "repo"))
	})
	t.Run("valid path", func(t *testing.T) {
		require.NoError(t, h.validatePath("owner", "repo"))
	})
}

// NewRevertHandler 성공 경로 — Events 비활성.
func TestNewRevertHandler_Success_EventsDisabled(t *testing.T) {
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: t.TempDir()},
		Logging: config.LoggingConfig{Level: "error", Format: "text", Output: "stdout", Development: true},
		Events:  config.EventsConfig{Enabled: false},
	}
	h, err := NewRevertHandler(cfg)
	require.NoError(t, err)
	require.NotNil(t, h)
}

// NewRevertHandler — Events 활성이지만 Redis 연결 실패 시 경고 후 계속 진행.
func TestNewRevertHandler_EventsEnabled_ConnFailStillReturnsHandler(t *testing.T) {
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: t.TempDir()},
		Logging: config.LoggingConfig{Level: "error", Format: "text", Output: "stdout", Development: true},
		Events: config.EventsConfig{
			Enabled:     true,
			Mode:        "standalone",
			NodeAddress: "nonexistent-redis-host:6379",
		},
	}
	h, err := NewRevertHandler(cfg)
	require.NoError(t, err)
	require.NotNil(t, h)
}

// NewRevertHandler — 잘못된 RepoRoot 는 path config 생성 실패.
func TestNewRevertHandler_InvalidRepoRoot(t *testing.T) {
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: ""},
		Logging: config.LoggingConfig{Level: "error", Format: "text", Output: "stdout", Development: true},
		Events:  config.EventsConfig{Enabled: false},
	}
	_, err := NewRevertHandler(cfg)
	require.Error(t, err)
}

// createFullWorktree 는 bare repo 로부터 detached worktree 를 만든다.
func TestCreateFullWorktree(t *testing.T) {
	root := t.TempDir()
	bareDir, _ := setupBareRepoWithCommits(t, root, "owner", "repo")
	ctx := context.Background()

	t.Run("valid ref creates worktree", func(t *testing.T) {
		wtDir := filepath.Join(t.TempDir(), "wt")
		err := createFullWorktree(ctx, bareDir, wtDir, "main")
		require.NoError(t, err)
		_, statErr := os.Stat(filepath.Join(wtDir, "README.md"))
		require.NoError(t, statErr)
	})

	t.Run("invalid ref errors", func(t *testing.T) {
		wtDir := filepath.Join(t.TempDir(), "wt-bad")
		err := createFullWorktree(ctx, bareDir, wtDir, "no-such-ref")
		require.Error(t, err)
	})
}
