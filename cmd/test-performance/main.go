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
	"github.com/collabops/git-executor/internal/performance"
	"github.com/collabops/git-executor/internal/repositories"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <repo-path>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Example: %s test-repos/medium-repo\n", os.Args[0])
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

	// Test without optimization
	fmt.Printf("🔍 Testing performance on repository: %s\n", repoPath)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	// Test 1: Without commit-graph optimization
	fmt.Printf("📊 Test 1: Baseline (no optimization)\n")
	os.Setenv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_COMMIT_GRAPH_OPTIMIZATION", "false")
	runPerformanceTest(repo, repoPath, logger, false)

	fmt.Printf("\n")

	// Test 2: With commit-graph optimization
	fmt.Printf("⚡ Test 2: With commit-graph optimization\n")
	os.Setenv("COLLABOPS_PERFORMANCE_FEATURES_ENABLE_COMMIT_GRAPH_OPTIMIZATION", "true")
	runPerformanceTest(repo, repoPath, logger, true)

	fmt.Printf("\n🎯 Performance test completed!\n")
}

func runPerformanceTest(repo *gogit.Repository, repoPath string, logger *logging.Logger, optimized bool) {
	// Initialize performance config and commit-graph optimizer
	perfConfig := performance.NewPerformanceConfig()
	commitGraphOpt := performance.NewCommitGraphOptimizer(perfConfig, logger.Logger)

	if optimized {
		// Ensure commit-graph exists for the repository
		if err := commitGraphOpt.EnsureCommitGraph(repoPath); err != nil {
			fmt.Printf("   ⚠️  Failed to ensure commit-graph: %v\n", err)
		} else {
			fmt.Printf("   ✅ Commit-graph optimization enabled\n")
		}
	}

	// Create path config and repository manager
	pathConfig, err := repositories.NewPathConfig("/Users/johnbaek/collabops-git-executor/test-repos")
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

	// Get performance stats
	stats := overviewService.GetPerformanceStats(repoPath)
	fmt.Printf("   📈 Optimization status: %v\n", stats["optimization_active"])

	// Test the critical bottleneck function: GetTreeWithCommit
	ctx := context.Background()

	fmt.Printf("   🧪 Running directory listing test...\n")

	// Measure performance for root directory listing (the critical operation)
	start := time.Now()

	entries, meta, err := overviewService.GetTreeWithCommit(ctx, repo, "HEAD", "")

	duration := time.Since(start)

	if err != nil {
		fmt.Printf("   ❌ Test failed: %v\n", err)
		return
	}

	// Print results
	fmt.Printf("   ⏱️  Duration: %v\n", duration)
	fmt.Printf("   📁 Files/dirs: %d\n", len(entries))
	fmt.Printf("   📊 Commits scanned: %d\n", meta.ScannedCommits)
	fmt.Printf("   ⏰ Time budget used: %dms\n", meta.TimeBudgetMs)

	if meta.Incomplete {
		fmt.Printf("   ⚠️  Result incomplete (reason: %s)\n", meta.CutReason)
	} else {
		fmt.Printf("   ✅ Complete result\n")
	}

	// Convert to milliseconds for better comparison
	durationMs := float64(duration.Nanoseconds()) / 1e6
	fmt.Printf("   🏁 Total time: %.1fms\n", durationMs)
}
