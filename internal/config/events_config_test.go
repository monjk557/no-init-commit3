package config

import (
	"testing"
	"time"
)

// parseEventsConfig 는 canonical EVENT_BUS_REDIS_* 스키마를 우선하고,
// 부재 시 DEPRECATED REDIS_URL 로 폴백하며, 둘 다 없으면 events 를 비활성화한다.
func TestParseEventsConfig(t *testing.T) {
	// 모든 관련 env 를 빈 값으로 리셋하는 헬퍼 (외부 CI env 오염 방지).
	resetEnv := func(t *testing.T) {
		for _, k := range []string{
			"EVENT_BUS_REDIS_MODE", "EVENT_BUS_REDIS_NODE_ADDRESS",
			"EVENT_BUS_REDIS_SOCKET_TIMEOUT_S", "EVENT_BUS_REDIS_USERNAME",
			"EVENT_BUS_REDIS_PASSWORD", "REDIS_URL",
		} {
			t.Setenv(k, "")
		}
	}

	t.Run("canonical standalone with ACL", func(t *testing.T) {
		resetEnv(t)
		t.Setenv("EVENT_BUS_REDIS_MODE", "standalone")
		t.Setenv("EVENT_BUS_REDIS_NODE_ADDRESS", "redis:6379")
		t.Setenv("EVENT_BUS_REDIS_USERNAME", "app")
		t.Setenv("EVENT_BUS_REDIS_PASSWORD", "secret")
		t.Setenv("EVENT_BUS_REDIS_SOCKET_TIMEOUT_S", "3")

		cfg, err := parseEventsConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.Enabled || cfg.Mode != RedisModeStandalone || cfg.NodeAddress != "redis:6379" {
			t.Fatalf("unexpected cfg: %+v", cfg)
		}
		if cfg.Username != "app" || cfg.Password != "secret" {
			t.Fatalf("ACL not parsed: %+v", cfg)
		}
		if cfg.SocketTimeout != 3*time.Second {
			t.Fatalf("socket timeout = %v, want 3s", cfg.SocketTimeout)
		}
		if cfg.RedisURL != "" {
			t.Fatalf("RedisURL should be empty in canonical path: %q", cfg.RedisURL)
		}
	})

	t.Run("canonical cluster, default mode and timeout", func(t *testing.T) {
		resetEnv(t)
		t.Setenv("EVENT_BUS_REDIS_NODE_ADDRESS", "n1:6379,n2:6379")
		t.Setenv("EVENT_BUS_REDIS_MODE", "cluster")

		cfg, err := parseEventsConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Mode != RedisModeCluster || cfg.SocketTimeout != defaultRedisSocketTimeout {
			t.Fatalf("unexpected cfg: %+v", cfg)
		}
	})

	t.Run("invalid mode is rejected", func(t *testing.T) {
		resetEnv(t)
		t.Setenv("EVENT_BUS_REDIS_NODE_ADDRESS", "redis:6379")
		t.Setenv("EVENT_BUS_REDIS_MODE", "sentinel")
		if _, err := parseEventsConfig(); err == nil {
			t.Fatal("expected error for invalid mode")
		}
	})

	t.Run("invalid socket timeout is rejected", func(t *testing.T) {
		resetEnv(t)
		t.Setenv("EVENT_BUS_REDIS_NODE_ADDRESS", "redis:6379")
		t.Setenv("EVENT_BUS_REDIS_SOCKET_TIMEOUT_S", "-1")
		if _, err := parseEventsConfig(); err == nil {
			t.Fatal("expected error for negative socket timeout")
		}
	})

	t.Run("legacy REDIS_URL fallback", func(t *testing.T) {
		resetEnv(t)
		t.Setenv("REDIS_URL", "redis://redis:6379/0")

		cfg, err := parseEventsConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.Enabled || cfg.RedisURL != "redis://redis:6379/0" || cfg.NodeAddress != "" {
			t.Fatalf("unexpected legacy cfg: %+v", cfg)
		}
	})

	t.Run("nothing set disables events", func(t *testing.T) {
		resetEnv(t)
		cfg, err := parseEventsConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Enabled {
			t.Fatalf("events should be disabled when no redis env set: %+v", cfg)
		}
	})

	t.Run("canonical takes precedence over legacy", func(t *testing.T) {
		resetEnv(t)
		t.Setenv("EVENT_BUS_REDIS_NODE_ADDRESS", "redis:6379")
		t.Setenv("REDIS_URL", "redis://legacy:6379/0")

		cfg, err := parseEventsConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.NodeAddress != "redis:6379" || cfg.RedisURL != "" {
			t.Fatalf("canonical should win over legacy: %+v", cfg)
		}
	})
}
