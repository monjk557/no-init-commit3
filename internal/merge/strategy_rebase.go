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

// performRebase performs a rebase by reapplying source branch commits onto target branch
// Rebase takes commits from source branch (after merge base) and applies them one by one onto target
func (h *MergeOperationsHandler) performRebase(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *MergeAnalysisResult) (*object.Commit, error) {
	// Get source and target commits
	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get source commit: %w", err)
	}

	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	h.logger.Info("Performing rebase",
		zap.String("source", sourceCommit.Hash.String()),
		zap.String("target", targetCommit.Hash.String()))

	// Find merge base (common ancestor)
	mergeBases, err := sourceCommit.MergeBase(targetCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}
	if len(mergeBases) == 0 {
		return nil, fmt.Errorf("no common ancestor found for rebase")
	}

	mergeBase := mergeBases[0]
	h.logger.Debug("Found merge base for rebase",
		zap.String("merge_base", mergeBase.Hash.String()))

	// If source and target are the same or source is already on target, nothing to rebase
	if mergeBase.Hash == sourceCommit.Hash {
		h.logger.Info("Source is already based on target, nothing to rebase")
		return targetCommit, nil
	}

	// Collect commits from merge base to source (exclusive of merge base, inclusive of source)
	commitsToRebase, err := h.getCommitsBetween(repo, mergeBase, sourceCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to get commits to rebase: %w", err)
	}

	if len(commitsToRebase) == 0 {
		h.logger.Info("No commits to rebase")
		return targetCommit, nil
	}

	h.logger.Info("Rebasing commits",
		zap.Int("commit_count", len(commitsToRebase)))

	// Start with target as the base
	currentBase := targetCommit

	// Apply each commit sequentially
	for i, commitToRebase := range commitsToRebase {
		h.logger.Debug("Rebasing commit",
			zap.Int("index", i+1),
			zap.Int("total", len(commitsToRebase)),
			zap.String("commit", commitToRebase.Hash.String()))

		// For rebase, we need to apply the commit's changes to the current base
		// The commit's changes are: (commitToRebase - commitToRebase.Parents[0])
		// We apply these changes to currentBase
		// This is different from a regular merge where we need a common ancestor

		// Get the commit's parent (the base from which this commit was made)
		var parentCommit *object.Commit
		if len(commitToRebase.ParentHashes) > 0 {
			parentCommit, err = repo.CommitObject(commitToRebase.ParentHashes[0])
			if err != nil {
				return nil, fmt.Errorf("failed to get parent commit for rebase: %w", err)
			}
		} else {
			// This is a root commit, just use its tree
			parentCommit = nil
		}

		// For rebase, we merge: currentBase (target) <- commitToRebase (source)
		// with parentCommit as the common ancestor
		// If it's a root commit, we just use the commit's tree directly
		var rebasedTreeHash plumbing.Hash
		var singleCommitAnalysis *MergeAnalysisResult

		if parentCommit == nil {
			// Root commit: use its tree directly merged with currentBase
			// This is a simple case - just apply the root commit's tree
			rebasedTreeHash = commitToRebase.TreeHash
		} else {
			// Perform 3-way merge: parentCommit (base) + commitToRebase (source) -> currentBase (target)
			// Create a temporary analysis for this single commit rebase
			var err error
			singleCommitAnalysis, err = h.analyzeMerge(ctx, repo, commitToRebase.Hash, currentBase.Hash, req)
			if err != nil {
				return nil, fmt.Errorf("failed to analyze rebase commit %s: %w", commitToRebase.Hash.String(), err)
			}

			// Check for conflicts if not forcing
			if singleCommitAnalysis.HasConflicts && !req.ForceConflict {
				return nil, fmt.Errorf("rebase conflict at commit %s: %d conflicts. Use force_conflict=true to override", commitToRebase.Hash.String(), singleCommitAnalysis.ConflictCount)
			}

			// Use createMergedTreeWithBase for rebase (using parentCommit as base)
			rebasedTreeHash, err = h.createMergedTreeWithBase(ctx, repo, commitToRebase, currentBase, parentCommit, singleCommitAnalysis, req.Strategy)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve tree for rebase commit %s: %w", commitToRebase.Hash.String(), err)
			}
		}

		// Create new commit with rebased tree
		// Preserve original commit author and message, but use new parent (currentBase)
		var author object.Signature
		var committer object.Signature

		// Allow override of author/committer if provided in request
		if req.Author != nil {
			authorDate := req.Author.Date
			// Date가 zero value인 경우 원본 커밋의 작성자 날짜 사용
			if authorDate.IsZero() {
				authorDate = commitToRebase.Author.When
			}
			author = object.Signature{
				Name:  req.Author.Name,
				Email: req.Author.Email,
				When:  authorDate,
			}
		} else {
			author = commitToRebase.Author
		}

		if req.Committer != nil {
			committerDate := req.Committer.Date
			// Date가 zero value인 경우 현재 시간 사용 (rebase는 커밋 시간 변경)
			if committerDate.IsZero() {
				committerDate = time.Now()
			}
			committer = object.Signature{
				Name:  req.Committer.Name,
				Email: req.Committer.Email,
				When:  committerDate,
			}
		} else {
			// Use current time for committer if not specified (rebase changes commit time)
			committer = object.Signature{
				Name:  author.Name,
				Email: author.Email,
				When:  time.Now(),
			}
		}

		// Create rebased commit
		rebasedCommit := &object.Commit{
			Message:      commitToRebase.Message,
			TreeHash:     rebasedTreeHash,
			ParentHashes: []plumbing.Hash{currentBase.Hash}, // Single parent - the current base
			Author:       author,
			Committer:    committer,
		}

		// Store the commit
		obj := repo.Storer.NewEncodedObject()
		if err := rebasedCommit.Encode(obj); err != nil {
			return nil, fmt.Errorf("failed to encode rebased commit: %w", err)
		}

		hash, err := repo.Storer.SetEncodedObject(obj)
		if err != nil {
			return nil, fmt.Errorf("failed to store rebased commit: %w", err)
		}

		// Reload the commit from the repository to ensure it's fully stored
		// This ensures we have a valid commit object with proper storage references for the next iteration
		currentBase, err = repo.CommitObject(hash)
		if err != nil {
			return nil, fmt.Errorf("failed to reload rebased commit: %w", err)
		}

		h.logger.Debug("Successfully rebased commit",
			zap.String("original", commitToRebase.Hash.String()),
			zap.String("rebased", hash.String()))
	}

	// Update target reference to point to the final rebased commit
	var refName plumbing.ReferenceName
	if strings.HasPrefix(req.TargetRef, "refs/") {
		refName = plumbing.ReferenceName(req.TargetRef)
	} else {
		refName = plumbing.ReferenceName("refs/heads/" + req.TargetRef)
	}

	ref := plumbing.NewHashReference(refName, currentBase.Hash)
	if err := repo.Storer.SetReference(ref); err != nil {
		return nil, fmt.Errorf("failed to update target reference: %w", err)
	}

	h.logger.Info("Rebase completed successfully",
		zap.String("final_commit", currentBase.Hash.String()),
		zap.Int("rebased_commits", len(commitsToRebase)))

	return currentBase, nil
}
