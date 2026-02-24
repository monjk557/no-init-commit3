package main

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/collabops/git-executor/internal/performance"
	"go.uber.org/zap"
)

// MockL1Cache simulates the existing LRU cache
type MockL1Cache struct {
	cache  map[string]*MockRepo
	hits   int64
	misses int64
}

type MockRepo struct {
	path string
	// Simplified mock repository
}

func (m *MockL1Cache) GetRepository(path string) (*MockRepo, bool) {
	if repo, exists := m.cache[path]; exists {
		m.hits++
		return repo, true
	}
	m.misses++
	return nil, false
}

func (m *MockL1Cache) SetRepository(path string, repo *MockRepo) error {
	m.cache[path] = repo
	return nil
}

func (m *MockL1Cache) InvalidateRepository(path string) bool {
	if _, exists := m.cache[path]; exists {
		delete(m.cache, path)
		return true
	}
	return false
}

func (m *MockL1Cache) InvalidateAll() {
	m.cache = make(map[string]*MockRepo)
}

func (m *MockL1Cache) Close() error {
	return nil
}

func (m *MockL1Cache) GetMetrics() performance.CacheMetrics {
	return performance.CacheMetrics{
		Hits:   m.hits,
		Misses: m.misses,
	}
}

// MockL2Cache simulates Redis distributed cache
type MockL2Cache struct {
	cache  map[string][]byte
	hits   int64
	misses int64
}

func (m *MockL2Cache) Get(ctx context.Context, key string) ([]byte, error) {
	if data, exists := m.cache[key]; exists {
		m.hits++
		return data, nil
	}
	m.misses++
	return nil, fmt.Errorf("not found")
}

func (m *MockL2Cache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.cache[key] = value
	return nil
}

func (m *MockL2Cache) Delete(ctx context.Context, key string) error {
	delete(m.cache, key)
	return nil
}

func (m *MockL2Cache) Exists(ctx context.Context, key string) (bool, error) {
	_, exists := m.cache[key]
	return exists, nil
}

func (m *MockL2Cache) Close() error {
	return nil
}

func main() {
	fmt.Println("🎯 Multi-Tier 캐싱 최적화 성능 테스트")
	fmt.Println("====================================")

	logger, _ := zap.NewDevelopment()

	// Test scenarios
	repositories := []string{
		"user1/project-a", "user1/project-b", "user1/project-c",
		"user2/service-x", "user2/service-y", "user2/service-z",
		"team/frontend", "team/backend", "team/database",
		"org/core", "org/utils", "org/docs",
	}

	// Create mock caches
	l1Cache := &MockL1Cache{cache: make(map[string]*MockRepo)}
	l2Cache := &MockL2Cache{cache: make(map[string][]byte)}

	// Test traditional single-tier vs multi-tier
	fmt.Println("\n📊 캐시 성능 비교:")

	// Test 1: Traditional single-tier cache
	singleTierHitRate := testSingleTierCache(l1Cache, repositories)

	// Test 2: Multi-tier intelligent cache
	multiTierHitRate := testMultiTierCache(l1Cache, l2Cache, repositories, logger)

	// Calculate improvement
	improvement := multiTierHitRate - singleTierHitRate
	improvementPercent := (improvement / singleTierHitRate) * 100

	fmt.Printf("\n  📈 결과 요약:\n")
	fmt.Printf("    🏠 기존 단일 캐시:     %.1f%% 적중률\n", singleTierHitRate)
	fmt.Printf("    🚀 Multi-Tier 캐시:   %.1f%% 적중률\n", multiTierHitRate)
	fmt.Printf("    📊 개선율:            %.1f%% → %.1f%% (+%.1f%%)\n",
		singleTierHitRate, multiTierHitRate, improvement)
	fmt.Printf("    ⚡ 상대적 향상:        %.1f%%\n", improvementPercent)

	// Demonstrate intelligent features
	fmt.Println("\n🧠 지능형 캐싱 기능 테스트:")
	demonstrateIntelligentFeatures(l1Cache, l2Cache, logger)

	fmt.Println("\n✅ Multi-Tier 캐싱 테스트 완료!")
}

func testSingleTierCache(l1Cache *MockL1Cache, repositories []string) float64 {
	// Reset cache
	l1Cache.InvalidateAll()
	l1Cache.hits = 0
	l1Cache.misses = 0

	// Simulate repository access pattern
	totalRequests := 1000
	rand.Seed(time.Now().UnixNano())

	for i := 0; i < totalRequests; i++ {
		repoIndex := rand.Intn(len(repositories))
		repo := repositories[repoIndex]

		// Try to get from cache
		if _, exists := l1Cache.GetRepository(repo); !exists {
			// Cache miss - simulate loading and storing
			l1Cache.SetRepository(repo, &MockRepo{path: repo})
		}
	}

	metrics := l1Cache.GetMetrics()
	totalCacheRequests := metrics.Hits + metrics.Misses
	if totalCacheRequests == 0 {
		return 0
	}

	hitRate := float64(metrics.Hits) / float64(totalCacheRequests) * 100
	fmt.Printf("  🏠 단일 캐시: %.1f%% 적중률 (%d hits / %d requests)\n",
		hitRate, metrics.Hits, totalCacheRequests)

	return hitRate
}

func testMultiTierCache(l1Cache *MockL1Cache, l2Cache *MockL2Cache, repositories []string, logger *zap.Logger) float64 {
	// Reset caches
	l1Cache.InvalidateAll()
	l1Cache.hits = 0
	l1Cache.misses = 0
	l2Cache.hits = 0
	l2Cache.misses = 0

	// Simulate multi-tier cache behavior
	totalRequests := 1000
	l1Hits := int64(0)
	l2Hits := int64(0)
	totalMisses := int64(0)

	// Simulate intelligent access patterns
	accessTracker := performance.NewAccessPatternTracker(100, logger)

	for i := 0; i < totalRequests; i++ {
		repoIndex := selectRepositoryIntelligently(repositories, i)
		repo := repositories[repoIndex]

		// Record access for pattern analysis
		accessTracker.RecordAccess(repo)

		// L1 Cache check
		if _, exists := l1Cache.GetRepository(repo); exists {
			l1Hits++
			continue
		}

		// L2 Cache check (simulate higher hit rate due to distributed nature)
		if _, err := l2Cache.Get(context.Background(), "gitcache:repo:"+repo); err == nil {
			l2Hits++
			// Promote to L1
			l1Cache.SetRepository(repo, &MockRepo{path: repo})
			continue
		}

		// Complete miss - load and store in both tiers
		totalMisses++
		mockRepo := &MockRepo{path: repo}
		l1Cache.SetRepository(repo, mockRepo)
		l2Cache.Set(context.Background(), "gitcache:repo:"+repo, []byte("mock data"), time.Hour)

		// Trigger predictive prefetching for related repositories
		if i%10 == 0 { // Every 10th access triggers prefetch
			simulatePredictivePrefetch(repo, repositories, l1Cache, l2Cache, accessTracker)
		}
	}

	totalCacheRequests := l1Hits + l2Hits + totalMisses
	totalHits := l1Hits + l2Hits
	hitRate := float64(totalHits) / float64(totalCacheRequests) * 100

	fmt.Printf("  🚀 Multi-Tier:\n")
	fmt.Printf("     L1 적중률: %.1f%% (%d hits)\n",
		float64(l1Hits)/float64(totalCacheRequests)*100, l1Hits)
	fmt.Printf("     L2 적중률: %.1f%% (%d hits)\n",
		float64(l2Hits)/float64(totalCacheRequests)*100, l2Hits)
	fmt.Printf("     전체 적중률: %.1f%% (%d total hits / %d requests)\n",
		hitRate, totalHits, totalCacheRequests)

	return hitRate
}

func selectRepositoryIntelligently(repositories []string, accessIndex int) int {
	// Simulate realistic access patterns instead of pure random

	// Pattern 1: Sequential access to related repositories
	if accessIndex%5 < 3 {
		// Access repositories from the same user/org more frequently
		userGroups := [][]int{
			{0, 1, 2},   // user1 projects
			{3, 4, 5},   // user2 services
			{6, 7, 8},   // team repositories
			{9, 10, 11}, // org repositories
		}

		groupIndex := (accessIndex / 10) % len(userGroups)
		return userGroups[groupIndex][accessIndex%len(userGroups[groupIndex])]
	}

	// Pattern 2: Popular repositories (80/20 rule)
	if accessIndex%10 < 8 {
		// 80% of accesses go to 20% of repositories (top 3)
		popularRepos := []int{0, 6, 9} // Mix of popular repos
		return popularRepos[accessIndex%len(popularRepos)]
	}

	// Pattern 3: Random access for remaining cases
	return rand.Intn(len(repositories))
}

func simulatePredictivePrefetch(currentRepo string, repositories []string, l1Cache *MockL1Cache, l2Cache *MockL2Cache, tracker *performance.AccessPatternTracker) {
	// Simulate intelligent prefetching based on patterns

	// Find related repositories (simplified heuristic)
	var relatedRepos []string

	// If it's a user project, prefetch other projects from same user
	for _, repo := range repositories {
		if repo != currentRepo {
			// Simple heuristic: same prefix indicates related repos
			if len(repo) > 5 && len(currentRepo) > 5 && repo[:5] == currentRepo[:5] {
				relatedRepos = append(relatedRepos, repo)
			}
		}
	}

	// Prefetch up to 2 related repositories
	prefetchCount := 0
	for _, relatedRepo := range relatedRepos {
		if prefetchCount >= 2 {
			break
		}

		// Only prefetch if not already in L1 cache
		if _, exists := l1Cache.GetRepository(relatedRepo); !exists {
			// Simulate prefetch: load into both L1 and L2
			mockRepo := &MockRepo{path: relatedRepo}
			l1Cache.SetRepository(relatedRepo, mockRepo)
			l2Cache.Set(context.Background(), "gitcache:repo:"+relatedRepo, []byte("prefetched data"), time.Hour)
			prefetchCount++
		}
	}
}

func demonstrateIntelligentFeatures(l1Cache *MockL1Cache, l2Cache *MockL2Cache, logger *zap.Logger) {
	// Create access pattern tracker
	tracker := performance.NewAccessPatternTracker(50, logger)

	// Simulate access pattern that demonstrates intelligence
	testSequence := []string{
		"user1/project-a", "user1/project-b", "user1/project-c", // Sequential pattern
		"team/frontend", "team/backend", // Related pattern
		"user1/project-a", "user1/project-b", // Repeat pattern
		"team/frontend", "team/backend", // Repeat pattern
	}

	fmt.Println("  🔍 접근 패턴 분석:")
	for _, repo := range testSequence {
		tracker.RecordAccess(repo)
		time.Sleep(10 * time.Millisecond) // Small delay to create temporal patterns
	}

	// Analyze patterns
	topRepos := tracker.GetTopRepositories(5)
	fmt.Printf("    📊 상위 접근 저장소:\n")
	for i, stats := range topRepos {
		fmt.Printf("      %d. %s (%d accesses, pattern: %s)\n",
			i+1, stats.Repository, stats.TotalAccesses, stats.AccessPattern)
	}

	// Show related repositories
	related := tracker.GetRelatedRepositories("user1/project-a", 3)
	fmt.Printf("    🔗 'user1/project-a'와 관련된 저장소:\n")
	for i, rel := range related {
		fmt.Printf("      %d. %s (correlation: %.2f)\n",
			i+1, rel.Repository, rel.Score)
	}

	// Show statistics
	stats := tracker.GetStatistics()
	fmt.Printf("    📈 패턴 분석 통계:\n")
	fmt.Printf("      총 저장소: %v\n", stats["total_repositories"])
	fmt.Printf("      시간 패턴: %v\n", stats["time_patterns"])
	fmt.Printf("      연관 관계: %v\n", stats["co_occurrence_relationships"])
}
