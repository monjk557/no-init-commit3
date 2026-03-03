package merge

import (
	"context"
	"fmt"
	"strings"
)

// snapshot_adapter.go - Layer 2: Adapters (native git ↔ DTO 변환)
// go-git 타입 제거 — native git ls-tree 사용

// TreeToSnapshot converts a git tree (by hash) to a TreeSnapshot using native git
func TreeToSnapshot(ctx context.Context, repoPath string, treeHash string) (*TreeSnapshot, error) {
	if treeHash == "" {
		return NewTreeSnapshot(), nil
	}

	snapshot := NewTreeSnapshot()

	// Use recursive ls-tree to get all entries
	entries, err := gitLsTree(ctx, repoPath, treeHash, true)
	if err != nil {
		return nil, fmt.Errorf("failed to list tree: %w", err)
	}

	for _, entry := range entries {
		isDir := entry.Type == "tree"
		fileSnapshot := FileSnapshot{
			Hash:  entry.Hash,
			Mode:  parseModeToUint32(entry.Mode),
			IsDir: isDir,
		}
		snapshot.AddEntry(entry.Path, fileSnapshot)
	}

	return snapshot, nil
}

// CommitToSnapshots extracts TreeSnapshots from base, source, and target commits
// Returns (baseSnapshot, sourceSnapshot, targetSnapshot, error)
func CommitToSnapshots(
	ctx context.Context,
	repoPath string,
	sourceHash, targetHash string,
) (*TreeSnapshot, *TreeSnapshot, *TreeSnapshot, error) {
	// Find merge base
	mergeBaseHash, err := gitMergeBase(ctx, repoPath, sourceHash, targetHash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	// Get commit objects to access tree hashes
	baseCommit, err := gitCatFileCommit(ctx, repoPath, mergeBaseHash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get base commit: %w", err)
	}

	sourceCommit, err := gitCatFileCommit(ctx, repoPath, sourceHash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get source commit: %w", err)
	}

	targetCommit, err := gitCatFileCommit(ctx, repoPath, targetHash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	// Convert to snapshots
	baseSnapshot, err := TreeToSnapshot(ctx, repoPath, baseCommit.TreeHash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to convert base tree: %w", err)
	}

	sourceSnapshot, err := TreeToSnapshot(ctx, repoPath, sourceCommit.TreeHash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to convert source tree: %w", err)
	}

	targetSnapshot, err := TreeToSnapshot(ctx, repoPath, targetCommit.TreeHash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to convert target tree: %w", err)
	}

	return baseSnapshot, sourceSnapshot, targetSnapshot, nil
}

// parseModeToUint32 converts a git mode string (e.g., "100644") to uint32
func parseModeToUint32(mode string) uint32 {
	mode = strings.TrimSpace(mode)
	var result uint32
	for _, c := range mode {
		if c >= '0' && c <= '7' {
			result = result*8 + uint32(c-'0')
		}
	}
	return result
}
