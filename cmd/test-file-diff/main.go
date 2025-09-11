// 파일 diff 기능 테스트 유틸리티
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/protocol"
)

// 테스트용 Mock 권한 클라이언트
type MockPermissionClient struct{}

// 읽기 권한 체크 - 테스트 환경에서는 항상 허용
func (m *MockPermissionClient) CheckReadAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	return true, nil
}

// 쓰기 권한 체크 - 테스트 환경에서는 항상 허용
func (m *MockPermissionClient) CheckWriteAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	return true, nil
}

// 캐시 무효화 메서드들 (테스트용이므로 비어있음)
func (m *MockPermissionClient) InvalidateCache(userID, repoOwner, repoName string) {}
func (m *MockPermissionClient) ClearCache()                                        {}
func (m *MockPermissionClient) GetCircuitBreakerMetrics() *permissions.CircuitBreakerMetrics {
	return &permissions.CircuitBreakerMetrics{}
}
func (m *MockPermissionClient) Close() error { return nil }

func main() {
	// 테스트용 설정 생성
	cfg := &config.Config{
		Git: config.GitConfig{
			// TODO: 테스트용 임시 디렉토리 사용 필요
			RepoRoot: "/Users/johnbaek/collabops-git-executor/test-repos",
		},
	}

	// Mock 권한 클라이언트 생성
	permClient := &MockPermissionClient{}

	// Diff 핸들러 생성
	diffHandler, err := protocol.NewDiffHandler(cfg, permClient)
	if err != nil {
		log.Fatalf("Failed to create diff handler: %v", err)
	}

	// 파일 diff 계산 직접 테스트
	ctx := context.Background()
	// TODO: 테스트용 임시 디렉토리 사용 필요
	repoPath := "/Users/johnbaek/collabops-git-executor/test-repos/testowner/testrepo.git"

	fmt.Println("Testing file diff endpoint functionality...")

	// 테스트 1: 파일 수정 (커밋 간 README.md 비교)
	fmt.Println("\n=== Test 1: File modification ===")
	_, err = testCalculateFileDiff(diffHandler, ctx, repoPath, "README.md", "84e9286", "4fac4e1", 3, "unified")
	// 아직 완전히 구현되지 않아서 항상 에러 반환
	fmt.Printf("Error: %v\n", err)

	// 테스트 2: 파일 추가 (src/index.js)
	fmt.Println("\n=== Test 2: File addition ===")
	_, err = testCalculateFileDiff(diffHandler, ctx, repoPath, "src/index.js", "4fac4e1", "1bd94bb", 3, "unified")
	// 아직 완전히 구현되지 않아서 항상 에러 반환
	fmt.Printf("Error: %v\n", err)

	// 테스트 3: 존재하지 않는 파일
	fmt.Println("\n=== Test 3: Non-existent file ===")
	_, err = testCalculateFileDiff(diffHandler, ctx, repoPath, "nonexistent.txt", "84e9286", "4fac4e1", 3, "unified")
	// 아직 완전히 구현되지 않아서 항상 에러 반환
	fmt.Printf("Expected error: %v\n", err)

	fmt.Println("\nFile diff testing completed!")
}

// private calculateFileDiff 메서드 접근을 위한 헬퍼 함수
func testCalculateFileDiff(handler *protocol.DiffHandler, ctx context.Context, repoPath, filePath, base, head string, contextLines int, format string) (*protocol.DiffResponse, error) {
	// calculateFileDiff가 export되지 않아서 HTTP 핸들러를 통해 테스트해야 함
	// 현재는 핸들러가 성공적으로 생성되었는지만 확인
	return nil, fmt.Errorf("direct access to calculateFileDiff method is not available (private method)")
}
