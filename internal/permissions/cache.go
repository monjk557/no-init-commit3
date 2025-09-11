// permissions 패키지는 사용자 권한 검증과 캐싱 기능을 제공합니다.
// 권한 결과 캐싱, 서킷 브레이커, 외부 권한 서비스 클라이언트를 포함합니다.
package permissions

import (
	"sync"
	"time"
)

// PermissionCache는 권한 검사 결과의 스레드 안전한 캐싱을 제공합니다.
// 동시 액세스를 위해 sync.Map을 사용하고 TTL 기반 만료을 포함합니다.
type PermissionCache struct {
	cache sync.Map
	ttl   time.Duration
	// We could add a cleanup goroutine here in the future for automatic expiration
}

// NewPermissionCache는 지정된 TTL로 새로운 권한 캐시를 생성합니다.
func NewPermissionCache(ttl time.Duration) *PermissionCache {
	return &PermissionCache{
		cache: sync.Map{},
		ttl:   ttl,
	}
}

// Get은 캐시에서 권한 결과를 가져옵니다.
// 키가 존재하지 않으면 nil을 반환합니다.
func (pc *PermissionCache) Get(key string) *PermissionResult {
	value, exists := pc.cache.Load(key)
	if !exists {
		return nil
	}

	result, ok := value.(*PermissionResult)
	if !ok {
		// This should never happen, but handle it gracefully
		pc.cache.Delete(key)
		return nil
	}

	return result
}

// Set stores a permission result in the cache.
func (pc *PermissionCache) Set(key string, result *PermissionResult) {
	pc.cache.Store(key, result)
}

// Delete removes a permission result from the cache.
func (pc *PermissionCache) Delete(key string) {
	pc.cache.Delete(key)
}

// Clear removes all entries from the cache.
func (pc *PermissionCache) Clear() {
	pc.cache.Range(func(key, value interface{}) bool {
		pc.cache.Delete(key)
		return true
	})
}

// Close gracefully shuts down the cache.
// Currently, this doesn't do anything since sync.Map doesn't require cleanup,
// but it's here for future extensibility (e.g., stopping cleanup goroutines).
func (pc *PermissionCache) Close() {
	// Future: Stop any background cleanup goroutines
}

// CleanupExpired는 캐시에서 만료된 항목을 제거합니다.
// 주기적으로 호출할 수 있는 수동 정리 메소드입니다.
func (pc *PermissionCache) CleanupExpired() {
	now := time.Now()
	pc.cache.Range(func(key, value interface{}) bool {
		result, ok := value.(*PermissionResult)
		if !ok {
			// Invalid entry, remove it
			pc.cache.Delete(key)
			return true
		}

		if now.After(result.ExpiresAt) {
			pc.cache.Delete(key)
		}
		return true
	})
}

// Size returns the approximate number of entries in the cache.
// Note: This iterates through all entries, so it's O(n) and should be used sparingly.
func (pc *PermissionCache) Size() int {
	count := 0
	pc.cache.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}
