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

// performSquashMerge performs a squash merge by combining all source commits into a single commit
// Squash merge creates a single commit that contains all changes from the source branch,
// applied on top of the target branch. Unlike regular merge, it doesn't create a merge commit.
func (h *MergeOperationsHandler) performSquashMerge(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *MergeAnalysisResult) (*object.Commit, error) {
	// Get source and target commits
	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get source commit: %w", err)
	}

	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	h.logger.Info("Performing squash merge",
		zap.String("source", sourceCommit.Hash.String()),
		zap.String("target", targetCommit.Hash.String()))

	// For squash merge, we need to merge source's tree onto target's tree
	// This is similar to a three-way merge, but we create a single-parent commit instead of a merge commit
	if analysis.HasConflicts && !req.ForceConflict {
		// Even for squash merge, we should check conflicts
		return nil, fmt.Errorf("squash merge has %d conflicts. Use force_conflict=true to override", analysis.ConflictCount)
	}

	// Use centralized strategy resolution logic
	squashTreeHash, err := h.resolveMergeTree(ctx, repo, sourceCommit, targetCommit, analysis, req.Strategy, req.ForceConflict, true)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve merge tree for squash: %w", err)
	}

	// Generate squash commit message
	message := h.generateSquashCommitMessage(req, sourceCommit, targetCommit)

	// Set up author and committer using centralized signature creation
	// For squash, default to source commit author if not specified
	now := time.Now()
	author := CreateAuthorSignature(req, &sourceCommit.Author, now)
	committer := CreateCommitterSignature(req, &author, now)

	// Create squash commit with single parent (target only)
	// Unlike merge commit, squash commit has only one parent (the target)
	commit := &object.Commit{
		Message:      message,
		TreeHash:     squashTreeHash,
		ParentHashes: []plumbing.Hash{targetHash}, // Single parent - target branch
		Author:       author,
		Committer:    committer,
	}

	h.logger.Info("Creating squash commit",
		zap.String("squash_tree_hash", squashTreeHash.String()),
		zap.String("target_hash", targetHash.String()))

	// Store the commit
	obj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return nil, fmt.Errorf("failed to encode squash commit: %w", err)
	}

	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to store squash commit: %w", err)
	}

	commit.Hash = hash

	h.logger.Info("Squash commit created",
		zap.String("commit_hash", hash.String()),
		zap.String("commit_tree_hash", commit.TreeHash.String()))

	// Update the target reference to point to the new squash commit
	var refName plumbing.ReferenceName
	if strings.HasPrefix(req.TargetRef, "refs/") {
		refName = plumbing.ReferenceName(req.TargetRef)
	} else {
		refName = plumbing.ReferenceName("refs/heads/" + req.TargetRef)
	}

	ref := plumbing.NewHashReference(refName, hash)
	if err := repo.Storer.SetReference(ref); err != nil {
		return nil, fmt.Errorf("failed to update target reference: %w", err)
	}

	h.logger.Info("Squash merge completed successfully",
		zap.String("commit_hash", hash.String()),
		zap.String("target_ref", req.TargetRef))

	return commit, nil
}

// generateSquashCommitMessage generates an appropriate commit message for squash merge
func (h *MergeOperationsHandler) generateSquashCommitMessage(req *MergeRequest, source, target *object.Commit) string {
	if req.Message != "" {
		return req.Message
	}

	// Generate default squash merge message
	sourceRef := req.SourceRef
	targetRef := req.TargetRef

	// Simplify ref names if they're full refs
	sourceRef = strings.TrimPrefix(sourceRef, "refs/heads/")
	targetRef = strings.TrimPrefix(targetRef, "refs/heads/")

	return fmt.Sprintf("Squash merge branch '%s' into '%s'", sourceRef, targetRef)
}
