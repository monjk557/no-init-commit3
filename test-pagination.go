package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/overview"
	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
)

func main() {
	fmt.Println("🚀 CollabOps 스마트 페이지네이션 테스트")
	fmt.Println("==========================================")

	// 기본 설정 - 현재 디렉토리의 절대 경로 구하기
	currentDir, err := os.Getwd()
	if err != nil {
		log.Fatal("Failed to get current directory:", err)
	}
	absPath, err := filepath.Abs(currentDir)
	if err != nil {
		log.Fatal("Failed to get absolute path:", err)
	}

	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: absPath, // 현재 디렉토리의 절대 경로를 테스트 저장소로 사용
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
	}

	// 로거 초기화
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		log.Fatal("Failed to create logger:", err)
	}

	// PathConfig 초기화
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		log.Fatal("Failed to create path config:", err)
	}

	// RepoManager 초기화
	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	if err != nil {
		log.Fatal("Failed to create repository manager:", err)
	}

	// OverviewService 초기화 (nil for database since this is just a test)
	service, err := overview.NewRepositoryOverviewService(logger, pathConfig, repoManager, nil)
	if err != nil {
		log.Fatal("Failed to create overview service:", err)
	}

	fmt.Println("✅ 서비스 초기화 완료")

	// Git 저장소 열기 (현재 collabops-git-executor 저장소)
	gitRepo, err := gogit.PlainOpen(".")
	if err != nil {
		log.Fatal("Failed to open git repository:", err)
	}

	fmt.Println("✅ Git 저장소 열기 완료")

	// 페이지네이션 테스트
	ctx := context.Background()

	// 테스트 요청 생성
	req := &overview.TreeDirectoryRequest{
		Owner:   "test-org",
		Repo:    "collabops-git-executor",
		Ref:     "main",
		Path:    "",
		Page:    1,
		PerPage: 10, // 10개씩 페이지네이션
	}

	fmt.Printf("📊 테스트 요청: Page=%d, PerPage=%d\n", req.Page, req.PerPage)

	// 성능 측정 시작
	startTime := time.Now()

	// 페이지네이션 적용된 트리 조회
	response, err := service.GetTreeWithCommitPaginated(ctx, gitRepo, req, "http://localhost:8090")
	if err != nil {
		log.Fatal("Failed to get paginated tree:", err)
	}

	duration := time.Since(startTime)

	fmt.Println("✅ 페이지네이션 조회 성공!")
	fmt.Printf("⏱️  응답 시간: %v\n", duration)
	fmt.Printf("📁 반환된 파일 수: %d\n", len(response.Tree))

	if response.Pagination != nil {
		fmt.Printf("📄 페이지네이션 정보:\n")
		fmt.Printf("   - 현재 페이지: %d\n", response.Pagination.Page)
		fmt.Printf("   - 페이지당 항목: %d\n", response.Pagination.PerPage)
		fmt.Printf("   - 전체 파일 수: %d\n", response.Pagination.TotalFiles)
		fmt.Printf("   - 전체 페이지 수: %d\n", response.Pagination.TotalPages)
		fmt.Printf("   - 다음 페이지: %v\n", response.Pagination.HasNextPage)
	}

	fmt.Println("\n📋 반환된 파일들:")
	for i, file := range response.Tree {
		fmt.Printf("   %d. %s (%s)\n", i+1, file.Name, file.Type)
		if file.LatestCommit != nil {
			fmt.Printf("      커밋: %s\n", file.LatestCommit.SHA[:8])
		}
	}

	// 성공 기준 확인
	fmt.Println("\n🎯 Week 1 목표 달성 확인:")

	if duration < 500*time.Millisecond {
		fmt.Printf("✅ 응답시간 목표 달성: %v < 500ms\n", duration)
	} else {
		fmt.Printf("❌ 응답시간 개선 필요: %v >= 500ms\n", duration)
	}

	if response.Pagination != nil {
		fmt.Printf("✅ 페이지네이션 적용 성공\n")
	} else {
		fmt.Printf("❌ 페이지네이션 정보 누락\n")
	}

	if len(response.Tree) == req.PerPage || (response.Pagination != nil && response.Pagination.Page == response.Pagination.TotalPages) {
		fmt.Printf("✅ 페이지 크기 제한 적용 성공\n")
	} else {
		fmt.Printf("❌ 페이지 크기 제한 미적용\n")
	}

	fmt.Println("\n🎉 Phase 2 Week 1 테스트 완료!")
}
