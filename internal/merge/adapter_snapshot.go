package merge

import (
	"fmt"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// snapshot_adapter.go - Layer 2: Adapters (go-git ↔ DTO 변환)
// 이 파일은 go-git 타입을 사용하지만, 순수 레이어(Layer 0-1)로 변환하는 역할만 함

// TreeToSnapshot converts a go-git Tree to a TreeSnapshot
// This is the bridge between go-git and our pure types
func TreeToSnapshot(tree *object.Tree, repo *gogit.Repository) (*TreeSnapshot, error) {
	if tree == nil {
		return NewTreeSnapshot(), nil
	}

	snapshot := NewTreeSnapshot()

	// Recursively walk the tree
	err := walkTree(tree, "", repo, snapshot)
	if err != nil {
		return nil, fmt.Errorf("failed to walk tree: %w", err)
	}

	return snapshot, nil
}

// walkTree recursively walks a tree and adds entries to the snapshot
func walkTree(tree *object.Tree, prefix string, repo *gogit.Repository, snapshot *TreeSnapshot) error {
	for _, entry := range tree.Entries {
		path := entry.Name
		if prefix != "" {
			path = prefix + "/" + entry.Name
		}

		isDir := entry.Mode == filemode.Dir

		fileSnapshot := FileSnapshot{
			Hash:  entry.Hash.String(),
			Mode:  uint32(entry.Mode),
			IsDir: isDir,
		}

		snapshot.AddEntry(path, fileSnapshot)

		// Recursively process directories
		if isDir {
			subTree, err := repo.TreeObject(entry.Hash)
			if err != nil {
				return fmt.Errorf("failed to get subtree %s: %w", path, err)
			}
			if err := walkTree(subTree, path, repo, snapshot); err != nil {
				return err
			}
		}
	}

	return nil
}

// CommitToSnapshots extracts TreeSnapshots from base, source, and target commits
// Returns (baseSnapshot, sourceSnapshot, targetSnapshot, error)
func CommitToSnapshots(
	repo *gogit.Repository,
	sourceCommit, targetCommit *object.Commit,
) (*TreeSnapshot, *TreeSnapshot, *TreeSnapshot, error) {
	// Find merge base
	mergeBases, err := sourceCommit.MergeBase(targetCommit)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to find merge base: %w", err)
	}
	if len(mergeBases) == 0 {
		return nil, nil, nil, fmt.Errorf("no common ancestor found")
	}
	baseCommit := mergeBases[0]

	// Get trees
	baseTree, err := baseCommit.Tree()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get base tree: %w", err)
	}

	sourceTree, err := sourceCommit.Tree()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get source tree: %w", err)
	}

	targetTree, err := targetCommit.Tree()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get target tree: %w", err)
	}

	// Convert to snapshots
	baseSnapshot, err := TreeToSnapshot(baseTree, repo)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to convert base tree: %w", err)
	}

	sourceSnapshot, err := TreeToSnapshot(sourceTree, repo)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to convert source tree: %w", err)
	}

	targetSnapshot, err := TreeToSnapshot(targetTree, repo)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to convert target tree: %w", err)
	}

	return baseSnapshot, sourceSnapshot, targetSnapshot, nil
}
