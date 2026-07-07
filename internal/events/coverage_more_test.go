package events

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/collabops/git-executor/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// GetRelativeRepoPath 는 순수 경로 로직 — 네트워크 의존 없음.
func TestGetRelativeRepoPath(t *testing.T) {
	tests := []struct {
		name     string
		fullPath string
		repoRoot string
		want     string
	}{
		{
			name:     "strips repo root prefix",
			fullPath: "/var/lib/git/repos/owner/repo.git",
			repoRoot: "/var/lib/git/repos",
			want:     "owner/repo.git",
		},
		{
			name:     "trailing slash on root is normalized",
			fullPath: "/var/lib/git/repos/owner/repo.git",
			repoRoot: "/var/lib/git/repos/",
			want:     "owner/repo.git",
		},
		{
			name:     "root equals full path yields empty relative",
			fullPath: "/var/lib/git/repos",
			repoRoot: "/var/lib/git/repos",
			want:     "",
		},
		{
			name:     "prefix mismatch returns input unchanged",
			fullPath: "owner/repo.git",
			repoRoot: "/var/lib/git/repos",
			want:     "owner/repo.git",
		},
		{
			name:     "different absolute prefix returned as-is",
			fullPath: "/some/other/path/repo.git",
			repoRoot: "/var/lib/git/repos",
			want:     "/some/other/path/repo.git",
		},
		{
			name:     "unclean path is cleaned before stripping",
			fullPath: "/var/lib/git/repos/./owner/../owner/repo.git",
			repoRoot: "/var/lib/git/repos",
			want:     "owner/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetRelativeRepoPath(tt.fullPath, tt.repoRoot)
			assert.Equal(t, tt.want, got)
		})
	}
}

// 빈 redisURL 은 즉시 에러.
func TestNewRedisStreamClient_EmptyURL(t *testing.T) {
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))
	_, err := NewRedisStreamClient("   ", logger)
	require.Error(t, err)
}

// logger 가 nil 이면 기본 production logger 를 만들어 정상 동작한다.
func TestNewRedisStreamClient_NilLogger(t *testing.T) {
	mr := miniredis.RunT(t)
	client, err := NewRedisStreamClient("redis://"+mr.Addr()+"/0", nil)
	require.NoError(t, err)
	require.NotNil(t, client)
	t.Cleanup(func() { _ = client.Close() })
}

// 콤마가 포함되면 cluster 경로로 분기하고, 붙을 수 없는 호스트면 에러.
func TestNewRedisStreamClient_ClusterPath_ConnFail(t *testing.T) {
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))
	url := "redis://nonexistent-host-a:6379/0,redis://nonexistent-host-b:6379/0"
	_, err := NewRedisStreamClient(url, logger)
	require.Error(t, err)
}

// cluster URL 파싱 실패 (잘못된 scheme) → 에러.
func TestNewClusterRedisStreamClient_ParseError(t *testing.T) {
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))
	// 첫 노드는 정상 파싱, 두 번째는 잘못된 scheme 로 파싱 실패.
	url := "redis://ok-host:6379/0,http://not-a-redis-url"
	_, err := NewRedisStreamClient(url, logger)
	require.Error(t, err)
}

// 콤마만 있고 유효 주소가 하나도 없으면 → 에러.
func TestNewClusterRedisStreamClient_NoValidAddrs(t *testing.T) {
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))
	_, err := NewRedisStreamClient(", ,", logger)
	require.Error(t, err)
}

// NewRedisStreamClientFromConfig: logger 가 nil 이어도 기본 logger 로 붙는다.
func TestNewRedisStreamClientFromConfig_NilLogger(t *testing.T) {
	mr := miniredis.RunT(t)
	client, err := NewRedisStreamClientFromConfig(config.EventsConfig{
		Enabled:       true,
		Mode:          "standalone",
		NodeAddress:   mr.Addr(),
		SocketTimeout: 2 * time.Second,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, client)
	t.Cleanup(func() { _ = client.Close() })
}

// cluster 모드 → newStreamClientFromOptions 의 cluster 분기 실행 (연결 실패).
func TestNewRedisStreamClientFromConfig_ClusterMode_ConnFail(t *testing.T) {
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))
	_, err := NewRedisStreamClientFromConfig(config.EventsConfig{
		Enabled:       true,
		Mode:          "cluster",
		NodeAddress:   "nonexistent-c-1:6379,nonexistent-c-2:6379",
		SocketTimeout: 1 * time.Second,
	}, logger)
	require.Error(t, err)
}

// 잘못된 mode 는 buildRedisConnOptions 단계에서 에러.
func TestNewRedisStreamClientFromConfig_InvalidMode(t *testing.T) {
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))
	_, err := NewRedisStreamClientFromConfig(config.EventsConfig{
		Enabled:     true,
		Mode:        "sentinel",
		NodeAddress: "redis:6379",
	}, logger)
	require.Error(t, err)
}
