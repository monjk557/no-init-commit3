package events

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"go.uber.org/zap"
)

// lazyDialCooldown 은 dial 실패 후 다음 재시도까지의 최소 간격이다. Redis 가
// 죽어 있는 동안 매 이벤트마다 socketTimeout 만큼 블로킹되는 것을 막는다.
const lazyDialCooldown = 5 * time.Second

// lazyStreamClient 는 Redis 연결을 첫 사용 시점까지 미루고, 실패하면 다음
// 이벤트에서 다시 붙는 RedisStreamClient 이다.
//
// 기존에는 각 핸들러가 생성 시점에 NewRedisStreamClientFromConfig 를 1회만
// 호출하고, 실패하면 eventsClient 를 영구 nil 로 두었다. pod 기동이 Redis
// 순단과 겹치면 그 레플리카는 프로세스가 죽을 때까지 이벤트를 발행하지 못했다
// (Issue-6414). 연결 획득을 사용 시점으로 옮기면 순단이 회복되는 즉시 자동으로
// 복구된다.
type lazyStreamClient struct {
	cfg    config.EventsConfig
	logger *zap.Logger

	// ponytail: 단일 mutex — dial 중에는 다른 publish 가 대기한다. 발행은 모두
	// goroutine 에서 일어나므로 요청 경로를 막지 않는다. 경합이 문제가 되면
	// singleflight 로 교체.
	mu        sync.Mutex
	client    RedisStreamClient
	cooldown  time.Duration
	nextRetry time.Time
	closed    bool
}

// NewLazyRedisStreamClient 는 즉시 연결하지 않는 클라이언트를 만든다. 설정이
// 잘못됐거나 Redis 가 떠 있지 않아도 생성은 항상 성공하며, 실제 연결 실패는
// AddCommitEventToStream 의 에러로 전달된다.
func NewLazyRedisStreamClient(cfg config.EventsConfig, logger *zap.Logger) RedisStreamClient {
	return &lazyStreamClient{cfg: cfg, logger: logger, cooldown: lazyDialCooldown}
}

// resolve 는 캐시된 클라이언트를 반환하고, 없으면 cooldown 을 지켜 재연결한다.
func (l *lazyStreamClient) resolve() (RedisStreamClient, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil, fmt.Errorf("events client is closed")
	}
	if l.client != nil {
		return l.client, nil
	}
	if now := time.Now(); now.Before(l.nextRetry) {
		return nil, fmt.Errorf("redis events client unavailable, retrying in %s", time.Until(l.nextRetry).Truncate(time.Millisecond))
	}

	client, err := NewRedisStreamClientFromConfig(l.cfg, l.logger)
	if err != nil {
		l.nextRetry = time.Now().Add(l.cooldown)
		if l.logger != nil {
			l.logger.Warn("Redis events client connect failed, event dropped (will retry on next event)",
				zap.Duration("retry_after", l.cooldown),
				zap.Error(err))
		}
		return nil, fmt.Errorf("failed to connect Redis events client: %w", err)
	}

	l.client = client
	if l.logger != nil {
		l.logger.Info("Redis events client connected")
	}
	return client, nil
}

func (l *lazyStreamClient) AddCommitEventToStream(ctx context.Context, event *CommitEvent) error {
	client, err := l.resolve()
	if err != nil {
		return err
	}
	return client.AddCommitEventToStream(ctx, event)
}

func (l *lazyStreamClient) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.closed = true
	if l.client == nil {
		return nil
	}
	client := l.client
	l.client = nil
	return client.Close()
}
