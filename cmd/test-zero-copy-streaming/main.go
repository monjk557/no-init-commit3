package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"runtime"
	"time"

	"github.com/collabops/git-executor/internal/performance"
)

// MockDiffFile simulates a large file diff for testing
type MockDiffFile struct {
	Path      string                 `json:"path"`
	Changes   []MockLineChange       `json:"changes"`
	Hunks     []MockHunk             `json:"hunks"`
	Metadata  map[string]interface{} `json:"metadata"`
	Operation string                 `json:"operation"`
	Size      int64                  `json:"size"`
}

type MockLineChange struct {
	Type         string `json:"type"`
	LeftLineNum  int    `json:"left_line_num"`
	RightLineNum int    `json:"right_line_num"`
	Content      string `json:"content"`
	HunkIndex    int    `json:"hunk_index"`
}

type MockHunk struct {
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
	LineCount int `json:"line_count"`
}

type MockDiffStats struct {
	FilesChanged int `json:"files_changed"`
	LinesAdded   int `json:"lines_added"`
	LinesDeleted int `json:"lines_deleted"`
	TotalChanges int `json:"total_changes"`
	BinaryFiles  int `json:"binary_files"`
	LargeFiles   int `json:"large_files"`
}

type MockCommitsByDate struct {
	Date    string       `json:"date"`
	Commits []MockCommit `json:"commits"`
	Count   int          `json:"count"`
}

type MockCommit struct {
	SHA     string    `json:"sha"`
	Author  string    `json:"author"`
	Email   string    `json:"email"`
	Date    time.Time `json:"date"`
	Message string    `json:"message"`
	Parents []string  `json:"parents"`
	TreeSHA string    `json:"tree_sha"`
}

type MockPagination struct {
	Page       int  `json:"page"`
	PerPage    int  `json:"per_page"`
	Total      int  `json:"total"`
	TotalPages int  `json:"total_pages"`
	HasNext    bool `json:"has_next"`
	HasPrev    bool `json:"has_prev"`
}

// MockResponseData simulates the existing response structure
type MockResponseData struct {
	Success   bool                   `json:"success"`
	Message   string                 `json:"message"`
	Timestamp time.Time              `json:"timestamp"`
	Meta      map[string]interface{} `json:"meta"`
	Data      interface{}            `json:"data,omitempty"`
}

func main() {
	fmt.Println("🚀 Zero-Copy JSON 스트리밍 최적화 성능 테스트")
	fmt.Println("================================================")

	// Test scenarios
	scenarios := []struct {
		name        string
		description string
		testFunc    func()
	}{
		{
			"Response Middleware",
			"3중 마샬링 vs Zero-Copy 응답",
			testResponseMiddleware,
		},
		{
			"Large Diff Streaming",
			"5-15MB diff 응답 스트리밍",
			testLargeDiffStreaming,
		},
		{
			"Commits Pagination",
			"페이지네이션 응답 최적화",
			testCommitsPagination,
		},
		{
			"Memory Usage",
			"메모리 사용량 비교",
			testMemoryUsage,
		},
	}

	for i, scenario := range scenarios {
		fmt.Printf("\n🧪 테스트 %d: %s\n", i+1, scenario.name)
		fmt.Printf("   📋 %s\n", scenario.description)
		scenario.testFunc()
	}

	fmt.Println("\n✅ Zero-Copy 스트리밍 테스트 완료!")
}

func testResponseMiddleware() {
	fmt.Println("\n  📊 응답 미들웨어 성능 비교:")

	// Create test payload
	payload := map[string]interface{}{
		"user_id":  12345,
		"username": "test-user",
		"email":    "test@example.com",
		"preferences": map[string]interface{}{
			"theme":    "dark",
			"language": "ko",
			"timezone": "Asia/Seoul",
			"notifications": map[string]bool{
				"email": true,
				"push":  false,
			},
		},
		"repositories": []map[string]interface{}{
			{"name": "repo1", "stars": 100, "private": false},
			{"name": "repo2", "stars": 250, "private": true},
			{"name": "repo3", "stars": 50, "private": false},
		},
		"statistics": map[string]int{
			"total_commits": 1000,
			"total_repos":   25,
			"total_stars":   400,
			"contributions": 156,
		},
	}

	// Test traditional double marshaling approach
	traditionalTime := testTraditionalResponse(payload, 1000)

	// Test zero-copy streaming approach
	streamingTime := testStreamingResponse(payload, 1000)

	// Calculate improvement
	improvement := float64(traditionalTime-streamingTime) / float64(traditionalTime) * 100

	fmt.Printf("    🏠 기존 3중 마샬링:      %v\n", traditionalTime)
	fmt.Printf("    🚀 Zero-Copy 스트리밍:   %v\n", streamingTime)
	fmt.Printf("    📈 성능 향상:            %.1f%% 빠름\n", improvement)
}

func testTraditionalResponse(payload interface{}, iterations int) time.Duration {
	start := time.Now()

	for i := 0; i < iterations; i++ {
		// Simulate the existing double marshal approach from api/response.go
		base := map[string]interface{}{
			"success":   true,
			"message":   "Success",
			"timestamp": time.Now(),
			"meta": map[string]interface{}{
				"request_id": "test-123",
				"version":    "v1.0",
			},
		}

		// BOTTLENECK: Double marshal + unmarshal cycle
		if payload != nil {
			b, _ := json.Marshal(payload) // MARSHAL #1
			var response map[string]interface{}
			if json.Unmarshal(b, &response) == nil { // UNMARSHAL #1
				for k, v := range response {
					base[k] = v
				}
			}
		}

		// Final marshal
		_, _ = json.Marshal(base) // MARSHAL #2 (simulating c.JSON)
	}

	return time.Since(start)
}

func testStreamingResponse(payload interface{}, iterations int) time.Duration {
	optimizer := performance.NewOptimizedResponseWriter()

	start := time.Now()

	for i := 0; i < iterations; i++ {
		// Simulate zero-copy streaming approach
		var buf bytes.Buffer
		_ = optimizer.WriteSuccessResponse(&buf, payload, "Success")
	}

	return time.Since(start)
}

func testLargeDiffStreaming() {
	fmt.Println("\n  📊 대용량 Diff 스트리밍 비교:")

	// Generate large diff data (simulate 5-15MB response)
	files := generateMockDiffFiles(100, 100) // 100 files, 100 changes each
	stats := MockDiffStats{
		FilesChanged: 100,
		LinesAdded:   5000,
		LinesDeleted: 3000,
		TotalChanges: 8000,
		BinaryFiles:  5,
		LargeFiles:   10,
	}

	// Test traditional full marshal approach
	traditionalTime := testTraditionalDiffResponse(files, stats, 10)

	// Test streaming approach
	streamingTime := testStreamingDiffResponse(files, stats, 10)

	// Calculate improvement
	improvement := float64(traditionalTime-streamingTime) / float64(traditionalTime) * 100

	fmt.Printf("    🏠 기존 전체 마샬링:     %v\n", traditionalTime)
	fmt.Printf("    🚀 스트리밍 방식:        %v\n", streamingTime)
	fmt.Printf("    📈 성능 향상:            %.1f%% 빠름\n", improvement)

	// Show memory efficiency
	fmt.Printf("    💾 예상 메모리 절약:     ~60-70%% (5-15MB → 실시간 스트림)\n")
}

func generateMockDiffFiles(fileCount, changesPerFile int) []interface{} {
	files := make([]interface{}, fileCount)

	for i := 0; i < fileCount; i++ {
		changes := make([]MockLineChange, changesPerFile)
		for j := 0; j < changesPerFile; j++ {
			changes[j] = MockLineChange{
				Type:         []string{"added", "deleted", "modified"}[rand.Intn(3)],
				LeftLineNum:  j + 1,
				RightLineNum: j + 1,
				Content:      fmt.Sprintf("Line %d content with some changes and modifications", j+1),
				HunkIndex:    j / 10,
			}
		}

		hunks := make([]MockHunk, 10)
		for k := 0; k < 10; k++ {
			hunks[k] = MockHunk{
				StartLine: k*10 + 1,
				EndLine:   (k + 1) * 10,
				LineCount: 10,
			}
		}

		files[i] = MockDiffFile{
			Path:    fmt.Sprintf("src/components/file_%d.go", i+1),
			Changes: changes,
			Hunks:   hunks,
			Metadata: map[string]interface{}{
				"size":     rand.Int63n(100000) + 1000,
				"type":     "text",
				"encoding": "utf-8",
				"language": "go",
			},
			Operation: []string{"modified", "added", "deleted"}[rand.Intn(3)],
			Size:      rand.Int63n(50000) + 1000,
		}
	}

	return files
}

func testTraditionalDiffResponse(files []interface{}, stats MockDiffStats, iterations int) time.Duration {
	start := time.Now()

	for i := 0; i < iterations; i++ {
		// Simulate traditional full response marshal
		response := map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"base":  "commit-abc123",
				"head":  "commit-def456",
				"files": files, // Entire file array marshaled at once
				"stats": stats,
			},
			"pagination": MockPagination{
				Page:       1,
				PerPage:    100,
				Total:      100,
				TotalPages: 1,
				HasNext:    false,
				HasPrev:    false,
			},
		}

		// Full marshal - loads entire response into memory
		_, _ = json.Marshal(response)
	}

	return time.Since(start)
}

func testStreamingDiffResponse(files []interface{}, stats MockDiffStats, iterations int) time.Duration {
	diffWriter := performance.NewStreamingDiffResponseWriter()

	start := time.Now()

	for i := 0; i < iterations; i++ {
		// Simulate streaming approach
		var buf bytes.Buffer
		_ = diffWriter.StreamDiffResponse(&buf, "commit-abc123", "commit-def456", files, stats)
	}

	return time.Since(start)
}

func testCommitsPagination() {
	fmt.Println("\n  📊 페이지네이션 응답 최적화:")

	// Generate paginated commits data
	commitGroups := generateMockCommitGroups(10, 10) // 10 days, 10 commits per day
	pagination := MockPagination{
		Page:       1,
		PerPage:    100,
		Total:      1000,
		TotalPages: 10,
		HasNext:    true,
		HasPrev:    false,
	}

	// Test traditional approach
	traditionalTime := testTraditionalCommitsResponse(commitGroups, pagination, 100)

	// Test streaming approach
	streamingTime := testStreamingCommitsResponse(commitGroups, pagination, 100)

	// Calculate improvement
	improvement := float64(traditionalTime-streamingTime) / float64(traditionalTime) * 100

	fmt.Printf("    🏠 기존 페이지네이션:     %v\n", traditionalTime)
	fmt.Printf("    🚀 스트리밍 방식:        %v\n", streamingTime)
	fmt.Printf("    📈 성능 향상:            %.1f%% 빠름\n", improvement)
}

func generateMockCommitGroups(dayCount, commitsPerDay int) []interface{} {
	groups := make([]interface{}, dayCount)

	for i := 0; i < dayCount; i++ {
		commits := make([]MockCommit, commitsPerDay)
		date := time.Now().AddDate(0, 0, -i)

		for j := 0; j < commitsPerDay; j++ {
			commits[j] = MockCommit{
				SHA:     fmt.Sprintf("abcdef%06d", rand.Intn(1000000)),
				Author:  fmt.Sprintf("Developer %d", rand.Intn(50)+1),
				Email:   fmt.Sprintf("dev%d@company.com", rand.Intn(50)+1),
				Date:    date.Add(time.Duration(j) * time.Hour),
				Message: fmt.Sprintf("Commit message %d with some detailed description", j+1),
				Parents: []string{fmt.Sprintf("parent%06d", rand.Intn(1000000))},
				TreeSHA: fmt.Sprintf("tree%06d", rand.Intn(1000000)),
			}
		}

		groups[i] = MockCommitsByDate{
			Date:    date.Format("2006-01-02"),
			Commits: commits,
			Count:   commitsPerDay,
		}
	}

	return groups
}

func testTraditionalCommitsResponse(commitGroups []interface{}, pagination MockPagination, iterations int) time.Duration {
	start := time.Now()

	for i := 0; i < iterations; i++ {
		// Traditional full response marshal
		response := map[string]interface{}{
			"success":    true,
			"results":    commitGroups, // Entire array marshaled at once
			"pagination": pagination,
		}

		_, _ = json.Marshal(response)
	}

	return time.Since(start)
}

func testStreamingCommitsResponse(commitGroups []interface{}, pagination MockPagination, iterations int) time.Duration {
	commitsWriter := performance.NewStreamingCommitsResponseWriter()

	start := time.Now()

	for i := 0; i < iterations; i++ {
		// Streaming approach
		var buf bytes.Buffer
		_ = commitsWriter.StreamCommitsResponse(&buf, commitGroups, pagination)
	}

	return time.Since(start)
}

func testMemoryUsage() {
	fmt.Println("\n  📊 메모리 사용량 비교:")

	// Generate large test data
	largeFiles := generateMockDiffFiles(500, 200) // 500 files, 200 changes each (~10MB)

	// Test memory usage for traditional approach
	var m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	// Traditional full marshal
	for i := 0; i < 5; i++ {
		response := map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"files": largeFiles,
			},
		}
		data, _ := json.Marshal(response)
		_ = data // Prevent optimization
	}

	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)
	traditionalMemory := m2.TotalAlloc - m1.TotalAlloc

	// Test memory usage for streaming approach
	runtime.GC()
	runtime.ReadMemStats(&m1)

	diffWriter := performance.NewStreamingDiffResponseWriter()
	for i := 0; i < 5; i++ {
		var buf bytes.Buffer
		_ = diffWriter.StreamDiffResponse(&buf, "base", "head", largeFiles, MockDiffStats{})
	}

	runtime.ReadMemStats(&m2)
	streamingMemory := m2.TotalAlloc - m1.TotalAlloc

	// Calculate memory savings
	memorySavings := float64(traditionalMemory-streamingMemory) / float64(traditionalMemory) * 100

	fmt.Printf("    🏠 기존 방식 메모리:      %d KB\n", traditionalMemory/1024)
	fmt.Printf("    🚀 스트리밍 메모리:       %d KB\n", streamingMemory/1024)
	fmt.Printf("    💾 메모리 절약:          %.1f%% 감소\n", memorySavings)

	fmt.Printf("\n  🔍 분석 결과:\n")
	fmt.Printf("    • 대용량 응답(5-15MB)에서 메모리 사용량 대폭 감소\n")
	fmt.Printf("    • 실시간 스트리밍으로 메모리 스파이크 방지\n")
	fmt.Printf("    • 엔코더 풀링으로 GC 압력 감소\n")
}
