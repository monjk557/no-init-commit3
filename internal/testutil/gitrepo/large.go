package gitrepo

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// =============================================================================
// Large Repository Fixtures (R3_big)
// 회귀: "대용량 레포에서도 성능이 유지되고 timeout이 발생하지 않는다"
// =============================================================================

// LargeRepoConfig configures large repo generation
type LargeRepoConfig struct {
	CommitCount    int  // Number of commits (default: 100)
	BranchCount    int  // Number of branches (default: 10)
	FileCount      int  // Files per commit (default: 5)
	DirDepth       int  // Directory nesting depth (default: 3)
	LargeDiffLines int  // Lines in large diff files (default: 1000)
	SkipIfSlow     bool // Skip in CI if too slow
}

// DefaultLargeRepoConfig returns sensible defaults for testing
func DefaultLargeRepoConfig() LargeRepoConfig {
	return LargeRepoConfig{
		CommitCount:    100,
		BranchCount:    10,
		FileCount:      5,
		DirDepth:       3,
		LargeDiffLines: 1000,
		SkipIfSlow:     true,
	}
}

// SmallLargeRepoConfig returns config for quick tests
func SmallLargeRepoConfig() LargeRepoConfig {
	return LargeRepoConfig{
		CommitCount:    10,
		BranchCount:    3,
		FileCount:      2,
		DirDepth:       2,
		LargeDiffLines: 100,
		SkipIfSlow:     false,
	}
}

// MakeLargeRepoFixtures creates a large repository for performance testing
func (r *TestRepo) MakeLargeRepoFixtures(cfg LargeRepoConfig) {
	r.T.Helper()

	if cfg.SkipIfSlow {
		// Skip in short mode
		if testing.Short() {
			r.T.Skip("skipping large repo fixture in short mode")
		}
	}

	// Create many commits
	for i := 0; i < cfg.CommitCount; i++ {
		files := make(map[string]string)
		for j := 0; j < cfg.FileCount; j++ {
			path := makeDeepPath(cfg.DirDepth, i, j)
			content := fmt.Sprintf("Content for commit %d, file %d\n", i, j)
			files[path] = content
		}
		r.CommitMultiple(files, fmt.Sprintf("Commit %d", i))
	}

	// Create many branches
	mainTip := r.HeadHash()
	for i := 0; i < cfg.BranchCount; i++ {
		branchName := fmt.Sprintf("branch-%d", i)
		r.BranchCreate(branchName)
		r.Commit(fmt.Sprintf("branch%d.txt", i), fmt.Sprintf("Branch %d content", i), fmt.Sprintf("Branch %d commit", i))
		r.BranchCheckout("main")
	}

	// Reset main to original tip
	_ = mainTip // For future use
}

// MakeLargeDiff creates a scenario with large diff
func (r *TestRepo) MakeLargeDiff(lineCount int) (source, target plumbing.Hash) {
	r.T.Helper()

	// Create base file with many lines
	var lines strings.Builder
	for i := 0; i < lineCount; i++ {
		fmt.Fprintf(&lines, "Line %d: original content\n", i)
	}
	target = r.Commit("large.txt", lines.String(), "Large file base")

	// Create branch with modifications
	r.BranchCreate("large-diff")
	lines.Reset()
	for i := 0; i < lineCount; i++ {
		if i%10 == 0 {
			fmt.Fprintf(&lines, "Line %d: MODIFIED content\n", i)
		} else {
			fmt.Fprintf(&lines, "Line %d: original content\n", i)
		}
	}
	source = r.Commit("large.txt", lines.String(), "Large file modified")

	r.BranchCheckout("main")
	return source, target
}

// MakeManyFiles creates a commit with many files
func (r *TestRepo) MakeManyFiles(count int) plumbing.Hash {
	r.T.Helper()

	files := make(map[string]string)
	for i := 0; i < count; i++ {
		path := fmt.Sprintf("dir%d/file%d.txt", i/100, i)
		files[path] = fmt.Sprintf("File %d content", i)
	}
	return r.CommitMultiple(files, fmt.Sprintf("Add %d files", count))
}

// MakeDeepNesting creates deeply nested directory structure
func (r *TestRepo) MakeDeepNesting(depth int) plumbing.Hash {
	r.T.Helper()

	path := ""
	for i := 0; i < depth; i++ {
		path = filepath.Join(path, fmt.Sprintf("level%d", i))
	}
	path = filepath.Join(path, "deepfile.txt")

	return r.Commit(path, "Deep content", "Deep nesting commit")
}

func makeDeepPath(depth, commit, file int) string {
	parts := make([]string, depth)
	for i := 0; i < depth; i++ {
		parts[i] = fmt.Sprintf("d%d", (commit+file+i)%10)
	}
	return filepath.Join(append(parts, fmt.Sprintf("f%d_%d.txt", commit, file))...)
}
