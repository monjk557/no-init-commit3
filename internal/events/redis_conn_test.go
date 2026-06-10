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

// buildRedisConnOptions 는 EVENT_BUS_REDIS_* (host:port) 스키마를 정규화한다.
func TestBuildRedisConnOptions(t *testing.T) {
	tests := []struct {
		name      string
		cfg       config.EventsConfig
		wantMode  string
		wantAddrs []string
		wantErr   bool
	}{
		{
			name:      "standalone single address",
			cfg:       config.EventsConfig{Mode: "standalone", NodeAddress: "redis:6379", SocketTimeout: 5 * time.Second},
			wantMode:  "standalone",
			wantAddrs: []string{"redis:6379"},
		},
		{
			name:      "cluster csv with whitespace",
			cfg:       config.EventsConfig{Mode: "cluster", NodeAddress: "n1:6379, n2:6379 ,n3:6379"},
			wantMode:  "cluster",
			wantAddrs: []string{"n1:6379", "n2:6379", "n3:6379"},
		},
		{
			name:      "empty mode defaults to standalone",
			cfg:       config.EventsConfig{NodeAddress: "redis:6379"},
			wantMode:  "standalone",
			wantAddrs: []string{"redis:6379"},
		},
		{
			name:    "empty address is an error",
			cfg:     config.EventsConfig{Mode: "standalone", NodeAddress: "  ,  "},
			wantErr: true,
		},
		{
			name:    "invalid mode is an error",
			cfg:     config.EventsConfig{Mode: "sentinel", NodeAddress: "redis:6379"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildRedisConnOptions(tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantMode, got.mode)
			assert.Equal(t, tt.wantAddrs, got.addrs)
			// 타임아웃 미지정 시 5s 기본값으로 채워진다.
			assert.Greater(t, got.socketTimeout, time.Duration(0))
		})
	}
}

// EVENT_BUS_REDIS_* (host:port) 스키마로 standalone 클라이언트가 붙는다.
func TestNewRedisStreamClientFromConfig_Standalone(t *testing.T) {
	mr := miniredis.RunT(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClientFromConfig(config.EventsConfig{
		Enabled:       true,
		Mode:          "standalone",
		NodeAddress:   mr.Addr(), // miniredis 는 host:port 형태를 준다
		SocketTimeout: 5 * time.Second,
	}, logger)
	require.NoError(t, err)
	require.NotNil(t, client)
	t.Cleanup(func() { _ = client.Close() })
}

// NODE_ADDRESS 없이 RedisURL 만 있으면 DEPRECATED URL 경로로 폴백한다.
func TestNewRedisStreamClientFromConfig_LegacyURLFallback(t *testing.T) {
	mr := miniredis.RunT(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClientFromConfig(config.EventsConfig{
		Enabled:  true,
		RedisURL: "redis://" + mr.Addr() + "/0",
	}, logger)
	require.NoError(t, err)
	require.NotNil(t, client)
	t.Cleanup(func() { _ = client.Close() })
}

// 주소가 전혀 없으면 에러.
func TestNewRedisStreamClientFromConfig_NoAddress(t *testing.T) {
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))
	_, err := NewRedisStreamClientFromConfig(config.EventsConfig{Enabled: true}, logger)
	require.Error(t, err)
}
