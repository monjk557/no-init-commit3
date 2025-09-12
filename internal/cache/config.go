// Package cache는 성능 향상을 위한 저장소 캐싱을 구현합니다
package cache

import (
	"fmt"
	"time"
)

// CacheConfig는 저장소 캐시에 대한 설정 매개변수를 정의합니다
type CacheConfig struct {
	// MaxRepositories는 캐시할 최대 저장소 수입니다
	// 기본값: 100, 범위: 1-10000
	MaxRepositories int `yaml:"max_repositories" json:"max_repositories"`

	// TTL은 캐시 항목의 수명(생존 시간)입니다
	// 기본값: 30분, 범위: 1분 - 24시간
	TTL time.Duration `yaml:"ttl" json:"ttl"`

	// MaxMemoryMB는 메가바이트 단위의 선택적 최대 메모리 사용량입니다
	// 기본값: 0 (무제한), 범위: 0-4096
	MaxMemoryMB int `yaml:"max_memory_mb" json:"max_memory_mb"`

	// CleanupInterval은 TTL 기반 정리를 실행하는 반복 주기입니다
	// 기본값: 5분, 범위: 1분 - 1시간
	CleanupInterval time.Duration `yaml:"cleanup_interval" json:"cleanup_interval"`

	// Enabled는 캐싱이 활성화되어 있는지 결정합니다
	// 기본값: true
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// 기본 설정 값들
const (
	DefaultMaxRepositories = 100
	DefaultTTL             = 30 * time.Minute
	DefaultMaxMemoryMB     = 0 // unlimited
	DefaultCleanupInterval = 5 * time.Minute
	DefaultEnabled         = true

	// 유효성 검사 제한
	MinMaxRepositories = 1
	MaxMaxRepositories = 10000
	MinTTL             = 1 * time.Minute
	MaxTTL             = 24 * time.Hour
	MaxMaxMemoryMB     = 4096
	MinCleanupInterval = 1 * time.Minute
	MaxCleanupInterval = 1 * time.Hour
)

// NewCacheConfig는 합리적인 기본값으로 새로운 캐시 설정을 생성합니다
func NewCacheConfig() *CacheConfig {
	return &CacheConfig{
		MaxRepositories: DefaultMaxRepositories,
		TTL:             DefaultTTL,
		MaxMemoryMB:     DefaultMaxMemoryMB,
		CleanupInterval: DefaultCleanupInterval,
		Enabled:         DefaultEnabled,
	}
}

// Validate는 설정에서 유효한 값을 확인하고 수정 사항을 적용합니다
func (c *CacheConfig) Validate() error {
	var errors []string

	// MaxRepositories 유효성 검사
	if c.MaxRepositories < MinMaxRepositories {
		errors = append(errors, fmt.Sprintf("max_repositories must be at least %d, got %d", MinMaxRepositories, c.MaxRepositories))
	}
	if c.MaxRepositories > MaxMaxRepositories {
		errors = append(errors, fmt.Sprintf("max_repositories must be at most %d, got %d", MaxMaxRepositories, c.MaxRepositories))
	}

	// TTL 유효성 검사
	if c.TTL < MinTTL {
		errors = append(errors, fmt.Sprintf("ttl must be at least %v, got %v", MinTTL, c.TTL))
	}
	if c.TTL > MaxTTL {
		errors = append(errors, fmt.Sprintf("ttl must be at most %v, got %v", MaxTTL, c.TTL))
	}

	// MaxMemoryMB 유효성 검사 (0은 무제한을 의미하므로 유효함)
	if c.MaxMemoryMB < 0 {
		errors = append(errors, fmt.Sprintf("max_memory_mb cannot be negative, got %d", c.MaxMemoryMB))
	}
	if c.MaxMemoryMB > MaxMaxMemoryMB {
		errors = append(errors, fmt.Sprintf("max_memory_mb must be at most %d, got %d", MaxMaxMemoryMB, c.MaxMemoryMB))
	}

	// CleanupInterval 유효성 검사
	if c.CleanupInterval < MinCleanupInterval {
		errors = append(errors, fmt.Sprintf("cleanup_interval must be at least %v, got %v", MinCleanupInterval, c.CleanupInterval))
	}
	if c.CleanupInterval > MaxCleanupInterval {
		errors = append(errors, fmt.Sprintf("cleanup_interval must be at most %v, got %v", MaxCleanupInterval, c.CleanupInterval))
	}

	// 유효성 검사 에러가 있으면 반환
	if len(errors) > 0 {
		return fmt.Errorf("cache configuration validation failed: %v", errors)
	}

	return nil
}

// ApplyDefaults는 0이거나 설정되지 않은 필드에 기본값을 설정합니다
func (c *CacheConfig) ApplyDefaults() {
	if c.MaxRepositories == 0 {
		c.MaxRepositories = DefaultMaxRepositories
	}
	if c.TTL == 0 {
		c.TTL = DefaultTTL
	}
	// MaxMemoryMB는 0(무제한)이 될 수 있으므로 기본값 적용 안함
	if c.CleanupInterval == 0 {
		c.CleanupInterval = DefaultCleanupInterval
	}
	// Enabled는 bool타입이고, false도 유효한 값이므로 명시적 확인 필요
}

// ValidateAndApplyDefaults는 기본값을 적용한 후 설정을 유효성 검사합니다
func (c *CacheConfig) ValidateAndApplyDefaults() error {
	c.ApplyDefaults()
	return c.Validate()
}

// String은 캐시 설정의 사람이 읽을 수 있는 표현을 반환합니다
func (c *CacheConfig) String() string {
	return fmt.Sprintf("CacheConfig{MaxRepositories: %d, TTL: %v, MaxMemoryMB: %d, CleanupInterval: %v, Enabled: %t}",
		c.MaxRepositories, c.TTL, c.MaxMemoryMB, c.CleanupInterval, c.Enabled)
}

// IsMemoryLimited는 메모리 사용량이 제한되어 있으면 true를 반환합니다
func (c *CacheConfig) IsMemoryLimited() bool {
	return c.MaxMemoryMB > 0
}

// MemoryLimitBytes는 메모리 제한을 바이트 단위로 반환하거나, 무제한이면 0을 반환합니다
func (c *CacheConfig) MemoryLimitBytes() int64 {
	if c.MaxMemoryMB == 0 {
		return 0
	}
	return int64(c.MaxMemoryMB) * 1024 * 1024
}
