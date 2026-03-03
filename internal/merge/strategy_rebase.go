package merge

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// performRebase performs a rebase by reapplying source branch commits onto target branch.
// Each commit from source (after merge base) is applied sequentially onto target using
// native git commands.
func (h *MergeOperationsHandler) performRebase(ctx context.Context, repoPath string, sourceHash, targetHash string, req *MergeRequest, analysis *MergeAnalysisResult) (*NativeCommit, error) {
	h.logger.Info("Performing rebase",
		zap.String("source", sourceHash),
		zap.String("target", targetHash))

	// Find merge base
	mergeBaseHash, err := gitMergeBase(ctx, repoPath, sourceHash, targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	h.logger.Debug("Found merge base for rebase",
		zap.String("merge_base", mergeBaseHash))

	// If source is already based on target, nothing to rebase
	if mergeBaseHash == sourceHash {
		h.logger.Info("Source is already based on target, nothing to rebase")
		commit, err := gitCatFileCommit(ctx, repoPath, targetHash)
		if err != nil {
			return nil, fmt.Errorf("failed to read target commit: %w", err)
		}
		return commit, nil
	}

	// Get commits to rebase in chronological order (oldest first)
	commitHashes, err := gitRevListReverse(ctx, repoPath, mergeBaseHash, sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commits to rebase: %w", err)
	}

	if len(commitHashes) == 0 {
		h.logger.Info("No commits to rebase")
		commit, err := gitCatFileCommit(ctx, repoPath, targetHash)
		if err != nil {
			return nil, fmt.Errorf("failed to read target commit: %w", err)
		}
		return commit, nil
	}

	h.logger.Info("Rebasing commits",
		zap.Int("commit_count", len(commitHashes)))

	// Start with target as the base
	currentBaseHash := targetHash

	// Apply each commit sequentially
	for i, commitHash := range commitHashes {
		commitToRebase, err := gitCatFileCommit(ctx, repoPath, commitHash)
		if err != nil {
			return nil, fmt.Errorf("failed to read commit %s: %w", commitHash, err)
		}

		h.logger.Debug("Rebasing commit",
			zap.Int("index", i+1),
			zap.Int("total", len(commitHashes)),
			zap.String("commit", commitHash))

		// Get the current base commit for merge-tree
		currentBase, err := gitCatFileCommit(ctx, repoPath, currentBaseHash)
		if err != nil {
			return nil, fmt.Errorf("failed to read current base: %w", err)
		}

		// Root commits: use their tree directly
		var rebasedTreeHash string
		if len(commitToRebase.ParentHashes) == 0 {
			rebasedTreeHash = commitToRebase.TreeHash
		} else {
			// Analyze conflicts for this step
			singleCommitAnalysis, err := h.analyzeMerge(ctx, repoPath, commitHash, currentBaseHash, req)
			if err != nil {
				return nil, fmt.Errorf("failed to analyze rebase commit %s: %w", commitHash, err)
			}

			if singleCommitAnalysis.HasConflicts && !req.ForceConflict {
				return nil, fmt.Errorf("rebase conflict at commit %s: %d conflicts. Use force_conflict=true to override",
					commitHash, singleCommitAnalysis.ConflictCount)
			}

			// Resolve tree using the commit's parent as merge base
			rebasedTreeHash, err = h.resolveMergeTree(ctx, repoPath, commitToRebase, currentBase, singleCommitAnalysis, req.Strategy, req.ForceConflict, false)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve tree for rebase commit %s: %w", commitHash, err)
			}
		}

		// Create rebased commit preserving original author and message
		now := time.Now()
		author := CreateRebaseAuthorSignature(req, commitToRebase.Author, now)
		committer := CreateRebaseCommitterSignature(req, author, now)

		newCommitHash, err := gitCommitTree(ctx, repoPath, rebasedTreeHash,
			[]string{currentBaseHash}, author, committer, commitToRebase.Message)
		if err != nil {
			return nil, fmt.Errorf("failed to create rebased commit: %w", err)
		}

		h.logger.Debug("Successfully rebased commit",
			zap.String("original", commitHash),
			zap.String("rebased", newCommitHash))

		currentBaseHash = newCommitHash
	}

	// Update target reference to point to the final rebased commit
	refName := resolveRefName(req.TargetRef)
	if err := gitUpdateRef(ctx, repoPath, refName, currentBaseHash); err != nil {
		return nil, fmt.Errorf("failed to update target reference: %w", err)
	}

	h.logger.Info("Rebase completed successfully",
		zap.String("final_commit", currentBaseHash),
		zap.Int("rebased_commits", len(commitHashes)))

	// Read back the final commit
	finalCommit, err := gitCatFileCommit(ctx, repoPath, currentBaseHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read final rebased commit: %w", err)
	}

	return finalCommit, nil
}
