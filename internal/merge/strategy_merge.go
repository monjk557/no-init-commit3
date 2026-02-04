package merge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// performThreeWayMerge performs a three-way merge and creates a merge commit
func (h *MergeOperationsHandler) performThreeWayMerge(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *MergeAnalysisResult) (*object.Commit, error) {
	// Get source and target commits
	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get source commit: %w", err)
	}

	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	// Use centralized strategy resolution logic
	mergeTreeHash, err := h.resolveMergeTree(ctx, repo, sourceCommit, targetCommit, analysis, req.Strategy, req.ForceConflict, false)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve merge tree: %w", err)
	}

	if analysis.HasConflicts && !req.ForceConflict {
		// This should not happen as we check for conflicts earlier
		return nil, fmt.Errorf("conflicts detected but force_conflict is false")
	}

	// Generate merge commit message
	message := generateMergeCommitMessage(req, sourceCommit, targetCommit)

	// Set up author and committer using centralized signature creation
	now := time.Now()
	author := CreateAuthorSignature(req, nil, now)
	committer := CreateCommitterSignature(req, &author, now)

	// Create merge commit
	commit := &object.Commit{
		Message:      message,
		TreeHash:     mergeTreeHash, // Use strategy-determined tree
		ParentHashes: []plumbing.Hash{targetHash, sourceHash},
		Author:       author,
		Committer:    committer,
	}

	// Store the commit
	obj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return nil, fmt.Errorf("failed to encode merge commit: %w", err)
	}

	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to store merge commit: %w", err)
	}

	commit.Hash = hash

	// Update the target reference to point to the new merge commit
	if strings.HasPrefix(req.TargetRef, "refs/") {
		refName := plumbing.ReferenceName(req.TargetRef)
		ref := plumbing.NewHashReference(refName, hash)
		if err := repo.Storer.SetReference(ref); err != nil {
			return nil, fmt.Errorf("failed to update target reference: %w", err)
		}
	} else {
		refName := plumbing.ReferenceName("refs/heads/" + req.TargetRef)
		ref := plumbing.NewHashReference(refName, hash)
		if err := repo.Storer.SetReference(ref); err != nil {
			return nil, fmt.Errorf("failed to update target reference: %w", err)
		}
	}

	return commit, nil
}

// performFastForwardMerge performs a fast-forward merge by updating the target reference
func (h *MergeOperationsHandler) performFastForwardMerge(repo *gogit.Repository, targetHash, sourceHash plumbing.Hash, targetRef string) error {
	// Determine the reference name to update
	var refName plumbing.ReferenceName
	if strings.HasPrefix(targetRef, "refs/") {
		refName = plumbing.ReferenceName(targetRef)
	} else {
		refName = plumbing.ReferenceName("refs/heads/" + targetRef)
	}

	// Update the reference to point to the source commit
	ref := plumbing.NewHashReference(refName, sourceHash)
	return repo.Storer.SetReference(ref)
}

// generateMergeCommitMessage generates an appropriate merge commit message
func generateMergeCommitMessage(req *MergeRequest, source, target *object.Commit) string {
	if req.Message != "" {
		return req.Message
	}

	// Generate default merge message
	sourceRef := req.SourceRef
	targetRef := req.TargetRef

	// Simplify ref names if they're full refs
	sourceRef = strings.TrimPrefix(sourceRef, "refs/heads/")
	targetRef = strings.TrimPrefix(targetRef, "refs/heads/")

	return fmt.Sprintf("Merge branch '%s' into '%s'", sourceRef, targetRef)
}

// resolveMergeTree determines which tree to use based on merge strategy and conflict status
// This function centralizes the strategy resolution logic used by both regular and squash merges
func (h *MergeOperationsHandler) resolveMergeTree(
	ctx context.Context,
	repo *gogit.Repository,
	sourceCommit, targetCommit *object.Commit,
	analysis *MergeAnalysisResult,
	strategy MergeStrategy,
	forceConflict bool,
	isSquash bool,
) (plumbing.Hash, error) {
	// If there are conflicts and we're not forcing, we should not call this function
	// But handle it gracefully anyway
	if analysis.HasConflicts && !forceConflict {
		if isSquash {
			// For squash, prefer source changes when not forcing
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
			// Keep our version (target)
			if isSquash {
				h.logger.Warn("'ours' strategy in squash merge - keeping target tree (may lose source changes)")
			} else {
				h.logger.Debug("Using 'ours' strategy - keeping target tree")
			}
			return targetCommit.TreeHash, nil

		case MergeStrategyTheirs:
			// Keep their version (source)
			if isSquash {
				h.logger.Debug("Using 'theirs' strategy for squash - using source tree")
			} else {
				h.logger.Debug("Using 'theirs' strategy - using source tree")
			}
			return sourceCommit.TreeHash, nil

		case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyORT:
			// For recursive/resolve/ORT strategy, perform actual 3-way merge
			if analysis.AutoResolvable > 0 && analysis.AutoResolvable == analysis.ConflictCount {
				// All conflicts are auto-resolvable, create merged tree
				h.logger.Debug("Performing 3-way merge - all conflicts auto-resolvable",
					zap.String("strategy", string(strategy)))

				mergedTree, err := h.createMergedTree(ctx, repo, sourceCommit, targetCommit, analysis, strategy)
				if err != nil {
					fallbackTree := sourceCommit.TreeHash
					if !isSquash {
						fallbackTree = targetCommit.TreeHash
					}
					h.logger.Error("Failed to create merged tree, falling back",
						zap.Error(err),
						zap.Bool("is_squash", isSquash))
					return fallbackTree, nil
				}
				h.logger.Info("Successfully created merged tree",
					zap.String("merged_tree", mergedTree.String()),
					zap.String("strategy", string(strategy)))
				return mergedTree, nil
			} else if analysis.AutoResolvable > 0 {
				// Mixed auto-resolvable and manual conflicts
				h.logger.Warn("Not all conflicts auto-resolvable - attempting partial merge",
					zap.Int("auto_resolvable", analysis.AutoResolvable),
					zap.Int("total_conflicts", analysis.ConflictCount),
					zap.String("strategy", string(strategy)))

				// Attempt to create merged tree with auto-resolvable conflicts
				mergedTree, err := h.createMergedTree(ctx, repo, sourceCommit, targetCommit, analysis, strategy)
				if err != nil {
					fallbackTree := sourceCommit.TreeHash
					if !isSquash {
						fallbackTree = targetCommit.TreeHash
					}
					h.logger.Error("Failed to create partially merged tree, falling back",
						zap.Error(err),
						zap.Bool("is_squash", isSquash))
					return fallbackTree, nil
				}
				h.logger.Info("Successfully created partially merged tree",
					zap.String("merged_tree", mergedTree.String()),
					zap.String("strategy", string(strategy)))
				return mergedTree, nil
			} else {
				// No auto-resolvable conflicts
				fallbackTree := sourceCommit.TreeHash
				if !isSquash {
					fallbackTree = targetCommit.TreeHash
				}
				h.logger.Warn("No auto-resolvable conflicts - using fallback tree",
					zap.Bool("is_squash", isSquash),
					zap.String("strategy", string(strategy)))
				return fallbackTree, nil
			}

		case MergeStrategySubtree:
			// Subtree merge uses source tree
			h.logger.Debug("Using 'subtree' strategy - using source tree")
			return sourceCommit.TreeHash, nil

		default:
			// Fallback based on merge method
			fallbackTree := sourceCommit.TreeHash
			if !isSquash {
				fallbackTree = targetCommit.TreeHash
			}
			h.logger.Warn("Unknown strategy - defaulting to fallback tree",
				zap.String("strategy", string(strategy)),
				zap.Bool("is_squash", isSquash))
			return fallbackTree, nil
		}
	}

	// No conflicts - perform proper 3-way merge even when clean
	// This ensures we get the correct merged tree structure
	h.logger.Debug("No conflicts - performing 3-way merge",
		zap.String("strategy", string(strategy)),
		zap.Bool("is_squash", isSquash))

	mergedTree, err := h.createMergedTree(ctx, repo, sourceCommit, targetCommit, analysis, strategy)
	if err != nil {
		// Fallback based on merge method
		fallbackTree := sourceCommit.TreeHash
		if !isSquash {
			fallbackTree = targetCommit.TreeHash
		}
		h.logger.Warn("Failed to create merged tree for clean merge, using fallback",
			zap.Error(err),
			zap.Bool("is_squash", isSquash))
		return fallbackTree, nil
	}

	h.logger.Debug("Successfully created merged tree for clean merge",
		zap.String("merged_tree", mergedTree.String()),
		zap.String("strategy", string(strategy)))
	return mergedTree, nil
}
