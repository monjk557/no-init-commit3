package tags

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// service_coverage_internal_test.go — 순수 헬퍼 / 에러 경로를 internal(package tags)
// 에서 직접 커버한다. getTagDate, createLightweightTag 실패 경로, 그리고
// NewTagsHandler 의 events-enabled 분기(Redis 미설정 → best-effort no-op).

func TestGetTagDate(t *testing.T) {
	t.Parallel()

	when := time.Date(2024, 3, 2, 1, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		tag  Tag
		want time.Time
	}{
		{
			name: "nil tagger returns zero time",
			tag:  Tag{Name: "v1", Tagger: nil},
			want: time.Time{},
		},
		{
			name: "tagger set returns tagger date",
			tag:  Tag{Name: "v2", Tagger: &Tagger{Name: "t", Email: "t@t.com", Date: when}},
			want: when,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, getTagDate(tc.tag))
		})
	}
}

func TestCreateLightweightTag_InvalidRepo_ReturnsError(t *testing.T) {
	t.Parallel()

	// 존재하지 않는 git dir → NativeCreateLightweightTag 가 실패해야 한다.
	badDir := filepath.Join(t.TempDir(), "does-not-exist.git")

	tag, err := createLightweightTag(context.Background(), badDir, "v1.0.0",
		"abcd1234567890abcd1234567890abcd12345678")

	require.Error(t, err)
	assert.Nil(t, tag)
}

func TestNewTagsHandler_EventsEnabled_RedisUnconfigured_DegradesToNoop(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Git.RepoRoot = t.TempDir()
	cfg.Logging = config.LoggingConfig{
		Level: "error", Format: "text", Output: "stdout", Development: true,
	}
	// Events.Enabled 이지만 NodeAddress / RedisURL 미설정 → lazy 클라이언트가
	// 붙어 있고, 실제 발행 시점에 에러로 degrade 한다 (Issue-6414: 생성 시점
	// 연결 실패로 eventsClient 를 영구 nil 로 두지 않는다).
	cfg.Events = config.EventsConfig{Enabled: true}

	h, err := NewTagsHandler(cfg)
	require.NoError(t, err)
	require.NotNil(t, h)
	require.NotNil(t, h.eventsClient, "events client must stay wired so it can reconnect later")
	assert.Error(t,
		h.eventsClient.AddCommitEventToStream(context.Background(), &events.CommitEvent{
			RepoPath: "ws/repo.git", EventType: events.EventTypeTagPush, CommitHash: "abc", RefName: "refs/tags/v1",
		}),
		"unreachable Redis should surface a publish error, not panic")
}
