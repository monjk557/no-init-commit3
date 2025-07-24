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

	// Test 1: Load default configuration
	fmt.Println("\n📁 Test 1: Default Configuration")
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("❌ Failed to load config: %v", err)
	}
	fmt.Printf("✅ Config loaded successfully\n")

	// Test 2: Display loaded configuration
	fmt.Println("\n⚙️  Test 2: Configuration Values")
	fmt.Printf("Server Host: %s\n", cfg.Server.Host)
	fmt.Printf("Server Port: %d\n", cfg.Server.Port)
	fmt.Printf("Server Base Path: %s\n", cfg.Server.BasePath)
	fmt.Printf("Git Repo Root: %s\n", cfg.Git.RepoRoot)
	fmt.Printf("Log Level: %s\n", cfg.Logging.Level)
	fmt.Printf("Log Format: %s\n", cfg.Logging.Format)

	// Test 3: JSON representation
	fmt.Println("\n📄 Test 3: Full Configuration (JSON)")
	configJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Printf("❌ Failed to marshal config: %v\n", err)
	} else {
		fmt.Printf("✅ Configuration JSON:\n%s\n", string(configJSON))
	}

	// Test 4: Configuration validation happens automatically during Load()
	fmt.Println("\n🔒 Test 4: Configuration Validation")
	fmt.Printf("✅ Configuration was validated automatically during loading\n")

	fmt.Println("\n🎉 Configuration testing completed!")
}
