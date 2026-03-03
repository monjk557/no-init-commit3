package merge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// performThreeWayMerge performs a three-way merge and creates a merge commit
// using native git commands.
func (h *MergeOperationsHandler) performThreeWayMerge(ctx context.Context, repoPath string, sourceHash, targetHash string, req *MergeRequest, analysis *MergeAnalysisResult) (*NativeCommit, error) {
	// Read source and target commits
	sourceCommit, err := gitCatFileCommit(ctx, repoPath, sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get source commit: %w", err)
	}
	targetCommit, err := gitCatFileCommit(ctx, repoPath, targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	// Use centralized strategy resolution logic
	mergeTreeHash, err := h.resolveMergeTree(ctx, repoPath, sourceCommit, targetCommit, analysis, req.Strategy, req.ForceConflict, false)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve merge tree: %w", err)
	}

	if analysis.HasConflicts && !req.ForceConflict {
		return nil, fmt.Errorf("conflicts detected but force_conflict is false")
	}

	// Generate merge commit message
	message := generateMergeCommitMessage(req)

	// Set up author and committer
	now := time.Now()
	author := CreateAuthorSignature(req, nil, now)
	committer := CreateCommitterSignature(req, &author, now)

	// Create merge commit via git commit-tree (two parents: target, source)
	commitHash, err := gitCommitTree(ctx, repoPath, mergeTreeHash,
		[]string{targetHash, sourceHash}, author, committer, message)
	if err != nil {
		return nil, fmt.Errorf("failed to create merge commit: %w", err)
	}

	// Update the target reference
	refName := resolveRefName(req.TargetRef)
	if err := gitUpdateRef(ctx, repoPath, refName, commitHash); err != nil {
		return nil, fmt.Errorf("failed to update target reference: %w", err)
	}

	// Read back the created commit
	commit, err := gitCatFileCommit(ctx, repoPath, commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read created commit: %w", err)
	}

	return commit, nil
}

// generateMergeCommitMessage generates an appropriate merge commit message
func generateMergeCommitMessage(req *MergeRequest) string {
	if req.Message != "" {
		return req.Message
	}
	sourceRef := strings.TrimPrefix(req.SourceRef, "refs/heads/")
	targetRef := strings.TrimPrefix(req.TargetRef, "refs/heads/")
	return fmt.Sprintf("Merge branch '%s' into '%s'", sourceRef, targetRef)
}

// resolveMergeTree determines which tree to use based on merge strategy and conflict status.
// This function centralizes the strategy resolution logic used by both regular and squash merges.
func (h *MergeOperationsHandler) resolveMergeTree(
	ctx context.Context,
	repoPath string,
	sourceCommit, targetCommit *NativeCommit,
	analysis *MergeAnalysisResult,
	strategy MergeStrategy,
	forceConflict bool,
	isSquash bool,
) (string, error) {
	// If there are conflicts and we're not forcing, return appropriate tree
	if analysis.HasConflicts && !forceConflict {
		if isSquash {
			return sourceCommit.TreeHash, nil
		}
		return targetCommit.TreeHash, nil
	}

	// Handle conflicts with force
	if analysis.HasConflicts && forceConflict {
		h.logger.Info("Resolving conflicts using strategy",
			zap.String("strategy", string(strategy)),
			zap.Int("conflict_count", analysis.ConflictCount),
			zap.Bool("is_squash", isSquash))

		switch strategy {
		case MergeStrategyOurs:
			if isSquash {
				h.logger.Warn("'ours' strategy in squash merge - keeping target tree")
			}
			return targetCommit.TreeHash, nil

		case MergeStrategyTheirs:
			return sourceCommit.TreeHash, nil

		case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyORT:
			// Perform actual 3-way merge via git merge-tree
			mergedTree, err := h.createMergedTree(ctx, repoPath, sourceCommit, targetCommit, analysis, strategy)
			if err != nil {
				fallbackTree := sourceCommit.TreeHash
				if !isSquash {
					fallbackTree = targetCommit.TreeHash
				}
				h.logger.Error("Failed to create merged tree, falling back",
					zap.Error(err),
					zap.Bool("is_squash", isSquash),
					zap.String("fallback_tree", fallbackTree))
				return fallbackTree, nil
			}
			h.logger.Info("Successfully created merged tree",
				zap.String("merged_tree", mergedTree),
				zap.String("strategy", string(strategy)))
			return mergedTree, nil

		case MergeStrategySubtree:
			return sourceCommit.TreeHash, nil

		default:
			fallbackTree := sourceCommit.TreeHash
			if !isSquash {
				fallbackTree = targetCommit.TreeHash
			}
			h.logger.Warn("Unknown strategy - using fallback tree",
				zap.String("strategy", string(strategy)))
			return fallbackTree, nil
		}
	}

	// No conflicts - perform proper 3-way merge
	h.logger.Debug("No conflicts - performing 3-way merge",
		zap.String("strategy", string(strategy)),
		zap.Bool("is_squash", isSquash))

	mergedTree, err := h.createMergedTree(ctx, repoPath, sourceCommit, targetCommit, analysis, strategy)
	if err != nil {
		fallbackTree := sourceCommit.TreeHash
		if !isSquash {
			fallbackTree = targetCommit.TreeHash
		}
		h.logger.Warn("Failed to create merged tree for clean merge, using fallback",
			zap.Error(err), zap.Bool("is_squash", isSquash))
		return fallbackTree, nil
	}

	h.logger.Debug("Successfully created merged tree for clean merge",
		zap.String("merged_tree", mergedTree))
	return mergedTree, nil
}

// resolveRefName normalizes a ref to its full form (refs/heads/...).
func resolveRefName(ref string) string {
	if strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "refs/heads/" + ref
}
