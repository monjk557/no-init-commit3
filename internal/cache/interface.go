package cache

import gogit "github.com/go-git/go-git/v5"

// RepositoryCache는 Git 저장소 캐싱에 대한 인터페이스를 정의합니다
type RepositoryCache interface {
	// GetRepository는 파일시스템 경로로 캐시된 저장소를 검색합니다.
	// 찾으면 저장소와 true를, 캐시되지 않았으면 nil과 false를 반환합니다.
	GetRepository(path string) (*gogit.Repository, bool)

	// SetRepository는 파일시스템 경로를 키로 사용하여 캐시에 저장소를 저장합니다.
	// 캐시 구현체는 자체 정책에 따라 제거를 처리합니다.
	SetRepository(path string, repo *gogit.Repository) error

	// InvalidateRepository는 캐시에서 저장소를 제거합니다.
	// 저장소가 수정될 때 (예: push 후) 호출되어야 합니다.
	InvalidateRepository(path string) bool

	// InvalidateAll은 캐시에서 모든 저장소를 제거합니다.
	// 캐시 유지보수나 전역 저장소 변경이 발생할 때 유용합니다.
	InvalidateAll()

	// Close는 캐시를 정상적으로 종료하고 모든 자원을 해제합니다.
	// 캐시가 더 이상 필요하지 않을 때 호출되어야 합니다.
	Close() error

	// GetMetrics는 모니터링을 위한 캐시 성능 메트릭을 반환합니다.
	GetMetrics() CacheMetrics
}

// RepositoryCacheFactory는 새로운 RepositoryCache 인스턴스를 생성합니다
type RepositoryCacheFactory func(config *CacheConfig) (RepositoryCache, error)
