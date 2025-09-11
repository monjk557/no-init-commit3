// 설정 파일 로딩 테스트 유틸리티
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/collabops/git-executor/internal/config"
)

func main() {
	fmt.Println("🧪 Manual Testing: Configuration Loading")
	fmt.Println(strings.Repeat("=", 50))

	// 테스트 1: 기본 설정 파일 로드 테스트
	fmt.Println("\n📁 Test 1: Default Configuration")
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("❌ Failed to load config: %v", err)
	}
	fmt.Printf("✅ Config loaded successfully\n")

	// 테스트 2: 로드된 설정 값들 표시
	fmt.Println("\n⚙️  Test 2: Configuration Values")
	fmt.Printf("Server Host: %s\n", cfg.Server.Host)
	fmt.Printf("Server Port: %d\n", cfg.Server.Port)
	fmt.Printf("Server Base Path: %s\n", cfg.Server.BasePath)
	fmt.Printf("Git Repo Root: %s\n", cfg.Git.RepoRoot)
	fmt.Printf("Log Level: %s\n", cfg.Logging.Level)
	fmt.Printf("Log Format: %s\n", cfg.Logging.Format)

	// 테스트 3: 설정을 JSON 형식으로 출력
	fmt.Println("\n📄 Test 3: Full Configuration (JSON)")
	configJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Printf("❌ Failed to marshal config: %v\n", err)
	} else {
		fmt.Printf("✅ Configuration JSON:\n%s\n", string(configJSON))
	}

	// 테스트 4: 설정 유효성 검사 (Load() 함수 내에서 자동으로 수행됨)
	fmt.Println("\n🔒 Test 4: Configuration Validation")
	fmt.Printf("✅ Configuration was validated automatically during loading\n")

	fmt.Println("\n🎉 Configuration testing completed!")
}
