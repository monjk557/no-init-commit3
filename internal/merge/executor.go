package merge

import (
	"context"
	"fmt"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// performMerge performs the actual Git merge operation
func (h *MergeOperationsHandler) performMerge(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *MergeAnalysisResult) (*MergeResult, error) {
	h.logger.Info("🚨 DEBUG: performMerge called",
		zap.String("method", string(req.Method)),
		zap.String("strategy", string(req.Strategy)),
		zap.String("source", sourceHash.String()),
		zap.String("target", targetHash.String()))
	result := &MergeResult{
		IsFastForward:    analysis.IsFastForward,
		HasConflicts:     analysis.HasConflicts,
		ConflictAnalysis: analysis,
	}

	// Route to appropriate merge method
	switch req.Method {
	case MergeMethodSquash:
		h.logger.Info("🚨 DEBUG: Executing SQUASH merge with protection")
		// Perform squash merge
		mergeCommit, err := h.performSquashMerge(ctx, repo, sourceHash, targetHash, req, analysis)
		if err != nil {
			return nil, fmt.Errorf("squash merge failed: %w", err)
		}
		result.MergeCommit = mergeCommit
		return result, nil

	case MergeMethodRebase:
		h.logger.Info("🚨 DEBUG: Executing REBASE merge with protection")
		// Perform rebase
		rebaseCommit, err := h.performRebase(ctx, repo, sourceHash, targetHash, req, analysis)
		if err != nil {
			return nil, fmt.Errorf("rebase failed: %w", err)
		}
		result.MergeCommit = rebaseCommit
		return result, nil

	case MergeMethodMerge:
		fallthrough
	default:
		h.logger.Info("🚨 DEBUG: Executing REGULAR merge with protection")
		// For merge method, always create a merge commit (even for fast-forward)
		// This is equivalent to git merge --no-ff
		// Perform three-way merge (which will create a merge commit)
		mergeCommit, err := h.performThreeWayMerge(ctx, repo, sourceHash, targetHash, req, analysis)
		if err != nil {
			return nil, fmt.Errorf("three-way merge failed: %w", err)
		}

		result.MergeCommit = mergeCommit
		return result, nil
	}
}

// generateMergeMessage generates the response message for a completed merge
func generateMergeMessage(result *MergeResult, req *MergeRequest) string {
	if result.IsFastForward {
		return fmt.Sprintf("Fast-forward merge from '%s' to '%s' completed successfully", req.SourceRef, req.TargetRef)
	}

	if result.HasConflicts {
		return fmt.Sprintf("Merge from '%s' to '%s' completed with conflicts resolved", req.SourceRef, req.TargetRef)
	}

	return fmt.Sprintf("Merge from '%s' to '%s' completed successfully", req.SourceRef, req.TargetRef)
}

// getMergeType returns the type of merge performed
func getMergeType(result *MergeResult) string {
	if result.IsFastForward {
		return "fast-forward"
	}
	if result.HasConflicts {
		return "three-way-with-conflicts"
	}
	return "three-way"
}

// getMergeTypeForMethod returns the type of merge performed based on method and result
func getMergeTypeForMethod(method MergeMethod, result *MergeResult) string {
	switch method {
	case MergeMethodSquash:
		return "squash"
	case MergeMethodRebase:
		return "rebase"
	case MergeMethodMerge:
		fallthrough
	default:
		return getMergeType(result)
	}
}

// commitToInfo converts a commit object to a simple commit info map
// Note: common.CommitInfo was removed, using map[string]interface{} instead
func (h *MergeOperationsHandler) commitToInfo(commit *object.Commit) map[string]interface{} {
	return map[string]interface{}{
		"sha":     commit.Hash.String(),
		"message": commit.Message,
		"author": map[string]interface{}{
			"name":  commit.Author.Name,
			"email": commit.Author.Email,
			"date":  commit.Author.When,
		},
		"committer": map[string]interface{}{
			"name":  commit.Committer.Name,
			"email": commit.Committer.Email,
			"date":  commit.Committer.When,
		},
		"created_at": commit.Author.When,
	}
}
