package merge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// performSquashMerge performs a squash merge by combining all source commits into a single commit.
// Unlike regular merge, it creates a single-parent commit (only target as parent).
func (h *MergeOperationsHandler) performSquashMerge(ctx context.Context, repoPath string, sourceHash, targetHash string, req *MergeRequest, analysis *MergeAnalysisResult) (*NativeCommit, error) {
	sourceCommit, err := gitCatFileCommit(ctx, repoPath, sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get source commit: %w", err)
	}
	targetCommit, err := gitCatFileCommit(ctx, repoPath, targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	h.logger.Info("Performing squash merge",
		zap.String("source", sourceHash),
		zap.String("target", targetHash))

	if analysis.HasConflicts && !req.ForceConflict {
		return nil, fmt.Errorf("squash merge has %d conflicts. Use force_conflict=true to override", analysis.ConflictCount)
	}

	// Resolve merge tree using centralized logic
	squashTreeHash, err := h.resolveMergeTree(ctx, repoPath, sourceCommit, targetCommit, analysis, req.Strategy, req.ForceConflict, true)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve merge tree for squash: %w", err)
	}

	// Generate squash commit message
	message := generateSquashCommitMessage(req)

	// Set up author and committer (default to source commit author)
	now := time.Now()
	sourceAuthor := &NativeSignature{
		Name:  sourceCommit.Author.Name,
		Email: sourceCommit.Author.Email,
		When:  sourceCommit.Author.When,
	}
	author := CreateAuthorSignature(req, sourceAuthor, now)
	committer := CreateCommitterSignature(req, &author, now)

	h.logger.Info("Creating squash commit",
		zap.String("squash_tree_hash", squashTreeHash),
		zap.String("target_hash", targetHash))

	// Create squash commit (single parent — target branch only)
	commitHash, err := gitCommitTree(ctx, repoPath, squashTreeHash,
		[]string{targetHash}, author, committer, message)
	if err != nil {
		return nil, fmt.Errorf("failed to create squash commit: %w", err)
	}

	// Update the target reference
	refName := resolveRefName(req.TargetRef)
	if err := gitUpdateRef(ctx, repoPath, refName, commitHash); err != nil {
		return nil, fmt.Errorf("failed to update target reference: %w", err)
	}

	h.logger.Info("Squash merge completed successfully",
		zap.String("commit_hash", commitHash),
		zap.String("target_ref", req.TargetRef))

	// Read back the created commit
	commit, err := gitCatFileCommit(ctx, repoPath, commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read created commit: %w", err)
	}

	return commit, nil
}

// generateSquashCommitMessage generates an appropriate commit message for squash merge
func generateSquashCommitMessage(req *MergeRequest) string {
	if req.Message != "" {
		return req.Message
	}
	sourceRef := strings.TrimPrefix(req.SourceRef, "refs/heads/")
	targetRef := strings.TrimPrefix(req.TargetRef, "refs/heads/")
	return fmt.Sprintf("Squash merge branch '%s' into '%s'", sourceRef, targetRef)
}
