package main

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/collabops/git-executor/internal/performance"
)

func main() {
	fmt.Println("🎯 Zero-Copy JSON 응답 최적화 핵심 테스트")
	fmt.Println("==========================================")

	analytics := performance.NewPerformanceAnalytics()

	// Test scenarios with different payload sizes and complexities
	scenarios := []struct {
		name        string
		description string
		payload     interface{}
		iterations  int
	}{
		{
			"Small Response",
			"사용자 프로필 (1KB)",
			generateSmallPayload(),
			2000,
		},
		{
			"Medium Response",
			"저장소 정보 (10KB)",
			generateMediumPayload(),
			1000,
		},
		{
			"Large Response",
			"diff 메타데이터 (100KB)",
			generateLargePayload(),
			100,
		},
		{
			"Complex Nested",
			"중첩된 구조 (50KB)",
			generateComplexNestedPayload(),
			200,
		},
	}

	fmt.Printf("\n📊 핵심 최적화 분석: 3중 마샬링 → 단일 마샬링\n")
	fmt.Printf("========================================================\n")

	totalTraditionalTime := time.Duration(0)
	totalOptimizedTime := time.Duration(0)

	for i, scenario := range scenarios {
		fmt.Printf("\n🧪 테스트 %d: %s\n", i+1, scenario.name)
		fmt.Printf("   📋 %s (%d iterations)\n", scenario.description, scenario.iterations)

		result := analytics.AnalyzeResponseOptimization(scenario.payload, scenario.iterations)

		fmt.Printf("   🏠 기존 3중 마샬링:    %v\n", result.TraditionalTime)
		fmt.Printf("   🚀 단일 마샬링:        %v\n", result.OptimizedTime)
		fmt.Printf("   📈 성능 향상:          %.1f%% 빠름\n", result.ImprovementPercent)
		fmt.Printf("   💾 페이로드 크기:      %d bytes\n", result.PayloadSize)
		fmt.Printf("   📊 영향도:             %s\n", result.GetOptimizationImpact())

		totalTraditionalTime += result.TraditionalTime
		totalOptimizedTime += result.OptimizedTime
	}

	// Overall summary
	overallImprovement := float64(totalTraditionalTime-totalOptimizedTime) / float64(totalTraditionalTime) * 100

	fmt.Printf("\n🎯 종합 결과\n")
	fmt.Printf("=================\n")
	fmt.Printf("📊 전체 기존 시간:      %v\n", totalTraditionalTime)
	fmt.Printf("🚀 전체 최적화 시간:    %v\n", totalOptimizedTime)
	fmt.Printf("⚡ 종합 성능 향상:       %.1f%% 빠름\n", overallImprovement)

	// Calculate theoretical impact for high-traffic scenario
	requestsPerSecond := 1000
	dailyRequests := requestsPerSecond * 86400 // 24 hours

	traditionalDailyTime := totalTraditionalTime * time.Duration(dailyRequests/3300) // Scaled
	optimizedDailyTime := totalOptimizedTime * time.Duration(dailyRequests/3300)
	dailySavings := traditionalDailyTime - optimizedDailyTime

	fmt.Printf("\n💡 실제 영향 분석 (1,000 req/sec 기준)\n")
	fmt.Printf("=====================================\n")
	fmt.Printf("📈 일일 요청 처리:      %d requests\n", dailyRequests)
	fmt.Printf("⏱️  기존 일일 처리 시간: %v\n", traditionalDailyTime)
	fmt.Printf("🚀 최적화 일일 시간:    %v\n", optimizedDailyTime)
	fmt.Printf("💰 일일 시간 절약:      %v\n", dailySavings)
	fmt.Printf("🏆 연간 시간 절약:      %v\n", dailySavings*365)

	fmt.Printf("\n🔍 핵심 최적화 요약\n")
	fmt.Printf("===================\n")
	fmt.Printf("✅ 3중 마샬링 → 단일 마샬링 (핵심)\n")
	fmt.Printf("✅ 중간 버퍼 할당 제거\n")
	fmt.Printf("✅ 직접 스트림 인코딩\n")
	fmt.Printf("✅ JSON 객체 플래튼닝 최적화\n")

	fmt.Printf("\n⚡ 모든 API 엔드포인트에 적용 가능한 핵심 최적화 완료!\n")
}

func generateSmallPayload() interface{} {
	return map[string]interface{}{
		"user_id":    12345,
		"username":   "test-user",
		"email":      "test@example.com",
		"active":     true,
		"created_at": time.Now().Format(time.RFC3339),
	}
}

func generateMediumPayload() interface{} {
	repositories := make([]map[string]interface{}, 20)
	for i := 0; i < 20; i++ {
		repositories[i] = map[string]interface{}{
			"id":          i + 1,
			"name":        fmt.Sprintf("repository-%d", i+1),
			"description": fmt.Sprintf("Description for repository %d with some details", i+1),
			"stars":       rand.Intn(1000),
			"forks":       rand.Intn(100),
			"language":    []string{"Go", "JavaScript", "Python", "Java"}[rand.Intn(4)],
			"private":     rand.Intn(2) == 1,
			"created_at":  time.Now().AddDate(0, 0, -rand.Intn(365)).Format(time.RFC3339),
			"updated_at":  time.Now().AddDate(0, 0, -rand.Intn(30)).Format(time.RFC3339),
		}
	}

	return map[string]interface{}{
		"user": map[string]interface{}{
			"id":       12345,
			"username": "developer",
			"email":    "dev@example.com",
		},
		"repositories": repositories,
		"stats": map[string]interface{}{
			"total_repos":   20,
			"total_stars":   rand.Intn(5000),
			"total_forks":   rand.Intn(500),
			"contributions": rand.Intn(1000),
		},
	}
}

func generateLargePayload() interface{} {
	files := make([]map[string]interface{}, 100)
	for i := 0; i < 100; i++ {
		hunks := make([]map[string]interface{}, 10)
		for j := 0; j < 10; j++ {
			hunks[j] = map[string]interface{}{
				"start_line": j*10 + 1,
				"end_line":   (j + 1) * 10,
				"line_count": 10,
				"context":    fmt.Sprintf("Context for hunk %d", j+1),
			}
		}

		files[i] = map[string]interface{}{
			"path":      fmt.Sprintf("src/components/file_%d.go", i+1),
			"operation": []string{"modified", "added", "deleted"}[rand.Intn(3)],
			"size":      rand.Int63n(100000) + 1000,
			"hunks":     hunks,
			"metadata": map[string]interface{}{
				"type":     "text",
				"encoding": "utf-8",
				"language": "go",
				"binary":   false,
			},
		}
	}

	return map[string]interface{}{
		"diff": map[string]interface{}{
			"base":  "commit-abc123",
			"head":  "commit-def456",
			"files": files,
			"stats": map[string]interface{}{
				"files_changed": 100,
				"lines_added":   rand.Intn(5000) + 1000,
				"lines_deleted": rand.Intn(3000) + 500,
				"binary_files":  5,
			},
		},
		"pagination": map[string]interface{}{
			"page":        1,
			"per_page":    100,
			"total":       100,
			"total_pages": 1,
		},
	}
}

func generateComplexNestedPayload() interface{} {
	commitGroups := make([]map[string]interface{}, 10)
	for i := 0; i < 10; i++ {
		commits := make([]map[string]interface{}, 15)
		for j := 0; j < 15; j++ {
			commits[j] = map[string]interface{}{
				"sha":     fmt.Sprintf("commit%06d", rand.Intn(1000000)),
				"author":  fmt.Sprintf("Developer %d", rand.Intn(20)+1),
				"email":   fmt.Sprintf("dev%d@company.com", rand.Intn(20)+1),
				"date":    time.Now().AddDate(0, 0, -i).Add(time.Duration(j) * time.Hour).Format(time.RFC3339),
				"message": fmt.Sprintf("Commit message %d with detailed description and context", j+1),
				"parents": []string{fmt.Sprintf("parent%06d", rand.Intn(1000000))},
				"stats": map[string]interface{}{
					"additions": rand.Intn(100),
					"deletions": rand.Intn(50),
					"files":     rand.Intn(10) + 1,
				},
			}
		}

		commitGroups[i] = map[string]interface{}{
			"date":    time.Now().AddDate(0, 0, -i).Format("2006-01-02"),
			"commits": commits,
			"count":   15,
			"stats": map[string]interface{}{
				"total_additions": rand.Intn(1000),
				"total_deletions": rand.Intn(500),
				"unique_authors":  rand.Intn(5) + 1,
			},
		}
	}

	return map[string]interface{}{
		"results": commitGroups,
		"summary": map[string]interface{}{
			"total_commits":   150,
			"date_range":      "10 days",
			"unique_authors":  rand.Intn(20) + 1,
			"total_files":     rand.Intn(500) + 100,
			"total_additions": rand.Intn(10000) + 5000,
			"total_deletions": rand.Intn(5000) + 2000,
		},
		"pagination": map[string]interface{}{
			"page":        1,
			"per_page":    10,
			"total":       50,
			"total_pages": 5,
			"has_next":    true,
			"has_prev":    false,
		},
	}
}
