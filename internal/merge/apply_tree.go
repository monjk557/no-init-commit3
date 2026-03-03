package merge

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// 3-way merge tree creation via native `git merge-tree --write-tree`
// ---------------------------------------------------------------------------
//
// Replaces the previous 821-line Go implementation that manually walked three
// tree objects and resolved file-by-file. The native git merge-tree command
// (Git 2.38+) performs the identical 3-way merge in C, leveraging rename
// detection, ORT strategy, and direct object store writes — typically 10-50x
// faster than the Go reimplementation.
// ---------------------------------------------------------------------------

// createMergedTree performs a 3-way merge by finding the merge base automatically.
// This is the primary entry point called from resolveMergeTree in strategy_merge.go.
func (h *MergeOperationsHandler) createMergedTree(
	ctx context.Context,
	repoPath string,
	sourceCommit, targetCommit *NativeCommit,
	analysis *MergeAnalysisResult,
	strategy MergeStrategy,
) (string, error) {
	if sourceCommit == nil || targetCommit == nil {
		return "", fmt.Errorf("source and target commits cannot be nil")
	}

	// Find merge base
	mergeBaseHash, err := gitMergeBase(ctx, repoPath, sourceCommit.Hash, targetCommit.Hash)
	if err != nil {
		return "", fmt.Errorf("failed to find merge base: %w", err)
	}

	h.logger.Debug("Found merge base for tree creation",
		zap.String("merge_base", mergeBaseHash),
		zap.String("source", sourceCommit.Hash),
		zap.String("target", targetCommit.Hash))

	return h.createMergedTreeWithBase(ctx, repoPath, sourceCommit, targetCommit, mergeBaseHash, analysis, strategy)
}

// createMergedTreeWithBase performs a 3-way merge with an explicit base commit.
// Used for rebase operations where the base is the parent of the commit being rebased.
func (h *MergeOperationsHandler) createMergedTreeWithBase(
	ctx context.Context,
	repoPath string,
	sourceCommit, targetCommit *NativeCommit,
	baseCommitHash string,
	analysis *MergeAnalysisResult,
	strategy MergeStrategy,
) (string, error) {
	if baseCommitHash == "" {
		return "", fmt.Errorf("base commit hash cannot be empty")
	}

	// Determine strategy flags for merge-tree
	// For ours/theirs strategies, we handle differently
	switch strategy {
	case MergeStrategyOurs:
		// "ours" means keep target's tree entirely
		h.logger.Debug("Using 'ours' strategy — returning target tree",
			zap.String("target_tree", targetCommit.TreeHash))
		return targetCommit.TreeHash, nil

	case MergeStrategyTheirs:
		// "theirs" means keep source's tree entirely
		h.logger.Debug("Using 'theirs' strategy — returning source tree",
			zap.String("source_tree", sourceCommit.TreeHash))
		return sourceCommit.TreeHash, nil

	case MergeStrategySubtree:
		// Subtree strategy: use source tree
		h.logger.Debug("Using 'subtree' strategy — returning source tree",
			zap.String("source_tree", sourceCommit.TreeHash))
		return sourceCommit.TreeHash, nil
	}

	// For recursive/resolve/ORT: use git merge-tree --write-tree
	// merge-tree merges <ours> <theirs> with --merge-base=<base>
	// In our terminology: target = ours, source = theirs
	result, err := gitMergeTree(ctx, repoPath, baseCommitHash, targetCommit.Hash, sourceCommit.Hash)
	if err != nil {
		return "", fmt.Errorf("git merge-tree failed: %w", err)
	}

	h.logger.Info("merge-tree completed",
		zap.String("tree_hash", result.TreeHash),
		zap.Bool("has_conflicts", result.HasConflicts),
		zap.Int("conflict_count", len(result.Conflicts)),
		zap.String("strategy", string(strategy)))

	// Update analysis with conflict info from merge-tree if available
	if result.HasConflicts && analysis != nil {
		analysis.HasConflicts = true
		if analysis.ConflictCount == 0 {
			analysis.ConflictCount = len(result.Conflicts)
		}
	}

	return result.TreeHash, nil
}

// replaceEntryInTree replaces a single entry at the given path in a tree,
// creating new tree objects for each directory level. Returns the new root
// tree hash.
func replaceEntryInTree(ctx context.Context, repoPath, rootTree, path string, newEntry *NativeTreeEntry) (string, error) {
	parts := splitPath(path)
	if len(parts) == 0 {
		return rootTree, fmt.Errorf("empty path")
	}

	return replaceEntryRecursive(ctx, repoPath, rootTree, parts, newEntry)
}

func replaceEntryRecursive(ctx context.Context, repoPath, currentTree string, pathParts []string, newEntry *NativeTreeEntry) (string, error) {
	entries, err := gitLsTree(ctx, repoPath, currentTree, false)
	if err != nil {
		return "", fmt.Errorf("ls-tree %s: %w", currentTree, err)
	}

	targetName := pathParts[0]

	if len(pathParts) == 1 {
		// Leaf: replace this entry
		found := false
		for i := range entries {
			if entries[i].Path == targetName {
				entries[i].Mode = newEntry.Mode
				entries[i].Type = newEntry.Type
				entries[i].Hash = newEntry.Hash
				found = true
				break
			}
		}
		if !found {
			entries = append(entries, *newEntry)
		}
		return gitMktree(ctx, repoPath, entries)
	}

	// Non-leaf: recurse into subdirectory
	for i := range entries {
		if entries[i].Path == targetName && entries[i].Type == "tree" {
			newSubTree, err := replaceEntryRecursive(ctx, repoPath, entries[i].Hash, pathParts[1:], newEntry)
			if err != nil {
				return "", err
			}
			entries[i].Hash = newSubTree
			return gitMktree(ctx, repoPath, entries)
		}
	}

	return "", fmt.Errorf("directory %s not found in tree", targetName)
}

// isEnvironmentFile detects if a path matches environment file patterns.
func isEnvironmentFile(path string) bool {
	patterns := []string{
		".env",
		".env.*",
		"*.env",
		".env.local",
		".env.development",
		".env.production",
		".env.staging",
		".env.test",
		"config/secrets*",
		"config/credentials*",
	}
	for _, p := range patterns {
		if matchPath(path, p) {
			return true
		}
	}
	return false
}

// matchPath performs simple glob-like pattern matching.
func matchPath(path, pattern string) bool {
	// Exact match
	if path == pattern {
		return true
	}
	// Simple wildcard matching
	if len(pattern) > 0 && pattern[0] == '*' {
		suffix := pattern[1:]
		return len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix
	}
	if len(pattern) > 0 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(path) >= len(prefix) && path[:len(prefix)] == prefix
	}
	// Check for .env.* pattern (e.g. .env.local matches .env.*)
	if pattern == ".env.*" {
		return len(path) > 4 && path[:4] == ".env" && path[4] == '.'
	}
	return false
}

// ---------------------------------------------------------------------------
// Path helpers
// ---------------------------------------------------------------------------

func splitPath(path string) []string {
	var parts []string
	current := ""
	for _, c := range path {
		if c == '/' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

func parentDir(path string) string {
	parts := splitPath(path)
	if len(parts) <= 1 {
		return ""
	}
	result := ""
	for i, p := range parts[:len(parts)-1] {
		if i > 0 {
			result += "/"
		}
		result += p
	}
	return result
}

func baseName(path string) string {
	parts := splitPath(path)
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}
