package main

import (
	"context"
	"fmt"
	"os"
	"time"

	gogit "github.com/go-git/go-git/v5"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/overview"
	"github.com/collabops/git-executor/internal/repositories"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <repo-path>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Example: %s .\n", os.Args[0])
		os.Exit(1)
	}

	repoPath := os.Args[1]

	// Initialize logging
	logCfg := config.LoggingConfig{
		Level:       "info",
		Format:      "text",
		Output:      "stdout",
		Development: true,
	}

	logger, err := logging.NewLogger(logCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create logger: %v\n", err)
		os.Exit(1)
	}

	// Open repository
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open repository %s: %v\n", repoPath, err)
		os.Exit(1)
	}

	fmt.Printf("🌳 Tree Traversal Performance Test: %s\n", repoPath)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	// Test 1: Without parallel tree traversal
	fmt.Printf("📊 Test 1: Sequential tree processing (baseline)\n")
	os.Setenv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_COMMIT_GRAPH_OPTIMIZATION", "false")
	os.Setenv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_OBJECT_CACHE", "false")
	os.Setenv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_FD_POOL", "false")
	runLanguageDetectionTest(repo, repoPath, logger, "baseline")

	fmt.Printf("\n")

	// Test 2: With parallel tree traversal (via all optimizations)
	fmt.Printf("⚡ Test 2: Parallel tree processing (optimized)\n")
	os.Setenv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_COMMIT_GRAPH_OPTIMIZATION", "true")
	os.Setenv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_OBJECT_CACHE", "true")
	os.Setenv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_FD_POOL", "true")
	runLanguageDetectionTest(repo, repoPath, logger, "optimized")

	fmt.Printf("\n🎯 Tree traversal test completed!\n")
}

func runLanguageDetectionTest(repo *gogit.Repository, repoPath string, logger *logging.Logger, testType string) {
	// Create path config and repository manager
	pathConfig, err := repositories.NewPathConfig("/Users/johnbaek/collabops-git-executor")
	if err != nil {
		fmt.Printf("   ❌ Failed to create path config: %v\n", err)
		return
	}

	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	if err != nil {
		fmt.Printf("   ❌ Failed to create repository manager: %v\n", err)
		return
	}

	// Create overview service
	overviewService, err := overview.NewRepositoryOverviewService(logger, pathConfig, repoManager)
	if err != nil {
		fmt.Printf("   ❌ Failed to create overview service: %v\n", err)
		return
	}

	if testType == "baseline" {
		fmt.Printf("   📊 Using sequential tree processing\n")
	} else {
		fmt.Printf("   ⚡ Using parallel tree processing\n")
	}

	// Test the tree traversal operation: GetLanguageDistribution
	ctx := context.Background()

	fmt.Printf("   🧪 Running language distribution test...\n")

	// Measure performance for language distribution (tree traversal heavy operation)
	start := time.Now()

	langDist := overviewService.GetLanguageDistribution(ctx, repo, "HEAD")

	duration := time.Since(start)

	// Print results
	fmt.Printf("   ⏱️  Duration: %v\n", duration)
	fmt.Printf("   📁 Languages detected: %d\n", len(langDist.Languages))
	fmt.Printf("   📊 Total files processed: %d\n", langDist.TotalFiles)
	fmt.Printf("   💾 Total bytes: %d\n", langDist.TotalBytes)

	// Convert to milliseconds for better comparison
	durationMs := float64(duration.Nanoseconds()) / 1e6
	fmt.Printf("   🏁 Total time: %.1fms\n", durationMs)
}
