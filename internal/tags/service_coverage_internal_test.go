package tags

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
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
	// Events.Enabled 이지만 NodeAddress / RedisURL 미설정 →
	// NewRedisStreamClientFromConfig 가 즉시 에러(연결 시도 없음) → Warn 후
	// eventsClient=nil 로 degrade 하고 handler 는 정상 반환되어야 한다.
	cfg.Events = config.EventsConfig{Enabled: true}

	h, err := NewTagsHandler(cfg)
	require.NoError(t, err)
	require.NotNil(t, h)
	assert.Nil(t, h.eventsClient, "unreachable Redis config should degrade to nil events client")
}
