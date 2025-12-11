// 리포지토리 경로 관리 테스트 유틸리티
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/collabops/git-executor/internal/repositories"
)

// 테스트 7: 패키지 통합 기능 테스트
func testPackageIntegration() {
	fmt.Println("\n🔧 Test 7: Package Integration Features")

	// 테스트용 임시 디렉토리 생성
	tempDir, err := os.MkdirTemp("", "integration-test-")
	if err != nil {
		fmt.Printf("❌ Failed to create temp dir: %v\n", err)
		return
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	// 테스트 설정 생성
	testConfig, err := repositories.NewPathConfig(tempDir)
	if err != nil {
		fmt.Printf("❌ Failed to create test config: %v\n", err)
		return
	}

	// 전역 설정으로 지정
	err = repositories.SetGlobalConfig(testConfig)
	if err != nil {
		fmt.Printf("❌ Failed to set global config: %v\n", err)
		return
	}

	// 고수준 통합 함수 테스트
	fmt.Println("\n🚀 High-Level Integration Functions:")

	// 1. 리포지토리 경로 가져오기 및 검증
	path, err := repositories.GetAndValidateRepoPath(testConfig, "integration", "test")
	if err != nil {
		fmt.Printf("❌ GetAndValidateRepoPath failed: %v\n", err)
	} else {
		fmt.Printf("✅ GetAndValidateRepoPath: %s\n", path)
	}

	// 2. 테스트용 Mock 리포지토리 생성
	repoPath := filepath.Join(tempDir, "integration", "valid-repo.git")
	err = createMockBareRepo(repoPath)
	if err != nil {
		fmt.Printf("❌ Failed to create mock repo: %v\n", err)
		return
	}

	// 3. 리포지토리 접근 권한 확인
	verifiedPath, err := repositories.VerifyRepositoryAccess(testConfig, "integration", "valid-repo")
	if err != nil {
		fmt.Printf("❌ VerifyRepositoryAccess failed: %v\n", err)
	} else {
		fmt.Printf("✅ VerifyRepositoryAccess: %s\n", verifiedPath)
	}

	// 4. 리포지토리 정보 조회
	info, err := repositories.GetRepositoryInfo(testConfig, "integration", "valid-repo")
	if err != nil {
		fmt.Printf("❌ GetRepositoryInfo failed: %v\n", err)
	} else {
		fmt.Printf("✅ Repository Info:\n")
		fmt.Printf("   Owner: %s\n", info.Owner)
		fmt.Printf("   Name: %s\n", info.Name)
		fmt.Printf("   Type: %s\n", info.Type)
		fmt.Printf("   Exists: %v\n", info.Exists)
		fmt.Printf("   IsValid: %v\n", info.IsValid)
		fmt.Printf("   IsAccessible: %v\n", info.IsAccessible)
	}

	// 기본 설정 함수 테스트
	fmt.Println("\n🌐 Default Configuration Functions:")

	// 전역 설정 사용
	defaultPath, err := repositories.GetAndValidateRepoPathWithDefault("global", "test")
	if err != nil {
		fmt.Printf("❌ GetAndValidateRepoPathWithDefault failed: %v\n", err)
	} else {
		fmt.Printf("✅ GetAndValidateRepoPathWithDefault: %s\n", defaultPath)
	}

	// 배치 작업 테스트
	fmt.Println("\n📦 Batch Operations:")

	// 여러 테스트 리포지토리 생성
	_ = createMockNonBareRepo(filepath.Join(tempDir, "batch", "repo1.git"))

	repos := []struct{ Owner, Repo string }{
		{"integration", "valid-repo"},
		{"batch", "repo1"},
		{"missing", "repo"},
		{"../malicious", "attack"},
	}

	batchInfo := repositories.GetBatchRepositoryInfo(testConfig, repos)
	fmt.Printf("✅ Batch Repository Info:\n")
	fmt.Printf("   Total: %d\n", batchInfo.TotalCount)
	fmt.Printf("   Valid: %d\n", batchInfo.ValidCount)
	fmt.Printf("   Errors: %d\n", batchInfo.ErrorCount)

	for i, repo := range batchInfo.Repositories {
		fmt.Printf("   [%d] %s/%s: exists=%v, valid=%v, accessible=%v\n",
			i+1, repo.Owner, repo.Name, repo.Exists, repo.IsValid, repo.IsAccessible)
	}

	// 에러 처리 테스트
	fmt.Println("\n🚫 Error Handling:")

	// 잘못된 경로 시도
	_, err = repositories.GetAndValidateRepoPath(testConfig, "../invalid", "repo")
	if err != nil {
		fmt.Printf("✅ CORRECTLY BLOCKED invalid path: %v\n", err)
	} else {
		fmt.Printf("❌ SECURITY ISSUE: Invalid path was allowed!\n")
	}

	// 존재하지 않는 리포지토리 접근 시도
	_, err = repositories.VerifyRepositoryAccess(testConfig, "missing", "repo")
	if err != nil {
		fmt.Printf("✅ CORRECTLY BLOCKED missing repo access: %v\n", err)
	} else {
		fmt.Printf("❌ SECURITY ISSUE: Missing repo access was allowed!\n")
	}
}

func main() {
	fmt.Println("🧪 Manual Testing: Repository Path Management")
	fmt.Println(strings.Repeat("=", 50))

	// Test 1: Path Configuration
	fmt.Println("\n📁 Test 1: Path Configuration")
	config, err := repositories.NewPathConfig("/var/lib/git/repos")
	if err != nil {
		log.Fatalf("❌ Failed to create path config: %v", err)
	}
	fmt.Printf("✅ Created config with root: %s\n", config.GetRepoRoot())

	// Test 2: Valid Path Generation
	fmt.Println("\n🛣️  Test 2: Valid Path Generation")
	testCases := []struct {
		owner, repo string
		description string
	}{
		{"jaemin", "collabops", "Standard repository"},
		{"user-name", "project-123", "Hyphens and numbers"},
		{"org_name", "complex_project", "Underscores"},
	}

	for _, tc := range testCases {
		path, err := repositories.GetRepoPath(config, tc.owner, tc.repo)
		if err != nil {
			fmt.Printf("❌ %s: %v\n", tc.description, err)
		} else {
			fmt.Printf("✅ %s: %s\n", tc.description, path)
		}
	}

	// Test 3: Path Validation
	fmt.Println("\n🔒 Test 3: Path Validation (Security)")
	validPaths := []string{
		"/var/lib/git/repos/user/project.git",
		"/var/lib/git/repos/organization/complex-name.git",
	}

	invalidPaths := []string{
		"/tmp/malicious/repo.git",                // Outside root
		"/var/lib/git/repos/../../../etc/passwd", // Path traversal
		"/var/lib/git/repos",                     // Repository root
		"/var/lib/git/repos/user",                // Missing structure
		"/var/lib/git/repos/user/project",        // Missing .git
	}

	fmt.Println("\n✅ Valid paths:")
	for _, path := range validPaths {
		err := repositories.ValidateRepoPath(config, path)
		if err != nil {
			fmt.Printf("❌ UNEXPECTED: %s -> %v\n", path, err)
		} else {
			fmt.Printf("✅ %s\n", path)
		}
	}

	fmt.Println("\n🚫 Invalid paths (should be blocked):")
	for _, path := range invalidPaths {
		err := repositories.ValidateRepoPath(config, path)
		if err != nil {
			fmt.Printf("✅ BLOCKED: %s -> %v\n", path, err)
		} else {
			fmt.Printf("❌ SECURITY ISSUE: %s was allowed!\n", path)
		}
	}

	// Test 4: Attack Prevention
	fmt.Println("\n🛡️  Test 4: Security Attack Prevention")
	attackVectors := []struct {
		owner, repo string
		description string
	}{
		{"../malicious", "repo", "Path traversal in owner"},
		{"user", "project/../hack", "Path traversal in repo"},
		{"/root", "project", "Absolute path in owner"},
		{"user", "/etc/passwd", "Absolute path in repo"},
		{"user*", "project", "Invalid character (asterisk)"},
		{"user", "project?", "Invalid character (question mark)"},
		{"CON", "project", "Windows reserved name"},
		{"user", "aux", "Windows reserved name (lowercase)"},
	}

	for _, attack := range attackVectors {
		_, err := repositories.GetRepoPath(config, attack.owner, attack.repo)
		if err != nil {
			fmt.Printf("✅ BLOCKED: %s -> %v\n", attack.description, err)
		} else {
			fmt.Printf("❌ SECURITY ISSUE: %s was allowed!\n", attack.description)
		}
	}

	// Test 5: Default Configuration
	fmt.Println("\n⚙️  Test 5: Default Configuration")
	defaultPath, err := repositories.GetRepoPathWithDefault("example", "project")
	if err != nil {
		fmt.Printf("❌ Default config failed: %v\n", err)
	} else {
		fmt.Printf("✅ Default config path: %s\n", defaultPath)
	}

	// 테스트 6: 리포지토리 존재 여부 확인
	fmt.Println("\n📁 Test 6: Repository Existence Checking")

	// 테스트용 임시 디렉토리 생성
	tempDir, err := os.MkdirTemp("", "git-repo-test-")
	if err != nil {
		fmt.Printf("❌ Failed to create temp dir: %v\n", err)
	} else {
		defer func() { _ = os.RemoveAll(tempDir) }()

		// 테스트 설정 생성
		testConfig, err := repositories.NewPathConfig(tempDir)
		if err != nil {
			fmt.Printf("❌ Failed to create test config: %v\n", err)
		} else {
			// 존재하지 않는 리포지토리 테스트
			nonExistentPath := filepath.Join(tempDir, "user", "missing.git")
			err = repositories.EnsureRepoExists(testConfig, nonExistentPath)
			if err != nil {
				fmt.Printf("✅ CORRECTLY BLOCKED non-existent repo: %v\n", err)
			} else {
				fmt.Printf("❌ SECURITY ISSUE: Non-existent repo was allowed!\n")
			}

			// Mock Git 리포지토리 구조 생성 (bare)
			bareRepoPath := filepath.Join(tempDir, "user", "bare-repo.git")
			err = createMockBareRepo(bareRepoPath)
			if err != nil {
				fmt.Printf("❌ Failed to create mock repo: %v\n", err)
			} else {
				err = repositories.EnsureRepoExists(testConfig, bareRepoPath)
				if err != nil {
					fmt.Printf("❌ UNEXPECTED: Valid bare repo rejected: %v\n", err)
				} else {
					fmt.Printf("✅ Valid bare repository accepted: %s\n", bareRepoPath)
				}
			}

			// Mock non-bare 리포지토리 생성
			nonBareRepoPath := filepath.Join(tempDir, "user", "regular-repo.git")
			err = createMockNonBareRepo(nonBareRepoPath)
			if err != nil {
				fmt.Printf("❌ Failed to create mock non-bare repo: %v\n", err)
			} else {
				err = repositories.EnsureRepoExists(testConfig, nonBareRepoPath)
				if err != nil {
					fmt.Printf("❌ UNEXPECTED: Valid non-bare repo rejected: %v\n", err)
				} else {
					fmt.Printf("✅ Valid non-bare repository accepted: %s\n", nonBareRepoPath)
				}
			}

			// 잘못된 리포지토리 테스트 (빈 디렉토리)
			invalidRepoPath := filepath.Join(tempDir, "user", "invalid.git")
			_ = os.MkdirAll(invalidRepoPath, 0755)
			err = repositories.EnsureRepoExists(testConfig, invalidRepoPath)
			if err != nil {
				fmt.Printf("✅ CORRECTLY BLOCKED invalid repo: %v\n", err)
			} else {
				fmt.Printf("❌ SECURITY ISSUE: Invalid repo was allowed!\n")
			}
		}
	}

	// 테스트 7: 패키지 통합 기능
	testPackageIntegration()

	fmt.Println("\n🎉 Complete package integration testing finished!")
}

// 테스트용 Mock Git 리포지토리 생성 헬퍼 함수

// Bare 리포지토리 생성 (웹 인터페이스용)
func createMockBareRepo(repoPath string) error {
	// 디렉토리 구조 생성
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		return err
	}

	// 필수 디렉토리 생성
	dirs := []string{"refs", "refs/heads", "refs/tags", "objects", "objects/info", "objects/pack"}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(repoPath, dir), 0755); err != nil {
			return err
		}
	}

	// 필수 파일 생성
	files := map[string]string{
		"HEAD":        "ref: refs/heads/main\n",
		"config":      "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n",
		"description": "Unnamed repository; edit this file 'description' to name the repositories.\n",
	}

	for filename, content := range files {
		if err := os.WriteFile(filepath.Join(repoPath, filename), []byte(content), 0644); err != nil {
			return err
		}
	}

	return nil
}

// Non-bare 리포지토리 생성 (일반 작업 디렉토리용)
func createMockNonBareRepo(repoPath string) error {
	// 디렉토리 구조 생성
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		return err
	}

	// .git 디렉토리 생성
	gitDir := filepath.Join(repoPath, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		return err
	}

	// .git 내부에 필수 디렉토리 생성
	dirs := []string{"refs", "refs/heads", "refs/tags", "objects", "objects/info", "objects/pack"}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(gitDir, dir), 0755); err != nil {
			return err
		}
	}

	// .git 내부에 필수 파일 생성
	files := map[string]string{
		"HEAD":        "ref: refs/heads/main\n",
		"config":      "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n",
		"description": "Unnamed repository; edit this file 'description' to name the repositories.\n",
	}

	for filename, content := range files {
		if err := os.WriteFile(filepath.Join(gitDir, filename), []byte(content), 0644); err != nil {
			return err
		}
	}

	return nil
}
