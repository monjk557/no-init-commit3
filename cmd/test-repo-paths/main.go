package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/collabops/git-executor/internal/repository"
)

func main() {
	fmt.Println("🧪 Manual Testing: Repository Path Management")
	fmt.Println(strings.Repeat("=", 50))

	// Test 1: Path Configuration
	fmt.Println("\n📁 Test 1: Path Configuration")
	config, err := repository.NewPathConfig("/var/lib/git/repos")
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
		path, err := repository.GetRepoPath(config, tc.owner, tc.repo)
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
		err := repository.ValidateRepoPath(config, path)
		if err != nil {
			fmt.Printf("❌ UNEXPECTED: %s -> %v\n", path, err)
		} else {
			fmt.Printf("✅ %s\n", path)
		}
	}

	fmt.Println("\n🚫 Invalid paths (should be blocked):")
	for _, path := range invalidPaths {
		err := repository.ValidateRepoPath(config, path)
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
		_, err := repository.GetRepoPath(config, attack.owner, attack.repo)
		if err != nil {
			fmt.Printf("✅ BLOCKED: %s -> %v\n", attack.description, err)
		} else {
			fmt.Printf("❌ SECURITY ISSUE: %s was allowed!\n", attack.description)
		}
	}

	// Test 5: Default Configuration
	fmt.Println("\n⚙️  Test 5: Default Configuration")
	defaultPath, err := repository.GetRepoPathWithDefault("example", "project")
	if err != nil {
		fmt.Printf("❌ Default config failed: %v\n", err)
	} else {
		fmt.Printf("✅ Default config path: %s\n", defaultPath)
	}

	fmt.Println("\n🎉 Manual testing completed!")
}
