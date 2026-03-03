package merge

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// performMerge performs the actual Git merge operation using native git commands.
func (h *MergeOperationsHandler) performMerge(ctx context.Context, repoPath string, sourceHash, targetHash string, req *MergeRequest, analysis *MergeAnalysisResult) (*MergeResult, error) {
	h.logger.Info("performMerge called",
		zap.String("method", string(req.Method)),
		zap.String("strategy", string(req.Strategy)),
		zap.String("source", sourceHash),
		zap.String("target", targetHash))

	result := &MergeResult{
		IsFastForward:    analysis.IsFastForward,
		HasConflicts:     analysis.HasConflicts,
		ConflictAnalysis: analysis,
	}

	switch req.Method {
	case MergeMethodSquash:
		h.logger.Info("Executing SQUASH merge")
		mergeCommit, err := h.performSquashMerge(ctx, repoPath, sourceHash, targetHash, req, analysis)
		if err != nil {
			return nil, fmt.Errorf("squash merge failed: %w", err)
		}
		result.MergeCommit = mergeCommit
		return result, nil

	case MergeMethodRebase:
		h.logger.Info("Executing REBASE merge")
		rebaseCommit, err := h.performRebase(ctx, repoPath, sourceHash, targetHash, req, analysis)
		if err != nil {
			return nil, fmt.Errorf("rebase failed: %w", err)
		}
		result.MergeCommit = rebaseCommit
		return result, nil

	case MergeMethodMerge:
		fallthrough
	default:
		h.logger.Info("Executing REGULAR merge")
		mergeCommit, err := h.performThreeWayMerge(ctx, repoPath, sourceHash, targetHash, req, analysis)
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

// commitToInfo converts a NativeCommit to a simple commit info map
func (h *MergeOperationsHandler) commitToInfo(commit *NativeCommit) map[string]interface{} {
	return map[string]interface{}{
		"sha":     commit.Hash,
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
