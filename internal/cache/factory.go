package cache

import (
	"fmt"
)

// NewRepositoryCache는 주어진 설정으로 새로운 저장소 캐시 인스턴스를 생성합니다
func NewRepositoryCache(config *CacheConfig) (RepositoryCache, error) {
	if config == nil {
		config = NewCacheConfig()
	}

	// LRU 캐시 구현 생성
	lruCache, err := NewLRUCache(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create LRU cache: %w", err)
	}

	return lruCache, nil
}

// NewDefaultRepositoryCache는 기본 설정으로 저장소 캐시를 생성합니다
func NewDefaultRepositoryCache() (RepositoryCache, error) {
	config := NewCacheConfig()
	return NewRepositoryCache(config)
}
