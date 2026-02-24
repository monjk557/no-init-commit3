package performance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"go.uber.org/zap"
)

// CommitGraphOptimizer handles commit-graph optimizations
type CommitGraphOptimizer struct {
	config *PerformanceConfig
	logger *zap.Logger
}

// NewCommitGraphOptimizer creates a new commit-graph optimizer
func NewCommitGraphOptimizer(config *PerformanceConfig, logger *zap.Logger) *CommitGraphOptimizer {
	return &CommitGraphOptimizer{
		config: config,
		logger: logger,
	}
}

// EnsureCommitGraph ensures that commit-graph exists for the repository
func (cg *CommitGraphOptimizer) EnsureCommitGraph(repoPath string) error {
	if !cg.config.EnableCommitGraphOptimization {
		return nil
	}

	// Check if commit-graph already exists
	commitGraphPath := filepath.Join(repoPath, ".git", "objects", "info", "commit-graph")
	if _, err := os.Stat(commitGraphPath); err == nil {
		cg.logger.Debug("Commit-graph already exists", zap.String("repo_path", repoPath))
		return nil
	}

	// Generate commit-graph
	start := time.Now()
	cmd := exec.Command("git", "commit-graph", "write", "--reachable")
	cmd.Dir = repoPath

	if err := cmd.Run(); err != nil {
		cg.logger.Warn("Failed to generate commit-graph",
			zap.String("repo_path", repoPath),
			zap.Error(err))
		return fmt.Errorf("failed to generate commit-graph: %w", err)
	}

	duration := time.Since(start)
	cg.logger.Info("Generated commit-graph",
		zap.String("repo_path", repoPath),
		zap.Duration("duration", duration))

	return nil
}

// HasCommitGraph checks if the repository has a commit-graph
func (cg *CommitGraphOptimizer) HasCommitGraph(repoPath string) bool {
	commitGraphPath := filepath.Join(repoPath, ".git", "objects", "info", "commit-graph")
	_, err := os.Stat(commitGraphPath)
	return err == nil
}

// OptimizeLogTraversal provides optimized log traversal using commit-graph when available
func (cg *CommitGraphOptimizer) OptimizeLogTraversal(repo *gogit.Repository, from plumbing.Hash) (*gogit.LogOptions, bool) {
	if !cg.config.EnableCommitGraphOptimization {
		return &gogit.LogOptions{From: from}, false
	}

	// When commit-graph is available, Git's internal operations are automatically optimized
	// The go-git library will use the commit-graph for faster parent lookups
	return &gogit.LogOptions{
		From: from,
		// Additional optimizations can be added here as go-git evolves
	}, true
}

// GetOptimizationStats returns statistics about commit-graph usage
func (cg *CommitGraphOptimizer) GetOptimizationStats(repoPath string) map[string]interface{} {
	stats := map[string]interface{}{
		"commit_graph_enabled": cg.config.EnableCommitGraphOptimization,
		"commit_graph_exists":  cg.HasCommitGraph(repoPath),
		"optimization_active":  cg.config.EnableCommitGraphOptimization && cg.HasCommitGraph(repoPath),
	}

	if cg.HasCommitGraph(repoPath) {
		commitGraphPath := filepath.Join(repoPath, ".git", "objects", "info", "commit-graph")
		if fileInfo, err := os.Stat(commitGraphPath); err == nil {
			stats["commit_graph_size"] = fileInfo.Size()
			stats["commit_graph_modified"] = fileInfo.ModTime()
		}
	}

	return stats
}
