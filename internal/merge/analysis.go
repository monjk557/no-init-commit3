package merge

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// analyzeMerge performs merge conflict analysis
func (h *MergeOperationsHandler) analyzeMerge(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest) (*MergeAnalysisResult, error) {
	// Create analysis request with already opened repository
	analysisReq := &MergeAnalysisRequest{
		Repository:     repo, // Use already opened repository (중복 오픈 방지)
		Owner:          "",   // Not needed when Repository is provided
		Repo:           "",   // Not needed when Repository is provided
		SourceRef:      sourceHash.String(),
		TargetRef:      targetHash.String(),
		Strategy:       MergeStrategy(req.Strategy),
		AutoResolve:    req.ForceConflict, // Only auto-resolve if forcing
		IncludeContent: true,
		MaxFileSize:    1024 * 1024, // 1MB max for conflict analysis
	}

	conflictDetector := NewConflictDetector(h.pathConfig, h.logger)
	result, err := conflictDetector.AnalyzeMerge(ctx, analysisReq)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze merge: %w", err)
	}

	return result, nil
}

// analyzeRebaseDryRun performs rebase-specific dry run analysis
// Rebase는 각 커밋을 순차적으로 rebase하면서 각 단계에서 conflict를 확인해야 함
func (h *MergeOperationsHandler) analyzeRebaseDryRun(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest) (*MergeAnalysisResult, error) {
	// Get source and target commits
	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get source commit: %w", err)
	}

	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	// Find merge base (common ancestor)
	mergeBases, err := sourceCommit.MergeBase(targetCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}
	if len(mergeBases) == 0 {
		return nil, fmt.Errorf("no common ancestor found for rebase")
	}

	mergeBase := mergeBases[0]

	// If source and target are the same or source is already on target, nothing to rebase
	if mergeBase.Hash == sourceCommit.Hash {
		return &MergeAnalysisResult{
			CanMerge:          true,
			HasConflicts:      false,
			IsFastForward:     false,
			ConflictCount:     0,
			FileConflicts:     []FileConflict{},
			SourceCommit:      nil, // Will be set if needed
			TargetCommit:      nil,
			Strategy:          MergeStrategy(req.Strategy),
			AutoResolvable:    0,
			SeverityBreakdown: make(map[ConflictSeverity]int),
			EstimatedTime:     0,
			Recommendations:   []string{"Source is already based on target, nothing to rebase"},
		}, nil
	}

	// Collect commits from merge base to source (exclusive of merge base, inclusive of source)
	commitsToRebase, err := h.getCommitsBetween(repo, mergeBase, sourceCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to get commits to rebase: %w", err)
	}

	if len(commitsToRebase) == 0 {
		return &MergeAnalysisResult{
			CanMerge:          true,
			HasConflicts:      false,
			IsFastForward:     false,
			ConflictCount:     0,
			FileConflicts:     []FileConflict{},
			Strategy:          MergeStrategy(req.Strategy),
			AutoResolvable:    0,
			SeverityBreakdown: make(map[ConflictSeverity]int),
			EstimatedTime:     0,
			Recommendations:   []string{"No commits to rebase"},
		}, nil
	}

	// Analyze each commit sequentially (simulating rebase)
	allConflicts := make(map[string]FileConflict) // Use map to deduplicate conflicts
	currentBase := targetCommit
	totalConflictCount := 0
	var firstConflictCommit *object.Commit
	firstConflictCommitSHA := ""

	h.logger.Info("Analyzing rebase dry run",
		zap.Int("commits_to_rebase", len(commitsToRebase)))

	for i, commitToRebase := range commitsToRebase {
		// Get the commit's parent (the base from which this commit was made)
		var parentCommit *object.Commit
		if len(commitToRebase.ParentHashes) > 0 {
			parentCommit, err = repo.CommitObject(commitToRebase.ParentHashes[0])
			if err != nil {
				h.logger.Warn("Failed to get parent commit for rebase analysis",
					zap.String("commit", commitToRebase.Hash.String()),
					zap.Error(err))
				continue
			}
		}

		// For non-root commits, analyze conflict with current base
		if parentCommit != nil {
			// Analyze this commit's rebase onto current base
			singleAnalysis, err := h.analyzeMerge(ctx, repo, commitToRebase.Hash, currentBase.Hash, req)
			if err != nil {
				h.logger.Warn("Failed to analyze rebase commit",
					zap.String("commit", commitToRebase.Hash.String()),
					zap.Error(err))
				continue
			}

			// Collect conflicts from this step
			if singleAnalysis.HasConflicts {
				if firstConflictCommit == nil {
					firstConflictCommit = commitToRebase
					firstConflictCommitSHA = commitToRebase.Hash.String()
				}
				totalConflictCount += singleAnalysis.ConflictCount

				// Merge conflicts into allConflicts map (deduplicate by path)
				for _, conflict := range singleAnalysis.FileConflicts {
					allConflicts[conflict.FilePath] = conflict
				}
			}

			// Update current base for next iteration (simulating successful rebase)
			// In dry run, we don't actually create commits, so we simulate by using target
			// This is approximate - actual rebase would have new commits
			// For analysis purposes, we continue with the assumption that rebase succeeds
		}

		h.logger.Debug("Analyzed rebase commit",
			zap.Int("index", i+1),
			zap.Int("total", len(commitsToRebase)),
			zap.String("commit", commitToRebase.Hash.String()),
			zap.Bool("has_conflicts", parentCommit != nil && totalConflictCount > 0))
	}

	// Convert conflicts map to slice
	fileConflicts := make([]FileConflict, 0, len(allConflicts))
	for _, conflict := range allConflicts {
		fileConflicts = append(fileConflicts, conflict)
	}

	// Calculate auto-resolvable count
	autoResolvable := 0
	for _, conflict := range fileConflicts {
		if conflict.AutoResolvable {
			autoResolvable++
		}
	}

	// Calculate severity breakdown
	severityBreakdown := make(map[ConflictSeverity]int)
	for _, conflict := range fileConflicts {
		severityBreakdown[conflict.Severity]++
	}

	// Generate recommendations
	recommendations := []string{}
	if totalConflictCount > 0 {
		recommendations = append(recommendations, fmt.Sprintf("Rebase will encounter %d conflicts across %d commits", totalConflictCount, len(commitsToRebase)))
		if firstConflictCommitSHA != "" {
			recommendations = append(recommendations, fmt.Sprintf("First conflict occurs at commit %s: %s", firstConflictCommitSHA[:8], firstConflictCommit.Message))
		}
		if autoResolvable > 0 {
			recommendations = append(recommendations, fmt.Sprintf("%d conflicts may be auto-resolvable", autoResolvable))
		}
	} else {
		recommendations = append(recommendations, "Rebase will complete without conflicts")
	}

	// Add rebase-specific metadata
	metadata := map[string]interface{}{
		"commits_to_rebase":     len(commitsToRebase),
		"first_conflict_commit": firstConflictCommitSHA,
		"rebase_method":         "sequential",
	}

	return &MergeAnalysisResult{
		CanMerge:          totalConflictCount == 0 || (req.ForceConflict && autoResolvable == totalConflictCount),
		HasConflicts:      totalConflictCount > 0,
		IsFastForward:     false,              // Rebase is never fast-forward
		ConflictCount:     len(fileConflicts), // Unique file conflicts
		FileConflicts:     fileConflicts,
		MergeBase:         nil, // Not used for rebase
		SourceCommit:      nil, // Can be set if needed
		TargetCommit:      nil,
		Strategy:          MergeStrategy(req.Strategy),
		AutoResolvable:    autoResolvable,
		SeverityBreakdown: severityBreakdown,
		EstimatedTime:     time.Duration(totalConflictCount) * time.Minute, // Rough estimate
		Recommendations:   recommendations,
		Metadata:          metadata,
	}, nil
}

// getCommitsBetween collects commits from 'from' (exclusive) to 'to' (inclusive) in chronological order
// Returns commits in order from oldest to newest
func (h *MergeOperationsHandler) getCommitsBetween(repo *gogit.Repository, from, to *object.Commit) ([]*object.Commit, error) {
	if from.Hash == to.Hash {
		return []*object.Commit{}, nil
	}

	// Get commit log from 'to' commit
	commitIter, err := repo.Log(&gogit.LogOptions{
		From: to.Hash,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer commitIter.Close()

	var commits []*object.Commit
	fromHashStr := from.Hash.String()

	err = commitIter.ForEach(func(commit *object.Commit) error {
		// Stop when we reach the 'from' commit
		if commit.Hash.String() == fromHashStr {
			return fmt.Errorf("stop iteration")
		}

		commits = append(commits, commit)
		return nil
	})

	// Check if we stopped at the 'from' commit (normal case)
	if err != nil && err.Error() != "stop iteration" {
		return nil, fmt.Errorf("failed to iterate commits: %w", err)
	}

	// Reverse the list to get chronological order (oldest first)
	// Since we collected from newest to oldest, we need to reverse
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}

	return commits, nil
}
