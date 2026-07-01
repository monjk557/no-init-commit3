package packcache

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Env var names for the pack-objects cache.
const (
	EnvEnabled  = "GIT_EXECUTOR_PACK_CACHE_ENABLED"
	EnvDir      = "GIT_EXECUTOR_PACK_CACHE_DIR"
	EnvTTL      = "GIT_EXECUTOR_PACK_CACHE_TTL"
	EnvMaxBytes = "GIT_EXECUTOR_PACK_CACHE_MAX_BYTES"
)

// Config controls the pack-objects cache. Disabled by default so the cache can
// be merged dark and enabled per environment during rollout.
type Config struct {
	// Enabled turns the cache on. Off by default.
	Enabled bool
	// Dir is the on-disk directory for cached pack files. Should live on the
	// repo storage volume so it shares the pods' lifecycle.
	Dir string
	// TTL bounds how long a cached pack is served before it is treated as a
	// miss. Short by design (identical CI clone bursts arrive within seconds);
	// push/gc invalidation (follow-up) handles correctness beyond the TTL.
	TTL time.Duration
	// MaxBytes caps total on-disk cache size (eviction is a follow-up).
	MaxBytes int64
}

// Default returns conservative defaults with the cache disabled.
func Default() Config {
	return Config{
		Enabled:  false,
		Dir:      "/git-repos/.packcache",
		TTL:      120 * time.Second,
		MaxBytes: 10 << 30, // 10 GiB
	}
}

// FromEnv builds a Config from environment variables, falling back to Default
// for any unset/invalid value.
func FromEnv() Config {
	c := Default()

	if v, ok := os.LookupEnv(EnvEnabled); ok {
		c.Enabled = parseBool(v, c.Enabled)
	}
	if v := strings.TrimSpace(os.Getenv(EnvDir)); v != "" {
		c.Dir = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTTL)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			c.TTL = d
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvMaxBytes)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			c.MaxBytes = n
		}
	}
	return c
}

func parseBool(v string, def bool) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return def
	}
}
