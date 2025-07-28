// Package cache implements repository caching for improved performance
package cache

import (
	"fmt"
	"time"
)

// CacheConfig defines configuration parameters for the repository cache
type CacheConfig struct {
	// MaxRepositories is the maximum number of repositories to cache
	// Default: 100, Range: 1-10000
	MaxRepositories int `yaml:"max_repositories" json:"max_repositories"`

	// TTL is the time-to-live for cache entries
	// Default: 30 minutes, Range: 1 minute - 24 hours
	TTL time.Duration `yaml:"ttl" json:"ttl"`

	// MaxMemoryMB is the optional maximum memory usage in megabytes
	// Default: 0 (unlimited), Range: 0-4096
	MaxMemoryMB int `yaml:"max_memory_mb" json:"max_memory_mb"`

	// CleanupInterval is how often to run TTL-based cleanup
	// Default: 5 minutes, Range: 1 minute - 1 hour
	CleanupInterval time.Duration `yaml:"cleanup_interval" json:"cleanup_interval"`

	// Enabled determines if caching is enabled
	// Default: true
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// Default configuration values
const (
	DefaultMaxRepositories = 100
	DefaultTTL             = 30 * time.Minute
	DefaultMaxMemoryMB     = 0 // unlimited
	DefaultCleanupInterval = 5 * time.Minute
	DefaultEnabled         = true

	// Validation limits
	MinMaxRepositories = 1
	MaxMaxRepositories = 10000
	MinTTL             = 1 * time.Minute
	MaxTTL             = 24 * time.Hour
	MaxMaxMemoryMB     = 4096
	MinCleanupInterval = 1 * time.Minute
	MaxCleanupInterval = 1 * time.Hour
)

// NewCacheConfig creates a new cache configuration with sensible defaults
func NewCacheConfig() *CacheConfig {
	return &CacheConfig{
		MaxRepositories: DefaultMaxRepositories,
		TTL:             DefaultTTL,
		MaxMemoryMB:     DefaultMaxMemoryMB,
		CleanupInterval: DefaultCleanupInterval,
		Enabled:         DefaultEnabled,
	}
}

// Validate checks the configuration for valid values and applies corrections
func (c *CacheConfig) Validate() error {
	var errors []string

	// Validate MaxRepositories
	if c.MaxRepositories < MinMaxRepositories {
		errors = append(errors, fmt.Sprintf("max_repositories must be at least %d, got %d", MinMaxRepositories, c.MaxRepositories))
	}
	if c.MaxRepositories > MaxMaxRepositories {
		errors = append(errors, fmt.Sprintf("max_repositories must be at most %d, got %d", MaxMaxRepositories, c.MaxRepositories))
	}

	// Validate TTL
	if c.TTL < MinTTL {
		errors = append(errors, fmt.Sprintf("ttl must be at least %v, got %v", MinTTL, c.TTL))
	}
	if c.TTL > MaxTTL {
		errors = append(errors, fmt.Sprintf("ttl must be at most %v, got %v", MaxTTL, c.TTL))
	}

	// Validate MaxMemoryMB (0 means unlimited, so it's valid)
	if c.MaxMemoryMB < 0 {
		errors = append(errors, fmt.Sprintf("max_memory_mb cannot be negative, got %d", c.MaxMemoryMB))
	}
	if c.MaxMemoryMB > MaxMaxMemoryMB {
		errors = append(errors, fmt.Sprintf("max_memory_mb must be at most %d, got %d", MaxMaxMemoryMB, c.MaxMemoryMB))
	}

	// Validate CleanupInterval
	if c.CleanupInterval < MinCleanupInterval {
		errors = append(errors, fmt.Sprintf("cleanup_interval must be at least %v, got %v", MinCleanupInterval, c.CleanupInterval))
	}
	if c.CleanupInterval > MaxCleanupInterval {
		errors = append(errors, fmt.Sprintf("cleanup_interval must be at most %v, got %v", MaxCleanupInterval, c.CleanupInterval))
	}

	// Return validation errors if any
	if len(errors) > 0 {
		return fmt.Errorf("cache configuration validation failed: %v", errors)
	}

	return nil
}

// ApplyDefaults sets default values for any zero/unset fields
func (c *CacheConfig) ApplyDefaults() {
	if c.MaxRepositories == 0 {
		c.MaxRepositories = DefaultMaxRepositories
	}
	if c.TTL == 0 {
		c.TTL = DefaultTTL
	}
	// MaxMemoryMB can be 0 (unlimited), so don't apply default
	if c.CleanupInterval == 0 {
		c.CleanupInterval = DefaultCleanupInterval
	}
	// Enabled is bool, false is a valid value, so check explicitly
}

// ValidateAndApplyDefaults applies defaults and then validates the configuration
func (c *CacheConfig) ValidateAndApplyDefaults() error {
	c.ApplyDefaults()
	return c.Validate()
}

// String returns a human-readable representation of the cache configuration
func (c *CacheConfig) String() string {
	return fmt.Sprintf("CacheConfig{MaxRepositories: %d, TTL: %v, MaxMemoryMB: %d, CleanupInterval: %v, Enabled: %t}",
		c.MaxRepositories, c.TTL, c.MaxMemoryMB, c.CleanupInterval, c.Enabled)
}

// IsMemoryLimited returns true if memory usage is limited
func (c *CacheConfig) IsMemoryLimited() bool {
	return c.MaxMemoryMB > 0
}

// MemoryLimitBytes returns the memory limit in bytes, or 0 if unlimited
func (c *CacheConfig) MemoryLimitBytes() int64 {
	if c.MaxMemoryMB == 0 {
		return 0
	}
	return int64(c.MaxMemoryMB) * 1024 * 1024
}
