package events

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/collabops/git-executor/internal/config"
	"github.com/stretchr/testify/require"
)

// freeAddr 는 즉시 반납한 127.0.0.1 의 빈 포트를 돌려준다. 처음엔 연결이
// 거부되고, 나중에 같은 주소로 miniredis 를 띄워 Redis 복구를 재현한다.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// Issue-6414 회귀: Redis 순단 중 생성된 클라이언트는 영구 불능이 되면 안 되고,
// Redis 가 돌아오면 다음 이벤트부터 스스로 발행을 재개해야 한다.
func TestLazyStreamClientRecoversAfterRedisOutage(t *testing.T) {
	addr := freeAddr(t)
	cfg := config.EventsConfig{
		Enabled:       true,
		NodeAddress:   addr,
		Mode:          config.RedisModeStandalone,
		SocketTimeout: 500 * time.Millisecond,
	}

	client := NewLazyRedisStreamClient(cfg, nil)
	defer func() { _ = client.Close() }()
	client.(*lazyStreamClient).cooldown = 0 // 테스트에서 재시도 대기 제거

	event := &CommitEvent{
		RepoPath:   "ws/repo.git",
		EventType:  EventTypeMerge,
		CommitHash: "abc123",
		RefName:    "refs/heads/main",
		Timestamp:  time.Now(),
	}

	ctx := context.Background()
	require.Error(t, client.AddCommitEventToStream(ctx, event), "redis 미가동 중에는 에러를 돌려줘야 한다")

	mr := miniredis.NewMiniRedis()
	require.NoError(t, mr.StartAddr(addr))
	defer mr.Close()

	require.NoError(t, client.AddCommitEventToStream(ctx, event), "redis 복구 후에는 같은 클라이언트로 발행되어야 한다")
}
